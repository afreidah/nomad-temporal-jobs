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
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/lib/pq"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"munchbox/temporal-workers/shared"
)

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
		return fmt.Errorf("TrivyServerAddr is required")
	}
	if c.DBHost == "" {
		return fmt.Errorf("DBHost is required")
	}
	if c.DBUser == "" {
		return fmt.Errorf("DBUser is required")
	}
	if c.DBPassword == "" {
		return fmt.Errorf("DBPassword is required")
	}
	return nil
}

// -------------------------------------------------------------------------
// ACTIVITY STRUCT
// -------------------------------------------------------------------------

// Activities holds shared dependencies for trivy scan activities. Register
// an instance with the Temporal worker to expose all exported methods as
// activity implementations.
type Activities struct {
	config Config
	db     *sql.DB
}

// New creates an Activities instance with a pooled database connection.
func New(cfg Config) (*Activities, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode)
	if cfg.DBSSLRootCert != "" {
		connStr += " sslrootcert=" + cfg.DBSSLRootCert
	}

	db, err := otelsql.Open("postgres", connStr,
		otelsql.WithAttributes(
			semconv.DBSystemPostgreSQL,
			semconv.DBNamespace(cfg.DBName),
			semconv.ServerAddress(cfg.DBHost),
			semconv.ServerPort(5432),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &Activities{config: cfg, db: db}, nil
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
	_, span := shared.StartClientSpan(ctx, "nomad.get_running_images",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

	client, err := shared.NewNomadClient()
	if err != nil {
		return nil, fmt.Errorf("create Nomad client: %w", err)
	}

	allocs, _, err := client.Allocations().List(nil)
	if err != nil {
		return nil, fmt.Errorf("list allocations: %w", err)
	}

	imageMap := make(map[string]bool)
	for _, stub := range allocs {
		if stub.ClientStatus != "running" {
			continue
		}

		alloc, _, err := client.Allocations().Info(stub.ID, nil)
		if err != nil {
			logger.Warn("Failed to get allocation info", "alloc_id", stub.ID, "error", err)
			continue
		}

		if alloc.Job == nil {
			continue
		}

		for _, tg := range alloc.Job.TaskGroups {
			for _, task := range tg.Tasks {
				if task.Driver != "docker" || task.Config == nil {
					continue
				}
				if img, ok := task.Config["image"]; ok {
					if imgStr, ok := img.(string); ok && imgStr != "" {
						imageMap[imgStr] = true
					}
				}
			}
		}
	}

	images := make([]string, 0, len(imageMap))
	for img := range imageMap {
		images = append(images, img)
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
	ctx, span := shared.StartClientSpan(ctx, "trivy.scan",
		attribute.String("trivy.image", image),
		attribute.String("trivy.server", a.config.TrivyServerAddr),
		shared.PeerServiceAttr("trivy-server"),
	)
	defer span.End()

	cmd := exec.CommandContext(ctx, "trivy", "image",
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

		// --- Permanent failures: image does not exist or is inaccessible ---
		if strings.Contains(errMsg, "manifest unknown") ||
			strings.Contains(errMsg, "not found") {
			result.Status = "pull_failed"
			result.Error = errMsg
			span.SetStatus(codes.Error, "pull_failed")
			span.SetAttributes(attribute.String("trivy.error", errMsg))
			logger.Warn("Image not found", "image", image, "error", errMsg)
			return result, temporal.NewNonRetryableApplicationError(
				"image not found: "+image, "PULL_FAILED", nil)
		}

		// --- Transient failures: server down, network issues ---
		if strings.Contains(errMsg, "connection refused") ||
			strings.Contains(errMsg, "timeout") ||
			strings.Contains(errMsg, "connection reset") {
			span.SetStatus(codes.Error, "transient")
			span.SetAttributes(attribute.String("trivy.error", errMsg))
			logger.Warn("Transient scan error, will retry", "image", image, "error", errMsg)
			return result, fmt.Errorf("trivy server unavailable for %s: %s", image, errMsg)
		}

		// --- Unknown failures: let Temporal decide ---
		result.Status = "error"
		result.Error = fmt.Sprintf("%v: %s", err, errMsg)
		span.SetStatus(codes.Error, "scan_failed")
		span.SetAttributes(attribute.String("trivy.error", result.Error))
		logger.Error("Scan failed", "image", image, "error", result.Error)
		return result, fmt.Errorf("scan failed for %s: %w", image, err)
	}

	// --- Parse trivy JSON output ---
	var trivyOutput struct {
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

	if err := json.Unmarshal(stdout.Bytes(), &trivyOutput); err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("failed to parse trivy output: %v", err)
		return result, temporal.NewNonRetryableApplicationError(
			result.Error, "PARSE_FAILED", nil)
	}

	// --- Collect and deduplicate vulnerabilities ---
	seen := make(map[string]bool)
	for _, res := range trivyOutput.Results {
		for _, vuln := range res.Vulnerabilities {
			if seen[vuln.VulnerabilityID] {
				continue
			}
			seen[vuln.VulnerabilityID] = true

			desc := vuln.Description
			if len(desc) > 1000 {
				desc = desc[:997] + "..."
			}

			result.Vulnerabilities = append(result.Vulnerabilities, Vulnerability{
				VulnID:           vuln.VulnerabilityID,
				Severity:         vuln.Severity,
				PkgName:          vuln.PkgName,
				InstalledVersion: vuln.InstalledVersion,
				FixedVersion:     vuln.FixedVersion,
				Title:            vuln.Title,
				Description:      desc,
			})

			switch strings.ToUpper(vuln.Severity) {
			case "CRITICAL":
				result.CriticalCount++
			case "HIGH":
				result.HighCount++
			case "MEDIUM":
				result.MediumCount++
			case "LOW":
				result.LowCount++
			}
		}
	}

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
