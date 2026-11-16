// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"gorm.io/gorm"
)

const (
	securityPostureReportMigrationID = 2026111602
	securityPostureReportTable       = "router_security_posture_reports"
	securityPostureReportSchema      = "metrum.ai/router-security-posture-summary/v1"
)

// SecurityPostureReport stores only check identifiers and outcomes. Detailed
// CLI findings remain local so target addresses or free-form text never enter
// the usage database.
type SecurityPostureReport struct {
	Schema      string                       `json:"schema"`
	GeneratedAt string                       `json:"generated_at"`
	Outcome     string                       `json:"outcome"`
	Findings    []SecurityPostureReportCheck `json:"findings"`
}

type SecurityPostureReportCheck struct {
	Check    string `json:"check"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
}

type securityPostureReportRow struct {
	ID          int    `gorm:"column:id;primaryKey"`
	Schema      string `gorm:"column:report_schema;type:text;not null"`
	GeneratedAt string `gorm:"column:generated_at;type:text;not null"`
	Outcome     string `gorm:"column:outcome;type:text;not null"`
	Findings    string `gorm:"column:findings_json;type:text;not null"`
}

func (securityPostureReportRow) TableName() string { return securityPostureReportTable }

func SecurityPostureReportMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               securityPostureReportMigrationID,
		Scope:            usageMigrationScope,
		Name:             "store the latest security posture summary",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("usage.security-posture-report.v1"),
		SchemaVersion:    17,
		DataVersion:      0,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "usage.security-posture-report.apply.v1@applySecurityPostureReportMigration",
		PostconditionKey: "usage.security-posture-report.schema.v1@verifySecurityPostureReportMigration",
		Dependencies:     []int{automaticContainmentMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applySecurityPostureReportMigration,
		Verify:           verifySecurityPostureReportMigration,
	})
}

func applySecurityPostureReportMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("security posture report migration requires database")
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS router_security_posture_reports (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		report_schema TEXT NOT NULL,
		generated_at TEXT NOT NULL,
		outcome TEXT NOT NULL,
		findings_json TEXT NOT NULL
	)`).Error; err != nil {
		return fmt.Errorf("create security posture report table: %w", err)
	}
	return verifySecurityPostureReportMigration(db)
}

func verifySecurityPostureReportMigration(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(securityPostureReportTable) {
		return errors.New("required security posture report table is missing")
	}
	for _, column := range []string{"id", "report_schema", "generated_at", "outcome", "findings_json"} {
		if !db.Migrator().HasColumn(securityPostureReportTable, column) {
			return fmt.Errorf("required security posture report column %s is missing", column)
		}
	}
	return nil
}

func (s *usageStore) saveSecurityPostureReport(report SecurityPostureReport) error {
	if s == nil || s.db == nil {
		return errors.New("usage database is unavailable")
	}
	findings, err := json.Marshal(report.Findings)
	if err != nil {
		return fmt.Errorf("encode security posture checks: %w", err)
	}
	return s.db.Exec(`INSERT INTO router_security_posture_reports (id, report_schema, generated_at, outcome, findings_json)
		VALUES (1, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET
		report_schema = excluded.report_schema, generated_at = excluded.generated_at,
		outcome = excluded.outcome, findings_json = excluded.findings_json`,
		report.Schema, report.GeneratedAt, report.Outcome, string(findings)).Error
}

func (s *usageStore) latestSecurityPostureReport() (*SecurityPostureReport, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("usage database is unavailable")
	}
	var row securityPostureReportRow
	err := s.db.Take(&row, "id = ?", 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	report := &SecurityPostureReport{Schema: row.Schema, GeneratedAt: row.GeneratedAt, Outcome: row.Outcome}
	if err := json.Unmarshal([]byte(row.Findings), &report.Findings); err != nil {
		return nil, fmt.Errorf("decode stored security posture checks: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, report.GeneratedAt); err != nil {
		return nil, errors.New("stored security posture timestamp is invalid")
	}
	return report, nil
}

func (s *Service) handleAdminSecurityPostureReport(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil || s.usage.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "usage-store-unavailable", "message": "usage-store-unavailable"}})
		return
	}
	switch r.Method {
	case http.MethodGet:
		report, err := s.usage.latestSecurityPostureReport()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "security-posture-report-unavailable", "message": "security-posture-report-unavailable"}})
			return
		}
		if report == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"type": "security-posture-report-not-found", "message": "security-posture-report-not-found"}})
			return
		}
		writeJSON(w, http.StatusOK, report)
	case http.MethodPost:
		var report SecurityPostureReport
		reader := http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil {
			status := http.StatusBadRequest
			var maxBytes *http.MaxBytesError
			if errors.As(err, &maxBytes) {
				status = http.StatusRequestEntityTooLarge
			}
			writeJSON(w, status, map[string]any{"error": map[string]any{"type": "invalid-security-posture-report", "message": "invalid-security-posture-report"}})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-security-posture-report", "message": "invalid-security-posture-report"}})
			return
		}
		if err := validateSecurityPostureReport(report); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-security-posture-report", "message": "invalid-security-posture-report"}})
			return
		}
		if err := s.usage.saveSecurityPostureReport(report); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "security-posture-report-unavailable", "message": "security-posture-report-unavailable"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": true})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"type": "method-not-allowed", "message": "method-not-allowed"}})
	}
}

func validateSecurityPostureReport(report SecurityPostureReport) error {
	if report.Schema != securityPostureReportSchema || len(report.Findings) == 0 || len(report.Findings) > 256 {
		return errors.New("invalid schema or finding count")
	}
	if _, err := time.Parse(time.RFC3339, report.GeneratedAt); err != nil {
		return errors.New("invalid generated_at")
	}
	if report.Outcome != "pass" && report.Outcome != "warn" && report.Outcome != "fail" {
		return errors.New("invalid outcome")
	}
	wantOutcome := "pass"
	seen := make(map[string]struct{}, len(report.Findings))
	for _, finding := range report.Findings {
		check := finding.Check
		if len(check) == 0 || len(check) > 64 {
			return errors.New("invalid check")
		}
		for _, char := range check {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return errors.New("invalid check")
			}
		}
		if _, exists := seen[check]; exists {
			return errors.New("duplicate check")
		}
		seen[check] = struct{}{}
		switch finding.Status {
		case "pass":
			if finding.Severity != "info" {
				return errors.New("invalid passing severity")
			}
		case "warn":
			if finding.Severity != "medium" {
				return errors.New("invalid warning severity")
			}
			if wantOutcome == "pass" {
				wantOutcome = "warn"
			}
		case "fail":
			if finding.Severity != "high" {
				return errors.New("invalid failing severity")
			}
			wantOutcome = "fail"
		default:
			return errors.New("invalid status")
		}
	}
	if report.Outcome != wantOutcome {
		return errors.New("outcome does not match findings")
	}
	return nil
}
