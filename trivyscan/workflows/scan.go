// -------------------------------------------------------------------------------
// Trivy Scan Workflow - Container Image Vulnerability Scanning
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Discovers running container images across the Nomad cluster, scans them
// through the Trivy server with bounded concurrency, and persists each CVE
// result to PostgreSQL. Scan failures are recorded with error status and do
// not block the run. Pure orchestration -- all I/O happens in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/trivyscan/activities"
)

// --- Nil-typed activity stub for compile-time method references ---
var a *activities.Activities

var retryStandard = &temporal.RetryPolicy{
	InitialInterval:    time.Second,
	BackoffCoefficient: 2.0,
	MaximumInterval:    time.Minute,
	MaximumAttempts:    3,
}

// quickOpts covers fast operations: image discovery and result saves.
var quickOpts = workflow.ActivityOptions{
	StartToCloseTimeout:    5 * time.Minute,
	ScheduleToCloseTimeout: 15 * time.Minute,
	RetryPolicy:            retryStandard,
}

// scanOpts covers per-image Trivy scans, which run longer.
var scanOpts = workflow.ActivityOptions{
	StartToCloseTimeout:    30 * time.Minute,
	ScheduleToCloseTimeout: 60 * time.Minute,
	RetryPolicy:            retryStandard,
}

// Scan discovers running images and scans them with bounded concurrency,
// saving each result individually to stay under Temporal's payload limit.
func Scan(ctx workflow.Context, config activities.ScanConfig) error {
	logger := workflow.GetLogger(ctx)
	config.ApplyDefaults()
	logger.Info("Starting trivy scan workflow", "concurrency", config.Concurrency)

	quickCtx := workflow.WithActivityOptions(ctx, quickOpts)

	// --- Discover running images ---
	var images []string
	if err := workflow.ExecuteActivity(quickCtx, a.GetRunningImages).Get(quickCtx, &images); err != nil {
		return fmt.Errorf("get running images: %w", err)
	}
	logger.Info("Found images to scan", "count", len(images))

	// --- Bounded-concurrency scan + save per image ---
	results := make([]activities.ScanResult, len(images))

	sem := workflow.NewBufferedChannel(ctx, config.Concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, img := range images {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil) // acquire a slot
			defer sem.Receive(gctx, nil)

			results[i] = scanImage(gctx, img)
		})
	}
	wg.Wait(ctx)

	var totalCritical, totalHigh int
	for i := range results {
		totalCritical += results[i].CriticalCount
		totalHigh += results[i].HighCount
	}

	logger.Info("Trivy scan complete",
		"images", len(results),
		"critical", totalCritical,
		"high", totalHigh)

	if totalCritical > 0 || totalHigh > 0 {
		logger.Warn("Vulnerabilities found", "critical", totalCritical, "high", totalHigh)
	}

	return nil
}

// scanImage scans one image and saves the result. A scan failure is recorded
// as an error-status result rather than propagated; a save failure is logged.
// Both activities run with a stable per-image ActivityID for dedup/traceability.
func scanImage(ctx workflow.Context, image string) activities.ScanResult {
	logger := workflow.GetLogger(ctx)

	scanCtx := workflow.WithActivityOptions(ctx, withActivityID(scanOpts, "ScanImage:"+image))
	var result activities.ScanResult
	if err := workflow.ExecuteActivity(scanCtx, a.ScanImage, image).Get(scanCtx, &result); err != nil {
		logger.Warn("Scan activity error", "image", image, "error", err)
		result = activities.ScanResult{
			Image:     image,
			Status:    "error",
			Error:     err.Error(),
			ScannedAt: workflow.Now(ctx),
		}
	}

	saveCtx := workflow.WithActivityOptions(ctx, withActivityID(quickOpts, "SaveScan:"+image))
	if err := workflow.ExecuteActivity(saveCtx, a.SaveScanResult, result).Get(saveCtx, nil); err != nil {
		logger.Error("Failed to save scan result", "image", image, "error", err)
	}

	return result
}

// withActivityID returns a copy of opts with a stable ActivityID set.
func withActivityID(opts workflow.ActivityOptions, id string) workflow.ActivityOptions {
	opts.ActivityID = id
	return opts
}
