// -------------------------------------------------------------------------------
// Shared SSH - Client Construction & Connect Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers NewSSHClient (key + host-CA parsing) and the Connect/connect dial path
// against a closed port -- a real, fast dial failure, no SSH server needed.
// Reuses testSigner/writeTemp from ssh_auth_test.go.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestNewSSHClientAndConnect_DialFails(t *testing.T) {
	signer, keyPEM := testSigner(t)
	cfg := SSHConfig{
		KeyPath:    writeTemp(t, "id", keyPEM),
		HostCAPath: writeTemp(t, "ca.pub", ssh.MarshalAuthorizedKey(signer.PublicKey())),
	}

	c, err := NewSSHClient(cfg)
	if err != nil {
		t.Fatalf("NewSSHClient: %v", err)
	}

	// Port 1 is reserved and closed, so the dial is refused immediately. This
	// also exercises the default-port branch (Connect with Port 0 -> :22).
	if _, err := c.Connect(SSHTarget{Host: "127.0.0.1", Port: 1}); err == nil {
		t.Fatal("expected a dial error connecting to a closed port")
	}
	if _, err := c.Connect(SSHTarget{Host: "127.0.0.1"}); err == nil {
		t.Error("expected a dial error on the default port")
	}

	// The one-shot helpers all connect-then-run; a closed port makes each fail
	// at the dial, covering their shared connect call.
	ctx := context.Background()
	tgt := SSHTarget{Host: "127.0.0.1", Port: 1}
	if _, err := c.Run(ctx, tgt, "true"); err == nil {
		t.Error("Run should fail to dial a closed port")
	}
	if _, err := c.RunWithHeartbeat(ctx, tgt, "true", time.Hour); err == nil {
		t.Error("RunWithHeartbeat should fail to dial a closed port")
	}
	if _, err := c.DirSize(ctx, tgt, "/"); err == nil {
		t.Error("DirSize should fail to dial a closed port")
	}
}
