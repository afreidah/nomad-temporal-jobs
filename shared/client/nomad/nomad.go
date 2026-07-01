// -------------------------------------------------------------------------------
// Shared Nomad Client - Instrumented Nomad API Client Factory
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Creates Nomad API clients with OTel-instrumented HTTP transport so that
// Nomad API calls appear as edges in the Tempo service graph. Used by
// trivyscan and nodecleanup workflows.
// -------------------------------------------------------------------------------

package nomad

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"munchbox/temporal-workers/shared"
)

// NewNomadClient creates a configured Nomad API client with OTel-instrumented
// HTTP transport so calls appear as edges in the service graph.
func NewNomadClient() (*api.Client, error) {
	nomadAddr := os.Getenv("NOMAD_ADDR")
	if nomadAddr == "" {
		nomadAddr = "https://nomad.service.consul:4646"
	}

	config := api.DefaultConfig()
	config.Address = nomadAddr

	if token := os.Getenv("NOMAD_TOKEN"); token != "" {
		config.SecretID = token
	}
	if caCert := os.Getenv("NOMAD_CACERT"); caCert != "" {
		config.TLSConfig.CACert = caCert
	}

	config.HttpClient = &http.Client{Transport: shared.OTelTransport("nomad", nil)}

	return api.NewClient(config)
}

// ScaleNomadJob scales a job's task group to count. Idempotent -- Nomad
// accepts the call when the job is already at the requested count. The error
// is returned verbatim so callers can classify it (e.g. job-not-found as
// non-retryable).
func ScaleNomadJob(ctx context.Context, client *api.Client, jobName, groupName string, count int, reason string) error {
	c := count
	if _, _, err := client.Jobs().Scale(jobName, groupName, &c, reason, false, nil, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return fmt.Errorf("scale %s/%s to %d: %w", jobName, groupName, count, err)
	}
	return nil
}

