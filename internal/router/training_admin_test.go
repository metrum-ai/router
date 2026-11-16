// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrainingAdminAuthorizationAuditAndMissingCapture(t *testing.T) {
	key, err := GenerateAdminAPIKey("training-test", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ADMIN_KEY_TRAINING_TEST_HASH", key.TokenHash)
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "scoped_denied", true: "global_allowed"}[global], func(t *testing.T) {
			dir := t.TempDir()
			cfg := testConfig(t, "http://127.0.0.1:1", "unused", dir)
			cfg.Admin.Enabled = true
			cfg.Admin.Listen = "127.0.0.1:0"
			configureDocsSmokeCapacity(cfg)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 7}}
			domain := "acme-support/test"
			if global {
				domain = "*"
			}
			t.Setenv("TRACE_TEST_ADMIN_HASH", mustBcryptHash(t, "test-admin-password"))
			cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", Subject: "basic:admin", Domain: domain, PasswordHashEnv: "TRACE_TEST_ADMIN_HASH"}}}
			cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: "training-test", KeyHashEnv: "ROUTER_ADMIN_KEY_TRAINING_TEST_HASH", Domain: domain, Permissions: []string{"admin:training:export", "admin:training:label"}}}
			cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, adminkey:training-test, " + domain + ", content:training, export|label"}}
			svc, err := newServiceWithVerifiedFixture(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			now := time.Now().UTC()
			f := trainingFilters{From: now.Add(-4 * time.Hour), ValidationFrom: now.Add(-2 * time.Hour), TestFrom: now.Add(-time.Hour), To: now, TeacherTermsDecision: map[string]string{"decision": "synthetic unit test", "training_outputs_allowed": "true"}, TaskSource: map[string]string{"license": "Apache-2.0"}}
			body, _ := json.Marshal(f)
			var requestSequence atomic.Uint64
			call := func(path, body string, admin bool) int {
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Idempotency-Key", fmt.Sprintf("training-e2e-%d", requestSequence.Add(1)))
				if admin {
					r.Header.Set("Authorization", "Bearer "+key.Token)
				}
				w := httptest.NewRecorder()
				svc.AdminHandler().ServeHTTP(w, r)
				return w.Code
			}
			if got := call("/admin/v1/training/export", string(body), false); got != 401 {
				t.Fatalf("unauth=%d", got)
			}
			want := 403
			if global {
				want = 200
			}
			if got := call("/admin/v1/training/export", string(body), true); got != want {
				t.Fatalf("export=%d want %d", got, want)
			}
			if !global {
				return
			}
			var n int64
			if err := svc.usage.db.Model(&securityAccessEventRecord{}).Where("event_type = ?", "training_export").Count(&n).Error; err != nil || n != 1 {
				t.Fatalf("durable audit count=%d err=%v", n, err)
			}
			label := `{"version":"router-outcome-v1","task_hash":"` + strings.Repeat("a", 64) + `","task_success":true,"schema_valid":true,"quality":1,"backend":"acme-support-v1","seed":1}`
			if got := call("/admin/v1/training/outcomes/uncaptured", label, true); got != 404 {
				t.Fatalf("uncaptured label=%d", got)
			}
			if got := call("/admin/v1/training/outcomes/uncaptured", strings.TrimSuffix(label, "}")+`,"expected_final":"J"}`, true); got != 404 {
				t.Fatalf("A-J anchor rejected: %d", got)
			}
			if got := call("/admin/v1/training/outcomes/uncaptured", strings.TrimSuffix(label, "}")+`,"expected_final":"Z"}`, true); got != 400 {
				t.Fatalf("unknown anchor accepted: %d", got)
			}
			svc.cfg.Server.AdminReports.Security.Enabled = false
			if got := call("/admin/v1/training/export", string(body), true); got != 503 {
				t.Fatalf("disabled audit export=%d", got)
			}
			svc.cfg.Server.AdminReports.Security.Enabled = true
			if err := svc.usage.db.Migrator().DropTable(&securityAccessEventRecord{}); err != nil {
				t.Fatal(err)
			}
			if got := call("/admin/v1/training/export", string(body), true); got != 503 {
				t.Fatalf("failed durable audit export=%d", got)
			}
		})
	}
}

func TestTrainingChatRejectsBrokenPairingAndArguments(t *testing.T) {
	for _, raw := range []string{
		`[{"role":"tool","tool_call_id":"missing","content":"x"}]`,
		`[{"role":"assistant","tool_calls":[{"id":"x","function":{"arguments":"{}"}}]}]`,
		`[{"role":"assistant","tool_calls":[{"id":"x","function":{"arguments":"not-json"}}]},{"role":"tool","tool_call_id":"x"}]`,
	} {
		var m []any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		if trainingChatValid(m) {
			t.Fatalf("accepted invalid: %s", raw)
		}
	}
}
