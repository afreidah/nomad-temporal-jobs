// -------------------------------------------------------------------------------
// Trivy Scan Activities - Vulnerability Scanning for Container Images
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Implements Temporal activities for discovering running container images in
// Nomad, scanning them with Trivy server, and storing CVE results in
// PostgreSQL. All methods on the Activities struct share a pooled DB
// connection and pre-configured clients.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"munchbox/temporal-workers/shared"
)

// attrTrivyError is the span attribute key for a recorded Trivy scan error.
const attrTrivyError = "trivy.error"

// trivyBin is the absolute path to the Trivy binary in the runtime image. Using
// a fixed path avoids resolving the command through PATH (which could include a
// writable directory).
const trivyBin = "/usr/local/bin/trivy"

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// Config holds environment-driven settings for trivy scan activities.
type Config struct {
	TrivyServerAddr string
	DBHost          string
	DBPort          string
	DBUser          string
	DBPassword      string
	DBName          string
	DBSSLMode       string
	DBSSLRootCert   string
}

// Validate checks that required fields are present.
func (c Config) Validate() error {
	if c.TrivyServerAddr == "" {
		return errors.New("TrivyServerAddr is required")
	}
	if c.DBHost == "" {
		return errors.New("DBHost is required")
	}
	if c.DBUser == "" {
		return errors.New("DBUser is required")
	}
	if c.DBPassword == "" {
		return errors.New("DBPassword is required")
	}
	return nil
}

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// imageDiscoverer is the trivy worker's view of shared.Nomad -- it only discovers
// running container images. *shared.Nomad satisfies it structurally.
type imageDiscoverer interface {
	RunningImages(ctx context.Context) ([]string, error)
}

// Activities holds shared dependencies for trivy scan activities. Register
// an instance with the Temporal worker to expose all exported methods as
// activity implementations.
type Activities struct {
	config Config
	db     *sql.DB
	nomad  imageDiscoverer
}

// New creates an Activities instance with a pooled database connection and a
// shared Nomad client (reused across activity invocations rather than rebuilt
// per call).
func New(cfg Config) (*Activities, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	nomad, err := shared.NewNomad()
	if err != nil {
		return nil, fmt.Errorf("create nomad client: %w", err)
	}

	db, err := shared.NewPostgresDB(shared.PostgresConfig{
		Host:        cfg.DBHost,
		Port:        cfg.DBPort,
		User:        cfg.DBUser,
		Password:    cfg.DBPassword,
		DBName:      cfg.DBName,
		SSLMode:     cfg.DBSSLMode,
		SSLRootCert: cfg.DBSSLRootCert,
	})
	if err != nil {
		return nil, err
	}

	return &Activities{config: cfg, db: db, nomad: nomad}, nil
}

