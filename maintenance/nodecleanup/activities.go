// -------------------------------------------------------------------------------
// Node Cleanup Activities - Orphaned Data Directory Removal via SSH
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Implements Temporal activities for discovering Nomad client nodes and
// cleaning up orphaned job data directories via SSH. Orphaned directories
// accumulate when Nomad jobs move between nodes and their ephemeral data
// is left behind. Safety features include dry-run mode, a configurable
// grace period, system directory exclusion, and protection of every
// directory a live allocation on the node mounts.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"

	"munchbox/temporal-workers/shared/client/nomad"
	"munchbox/temporal-workers/shared/client/ssh"

	"go.temporal.io/sdk/activity"
)

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// nomadClient is this worker's view of nomad.Nomad -- the node and alloc
// operations the cleanup activities call. *nomad.Nomad satisfies it
// structurally.
type nomadClient interface {
	ClientNodes(ctx context.Context) ([]nomad.NomadNode, error)
	RunningWorkloads(ctx context.Context, nodeID string) (nomad.NodeWorkloads, error)
}

// Activities holds shared dependencies for node cleanup activities. Register
// an instance with the Temporal worker to expose all exported methods as
// activity implementations.
type Activities struct {
	nomad nomadClient
	host  ssh.HostConnector
}

// New creates an Activities instance over the shared Nomad client and a remote-
// host connector (reused across activity invocations rather than rebuilt per
// call).
func New(nomad nomadClient, host ssh.HostConnector) *Activities {
	return &Activities{nomad: nomad, host: host}
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// CleanupConfig holds workflow-level configuration passed as input.
type CleanupConfig struct {
	DataDir           string `json:"data_dir"`            // Base directory to scan (default: /opt/nomad/data)
	GraceDays         int    `json:"grace_days"`          // Only delete directories older than this (default: 7)
	Concurrency       int    `json:"concurrency"`         // Max nodes cleaned in parallel (default: 4)
	DryRun            bool   `json:"dry_run"`             // If true, only report what would be deleted
	DockerPrune       bool   `json:"docker_prune"`        // If true, also prune unused Docker images
	ContainerdPrune   bool   `json:"containerd_prune"`    // If true, also reclaim the orphaned containerd moby image store
	RootfsPrune       bool   `json:"rootfs_prune"`        // If true, also sweep orphaned /var/lib/docker/rootfs entries
	BuildxVolumePrune bool   `json:"buildx_volume_prune"` // If true, also remove dangling buildx builder-state volumes
}

// CleanupResult holds the outcome of a cleanup operation on a single node.
type CleanupResult struct {
	NodeName             string   `json:"node_name"`
	NodeAddr             string   `json:"node_addr"`
	Scanned              int      `json:"scanned"`
	Orphaned             int      `json:"orphaned"`
	Deleted              int      `json:"deleted"`
	Skipped              int      `json:"skipped"`
	DockerSpaceFreed     string   `json:"docker_space_freed"`
	ContainerdSpaceFreed string   `json:"containerd_space_freed"`
	ContainerdSkipped    bool     `json:"containerd_skipped,omitempty"`
	ContainerdSkipReason string   `json:"containerd_skip_reason,omitempty"`
	RootfsSpaceFreed     string   `json:"rootfs_space_freed"`
	BuildxSpaceFreed     string   `json:"buildx_space_freed"`
	Errors               []string `json:"errors,omitempty"`
	Output               string   `json:"output"`
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// GetAllNomadClientNodes retrieves all ready Nomad client nodes with their
// SSH addresses and node metadata. Creates a client span to Nomad for
// service graph visibility.
func (a *Activities) GetAllNomadClientNodes(ctx context.Context) ([]nodes.NodeInfo, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Retrieving all Nomad client nodes")

	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.list_nodes")
	defer span.End()

	found, err := a.nomad.ClientNodes(ctx)
	if err != nil {
		return nil, err
	}

	infos := make([]nodes.NodeInfo, 0, len(found))
	for _, n := range found {
		infos = append(infos, nodes.NodeInfo{
			ID:       n.ID,
			Name:     n.Name,
			Address:  n.Address,
			HTTPAddr: n.HTTPAddr,
			IsOracle: strings.HasPrefix(n.Name, "oracle"),
		})
	}

	logger.Info("Found client nodes", "count", len(infos))
	return infos, nil
}

// CleanupNodeViaSSH removes orphaned Nomad job data directories on a node. The
// node's live jobs and the host paths they mount come from the Nomad API (so no
// token is shipped to the node), and failing to read them aborts the node
// before anything is deleted; the directory scan and deletions run over SSH
// because the data lives on the node's disk and Nomad exposes no API for it.
// Dry-run reports what would be deleted without removing anything.
func (a *Activities) CleanupNodeViaSSH(ctx context.Context, node nodes.NodeInfo, config CleanupConfig) (CleanupResult, error) {
	logger := activity.GetLogger(ctx)
	result := CleanupResult{
		NodeName: node.Name, NodeAddr: node.Address,
		DockerSpaceFreed: "0B", ContainerdSpaceFreed: "0B", RootfsSpaceFreed: "0B", BuildxSpaceFreed: "0B",
	}

	workloads, err := a.runningWorkloads(ctx, node.ID)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	active := newActiveDirs(workloads, config.DataDir)

	logger.Info("Connecting to node via SSH", "node", node.Name, "address", node.Address)
	conn, err := a.host.Connect(nodes.Target(node))
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("ssh connect: %v", err))
		return result, err
	}
	defer func() { _ = conn.Close() }()

	entries, err := listDataDirs(conn, config.DataDir)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}

	var out strings.Builder
	out.WriteString(a.sweepDataDirs(ctx, conn, entries, active, config, &result))

	if config.DockerPrune {
		freed, dockerOut := a.dockerPrune(ctx, conn, config.DryRun)
		result.DockerSpaceFreed = freed
		out.WriteString(dockerOut)
	}

	if config.ContainerdPrune {
		space, skipped, reason, note := a.containerdPrune(ctx, conn, config.DryRun)
		result.ContainerdSpaceFreed = space
		result.ContainerdSkipped = skipped
		result.ContainerdSkipReason = reason
		out.WriteString(note)
	}

	if config.RootfsPrune {
		space, note := a.reclaimStep(ctx, "Docker rootfs Cleanup", config.DryRun, func() (ssh.ReclaimResult, error) {
			return conn.RootfsPrune(ctx, config.DryRun)
		})
		result.RootfsSpaceFreed = space
		out.WriteString(note)
	}

	if config.BuildxVolumePrune {
		space, note := a.reclaimStep(ctx, "Buildx Volume Cleanup", config.DryRun, func() (ssh.ReclaimResult, error) {
			return conn.BuildxVolumePrune(ctx, config.DryRun)
		})
		result.BuildxSpaceFreed = space
		out.WriteString(note)
	}

	result.Output = out.String()
	logger.Info("Node cleanup complete",
		"node", node.Name,
		"scanned", result.Scanned,
		"orphaned", result.Orphaned,
		"deleted", result.Deleted,
		"skipped", result.Skipped)
	return result, nil
}

