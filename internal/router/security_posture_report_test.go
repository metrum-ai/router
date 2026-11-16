// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityPostureReportAdminRoundTripAndWriteAuthorization(t *testing.T) {
	store, err := OpenUsageStore(freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enforcer, err := newAuthzEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range [][4]string{
		{"adminkey:writer", AdminGlobalDomain, authzObjectSecurityReports, "read"},
		{"adminkey:writer", AdminGlobalDomain, authzObjectSecurityReports, "write"},
		{"adminkey:reader", AdminGlobalDomain, authzObjectSecurityReports, "read"},
	} {
		if _, err := enforcer.AddPolicy(rule[0], rule[1], rule[2], rule[3]); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{usage: store, cfg: &Config{Server: ServerConfig{AdminReports: AdminReportsConfig{
		Enabled: true, PathPrefix: "/admin/reports", Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 30},
	}}}, authorizer: &authorizer{enforcer: enforcer}}

	body := `{"schema":"metrum.ai/router-security-posture-summary/v1","generated_at":"2026-11-16T06:00:00Z","outcome":"warn","findings":[{"check":"admin-listener","status":"pass","severity":"info"},{"check":"caller-key-age","status":"warn","severity":"medium"}]}`
	request := func(method, subject, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/admin/reports/api/security/posture/latest", strings.NewReader(payload))
		principal := Principal{Subject: subject, Kind: "adminkey", CredentialID: "cred-1", Issuer: "test"}
		req = req.WithContext(WithPrincipal(req.Context(), principal))
		response := httptest.NewRecorder()
		service.handleAdminReports(response, req)
		return response
	}
	if got := request(http.MethodPost, "adminkey:reader", body); got.Code != http.StatusForbidden {
		t.Fatalf("reader write status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, "adminkey:writer", strings.Replace(body, `"findings":[`, `"details":"must-not-store","findings":[`, 1)); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown detail field status=%d body=%s", got.Code, got.Body.String())
	}
	var writeAuditCount int64
	if err := store.db.Table("security_access_events").Where("event_type = ? AND http_method = ? AND status_code = ? AND admin_subject = ?", "admin_access", http.MethodPost, http.StatusOK, "adminkey:writer").Count(&writeAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, "adminkey:writer", body); got.Code != http.StatusOK {
		t.Fatalf("write status=%d body=%s", got.Code, got.Body.String())
	}
	var writeAuditCountAfter int64
	if err := store.db.Table("security_access_events").Where("event_type = ? AND http_method = ? AND status_code = ? AND admin_subject = ?", "admin_access", http.MethodPost, http.StatusOK, "adminkey:writer").Count(&writeAuditCountAfter).Error; err != nil {
		t.Fatal(err)
	}
	if writeAuditCountAfter != writeAuditCount+1 {
		t.Fatalf("successful posture report write added %d safe admin-access audit events, want 1", writeAuditCountAfter-writeAuditCount)
	}
	response := request(http.MethodGet, "adminkey:reader", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	var report SecurityPostureReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != securityPostureReportSchema || report.Outcome != "warn" || len(report.Findings) != 2 || strings.Contains(response.Body.String(), "must-not-store") {
		t.Fatalf("unexpected persisted safe summary: %s", response.Body.String())
	}
	var stored string
	if err := store.db.Raw("SELECT findings_json FROM router_security_posture_reports WHERE id = 1").Row().Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "detail") || strings.Contains(stored, "must-not-store") {
		t.Fatalf("stored report includes non-summary data: %s", stored)
	}
}

func TestValidateSecurityPostureReport(t *testing.T) {
	report := SecurityPostureReport{
		Schema: securityPostureReportSchema, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Outcome: "pass",
		Findings: []SecurityPostureReportCheck{{Check: "admin-listener", Status: "pass", Severity: "info"}},
	}
	if err := validateSecurityPostureReport(report); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	report.Findings[0].Check = "admin listener"
	if err := validateSecurityPostureReport(report); err == nil {
		t.Fatal("invalid check identifier accepted")
	}
}
