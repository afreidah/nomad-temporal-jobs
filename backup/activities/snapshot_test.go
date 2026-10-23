// -------------------------------------------------------------------------------
// Backup Activities - Nomad Snapshot Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives TakeNomadSnapshot against a fake nomadSnapshotter so the stream-to-file
// path is covered without a real cluster (the live *nomadapi.Client adapter is
// nomadSnap). Also covers the snapshot, mkdir, and stream error paths, and the
// nomadSnap adapter itself against a local httptest server.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
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

func TestTakeNomadSnapshot_MkdirFails(t *testing.T) {
	a := &Activities{
		config: Config{NomadBackupDir: blockedDir(t)},
		nomad:  fakeSnapshotter{data: []byte("x")},
	}
	env := actEnv()
	env.RegisterActivity(a.TakeNomadSnapshot)

	if _, err := env.ExecuteActivity(a.TakeNomadSnapshot); err == nil {
		t.Fatal("expected error when the backup dir cannot be created")
	}
}

type errSnapshotter struct{}

func (errSnapshotter) Snapshot(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(errReader{}), nil
}

func TestTakeNomadSnapshot_StreamFails(t *testing.T) {
	a := &Activities{
		config: Config{NomadBackupDir: t.TempDir()},
		nomad:  errSnapshotter{},
	}
	env := actEnv()
	env.RegisterActivity(a.TakeNomadSnapshot)

	if _, err := env.ExecuteActivity(a.TakeNomadSnapshot); err == nil {
		t.Fatal("expected error when the snapshot stream fails mid-copy")
	}
}

// nomadSnap drives the real Nomad API client against a local test server
// that serves a snapshot with a valid sha-256 Digest header.
func TestNomadSnap_Snapshot(t *testing.T) {
	body := []byte("raft-bytes")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/operator/snapshot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(sum[:]))
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := nomadapi.NewClient(&nomadapi.Config{Address: srv.URL})
	if err != nil {
		t.Fatalf("nomad client: %v", err)
	}
	rc, err := nomadSnap{c: c}.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("snapshot = %q, want %q", got, body)
	}
}
