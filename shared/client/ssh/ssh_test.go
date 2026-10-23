package ssh

import (
	"context"
	"errors"
	"testing"
)

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

func TestParseDuBytes(t *testing.T) {
	cases := []struct {
		name, out string
		want      int64
		wantErr   bool
	}{
		{"total", "123456789\t/mnt/gdrive/munchbox-data/registry\n", 123456789, false},
		{"total after warnings", "du: cannot read directory 'x': Permission denied\n42\t/data\n", 42, false},
		{"empty", "", 0, true},
		{"garbage", "not a number\n", 0, true},
	}
	for _, c := range cases {
		got, err := parseDuBytes(c.out)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: parseDuBytes = %d, %v; want %d, err %v", c.name, got, err, c.want, c.wantErr)
		}
	}
}

func TestDuTotal(t *testing.T) {
	exit1 := errors.New("exit status 1")
	cases := []struct {
		name           string
		out            string
		runErr, ctxErr error
		want           int64
		wantErr        error
	}{
		{"ok", "42\t/data\n", nil, nil, 42, nil},
		{"total despite non-zero exit", "du: denied\n42\t/data\n", exit1, nil, 42, nil},
		{"cancelled", "42\t/data\n", nil, context.Canceled, 0, context.Canceled},
		{"run error", "", exit1, nil, 0, exit1},
	}
	for _, c := range cases {
		got, err := duTotal(c.out, c.runErr, c.ctxErr)
		if got != c.want || !errors.Is(err, c.wantErr) {
			t.Errorf("%s: duTotal = %d, %v; want %d, %v", c.name, got, err, c.want, c.wantErr)
		}
	}
	if _, err := duTotal("garbage", nil, nil); err == nil {
		t.Error("unparseable output with a clean exit must fail")
	}
}
