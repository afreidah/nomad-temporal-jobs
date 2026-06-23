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