// sweepDataDirs classifies each data-dir entry and deletes the orphans (or, in
// dry-run, reports them), updating the scan counters on result and returning the
// per-entry log. Split out of CleanupNodeViaSSH so that method stays a flat
// sequence of steps rather than nesting the scan loop's switch inside it.
func (a *Activities) sweepDataDirs(ctx context.Context, conn ssh.RemoteHost, entries []dirEntry, active activeDirs, config CleanupConfig, result *CleanupResult) string {
	logger := activity.GetLogger(ctx)
	now := time.Now()
	var out strings.Builder
	for _, e := range entries {
		result.Scanned++
		activity.RecordHeartbeat(ctx, e.name) // progress signal so a long scan trips HeartbeatTimeout, not StartToClose

		action, ageDays := classifyEntry(e, active, config, now)
		switch action {
		case entrySkipExcluded:
			result.Skipped++
		case entryActive:
			fmt.Fprintf(&out, "OK (active): %s\n", e.name)
		case entryWithinGrace:
			fmt.Fprintf(&out, "SKIP (%dd old, grace=%dd): %s\n", ageDays, config.GraceDays, e.name)
			result.Skipped++
		case entryOrphan:
			result.Orphaned++
			if config.DryRun {
				fmt.Fprintf(&out, "WOULD DELETE (%dd old): %s\n", ageDays, e.name)
				continue
			}
			path := strings.TrimRight(config.DataDir, "/") + "/" + e.name
			if derr := conn.RemoveAll(path); derr != nil {
				logger.Warn("Failed to delete orphan dir", "dir", e.name, "error", derr)
				result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", e.name, derr))
				continue
			}
			fmt.Fprintf(&out, "DELETED (%dd old): %s\n", ageDays, e.name)
			result.Deleted++
		}
	}
	return out.String()
}

// orphanExcludes are the Nomad data subdirectories that are never cleanup
// candidates -- they hold live runtime state, not per-job ephemeral data.
var orphanExcludes = map[string]struct{}{
	"alloc": {}, "plugins": {}, "tmp": {}, "server": {}, "client": {},
}

