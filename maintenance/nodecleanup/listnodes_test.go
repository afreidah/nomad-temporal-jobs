// -------------------------------------------------------------------------------
// Node Cleanup Activities - Node Listing Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs GetAllNomadClientNodes with a fake nomadClient, covering the NomadNode ->
// NodeInfo mapping and the oracle-host detection without a real Nomad cluster.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"
)

type fakeNomad struct {
	nodes   []shared.NomadNode
	nodeErr error
	jobs    map[string]struct{}
	jobErr  error
}

func (f *fakeNomad) ClientNodes(_ context.Context) ([]shared.NomadNode, error) {
	return f.nodes, f.nodeErr
}

func (f *fakeNomad) RunningJobIDs(_ context.Context, _ string) (map[string]struct{}, error) {
	return f.jobs, f.jobErr
}

func TestGetAllNomadClientNodes(t *testing.T) {
	a := &Activities{nomad: &fakeNomad{nodes: []shared.NomadNode{
		{ID: "n1", Name: "worker-1", Address: "10.0.0.1", HTTPAddr: "10.0.0.1:4646"},
		{ID: "n2", Name: "oracle-arm-1", Address: "10.0.0.2", HTTPAddr: "10.0.0.2:4646"},
	}}}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.GetAllNomadClientNodes)

	val, err := env.ExecuteActivity(a.GetAllNomadClientNodes)
	if err != nil {
		t.Fatalf("GetAllNomadClientNodes: %v", err)
	}
	var infos []nodes.NodeInfo
	if err := val.Get(&infos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d nodes, want 2", len(infos))
	}
	if infos[0].IsOracle {
		t.Error("worker-1 should not be flagged oracle")
	}
	if !infos[1].IsOracle {
		t.Error("oracle-arm-1 should be flagged oracle (name prefix)")
	}
	if infos[1].Address != "10.0.0.2" {
		t.Errorf("address mapping wrong: %q", infos[1].Address)
	}
}

func TestGetAllNomadClientNodes_Error(t *testing.T) {
	a := &Activities{nomad: &fakeNomad{nodeErr: errors.New("nomad down")}}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.GetAllNomadClientNodes)

	if _, err := env.ExecuteActivity(a.GetAllNomadClientNodes); err == nil {
		t.Fatal("expected an error when ClientNodes fails")
	}
}
