// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Actual routerctl subprocess -> loopback HTTP -> real shared mount/Casbin/SQL
// guard. Authenticator inventory and change-audit adapter remain test fixtures;
// this is not production listener/audit-replay or separate-replica acceptance.
func TestPolicyCLIRealMountedLifecycle(t *testing.T) {
	store, deps, p, lines := policyMutationFixture(t)
	lines[0] = strings.Replace(lines[0], "read|write|activate", "read|write|activate|rollback", 1)
	cfg := deps.PublishedConfig()
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "inline", Policy: lines}
	old := deps.Current
	deps.Current = func(tx *gorm.DB, cfg *Config) (*Service, error) {
		s, e := old(tx, cfg)
		if e == nil {
			s.configSnapshot = NewConfigSnapshot()
			s.configSnapshot.Store(cfg)
		}
		return s, e
	}
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	registry, err := NewAdminV1Registry(policyOperationSpecs())
	if err != nil {
		t.Fatal(err)
	}
	current, err := deps.Current(store.db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAdminV1(mux, AdminV1Deps{Registry: registry, Authenticate: &fakeAuthenticator{principal: p}, Authorize: current.Authorize, PolicyCurrentETag: func(*http.Request) (string, int64, error) {
		var row policyMutationRevision
		e := store.db.First(&row, 1).Error
		return AdminETagForRevision(row.Revision), row.Revision, e
	}, IdempotencyOn: true, Idempotency: NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 32), Denials: &recordingDenialSink{}, CredentialEvents: &recordingCredentialSink{}, CursorCodec: NewAdminCursorCodec([]byte("policy-cli-smoke-cursor-key-012345")), Handlers: newPolicyAdminHandlers(store, deps, func(tx *gorm.DB, _ int64, r policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, r.SetID, p.Subject, "cli", "req", "", time.Now())
	})}); err != nil {
		t.Fatal(err)
	}
	const credential = "local-only-policy-cli-smoke-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+credential {
			w.WriteHeader(401)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "routerctl")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/metrum-ai-routerctl").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	run := func(operation string, args ...string) struct {
		Data json.RawMessage `json:"data"`
		ETag string          `json:"etag"`
		Link string          `json:"link"`
	} {
		t.Helper()
		argv := append([]string{"policies", operation, "--router-url", server.URL}, args...)
		cmd := exec.CommandContext(ctx, binary, argv...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "ROUTER_ADMIN_KEY=") && !strings.HasPrefix(value, "ROUTER_ADMIN_KEY_FILE=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "ROUTER_ADMIN_KEY="+credential, "ROUTER_ADMIN_KEY_FILE=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %s: %v %s", operation, err, out)
		}
		var result struct {
			Data json.RawMessage `json:"data"`
			ETag string          `json:"etag"`
			Link string          `json:"link"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("CLI output: %v %s", err, out)
		}
		return result
	}
	file := filepath.Join(t.TempDir(), "draft.json")
	for i, id := range []string{"first", "second"} {
		body := `{"id":"ID","name":"ID","version":1,"rules":[{"subject_or_role":"root","domain":"*","object":"admin:policy","action":"read|write|activate|rollback"},{"subject_or_role":"root","domain":"*","object":"admin:reports","action":"read"}],"role_memberships":[{"subject":"root","role":"security-admin","domain":"*"}]}`
		body = strings.ReplaceAll(body, "ID", id)
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		tag := AdminETagForRevision(int64(2 * i))
		run("draft", "--file", file, "--if-match", tag, "--idempotency-key", "create-"+id)
		got := run("get", "--set-id", id)
		if got.ETag != AdminETagForRevision(int64(2*i+1)) {
			t.Fatal("representation ETag lost")
		}
		run("validate", "--set-id", id)
		run("activate", "--set-id", id, "--if-match", got.ETag, "--idempotency-key", "activate-"+id)
	}
	page := run("list", "--limit", "1")
	if !strings.Contains(page.Link, "cursor=") {
		t.Fatal("signed page Link missing")
	}
	run("diff", "--set-id", "second", "--against", "first")
	rolled := run("rollback", "--set-id", "first", "--if-match", AdminETagForRevision(4), "--idempotency-key", "rollback-first")
	var result struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(rolled.Data, &result); err != nil || result.ID == "first" || result.ID == "second" || result.ID == "" || result.Revision != 5 {
		t.Fatalf("rollback did not clone: %s %v", rolled.Data, err)
	}
}
