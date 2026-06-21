// -------------------------------------------------------------------------------
// Aptly Cleanup Activities - Reclaim Repository Storage
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs `aptly db cleanup` in a one-shot container against the pool volume,
// dropping packages no longer referenced by any snapshot or repo. Invoked
// while the aptly job is scaled to 0 so the running server isn't holding the
// single-writer leveldb lock. The node-find / scale / wait / measure activities
// the saga also uses live in the shared nodes package (and are shared with
// registry-GC).
// -------------------------------------------------------------------------------

package aptlycleanup

import (
	"context"
	"fmt"
	"time"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"

	"github.com/moby/moby/api/types/container"
	"go.temporal.io/sdk/activity"
)

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// Activities holds the dependencies for the aptly-specific cleanup step.
// Register an instance with the Temporal worker to expose RunAptlyDBCleanup as
// an activity implementation; the generic saga steps come from a separate
// nodes.SagaActivities registration.
type Activities struct {
	runner shared.ContainerRunner
}

// New creates an Activities instance over the shared SSH client.
func New(runner shared.ContainerRunner) *Activities {
	return &Activities{runner: runner}
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// AptlyCleanupConfig is the workflow input.
type AptlyCleanupConfig struct {
	JobName   string `json:"job_name"`   // Nomad job hosting aptly. Default "aptly".
	GroupName string `json:"group_name"` // Task group to scale. Default = JobName.
	Image     string `json:"image"`      // aptly image for the one-shot cleanup. Default "urpylka/aptly:1.6.2".
	DataDir   string `json:"data_dir"`   // Host path of aptly's rootDir pool. Default "/mnt/gdrive/aptly".
}

// ApplyDefaults fills any unset field with its default.
func (c *AptlyCleanupConfig) ApplyDefaults() {
	if c.JobName == "" {
		c.JobName = "aptly"
	}
	if c.GroupName == "" {
		c.GroupName = c.JobName
	}
	if c.Image == "" {
		c.Image = "urpylka/aptly:1.6.2"
	}
	if c.DataDir == "" {
		c.DataDir = "/mnt/gdrive/aptly"
	}
}

// AptlyCleanupResult summarizes a cleanup run.
type AptlyCleanupResult struct {
	Node           string `json:"node"`
	BeforeBytes    string `json:"before_bytes"`
	AfterBytes     string `json:"after_bytes"`
	BytesReclaimed string `json:"bytes_reclaimed"`
	Output         string `json:"output"`
}

// RunAptlyDBCleanup runs `aptly db cleanup` in a one-shot container against the
// pool volume. Run only while the aptly job is scaled to 0 so the server isn't
// holding the leveldb lock. A minimal config supplying rootDir is all db
// cleanup needs (it never touches the publish endpoints); the entrypoint is
// overridden to a shell to write that config first. stderr is merged into the
// output for diagnosability.
func (a *Activities) RunAptlyDBCleanup(ctx context.Context, node nodes.NodeInfo, image, dataDir string) (string, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Running aptly db cleanup (one-shot)", "node", node.Name, "image", image, "data_dir", dataDir)

	inner := `printf '{"rootDir":"/opt/aptly"}' > /etc/aptly.conf && aptly db cleanup`
	out, err := a.runner.RunContainer(ctx, nodes.Target(node), &container.Config{
		Image:      image,
		Entrypoint: []string{"sh"},
		Cmd:        []string{"-c", inner},
	}, []string{dataDir + ":/opt/aptly"}, 30*time.Second)
	if err != nil {
		return out, fmt.Errorf("aptly db cleanup on %s: %w", node.Name, err)
	}

	logger.Info("aptly db cleanup complete", "node", node.Name)
	return out, nil
}
