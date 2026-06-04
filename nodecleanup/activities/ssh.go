// -------------------------------------------------------------------------------
// SSH Helpers - Remote Command Execution
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Thin SSH-exec helpers used across the worker (node cleanup, registry GC,
// aptly cleanup): a one-shot run that captures stdout, a long-running variant
// that heartbeats and captures combined output, and a shell-quoting helper.
// SSH client config is built by buildSSHConfig in activities.go.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"golang.org/x/crypto/ssh"
)

// runSSHCommand opens an SSH session to the node, runs cmd, and returns
// stdout. Used by short measurement commands.
func (a *Activities) runSSHCommand(node NodeInfo, cmd string) (string, error) {
	cfg, err := a.buildSSHConfig(node)
	if err != nil {
		return "", err
	}
	cli, err := ssh.Dial("tcp", node.Address+":22", cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", node.Name, err)
	}
	defer cli.Close()

	session, err := cli.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Run(cmd); err != nil {
		return stdout.String(), fmt.Errorf("ssh run %q: %w (stderr: %s)", cmd, err, stderr.String())
	}
	return stdout.String(), nil
}

// runSSHCommandWithHeartbeat runs an SSH command that may take a long time and
// emits an activity.RecordHeartbeat every `interval` so the activity's
// HeartbeatTimeout can detect stalls. Returns combined stdout+stderr captured
// up to that point.
func (a *Activities) runSSHCommandWithHeartbeat(ctx context.Context, node NodeInfo, cmd string, interval time.Duration) (string, error) {
	cfg, err := a.buildSSHConfig(node)
	if err != nil {
		return "", err
	}
	cli, err := ssh.Dial("tcp", node.Address+":22", cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", node.Name, err)
	}
	defer cli.Close()

	session, err := cli.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var combined bytes.Buffer
	session.Stdout = &combined
	session.Stderr = &combined

	if err := session.Start(cmd); err != nil {
		return "", fmt.Errorf("ssh start %q: %w", cmd, err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			if err != nil {
				return combined.String(), fmt.Errorf("ssh run %q: %w (output: %s)", cmd, err, combined.String())
			}
			return combined.String(), nil
		case <-ticker.C:
			activity.RecordHeartbeat(ctx, combined.Len())
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGTERM)
			return combined.String(), ctx.Err()
		}
	}
}

// shellQuote single-quotes a string for safe inclusion in a remote bash
// command. Embedded single quotes are escaped via the canonical `'\”`
// sequence.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
