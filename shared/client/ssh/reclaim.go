// -------------------------------------------------------------------------------
// Shared Reclaim Core - Dry-run-aware Sweep + Selection Logic
//
// Author: Alex Freidah
//
// The pure, testable heart of the storage-reclamation sweeps (rootfs, buildx
// volumes, containerd images). The live transport lives in the sibling
// docker/containerd/rootfs/buildx files; this file holds only logic that runs
// without a socket -- which candidates to remove and how a dry-run-aware sweep
// tallies them -- so it is unit-tested directly rather than behind a fake.
// -------------------------------------------------------------------------------

package ssh

import (
	"fmt"
	"os"
	"strings"
)

// ReclaimResult reports the outcome of a dry-run-aware storage sweep: candidates
// each removed (or, in dry-run, tallied) with best-effort byte accounting. A
// per-item removal failure is a warning, not fatal. Shared by the sweeps that
// reclaim by whole item (rootfs entries, buildx volumes).
type ReclaimResult struct {
	Reclaimed   uint64   // bytes freed (real run)
	Reclaimable uint64   // bytes that would be freed (dry run)
	Deleted     int      // items removed (real run)
	Candidates  int      // items that would be removed (dry run)
	Warnings    []string // per-item removal failures (non-fatal)
}

// pruneCandidate is one removable item with its size in bytes.
type pruneCandidate struct {
	id   string
	size int64
}

// sweep applies remove to each candidate unless dryRun. In dry-run it tallies
// Candidates and Reclaimable bytes; otherwise it tallies Deleted and Reclaimed
// bytes, turning a per-item removal failure into a warning rather than aborting
// the rest.
func sweep(candidates []pruneCandidate, dryRun bool, remove func(id string) error) ReclaimResult {
	var r ReclaimResult
	for _, c := range candidates {
		if dryRun {
			r.Candidates++
			r.Reclaimable += uint64(c.size)
			continue
		}
		if err := remove(c.id); err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("remove %s: %v", c.id, err))
			continue
		}
		r.Deleted++
		r.Reclaimed += uint64(c.size)
	}
	return r
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

// isBuildxStateVolume reports whether name is a buildx builder-state volume
// (buildx_buildkit_<builder>_state) -- a belt-and-suspenders check beyond the
// name filter so only builder cache, never an unrelated volume, is removed.
func isBuildxStateVolume(name string) bool {
	return strings.HasPrefix(name, buildxVolumePrefix) && strings.HasSuffix(name, buildxVolumeSuffix)
}

// containerdStoreIsSafe reports whether it is safe to prune the containerd moby
// image store given docker's active storage driver. It is safe only when
// docker's live store is overlay2; any other driver (e.g. overlayfs) means
// containerd IS the live image store and pruning it would delete live images.
func containerdStoreIsSafe(dockerStorageDriver string) (safe bool, reason string) {
	if dockerStorageDriver == "overlay2" {
		return true, ""
	}
	return false, fmt.Sprintf("docker storage driver is %q (containerd is the live image store)", dockerStorageDriver)
}
