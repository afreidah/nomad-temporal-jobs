package nodes

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func measureWorkflow(ctx workflow.Context, node NodeInfo, dir string) (Size, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	return MeasureBestEffort(ctx, node, dir, "before"), nil
}

func runMeasure(t *testing.T, n int64, err error) Size {
	t.Helper()
	var s *SagaActivities
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterActivity(s.MeasureDataDir)
	env.OnActivity(s.MeasureDataDir, mock.Anything, mock.Anything, "/data").Return(n, err)
	env.ExecuteWorkflow(measureWorkflow, NodeInfo{Name: "n1"}, "/data")
	if werr := env.GetWorkflowError(); werr != nil {
		t.Fatalf("workflow: %v", werr)
	}
	var got Size
	if gerr := env.GetWorkflowResult(&got); gerr != nil {
		t.Fatal(gerr)
	}
	return got
}

func TestMeasureBestEffort(t *testing.T) {
	if got := runMeasure(t, 42, nil); got != (Size{Bytes: 42, OK: true}) {
		t.Errorf("measured = %+v, want 42/OK", got)
	}
	if got := runMeasure(t, 0, errors.New("ssh down")); got != (Size{}) {
		t.Errorf("failed measure = %+v, want unknown", got)
	}
}

func TestSizeReport(t *testing.T) {
	const gib = 1024 * 1024 * 1024
	cases := []struct {
		name                         string
		before, after                Size
		wantBefore, wantAfter, wantR string
	}{
		{"both measured", Size{10 * gib, true}, Size{7 * gib, true}, "10.0GiB", "7.0GiB", "3.0GiB"},
		{"grew", Size{1 * gib, true}, Size{2 * gib, true}, "1.0GiB", "2.0GiB", "0B"},
		{"before unknown", Size{}, Size{7 * gib, true}, "unknown", "7.0GiB", "unknown"},
		{"after unknown", Size{10 * gib, true}, Size{}, "10.0GiB", "unknown", "unknown"},
	}
	for _, c := range cases {
		b, a, r := SizeReport(c.before, c.after)
		if b != c.wantBefore || a != c.wantAfter || r != c.wantR {
			t.Errorf("%s: SizeReport = %q/%q/%q, want %q/%q/%q", c.name, b, a, r, c.wantBefore, c.wantAfter, c.wantR)
		}
	}
}
