// -------------------------------------------------------------------------------
// Shared Remote-Host Capabilities - Logical Interfaces
//
// Author: Alex Freidah
//
// Workers operate on remote nodes, but they shouldn't depend on "SSH" as a
// concept -- they need capabilities (run a container, measure a directory,
// connect for file/Docker maintenance). These interfaces name those
// capabilities; *SSHClient is just the transport that implements them. A worker
// declares the capability it needs and is handed an *SSHClient at wiring time,
// which keeps the worker testable with a fake and free of transport details.
// -------------------------------------------------------------------------------

package ssh

import (
	"context"
	"os"
	"time"

	"github.com/moby/moby/api/types/container"
)

// ContainerRunner runs a one-shot container on a remote host's Docker daemon
// (tunneled over SSH). Maintenance workers that execute a packaged tool in a
// container (aptly cleanup, registry GC) depend on this.
type ContainerRunner interface {
	RunContainer(ctx context.Context, t SSHTarget, cfg *container.Config, binds []string, heartbeat time.Duration) (string, error)
}

// DirMeasurer reports the total size in bytes of a directory on a remote host,
// walked over SFTP. Used for before/after data-dir reporting.
type DirMeasurer interface {
	DirSize(ctx context.Context, t SSHTarget, dir string) (int64, error)
}

// HostConnector opens a reusable connection to a remote host for multi-step
// operations. The caller Closes the returned RemoteHost.
type HostConnector interface {
	Connect(t SSHTarget) (RemoteHost, error)
}

// RemoteHost is an open connection to one host: SFTP file operations and Docker
// maintenance over the tunneled daemon. Close it when done.
type RemoteHost interface {
	Close() error
	ReadDir(dir string) ([]os.FileInfo, error)
	RemoveAll(path string) error
	// DockerSystemPrune reclaims unused containers, images, networks, and build
	// cache on the host's Docker daemon -- the moby equivalent of
	// `docker system prune -af`. Returns the total bytes reclaimed and a
	// per-step warning list (a failed individual prune is reported, not fatal).
	DockerSystemPrune(ctx context.Context) (reclaimed uint64, warnings []string, err error)
	// ContainerdPrune reclaims the orphaned containerd "moby"-namespace image
	// store -- the duplicate left when docker runs on overlay2 while containerd
	// still holds moby images that `docker system prune` can't reach. It is
	// store-aware: it skips (Skipped=true) when docker's live store is not
	// overlay2, so it never deletes a live image. dryRun reports candidates
	// without deleting.
	ContainerdPrune(ctx context.Context, dryRun bool) (ContainerdPruneResult, error)
}

// Compile-time proof the SSH transport implements every capability.
var (
	_ ContainerRunner = (*SSHClient)(nil)
	_ DirMeasurer     = (*SSHClient)(nil)
	_ HostConnector   = (*SSHClient)(nil)
	_ RemoteHost      = (*sshConn)(nil)
)
