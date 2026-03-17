// -------------------------------------------------------------------------------
// Backup Activities - Infrastructure Snapshot and Upload Operations
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Implements Temporal activities for creating Nomad, Consul, PostgreSQL, and
// container registry snapshots, uploading them to S3-compatible storage, and
// cleaning up old backups based on retention policies. All methods on the
// Activities struct share a pre-configured S3 client. CLI tools (nomad,
// consul, pg_dumpall, tar, gzip) must be available in PATH.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"go.temporal.io/sdk/activity"
)

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// Config holds environment-driven settings for backup activities.
type Config struct {
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string

	NomadBackupDir    string // default: /mnt/gdrive/nomad-snapshots
	ConsulBackupDir   string // default: /mnt/gdrive/consul-snapshots
	PostgresBackupDir string // default: /mnt/gdrive/postgres-backups
	RegistryBackupDir string // default: /mnt/gdrive/registry-backups
	RegistryDataDir   string // default: /mnt/gdrive/munchbox-data/registry
}

// Validate checks that required S3 fields are present and applies defaults
// for optional directory paths.
func (c *Config) Validate() error {
	if c.S3Endpoint == "" {
		return fmt.Errorf("S3Endpoint is required")
	}
	if c.S3Bucket == "" {
		return fmt.Errorf("S3Bucket is required")
	}
	if c.S3AccessKey == "" {
		return fmt.Errorf("S3AccessKey is required")
	}
	if c.S3SecretKey == "" {
		return fmt.Errorf("S3SecretKey is required")
	}

	if c.NomadBackupDir == "" {
		c.NomadBackupDir = "/mnt/gdrive/nomad-snapshots"
	}
	if c.ConsulBackupDir == "" {
		c.ConsulBackupDir = "/mnt/gdrive/consul-snapshots"
	}
	if c.PostgresBackupDir == "" {
		c.PostgresBackupDir = "/mnt/gdrive/postgres-backups"
	}
	if c.RegistryBackupDir == "" {
		c.RegistryBackupDir = "/mnt/gdrive/registry-backups"
	}
	if c.RegistryDataDir == "" {
		c.RegistryDataDir = "/mnt/gdrive/munchbox-data/registry"
	}
	return nil
}

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// Activities holds shared dependencies for backup activities. Register an
// instance with the Temporal worker to expose all exported methods as
// activity implementations.
type Activities struct {
	config   Config
	s3Client *s3.Client
}

// New creates an Activities instance with a pre-configured S3 client.
// Returns an error if the config is invalid.
func New(cfg Config) (*Activities, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	endpoint := cfg.S3Endpoint
	s3Client := s3.New(s3.Options{
		BaseEndpoint: &endpoint,
		Region:       "us-east-1", // required by SDK but ignored by s3-orchestrator
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		UsePathStyle: true,
	})

	return &Activities{config: cfg, s3Client: s3Client}, nil
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// BackupResult contains the outcome of a backup workflow execution.
type BackupResult struct {
	NomadSnapshot  string    `json:"nomad_snapshot"`
	ConsulSnapshot string    `json:"consul_snapshot"`
	PostgresBackup string    `json:"postgres_backup"`
	RegistryBackup string    `json:"registry_backup"`
	NomadS3Key     string    `json:"nomad_s3_key"`
	ConsulS3Key    string    `json:"consul_s3_key"`
	PostgresS3Key  string    `json:"postgres_s3_key"`
	Timestamp      time.Time `json:"timestamp"`
	Success        bool      `json:"success"`
	Error          string    `json:"error,omitempty"`
}

// RetentionConfig holds retention settings passed as workflow input so
// values are deterministic across replays.
type RetentionConfig struct {
	LocalDays int `json:"local_days"`
	S3Days    int `json:"s3_days"`
}

// -------------------------------------------------------------------------
// SNAPSHOT ACTIVITIES
// -------------------------------------------------------------------------

// TakeNomadSnapshot creates a Raft snapshot of the Nomad cluster state
// including job specs, allocations, ACLs, and scheduler configuration.
// Requires NOMAD_TOKEN with snapshot permissions in the environment.
func (a *Activities) TakeNomadSnapshot(ctx context.Context) (string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for nomad edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "nomad.snapshot",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

	if err := os.MkdirAll(a.config.NomadBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := filepath.Join(a.config.NomadBackupDir, fmt.Sprintf("nomad-%s.snap", timestamp))

	cmd := exec.CommandContext(ctx, "nomad", "operator", "snapshot", "save", filename)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("nomad snapshot failed: %w, output: %s", err, output)
	}

	// Log file size for operational visibility
	if info, statErr := os.Stat(filename); statErr == nil {
		logger.Info("Nomad snapshot saved", "path", filename, "size_bytes", info.Size())
	}

	return filename, nil
}

// TakeConsulSnapshot creates a Raft snapshot of the Consul cluster state
// including KV store, service catalog, ACLs, sessions, and intentions.
// Vault data is included since Vault uses Consul as its storage backend.
// Requires CONSUL_HTTP_TOKEN with snapshot permissions in the environment.
func (a *Activities) TakeConsulSnapshot(ctx context.Context) (string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for consul edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "consul.snapshot",
		shared.PeerServiceAttr("consul"),
	)
	defer span.End()

	if err := os.MkdirAll(a.config.ConsulBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := filepath.Join(a.config.ConsulBackupDir, fmt.Sprintf("consul-%s.snap", timestamp))

	cmd := exec.CommandContext(ctx, "consul", "snapshot", "save", filename)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("consul snapshot failed: %w, output: %s", err, output)
	}

	if info, statErr := os.Stat(filename); statErr == nil {
		logger.Info("Consul snapshot saved", "path", filename, "size_bytes", info.Size())
	}

	return filename, nil
}

