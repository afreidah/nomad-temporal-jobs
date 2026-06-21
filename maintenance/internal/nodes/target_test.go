// -------------------------------------------------------------------------------
// Maintenance Node Helpers - Target Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Pins the SSH target shape: every node is reached as root over its address.
// -------------------------------------------------------------------------------

package nodes

import "testing"

func TestTarget(t *testing.T) {
	got := Target(NodeInfo{Address: "10.200.0.11", Name: "worker-1", IsOracle: true})
	if got.Host != "10.200.0.11" {
		t.Errorf("Host = %q, want 10.200.0.11", got.Host)
	}
	// The worker always connects as root, even on oracle hosts (the Vault SSH CA
	// issues a root principal those hosts accept).
	if got.User != "root" {
		t.Errorf("User = %q, want root", got.User)
	}
}
