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
