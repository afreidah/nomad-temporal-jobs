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

package activities

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/moby/moby/client"
	"go.temporal.io/sdk/activity"
)

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// Config holds SSH and Postgres settings for node cleanup activities. SSH
// serves orphaned-data, registry-GC, and aptly cleanup; the Postgres fields
// serve the postgres-maintenance workflow.
type Config struct {
	SSHKeyPath    string // Path to SSH private key (default: /root/.ssh/id_ed25519)
	SSHCertPath   string // Path to SSH client certificate (default: /root/.ssh/id_ed25519-cert.pub)
	SSHHostCAPath string // Path to SSH host CA public key (default: /root/.ssh/ssh-host-ca.pub)

	PostgresHost        string // default: postgres-primary.service.consul
	PostgresPort        string // default: 5432
	PostgresUser        string // default: postgres
	PostgresPassword    string // from PGPASSWORD; no default
	PostgresSSLMode     string // default: prefer
	PostgresSSLRootCert string // optional CA path for verify-ca/verify-full
}

// ApplyDefaults fills optional SSH and Postgres fields with their defaults.
func (c *Config) ApplyDefaults() {
	if c.SSHKeyPath == "" {
		c.SSHKeyPath = "/root/.ssh/id_ed25519"
	}
	if c.SSHCertPath == "" {
		c.SSHCertPath = "/root/.ssh/id_ed25519-cert.pub"
	}
	if c.SSHHostCAPath == "" {
		c.SSHHostCAPath = "/root/.ssh/ssh-host-ca.pub"
	}
	if c.PostgresHost == "" {
		c.PostgresHost = "postgres-primary.service.consul"
	}
	if c.PostgresPort == "" {
		c.PostgresPort = "5432"
	}
	if c.PostgresUser == "" {
		c.PostgresUser = "postgres"
	}
	if c.PostgresSSLMode == "" {
		c.PostgresSSLMode = "prefer"
	}
}

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// Activities holds shared dependencies for node cleanup activities. Register
// an instance with the Temporal worker to expose all exported methods as
// activity implementations.
type Activities struct {
	config Config
	nomad  *nomadapi.Client
	ssh    *shared.SSHClient
}

// New creates an Activities instance with defaults applied and shared Nomad and
// SSH clients reused across activity invocations (rather than rebuilt per call).
func New(cfg Config) (*Activities, error) {
	cfg.ApplyDefaults()
	nomad, err := shared.NewNomadClient()
	if err != nil {
		return nil, fmt.Errorf("create nomad client: %w", err)
	}
	sshClient, err := shared.NewSSHClient(shared.SSHConfig{
		KeyPath:    cfg.SSHKeyPath,
		CertPath:   cfg.SSHCertPath,
		HostCAPath: cfg.SSHHostCAPath,
	})
	if err != nil {
		return nil, fmt.Errorf("create ssh client: %w", err)
	}
	return &Activities{config: cfg, nomad: nomad, ssh: sshClient}, nil
}

