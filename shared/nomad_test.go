// -------------------------------------------------------------------------------
// Shared Nomad Helpers - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the pure helpers extracted for reuse across the trivyscan and
// nodecleanup workers: SSH-address resolution, the running-alloc filter, and
// job-not-found classification.
// -------------------------------------------------------------------------------

package shared

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/nomad/api"
)

func TestNodeSSHAddress(t *testing.T) {
	tests := []struct {
		name string
		node *api.Node
		want string
	}{
		{
			name: "prefers ip-address attribute",
			node: &api.Node{
				Attributes: map[string]string{"unique.network.ip-address": "10.0.0.7"},
				HTTPAddr:   "10.0.0.7:4646",
			},
			want: "10.0.0.7",
		},
		{
			name: "falls back to HTTPAddr with port stripped",
			node: &api.Node{HTTPAddr: "192.168.1.5:4646"},
			want: "192.168.1.5",
		},
		{
			name: "HTTPAddr without a port",
			node: &api.Node{HTTPAddr: "192.168.1.5"},
			want: "192.168.1.5",
		},
		{
			name: "empty attribute falls through to HTTPAddr",
			node: &api.Node{
				Attributes: map[string]string{"unique.network.ip-address": ""},
				HTTPAddr:   "172.16.0.1:4646",
			},
			want: "172.16.0.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeSSHAddress(tt.node); got != tt.want {
				t.Errorf("NodeSSHAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunningAllocStubs(t *testing.T) {
	allocs := []*api.AllocationListStub{
		{ID: "a", ClientStatus: api.AllocClientStatusRunning},
		{ID: "b", ClientStatus: api.AllocClientStatusComplete},
		{ID: "c", ClientStatus: api.AllocClientStatusRunning},
		{ID: "d", ClientStatus: api.AllocClientStatusFailed},
	}

	got := RunningAllocStubs(allocs)
	if len(got) != 2 {
		t.Fatalf("got %d running allocs, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("got ids %q, %q; want a, c", got[0].ID, got[1].ID)
	}

	if n := len(RunningAllocStubs(nil)); n != 0 {
		t.Errorf("nil input: got %d, want 0", n)
	}
}

// IsJobNotFound's typed-status branch (api.UnexpectedResponseError with a 404)
// fires against a live Nomad client but can't be constructed here -- the type's
// fields and constructor are unexported -- so these cases exercise the string
// fallback, which is what classifies wrapped errors in practice.
func TestIsJobNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"404 status in message", errors.New("Unexpected response code: 404"), true},
		{"job not found phrase", errors.New(`error scaling job: "job not found"`), true},
		{"wrapped job not found", fmt.Errorf("scale web/group: %w", errors.New("job not found")), true},
		{"unrelated transient error", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsJobNotFound(tt.err); got != tt.want {
				t.Errorf("IsJobNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
