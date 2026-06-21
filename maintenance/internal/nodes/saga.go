// -------------------------------------------------------------------------------
// Maintenance Saga Activities - Shared Job Scale / Wait / Find / Measure
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Generic, job-name-parameterized activities shared by the registry-GC and
// aptly-cleanup sagas: locate a job's node, scale a job, wait for its allocs
// to drain or come back, and measure a host directory. The scale/wait core
// lives in the shared Nomad client; these methods add the Temporal span,
// heartbeat, logging, and error classification. Both sagas register one
// SagaActivities instance with the worker and reference these by name.
// -------------------------------------------------------------------------------

package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// nomad is the saga's view of shared.Nomad -- the job-find, scale, and
// alloc-wait operations these activities call. *shared.Nomad satisfies it
// structurally.
type nomad interface {
	FindJobNode(ctx context.Context, jobName string) (shared.NomadNode, error)
	ScaleJob(ctx context.Context, jobName, groupName string, count int, reason string) error
	WaitAllocCount(ctx context.Context, jobName string, target int, interval time.Duration, onPoll func(running int)) error
}

// SagaActivities holds the shared dependencies for the generic saga steps.
// Register one instance with the Temporal worker to expose its exported methods
// (FindJobNode, MeasureDataDir, ScaleJob, WaitJobDrained, WaitJobRunning) as
// activity implementations used by every job-scaling saga.
type SagaActivities struct {
	nomad nomad
	disk  shared.DirMeasurer
}

// NewSagaActivities builds the shared saga activities over the given Nomad and
// SSH clients (reused across invocations rather than rebuilt per call).
func NewSagaActivities(n nomad, disk shared.DirMeasurer) *SagaActivities {
	return &SagaActivities{nomad: n, disk: disk}
}

// -------------------------------------------------------------------------
// FIND JOB NODE
// -------------------------------------------------------------------------

// FindJobNode queries the Nomad API for a running alloc of the named job and
// returns the NodeInfo for SSH dialing. Wraps a "no running alloc" condition
// as a non-retryable error so the workflow fails fast instead of retry-
// storming on a terminally-misconfigured cluster.
func (a *SagaActivities) FindJobNode(ctx context.Context, jobName string) (NodeInfo, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Finding node for job", "job", jobName)

	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.find_job_node")
	defer span.End()

	node, err := a.nomad.FindJobNode(ctx, jobName)
	if errors.Is(err, shared.ErrNoRunningAlloc) {
		// Fail fast: a terminally-misconfigured cluster shouldn't retry-storm.
		return NodeInfo{}, temporal.NewNonRetryableApplicationError(err.Error(), "NoRunningAlloc", err)
	}
	if err != nil {
		return NodeInfo{}, err
	}
	return NodeInfo{
		ID:       node.ID,
		Name:     node.Name,
		Address:  node.Address,
		HTTPAddr: node.HTTPAddr,
		IsOracle: strings.HasPrefix(node.Name, "oracle"),
	}, nil
}

// -------------------------------------------------------------------------
// MEASURE DATA DIR
// -------------------------------------------------------------------------

// MeasureDataDir returns the total size in bytes of a directory on the given
// node, walked over SFTP. Used for before/after reporting; the path is
// host-side (e.g. /mnt/gdrive) and the Nomad API doesn't expose disk usage.
func (a *SagaActivities) MeasureDataDir(ctx context.Context, node NodeInfo, dataDir string) (int64, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Measuring data dir", "node", node.Name, "path", dataDir)

	n, err := a.disk.DirSize(ctx, Target(node), dataDir)
	if err != nil {
		return 0, fmt.Errorf("measure %s on %s: %w", dataDir, node.Name, err)
	}
	return n, nil
}

// -------------------------------------------------------------------------
// SCALE JOB
// -------------------------------------------------------------------------

// ScaleJob scales the named Nomad job's task group to the target count.
// Idempotent — Nomad accepts the call when the job is already at the requested
// count. Used to scale down to 0 before a maintenance op and back to 1 in the
// deferred compensation. A "job not found" error is wrapped as non-retryable;
// transient API errors surface plain so Temporal retries per the activity's
// RetryPolicy.
func (a *SagaActivities) ScaleJob(ctx context.Context, jobName, groupName string, count int) error {
	logger := activity.GetLogger(ctx)
	logger.Info("Scaling Nomad job", "job", jobName, "group", groupName, "count", count)

	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.scale_job")
	defer span.End()

	reason := fmt.Sprintf("temporal workflow: scale to %d", count)
	if err := a.nomad.ScaleJob(ctx, jobName, groupName, count, reason); err != nil {
		if shared.IsJobNotFound(err) {
			return temporal.NewNonRetryableApplicationError(err.Error(), "JobNotFound", err)
		}
		return err
	}
	return nil
}

// -------------------------------------------------------------------------
// WAIT FOR ALLOCS DRAINED / RUNNING
// -------------------------------------------------------------------------

// WaitJobDrained polls the Nomad API until the named job has zero running
// allocations. Heartbeats every poll. Bounded by the activity's
// StartToCloseTimeout (set on the workflow side); returns ctx.Err() when
// exceeded.
func (a *SagaActivities) WaitJobDrained(ctx context.Context, jobName string) error {
	return a.waitAllocCount(ctx, jobName, 0, 3*time.Second, "drained")
}

// WaitJobRunning polls the Nomad API until the named job has at least one
// running allocation (i.e. the scale-up succeeded and a new alloc passed its
// start sequence). Bounded by the activity's StartToCloseTimeout.
func (a *SagaActivities) WaitJobRunning(ctx context.Context, jobName string) error {
	return a.waitAllocCount(ctx, jobName, 1, 3*time.Second, "running")
}

// waitAllocCount wraps the shared Nomad alloc-count wait with activity
// heartbeat and logging. Target 0 succeeds when running drops to 0; >=1
// succeeds when running is at least target.
func (a *SagaActivities) waitAllocCount(ctx context.Context, jobName string, target int, interval time.Duration, label string) error {
	logger := activity.GetLogger(ctx)
	return a.nomad.WaitAllocCount(ctx, jobName, target, interval, func(running int) {
		activity.RecordHeartbeat(ctx, running)
		logger.Info("Waiting", "job", jobName, "label", label, "running", running, "target", target)
	})
}