// sshTarget builds the SSH target for a node. The worker connects as root
// everywhere -- the Vault SSH CA issues a root principal the oracle hosts
// accept too -- so there is no per-node user or sudo handling, and root reaches
// root-owned data dirs and the docker socket directly.
func sshTarget(node NodeInfo) shared.SSHTarget {
	return shared.SSHTarget{Host: node.Address, User: "root"}
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

// NodeInfo contains information about a Nomad client node needed for SSH
// connection and cleanup script execution.
type NodeInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	HTTPAddr string `json:"http_addr"` // Nomad agent HTTP address (e.g., "10.200.0.11:4646")
	IsOracle bool   `json:"is_oracle"` // Oracle nodes use ubuntu user instead of root
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// GetAllNomadClientNodes retrieves all ready Nomad client nodes with their
// SSH addresses and node metadata. Creates a client span to Nomad for
// service graph visibility.
func (a *Activities) GetAllNomadClientNodes(ctx context.Context) ([]NodeInfo, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Retrieving all Nomad client nodes")

	_, span := shared.StartClientSpan(ctx, "nomad.list_nodes",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

	client := a.nomad
	nodeList, _, err := client.Nodes().List(nil)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	var nodes []NodeInfo
	for _, n := range nodeList {
		if n.Status != "ready" {
			logger.Info("Skipping node - not ready", "node", n.Name, "status", n.Status)
			continue
		}

		node, _, err := client.Nodes().Info(n.ID, nil)
		if err != nil {
			logger.Warn("Failed to get node info", "node", n.Name, "error", err)
			continue
		}

		// Prefer the node's IP address for SSH; fall back to HTTPAddr
		addr := node.Attributes["unique.network.ip-address"]
		if addr == "" {
			addr = node.HTTPAddr
			if idx := strings.LastIndex(addr, ":"); idx != -1 {
				addr = addr[:idx]
			}
		}

		nodes = append(nodes, NodeInfo{
			ID:       n.ID,
			Name:     n.Name,
			Address:  addr,
			HTTPAddr: node.HTTPAddr,
			IsOracle: strings.HasPrefix(n.Name, "oracle"),
		})
	}

	logger.Info("Found client nodes", "count", len(nodes))
	return nodes, nil
}

// CleanupNodeViaSSH removes orphaned Nomad job data directories on a node. The
// set of running jobs comes from the Nomad API (so no token is shipped to the
// node); the directory scan and deletions run over SSH because the data lives
// on the node's disk and Nomad exposes no API for it. Dry-run reports what
// would be deleted without removing anything.
func (a *Activities) CleanupNodeViaSSH(ctx context.Context, node NodeInfo, config CleanupConfig) (CleanupResult, error) {
	logger := activity.GetLogger(ctx)
	result := CleanupResult{NodeName: node.Name, NodeAddr: node.Address, DockerSpaceFreed: "0B"}

	// Running jobs on this node, straight from the Nomad API.
	running, err := a.runningJobs(ctx, node.ID)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}

	logger.Info("Connecting to node via SSH", "node", node.Name, "address", node.Address)
	conn, err := a.ssh.Connect(sshTarget(node))
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
		if _, excluded := orphanExcludes[e.name]; excluded {
			result.Skipped++
			continue
		}
		if isJobRunning(e.name, running) {
			fmt.Fprintf(&out, "OK (active): %s\n", e.name)
			continue
		}
		ageDays := int(now.Sub(e.mtime).Hours() / 24)
		if ageDays < config.GraceDays {
			fmt.Fprintf(&out, "SKIP (%dd old, grace=%dd): %s\n", ageDays, config.GraceDays, e.name)
			result.Skipped++
			continue
		}

		result.Orphaned++
		path := strings.TrimRight(config.DataDir, "/") + "/" + e.name
		if config.DryRun {
			fmt.Fprintf(&out, "WOULD DELETE (%dd old): %s\n", ageDays, e.name)
			continue
		}
		if derr := conn.RemoveAll(path); derr != nil {
			logger.Warn("Failed to delete orphan dir", "dir", e.name, "error", derr)
			result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", e.name, derr))
			continue
		}
		fmt.Fprintf(&out, "DELETED (%dd old): %s\n", ageDays, e.name)
		result.Deleted++
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
	_, span := shared.StartClientSpan(ctx, "nomad.node_allocations", shared.PeerServiceAttr("nomad"))
	defer span.End()

	allocs, _, err := a.nomad.Nodes().Allocations(nodeID, nil)
	if err != nil {
		return nil, fmt.Errorf("list allocations for node %s: %w", nodeID, err)
	}
	running := make(map[string]struct{})
	for _, al := range allocs {
		if al.ClientStatus == "running" {
			running[al.JobID] = struct{}{}
		}
	}
	return running, nil
}

// listDataDirs returns the immediate subdirectories of dataDir on the remote
// host with their modification times, over SFTP.
func listDataDirs(conn *shared.SSHConn, dataDir string) ([]dirEntry, error) {
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

// dockerPrune reclaims unused Docker resources on the node through the Docker
// API (tunneled over conn) -- the equivalent of `docker system prune -af`. In
// dry-run it does nothing. Returns the reclaimed-space string and a log
// fragment.
func (a *Activities) dockerPrune(ctx context.Context, conn *shared.SSHConn, dryRun bool) (string, string) {
	if dryRun {
		return "0B", "=== Docker Cleanup (dry run; skipped) ===\n"
	}

	cli, err := conn.DockerClient()
	if err != nil {
		return "0B", "=== Docker Cleanup ===\ndocker client: " + err.Error() + "\n"
	}
	defer func() { _ = cli.Close() }()

	var reclaimed uint64
	var note strings.Builder
	note.WriteString("=== Docker Cleanup ===\n")

	if cp, perr := cli.ContainerPrune(ctx, client.ContainerPruneOptions{}); perr != nil {
		fmt.Fprintf(&note, "container prune: %v\n", perr)
	} else {
		reclaimed += cp.Report.SpaceReclaimed
	}
	// dangling=false prunes all unused images, matching `prune -a`.
	if ip, perr := cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: client.Filters{}.Add("dangling", "false")}); perr != nil {
		fmt.Fprintf(&note, "image prune: %v\n", perr)
	} else {
		reclaimed += ip.Report.SpaceReclaimed
	}
	if _, perr := cli.NetworkPrune(ctx, client.NetworkPruneOptions{}); perr != nil {
		fmt.Fprintf(&note, "network prune: %v\n", perr)
	}
	if bp, perr := cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: true}); perr != nil {
		fmt.Fprintf(&note, "build cache prune: %v\n", perr)
	} else {
		reclaimed += bp.Report.SpaceReclaimed
	}

	freed := HumanBytes(int64(reclaimed))
	fmt.Fprintf(&note, "reclaimed %s\n", freed)
	return freed, note.String()
}
