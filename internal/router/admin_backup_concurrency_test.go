// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestBackupWithConcurrentAuditedMutations(t *testing.T) {
	t.Setenv("ALPHA_API_KEY", "synthetic-alpha")
	t.Setenv("BETA_API_KEY", "synthetic-beta")
	svc := adminOperationsTestService(t, configAdminTestDB(t), "concurrent-backup")
	svc.cfg.Server.AdminAPI.RateLimit = AdminRateLimitConfig{ReadRPS: 1000, ReadBurst: 1000, WriteRPS: 100, WriteBurst: 100}
	mux := adminOperationsServiceMux(t, svc)
	other, err := openUsageDB(svc.cfg.Server.UsageDB)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := other.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	writer := &usageStore{db: other}
	holding := make(chan struct{}, 1)
	// The label mutation already holds SQLite's writer reservation here.
	// Keep it open long enough to overlap the backup on a separate pool.
	err = other.Callback().Create().Before("gorm:create").Register("test:overlap-backup", func(tx *gorm.DB) {
		if tx.Statement.Table == (contentCaptureRecord{}).TableName() {
			holding <- struct{}{}
			time.Sleep(50 * time.Millisecond)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	const iterations = 100
	for i := range iterations {
		id := fmt.Sprintf("concurrent-label-%d", i)
		done := make(chan error, 1)
		go func() {
			done <- writer.CreateTrainingOutcomeAudited(contentCaptureRecord{RequestID: id}, AdminAuditRecord{
				EventID: id, Domain: AdminGlobalDomain, Object: "content:training", Action: "label",
			})
		}()
		select {
		case <-holding:
		case err := <-done:
			t.Fatalf("mutation finished before acquiring the writer reservation: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the mutation")
		}
		start := adminOperationsTestRequest(t, mux, http.MethodPost, "/admin/v1/control-plane/backups", map[string]any{})
		if start.Code != http.StatusAccepted {
			t.Fatalf("backup %d returned %d", i, start.Code)
		}
		op := awaitAdminOperationAPI(t, mux, start)
		if err := <-done; err != nil {
			t.Fatalf("mutation %d: %v", i, err)
		}
		if op.Status != adminOperationSucceeded {
			t.Errorf("backup %d returned %s (%s)", i, op.Status, op.ErrorCode)
		}
	}

	export := adminAuditExportHandlers(svc.usage.db, nil)["admin.audit.export"]
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil)
	ctx := WithAdminAuditMeta(req.Context(), AdminAuditMeta{Subject: "adminkey:operations-test", Domain: AdminGlobalDomain})
	resp := httptest.NewRecorder()
	export(resp, req.WithContext(WithAdminPage(ctx, &AdminPage{Limit: 2*iterations + 1})))
	var doc adminAuditExportDocument
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if resp.Code != http.StatusOK || doc.HasMore || len(doc.Items) != 2*iterations {
		t.Fatalf("audit export: status=%d rows=%d more=%t", resp.Code, len(doc.Items), doc.HasMore)
	}
	if err := VerifyAdminAuditExport(resp.Body); err != nil {
		t.Fatal(err)
	}
}
