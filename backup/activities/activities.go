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
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"go.opentelemetry.io/otel/attribute"
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

	PostgresHost string // default: postgres-primary.service.consul
	PostgresUser string // default: postgres
}

// ApplyDefaults fills optional directory paths and Postgres connection
// settings with their defaults. Mutating; call it before Validate.
func (c *Config) ApplyDefaults() {
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
	if c.PostgresHost == "" {
		c.PostgresHost = "postgres-primary.service.consul"
	}
	if c.PostgresUser == "" {
		c.PostgresUser = "postgres"
	}
}

// Validate checks that the required S3 fields are present. Pure: it does not
// mutate the config.
func (c *Config) Validate() error {
	if c.S3Endpoint == "" {
		return errors.New("S3Endpoint is required")
	}
	if c.S3Bucket == "" {
		return errors.New("S3Bucket is required")
	}
	if c.S3AccessKey == "" {
		return errors.New("S3AccessKey is required")
	}
	if c.S3SecretKey == "" {
		return errors.New("S3SecretKey is required")
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
	nomad    *nomadapi.Client
}

// New creates an Activities instance with a pre-configured S3 client and a
// shared Nomad client (reused across invocations). Returns an error if the
// config is invalid.
func New(cfg Config) (*Activities, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	nomad, err := shared.NewNomadClient()
	if err != nil {
		return nil, fmt.Errorf("create nomad client: %w", err)
	}

	endpoint := cfg.S3Endpoint
	s3Client := s3.New(s3.Options{
		BaseEndpoint: &endpoint,
		Region:       "us-east-1", // required by SDK but ignored by s3-orchestrator
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		UsePathStyle: true,
	})

	return &Activities{config: cfg, s3Client: s3Client, nomad: nomad}, nil
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// DatabaseBackup records the outcome of backing up a single PostgreSQL
// database: the local dump path and, if the upload succeeded, its S3 key.
type DatabaseBackup struct {
	Database  string `json:"database"`
	LocalPath string `json:"local_path"`
	S3Key     string `json:"s3_key,omitempty"`
}

// BackupResult contains the outcome of a backup workflow execution. The
// PostgreSQL leg now produces one dump per database (plus a globals dump)
// rather than a single cluster-wide file.
type BackupResult struct {
	NomadSnapshot  string `json:"nomad_snapshot"`
	ConsulSnapshot string `json:"consul_snapshot"`
	NomadS3Key     string `json:"nomad_s3_key"`
	ConsulS3Key    string `json:"consul_s3_key"`

	PostgresGlobals      string           `json:"postgres_globals"`
	PostgresGlobalsS3Key string           `json:"postgres_globals_s3_key,omitempty"`
	PostgresDatabases    []DatabaseBackup `json:"postgres_databases"`

	Timestamp time.Time `json:"timestamp"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}

// BackupConfig holds workflow-level configuration passed as input so values
// are deterministic across replays.
type BackupConfig struct {
	// LocalDays is the local-backup retention window. Default 7.
	LocalDays int `json:"local_days"`
	// S3Days is the S3-backup retention window. Default 30.
	S3Days int `json:"s3_days"`
	// DumpConcurrency bounds how many per-database pg_dump activities run
	// at once so the parallel dumps don't overwhelm the primary. Default 4.
	DumpConcurrency int `json:"dump_concurrency"`
}

// ApplyDefaults fills in unset fields with their defaults. Called by the
// workflow before any activities run so the values are deterministic across
// replay.
func (c *BackupConfig) ApplyDefaults() {
	if c.LocalDays <= 0 {
		c.LocalDays = 7
	}
	if c.S3Days <= 0 {
		c.S3Days = 30
	}
	if c.DumpConcurrency <= 0 {
		c.DumpConcurrency = 4
	}
}

// -------------------------------------------------------------------------
// SNAPSHOT ACTIVITIES
// -------------------------------------------------------------------------

// TakeNomadSnapshot creates a Raft snapshot of the Nomad cluster state
// including job specs, allocations, ACLs, and scheduler configuration. Streams
// the snapshot from the Nomad API through the shared OTel-instrumented client
// (so it appears as a service-graph edge) straight to disk. Requires
// NOMAD_TOKEN with snapshot permissions in the environment.
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

	snap, err := a.nomad.Operator().Snapshot((&nomadapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("nomad snapshot: %w", err)
	}
	defer func() { _ = snap.Close() }()

	written, err := streamToFile(filename, snap)
	if err != nil {
		return "", fmt.Errorf("write nomad snapshot: %w", err)
	}

	logger.Info("Nomad snapshot saved", "path", filename, "size_bytes", written)
	return filename, nil
}

// TakeConsulSnapshot creates a Raft snapshot of the Consul cluster state
// including KV store, service catalog, ACLs, sessions, and intentions. Vault
// data is included since Vault uses Consul as its storage backend. Streams the
// snapshot from the Consul API through the shared OTel-instrumented client
// straight to disk. The ACL token comes from CONSUL_HTTP_TOKEN and the address
// from the CONSUL_* environment (default: local agent).
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

	// nil Vault client: the ACL token falls back to CONSUL_HTTP_TOKEN.
	client, err := shared.NewConsulClient(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("create consul client: %w", err)
	}
	snap, _, err := client.Snapshot().Save((&consulapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("consul snapshot: %w", err)
	}
	defer func() { _ = snap.Close() }()

	written, err := streamToFile(filename, snap)
	if err != nil {
		return "", fmt.Errorf("write consul snapshot: %w", err)
	}

	logger.Info("Consul snapshot saved", "path", filename, "size_bytes", written)
	return filename, nil
}

// ListPostgresDatabases returns the names of all non-template, connectable
// databases in the cluster. The workflow fans these out into per-database
// dumps. Queries the catalog directly through the shared instrumented client.
// Requires PGPASSWORD in the environment.
func (a *Activities) ListPostgresDatabases(ctx context.Context) ([]string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for postgres edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "postgres.list_databases",
		shared.PeerServiceAttr("postgres-primary"),
	)
	defer span.End()

	dbs, err := shared.ListDatabaseNames(ctx, shared.PostgresConfig{
		Host:     a.config.PostgresHost,
		Port:     "5432",
		User:     a.config.PostgresUser,
		Password: os.Getenv("PGPASSWORD"),
		DBName:   "postgres",
		SSLMode:  "prefer",
	})
	if err != nil {
		return nil, err
	}

	logger.Info("Discovered PostgreSQL databases", "count", len(dbs))
	return dbs, nil
}

// BackupPostgresGlobals dumps cluster-wide objects (roles, tablespaces, and
// grants) that per-database pg_dump does not capture. Without this, a
// full-cluster restore would come up with no roles or permissions. Requires
// PGPASSWORD in the environment.
func (a *Activities) BackupPostgresGlobals(ctx context.Context) (string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for postgres edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "postgres.backup_globals",
		shared.PeerServiceAttr("postgres-primary"),
	)
	defer span.End()

	if err := os.MkdirAll(a.config.PostgresBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := filepath.Join(a.config.PostgresBackupDir, fmt.Sprintf("postgres-globals-%s.sql.gz", timestamp))

	// pg_dumpall has no Go-native equivalent, so it stays a subprocess -- but
	// its stdout is gzipped in-process rather than piped through a shell, so
	// there is no bash, no pipefail, and no shell-injection surface.
	if err := runDumpToGzip(ctx, filename, 0,
		"pg_dumpall", "-h", a.config.PostgresHost, "-U", a.config.PostgresUser, "--globals-only"); err != nil {
		return "", fmt.Errorf("postgres globals dump: %w", err)
	}

	if info, statErr := os.Stat(filename); statErr == nil {
		logger.Info("PostgreSQL globals saved", "path", filename, "size_bytes", info.Size())
	}

	return filename, nil
}

// BackupPostgresDatabase dumps a single database to its own gzipped file.
// Smaller per-database files restore faster and spread across storage more
// evenly than one cluster-wide dump. Long-running for large databases, so it
// heartbeats while the dump runs. Requires PGPASSWORD in the environment.
func (a *Activities) BackupPostgresDatabase(ctx context.Context, database string) (string, error) {
	logger := activity.GetLogger(ctx)

	// Client span for postgres edge in service graph
	ctx, span := shared.StartClientSpan(ctx, "postgres.backup_database",
		attribute.String("postgres.database", database),
		shared.PeerServiceAttr("postgres-primary"),
	)
	defer span.End()

	safe := SanitizeDBName(database)
	dbDir := filepath.Join(a.config.PostgresBackupDir, safe)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := filepath.Join(dbDir, fmt.Sprintf("postgres-%s-%s.sql.gz", safe, timestamp))

	// pg_dump stays a subprocess (no Go-native equivalent); its stdout is
	// gzipped in-process and the dump heartbeats so a stall on a large
	// database trips the activity's HeartbeatTimeout. The database name is
	// passed as a direct argument, not interpolated into a shell command.
	if err := runDumpToGzip(ctx, filename, 30*time.Second,
		"pg_dump", "-h", a.config.PostgresHost, "-U", a.config.PostgresUser, "-d", database); err != nil {
		return "", fmt.Errorf("postgres dump of %q: %w", database, err)
	}

	if info, statErr := os.Stat(filename); statErr == nil {
		logger.Info("PostgreSQL database saved", "database", database, "path", filename, "size_bytes", info.Size())
	}

	return filename, nil
}

// -------------------------------------------------------------------------
// S3 UPLOAD ACTIVITIES
// -------------------------------------------------------------------------

// Multipart upload tuning: large dumps (hundreds of MB) upload as parallel
// chunks instead of one slow stream that would blow the activity timeout.
const (
	uploadPartSize          = 16 * 1024 * 1024 // 16 MB parts
	uploadConcurrency       = 4                // parallel part uploads
	uploadHeartbeatInterval = 30 * time.Second
)

// UploadToS3 uploads a local backup file to S3 storage using multipart upload.
// The S3 key is constructed from the prefix and the original filename. If the
// upload fails with a 507 quota error, the oldest backup under the same prefix
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

	uploader := manager.NewUploader(a.s3Client, func(u *manager.Uploader) {
		u.PartSize = uploadPartSize
		u.Concurrency = uploadConcurrency
	})

	const maxEvictions = 3
	for attempt := range maxEvictions + 1 {
		file, err := os.Open(localPath)
		if err != nil {
			return "", fmt.Errorf("open file %s: %w", localPath, err)
		}

		logger.Info("Uploading to S3", "key", key, "size_bytes", info.Size(), "bucket", bucket)

		err = uploadWithHeartbeat(ctx, uploader, &s3.PutObjectInput{
			Bucket: &bucket,
			Key:    &key,
			Body:   file,
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

// SanitizeDBName makes a database name safe for use in a backup path (the
// per-database subdirectory and filename, and the matching S3 prefix) by
// replacing any character outside [A-Za-z0-9._-] with an underscore.
func SanitizeDBName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// streamToFile copies r into a freshly created file at path and returns the
// number of bytes written. A partial file is removed if the copy or close
// fails, so a failed snapshot never leaves a truncated artifact behind.
func streamToFile(path string, r io.Reader) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", path, err)
	}
	n, err := io.Copy(f, r)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return 0, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return 0, fmt.Errorf("close %s: %w", path, err)
	}
	return n, nil
}

// runDumpToGzip runs a pg_dump-family command (name + args), streaming its
// stdout through gzip into filename. stderr is captured for diagnostics. When
// heartbeat > 0 the activity heartbeats while the dump runs, so a stall on a
// large database trips the activity's HeartbeatTimeout. A partial output file
// is removed on failure.
func runDumpToGzip(ctx context.Context, filename string, heartbeat time.Duration, name string, args ...string) error {
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create %s: %w", filename, err)
	}
	gz := gzip.NewWriter(f)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+os.Getenv("PGPASSWORD"))
	cmd.Stdout = gz
	cmd.Stderr = &stderr

	var runErr error
	if heartbeat > 0 {
		_, runErr = shared.WithHeartbeat(ctx, heartbeat, func() (struct{}, error) {
			return struct{}{}, cmd.Run()
		})
	} else {
		runErr = cmd.Run()
	}
	if runErr != nil {
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(filename)
		return fmt.Errorf("%s: %w (stderr: %s)", name, runErr, strings.TrimSpace(stderr.String()))
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(filename)
		return fmt.Errorf("flush gzip %s: %w", filename, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(filename)
		return fmt.Errorf("close %s: %w", filename, err)
	}
	return nil
}

// uploadWithHeartbeat runs a multipart upload while heartbeating, so a stalled
// upload trips the activity's HeartbeatTimeout instead of silently running to
// the StartToClose timeout. The uploader respects ctx cancellation, so on
// timeout it returns promptly.
func uploadWithHeartbeat(ctx context.Context, uploader *manager.Uploader, input *s3.PutObjectInput) error {
	_, err := shared.WithHeartbeat(ctx, uploadHeartbeatInterval, func() (struct{}, error) {
		_, e := uploader.Upload(ctx, input)
		return struct{}{}, e
	})
	return err
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

	oldestObj := slices.MinFunc(objects, func(x, y s3types.Object) int {
		return x.LastModified.Compare(*y.LastModified)
	})

	oldest := aws.ToString(oldestObj.Key)
	logger.Info("Evicting oldest S3 backup", "key", oldest,
		"age_days", int(time.Since(*oldestObj.LastModified).Hours()/24))

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
	Info(string, ...any)
	Warn(string, ...any)
}) error {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			logger.Info("Backup directory does not exist, skipping", "dir", dir)
			return nil
		}
		return fmt.Errorf("stat dir %s: %w", dir, err)
	}

	// WalkDir recurses so per-database subdirectories are cleaned too.
	deleted := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		isBackup := filepath.Ext(name) == ".snap" ||
			strings.HasSuffix(name, ".sql.gz") ||
			strings.HasSuffix(name, ".tar.gz")
		if !isBackup {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			logger.Warn("Failed to stat file", "file", name, "error", err)
			return nil
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				logger.Warn("Failed to remove old backup", "path", path, "error", err)
			} else {
				logger.Info("Removed old backup", "path", path,
					"age_days", int(time.Since(info.ModTime()).Hours()/24))
				deleted++
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk dir %s: %w", dir, err)
	}

	logger.Info("Cleanup complete", "dir", dir, "deleted_count", deleted)
	return nil
}
