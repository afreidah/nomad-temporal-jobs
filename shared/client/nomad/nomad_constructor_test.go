// -------------------------------------------------------------------------------
// Shared - Nomad Constructor Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// NewNomadClient/NewNomad only build the api.Client (no connection happens until
// a request), so the happy path is unit-coverable without a cluster.
// -------------------------------------------------------------------------------

package nomad

import "testing"

func TestNewNomadClient(t *testing.T) {
	c, err := NewNomadClient()
	if err != nil {
		t.Fatalf("NewNomadClient: %v", err)
	}
	if c == nil {
		t.Fatal("NewNomadClient returned a nil client")
	}
}

func TestNewNomad(t *testing.T) {
	n, err := NewNomad()
	if err != nil {
		t.Fatalf("NewNomad: %v", err)
	}
	if n == nil {
		t.Fatal("NewNomad returned nil")
	}
}
