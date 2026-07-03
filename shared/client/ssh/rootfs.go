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
// Detection is over SFTP only, no remote shell: list the entries, read the
// host mount table (/proc/self/mountinfo), and delete only entries whose path
// appears nowhere in it. Anything currently mounted (as a target, upper, lower,
// or work dir) is kept, so a live container's rootfs is never touched.
// -------------------------------------------------------------------------------

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	rootfsOverlayDir = "/var/lib/docker/rootfs/overlayfs"
	hostMountInfo    = "/proc/self/mountinfo"
)

// RootfsPruneResult reports the outcome of an orphaned-rootfs sweep.
type RootfsPruneResult struct {
	Reclaimed   uint64   // bytes freed (real run)
	Reclaimable uint64   // bytes that would be freed (dry run)
	Deleted     int      // entries removed (real run)
	Candidates  int      // entries that would be removed (dry run)
	Warnings    []string // per-entry removal failures (non-fatal)
}

// RootfsPrune removes entries under /var/lib/docker/rootfs/overlayfs that are
// not referenced by any live mount. When the directory is absent (the common
// case -- only nodes that migrated storage layout have it) it is a no-op.
// dryRun reports candidates without deleting. A per-entry removal failure is a
// warning, not fatal.
func (s *sshConn) RootfsPrune(ctx context.Context, dryRun bool) (RootfsPruneResult, error) {
	entries, err := s.ReadDir(rootfsOverlayDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RootfsPruneResult{}, nil
		}
		return RootfsPruneResult{}, fmt.Errorf("list %s: %w", rootfsOverlayDir, err)
	}

	mounts, err := s.readFile(hostMountInfo)
	if err != nil {
		return RootfsPruneResult{}, fmt.Errorf("read host mount table: %w", err)
	}
	orphans := rootfsOrphans(entries, string(mounts))

	var result RootfsPruneResult
	for _, name := range orphans {
		path := rootfsOverlayDir + "/" + name
		size, _ := s.DirSize(path) // best-effort accounting
		if dryRun {
			result.Candidates++
			result.Reclaimable += uint64(size)
			continue
		}
		if derr := s.RemoveAll(path); derr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("remove %s: %v", name, derr))
			continue
		}
		result.Deleted++
		result.Reclaimed += uint64(size)
	}
	return result, nil
}

// rootfsOrphans returns the names of directory entries whose full path does not
// appear anywhere in the mount table -- i.e. nothing is mounted at, from, or
// under them. Conservative on purpose: any path still referenced by a mount
// (target/upper/lower/work dir) is kept.
func rootfsOrphans(entries []os.FileInfo, mountInfo string) []string {
	var orphans []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.Contains(mountInfo, rootfsOverlayDir+"/"+e.Name()) {
			continue
		}
		orphans = append(orphans, e.Name())
	}
	return orphans
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
