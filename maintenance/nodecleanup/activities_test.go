// -------------------------------------------------------------------------------
// Node Cleanup Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for the package's pure functions (orphan classification, job-running
// detection). External dependencies (Nomad API, SSH) are not tested here.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// ORPHAN DETECTION
// -------------------------------------------------------------------------

func TestIsJobRunning(t *testing.T) {
	running := map[string]struct{}{"prometheus": {}, "loki": {}}
	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"exact match", "prometheus", true},
		{"index-suffixed match", "loki-2", true},
		{"trailing dash, no digits", "loki-", true},
		{"not running", "old-job", false},
		{"prefix is not a match", "prometheus-extra", false},
	}
	for _, c := range cases {
		if got := isJobRunning(c.dir, running); got != c.want {
			t.Errorf("%s: isJobRunning(%q) = %v, want %v", c.name, c.dir, got, c.want)
		}
	}
}

func TestClassifyEntry(t *testing.T) {
	running := map[string]struct{}{"loki": {}}
	cfg := CleanupConfig{GraceDays: 7}
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	oldDir := now.AddDate(0, 0, -10)
	recentDir := now.AddDate(0, 0, -2)

	cases := []struct {
		name       string
		entry      dirEntry
		wantAction orphanAction
		wantAge    int
	}{
		{"excluded runtime dir", dirEntry{name: "alloc", mtime: oldDir}, entrySkipExcluded, 0},
		{"running job", dirEntry{name: "loki-1", mtime: oldDir}, entryActive, 0},
		{"orphan within grace", dirEntry{name: "old-job", mtime: recentDir}, entryWithinGrace, 2},
		{"orphan past grace", dirEntry{name: "old-job", mtime: oldDir}, entryOrphan, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, age := classifyEntry(c.entry, running, cfg, now)
			if action != c.wantAction || age != c.wantAge {
				t.Errorf("classifyEntry = (%d, %d), want (%d, %d)", action, age, c.wantAction, c.wantAge)
			}
		})
	}
}
