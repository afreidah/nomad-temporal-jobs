// -------------------------------------------------------------------------------
// Node Cleanup Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for pure functions: output parsing, script generation, and SSH config
// construction. External dependencies (Nomad API, SSH) are not tested here.
// -------------------------------------------------------------------------------

package activities

import "testing"

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

// -------------------------------------------------------------------------
// CONFIG VALIDATION
// -------------------------------------------------------------------------

func TestConfig_ApplyDefaults_Defaults(t *testing.T) {
	cfg := Config{}
	cfg.ApplyDefaults()

	if cfg.SSHKeyPath != "/root/.ssh/id_ed25519" {
		t.Errorf("SSHKeyPath = %q, want default", cfg.SSHKeyPath)
	}
	if cfg.SSHCertPath != "/root/.ssh/id_ed25519-cert.pub" {
		t.Errorf("SSHCertPath = %q, want default", cfg.SSHCertPath)
	}
	if cfg.SSHHostCAPath != "/root/.ssh/ssh-host-ca.pub" {
		t.Errorf("SSHHostCAPath = %q, want default", cfg.SSHHostCAPath)
	}
}

func TestConfig_ApplyDefaults_PreservesCustomValues(t *testing.T) {
	cfg := Config{
		SSHKeyPath:    "/custom/key",
		SSHCertPath:   "/custom/cert",
		SSHHostCAPath: "/custom/ca",
	}
	cfg.ApplyDefaults()

	if cfg.SSHKeyPath != "/custom/key" {
		t.Errorf("SSHKeyPath = %q, want /custom/key", cfg.SSHKeyPath)
	}
}
