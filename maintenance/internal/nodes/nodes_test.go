// -------------------------------------------------------------------------------
// Shared Node Primitives - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests the pure HumanBytes formatter. The saga activities touch the Nomad API
// and SSH and are exercised via the registry-GC / aptly-cleanup workflow test
// suites with mocks rather than directly here.
// -------------------------------------------------------------------------------

package nodes

import "testing"

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{2048, "2.0KiB"},
		{2 * 1024 * 1024, "2.0MiB"},
		{2 * 1024 * 1024 * 1024, "2.0GiB"},
		{150 * 1024 * 1024, "150MiB"},
		{-1, "-1B"},
	}
	for _, c := range cases {
		got := HumanBytes(c.in)
		if got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
