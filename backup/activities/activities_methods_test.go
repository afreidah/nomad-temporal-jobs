// -------------------------------------------------------------------------------
// Backup Activities - Method Integration Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs the activity methods in a TestActivityEnvironment with fakes injected for
// the s3Store / databaseLister / consulSnapshotter consumer interfaces, so the
// orchestration logic (eviction-and-retry, snapshot streaming, age-based S3
// cleanup) is exercised without any real S3, Consul, or Postgres. Also covers
// the filesystem cleanup helpers against a real temp directory.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"go.temporal.io/sdk/testsuite"
)

// --- fakes for the consumer interfaces ---------------------------------------

type fakeStore struct {
	putErrs   []error // returned per Put call, in order
	putN      int
	putKeys   []string
	objs      []s3types.Object
	listErr   error
	deleted   []string
	delErr    error
	oldestKey string
	oldestErr error
	evictions int
}

func (f *fakeStore) Put(_ context.Context, key string, _ io.Reader) error {
	f.putKeys = append(f.putKeys, key)
	var err error
	if f.putN < len(f.putErrs) {
		err = f.putErrs[f.putN]
	}
	f.putN++
	return err
}

func (f *fakeStore) ListObjects(_ context.Context, _ string) ([]s3types.Object, error) {
	return f.objs, f.listErr
}

func (f *fakeStore) DeleteObject(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return f.delErr
}

func (f *fakeStore) DeleteOldest(_ context.Context, _, _ string) (string, error) {
	f.evictions++
	return f.oldestKey, f.oldestErr
}

type fakeConsul struct {
	rc  io.ReadCloser
	err error
}

func (f *fakeConsul) SaveSnapshot(_ context.Context) (io.ReadCloser, error) { return f.rc, f.err }

type fakeLister struct {
	dbs []string
	err error
}

func (f *fakeLister) ListDatabases(_ context.Context) ([]string, error) { return f.dbs, f.err }

func actEnv() *testsuite.TestActivityEnvironment {
	return (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
}

// --- TakeConsulSnapshot ------------------------------------------------------

func TestTakeConsulSnapshot(t *testing.T) {
	dir := t.TempDir()
	a := &Activities{
		config: Config{ConsulBackupDir: dir},
		consul: &fakeConsul{rc: io.NopCloser(strings.NewReader("snap-bytes"))},
	}
	env := actEnv()
	env.RegisterActivity(a.TakeConsulSnapshot)

	val, err := env.ExecuteActivity(a.TakeConsulSnapshot)
	if err != nil {
		t.Fatalf("TakeConsulSnapshot: %v", err)
	}
	var path string
	if err := val.Get(&path); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("snapshot file not written: %v", err)
	}
	if string(got) != "snap-bytes" {
		t.Errorf("snapshot content = %q, want %q", got, "snap-bytes")
	}
}

func TestTakeConsulSnapshot_Error(t *testing.T) {
	a := &Activities{
		config: Config{ConsulBackupDir: t.TempDir()},
		consul: &fakeConsul{err: errors.New("consul down")},
	}
	env := actEnv()
	env.RegisterActivity(a.TakeConsulSnapshot)

	if _, err := env.ExecuteActivity(a.TakeConsulSnapshot); err == nil {
		t.Fatal("expected an error when SaveSnapshot fails")
	}
}

// --- ListPostgresDatabases ---------------------------------------------------

func TestListPostgresDatabases(t *testing.T) {
	a := &Activities{pg: &fakeLister{dbs: []string{"app", "metrics"}}}
	env := actEnv()
	env.RegisterActivity(a.ListPostgresDatabases)

	val, err := env.ExecuteActivity(a.ListPostgresDatabases)
	if err != nil {
		t.Fatalf("ListPostgresDatabases: %v", err)
	}
	var dbs []string
	if err := val.Get(&dbs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dbs) != 2 || dbs[0] != "app" || dbs[1] != "metrics" {
		t.Errorf("dbs = %v, want [app metrics]", dbs)
	}
}

func TestListPostgresDatabases_Error(t *testing.T) {
	a := &Activities{pg: &fakeLister{err: errors.New("no catalog")}}
	env := actEnv()
	env.RegisterActivity(a.ListPostgresDatabases)
	if _, err := env.ExecuteActivity(a.ListPostgresDatabases); err == nil {
		t.Fatal("expected an error when ListDatabases fails")
	}
}

// --- UploadToS3 --------------------------------------------------------------

