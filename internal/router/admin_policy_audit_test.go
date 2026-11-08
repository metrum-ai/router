// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

// adminPolicyAuditRow mirrors the usage-side audit table for read-back only.
type adminPolicyAuditRow struct {
	EventID          string
	At               string
	PrincipalSubject string
	PrincipalKind    string
	CredentialID     string
	Issuer           string
	Object           string
	Action           string
	OperationID      string
	RequestID        string
	Outcome          string
	Ticket           string
	ConfigSetID      string
	ConfigRevision   int64
	AfterHash        string
}

// TestPolicyMutationAuditWritesOneRowInTheChangeTransaction proves the #869
// binding: one usage audit row per guarded policy mutation, written inside the
// caller's transaction, carrying the authenticated actor, operation, revision
// and immutable hashes.
func TestPolicyMutationAuditWritesOneRowInTheChangeTransaction(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := applyUsageAdminAuditMigration(store.db); err != nil {
		t.Fatal(err)
	}

	meta := AdminAuditMeta{
		Subject: "operator", Kind: "mtls", CredentialID: "cred-1", Issuer: "test-ca",
		Domain: "*", Object: "admin:policy", Action: "write",
		OperationID: "createPolicySet", RequestID: "req-1", Ticket: "INC-7", RequestHash: "reqhash",
	}
	writer, err := policyMutationAuditWriter(WithAdminAuditMeta(context.Background(), meta))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		return writer(tx, 7, policyResourceResult{SetID: "set-1", AfterHash: "after-hash"})
	}); err != nil {
		t.Fatal(err)
	}

	var rows []adminPolicyAuditRow
	if err := store.db.Table(usageAdminAuditTable).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly one", len(rows))
	}
	row := rows[0]
	for _, tc := range []struct{ name, got, want string }{
		{"principal", row.PrincipalSubject, "operator"},
		{"kind", row.PrincipalKind, "mtls"},
		{"credential", row.CredentialID, "cred-1"},
		{"issuer", row.Issuer, "test-ca"},
		{"object", row.Object, "admin:policy"},
		{"action", row.Action, "write"},
		{"operation", row.OperationID, "createPolicySet"},
		{"request", row.RequestID, "req-1"},
		{"outcome", row.Outcome, "committed"},
		{"ticket", row.Ticket, "INC-7"},
		{"config set", row.ConfigSetID, "set-1"},
		{"after hash", row.AfterHash, "after-hash"},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if row.ConfigRevision != 7 {
		t.Fatalf("config revision = %d, want 7", row.ConfigRevision)
	}
	if row.EventID == "" || row.At == "" {
		t.Fatalf("audit row lacks identity/time: %+v", row)
	}
}

// TestPolicyMutationAuditFailureAbortsTheChange proves a rejected audit insert
// rolls the policy change back: no audit row means no committed mutation.
func TestPolicyMutationAuditFailureAbortsTheChange(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := applyUsageAdminAuditMigration(store.db); err != nil {
		t.Fatal(err)
	}
	if err := applyPolicyRuntimeMigration(store.db); err != nil {
		t.Fatal(err)
	}

	meta := AdminAuditMeta{Subject: "operator", Kind: "mtls", Object: "admin:policy", Action: "write", OperationID: "createPolicySet"}
	writer, err := policyMutationAuditWriter(WithAdminAuditMeta(context.Background(), meta))
	if err != nil {
		t.Fatal(err)
	}
	// Refuse every audit insert: the change's own marker must not survive.
	if err := store.db.Migrator().DropTable(usageAdminAuditTable); err != nil {
		t.Fatal(err)
	}
	err = store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&policyMutationRevision{ID: 99, Revision: 1}).Error; err != nil {
			return err
		}
		return writer(tx, 1, policyResourceResult{SetID: "set-1"})
	})
	if err == nil {
		t.Fatal("mutation committed without its audit row")
	}
	var count int64
	if err := store.db.Model(&policyMutationRevision{}).Where("id = ?", 99).Count(&count).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("policy change survived a failed audit insert")
	}
}

// TestPolicyMutationAuditRequiresAuthenticatedContext proves the binding fails
// closed when the request carries no authenticated audit context, and that the
// handler answers the fixed unavailable problem before any policy work.
func TestPolicyMutationAuditRequiresAuthenticatedContext(t *testing.T) {
	if _, err := policyMutationAuditWriter(context.Background()); !errors.Is(err, ErrPolicyAuditUnavailable) {
		t.Fatalf("missing audit context = %v, want ErrPolicyAuditUnavailable", err)
	}
	if _, err := policyMutationAuditWriter(WithAdminAuditMeta(context.Background(), AdminAuditMeta{})); !errors.Is(err, ErrPolicyAuditUnavailable) {
		t.Fatalf("empty subject = %v, want ErrPolicyAuditUnavailable", err)
	}

	handler := &policyAdminHandler{auditFor: policyMutationAuditWriter}
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/policy-sets", nil)
	request.Header.Set("If-Match", "0")
	ctx := WithPrincipal(request.Context(), Principal{Subject: "operator", Kind: "mtls"})
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.mutate(recorder, request, policyResourceCommand{Kind: "create-set", Policy: []string{"p, role, *, metrics, read"}}, http.StatusCreated)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", recorder.Code, recorder.Body.String())
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "unavailable" {
		t.Fatalf("code = %q, want unavailable", problem.Code)
	}
}