// Close shuts down the database connection pool.
func (a *Activities) Close() error {
	return a.db.Close()
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Vulnerability holds details about a single CVE.
type Vulnerability struct {
	VulnID           string `json:"vuln_id"`
	Severity         string `json:"severity"`
	PkgName          string `json:"pkg_name"`
	InstalledVersion string `json:"installed_version"`
	FixedVersion     string `json:"fixed_version"`
	Title            string `json:"title"`
	Description      string `json:"description"`
}

// ScanResult holds vulnerability scan results for one image.
type ScanResult struct {
	Image           string          `json:"image"`
	Status          string          `json:"status"`
	Error           string          `json:"error,omitempty"`
	CriticalCount   int             `json:"critical_count"`
	HighCount       int             `json:"high_count"`
	MediumCount     int             `json:"medium_count"`
	LowCount        int             `json:"low_count"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
	ScannedAt       time.Time       `json:"scanned_at"`
}

// ScanConfig holds workflow-level configuration passed as input so values
// are deterministic across replays.
type ScanConfig struct {
	// Concurrency bounds how many images scan in parallel so the burst
	// doesn't overwhelm the Trivy server. Default 10.
	Concurrency int `json:"concurrency"`
}

// ApplyDefaults fills any unset field with its fleet-wide default.
func (c *ScanConfig) ApplyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 10
	}
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// GetRunningImages queries Nomad for all unique Docker images across
// running allocations. Creates a client span to Nomad for service graph
// visibility.
func (a *Activities) GetRunningImages(ctx context.Context) ([]string, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Discovering running images from Nomad")

	// --- Client span for nomad edge in service graph ---
	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.get_running_images")
	defer span.End()

	images, err := a.nomad.RunningImages(ctx)
	if err != nil {
		return nil, err
	}

	logger.Info("Found unique images", "count", len(images))
	return images, nil
}

// ScanImage runs Trivy against a single container image using server mode.
// Transient errors (connection refused, timeouts) are returned as errors so
// Temporal retries them. Permanent failures (image not found, manifest
// unknown) are returned as non-retryable with the error status recorded in
// the result.
func (a *Activities) ScanImage(ctx context.Context, image string) (ScanResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Scanning image", "image", image)

	result := ScanResult{
		Image:     image,
		Status:    "success",
		ScannedAt: time.Now(),
	}

	// --- Client span for trivy-server edge in service graph ---
	ctx, span := shared.StartPeerSpan(ctx, "trivy-server", "trivy.scan",
		attribute.String("trivy.image", image),
		attribute.String("trivy.server", a.config.TrivyServerAddr),
	)
	defer span.End()

	cmd := exec.CommandContext(ctx, trivyBin, "image",
		"--server", a.config.TrivyServerAddr,
		"--format", "json",
		"--timeout", "10m",
		"--scanners", "vuln",
		image)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		switch classifyTrivyError(errMsg) {
		case scanErrPermanent:
			result.Status = "pull_failed"
			result.Error = errMsg
			span.SetStatus(codes.Error, "pull_failed")
			span.SetAttributes(attribute.String(attrTrivyError, errMsg))
			logger.Warn("Image not found", "image", image, "error", errMsg)
			return result, temporal.NewNonRetryableApplicationError(
				"image not found: "+image, "PULL_FAILED", nil)
		case scanErrTransient:
			span.SetStatus(codes.Error, "transient")
			span.SetAttributes(attribute.String(attrTrivyError, errMsg))
			logger.Warn("Transient scan error, will retry", "image", image, "error", errMsg)
			return result, fmt.Errorf("trivy server unavailable for %s: %s", image, errMsg)
		default:
			result.Status = "error"
			result.Error = fmt.Sprintf("%v: %s", err, errMsg)
			span.SetStatus(codes.Error, "scan_failed")
			span.SetAttributes(attribute.String(attrTrivyError, result.Error))
			logger.Error("Scan failed", "image", image, "error", result.Error)
			return result, fmt.Errorf("scan failed for %s: %w", image, err)
		}
	}

	vulns, counts, err := parseTrivyOutput(stdout.Bytes())
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("failed to parse trivy output: %v", err)
		return result, temporal.NewNonRetryableApplicationError(
			result.Error, "PARSE_FAILED", nil)
	}
	result.Vulnerabilities = vulns
	result.CriticalCount = counts.Critical
	result.HighCount = counts.High
	result.MediumCount = counts.Medium
	result.LowCount = counts.Low

	span.SetAttributes(
		attribute.Int("trivy.critical", result.CriticalCount),
		attribute.Int("trivy.high", result.HighCount),
		attribute.Int("trivy.medium", result.MediumCount),
		attribute.Int("trivy.low", result.LowCount),
		attribute.Int("trivy.total", len(result.Vulnerabilities)),
	)

	logger.Info("Scan complete",
		"image", image,
		"critical", result.CriticalCount,
		"high", result.HighCount,
		"medium", result.MediumCount,
		"low", result.LowCount,
		"total_vulns", len(result.Vulnerabilities))

	return result, nil
}

// scanErrClass categorizes a trivy CLI failure so ScanImage can map it to the
// right Temporal outcome.
type scanErrClass int

const (
	scanErrUnknown   scanErrClass = iota // unclassified -- let Temporal decide
	scanErrPermanent                     // image missing/inaccessible -- non-retryable
	scanErrTransient                     // server down/network -- retryable
)

// classifyTrivyError inspects trivy's stderr and classifies the failure.
// Matching is substring-based because the trivy CLI exposes no stable typed
// error surface (the worker shells out to it; see the Dockerfile note).
func classifyTrivyError(stderr string) scanErrClass {
	switch {
	case strings.Contains(stderr, "manifest unknown"),
		strings.Contains(stderr, "not found"):
		return scanErrPermanent
	case strings.Contains(stderr, "connection refused"),
		strings.Contains(stderr, "timeout"),
		strings.Contains(stderr, "connection reset"):
		return scanErrTransient
	default:
		return scanErrUnknown
	}
}

// SeverityCounts tallies vulnerabilities by trivy severity.
type SeverityCounts struct {
	Critical, High, Medium, Low int
}

// trivyReport is the subset of trivy's JSON output the worker consumes.
type trivyReport struct {
	Results []struct {
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			Severity         string `json:"Severity"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Title            string `json:"Title"`
			Description      string `json:"Description"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// maxDescriptionLen bounds a stored CVE description; longer text is truncated
// with a trailing ellipsis to this exact length.
const maxDescriptionLen = 1000

// parseTrivyOutput unmarshals trivy JSON, deduplicates vulnerabilities by ID,
// truncates long descriptions, and tallies severities.
func parseTrivyOutput(stdout []byte) ([]Vulnerability, SeverityCounts, error) {
	var report trivyReport
	if err := json.Unmarshal(stdout, &report); err != nil {
		return nil, SeverityCounts{}, err
	}

	var (
		vulns  []Vulnerability
		counts SeverityCounts
	)
	seen := make(map[string]struct{})
	for _, res := range report.Results {
		for _, v := range res.Vulnerabilities {
			if _, ok := seen[v.VulnerabilityID]; ok {
				continue
			}
			seen[v.VulnerabilityID] = struct{}{}

			desc := v.Description
			if len(desc) > maxDescriptionLen {
				desc = desc[:maxDescriptionLen-3] + "..."
			}

			vulns = append(vulns, Vulnerability{
				VulnID:           v.VulnerabilityID,
				Severity:         v.Severity,
				PkgName:          v.PkgName,
				InstalledVersion: v.InstalledVersion,
				FixedVersion:     v.FixedVersion,
				Title:            v.Title,
				Description:      desc,
			})

			switch strings.ToUpper(v.Severity) {
			case "CRITICAL":
				counts.Critical++
			case "HIGH":
				counts.High++
			case "MEDIUM":
				counts.Medium++
			case "LOW":
				counts.Low++
			}
		}
	}
	return vulns, counts, nil
}

// SaveScanResult stores a single scan result and its vulnerabilities in
// PostgreSQL. Saves individually rather than in batches to stay under
// Temporal's 2MB activity input payload limit.
func (a *Activities) SaveScanResult(ctx context.Context, result ScanResult) error {
	logger := activity.GetLogger(ctx)
	logger.Info("Saving scan result", "image", result.Image, "vulns", len(result.Vulnerabilities))

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var scanID int
	err = tx.QueryRowContext(ctx,
		`INSERT INTO scans (image, status, error, scanned_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		result.Image, result.Status, nullString(result.Error), result.ScannedAt,
	).Scan(&scanID)
	if err != nil {
		return fmt.Errorf("insert scan for %s: %w", result.Image, err)
	}

	for _, vuln := range result.Vulnerabilities {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO vulnerabilities (scan_id, vuln_id, severity, pkg_name, installed_version, fixed_version, title, description)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			scanID, vuln.VulnID, vuln.Severity, vuln.PkgName,
			vuln.InstalledVersion, nullString(vuln.FixedVersion),
			nullString(vuln.Title), nullString(vuln.Description),
		)
		if err != nil {
			return fmt.Errorf("insert vulnerability %s: %w", vuln.VulnID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	logger.Info("Saved scan result", "image", result.Image)
	return nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
