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
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/activity"
	"golang.org/x/crypto/ssh"
)

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// Config holds SSH-related settings for node cleanup activities.
type Config struct {
	SSHKeyPath    string // Path to SSH private key (default: /root/.ssh/id_ed25519)
	SSHCertPath   string // Path to SSH client certificate (default: /root/.ssh/id_ed25519-cert.pub)
	SSHHostCAPath string // Path to SSH host CA public key (default: /root/.ssh/ssh-host-ca.pub)
}

// Validate applies defaults for optional fields.
func (c *Config) Validate() {
	if c.SSHKeyPath == "" {
		c.SSHKeyPath = "/root/.ssh/id_ed25519"
	}
	if c.SSHCertPath == "" {
		c.SSHCertPath = "/root/.ssh/id_ed25519-cert.pub"
	}
	if c.SSHHostCAPath == "" {
		c.SSHHostCAPath = "/root/.ssh/ssh-host-ca.pub"
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
}

// New creates an Activities instance with validated configuration.
func New(cfg Config) *Activities {
	cfg.Validate()
	return &Activities{config: cfg}
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

	ctx, span := shared.StartClientSpan(ctx, "nomad.list_nodes",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

	client, err := shared.NewNomadClient()
	if err != nil {
		return nil, fmt.Errorf("create Nomad client: %w", err)
	}

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

// CleanupNodeViaSSH connects to a node over SSH and executes a cleanup
// script that identifies and optionally removes orphaned job data
// directories. Returns detailed results including counts and any errors.
func (a *Activities) CleanupNodeViaSSH(ctx context.Context, node NodeInfo, config CleanupConfig) (CleanupResult, error) {
	logger := activity.GetLogger(ctx)
	result := CleanupResult{
		NodeName: node.Name,
		NodeAddr: node.Address,
	}

	logger.Info("Connecting to node via SSH", "node", node.Name, "address", node.Address)

	sshConfig, err := a.buildSSHConfig(node)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}

	// Connect to the node
	sshAddr := fmt.Sprintf("%s:22", node.Address)
	client, err := ssh.Dial("tcp", sshAddr, sshConfig)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("SSH connect failed: %v", err))
		return result, err
	}
	defer client.Close()

	// Build and execute the cleanup script
	sudoPrefix := ""
	if node.IsOracle {
		sudoPrefix = "sudo "
	}
	nomadToken := os.Getenv("NOMAD_TOKEN")
	script := buildCleanupScript(node.ID, node.HTTPAddr, config.DataDir, config.GraceDays, config.DryRun, config.DockerPrune, sudoPrefix, nomadToken)

	session, err := client.NewSession()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("SSH session failed: %v", err))
		return result, err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	logger.Info("Executing cleanup script", "node", node.Name)
	if err := session.Run(script); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("script failed: %v, stderr: %s", err, stderr.String()))
		return result, err
	}

	result.Output = stdout.String()
	parseCleanupOutput(&result, stdout.String())

	logger.Info("Node cleanup complete",
		"node", node.Name,
		"scanned", result.Scanned,
		"orphaned", result.Orphaned,
		"deleted", result.Deleted,
		"skipped", result.Skipped)

	return result, nil
}

// -------------------------------------------------------------------------
// SSH HELPERS
// -------------------------------------------------------------------------

// buildSSHConfig constructs an SSH client configuration with certificate-based
// auth (preferred) falling back to plain key auth. Host keys are verified
// against the host CA public key.
func (a *Activities) buildSSHConfig(node NodeInfo) (*ssh.ClientConfig, error) {
	sshUser := "root"
	if node.IsOracle {
		sshUser = "ubuntu"
	}

	keyData, err := os.ReadFile(a.config.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", a.config.SSHKeyPath, err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}

	// Prefer cert auth, fall back to plain key
	var authMethods []ssh.AuthMethod

	certData, err := os.ReadFile(a.config.SSHCertPath)
	if err == nil {
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(certData)
		if err == nil {
			if cert, ok := pubKey.(*ssh.Certificate); ok {
				if certSigner, err := ssh.NewCertSigner(cert, signer); err == nil {
					authMethods = append(authMethods, ssh.PublicKeys(certSigner))
				}
			}
		}
	}
	authMethods = append(authMethods, ssh.PublicKeys(signer))

	// Verify host keys against host CA
	hostCAData, err := os.ReadFile(a.config.SSHHostCAPath)
	if err != nil {
		return nil, fmt.Errorf("read host CA %s: %w", a.config.SSHHostCAPath, err)
	}

	hostCAKey, _, _, _, err := ssh.ParseAuthorizedKey(hostCAData)
	if err != nil {
		return nil, fmt.Errorf("parse host CA key: %w", err)
	}

	certChecker := &ssh.CertChecker{
		IsHostAuthority: func(auth ssh.PublicKey, address string) bool {
			return bytes.Equal(auth.Marshal(), hostCAKey.Marshal())
		},
	}

	return &ssh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: certChecker.CheckHostKey,
		Timeout:         30 * time.Second,
	}, nil
}

// -------------------------------------------------------------------------
// SCRIPT GENERATION
// -------------------------------------------------------------------------

