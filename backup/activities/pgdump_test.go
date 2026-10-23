// -------------------------------------------------------------------------------
// Backup Activities - Local Cleanup & Postgres Dump Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// CleanupOldBackups runs against real temp directories. The pg_dump/pg_dumpall
// activities are pointed at an unresolvable host (.invalid never resolves, RFC
// 6761) so the subprocess fails deterministically -- covering runDumpToGzip's
// failure path and the activities' error wrapping without a real Postgres. The
// success paths run a fake pg_dump/pg_dumpall script placed first on PATH.
// -------------------------------------------------------------------------------

package activities

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupOldBackups(t *testing.T) {
	dir := t.TempDir()
	oldBackup := filepath.Join(dir, "consul-old.snap")
	if err := os.WriteFile(oldBackup, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(oldBackup, old, old); err != nil {
		t.Fatal(err)
	}

	a := &Activities{config: Config{
		NomadBackupDir:    dir,
		ConsulBackupDir:   t.TempDir(),
		PostgresBackupDir: t.TempDir(),
		RegistryBackupDir: t.TempDir(),
	}}
	env := actEnv()
	env.RegisterActivity(a.CleanupOldBackups)

	if _, err := env.ExecuteActivity(a.CleanupOldBackups, 7); err != nil {
		t.Fatalf("CleanupOldBackups: %v", err)
	}
	if _, err := os.Stat(oldBackup); !os.IsNotExist(err) {
		t.Error("backup older than the retention period should have been removed")
	}
}

func TestBackupPostgresGlobals_DumpFails(t *testing.T) {
	a := &Activities{config: Config{
		PostgresBackupDir: t.TempDir(),
		PostgresHost:      "no-such-host.invalid",
		PostgresUser:      "u",
	}}
	env := actEnv()
	env.RegisterActivity(a.BackupPostgresGlobals)
	if _, err := env.ExecuteActivity(a.BackupPostgresGlobals); err == nil {
		t.Fatal("expected an error: pg_dumpall cannot reach an unresolvable host")
	}
}

func TestBackupPostgresDatabase_DumpFails(t *testing.T) {
	a := &Activities{config: Config{
		PostgresBackupDir: t.TempDir(),
		PostgresHost:      "no-such-host.invalid",
		PostgresUser:      "u",
	}}
	env := actEnv()
	env.RegisterActivity(a.BackupPostgresDatabase)
	if _, err := env.ExecuteActivity(a.BackupPostgresDatabase, "appdb"); err == nil {
		t.Fatal("expected an error: pg_dump cannot reach an unresolvable host")
	}
}

// A backup dir that cannot be created fails CleanupOldBackups with a wrapped
// stat error rather than being skipped as missing.
func TestCleanupOldBackups_StatFails(t *testing.T) {
	a := &Activities{config: Config{NomadBackupDir: blockedDir(t)}}
	env := actEnv()
	env.RegisterActivity(a.CleanupOldBackups)
	if _, err := env.ExecuteActivity(a.CleanupOldBackups, 7); err == nil {
		t.Fatal("expected an error when a backup dir cannot be stat'd")
	}
}

// fakeDumpBin puts a shell script named name ahead of the real binaries on
// PATH. It prints a fixed SQL line so the gzip success path is exercised
// without a real Postgres.
func fakeDumpBin(t *testing.T, name string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho '-- fake dump'\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// readGzip returns the decompressed contents of a gzip file.
func readGzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open dump: %v", err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	return string(b)
}

func TestBackupPostgresGlobals_Success(t *testing.T) {
	fakeDumpBin(t, "pg_dumpall")
	a := &Activities{config: Config{PostgresBackupDir: t.TempDir(), PostgresHost: "h", PostgresUser: "u"}}
	env := actEnv()
	env.RegisterActivity(a.BackupPostgresGlobals)

	val, err := env.ExecuteActivity(a.BackupPostgresGlobals)
	if err != nil {
		t.Fatalf("BackupPostgresGlobals: %v", err)
	}
	var path string
	if err := val.Get(&path); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := readGzip(t, path); got != "-- fake dump\n" {
		t.Errorf("dump content = %q", got)
	}
}

func TestBackupPostgresDatabase_Success(t *testing.T) {
	fakeDumpBin(t, "pg_dump")
	dir := t.TempDir()
	a := &Activities{config: Config{PostgresBackupDir: dir, PostgresHost: "h", PostgresUser: "u"}}
	env := actEnv()
	env.RegisterActivity(a.BackupPostgresDatabase)

	val, err := env.ExecuteActivity(a.BackupPostgresDatabase, "app db")
	if err != nil {
		t.Fatalf("BackupPostgresDatabase: %v", err)
	}
	var path string
	if err := val.Get(&path); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(dir, "app_db") {
		t.Errorf("dump path = %q, want it under the sanitized per-database dir", path)
	}
	if got := readGzip(t, path); got != "-- fake dump\n" {
		t.Errorf("dump content = %q", got)
	}
}

func TestBackupPostgresGlobals_MkdirFails(t *testing.T) {
	a := &Activities{config: Config{PostgresBackupDir: blockedDir(t)}}
	env := actEnv()
	env.RegisterActivity(a.BackupPostgresGlobals)
	if _, err := env.ExecuteActivity(a.BackupPostgresGlobals); err == nil {
		t.Fatal("expected an error when the backup dir cannot be created")
	}
}

func TestBackupPostgresDatabase_MkdirFails(t *testing.T) {
	a := &Activities{config: Config{PostgresBackupDir: blockedDir(t)}}
	env := actEnv()
	env.RegisterActivity(a.BackupPostgresDatabase)
	if _, err := env.ExecuteActivity(a.BackupPostgresDatabase, "appdb"); err == nil {
		t.Fatal("expected an error when the per-database dir cannot be created")
	}
}

func TestRunDumpToGzip_CreateFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "out.sql.gz")
	if err := runDumpToGzip(context.Background(), missing, 0, "true"); err == nil {
		t.Fatal("expected an error when the output file cannot be created")
	}
}
