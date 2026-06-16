// -------------------------------------------------------------------------------
// Shared Heartbeat - Run-While-Heartbeating Helper
//
// Author: Alex Freidah
//
// Long-running activities (database dumps, S3 uploads, remote commands) must
// emit periodic heartbeats so a stall trips the activity's HeartbeatTimeout
// instead of silently running to the StartToClose timeout. This generic
// captures that pattern once: run a function in a goroutine and heartbeat on
// an interval until it finishes or the context is cancelled.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
)

// WithHeartbeat runs fn in a goroutine while recording an activity heartbeat
// every interval, so a stalled operation trips the activity's HeartbeatTimeout.
// It returns as soon as fn finishes, or with ctx.Err() if the context is
// cancelled first. fn should honor ctx for cancellation to actually stop the
// work (exec.CommandContext kills its process on cancel; the AWS S3 uploader
// and most network clients abort their in-flight call). The heartbeat payload
// is an incrementing tick count.
func WithHeartbeat[T any](ctx context.Context, interval time.Duration, fn func() (T, error)) (T, error) {
	type outcome struct {
		val T
		err error
	}
	done := make(chan outcome, 1) // buffered so the goroutine never leaks on early return
	go func() {
		val, err := fn()
		done <- outcome{val, err}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ticks := 0
	for {
		select {
		case o := <-done:
			return o.val, o.err
		case <-ticker.C:
			ticks++
			activity.RecordHeartbeat(ctx, ticks)
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}
