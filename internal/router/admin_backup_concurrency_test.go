// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
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
	replicaConfig := *svc.cfg
	replica, err := newServiceWithVerifiedFixture(&replicaConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replica.Close)
	var controlPath string
	if err := svc.configControlPlane.Raw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&controlPath).Error; err != nil {
		t.Fatal(err)
	}
	replica.configControlPlane, err = openUsageDB(UsageDBConfig{Driver: "sqlite", Path: controlPath})
	if err != nil {
		t.Fatal(err)
	}
	controlPool, err := replica.configControlPlane.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlPool.Close() })
	muxes := []*http.ServeMux{mux, adminOperationsServiceMux(t, replica)}
	writer := replica.usage
	other := writer.db
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
	const workers = 4
	for i := range iterations / workers {
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
		t.Run(fmt.Sprintf("batch-%d", i), func(t *testing.T) {
			for n := range workers {
				t.Run(fmt.Sprintf("backup-%d", n), func(t *testing.T) {
					t.Parallel()
					mux := muxes[n%len(muxes)]
					start := adminOperationsTestRequest(t, mux, http.MethodPost, "/admin/v1/control-plane/backups", map[string]any{})
					if start.Code != http.StatusAccepted {
						t.Fatalf("backup returned %d: %s", start.Code, start.Body.String())
					}
					op := awaitAdminOperationAPI(t, mux, start)
					if op.Status != adminOperationSucceeded {
						t.Errorf("backup returned %s (%s)", op.Status, op.ErrorCode)
					}
				})
			}
		})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	for _, service := range []*Service{svc, replica} {
		if err := service.flushAdminCredentialAuditCounters(context.Background(), true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	export := adminAuditExportHandlers(svc.usage.db, svc.configControlPlane)["admin.audit.export"]
	counts := map[string]int{}
	domains := map[string]bool{AdminGlobalDomain: true}
	domains[string(AdminDomainGlobal)] = true
	domains[string(AdminDomainCredential)] = true
	for domain := range domains {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil)
		ctx := WithAdminAuditMeta(req.Context(), AdminAuditMeta{Subject: "adminkey:operations-test", Domain: domain})
		resp := httptest.NewRecorder()
		export(resp, req.WithContext(WithAdminPage(ctx, &AdminPage{Limit: 500})))
		var doc adminAuditExportDocument
		if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if resp.Code != http.StatusOK || doc.HasMore {
			t.Fatalf("domain %q status=%d more=%t", domain, resp.Code, doc.HasMore)
		}
		if err := VerifyAdminAuditExport(resp.Body); err != nil {
			t.Fatalf("domain %q: %v", domain, err)
		}
		for _, row := range doc.Items {
			counts[row.Source]++
		}
	}
	if counts["usage"] != iterations+iterations/workers || counts["control"] == 0 || counts["credential"] == 0 {
		t.Fatalf("verified audit rows by source: %v", counts)
	}
}