// WaitNomadAllocCount polls until the job's running-allocation count meets
// target -- target 0 succeeds when running drops to 0, target>=1 succeeds when
// running is at least target. onPoll (may be nil) is called each poll with the
// current running count, for heartbeats/logging. Returns ctx.Err() if the
// context ends first. Transient list errors are skipped and retried.
func WaitNomadAllocCount(ctx context.Context, client *api.Client, jobName string, target int, interval time.Duration, onPoll func(running int)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if allocs, _, err := client.Jobs().Allocations(jobName, false, nil); err == nil {
			running := len(RunningAllocStubs(allocs))
			if onPoll != nil {
				onPoll(running)
			}
			if (target == 0 && running == 0) || (target > 0 && running >= target) {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// RunningAllocStubs returns the allocation stubs whose client status is
// running, centralizing the running-status filter on the
// api.AllocClientStatusRunning constant instead of a bare "running" literal.
func RunningAllocStubs(allocs []*api.AllocationListStub) []*api.AllocationListStub {
	running := make([]*api.AllocationListStub, 0, len(allocs))
	for _, al := range allocs {
		if al.ClientStatus == api.AllocClientStatusRunning {
			running = append(running, al)
		}
	}
	return running
}

// NodeSSHAddress returns the best SSH-dialable address for a Nomad node: its
// unique.network.ip-address attribute, falling back to HTTPAddr with the port
// stripped.
func NodeSSHAddress(node *api.Node) string {
	if addr := node.Attributes["unique.network.ip-address"]; addr != "" {
		return addr
	}
	addr := node.HTTPAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		addr = addr[:idx]
	}
	return addr
}

// NomadNode is the neutral subset of a Nomad client node workers need for SSH
// dialing. Worker-specific classification (e.g. nodecleanup's IsOracle) is
// derived by the worker from these fields.
type NomadNode struct {
	ID       string
	Name     string
	Address  string
	HTTPAddr string
}

// ErrNoRunningAlloc is returned by Nomad.FindJobNode when a job has no running
// allocation. Callers that want fail-fast behavior can match it (e.g. wrap it
// as a non-retryable Temporal error).
var ErrNoRunningAlloc = errors.New("no running allocation for job")

// Nomad wraps the instrumented Nomad API client with the operations workers
// perform. Workers consume it through their own narrow interfaces (accept
// interfaces, return structs); a worker needing more adds a method here and
// widens only its own interface.
type Nomad struct {
	client *api.Client
}

// NewNomad builds a Nomad service over an OTel-instrumented client.
func NewNomad() (*Nomad, error) {
	client, err := NewNomadClient()
	if err != nil {
		return nil, err
	}
	return &Nomad{client: client}, nil
}

// RunningImages returns the unique Docker images across running allocations,
// sorted for deterministic scan order. Allocations whose info can't be fetched
// are skipped (best-effort discovery).
func (n *Nomad) RunningImages(ctx context.Context) ([]string, error) {
	allocs, _, err := n.client.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list allocations: %w", err)
	}
	imageSet := make(map[string]struct{})
	for _, stub := range RunningAllocStubs(allocs) {
		alloc, _, err := n.client.Allocations().Info(stub.ID, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil || alloc.Job == nil {
			continue
		}
		collectDockerImages(alloc.Job, imageSet)
	}
	return slices.Sorted(maps.Keys(imageSet)), nil
}

// collectDockerImages adds the image of every docker-driver task in job to set.
func collectDockerImages(job *api.Job, set map[string]struct{}) {
	for _, tg := range job.TaskGroups {
		for _, task := range tg.Tasks {
			if task.Driver != "docker" || task.Config == nil {
				continue
			}
			if img, ok := task.Config["image"].(string); ok && img != "" {
				set[img] = struct{}{}
			}
		}
	}
}

// ClientNodes returns the ready Nomad client nodes with SSH-dialable addresses.
// Nodes that aren't ready, or whose info can't be fetched, are skipped.
func (n *Nomad) ClientNodes(ctx context.Context) ([]NomadNode, error) {
	stubs, _, err := n.client.Nodes().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var nodes []NomadNode
	for _, s := range stubs {
		if s.Status != api.NodeStatusReady {
			continue
		}
		node, _, err := n.client.Nodes().Info(s.ID, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			continue
		}
		nodes = append(nodes, NomadNode{
			ID:       s.ID,
			Name:     s.Name,
			Address:  NodeSSHAddress(node),
			HTTPAddr: node.HTTPAddr,
		})
	}
	return nodes, nil
}

// RunningJobIDs returns the set of job IDs with a running allocation on nodeID.
func (n *Nomad) RunningJobIDs(ctx context.Context, nodeID string) (map[string]struct{}, error) {
	allocs, _, err := n.client.Nodes().Allocations(nodeID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list allocations for node %s: %w", nodeID, err)
	}
	running := make(map[string]struct{})
	for _, al := range allocs {
		if al.ClientStatus == api.AllocClientStatusRunning {
			running[al.JobID] = struct{}{}
		}
	}
	return running, nil
}

// FindJobNode returns the node running the named job's first running alloc
// (single-alloc jobs have one). Returns ErrNoRunningAlloc when none is running.
func (n *Nomad) FindJobNode(ctx context.Context, jobName string) (NomadNode, error) {
	allocs, _, err := n.client.Jobs().Allocations(jobName, false, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return NomadNode{}, fmt.Errorf("list allocs for %q: %w", jobName, err)
	}
	running := RunningAllocStubs(allocs)
	if len(running) == 0 {
		return NomadNode{}, fmt.Errorf("%w: %s", ErrNoRunningAlloc, jobName)
	}
	node, _, err := n.client.Nodes().Info(running[0].NodeID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return NomadNode{}, fmt.Errorf("get node info: %w", err)
	}
	return NomadNode{
		ID:       running[0].NodeID,
		Name:     node.Name,
		Address:  NodeSSHAddress(node),
		HTTPAddr: node.HTTPAddr,
	}, nil
}

// ScaleJob scales the named job's task group to count. The error is returned
// verbatim so callers can classify it (see IsJobNotFound).
func (n *Nomad) ScaleJob(ctx context.Context, jobName, groupName string, count int, reason string) error {
	return ScaleNomadJob(ctx, n.client, jobName, groupName, count, reason)
}

// WaitAllocCount polls until the job's running-allocation count meets target
// (see WaitNomadAllocCount).
func (n *Nomad) WaitAllocCount(ctx context.Context, jobName string, target int, interval time.Duration, onPoll func(running int)) error {
	return WaitNomadAllocCount(ctx, n.client, jobName, target, interval, onPoll)
}

// DispatchJob dispatches a parameterized job, returning the concrete dispatched
// job ID (e.g. "ci-runner/dispatch-1700000000-abcd") so callers can later target
// or reap that exact instance. Each call creates a new instance, so dispatch is
// not idempotent -- callers that must avoid duplicates should not retry blindly.
func (n *Nomad) DispatchJob(ctx context.Context, jobID string, meta map[string]string) (string, error) {
	resp, _, err := n.client.Jobs().Dispatch(jobID, meta, nil, "", (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("dispatch job %q: %w", jobID, err)
	}
	return resp.DispatchedJobID, nil
}

// StopJob stops and purges the named job, removing it from Nomad state. Used to
// reap an ephemeral dispatched runner. Idempotent enough for a reaper: a
// not-found job is reported via IsJobNotFound so callers can treat it as already
// gone.
func (n *Nomad) StopJob(ctx context.Context, jobID string) error {
	if _, _, err := n.client.Jobs().Deregister(jobID, true, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return fmt.Errorf("deregister job %q: %w", jobID, err)
	}
	return nil
}

// RunnerSlot is the dispatch identity of one active ephemeral runner: the repo
// it registered against and the labels it carries, both read back from the
// dispatched job's meta. The scaler buckets these to reconcile how many more
// runners each (repo, labels) still needs.
type RunnerSlot struct {
	RepoURL string
	Labels  string
}

// ActiveRunnerSlots returns the dispatch identity of every active (pending or
// running) dispatched child of parentJobID. It lists allocations, keeps those
// whose job is a dispatched child still occupying a slot, and reads each child
// job's repo_url + labels meta. A child whose job info can't be fetched (already
// garbage-collected) is skipped -- it no longer occupies a slot. Ephemeral
// runners aren't bound to a specific job, so the caller reconciles by counting
// these against queued jobs rather than tracking a runner per job_id.
func (n *Nomad) ActiveRunnerSlots(ctx context.Context, parentJobID string) ([]RunnerSlot, error) {
	allocs, _, err := n.client.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list allocations: %w", err)
	}
	prefix := parentJobID + "/dispatch-"
	active := make(map[string]struct{})
	for _, al := range allocs {
		if !strings.HasPrefix(al.JobID, prefix) {
			continue
		}
		if al.ClientStatus == api.AllocClientStatusPending || al.ClientStatus == api.AllocClientStatusRunning {
			active[al.JobID] = struct{}{}
		}
	}
	slots := make([]RunnerSlot, 0, len(active))
	for jobID := range active {
		job, _, err := n.client.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			continue
		}
		slots = append(slots, RunnerSlot{
			RepoURL: metaString(job.Meta, "repo_url"),
			Labels:  metaString(job.Meta, "labels"),
		})
	}
	return slots, nil
}

// metaString reads key from a possibly-nil job meta map.
func metaString(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}

// IsJobNotFound reports whether err indicates a Nomad job does not exist (the
// API returns HTTP 404 / "job not found"). It prefers the typed
// api.UnexpectedResponseError status code and falls back to string matching,
// so retry-gating lives in one audited place rather than inline substring checks.
func IsJobNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ue api.UnexpectedResponseError
	if errors.As(err, &ue) && ue.StatusCode() == http.StatusNotFound {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "job not found") || strings.Contains(msg, "404")
}
