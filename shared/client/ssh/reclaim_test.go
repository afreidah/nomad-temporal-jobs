// -------------------------------------------------------------------------------
// Shared Reclaim Helpers - Pure-logic Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The rootfs and buildx sweeps drive live SFTP/Docker, so they can't be unit-
// tested without a real host. Their safety-critical selection logic is pure and
// is tested here: which rootfs entries count as orphaned, and which volume names
// count as buildx builder-state volumes.
// -------------------------------------------------------------------------------

package ssh

import (
	"errors"
	"os"
	"testing"
	"time"
)

type stubFileInfo struct {
	name  string
	isDir bool
}

func (s stubFileInfo) Name() string       { return s.name }
func (s stubFileInfo) Size() int64        { return 0 }
func (s stubFileInfo) Mode() os.FileMode  { return os.ModeDir }
func (s stubFileInfo) ModTime() time.Time { return time.Time{} }
func (s stubFileInfo) IsDir() bool        { return s.isDir }
func (s stubFileInfo) Sys() any           { return nil }

func TestSweep(t *testing.T) {
	cands := []pruneCandidate{{id: "a", size: 10}, {id: "b", size: 20}, {id: "boom", size: 5}}

	// Dry-run tallies candidates + reclaimable, deletes nothing.
	dry := sweep(cands, true, func(string) error { t.Fatal("dry-run must not remove"); return nil })
	if dry.Candidates != 3 || dry.Reclaimable != 35 || dry.Deleted != 0 {
		t.Errorf("dry-run sweep = %+v, want 3 candidates / 35 reclaimable / 0 deleted", dry)
	}

	// Real run removes each; a per-item failure becomes a warning, not fatal.
	got := sweep(cands, false, func(id string) error {
		if id == "boom" {
			return errors.New("busy")
		}
		return nil
	})
	if got.Deleted != 2 || got.Reclaimed != 30 {
		t.Errorf("sweep deleted=%d reclaimed=%d, want 2 / 30", got.Deleted, got.Reclaimed)
	}
	if len(got.Warnings) != 1 {
		t.Errorf("sweep warnings = %v, want 1 (the failed item)", got.Warnings)
	}
}

func TestRootfsOrphans(t *testing.T) {
	entries := []os.FileInfo{
		stubFileInfo{name: "live", isDir: true},   // referenced by a mount -> kept
		stubFileInfo{name: "orphan", isDir: true}, // no mount -> swept
		stubFileInfo{name: "afile", isDir: false}, // not a dir -> ignored
	}
	// A mount table where only the "live" entry appears (as an overlay lowerdir).
	mountInfo := "42 41 0:44 / /run/x rw shared:1 - overlay overlay rw,lowerdir=" +
		rootfsOverlayDir + "/live/fs,upperdir=/x,workdir=/y\n"

	got := rootfsOrphans(entries, mountInfo)
	if len(got) != 1 || got[0] != "orphan" {
		t.Fatalf("rootfsOrphans = %v, want [orphan]", got)
	}
}

func TestIsBuildxStateVolume(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"buildx_buildkit_munchbox-builder0_state", true},
		{"buildx_buildkit_multiarch-builder0_state", true},
		{"buildx_buildkit_x", false},              // missing _state suffix
		{"myapp_data", false},                     // unrelated volume
		{"prefix_buildx_buildkit_x_state", false}, // prefix must be at the start
	}
	for _, tt := range tests {
		if got := isBuildxStateVolume(tt.name); got != tt.want {
			t.Errorf("isBuildxStateVolume(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
