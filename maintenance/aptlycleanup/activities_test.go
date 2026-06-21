// -------------------------------------------------------------------------------
// Aptly Cleanup Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs RunAptlyDBCleanup with a fake ContainerRunner, asserting the image and
// data-dir bind it asks for and its output/error pass-through -- no real Docker.
// -------------------------------------------------------------------------------

package aptlycleanup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"
)

type fakeRunner struct {
	out      string
	err      error
	gotImage string
	gotBinds []string
}

func (f *fakeRunner) RunContainer(_ context.Context, _ shared.SSHTarget, cfg *container.Config, binds []string, _ time.Duration) (string, error) {
	f.gotImage = cfg.Image
	f.gotBinds = binds
	return f.out, f.err
}

func TestRunAptlyDBCleanup(t *testing.T) {
	r := &fakeRunner{out: "cleanup ok"}
	a := New(r)
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.RunAptlyDBCleanup)

	val, err := env.ExecuteActivity(a.RunAptlyDBCleanup, nodes.NodeInfo{Name: "n1", Address: "10.0.0.1"}, "aptly:latest", "/data/aptly")
	if err != nil {
		t.Fatalf("RunAptlyDBCleanup: %v", err)
	}
	var out string
	if err := val.Get(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != "cleanup ok" {
		t.Errorf("output = %q, want %q", out, "cleanup ok")
	}
	if r.gotImage != "aptly:latest" {
		t.Errorf("image = %q, want aptly:latest", r.gotImage)
	}
	if len(r.gotBinds) != 1 || r.gotBinds[0] != "/data/aptly:/opt/aptly" {
		t.Errorf("binds = %v, want [/data/aptly:/opt/aptly]", r.gotBinds)
	}
}

func TestRunAptlyDBCleanup_Error(t *testing.T) {
	a := New(&fakeRunner{err: errors.New("container failed")})
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.RunAptlyDBCleanup)
	if _, err := env.ExecuteActivity(a.RunAptlyDBCleanup, nodes.NodeInfo{Name: "n1"}, "img", "/d"); err == nil {
		t.Fatal("expected an error when the container fails")
	}
}