// buildCleanupScript generates a bash script that runs on the remote node
// to identify and optionally remove orphaned Nomad data directories.
func buildCleanupScript(nodeID, httpAddr, dataDir string, graceDays int, dryRun, dockerPrune bool, sudoPrefix, nomadToken string) string {
	dryRunFlag := "true"
	if !dryRun {
		dryRunFlag = "false"
	}
	dockerPruneFlag := "false"
	if dockerPrune {
		dockerPruneFlag = "true"
	}

	return fmt.Sprintf(`#!/bin/bash
set -e

DATA_DIR="%s"
GRACE_DAYS=%d
DRY_RUN="%s"
DOCKER_PRUNE="%s"
NODE_ID="%s"
NOMAD_TOKEN="%s"
NOMAD_HTTP_ADDR="%s"

SCANNED=0
ORPHANED=0
DELETED=0
SKIPPED=0
DOCKER_SPACE_FREED="0B"

EXCLUDE="alloc plugins tmp server client"

NOMAD_CA=""
for ca in /etc/nomad.d/tls/ca.crt /opt/nomad/tls/vault-intermediate-ca.pem; do
  if [ -f "$ca" ]; then
    NOMAD_CA="$ca"
    break
  fi
done

if [ -z "$NOMAD_CA" ]; then
  echo "ERROR: Could not find Nomad CA certificate"
  exit 1
fi

RUNNING_JOBS=$(%scurl -sf --cacert "$NOMAD_CA" \
  -H "X-Nomad-Token: $NOMAD_TOKEN" \
  "https://${NOMAD_HTTP_ADDR}/v1/node/${NODE_ID}/allocations" 2>/dev/null | \
  %sjq -r '.[] | select(.ClientStatus == "running") | .JobID' 2>/dev/null | sort -u || echo "")

if [ -z "$RUNNING_JOBS" ]; then
  echo "ERROR: Could not get running jobs from local Nomad agent at ${NOMAD_HTTP_ADDR}"
  exit 1
fi

echo "Running jobs on this node:"
echo "$RUNNING_JOBS" | sed 's/^/  - /'
echo ""

strip_index() {
  echo "$1" | sed 's/-[0-9]*$//'
}

is_running() {
  local dir="$1"
  local base=$(strip_index "$dir")
  echo "$RUNNING_JOBS" | grep -qx "$dir" && return 0
  echo "$RUNNING_JOBS" | grep -qx "$base" && return 0
  return 1
}

for dir in "$DATA_DIR"/*/; do
  [ -d "$dir" ] || continue

  dirname=$(basename "$dir")
  SCANNED=$((SCANNED + 1))

  if echo "$EXCLUDE" | grep -qw "$dirname"; then
    echo "SKIP (system): $dirname"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  if is_running "$dirname"; then
    echo "OK (active): $dirname"
    continue
  fi

  mtime=$(%sstat -c %%Y "$dir" 2>/dev/null || echo 0)
  now=$(date +%%s)
  age_days=$(( (now - mtime) / 86400 ))

  if [ "$age_days" -lt "$GRACE_DAYS" ]; then
    echo "SKIP (${age_days}d old, grace=${GRACE_DAYS}d): $dirname"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  size=$(%sdu -sh "$dir" 2>/dev/null | cut -f1 || echo "?")

  ORPHANED=$((ORPHANED + 1))

  if [ "$DRY_RUN" = "true" ]; then
    echo "WOULD DELETE (${age_days}d old, $size): $dirname"
  else
    echo "DELETING (${age_days}d old, $size): $dirname"
    %srm -rf "$dir"
    DELETED=$((DELETED + 1))
  fi
done

echo ""

if [ "$DOCKER_PRUNE" = "true" ]; then
  echo "=== Docker Cleanup ==="
  if [ "$DRY_RUN" = "true" ]; then
    echo "Would prune unused Docker images (dry run)"
    %sdocker system df 2>/dev/null || echo "Docker not available"
  else
    PRUNE_OUTPUT=$(%sdocker system prune -af 2>&1 || echo "Docker prune failed")
    echo "$PRUNE_OUTPUT"
    DOCKER_SPACE_FREED=$(echo "$PRUNE_OUTPUT" | grep -oP 'Total reclaimed space: \K[0-9.]+[A-Za-z]+' || echo "0B")
  fi
fi

echo ""
echo "RESULT: scanned=$SCANNED orphaned=$ORPHANED deleted=$DELETED skipped=$SKIPPED docker_freed=$DOCKER_SPACE_FREED"
`, dataDir, graceDays, dryRunFlag, dockerPruneFlag, nodeID, nomadToken, httpAddr, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix)
}

// -------------------------------------------------------------------------
// OUTPUT PARSING
// -------------------------------------------------------------------------

// parseCleanupOutput extracts counts from the RESULT line of the cleanup
// script output and populates the CleanupResult fields.
func parseCleanupOutput(result *CleanupResult, output string) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "RESULT:") {
			continue
		}
		for _, part := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(part, "scanned="):
				fmt.Sscanf(part, "scanned=%d", &result.Scanned)
			case strings.HasPrefix(part, "orphaned="):
				fmt.Sscanf(part, "orphaned=%d", &result.Orphaned)
			case strings.HasPrefix(part, "deleted="):
				fmt.Sscanf(part, "deleted=%d", &result.Deleted)
			case strings.HasPrefix(part, "skipped="):
				fmt.Sscanf(part, "skipped=%d", &result.Skipped)
			case strings.HasPrefix(part, "docker_freed="):
				result.DockerSpaceFreed = strings.TrimPrefix(part, "docker_freed=")
			}
		}
	}
}