// TakePostgresBackup creates a full dump of all PostgreSQL databases
// compressed with gzip. Captures authentik, nextcloud, temporal, and all
// roles and permissions via pg_dumpall. Requires PGPASSWORD in the
// environment.
func (a *Activities) TakePostgresBackup(ctx context.Context) (string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for postgres edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "postgres.backup",
		shared.PeerServiceAttr("postgres-primary"),
	)
	defer span.End()

	if err := os.MkdirAll(a.config.PostgresBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := filepath.Join(a.config.PostgresBackupDir, fmt.Sprintf("postgres-%s.sql.gz", timestamp))

	// pipefail ensures pg_dumpall failures propagate through the gzip pipe
	cmd := exec.CommandContext(ctx, "bash", "-c",
		fmt.Sprintf("set -o pipefail; pg_dumpall -h postgres-primary.service.consul -U postgres | gzip > %s", filename))
	cmd.Env = append(os.Environ(), "PGPASSWORD="+os.Getenv("PGPASSWORD"))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("postgres backup failed: %w, output: %s", err, output)
	}

	if info, statErr := os.Stat(filename); statErr == nil {
		logger.Info("PostgreSQL backup saved", "path", filename, "size_bytes", info.Size())
	}

	return filename, nil
}

// TakeRegistryBackup creates a gzipped tarball of the container registry
// data directory including all pushed images, layers, manifests, and
// metadata.
func (a *Activities) TakeRegistryBackup(ctx context.Context) (string, error) {
	logger := activity.GetLogger(ctx)

	if err := os.MkdirAll(a.config.RegistryBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := filepath.Join(a.config.RegistryBackupDir, fmt.Sprintf("registry-%s.tar.gz", timestamp))

	cmd := exec.CommandContext(ctx, "tar", "-czf", filename,
		"-C", filepath.Dir(a.config.RegistryDataDir),
		filepath.Base(a.config.RegistryDataDir))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("registry backup failed: %w, output: %s", err, output)
	}

	if info, statErr := os.Stat(filename); statErr == nil {
		logger.Info("Registry backup saved", "path", filename, "size_bytes", info.Size())
	}

	return filename, nil
}

// -------------------------------------------------------------------------
// S3 UPLOAD ACTIVITIES
// -------------------------------------------------------------------------

// UploadToS3 uploads a local backup file to S3 storage. The S3 key is
// constructed from the prefix and the original filename. If the upload
// fails with a 507 quota error, the oldest backup under the same prefix
// is evicted and the upload is retried up to 3 times.
func (a *Activities) UploadToS3(ctx context.Context, localPath string, keyPrefix string) (string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for s3-orchestrator edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "s3.upload",
		shared.PeerServiceAttr("s3-orchestrator"),
	)
	defer span.End()

	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat file %s: %w", localPath, err)
	}

	key := keyPrefix + "/" + filepath.Base(localPath)
	bucket := a.config.S3Bucket

	const maxEvictions = 3
	for attempt := range maxEvictions + 1 {
		file, err := os.Open(localPath)
		if err != nil {
			return "", fmt.Errorf("open file %s: %w", localPath, err)
		}

		logger.Info("Uploading to S3", "key", key, "size_bytes", info.Size(), "bucket", bucket)

		_, err = a.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        &bucket,
			Key:           &key,
			Body:          file,
			ContentLength: aws.Int64(info.Size()),
		})
		_ = file.Close()

		if err == nil {
			logger.Info("S3 upload complete", "key", key, "size_bytes", info.Size())
			return key, nil
		}

		if !isQuotaError(err) || attempt == maxEvictions {
			return "", fmt.Errorf("upload %s to s3://%s/%s: %w", localPath, bucket, key, err)
		}

		logger.Warn("S3 quota exceeded, evicting oldest backup", "prefix", keyPrefix, "attempt", attempt+1)
		if evictErr := a.deleteOldestObject(ctx, keyPrefix, key); evictErr != nil {
			return "", fmt.Errorf("upload failed (quota) and eviction failed: upload: %w, evict: %v", err, evictErr)
		}
	}

	return "", fmt.Errorf("upload %s failed after %d eviction attempts", localPath, maxEvictions)
}

