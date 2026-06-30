// -------------------------------------------------------------------------------
// Shared GitHub Client - Repo List Helper Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
// -------------------------------------------------------------------------------

package git

import "testing"

func TestParseRepoList(t *testing.T) {
	got := ParseRepoList("# header\n a/b \n\nc/d\n# trailing\n")
	if len(got) != 2 || got[0] != "a/b" || got[1] != "c/d" {
		t.Errorf("ParseRepoList = %v, want [a/b c/d]", got)
	}
}

func TestSplitRepo(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"owner/repo", true},
		{" o/r ", true}, // surrounding whitespace is trimmed
		{"no-slash", false},
		{"/repo", false},
		{"owner/", false},
		{"o/r/x", false},
	}
	for _, c := range cases {
		if _, _, ok := SplitRepo(c.in); ok != c.wantOK {
			t.Errorf("SplitRepo(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
	}
}
