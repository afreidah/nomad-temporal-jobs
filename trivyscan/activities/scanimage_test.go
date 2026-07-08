// -------------------------------------------------------------------------------
// Trivy Activities - ScanImage Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives ScanImage through the injected trivyRunner seam so the parse/persist
// happy path and every error-classification branch are covered without a real
// trivy binary or Postgres. execTrivy (the production runner) is exercised
// separately via a canceled context.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"go.temporal.io/sdk/testsuite"
)

// stubTrivy returns a trivyRunner that yields fixed output, ignoring its args.
func stubTrivy(stdout, stderr string, err error) trivyRunner {
	return func(context.Context, string, string) ([]byte, string, error) {
		return []byte(stdout), stderr, err
	}
}

// TestScanImage_Success drives the full success path: the runner yields trivy
// JSON, storeScan persists it, and ScanImage returns a summary carrying only
// the severity counts (the CVE list stays in Postgres).
func TestScanImage_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO scans").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectExec("INSERT INTO vulnerabilities").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	raw := `{"Results":[{"Vulnerabilities":[
		{"VulnerabilityID":"CVE-2024-0001","Severity":"CRITICAL","PkgName":"openssl"}
	]}]}`
	a := &Activities{db: db, runTrivy: stubTrivy(raw, "", nil)}

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.ScanImage)
	val, err := env.ExecuteActivity(a.ScanImage, "octo/app:latest")
	if err != nil {
		t.Fatalf("ScanImage: %v", err)
	}
	var res ScanResult
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != "success" || res.CriticalCount != 1 {
		t.Errorf("result = %+v, want status success / 1 critical", res)
	}
	if len(res.Vulnerabilities) != 0 {
		t.Errorf("summary must not carry the CVE list, got %d", len(res.Vulnerabilities))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

// TestScanImage_ErrorClassification checks each trivy failure class maps to the
// right surfaced error: a missing image is permanent, a network failure is
// retryable, and anything unclassified falls through to a generic scan error.
func TestScanImage_ErrorClassification(t *testing.T) {
	cases := []struct {
		name    string
		stderr  string
		wantSub string
	}{
		{"permanent (image missing)", "manifest unknown: not found", "image not found"},
		{"transient (server down)", "connection refused", "trivy server unavailable"},
		{"default (unclassified)", "some other failure", "scan failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Activities{runTrivy: stubTrivy("", tc.stderr, errors.New("exit 1"))}
			env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
			env.RegisterActivity(a.ScanImage)

			_, err := env.ExecuteActivity(a.ScanImage, "octo/app:latest")
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestScanImage_StoreError verifies a persistence failure after a successful
// scan is surfaced (so Temporal retries) rather than reported as success.
func TestScanImage_StoreError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))

	raw := `{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-1","Severity":"LOW"}]}]}`
	a := &Activities{db: db, runTrivy: stubTrivy(raw, "", nil)}

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.ScanImage)
	if _, err := env.ExecuteActivity(a.ScanImage, "octo/app:latest"); err == nil {
		t.Fatal("expected an error when persistence fails")
	}
}

// TestExecTrivy_CanceledContext exercises the production runner's command build
// and Run without needing a trivy install: a canceled context makes Run fail
// immediately.
func TestExecTrivy_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := execTrivy(ctx, "octo/app:latest", "127.0.0.1:0"); err == nil {
		t.Fatal("expected an error running trivy with a canceled context")
	}
}
