// -------------------------------------------------------------------------------
// Shared SSH Client - Certificate-Authenticated Remote Command Execution
//
// Author: Alex Freidah
//
// A reusable SSH client for workers that operate on remote hosts (node
// cleanup, registry GC, aptly cleanup). It parses its credentials once and
// runs commands against any target with host-CA verification, certificate auth
// (key fallback), context-aware cancellation, and optional activity
// heartbeating for long-running commands. Workers build one client and share
// it across activity invocations; a single connection can run many commands.
//
// This replaces ad-hoc per-worker ssh.Dial plumbing and hand-built shell
// scripts: decision logic belongs in Go, and this client is the thin, audited
// transport that runs the resulting fixed commands.
// -------------------------------------------------------------------------------

package shared

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const defaultSSHTimeout = 30 * time.Second

// SSHConfig holds the credential and verification material for an SSHClient.
type SSHConfig struct {
	KeyPath    string        // SSH private key (required)
	CertPath   string        // SSH client certificate (optional; cert auth preferred when present)
	HostCAPath string        // host CA public key; remote host keys are verified against it (required)
	Timeout    time.Duration // dial timeout; defaults to 30s when zero
}

// SSHTarget identifies a host to connect to and how to log in.
type SSHTarget struct {
	Host string // hostname or IP (no port)
	User string // login user
	Port int    // SSH port; defaults to 22 when zero
	Sudo bool   // when true, commands are prefixed with "sudo "
}

// SSHClient runs commands on remote hosts over SSH. Construct it once with
// NewSSHClient and reuse it; it holds no per-host state and is safe for
// concurrent use.
type SSHClient struct {
	auth    []ssh.AuthMethod
	hostKey ssh.HostKeyCallback
	timeout time.Duration
}

// NewSSHClient parses the key, optional certificate, and host CA from cfg and
// returns a ready client. The credentials are read once here rather than on
// every command.
func NewSSHClient(cfg SSHConfig) (*SSHClient, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultSSHTimeout
	}

	signer, err := loadSigner(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	auth, err := buildAuthMethods(signer, cfg.CertPath)
	if err != nil {
		return nil, err
	}
	hostKey, err := hostCACallback(cfg.HostCAPath)
	if err != nil {
		return nil, err
	}

	return &SSHClient{auth: auth, hostKey: hostKey, timeout: timeout}, nil
}

// loadSigner reads and parses the SSH private key at keyPath.
func loadSigner(keyPath string) (ssh.Signer, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	return signer, nil
}

// buildAuthMethods returns certificate-then-key auth when certPath is set, or
// key-only when it is empty. A present-but-broken certificate is a hard error
// rather than a silent fall-through to bare-key auth, which would otherwise
// surface later as a confusing "permission denied" from the server.
func buildAuthMethods(signer ssh.Signer, certPath string) ([]ssh.AuthMethod, error) {
	var auth []ssh.AuthMethod
	if certPath != "" {
		certData, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("read ssh cert %s: %w", certPath, err)
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey(certData)
		if err != nil {
			return nil, fmt.Errorf("parse ssh cert %s: %w", certPath, err)
		}
		cert, ok := pub.(*ssh.Certificate)
		if !ok {
			return nil, fmt.Errorf("ssh cert %s is not a certificate", certPath)
		}
		certSigner, err := ssh.NewCertSigner(cert, signer)
		if err != nil {
			return nil, fmt.Errorf("build cert signer: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(certSigner))
	}
	auth = append(auth, ssh.PublicKeys(signer))
	return auth, nil
}

// hostCACallback reads the host CA public key at caPath and returns a callback
// that verifies remote host keys were signed by it.
func hostCACallback(caPath string) (ssh.HostKeyCallback, error) {
	caData, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read host CA %s: %w", caPath, err)
	}
	caKey, _, _, _, err := ssh.ParseAuthorizedKey(caData)
	if err != nil {
		return nil, fmt.Errorf("parse host CA key: %w", err)
	}
	checker := &ssh.CertChecker{
		IsHostAuthority: func(auth ssh.PublicKey, _ string) bool {
			return bytes.Equal(auth.Marshal(), caKey.Marshal())
		},
	}
	return checker.CheckHostKey, nil
}

// Connect opens a connection to t for multi-step operations (file ops, Docker
// prune). The caller must Close the returned RemoteHost. Multiple commands may
// be run sequentially on one connection, each in its own session.
func (c *SSHClient) Connect(t SSHTarget) (RemoteHost, error) {
	return c.connect(t)
}

// connect dials t and returns the concrete connection. Internal callers that
// need the full connection (RunContainer, the one-shot Run/DirSize helpers) use
// this; external callers receive the RemoteHost interface via Connect.
func (c *SSHClient) connect(t SSHTarget) (*sshConn, error) {
	port := t.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            c.auth,
		HostKeyCallback: c.hostKey,
		Timeout:         c.timeout,
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(port))
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", t.Host, err)
	}
	return &sshConn{client: conn, sudo: t.Sudo}, nil
}

