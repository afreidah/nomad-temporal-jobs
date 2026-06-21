// -------------------------------------------------------------------------------
// Postgres Maintenance Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs the maintenance activities with a fake pgMaintainer, covering the
// list/vacuum pass-through and error handling without a real Postgres.
// -------------------------------------------------------------------------------

package postgresmaint

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/testsuite"
)

type fakePG struct {
	dbs      []string
	listErr  error
	vacErr   error
	vacuumed []string
}

func (f *fakePG) ListDatabases(_ context.Context) ([]string, error) { return f.dbs, f.listErr }

func (f *fakePG) VacuumAnalyze(_ context.Context, db string) error {
	f.vacuumed = append(f.vacuumed, db)
	return f.vacErr
}

func actEnv() *testsuite.TestActivityEnvironment {
	return (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
}

func TestListPostgresDatabases(t *testing.T) {
	a := New(&fakePG{dbs: []string{"app", "metrics"}})
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
	if len(dbs) != 2 {
		t.Errorf("dbs = %v, want 2", dbs)
	}
}

func TestListPostgresDatabases_Error(t *testing.T) {
	a := New(&fakePG{listErr: errors.New("primary unreachable")})
	env := actEnv()
	env.RegisterActivity(a.ListPostgresDatabases)
	if _, err := env.ExecuteActivity(a.ListPostgresDatabases); err == nil {
		t.Fatal("expected an error when ListDatabases fails")
	}
}

func TestVacuumAnalyzeDatabase(t *testing.T) {
	pg := &fakePG{}
	a := New(pg)
	env := actEnv()
	env.RegisterActivity(a.VacuumAnalyzeDatabase)

	if _, err := env.ExecuteActivity(a.VacuumAnalyzeDatabase, "app"); err != nil {
		t.Fatalf("VacuumAnalyzeDatabase: %v", err)
	}
	if len(pg.vacuumed) != 1 || pg.vacuumed[0] != "app" {
		t.Errorf("vacuumed = %v, want [app]", pg.vacuumed)
	}
}

func TestVacuumAnalyzeDatabase_Error(t *testing.T) {
	a := New(&fakePG{vacErr: errors.New("vacuum blocked")})
	env := actEnv()
	env.RegisterActivity(a.VacuumAnalyzeDatabase)
	if _, err := env.ExecuteActivity(a.VacuumAnalyzeDatabase, "app"); err == nil {
		t.Fatal("expected an error when VacuumAnalyze fails")
	}
}
