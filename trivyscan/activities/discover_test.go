// -------------------------------------------------------------------------------
// Trivy Scan Activities - Image Discovery Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs GetRunningImages in a TestActivityEnvironment with a fake imageDiscoverer,
// so the activity's pass-through + error handling is covered without Nomad.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/testsuite"
)

type fakeDiscoverer struct {
	images []string
	err    error
}

func (f *fakeDiscoverer) RunningImages(_ context.Context) ([]string, error) {
	return f.images, f.err
}

func TestGetRunningImages(t *testing.T) {
	a := &Activities{nomad: &fakeDiscoverer{images: []string{"nginx:1.27", "redis:7"}}}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.GetRunningImages)

	val, err := env.ExecuteActivity(a.GetRunningImages)
	if err != nil {
		t.Fatalf("GetRunningImages: %v", err)
	}
	var imgs []string
	if err := val.Get(&imgs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(imgs) != 2 || imgs[0] != "nginx:1.27" || imgs[1] != "redis:7" {
		t.Errorf("images = %v, want [nginx:1.27 redis:7]", imgs)
	}
}

func TestGetRunningImages_Error(t *testing.T) {
	a := &Activities{nomad: &fakeDiscoverer{err: errors.New("nomad unreachable")}}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.GetRunningImages)

	if _, err := env.ExecuteActivity(a.GetRunningImages); err == nil {
		t.Fatal("expected an error when RunningImages fails")
	}
}
