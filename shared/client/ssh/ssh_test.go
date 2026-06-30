package ssh

import "testing"

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", `'plain'`},
		{"/path/with spaces", `'/path/with spaces'`},
		{"it's quoted", `'it'\''s quoted'`},
		{"", `''`},
	}
	for _, c := range cases {
		if got := ShellQuote(c.in); got != c.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
