// -------------------------------------------------------------------------------
// Shared Docker-over-SSH - Remote Docker API via the SSH Tunnel
//
// Author: Alex Freidah
//
// Drives a remote host's Docker daemon through the Docker API, tunneled to its
// /var/run/docker.sock over the shared SSH client's connection (StreamLocal
// forwarding). No docker CLI and no ssh binary are involved -- the moby client
// speaks the Docker API directly over the forwarded socket, reusing the same
// authenticated SSH transport the rest of the worker uses. Any job that needs
// to run a container on a remote node uses RunContainer.
//
// The socket is reached as the SSH login user, with no sudo (you cannot sudo a
// forwarded socket). The user must have docker access on the host -- own the
// socket or be in the docker group. SSHTarget.Sudo does not apply to this path.
// -------------------------------------------------------------------------------

package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"munchbox/temporal-workers/shared"
)

const remoteDockerSocket = "/var/run/docker.sock"

// dockerClient returns a Docker API client tunneled to this connection's host
// daemon over the SSH connection. The caller closes the returned client;
// closing the connection tears down the underlying transport. Internal: callers
// use the higher-level RunContainer / DockerSystemPrune instead.
func (s *sshConn) dockerClient() (*client.Client, error) {
	// The dialer ignores the requested network/addr and instead dials the
	// remote unix socket through the SSH connection.
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return s.client.DialContext(ctx, "unix", remoteDockerSocket)
	}

	cli, err := client.New(
		client.WithHost("unix://"+remoteDockerSocket),
		client.WithDialContext(dial),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return cli, nil
}

// DockerSystemPrune reclaims unused containers, images, networks, and build
// cache on the connection's host daemon -- the moby equivalent of
// `docker system prune -af`. It returns the total bytes reclaimed and a
// per-step warning list; an individual prune that fails is reported as a
// warning rather than aborting the rest. Only a failure to reach the daemon is
// returned as an error.
func (s *sshConn) DockerSystemPrune(ctx context.Context) (uint64, []string, error) {
	cli, err := s.dockerClient()
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = cli.Close() }()

	var reclaimed uint64
	var warnings []string

	if cp, perr := cli.ContainerPrune(ctx, client.ContainerPruneOptions{}); perr != nil {
		warnings = append(warnings, fmt.Sprintf("container prune: %v", perr))
	} else {
		reclaimed += cp.Report.SpaceReclaimed
	}
	// dangling=false prunes all unused images, matching `prune -a`.
	if ip, perr := cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: client.Filters{}.Add("dangling", "false")}); perr != nil {
		warnings = append(warnings, fmt.Sprintf("image prune: %v", perr))
	} else {
		reclaimed += ip.Report.SpaceReclaimed
	}
	if _, perr := cli.NetworkPrune(ctx, client.NetworkPruneOptions{}); perr != nil {
		warnings = append(warnings, fmt.Sprintf("network prune: %v", perr))
	}
	if bp, perr := cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: true}); perr != nil {
		warnings = append(warnings, fmt.Sprintf("build cache prune: %v", perr))
	} else {
		reclaimed += bp.Report.SpaceReclaimed
	}
	return reclaimed, warnings, nil
}

// RunContainer runs a one-shot container on t's Docker daemon (tunneled over
// SSH), waits for it to exit while heartbeating, and returns its combined
// stdout+stderr. It returns an error if the container exits non-zero. The image
// is not pulled, so it must already be present on the host. binds are
// host:container volume bindings; heartbeat is the activity-heartbeat interval
// while the container runs.
func (c *SSHClient) RunContainer(ctx context.Context, t SSHTarget, cfg *container.Config, binds []string, heartbeat time.Duration) (string, error) {
	conn, err := c.connect(t)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	cli, err := conn.dockerClient()
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()

	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: &container.HostConfig{Binds: binds},
	})
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	// Remove the container even if ctx is cancelled mid-run.
	defer func() {
		_, _ = cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	}()

	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}

	// Wait for the container to exit, heartbeating so a long run trips the
	// activity's HeartbeatTimeout rather than the StartToClose timeout.
	wait := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	var statusCode int64
	if _, werr := shared.WithHeartbeat(ctx, heartbeat, func() (struct{}, error) {
		select {
		case e := <-wait.Error:
			return struct{}{}, e
		case r := <-wait.Result:
			statusCode = r.StatusCode
			return struct{}{}, nil
		}
	}); werr != nil {
		return "", fmt.Errorf("wait for container: %w", werr)
	}

	logs, err := cli.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("read container logs: %w", err)
	}
	defer func() { _ = logs.Close() }()

	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, logs); err != nil {
		return out.String(), fmt.Errorf("demux container logs: %w", err)
	}
	output := out.String()

	if statusCode != 0 {
		return output, fmt.Errorf("container exited %d: %s", statusCode, strings.TrimSpace(output))
	}
	return output, nil
}
