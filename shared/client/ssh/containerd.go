// -------------------------------------------------------------------------------
// Shared Containerd-over-SSH - Orphaned moby-store Reclamation
//
// Author: Alex Freidah
//
// Drives a remote host's containerd through its gRPC API, tunneled to
// /run/containerd/containerd.sock over the shared SSH client's connection --
// the same StreamLocal-forwarding trick docker.go uses for the Docker socket.
// No ctr/nerdctl binary and no ssh exec are involved.
//
// Its one job is reclaiming the orphaned containerd "moby"-namespace image
// store: on hosts where docker runs on overlay2, containerd can still hold a
// duplicate copy of the same images that `docker system prune` never reaches.
// This is store-aware -- it refuses to touch containerd when containerd is
// docker's live image store (driver != overlay2), so it never deletes a live
// image. The socket is reached as the SSH login user (root everywhere here),
// which owns the root-only containerd socket directly.
// -------------------------------------------------------------------------------

package ssh

import (
	"context"
	"fmt"
	"net"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	remoteContainerdSocket = "/run/containerd/containerd.sock"
	// mobyNamespace is containerd's namespace for docker-managed images.
	mobyNamespace = "moby"
	// containerdDataDir is measured before/after to account reclaimed bytes;
	// containerd's delete API reports no space figure of its own.
	containerdDataDir = "/var/lib/containerd"
)

// ContainerdPruneResult reports the outcome of a containerd moby-store prune.
type ContainerdPruneResult struct {
	Reclaimed  uint64   // bytes freed from the containerd data dir
	Skipped    bool     // true when the store-aware gate declined to prune
	Reason     string   // why it was skipped (empty when it ran)
	Deleted    int      // images deleted (real run)
	Candidates int      // images that would be deleted (dry run)
	Warnings   []string // per-image delete failures (non-fatal)
}

// containerdClient returns a containerd API client tunneled to this
// connection's host daemon over the SSH connection, defaulting to namespace.
// The dial options replace containerd's defaults, so they must carry both the
// SSH-tunneling dialer and transport credentials; New still adds the
// namespace-propagating interceptors because a default namespace is set.
func (s *sshConn) containerdClient(namespace string) (*containerd.Client, error) {
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return s.client.DialContext(ctx, "unix", remoteContainerdSocket)
		}),
	}
	cli, err := containerd.New(
		remoteContainerdSocket,
		containerd.WithDefaultNamespace(namespace),
		containerd.WithDialOpts(dialOpts),
		containerd.WithTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("create containerd client: %w", err)
	}
	return cli, nil
}

// ContainerdPrune reclaims the orphaned containerd moby-namespace image store on
// the connection's host. It first reads docker's active storage driver through
// the tunneled Docker API: unless that driver is overlay2 (meaning containerd is
// NOT docker's live image store), it skips rather than risk deleting live
// images. Otherwise it deletes every moby-namespace image not backing an
// existing container -- the synchronous delete triggers containerd GC of the
// now-unreferenced content and snapshots. dryRun reports candidates without
// deleting. A per-image delete failure is a warning, not fatal; only failing to
// reach a daemon is returned as an error.
func (s *sshConn) ContainerdPrune(ctx context.Context, dryRun bool) (ContainerdPruneResult, error) {
	driver, err := s.dockerStorageDriver(ctx)
	if err != nil {
		return ContainerdPruneResult{}, err
	}
	if safe, reason := containerdStoreIsSafe(driver); !safe {
		return ContainerdPruneResult{Skipped: true, Reason: reason}, nil
	}

	cli, err := s.containerdClient(mobyNamespace)
	if err != nil {
		return ContainerdPruneResult{}, err
	}
	defer func() { _ = cli.Close() }()

	referenced, err := referencedImages(ctx, cli)
	if err != nil {
		return ContainerdPruneResult{}, err
	}

	imgs, err := cli.ListImages(ctx)
	if err != nil {
		return ContainerdPruneResult{}, fmt.Errorf("list containerd images: %w", err)
	}

	var candidates []string
	for _, img := range imgs {
		if _, keep := referenced[img.Name()]; !keep {
			candidates = append(candidates, img.Name())
		}
	}

	if dryRun {
		return ContainerdPruneResult{Candidates: len(candidates)}, nil
	}

	before, _ := s.DirSize(containerdDataDir) // best-effort size accounting
	var result ContainerdPruneResult
	is := cli.ImageService()
	for _, name := range candidates {
		if derr := is.Delete(ctx, name, images.SynchronousDelete()); derr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("delete %s: %v", name, derr))
			continue
		}
		result.Deleted++
	}
	after, _ := s.DirSize(containerdDataDir)
	if before > after {
		result.Reclaimed = uint64(before - after)
	}
	return result, nil
}

// dockerStorageDriver returns the docker daemon's active storage driver (e.g.
// "overlay2" or "overlayfs") via the tunneled Docker API.
func (s *sshConn) dockerStorageDriver(ctx context.Context) (string, error) {
	cli, err := s.dockerClient()
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()

	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}
	return info.Info.Driver, nil
}

// referencedImages returns the set of image refs backing an existing container
// in the client's namespace -- these must never be deleted.
func referencedImages(ctx context.Context, cli *containerd.Client) (map[string]struct{}, error) {
	containers, err := cli.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containerd containers: %w", err)
	}
	refs := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		info, ierr := c.Info(ctx)
		if ierr != nil {
			return nil, fmt.Errorf("containerd container info: %w", ierr)
		}
		if info.Image != "" {
			refs[info.Image] = struct{}{}
		}
	}
	return refs, nil
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
