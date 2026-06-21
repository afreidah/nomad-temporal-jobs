// -------------------------------------------------------------------------------
// Node Cleanup Activities - CleanupNodeViaSSH Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs CleanupNodeViaSSH end-to-end with a fake HostConnector / RemoteHost and a
// fake Nomad job set: orphan deletion, active-job protection, runtime-dir
// exclusion, and the Docker prune path -- all without a real host or daemon.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"
)

func runCleanup(t *testing.T, host shared.RemoteHost) CleanupResult {
	t.Helper()
	a := New(&fakeNomad{}, &fakeConnector{host: host})
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.CleanupNodeViaSSH)
	val, err := env.ExecuteActivity(a.CleanupNodeViaSSH, nodes.NodeInfo{Name: "n1"},
		CleanupConfig{DataDir: "/data", DockerPrune: true})
	if err != nil {
		t.Fatalf("CleanupNodeViaSSH: %v", err)
	}
	var r CleanupResult
	if err := val.Get(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

// Docker prune that emits per-step warnings still succeeds (warnings are noted).
func TestCleanupNodeViaSSH_DockerPruneWarnings(t *testing.T) {
	_ = runCleanup(t, &fakeRemoteHost{warnings: []string{"container prune: busy"}})
}

// A failed daemon prune is reported, not fatal; reclaimed space stays 0B.
func TestCleanupNodeViaSSH_DockerPruneError(t *testing.T) {
	r := runCleanup(t, &fakeRemoteHost{pruneErr: errors.New("daemon down")})
	if r.DockerSpaceFreed != "0B" {
		t.Errorf("on prune error DockerSpaceFreed = %q, want 0B", r.DockerSpaceFreed)
	}
}

// fakeFileInfo is a directory entry with a controllable name and mtime.
type fakeFileInfo struct {
	name  string
	mtime time.Time
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return os.ModeDir }
func (f fakeFileInfo) ModTime() time.Time { return f.mtime }
func (f fakeFileInfo) IsDir() bool        { return true }
func (f fakeFileInfo) Sys() any           { return nil }

type fakeRemoteHost struct {
	entries   []os.FileInfo
	readErr   error
	removed   []string
	reclaimed uint64
	warnings  []string
	pruneErr  error
}

func (f *fakeRemoteHost) Close() error                          { return nil }
func (f *fakeRemoteHost) ReadDir(string) ([]os.FileInfo, error) { return f.entries, f.readErr }
func (f *fakeRemoteHost) RemoveAll(p string) error              { f.removed = append(f.removed, p); return nil }
func (f *fakeRemoteHost) DockerSystemPrune(context.Context) (uint64, []string, error) {
	return f.reclaimed, f.warnings, f.pruneErr
}

type fakeConnector struct {
	host shared.RemoteHost
	err  error
}

func (f *fakeConnector) Connect(shared.SSHTarget) (shared.RemoteHost, error) {
	return f.host, f.err
}

func TestCleanupNodeViaSSH(t *testing.T) {
	old := time.Now().AddDate(0, 0, -30)
	host := &fakeRemoteHost{
		entries: []os.FileInfo{
			fakeFileInfo{name: "oldjob", mtime: old},  // orphan (not running, past grace) -> deleted
			fakeFileInfo{name: "livejob", mtime: old}, // running -> kept
			fakeFileInfo{name: "alloc", mtime: old},   // protected runtime dir -> skipped
		},
		reclaimed: 1024,
	}
	a := New(
		&fakeNomad{jobs: map[string]struct{}{"livejob": {}}},
		&fakeConnector{host: host},
	)
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.CleanupNodeViaSSH)

	cfg := CleanupConfig{DataDir: "/data", GraceDays: 7, DryRun: false, DockerPrune: true}
	val, err := env.ExecuteActivity(a.CleanupNodeViaSSH, nodes.NodeInfo{Name: "n1", Address: "10.0.0.1"}, cfg)
	if err != nil {
		t.Fatalf("CleanupNodeViaSSH: %v", err)
	}
	var result CleanupResult
	if err := val.Get(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", result.Deleted)
	}
	if result.Orphaned != 1 {
		t.Errorf("Orphaned = %d, want 1", result.Orphaned)
	}
	if len(host.removed) != 1 || host.removed[0] != "/data/oldjob" {
		t.Errorf("removed = %v, want [/data/oldjob]", host.removed)
	}
	if result.DockerSpaceFreed == "0B" {
		t.Error("expected DockerSpaceFreed to reflect the prune, got 0B")
	}
}

// In dry-run nothing is deleted, but orphans are still counted/reported.
func TestCleanupNodeViaSSH_DryRun(t *testing.T) {
	old := time.Now().AddDate(0, 0, -30)
	host := &fakeRemoteHost{entries: []os.FileInfo{
		fakeFileInfo{name: "oldjob", mtime: old},
	}}
	a := New(&fakeNomad{}, &fakeConnector{host: host})
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.CleanupNodeViaSSH)

	cfg := CleanupConfig{DataDir: "/data", GraceDays: 7, DryRun: true}
	val, err := env.ExecuteActivity(a.CleanupNodeViaSSH, nodes.NodeInfo{Name: "n1"}, cfg)
	if err != nil {
		t.Fatalf("CleanupNodeViaSSH: %v", err)
	}
	var result CleanupResult
	_ = val.Get(&result)
	if result.Orphaned != 1 {
		t.Errorf("Orphaned = %d, want 1", result.Orphaned)
	}
	if result.Deleted != 0 || len(host.removed) != 0 {
		t.Errorf("dry-run must not delete: Deleted=%d removed=%v", result.Deleted, host.removed)
	}
}
