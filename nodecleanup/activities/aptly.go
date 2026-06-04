// -------------------------------------------------------------------------------
// Aptly Cleanup Activities - Reclaim Repository Storage
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs `aptly db cleanup` in a one-shot container against the pool volume,
// dropping packages no longer referenced by any snapshot or repo. Invoked
// while the aptly job is scaled to 0 so the running server isn't holding the
// single-writer leveldb lock. Shares the node-find / scale / wait / measure
// activities with the registry-GC saga.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
)

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
func (a *Activities) RunAptlyDBCleanup(ctx context.Context, node NodeInfo, image, dataDir string) (string, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Running aptly db cleanup (one-shot)", "node", node.Name, "image", image, "data_dir", dataDir)

	sudo := ""
	if node.IsOracle {
		sudo = "sudo "
	}

	inner := `printf '{"rootDir":"/opt/aptly"}' > /etc/aptly.conf && aptly db cleanup`
	cmd := fmt.Sprintf(
		`%sdocker run --rm --entrypoint sh -v %s:/opt/aptly %s -c %s 2>&1`,
		sudo, shellQuote(dataDir), shellQuote(image), shellQuote(inner))

	out, err := a.runSSHCommandWithHeartbeat(ctx, node, cmd, 30*time.Second)
	if err != nil {
		return out, fmt.Errorf("aptly db cleanup on %s: %w (output: %s)", node.Name, err, strings.TrimSpace(out))
	}

	logger.Info("aptly db cleanup complete", "node", node.Name)
	return out, nil
}
