// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDBBackedAuthorizationLoadsActivePolicySet(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	if err := store.CreateAuthzPolicySet(authzPolicySetInput{
		ID:        "authz-admin-v1",
		Name:      "admin",
		Version:   1,
		CreatedBy: "basic:policy-admin",
		CreatedAt: now,
		Policy: []string{
			"g, caller:alice, metrics_admin, metrum-insights/test",
			"p, metrics_admin, metrum-insights/test, metrics, read",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("authz-admin-v1", "basic:policy-admin", "req-activate-1", "activate v1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].MetricsAdmin = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(dbPath)
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if svc.authorizer.activePolicySet != "authz-admin-v1" {
		t.Fatalf("active policy set = %q", svc.authorizer.activePolicySet)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDBBackedAuthorizationMissingActivePolicyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(dbPath)
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
	svc, err := New(cfg)
	if err == nil {
		svc.Close()
		t.Fatal("expected missing active DB policy to fail service startup")
	}
	if !strings.Contains(err.Error(), "no active authz policy set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthzPolicyActivationRejectsMalformedAndKeepsLastKnownValid(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 13, 0, 0, 0, time.UTC)
	if err := store.CreateAuthzPolicySet(authzPolicySetInput{
		ID:        "authz-admin-v1",
		Name:      "admin",
		Version:   1,
		CreatedBy: "basic:policy-admin",
		CreatedAt: now,
		Policy: []string{
			"g, caller:alice, metrics_admin, metrum-insights/test",
			"p, metrics_admin, metrum-insights/test, metrics, read",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("authz-admin-v1", "basic:policy-admin", "req-activate-1", "activate v1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	badSet := authzPolicySetRecord{
		ID:        "authz-admin-bad",
		Name:      "admin",
		Version:   2,
		Status:    authzPolicyStatusDraft,
		CreatedBy: "basic:policy-admin",
		CreatedAt: now.Add(2 * time.Minute).Format(time.RFC3339Nano),
	}
	if err := store.db.Create(&badSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&authzPolicyRuleRecord{
		PolicySetID:   "authz-admin-bad",
		Sequence:      1,
		PType:         "p",
		SubjectOrRole: "metrics_admin",
		Domain:        "",
		Object:        "metrics",
		Action:        "read",
	}).Error; err != nil {
		t.Fatal(err)
	}

	err = store.ActivateAuthzPolicySet("authz-admin-bad", "basic:policy-admin", "req-activate-bad", "Bearer example token in rejected summary", now.Add(3*time.Minute))
	if err == nil {
		t.Fatal("expected malformed policy activation to fail")
	}
	lines, activeID, err := store.LoadActiveAuthzPolicyLines()
	if err != nil {
		t.Fatal(err)
	}
	if activeID != "authz-admin-v1" {
		t.Fatalf("active policy set changed to %q", activeID)
	}
	if len(lines) != 2 {
		t.Fatalf("active policy lines=%v", lines)
	}
	var audit authzPolicyAuditEventRecord
	if err := store.db.Where("policy_set_id = ? AND action = ?", "authz-admin-bad", authzPolicyAuditValidationFailed).First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.SafeSummary != "[redacted]" {
		t.Fatalf("unsafe audit summary was not redacted: %q", audit.SafeSummary)
	}
}

func TestAuthzPolicyActivationRejectsUnsupportedRuleEffect(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 13, 30, 0, 0, time.UTC)
	set := authzPolicySetRecord{
		ID:        "authz-admin-deny",
		Name:      "admin",
		Version:   1,
		Status:    authzPolicyStatusDraft,
		CreatedBy: "basic:policy-admin",
		CreatedAt: now.Format(time.RFC3339Nano),
	}
	if err := store.db.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&authzPolicyRuleRecord{
		PolicySetID:   "authz-admin-deny",
		Sequence:      1,
		PType:         "p",
		SubjectOrRole: "report_reader",
		Domain:        "local/test",
		Object:        "admin:reports",
		Action:        "read",
		Effect:        "deny",
	}).Error; err != nil {
		t.Fatal(err)
	}
	err = store.ActivateAuthzPolicySet("authz-admin-deny", "basic:policy-admin", "req-effect-deny", "activate deny row", now.Add(time.Minute))
	if err == nil {
		t.Fatal("expected unsupported effect to fail activation")
	}
	if !strings.Contains(err.Error(), "unsupported effect") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSafeAuthzAuditFieldRedactsKeyShapedValues(t *testing.T) {
	for _, value := range []string{
		"sk-live-example-provider-key",
		"ghp_examplegithubtoken",
		"request with api_key material",
	} {
		if got := safeAuthzAuditField(value); got != "[redacted]" {
			t.Fatalf("safeAuthzAuditField(%q)=%q", value, got)
		}
	}
}

func TestAuthzPolicyRollbackReactivatesPreviousValidSet(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 14, 0, 0, 0, time.UTC)
	for _, input := range []authzPolicySetInput{
		{
			ID:        "authz-admin-v1",
			Name:      "admin",
			Version:   1,
			CreatedBy: "basic:policy-admin",
			CreatedAt: now,
			Policy: []string{
				"g, caller:alice, metrics_admin, metrum-insights/test",
				"p, metrics_admin, metrum-insights/test, metrics, read",
			},
		},
		{
			ID:        "authz-admin-v2",
			Name:      "admin",
			Version:   2,
			CreatedBy: "basic:policy-admin",
			CreatedAt: now.Add(time.Minute),
			Policy: []string{
				"g, caller:alice, report_reader, metrum-insights/test",
				"p, report_reader, metrum-insights/test, admin:reports, read",
			},
		},
	} {
		if err := store.CreateAuthzPolicySet(input); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ActivateAuthzPolicySet("authz-admin-v1", "basic:policy-admin", "req-activate-1", "activate v1", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("authz-admin-v2", "basic:policy-admin", "req-activate-2", "activate v2", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, activeID, err := store.LoadActiveAuthzPolicyLines(); err != nil || activeID != "authz-admin-v2" {
		t.Fatalf("active before rollback id=%q err=%v", activeID, err)
	}
	rolledBackID, err := store.RollbackAuthzPolicySet("basic:policy-admin", "req-rollback-1", "rollback to previous valid", now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if rolledBackID != "authz-admin-v1" {
		t.Fatalf("rolled back to %q", rolledBackID)
	}
	if _, activeID, err := store.LoadActiveAuthzPolicyLines(); err != nil || activeID != "authz-admin-v1" {
		t.Fatalf("active after rollback id=%q err=%v", activeID, err)
	}
	var count int64
	if err := store.db.Model(&authzPolicyAuditEventRecord{}).Where("action = ?", authzPolicyAuditRollback).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rollback audit count=%d", count)
	}
}
