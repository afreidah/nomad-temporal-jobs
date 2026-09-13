// -------------------------------------------------------------------------------
// Shared Worker Runtime - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Pins namespace resolution. The default matters as much as the override: a
// worker deployed without TEMPORAL_NAMESPACE must keep polling "default", or it
// silently stops serving its schedules.
// -------------------------------------------------------------------------------

package shared

import "testing"

func TestResolveNamespace(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"unset falls back to default", "", "default"},
		{"explicit value is used", "ci", "ci"},
		{"default may be named outright", "default", "default"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEMPORAL_NAMESPACE", tc.env)
			if got := resolveNamespace(); got != tc.want {
				t.Errorf("resolveNamespace() = %q, want %q", got, tc.want)
			}
		})
	}
}
