package nodes

import (
	"go.temporal.io/sdk/workflow"
)

// sagaRef is a nil-typed stub for referencing the MeasureDataDir activity from
// workflow code.
var sagaRef *SagaActivities

// Size is a measured directory size; OK is false when it could not be measured.
type Size struct {
	Bytes int64
	OK    bool
}

// MeasureBestEffort runs the MeasureDataDir activity for dir on node. Sizes only
// feed a workflow's before/after report, so a failed or timed-out measurement
// is logged and returned as an unknown Size rather than failing the workflow.
func MeasureBestEffort(ctx workflow.Context, node NodeInfo, dir, when string) Size {
	var n int64
	if err := workflow.ExecuteActivity(ctx, sagaRef.MeasureDataDir, node, dir).Get(ctx, &n); err != nil {
		workflow.GetLogger(ctx).Warn("Could not measure data dir; reporting its size as unknown",
			"when", when, "node", node.Name, "path", dir, "error", err)
		return Size{}
	}
	return Size{Bytes: n, OK: true}
}

// SizeReport formats before, after, and reclaimed sizes for a workflow result.
// An unmeasured size reports as "unknown", and so does reclaimed unless both
// sides were measured.
func SizeReport(before, after Size) (beforeStr, afterStr, reclaimed string) {
	beforeStr, afterStr, reclaimed = "unknown", "unknown", "unknown"
	if before.OK {
		beforeStr = HumanBytes(before.Bytes)
	}
	if after.OK {
		afterStr = HumanBytes(after.Bytes)
	}
	if before.OK && after.OK {
		reclaimed = "0B"
		if d := before.Bytes - after.Bytes; d > 0 {
			reclaimed = HumanBytes(d)
		}
	}
	return beforeStr, afterStr, reclaimed
}
