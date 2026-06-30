// -------------------------------------------------------------------------------
// Shared SSH - line() Unit Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the sudo-prefix helper applied to every command on a connection.
// -------------------------------------------------------------------------------

package ssh

import "testing"

func TestSSHConnLine(t *testing.T) {
	if got := (&sshConn{sudo: false}).line("ls -la"); got != "ls -la" {
		t.Errorf("no sudo: got %q, want %q", got, "ls -la")
	}
	if got := (&sshConn{sudo: true}).line("ls -la"); got != "sudo ls -la" {
		t.Errorf("sudo: got %q, want %q", got, "sudo ls -la")
	}
}
