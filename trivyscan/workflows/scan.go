// -------------------------------------------------------------------------------
// Trivy Scan Workflow - Container Image Vulnerability Scanning
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Orchestrates vulnerability scanning of all running container images in the
// Nomad cluster. Discovers images, scans them in parallel batches via Trivy
// server, and persists CVE results to PostgreSQL. Pure orchestration logic
// with no side effects -- all I/O happens in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/trivyscan/activities"
)

const batchSize = 10

// --- Nil-typed activity stub for compile-time method references ---
var a *activities.Activities

// Scan orchestrates image vulnerability scanning across the cluster.
// Images are scanned in parallel batches; results are saved as a single
// batch to PostgreSQL. Transient scan failures (trivy server down) are
// retried by Temporal. Permanent failures (image not found) are recorded
// with error status and do not block the workflow.
func Scan(ctx workflow.Context) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting trivy scan workflow")

	// --- Activity options for quick operations (discovery, saving) ---
	defaultOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}

	// --- Activity options for scanning (longer timeout, no heartbeat) ---
	scanOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Minute,
		ScheduleToCloseTimeout: 60 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}

	defaultCtx := workflow.WithActivityOptions(ctx, defaultOpts)

	// --- Discover running images ---
	var images []string
	err := workflow.ExecuteActivity(defaultCtx, a.GetRunningImages).Get(ctx, &images)
	if err != nil {
		return fmt.Errorf("failed to get running images: %w", err)
	}
	logger.Info("Found images to scan", "count", len(images))

	// --- Scan in batches ---
	var totalCritical, totalHigh, totalScans int

	for i := 0; i < len(images); i += batchSize {
		end := min(i+batchSize, len(images))
		batch := images[i:end]
		logger.Info("Processing batch", "batch", i/batchSize+1, "images", len(batch))

		// --- Launch parallel scans for this batch ---
		type scanFuture struct {
			image  string
			future workflow.Future
		}
		futures := make([]scanFuture, len(batch))
		for j, img := range batch {
			scanCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				ActivityID:             fmt.Sprintf("ScanImage:%s", img),
				StartToCloseTimeout:    scanOpts.StartToCloseTimeout,
				ScheduleToCloseTimeout: scanOpts.ScheduleToCloseTimeout,
				RetryPolicy:            scanOpts.RetryPolicy,
			})
			futures[j] = scanFuture{
				image:  img,
				future: workflow.ExecuteActivity(scanCtx, a.ScanImage, img),
			}
		}

		// --- Collect results and save individually ---
		for _, sf := range futures {
			var result activities.ScanResult
			err := sf.future.Get(ctx, &result)
			if err != nil {
				logger.Warn("Scan activity error", "image", sf.image, "error", err)
				result = activities.ScanResult{
					Image:     sf.image,
					Status:    "error",
					Error:     err.Error(),
					ScannedAt: workflow.Now(ctx),
				}
			}

			// Save each result individually to stay under Temporal's 2MB payload limit
			saveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				ActivityID:             fmt.Sprintf("SaveScan:%s", sf.image),
				StartToCloseTimeout:    defaultOpts.StartToCloseTimeout,
				ScheduleToCloseTimeout: defaultOpts.ScheduleToCloseTimeout,
				RetryPolicy:            defaultOpts.RetryPolicy,
			})
			if saveErr := workflow.ExecuteActivity(saveCtx, a.SaveScanResult, result).Get(ctx, nil); saveErr != nil {
				logger.Error("Failed to save scan result", "image", sf.image, "error", saveErr)
			}

			totalCritical += result.CriticalCount
			totalHigh += result.HighCount
			totalScans++
		}
	}

	logger.Info("Trivy scan complete",
		"images", totalScans,
		"critical", totalCritical,
		"high", totalHigh)

	if totalCritical > 0 || totalHigh > 0 {
		logger.Warn("Vulnerabilities found", "critical", totalCritical, "high", totalHigh)
	}

	return nil
}
