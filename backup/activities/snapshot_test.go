// -------------------------------------------------------------------------------
// Backup Activities - Nomad Snapshot Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives TakeNomadSnapshot against a fake nomadSnapshotter so the stream-to-file
// path is covered without a real cluster (the live *nomadapi.Client adapter is
// nomadSnap). Also covers the snapshot-error path.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

type fakeSnapshotter struct {
	data []byte
	err  error
}

func (f fakeSnapshotter) Snapshot(context.Context) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

func TestTakeNomadSnapshot(t *testing.T) {
	a := &Activities{
		config: Config{NomadBackupDir: t.TempDir()},
		nomad:  fakeSnapshotter{data: []byte("nomad-raft-snapshot-bytes")},
	}
	env := actEnv()
	env.RegisterActivity(a.TakeNomadSnapshot)

	val, err := env.ExecuteActivity(a.TakeNomadSnapshot)
	if err != nil {
		t.Fatalf("TakeNomadSnapshot: %v", err)
	}
	var path string
	if err := val.Get(&path); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("snapshot file not written: %v", err)
	}
	if string(got) != "nomad-raft-snapshot-bytes" {
		t.Errorf("snapshot content = %q", got)
	}
}

func TestTakeNomadSnapshot_Error(t *testing.T) {
	a := &Activities{
		config: Config{NomadBackupDir: t.TempDir()},
		nomad:  fakeSnapshotter{err: errors.New("nomad unreachable")},
	}
	env := actEnv()
	env.RegisterActivity(a.TakeNomadSnapshot)

	if _, err := env.ExecuteActivity(a.TakeNomadSnapshot); err == nil {
		t.Fatal("expected error when the snapshot API fails")
	}
}
