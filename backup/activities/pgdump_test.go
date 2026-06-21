// -------------------------------------------------------------------------------
// Backup Activities - Local Cleanup & Postgres Dump Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// CleanupOldBackups runs against real temp directories. The pg_dump/pg_dumpall
// activities are pointed at an unresolvable host (.invalid never resolves, RFC
// 6761) so the subprocess fails deterministically -- covering runDumpToGzip's
// failure path and the activities' error wrapping without a real Postgres.
// -------------------------------------------------------------------------------

package activities

import (
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