// Run opens a connection to t, runs cmd, and closes the connection. Use Connect
// when issuing several commands to the same host.
func (c *SSHClient) Run(ctx context.Context, t SSHTarget, cmd string) (string, error) {
	conn, err := c.connect(t)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	return conn.Run(ctx, cmd)
}

// RunWithHeartbeat opens a connection to t, runs cmd while heartbeating, and
// closes the connection.
func (c *SSHClient) RunWithHeartbeat(ctx context.Context, t SSHTarget, cmd string, interval time.Duration) (string, error) {
	conn, err := c.connect(t)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	return conn.RunWithHeartbeat(ctx, cmd, interval)
}

// DirSize opens a connection to t, measures dir over SFTP, and closes the
// connection. The walk is aborted if ctx is cancelled.
func (c *SSHClient) DirSize(ctx context.Context, t SSHTarget, dir string) (int64, error) {
	conn, err := c.connect(t)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	return conn.DirSize(dir)
}

// sshConn is the concrete SSH-backed implementation of RemoteHost: an open
// connection to one host for commands and SFTP file operations.
type sshConn struct {
	client *ssh.Client
	sudo   bool
	sftp   *sftp.Client // lazily opened by the SFTP helpers
}

// Close tears down the SFTP session (if any) and the underlying connection.
func (s *sshConn) Close() error {
	if s.sftp != nil {
		_ = s.sftp.Close()
	}
	return s.client.Close()
}

// sftpClient lazily opens and caches an SFTP session on the connection.
func (s *sshConn) sftpClient() (*sftp.Client, error) {
	if s.sftp == nil {
		c, err := sftp.NewClient(s.client)
		if err != nil {
			return nil, fmt.Errorf("open sftp: %w", err)
		}
		s.sftp = c
	}
	return s.sftp, nil
}

// ReadDir lists the immediate entries of dir on the remote host over SFTP.
func (s *sshConn) ReadDir(dir string) ([]os.FileInfo, error) {
	c, err := s.sftpClient()
	if err != nil {
		return nil, err
	}
	return c.ReadDir(dir)
}

// RemoveAll deletes path and everything under it on the remote host over SFTP.
func (s *sshConn) RemoveAll(path string) error {
	c, err := s.sftpClient()
	if err != nil {
		return err
	}
	return c.RemoveAll(path)
}

// DirSize returns the total size in bytes of the regular files under dir,
// walked over SFTP. Unreadable entries are skipped.
func (s *sshConn) DirSize(dir string) (int64, error) {
	c, err := s.sftpClient()
	if err != nil {
		return 0, err
	}
	var total int64
	walker := c.Walk(dir)
	for walker.Step() {
		if walker.Err() != nil {
			continue
		}
		if info := walker.Stat(); !info.IsDir() {
			total += info.Size()
		}
	}
	return total, nil
}

// Run executes cmd on the connection and returns its stdout. stderr is folded
// into the returned error. The command is cancelled (its session torn down) if
// ctx is done, which is more reliable than SSH signal forwarding.
func (s *sshConn) Run(ctx context.Context, cmd string) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(s.line(cmd)); err != nil {
		if ctx.Err() != nil {
			return stdout.String(), ctx.Err()
		}
		return stdout.String(), fmt.Errorf("ssh run %q: %w (stderr: %s)", cmd, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// RunWithHeartbeat executes a long-running command, recording an activity
// heartbeat every interval so a stall trips the HeartbeatTimeout. It returns
// the combined stdout+stderr (long-running tools often log progress to stderr).
// The session is torn down if ctx is cancelled.
func (s *sshConn) RunWithHeartbeat(ctx context.Context, cmd string, interval time.Duration) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()

	var combined bytes.Buffer
	session.Stdout = &combined
	session.Stderr = &combined

	if err := session.Start(s.line(cmd)); err != nil {
		return "", fmt.Errorf("ssh start %q: %w", cmd, err)
	}

	_, werr := WithHeartbeat(ctx, interval, func() (struct{}, error) {
		return struct{}{}, session.Wait()
	})
	if werr != nil {
		if ctx.Err() != nil {
			return combined.String(), ctx.Err()
		}
		return combined.String(), fmt.Errorf("ssh run %q: %w (output: %s)", cmd, werr, strings.TrimSpace(combined.String()))
	}
	return combined.String(), nil
}

// line applies the sudo prefix when the target requires it. It prefixes the
// whole command, so callers should pass a single command rather than relying on
// sudo to span a shell pipeline.
func (s *sshConn) line(cmd string) string {
	if s.sudo {
		return "sudo " + cmd
	}
	return cmd
}

// ShellQuote single-quotes a string for safe inclusion in a remote command.
// Embedded single quotes are escaped via the canonical '\” sequence.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
