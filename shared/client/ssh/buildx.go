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

	"github.com/moby/moby/client"
)

const (
	buildxVolumePrefix = "buildx_buildkit_"
	buildxVolumeSuffix = "_state"
)

// BuildxVolumePrune removes dangling buildx builder-state volumes through the
// tunneled Docker API. dryRun reports candidates without deleting. A per-volume
// removal failure is a warning, not fatal; only failing to reach the daemon is
// returned as an error.
func (s *sshConn) BuildxVolumePrune(ctx context.Context, dryRun bool) (ReclaimResult, error) {
	cli, err := s.dockerClient()
	if err != nil {
		return ReclaimResult{}, err
	}
	defer func() { _ = cli.Close() }()

	// dangling=true restricts to volumes referenced by no container; the name
	// filter narrows to buildx builder-state volumes.
	list, err := cli.VolumeList(ctx, client.VolumeListOptions{
		Filters: client.Filters{}.Add("dangling", "true").Add("name", buildxVolumePrefix),
	})
	if err != nil {
		return ReclaimResult{}, fmt.Errorf("list volumes: %w", err)
	}

	var candidates []pruneCandidate
	for _, v := range list.Items {
		if !isBuildxStateVolume(v.Name) {
			continue
		}
		size, _ := s.DirSize(v.Mountpoint) // best-effort accounting
		candidates = append(candidates, pruneCandidate{id: v.Name, size: size})
	}
	return sweep(candidates, dryRun, func(name string) error {
		_, derr := cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
		return derr
	}), nil
}
