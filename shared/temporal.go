// -------------------------------------------------------------------------------
// Shared Temporal Presets - Common Retry Policies and Activity Options
//
// Author: Alex Freidah
//
// The retry policy and activity-option shapes that nearly every workflow uses
// were duplicated in each domain's workflow package. Centralizing them here
// keeps the fleet-wide defaults in one place and lets a new workflow reach for
// a ready-made preset instead of re-declaring the boilerplate. Each function
// returns a fresh value so callers never share a mutable policy.
// -------------------------------------------------------------------------------

package shared

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// StandardRetry is the common exponential-backoff policy: 1s initial, 2x
// backoff, capped at 1m, 3 attempts. Suitable for most idempotent activities
// where transient failures (API blips, brief outages) deserve a few retries.
func StandardRetry() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    time.Minute,
		MaximumAttempts:    3,
	}
}

// NoRetry runs an activity exactly once. Use it for operations that must not be
// repeated after a partial failure (e.g. a partially-completed garbage-collect),
// where a compensation or the operator handles recovery instead.
func NoRetry() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{MaximumAttempts: 1}
}

// QuickActivityOptions covers fast operations (snapshots, listings, small
// writes, cleanup): 5m per attempt, 15m total including retries, StandardRetry.
func QuickActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 15 * time.Minute,
		RetryPolicy:            StandardRetry(),
	}
}

// LongActivityOptions covers long operations that must heartbeat (large dumps,
// multi-minute remote work): 30m per attempt, 60m total, 2m heartbeat,
// StandardRetry.
func LongActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Minute,
		ScheduleToCloseTimeout: 60 * time.Minute,
		HeartbeatTimeout:       2 * time.Minute,
		RetryPolicy:            StandardRetry(),
	}
}
