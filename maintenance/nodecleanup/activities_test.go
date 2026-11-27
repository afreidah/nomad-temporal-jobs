// -------------------------------------------------------------------------------
// Node Cleanup Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for the package's pure functions (orphan classification, job-running
// and mounted-directory detection). External dependencies (Nomad API, SSH) are
// not tested here.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"testing"
	"time"

	"munchbox/temporal-workers/shared/client/nomad"
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

func TestNewActiveDirs(t *testing.T) {
	w := nomad.NodeWorkloads{
		JobIDs: map[string]struct{}{"loki": {}},
		HostPaths: []string{
			"/data/prometheus",
			"/data/cache-${NOMAD_ALLOC_INDEX}/redis",
			"/data/${NOMAD_JOB_NAME}-a.b/x",
			"/data/../data/tidy//sub/",
			"/etc/nomad.d/tls/ca-chain.crt",
			"/database/elsewhere",
			"local/app.conf",
		},
	}
	d := newActiveDirs(w, "/data/")
	cases := []struct {
		dir  string
		want bool
	}{
		{"loki-1", true},     // job-name match still applies
		{"prometheus", true}, // mounted, exact
		{"cache-0", true},    // mounted, ${...} matches the index
		{"cache-12", true},   // any index
		{"cache", false},     // the literal dash is required
		{"web-a.b", true},    // a leading ${...} matches the job name
		{"web-axb", false},   // the dot is literal, not a regex wildcard
		{"tidy", true},       // the path is cleaned before it is indexed
		{"elsewhere", false}, // /database is not under /data
		{"app.conf", false},  // task-relative paths are ignored
		{"orphan", false},
	}
	for _, c := range cases {
		if got := d.contains(c.dir); got != c.want {
			t.Errorf("contains(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

// A live alloc mounting the data dir itself protects every entry in it.
func TestNewActiveDirs_DataDirMounted(t *testing.T) {
	d := newActiveDirs(nomad.NodeWorkloads{HostPaths: []string{"/data"}}, "/data")
	if !d.contains("anything") {
		t.Error("a mount of the data dir itself must protect every entry")
	}
}

func TestClassifyEntry(t *testing.T) {
	running := activeDirs{jobs: map[string]struct{}{"loki": {}}}
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
