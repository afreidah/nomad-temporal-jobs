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

func TestNewNomadClient_FromEnv(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "https://nomad.example:4646")
	t.Setenv("NOMAD_TOKEN", "secret-id")
	t.Setenv("NOMAD_CACERT", "/etc/nomad/ca.pem")

	if _, err := NewNomadClient(); err != nil {
		t.Fatalf("NewNomadClient: %v", err)
	}
}

func TestNewNomad_BadAddr(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "http://[::1")
	if _, err := NewNomad(); err == nil {
		t.Fatal("expected an error for an unparseable NOMAD_ADDR")
	}
}
