// Package ssh is a certificate-authenticated SSH client for workers that operate
// on remote hosts (node cleanup, registry GC, aptly cleanup): host-CA
// verification, certificate auth with key fallback, context cancellation, and
// optional activity heartbeating for long-running commands. It also carries the
// remote-Docker operations layered on an SSH connection (run container, system
// prune); remote.go declares the consumer-side interfaces those satisfy.
package ssh