// -------------------------------------------------------------------------
// CLEANUP ACTIVITIES
// -------------------------------------------------------------------------

// CleanupOldBackups removes local backup files older than the retention
// period across all backup directories.
func (a *Activities) CleanupOldBackups(ctx context.Context, retentionDays int) error {
	logger := activity.GetLogger(ctx)
	logger.Info("Cleaning up old local backups", "retention_days", retentionDays)

	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	dirs := []string{
		a.config.NomadBackupDir,
		a.config.ConsulBackupDir,
		a.config.PostgresBackupDir,
		a.config.RegistryBackupDir,
	}

	for _, dir := range dirs {
		if err := cleanupDirectory(dir, cutoff, logger); err != nil {
			return fmt.Errorf("cleanup %s: %w", dir, err)
		}
	}

	return nil
}

// CleanupOldS3Backups removes S3 backup objects older than the retention
// period by listing all objects under the backups/ prefix and deleting
// those whose LastModified exceeds the cutoff.
func (a *Activities) CleanupOldS3Backups(ctx context.Context, retentionDays int) error {
	logger := activity.GetLogger(ctx)

	// Client span for s3-orchestrator edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "s3.cleanup",
		shared.PeerServiceAttr("s3-orchestrator"),
	)
	defer span.End()

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	prefix := "backups/"
	bucket := a.config.S3Bucket
	deleted := 0

	logger.Info("Cleaning up old S3 backups", "retention_days", retentionDays, "cutoff", cutoff)

	paginator := s3.NewListObjectsV2Paginator(a.s3Client, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list S3 objects: %w", err)
		}

		for _, obj := range page.Contents {
			if obj.LastModified == nil || !obj.LastModified.Before(cutoff) {
				continue
			}
			key := aws.ToString(obj.Key)

			// Skip prefix markers
			if strings.HasSuffix(key, "/") {
				continue
			}

			_, err := a.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: &bucket,
				Key:    obj.Key,
			})
			if err != nil {
				logger.Warn("Failed to delete old S3 backup", "key", key, "error", err)
				continue
			}

			logger.Info("Deleted old S3 backup", "key", key,
				"age_days", int(time.Since(*obj.LastModified).Hours()/24))
			deleted++
		}
	}

	logger.Info("S3 cleanup complete", "deleted_count", deleted)
	return nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// isQuotaError reports whether an S3 error indicates insufficient storage
// (HTTP 507), which triggers the eviction-and-retry logic in UploadToS3.
func isQuotaError(err error) bool {
	return strings.Contains(err.Error(), "InsufficientStorage") || strings.Contains(err.Error(), "507")
}

// deleteOldestObject finds and removes the oldest S3 object under the
// given prefix, skipping the key currently being uploaded. Used to free
// quota when an upload fails with 507.
func (a *Activities) deleteOldestObject(ctx context.Context, prefix, skipKey string) error {
	logger := activity.GetLogger(ctx)
	bucket := a.config.S3Bucket

	searchPrefix := prefix + "/"
	var objects []s3types.Object

	paginator := s3.NewListObjectsV2Paginator(a.s3Client, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &searchPrefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects under %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			k := aws.ToString(obj.Key)
			if k != skipKey && !strings.HasSuffix(k, "/") {
				objects = append(objects, obj)
			}
		}
	}

	if len(objects) == 0 {
		return fmt.Errorf("no objects to evict under %s", prefix)
	}

	sort.Slice(objects, func(i, j int) bool {
		return objects[i].LastModified.Before(*objects[j].LastModified)
	})

	oldest := aws.ToString(objects[0].Key)
	logger.Info("Evicting oldest S3 backup", "key", oldest,
		"age_days", int(time.Since(*objects[0].LastModified).Hours()/24))

	_, err := a.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &oldest,
	})
	return err
}

// cleanupDirectory removes backup files older than the cutoff time from a
// single directory. Handles .snap, .sql.gz, and .tar.gz extensions. Skips
// non-existent directories gracefully. Deletion failures are logged but
// do not stop the cleanup process.
func cleanupDirectory(dir string, cutoff time.Time, logger interface {
	Info(string, ...interface{})
	Warn(string, ...interface{})
}) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("Backup directory does not exist, skipping", "dir", dir)
			return nil
		}
		return fmt.Errorf("read dir %s: %w", dir, err)
	}

	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		isBackup := filepath.Ext(name) == ".snap" ||
			strings.HasSuffix(name, ".sql.gz") ||
			strings.HasSuffix(name, ".tar.gz")
		if !isBackup {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			logger.Warn("Failed to stat file", "file", name, "error", err)
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, name)
			if err := os.Remove(path); err != nil {
				logger.Warn("Failed to remove old backup", "path", path, "error", err)
			} else {
				logger.Info("Removed old backup", "path", path,
					"age_days", int(time.Since(info.ModTime()).Hours()/24))
				deleted++
			}
		}
	}

	logger.Info("Cleanup complete", "dir", dir, "deleted_count", deleted)
	return nil
}