// indexSuffix matches a trailing "-<digits>" task-group index on a data dir
// name, so "myjob-2" maps back to the job "myjob".
var indexSuffix = regexp.MustCompile(`-[0-9]*$`)

// dirEntry is one immediate subdirectory of the data dir with its mtime.
type dirEntry struct {
	name  string
	mtime time.Time
}

// runningWorkloads returns the live job IDs on the node and the host paths
// their tasks mount, read from the Nomad API.
func (a *Activities) runningWorkloads(ctx context.Context, nodeID string) (nomad.NodeWorkloads, error) {
	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.node_allocations")
	defer span.End()

	return a.nomad.RunningWorkloads(ctx, nodeID)
}

// interpolationRef matches one ${...} reference in an uninterpolated job-spec
// path, such as ${NOMAD_ALLOC_INDEX}.
var interpolationRef = regexp.MustCompile(`\$\{[^}]*\}`)

// activeDirs answers whether a data-dir entry belongs to a live workload on the
// node: its name maps to a live job ID, or a live alloc mounts a path inside it.
// A data-dir name need not match its job's ID, so the mounts are what protect a
// directory named after something else.
type activeDirs struct {
	jobs     map[string]struct{}
	mounted  map[string]struct{}
	patterns []*regexp.Regexp // mounted names that carry ${...}, matched as wildcards
	all      bool             // a live alloc mounts the data dir itself
}

