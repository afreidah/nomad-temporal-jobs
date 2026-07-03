// -------------------------------------------------------------------------------
// Shared Containerd-over-SSH - Store-Gate Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The containerd prune tunnels a live daemon socket, so it can't be unit-tested
// without a real containerd (same limitation as DockerSystemPrune). What is
// pure and safety-critical is the store-aware gate: it must permit a prune only
// when docker's live store is overlay2.
// -------------------------------------------------------------------------------

package ssh

import "testing"

func TestContainerdStoreIsSafe(t *testing.T) {
	tests := []struct {
		driver   string
		wantSafe bool
	}{
		{"overlay2", true},   // docker on overlay2 -> containerd moby store is the orphaned duplicate
		{"overlayfs", false}, // containerd snapshotter is docker's live store -> must not prune
		{"btrfs", false},
		{"", false},
	}
	for _, tt := range tests {
		safe, reason := containerdStoreIsSafe(tt.driver)
		if safe != tt.wantSafe {
			t.Errorf("containerdStoreIsSafe(%q) safe = %v, want %v", tt.driver, safe, tt.wantSafe)
		}
		if !safe && reason == "" {
			t.Errorf("containerdStoreIsSafe(%q) returned no reason on unsafe", tt.driver)
		}
	}
}
