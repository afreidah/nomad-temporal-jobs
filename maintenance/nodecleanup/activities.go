// -------------------------------------------------------------------------------
// Node Cleanup Activities - Orphaned Data Directory Removal via SSH
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Implements Temporal activities for discovering Nomad client nodes and
// cleaning up orphaned job data directories via SSH. Orphaned directories
// accumulate when Nomad jobs move between nodes and their ephemeral data
// is left behind. Safety features include dry-run mode, a configurable
// grace period, and system directory exclusion.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"context"
	"fmt"
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
	RunningJobIDs(ctx context.Context, nodeID string) (map[string]struct{}, error)
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
	DataDir     string `json:"data_dir"`     // Base directory to scan (default: /opt/nomad/data)
	GraceDays   int    `json:"grace_days"`   // Only delete directories older than this (default: 7)
	DryRun      bool   `json:"dry_run"`      // If true, only report what would be deleted
	DockerPrune bool   `json:"docker_prune"` // If true, also prune unused Docker images
}

// CleanupResult holds the outcome of a cleanup operation on a single node.
type CleanupResult struct {
	NodeName         string   `json:"node_name"`
	NodeAddr         string   `json:"node_addr"`
	Scanned          int      `json:"scanned"`
	Orphaned         int      `json:"orphaned"`
	Deleted          int      `json:"deleted"`
	Skipped          int      `json:"skipped"`
	DockerSpaceFreed string   `json:"docker_space_freed"`
	Errors           []string `json:"errors,omitempty"`
	Output           string   `json:"output"`
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
// set of running jobs comes from the Nomad API (so no token is shipped to the
// node); the directory scan and deletions run over SSH because the data lives
// on the node's disk and Nomad exposes no API for it. Dry-run reports what
// would be deleted without removing anything.
func (a *Activities) CleanupNodeViaSSH(ctx context.Context, node nodes.NodeInfo, config CleanupConfig) (CleanupResult, error) {
	logger := activity.GetLogger(ctx)
	result := CleanupResult{NodeName: node.Name, NodeAddr: node.Address, DockerSpaceFreed: "0B"}

	// Running jobs on this node, straight from the Nomad API.
	running, err := a.runningJobs(ctx, node.ID)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}

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

	now := time.Now()
	var out strings.Builder
	for _, e := range entries {
		result.Scanned++
		activity.RecordHeartbeat(ctx, e.name) // progress signal so a long scan trips HeartbeatTimeout, not StartToClose

		action, ageDays := classifyEntry(e, running, config, now)
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

	if config.DockerPrune {
		freed, dockerOut := a.dockerPrune(ctx, conn, config.DryRun)
		result.DockerSpaceFreed = freed
		out.WriteString(dockerOut)
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

// runningJobs returns the set of job IDs with a running allocation on the node,
// read from the Nomad API.
func (a *Activities) runningJobs(ctx context.Context, nodeID string) (map[string]struct{}, error) {
	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.node_allocations")
	defer span.End()

	return a.nomad.RunningJobIDs(ctx, nodeID)
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
	entryActive                           // belongs to a running job
	entryWithinGrace                      // orphaned but younger than the grace period
	entryOrphan                           // a deletion candidate
)

// classifyEntry decides what should happen to one data-dir entry and returns
// the action plus the entry's age in days.
func classifyEntry(e dirEntry, running map[string]struct{}, cfg CleanupConfig, now time.Time) (orphanAction, int) {
	if _, excluded := orphanExcludes[e.name]; excluded {
		return entrySkipExcluded, 0
	}
	if isJobRunning(e.name, running) {
		return entryActive, 0
	}
	ageDays := int(now.Sub(e.mtime).Hours() / 24)
	if ageDays < cfg.GraceDays {
		return entryWithinGrace, ageDays
	}
	return entryOrphan, ageDays
}

// dockerPrune reclaims unused Docker resources on the node through the Docker
// API (tunneled over conn) -- the equivalent of `docker system prune -af`. In
// dry-run it does nothing. Returns the reclaimed-space string and a log
// fragment.
func (a *Activities) dockerPrune(ctx context.Context, conn ssh.RemoteHost, dryRun bool) (string, string) {
	if dryRun {
		return "0B", "=== Docker Cleanup (dry run; skipped) ===\n"
	}

	reclaimed, warnings, err := conn.DockerSystemPrune(ctx)
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
