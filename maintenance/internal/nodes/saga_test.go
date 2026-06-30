// -------------------------------------------------------------------------------
// Maintenance Saga Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs the generic job-scaling saga steps with a fake Nomad client, covering
// the node mapping, fail-fast non-retryable wrapping, and the drained/running
// wait loop (including the heartbeat callback) -- no real Nomad. The SSH-backed
// MeasureDataDir is left out (it needs an SSH server).
// -------------------------------------------------------------------------------

package nodes

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	nomadclient "munchbox/temporal-workers/shared/client/nomad"
	"munchbox/temporal-workers/shared/client/ssh"
)

type fakeSagaNomad struct {
	node     nomadclient.NomadNode
	findErr  error
	scaleErr error
	waitErr  error
	polls    []int // running counts reported to onPoll before WaitAllocCount returns
}

func (f *fakeSagaNomad) FindJobNode(_ context.Context, _ string) (nomadclient.NomadNode, error) {
	return f.node, f.findErr
}

func (f *fakeSagaNomad) ScaleJob(_ context.Context, _, _ string, _ int, _ string) error {
	return f.scaleErr
}

func (f *fakeSagaNomad) WaitAllocCount(_ context.Context, _ string, _ int, _ time.Duration, onPoll func(int)) error {
	for _, r := range f.polls {
		onPoll(r)
	}
	return f.waitErr
}

func sagaEnv() *testsuite.TestActivityEnvironment {
	return (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
}

type fakeDirMeasurer struct {
	size int64
	err  error
}

func (f *fakeDirMeasurer) DirSize(_ context.Context, _ ssh.SSHTarget, _ string) (int64, error) {
	return f.size, f.err
}

func TestSagaMeasureDataDir(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{}, &fakeDirMeasurer{size: 4096})
	env := sagaEnv()
	env.RegisterActivity(a.MeasureDataDir)

	val, err := env.ExecuteActivity(a.MeasureDataDir, NodeInfo{Name: "n1", Address: "10.0.0.1"}, "/mnt/data")
	if err != nil {
		t.Fatalf("MeasureDataDir: %v", err)
	}
	var n int64
	if err := val.Get(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n != 4096 {
		t.Errorf("size = %d, want 4096", n)
	}
}

func TestSagaMeasureDataDir_Error(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{}, &fakeDirMeasurer{err: errors.New("sftp down")})
	env := sagaEnv()
	env.RegisterActivity(a.MeasureDataDir)
	if _, err := env.ExecuteActivity(a.MeasureDataDir, NodeInfo{Name: "n1"}, "/mnt/data"); err == nil {
		t.Fatal("expected an error when DirSize fails")
	}
}

func TestSagaFindJobNode(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{
		node: nomadclient.NomadNode{ID: "n1", Name: "oracle-arm-1", Address: "10.0.0.9", HTTPAddr: "10.0.0.9:4646"},
	}, nil)
	env := sagaEnv()
	env.RegisterActivity(a.FindJobNode)

	val, err := env.ExecuteActivity(a.FindJobNode, "myjob")
	if err != nil {
		t.Fatalf("FindJobNode: %v", err)
	}
	var info NodeInfo
	if err := val.Get(&info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Address != "10.0.0.9" {
		t.Errorf("Address = %q, want 10.0.0.9", info.Address)
	}
	if !info.IsOracle {
		t.Error("oracle-arm-1 should be flagged oracle")
	}
}

func TestSagaFindJobNode_NoRunningAlloc(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{findErr: nomadclient.ErrNoRunningAlloc}, nil)
	env := sagaEnv()
	env.RegisterActivity(a.FindJobNode)
	if _, err := env.ExecuteActivity(a.FindJobNode, "myjob"); err == nil {
		t.Fatal("expected a (non-retryable) error when no alloc is running")
	}
}

func TestSagaScaleJob(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{}, nil)
	env := sagaEnv()
	env.RegisterActivity(a.ScaleJob)
	if _, err := env.ExecuteActivity(a.ScaleJob, "myjob", "grp", 0); err != nil {
		t.Fatalf("ScaleJob: %v", err)
	}
}

func TestSagaScaleJob_JobNotFound(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{scaleErr: errors.New(`error scaling job: "job not found"`)}, nil)
	env := sagaEnv()
	env.RegisterActivity(a.ScaleJob)
	if _, err := env.ExecuteActivity(a.ScaleJob, "myjob", "grp", 0); err == nil {
		t.Fatal("expected an error when the job is not found")
	}
}

func TestSagaWaitJobDrained(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{polls: []int{2, 1, 0}}, nil)
	env := sagaEnv()
	env.RegisterActivity(a.WaitJobDrained)
	if _, err := env.ExecuteActivity(a.WaitJobDrained, "myjob"); err != nil {
		t.Fatalf("WaitJobDrained: %v", err)
	}
}

func TestSagaWaitJobRunning_Error(t *testing.T) {
	a := NewSagaActivities(&fakeSagaNomad{polls: []int{0}, waitErr: errors.New("timed out")}, nil)
	env := sagaEnv()
	env.RegisterActivity(a.WaitJobRunning)
	if _, err := env.ExecuteActivity(a.WaitJobRunning, "myjob"); err == nil {
		t.Fatal("expected an error when the wait fails")
	}
}
