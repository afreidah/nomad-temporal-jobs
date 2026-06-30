// -------------------------------------------------------------------------------
// Registry GC Activities - RunRegistryGarbageCollect Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs RunRegistryGarbageCollect with a fake ContainerRunner: the dry-run /
// delete-untagged flags it builds, the blob-count parsing of the output, and
// error pass-through -- no real Docker.
// -------------------------------------------------------------------------------

package registrygc

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared/client/ssh"
)

type fakeRunner struct {
	out    string
	err    error
	gotCmd []string
}

func (f *fakeRunner) RunContainer(_ context.Context, _ ssh.SSHTarget, cfg *container.Config, _ []string, _ time.Duration) (string, error) {
	f.gotCmd = cfg.Cmd
	return f.out, f.err
}

func TestRunRegistryGarbageCollect(t *testing.T) {
	r := &fakeRunner{out: "blob eligible for deletion: aaa\nblob eligible for deletion: bbb\nother\n"}
	a := New(r)
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.RunRegistryGarbageCollect)

	cfg := RegistryGCConfig{RegistryImage: "registry:2", RegistryDataDir: "/data/reg", DryRun: true, DeleteUntagged: true}
	val, err := env.ExecuteActivity(a.RunRegistryGarbageCollect, nodes.NodeInfo{Name: "n1"}, cfg)
	if err != nil {
		t.Fatalf("RunRegistryGarbageCollect: %v", err)
	}
	var res RegistryGCRunResult
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.BlobsDeleted != 2 {
		t.Errorf("BlobsDeleted = %d, want 2", res.BlobsDeleted)
	}
	if !slices.Contains(r.gotCmd, "--dry-run") || !slices.Contains(r.gotCmd, "--delete-untagged") {
		t.Errorf("cmd = %v, want both --dry-run and --delete-untagged", r.gotCmd)
	}
}

func TestRunRegistryGarbageCollect_Error(t *testing.T) {
	a := New(&fakeRunner{err: errors.New("gc failed")})
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.RunRegistryGarbageCollect)
	if _, err := env.ExecuteActivity(a.RunRegistryGarbageCollect, nodes.NodeInfo{Name: "n1"}, RegistryGCConfig{}); err == nil {
		t.Fatal("expected an error when garbage-collect fails")
	}
}