func TestUploadToS3_Success(t *testing.T) {
	local := filepath.Join(t.TempDir(), "backup.snap")
	if err := os.WriteFile(local, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	a := &Activities{config: Config{S3Bucket: "b"}, store: store}
	env := actEnv()
	env.RegisterActivity(a.UploadToS3)

	val, err := env.ExecuteActivity(a.UploadToS3, local, "backups/nomad")
	if err != nil {
		t.Fatalf("UploadToS3: %v", err)
	}
	var key string
	if err := val.Get(&key); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if key != "backups/nomad/backup.snap" {
		t.Errorf("key = %q, want backups/nomad/backup.snap", key)
	}
	if store.evictions != 0 {
		t.Errorf("evictions = %d, want 0 on a clean upload", store.evictions)
	}
}

// A quota (507) error must evict the oldest backup and retry, not fail.
func TestUploadToS3_QuotaEvictsAndRetries(t *testing.T) {
	local := filepath.Join(t.TempDir(), "backup.snap")
	if err := os.WriteFile(local, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{
		putErrs:   []error{errors.New("InsufficientStorage: quota exceeded"), nil},
		oldestKey: "backups/nomad/oldest.snap",
	}
	a := &Activities{config: Config{S3Bucket: "b"}, store: store}
	env := actEnv()
	env.RegisterActivity(a.UploadToS3)

	if _, err := env.ExecuteActivity(a.UploadToS3, local, "backups/nomad"); err != nil {
		t.Fatalf("UploadToS3 should recover after eviction: %v", err)
	}
	if store.evictions != 1 {
		t.Errorf("evictions = %d, want 1", store.evictions)
	}
	if store.putN != 2 {
		t.Errorf("Put calls = %d, want 2 (initial + retry)", store.putN)
	}
}

// --- CleanupOldS3Backups -----------------------------------------------------

func TestCleanupOldS3Backups(t *testing.T) {
	old := time.Now().AddDate(0, 0, -40)
	recent := time.Now()
	store := &fakeStore{objs: []s3types.Object{
		{Key: aws.String("backups/old.snap"), LastModified: &old},
		{Key: aws.String("backups/recent.snap"), LastModified: &recent},
		{Key: aws.String("backups/nodate.snap"), LastModified: nil},
	}}
	a := &Activities{store: store}
	env := actEnv()
	env.RegisterActivity(a.CleanupOldS3Backups)

	if _, err := env.ExecuteActivity(a.CleanupOldS3Backups, 30); err != nil {
		t.Fatalf("CleanupOldS3Backups: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "backups/old.snap" {
		t.Errorf("deleted = %v, want [backups/old.snap]", store.deleted)
	}
}

// --- filesystem cleanup helpers ----------------------------------------------

type nopLog struct{}

func (nopLog) Info(string, ...any) {}
func (nopLog) Warn(string, ...any) {}

func TestCleanupDirectory(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -10)

	mk := func(name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldBackup := mk("old.snap", old)
	newBackup := mk("new.sql.gz", time.Now())
	oldOther := mk("notes.txt", old) // non-backup, must survive even though it's old

	cutoff := time.Now().AddDate(0, 0, -1)
	if err := cleanupDirectory(dir, cutoff, nopLog{}); err != nil {
		t.Fatalf("cleanupDirectory: %v", err)
	}

	if _, err := os.Stat(oldBackup); !os.IsNotExist(err) {
		t.Error("old backup should have been removed")
	}
	if _, err := os.Stat(newBackup); err != nil {
		t.Error("recent backup should have survived")
	}
	if _, err := os.Stat(oldOther); err != nil {
		t.Error("non-backup file should have survived regardless of age")
	}
}

func TestCleanupDirectory_MissingDirIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := cleanupDirectory(missing, time.Now(), nopLog{}); err != nil {
		t.Errorf("missing dir should be skipped, got %v", err)
	}
}

func TestIsBackupFile(t *testing.T) {
	cases := map[string]bool{
		"consul-20260101.snap":     true,
		"postgres-app.sql.gz":      true,
		"registry-20260101.tar.gz": true,
		"notes.txt":                false,
		"dump.sql":                 false,
		"archive.gz":               false,
		"snap":                     false,
	}
	for name, want := range cases {
		if got := isBackupFile(name); got != want {
			t.Errorf("isBackupFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// --- streamToFile ------------------------------------------------------------

func TestStreamToFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out")
	n, err := streamToFile(p, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("streamToFile: %v", err)
	}
	if n != 5 {
		t.Errorf("bytes written = %d, want 5", n)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// A copy failure must not leave a truncated artifact behind.
func TestStreamToFile_PartialIsRemoved(t *testing.T) {
	p := filepath.Join(t.TempDir(), "partial")
	if _, err := streamToFile(p, errReader{}); err == nil {
		t.Fatal("expected an error from a failing reader")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("partial file should have been removed after copy failure")
	}
}
