// -------------------------------------------------------------------------------
// Registry Garbage-Collect Activities - Registry-Specific Saga Steps
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The registry-specific pieces of the GC saga: its config/result types and the
// long-running garbage-collect step. The generic find/scale/wait/measure
// activities the saga also uses live in the shared nodes package (and are
// shared with aptly-cleanup).
// -------------------------------------------------------------------------------

package registrygc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"

	"github.com/moby/moby/api/types/container"
	"go.temporal.io/sdk/activity"
)

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// Activities holds the dependencies for the registry-specific GC step. Register
// an instance with the Temporal worker to expose RunRegistryGarbageCollect as
// an activity implementation; the generic saga steps come from a separate
// nodes.SagaActivities registration.
type Activities struct {
	ssh *shared.SSHClient
}

// New creates an Activities instance over the shared SSH client.
func New(ssh *shared.SSHClient) *Activities {
	return &Activities{ssh: ssh}
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// RegistryGCConfig holds workflow-level configuration passed as input.
type RegistryGCConfig struct {
	// JobName identifies the registry's Nomad job. Defaults to "registry".
	JobName string `json:"job_name"`
	// GroupName is the task group inside the job to scale. Defaults to the
	// JobName (matches the convention used by the munchbox-service pack).
	GroupName string `json:"group_name"`
	// RegistryDataDir is the host path bind-mounted into the registry
	// container as /var/lib/registry. Defaults to
	// "/mnt/gdrive/munchbox-data/registry".
	RegistryDataDir string `json:"registry_data_dir"`
	// RegistryImage is the docker image used for the one-shot GC run.
	// Should match the running registry's image. Defaults to "registry:3".
	RegistryImage string `json:"registry_image"`
	// DryRun runs garbage-collect with --dry-run, logging blobs that
	// would be deleted without actually freeing space.
	DryRun bool `json:"dry_run"`
	// DeleteUntagged tells GC to also remove manifests not referenced by
	// any tag (and the blobs they reference). Default true — without it
	// every tag overwrite (e.g. CI re-pushing :latest) leaves an
	// orphaned manifest forever.
	DeleteUntagged bool `json:"delete_untagged"`
}

// ApplyDefaults fills in unset fields with their defaults. Called by the
// workflow before any activities run so every activity sees a fully
// populated config and the values are deterministic across replay.
func (c *RegistryGCConfig) ApplyDefaults() {
	if c.JobName == "" {
		c.JobName = "registry"
	}
	if c.GroupName == "" {
		c.GroupName = c.JobName
	}
	if c.RegistryDataDir == "" {
		c.RegistryDataDir = "/mnt/gdrive/munchbox-data/registry"
	}
	if c.RegistryImage == "" {
		c.RegistryImage = "registry:3"
	}
}

// RegistryGCResult holds the workflow-level outcome reported back to the
// trigger / caller.
type RegistryGCResult struct {
	NodeName       string `json:"node_name"`
	NodeAddr       string `json:"node_addr"`
	BlobsDeleted   int    `json:"blobs_deleted"`
	BytesReclaimed string `json:"bytes_reclaimed"`
	BeforeBytes    string `json:"before_bytes"`
	AfterBytes     string `json:"after_bytes"`
	DryRun         bool   `json:"dry_run"`
}

// RegistryGCRunResult is the small struct returned by
// RunRegistryGarbageCollect. The workflow folds it into RegistryGCResult.
type RegistryGCRunResult struct {
	BlobsDeleted int    `json:"blobs_deleted"`
	Output       string `json:"output"`
}

// -------------------------------------------------------------------------
// RUN GARBAGE-COLLECT
// -------------------------------------------------------------------------

// RunRegistryGarbageCollect SSHes to the registry host and runs the docker
// garbage-collect command against the bind-mounted storage. This is the
// long-running step (multi-GB registries take minutes); it heartbeats
// periodically and reports the count of "blob eligible for deletion" lines
// emitted by the registry tool.
//
// Configured with MaxAttempts=1 by the workflow — a partial GC run shouldn't
// be repeated; let the deferred scale-back put the registry online instead and
// surface the failure.
func (a *Activities) RunRegistryGarbageCollect(ctx context.Context, node nodes.NodeInfo, config RegistryGCConfig) (RegistryGCRunResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Running registry garbage-collect",
		"node", node.Name, "image", config.RegistryImage,
		"dry_run", config.DryRun, "delete_untagged", config.DeleteUntagged)

	// garbage-collect [--dry-run] [--delete-untagged] /etc/distribution/config.yml
	//
	// The registry image embeds /etc/distribution/config.yml as the default
	// config its binary reads. The garbage-collect subcommand only needs the
	// storage rootdirectory from that config, which the embedded default points
	// at /var/lib/registry — the same path as the live container's bind mount.
	cmd := []string{"garbage-collect"}
	if config.DryRun {
		cmd = append(cmd, "--dry-run")
	}
	if config.DeleteUntagged {
		cmd = append(cmd, "--delete-untagged")
	}
	cmd = append(cmd, "/etc/distribution/config.yml")

	out, err := a.ssh.RunContainer(ctx, nodes.Target(node), &container.Config{
		Image: config.RegistryImage,
		Cmd:   cmd,
	}, []string{config.RegistryDataDir + ":/var/lib/registry"}, 30*time.Second)
	if err != nil {
		return RegistryGCRunResult{Output: out}, fmt.Errorf("registry garbage-collect on %s: %w", node.Name, err)
	}

	return RegistryGCRunResult{
		BlobsDeleted: parseBlobsDeleted(out),
		Output:       out,
	}, nil
}

// parseBlobsDeleted counts "blob eligible for deletion:" lines emitted by the
// registry garbage-collect tool.
func parseBlobsDeleted(output string) int {
	count := 0
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "blob eligible for deletion:") {
			count++
		}
	}
	return count
}
