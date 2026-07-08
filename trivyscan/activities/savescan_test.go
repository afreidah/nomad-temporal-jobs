// -------------------------------------------------------------------------------
// Trivy Activities - SaveScanResult Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives SaveScanResult against a sqlmock DB so the transaction path (insert
// scan, insert vulnerabilities, commit) is covered without a real Postgres.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.temporal.io/sdk/testsuite"
)

func TestSaveScanResult(t *testing.T) {
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

	a := &Activities{db: db}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.SaveScanResult)

	result := ScanResult{
		Image:     "nginx:latest",
		Status:    "completed",
		ScannedAt: time.Now(),
		Vulnerabilities: []Vulnerability{
			{VulnID: "CVE-2024-0001", Severity: "HIGH", PkgName: "openssl"},
		},
	}
	if _, err := env.ExecuteActivity(a.SaveScanResult, result); err != nil {
		t.Fatalf("SaveScanResult: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestSaveScanResultBeginError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))

	a := &Activities{db: db}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.SaveScanResult)

	if _, err := env.ExecuteActivity(a.SaveScanResult, ScanResult{Image: "x"}); err == nil {
		t.Fatal("expected error when BeginTx fails")
	}
}

// TestStoreScan_Success drives the inline persist path used by ScanImage:
// parse trivy JSON, write the scan + CVE rows, and return a summary that
// carries only the counts (never the vulnerability list, which stays in
// Postgres so it can't blow the Temporal payload limit).
func TestStoreScan_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO scans").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
	mock.ExpectExec("INSERT INTO vulnerabilities").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	a := &Activities{db: db}
	raw := []byte(`{"Results":[{"Vulnerabilities":[
		{"VulnerabilityID":"CVE-2024-0001","Severity":"CRITICAL","PkgName":"openssl"}
	]}]}`)

	summary, err := a.storeScan(context.Background(), "nginx:latest", raw, time.Now())
	if err != nil {
		t.Fatalf("storeScan: %v", err)
	}
	if summary.Status != "success" {
		t.Errorf("Status = %q, want success", summary.Status)
	}
	if summary.CriticalCount != 1 {
		t.Errorf("CriticalCount = %d, want 1", summary.CriticalCount)
	}
	if len(summary.Vulnerabilities) != 0 {
		t.Errorf("summary must not carry the CVE list, got %d entries", len(summary.Vulnerabilities))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

// TestStoreScan_ParseError verifies invalid trivy output fails before any DB
// access (a nil db would panic if the parse guard were missing).
func TestStoreScan_ParseError(t *testing.T) {
	a := &Activities{}
	if _, err := a.storeScan(context.Background(), "x", []byte("not json"), time.Now()); err == nil {
		t.Fatal("expected a parse error")
	}
}

// TestStoreScan_PersistError verifies a DB failure during persistence is
// surfaced (so Temporal retries the scan) rather than silently dropped.
func TestStoreScan_PersistError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))

	a := &Activities{db: db}
	raw := []byte(`{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-1","Severity":"LOW"}]}]}`)
	if _, err := a.storeScan(context.Background(), "x", raw, time.Now()); err == nil {
		t.Fatal("expected a persist error")
	}
}

// TestSaveScanResult_InsertScanError surfaces a failure inserting the scan row.
func TestSaveScanResult_InsertScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO scans").WillReturnError(errors.New("scans table gone"))
	mock.ExpectRollback()

	a := &Activities{db: db}
	if err := a.saveScanResult(context.Background(), ScanResult{Image: "octo/app:latest", Status: "error"}); err == nil {
		t.Fatal("expected an error when the scan insert fails")
	}
}

// TestSaveScanResult_InsertVulnError surfaces a failure inserting a CVE row
// after the scan row succeeded.
func TestSaveScanResult_InsertVulnError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO scans").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	mock.ExpectExec("INSERT INTO vulnerabilities").WillReturnError(errors.New("vuln insert failed"))
	mock.ExpectRollback()

	a := &Activities{db: db}
	res := ScanResult{
		Image:           "octo/app:latest",
		Status:          "success",
		Vulnerabilities: []Vulnerability{{VulnID: "CVE-1", Severity: "LOW"}},
	}
	if err := a.saveScanResult(context.Background(), res); err == nil {
		t.Fatal("expected an error when a vulnerability insert fails")
	}
}

// TestSaveScanResult_CommitError surfaces a commit failure at the end of the tx.
func TestSaveScanResult_CommitError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO scans").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	a := &Activities{db: db}
	if err := a.saveScanResult(context.Background(), ScanResult{Image: "octo/app:latest", Status: "error"}); err == nil {
		t.Fatal("expected an error when the commit fails")
	}
}

func TestClose(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.ExpectClose()

	a := &Activities{db: db}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
