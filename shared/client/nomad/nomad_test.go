// -------------------------------------------------------------------------------
// Shared Nomad Helpers - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the pure helpers extracted for reuse across the trivyscan and
// nodecleanup workers: SSH-address resolution, the running-alloc filter, and
// job-not-found classification.
// -------------------------------------------------------------------------------

package nomad

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/nomad/api"
)

func TestNodeSSHAddress(t *testing.T) {
	tests := []struct {
		name string
		node *api.Node
		want string
	}{
		{
			name: "prefers ip-address attribute",
			node: &api.Node{
				Attributes: map[string]string{"unique.network.ip-address": "10.0.0.7"},
				HTTPAddr:   "10.0.0.7:4646",
			},
			want: "10.0.0.7",
		},
		{
			name: "falls back to HTTPAddr with port stripped",
			node: &api.Node{HTTPAddr: "192.168.1.5:4646"},
			want: "192.168.1.5",
		},
		{
			name: "HTTPAddr without a port",
			node: &api.Node{HTTPAddr: "192.168.1.5"},
			want: "192.168.1.5",
		},
		{
			name: "empty attribute falls through to HTTPAddr",
			node: &api.Node{
				Attributes: map[string]string{"unique.network.ip-address": ""},
				HTTPAddr:   "172.16.0.1:4646",
			},
			want: "172.16.0.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeSSHAddress(tt.node); got != tt.want {
				t.Errorf("NodeSSHAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunningAllocStubs(t *testing.T) {
	allocs := []*api.AllocationListStub{
		{ID: "a", ClientStatus: api.AllocClientStatusRunning},
		{ID: "b", ClientStatus: api.AllocClientStatusComplete},
		{ID: "c", ClientStatus: api.AllocClientStatusRunning},
		{ID: "d", ClientStatus: api.AllocClientStatusFailed},
	}

	got := RunningAllocStubs(allocs)
	if len(got) != 2 {
		t.Fatalf("got %d running allocs, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("got ids %q, %q; want a, c", got[0].ID, got[1].ID)
	}

	if n := len(RunningAllocStubs(nil)); n != 0 {
		t.Errorf("nil input: got %d, want 0", n)
	}
}

// IsJobNotFound's typed-status branch (api.UnexpectedResponseError with a 404)
// fires against a live Nomad client but can't be constructed here -- the type's
// fields and constructor are unexported -- so these cases exercise the string
// fallback, which is what classifies wrapped errors in practice.
func TestIsJobNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"404 status in message", errors.New("Unexpected response code: 404"), true},
		{"job not found phrase", errors.New(`error scaling job: "job not found"`), true},
		{"wrapped job not found", fmt.Errorf("scale web/group: %w", errors.New("job not found")), true},
		{"unrelated transient error", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsJobNotFound(tt.err); got != tt.want {
				t.Errorf("IsJobNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCollectDockerImages(t *testing.T) {
	job := &api.Job{
		TaskGroups: []*api.TaskGroup{
			{Tasks: []*api.Task{
				{Driver: "docker", Config: map[string]any{"image": "nginx:1.27"}},
				{Driver: "docker", Config: map[string]any{"image": "redis:7"}},
				{Driver: "docker", Config: map[string]any{"image": "nginx:1.27"}},  // dup collapses
				{Driver: "exec", Config: map[string]any{"image": "ignored"}},       // non-docker driver
				{Driver: "docker", Config: nil},                                    // no config
				{Driver: "docker", Config: map[string]any{"image": ""}},            // empty image
				{Driver: "docker", Config: map[string]any{"command": "/bin/true"}}, // image absent
			}},
		},
	}

	set := make(map[string]struct{})
	collectDockerImages(job, set)

	want := map[string]struct{}{"nginx:1.27": {}, "redis:7": {}}
	if len(set) != len(want) {
		t.Fatalf("got %d images %v, want %d %v", len(set), set, len(want), want)
	}
	for img := range want {
		if _, ok := set[img]; !ok {
			t.Errorf("missing expected image %q", img)
		}
	}
}

func TestMainDockerImage(t *testing.T) {
	prestart := &api.TaskLifecycle{Hook: "prestart"}
	poststart := &api.TaskLifecycle{Hook: "poststart"}

	tests := []struct {
		name    string
		jobName string
		job     *api.Job
		want    string
		wantOK  bool
	}{
		{
			// Mirrors the real aptly job: two alpine prestart sidecars and a curl
			// poststart sidecar precede the aptly workload. The task named after
			// the job must win over the earlier sidecar images.
			name:    "picks task named after job over earlier sidecars",
			jobName: "aptly",
			job: &api.Job{TaskGroups: []*api.TaskGroup{{Tasks: []*api.Task{
				{Name: "setup-gpg", Driver: "docker", Lifecycle: prestart, Config: map[string]any{"image": "alpine:3.23.4"}},
				{Name: "setup-webui", Driver: "docker", Lifecycle: prestart, Config: map[string]any{"image": "alpine:3.23.4"}},
				{Name: "setup-repo", Driver: "docker", Lifecycle: poststart, Config: map[string]any{"image": "curlimages/curl:8.20.0"}},
				{Name: "aptly", Driver: "docker", Config: map[string]any{"image": "urpylka/aptly:1.6.3"}},
			}}}},
			want:   "urpylka/aptly:1.6.3",
			wantOK: true,
		},
		{
			// No name match: fall through to the first task with no lifecycle hook,
			// skipping the prestart sidecar ahead of it.
			name:    "falls back to first non-lifecycle task",
			jobName: "other",
			job: &api.Job{TaskGroups: []*api.TaskGroup{{Tasks: []*api.Task{
				{Name: "init", Driver: "docker", Lifecycle: prestart, Config: map[string]any{"image": "busybox:1"}},
				{Name: "app", Driver: "docker", Config: map[string]any{"image": "app:2"}},
			}}}},
			want:   "app:2",
			wantOK: true,
		},
		{
			// Only lifecycle sidecars exist -> last-resort fallback to any docker task.
			name:    "falls back to any docker task when all are sidecars",
			jobName: "x",
			job: &api.Job{TaskGroups: []*api.TaskGroup{{Tasks: []*api.Task{
				{Name: "only", Driver: "docker", Lifecycle: prestart, Config: map[string]any{"image": "sidecar:1"}},
			}}}},
			want:   "sidecar:1",
			wantOK: true,
		},
		{
			// Non-docker tasks and imageless docker tasks are skipped entirely.
			name:    "no docker image",
			jobName: "x",
			job: &api.Job{TaskGroups: []*api.TaskGroup{{Tasks: []*api.Task{
				{Name: "e", Driver: "exec", Config: map[string]any{"image": "ignored"}},
				{Name: "d", Driver: "docker", Config: map[string]any{"image": ""}},
			}}}},
			want:   "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, ok := mainDockerImage(tt.job, tt.jobName)
			if img != tt.want || ok != tt.wantOK {
				t.Errorf("mainDockerImage = (%q, %v), want (%q, %v)", img, ok, tt.want, tt.wantOK)
			}
		})
	}
}
