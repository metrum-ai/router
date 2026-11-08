// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAdminKeyOperationsContract(t *testing.T) {
	// A service with the authoritative store advertises the lifecycle routes; a
	// bare service and the package default registry do not, and repeated
	// construction never mutates the shared specs.
	svcKeys := &Service{adminKeys: &adminKeyStore{}}
	withKeys, err := svcKeys.adminKeyRegistry()
	if err != nil {
		t.Fatal(err)
	}
	withoutKeys, err := (&Service{}).adminKeyRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(withKeys.Operations()) != len(withoutKeys.Operations())+len(adminKeyOperations()) {
		t.Fatal("lifecycle routes are not service-scoped")
	}
	for _, again := range []*Service{svcKeys, svcKeys} {
		if dup, err := again.adminKeyRegistry(); err != nil || len(dup.Operations()) != len(withKeys.Operations()) {
			t.Fatal("repeated service construction mutated the shared operation specs")
		}
	}
	registry, err := NewAdminV1Registry(adminKeyOperations())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"issueAdminKey": "issue", "listAdminKeys": "list", "getAdminKey": "get", "rotateAdminKey": "rotate", "revokeAdminKey": "revoke"}
	seen := 0
	for _, op := range registry.Operations() {
		action, ok := want[op.ID]
		if !ok {
			continue
		}
		seen++
		if op.Object != adminKeyObject || op.Action != action || op.Domain != AdminDomainGlobal {
			t.Fatalf("%s must authorize on the global admin:keys object: %+v", op.ID, op)
		}
		if !strings.HasPrefix(op.Path, "/admin/v1/admin-keys") {
			t.Fatalf("%s path=%q", op.ID, op.Path)
		}
		if op.ID == "issueAdminKey" || op.ID == "rotateAdminKey" {
			if op.Idempotency != AdminIdempotencyHandler {
				t.Fatalf("%s one-time secret response must use handler-owned idempotency", op.ID)
			}
		} else if op.Method != "GET" && op.Idempotency == AdminIdempotencyNone {
			t.Fatalf("%s mutation must use an idempotency mode", op.ID)
		}
		if op.Method == "POST" && !op.RequiredPrecondition() {
			t.Fatalf("%s mutation must require If-Match", op.ID)
		}
		if op.SensitiveResponse && !strings.Contains(strings.Join(op.Response.(adminOpenObject).Members, ","), "secret") {
			t.Fatalf("%s sensitive response must document the one-time secret", op.ID)
		}
	}
	if seen != len(want) {
		t.Fatalf("registered admin-key operations=%d want=%d", seen, len(want))
	}
}

func TestAdminKeyHandlersAuthorizeThroughContract(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "keys.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAdminKeySchema(db); err != nil {
		t.Fatal(err)
	}
	store := newAdminKeyStore(db, func() time.Time { return time.Now().UTC() })
	svc := &Service{adminKeys: store, configControlPlane: db}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	principal := Principal{Subject: "adminkey:root"}
	var decisions atomic.Int64
	allowed := false
	keyRegistry, err := NewAdminV1Registry(adminKeyOperations())
	if err != nil {
		t.Fatal(err)
	}
	deps := &AdminV1Deps{
		Registry:     keyRegistry,
		Authenticate: AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) { return principal, nil }),
		Authorize: func(_ context.Context, p Principal, domain, object, action string) error {
			decisions.Add(1)
			if p != principal || domain != AdminGlobalDomain || object != adminKeyObject {
				t.Errorf("unexpected decision %q %q %q for %+v", domain, object, action, p)
			}
			if !allowed {
				return ErrAdminForbidden
			}
			return nil
		},
		Handlers:      svc.adminKeyHandlers(),
		Idempotency:   newAdminV1DBHarnessIdempotency(t, db),
		IdempotencyOn: true,
		CurrentETag:   func(*http.Request) (string, int64, error) { return `"1"`, 1, nil },
		// #869 requires the durable denial/credential sinks for mutations.
		Denials:          adminDenialSinkFunc(func(context.Context, AdminDenialRecord) error { return nil }),
		CredentialEvents: adminCredentialEventSinkFunc(func(context.Context, AdminCredentialEvent) error { return nil }),
	}
	mux := newAdminMux()
	if err := RegisterAdminV1(mux, *deps); err != nil {
		t.Fatal(err)
	}
	issue := func() *httptest.ResponseRecorder {
		body := strings.NewReader(`{"expires_at":"` + expires + `"}`)
		r := httptest.NewRequest("POST", "/admin/v1/admin-keys", body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", `"1"`)
		r.Header.Set("Idempotency-Key", "op-1")
		w := httptest.NewRecorder()
		mux.mux.ServeHTTP(w, r)
		return w
	}
	// Denied by the sole Authorizer: no handler work and no key row.
	if w := issue(); w.Code != http.StatusForbidden {
		t.Fatalf("denied issue status=%d want403", w.Code)
	}
	if list, _ := store.list(context.Background(), adminKeyListRequest{}); len(list.Keys) != 0 {
		t.Fatal("denied issue created a key")
	}
	allowed = true
	w := issue()
	if w.Code != http.StatusCreated {
		t.Fatalf("allowed issue status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"secret"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("issue response must carry the one-time secret with no-store")
	}
	// A retry is a stable 409 that never reissues and never replays the secret.
	retry := issue()
	if retry.Code != http.StatusConflict || !strings.Contains(retry.Body.String(), "one-time-secret-unavailable") || strings.Contains(retry.Body.String(), `"secret"`) {
		t.Fatalf("idempotent retry status=%d body=%s, want 409 one-time-secret-unavailable", retry.Code, retry.Body.String())
	}
	if decisions.Load() < 3 {
		t.Fatalf("every request must take an authorization decision: %d", decisions.Load())
	}
}

type failingCredentialEventSink struct{}

func (f *failingCredentialEventSink) RecordCredentialEvent(context.Context, AdminCredentialEvent) error {
	return errors.New("event store unavailable")
}

func mustMachineEventSink(t *testing.T) *adminMachineCredentialEventSink {
	t.Helper()
	sink, err := newAdminMachineCredentialEventSink(&failingCredentialEventSink{})
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func mustActivationStore(t *testing.T) *adminEmergencyActivationStore {
	t.Helper()
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "activation.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAdminEmergencyActivationSchema(db); err != nil {
		t.Fatal(err)
	}
	store, err := newAdminEmergencyActivationStore(db, 1, time.Now, func(reason string) string { return reason })
	if err != nil {
		t.Fatal(err)
	}
	return store
}
