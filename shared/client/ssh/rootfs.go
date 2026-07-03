// -------------------------------------------------------------------------------
// Shared Docker rootfs Sweep - Orphaned Container-rootfs Reclamation
//
// Author: Alex Freidah
//
// Reclaims stale entries under /var/lib/docker/rootfs/overlayfs left behind when
// a node's docker storage layout changed (e.g. a move to the containerd image
// store): full container root filesystems the current daemon no longer mounts.
// `docker system prune` never reaches them -- they belong to no image and no
// live container as far as the daemon is concerned.
//
// Detection is over SFTP only, no remote shell: list the entries, read the host
// mount table (/proc/self/mountinfo), and delete only entries whose path appears
// nowhere in it (see rootfsOrphans in reclaim.go). Anything currently mounted is
// kept, so a live container's rootfs is never touched.
// -------------------------------------------------------------------------------

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	rootfsOverlayDir = "/var/lib/docker/rootfs/overlayfs"
	hostMountInfo    = "/proc/self/mountinfo"
)

// RootfsPrune removes entries under /var/lib/docker/rootfs/overlayfs that are
// not referenced by any live mount. When the directory is absent (the common
// case -- only nodes that migrated storage layout have it) it is a no-op.
// dryRun reports candidates without deleting.
func (s *sshConn) RootfsPrune(ctx context.Context, dryRun bool) (ReclaimResult, error) {
	entries, err := s.ReadDir(rootfsOverlayDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReclaimResult{}, nil
		}
		return ReclaimResult{}, fmt.Errorf("list %s: %w", rootfsOverlayDir, err)
	}

	mounts, err := s.readFile(hostMountInfo)
	if err != nil {
		return ReclaimResult{}, fmt.Errorf("read host mount table: %w", err)
	}

	var candidates []pruneCandidate
	for _, name := range rootfsOrphans(entries, string(mounts)) {
		path := rootfsOverlayDir + "/" + name
		size, _ := s.DirSize(path) // best-effort accounting
		candidates = append(candidates, pruneCandidate{id: path, size: size})
	}
	return sweep(candidates, dryRun, s.RemoveAll), nil
}

// readFile reads path over SFTP. It uses a sequential read (not WriteTo) so it
// works for /proc files, which report size 0 but stream their content.
func (s *sshConn) readFile(path string) ([]byte, error) {
	c, err := s.sftpClient()
	if err != nil {
		return nil, err
	}
	f, err := c.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
