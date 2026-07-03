// -------------------------------------------------------------------------------
// Shared Buildx Volume Sweep - Orphaned Builder-cache Reclamation
//
// Author: Alex Freidah
//
// Reclaims the docker-container buildx builder cache. A builder created with the
// docker-container driver keeps its BuildKit state in a volume named
// buildx_buildkit_<builder>_state. When the builder is removed (`docker buildx
// rm`) the container goes away but the volume is left behind, dangling and often
// many GB. `docker system prune` never removes volumes, so it accumulates.
//
// This drives the Docker volume API tunneled over the SSH connection: it lists
// dangling volumes (referenced by no container) whose name is a buildx builder-
// state volume and removes them. It only ever touches unreferenced builder-state
// volumes -- never a volume in use, never a blanket volume prune -- so no live
// builder or application data is at risk.
// -------------------------------------------------------------------------------

package ssh

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"
)

const (
	buildxVolumePrefix = "buildx_buildkit_"
	buildxVolumeSuffix = "_state"
)

// BuildxPruneResult reports the outcome of a buildx builder-cache volume sweep.
type BuildxPruneResult struct {
	Reclaimed   uint64   // bytes freed (real run)
	Reclaimable uint64   // bytes that would be freed (dry run)
	Deleted     int      // volumes removed (real run)
	Candidates  int      // volumes that would be removed (dry run)
	Warnings    []string // per-volume removal failures (non-fatal)
}

// BuildxVolumePrune removes dangling buildx builder-state volumes through the
// tunneled Docker API. dryRun reports candidates without deleting. A per-volume
// removal failure is a warning, not fatal; only failing to reach the daemon is
// returned as an error.
func (s *sshConn) BuildxVolumePrune(ctx context.Context, dryRun bool) (BuildxPruneResult, error) {
	cli, err := s.dockerClient()
	if err != nil {
		return BuildxPruneResult{}, err
	}
	defer func() { _ = cli.Close() }()

	// dangling=true restricts to volumes referenced by no container; the name
	// filter narrows to buildx builder-state volumes.
	list, err := cli.VolumeList(ctx, client.VolumeListOptions{
		Filters: client.Filters{}.Add("dangling", "true").Add("name", buildxVolumePrefix),
	})
	if err != nil {
		return BuildxPruneResult{}, fmt.Errorf("list volumes: %w", err)
	}

	var result BuildxPruneResult
	for _, v := range list.Items {
		if !isBuildxStateVolume(v.Name) {
			continue
		}
		size, _ := s.DirSize(v.Mountpoint) // best-effort accounting
		if dryRun {
			result.Candidates++
			result.Reclaimable += uint64(size)
			continue
		}
		if _, derr := cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{}); derr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("remove %s: %v", v.Name, derr))
			continue
		}
		result.Deleted++
		result.Reclaimed += uint64(size)
	}
	return result, nil
}

// isBuildxStateVolume reports whether name is a buildx builder-state volume
// (buildx_buildkit_<builder>_state) -- a belt-and-suspenders check beyond the
// name filter so only builder cache, never an unrelated volume, is removed.
func isBuildxStateVolume(name string) bool {
	return strings.HasPrefix(name, buildxVolumePrefix) && strings.HasSuffix(name, buildxVolumeSuffix)
}