// newActiveDirs indexes the workloads' host paths by the data-dir entry each
// one falls under. Relative paths (task-local files) and paths outside dataDir
// are ignored. A ${...} reference in the entry name matches any text, since the
// API returns the path uninterpolated -- over-protecting is the safe failure.
func newActiveDirs(w nomad.NodeWorkloads, dataDir string) activeDirs {
	d := activeDirs{jobs: w.JobIDs, mounted: make(map[string]struct{})}
	root := path.Clean(dataDir)
	for _, p := range w.HostPaths {
		if !strings.HasPrefix(p, "/") {
			continue
		}
		p = path.Clean(p)
		if p == root {
			d.all = true
			continue
		}
		rest, ok := strings.CutPrefix(p, root+"/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		if !strings.Contains(name, "${") {
			d.mounted[name] = struct{}{}
			continue
		}
		literals := interpolationRef.Split(name, -1)
		for i, lit := range literals {
			literals[i] = regexp.QuoteMeta(lit)
		}
		d.patterns = append(d.patterns, regexp.MustCompile("^"+strings.Join(literals, ".*")+"$"))
	}
	return d
}

// contains reports whether the data-dir entry name belongs to a live workload.
func (d activeDirs) contains(name string) bool {
	if d.all || isJobRunning(name, d.jobs) {
		return true
	}
	if _, ok := d.mounted[name]; ok {
		return true
	}
	for _, re := range d.patterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// listDataDirs returns the immediate subdirectories of dataDir on the remote
// host with their modification times, over SFTP.
func listDataDirs(conn ssh.RemoteHost, dataDir string) ([]dirEntry, error) {
	infos, err := conn.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("list data dirs in %s: %w", dataDir, err)
	}

	var entries []dirEntry
	for _, fi := range infos {
		if fi.IsDir() {
			entries = append(entries, dirEntry{name: fi.Name(), mtime: fi.ModTime()})
		}
	}
	return entries, nil
}

// isJobRunning reports whether a data dir name (possibly suffixed with a
// task-group index) corresponds to a currently-running job.
func isJobRunning(dirName string, running map[string]struct{}) bool {
	if _, ok := running[dirName]; ok {
		return true
	}
	_, ok := running[indexSuffix.ReplaceAllString(dirName, "")]
	return ok
}

// orphanAction is what CleanupNodeViaSSH should do with one data-dir entry.
type orphanAction int

const (
	entrySkipExcluded orphanAction = iota // a protected Nomad runtime dir
	entryActive                           // belongs to a live workload
	entryWithinGrace                      // orphaned but younger than the grace period
	entryOrphan                           // a deletion candidate
)

// classifyEntry decides what should happen to one data-dir entry and returns
// the action plus the entry's age in days.
func classifyEntry(e dirEntry, active activeDirs, cfg CleanupConfig, now time.Time) (orphanAction, int) {
	if _, excluded := orphanExcludes[e.name]; excluded {
		return entrySkipExcluded, 0
	}
	if active.contains(e.name) {
		return entryActive, 0
	}
	ageDays := int(now.Sub(e.mtime).Hours() / 24)
	if ageDays < cfg.GraceDays {
		return entryWithinGrace, ageDays
	}
	return entryOrphan, ageDays
}

// pruneHeartbeat keeps a long image prune under the activity's HeartbeatTimeout.
// A multi-GB docker prune or a containerd store reclaim can run past the scan
// loop's per-directory heartbeat, so the prune calls heartbeat on their own.
const pruneHeartbeat = 30 * time.Second

// dockerPruneOutput bundles DockerSystemPrune's results so the call can run
// under a single-value heartbeat wrapper.
type dockerPruneOutput struct {
	reclaimed uint64
	warnings  []string
}

// dockerPrune reclaims unused Docker resources on the node through the Docker
// API (tunneled over conn) -- the equivalent of `docker system prune -af`. In
// dry-run it does nothing. Returns the reclaimed-space string and a log
// fragment.
func (a *Activities) dockerPrune(ctx context.Context, conn ssh.RemoteHost, dryRun bool) (string, string) {
	if dryRun {
		return "0B", "=== Docker Cleanup (dry run; skipped) ===\n"
	}

	res, err := shared.WithHeartbeat(ctx, pruneHeartbeat, func() (dockerPruneOutput, error) {
		reclaimed, warnings, e := conn.DockerSystemPrune(ctx)
		return dockerPruneOutput{reclaimed: reclaimed, warnings: warnings}, e
	})
	reclaimed, warnings := res.reclaimed, res.warnings
	if err != nil {
		return "0B", "=== Docker Cleanup ===\ndocker client: " + err.Error() + "\n"
	}

	var note strings.Builder
	note.WriteString("=== Docker Cleanup ===\n")
	for _, w := range warnings {
		note.WriteString(w + "\n")
	}
	freed := nodes.HumanBytes(int64(reclaimed))
	fmt.Fprintf(&note, "reclaimed %s\n", freed)
	return freed, note.String()
}

// containerdPrune reclaims the orphaned containerd moby image store on the node
// through the containerd API (tunneled over conn). It is store-aware: the prune
// skips itself when containerd is docker's live image store. In dry-run it
// reports the candidate count without deleting. Returns the reclaimed-space
// string, whether it was skipped, the skip reason, and a log fragment.
func (a *Activities) containerdPrune(ctx context.Context, conn ssh.RemoteHost, dryRun bool) (space string, skipped bool, reason string, note string) {
	res, err := shared.WithHeartbeat(ctx, pruneHeartbeat, func() (ssh.ContainerdPruneResult, error) {
		return conn.ContainerdPrune(ctx, dryRun)
	})
	if err != nil {
		return "0B", false, "", "=== Containerd Cleanup ===\ncontainerd client: " + err.Error() + "\n"
	}

	var out strings.Builder
	out.WriteString("=== Containerd Cleanup ===\n")
	if res.Skipped {
		fmt.Fprintf(&out, "skipped: %s\n", res.Reason)
		return "0B", true, res.Reason, out.String()
	}
	for _, w := range res.Warnings {
		out.WriteString(w + "\n")
	}
	if dryRun {
		fmt.Fprintf(&out, "dry run: %d image(s) would be deleted\n", res.Candidates)
		return "0B", false, "", out.String()
	}
	freed := nodes.HumanBytes(int64(res.Reclaimed))
	fmt.Fprintf(&out, "deleted %d image(s), reclaimed %s\n", res.Deleted, freed)
	return freed, false, "", out.String()
}

// reclaimStep runs a whole-item storage sweep (rootfs entries or buildx volumes)
// under a heartbeat and formats its outcome under the given title. In dry-run it
// reports what would be freed without deleting. Returns the reclaimed-space
// string and a log fragment.
func (a *Activities) reclaimStep(ctx context.Context, title string, dryRun bool, run func() (ssh.ReclaimResult, error)) (string, string) {
	res, err := shared.WithHeartbeat(ctx, pruneHeartbeat, run)
	if err != nil {
		return "0B", fmt.Sprintf("=== %s ===\n%v\n", title, err)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "=== %s ===\n", title)
	for _, w := range res.Warnings {
		out.WriteString(w + "\n")
	}
	if dryRun {
		freed := nodes.HumanBytes(int64(res.Reclaimable))
		fmt.Fprintf(&out, "dry run: %d item(s) would be removed, freeing %s\n", res.Candidates, freed)
		return "0B", out.String()
	}
	freed := nodes.HumanBytes(int64(res.Reclaimed))
	fmt.Fprintf(&out, "removed %d item(s), reclaimed %s\n", res.Deleted, freed)
	return freed, out.String()
}
