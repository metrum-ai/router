// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConfigChangeEventWritersStoreHashesAndRequestHash(t *testing.T) {
	db := configAdminTestDB(t)
	var events int64
	if err := db.Model(&adminAuditControlExportRecord{}).Where("row_hash = ''").Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("import/activate wrote %d unhashed events", events)
	}
	var runtime configControlPlaneRuntimeRow
	if err := db.Where("runtime_scope = ?", "prod").First(&runtime).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGroupTargets(db, "prod", GroupTargetsWriteRequest{
		Group: "agent-default", Targets: []GroupTargetSpec{{Provider: "beta", ModelRef: "b-one"}},
		Reason: "chain writer test", IdempotencyKey: "chain-writer-test",
	}, "adminkey:test", GroupWritePolicy{}, testAdminAuditMeta(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := bumpConfigRevision(tx, "prod", runtime.ActiveSetID, "resource_update", "adminkey:test", testAdminAuditMeta(), "", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&adminAuditControlExportRecord{}).Where("row_hash = ''").Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("config writers left %d unhashed events", events)
	}
	handler := adminAuditExportHandlers(nil, db)["admin.audit.export"]
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil)
	ctx := WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/concurrent-pg"})
	response := httptest.NewRecorder()
	handler(response, request.WithContext(ctx))
	if response.Code != http.StatusOK {
		t.Fatalf("control export=%d %s", response.Code, response.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(response.Body.String())); err != nil {
		t.Fatalf("verify config writer export: %v", err)
	}
	var group adminAuditControlExportRecord
	if err := db.Where("idempotency_key = ?", "chain-writer-test").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if group.RequestHash == "" || group.RowHash == "" {
		t.Fatalf("group event missed request hash or chain link: %+v", group)
	}
}

func TestAdminAuditChainMigrationUsesAssignedIDAndNextSchema(t *testing.T) {
	usage := AdminAuditChainUsageMigrationDefinition()
	control := AdminAuditChainControlMigrationDefinition()
	if usage.ID != 2026111628 || control.ID != 2026111628 {
		t.Fatalf("assigned migration IDs: usage=%d control=%d", usage.ID, control.ID)
	}
	if usage.SchemaVersion != 24 || control.SchemaVersion != 20 {
		t.Fatalf("next schema versions: usage=%d control=%d", usage.SchemaVersion, control.SchemaVersion)
	}
	if len(usage.Dependencies) != 1 || usage.Dependencies[0] != offlineOperationMarkerMigrationID {
		t.Fatalf("usage migration dependencies=%v", usage.Dependencies)
	}
	if len(control.Dependencies) != 1 || control.Dependencies[0] != offlineOperationMarkerMigrationID {
		t.Fatalf("control migration dependencies=%v", control.Dependencies)
	}
}

func TestConcurrentSQLiteAuditWritersProduceOneChain(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	const writers = 32
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			record := AdminAuditRecord{EventID: fmt.Sprintf("concurrent-%02d", i), At: time.Date(2026, 11, 16, 13, 0, i, 0, time.UTC), Domain: "team/concurrent", Outcome: "committed", Object: "admin:config"}
			err := withSQLiteBusyRetry(func() error {
				return store.db.Transaction(func(tx *gorm.DB) error { return insertAdminAuditUsageTx(tx, record) })
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	var rows []adminAuditExportRecord
	if err := store.db.Where("domain = ?", "team/concurrent").Order("at ASC").Order("event_id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != writers {
		t.Fatalf("concurrent events=%d, want %d", len(rows), writers)
	}
	previous := ""
	for i, record := range rows {
		row := adminAuditRowFromExportRecord(record, "usage")
		hash, err := adminAuditChainHash(previous, row)
		if err != nil || record.PreviousHash != previous || record.RowHash != hash {
			t.Fatalf("row %d (%s at %s) breaks concurrent chain: previous=%s stored_previous=%s row_hash=%s want=%s err=%v", i, record.EventID, record.At, previous, record.PreviousHash, record.RowHash, hash, err)
		}
		previous = hash
	}
}

func TestDuplicateUsageAuditEventDoesNotAdvanceChainHead(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	first := AdminAuditRecord{EventID: "duplicate-event", At: time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC), Domain: "team/retry", Outcome: "committed", Object: "admin:config"}
	if err := store.db.Transaction(func(tx *gorm.DB) error { return insertAdminAuditUsageTx(tx, first) }); err != nil {
		t.Fatal(err)
	}
	initialHead := readAdminAuditChainHead(store.db, "usage", first.Domain, usageAdminAuditTable, "domain")
	retry := first
	retry.Outcome = "denied"
	if err := store.db.Transaction(func(tx *gorm.DB) error { return insertAdminAuditUsageTx(tx, retry) }); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", first.EventID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate event rows=%d, want 1", count)
	}
	if got := readAdminAuditChainHead(store.db, "usage", first.Domain, usageAdminAuditTable, "domain"); got != initialHead {
		t.Fatalf("duplicate changed chain head to %q, want %q", got, initialHead)
	}
	handler := adminAuditExportHandlers(store.db, nil)["admin.audit.export"]
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: first.Domain}))
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("export after duplicate retry=%d %s", response.Code, response.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(response.Body.String())); err != nil {
		t.Fatalf("verify export after duplicate retry: %v", err)
	}
}

func TestAdminAuditDeletionMarkerCannotBeDeletedByAPIAndRetentionCanPrune(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	marker := AdminAuditRecord{EventID: "protected-delete-marker", At: time.Now().UTC().Add(-200 * 24 * time.Hour), Domain: AdminGlobalDomain, Outcome: "committed", Action: "delete", Detail: "deleted_event=old-event"}
	if err := store.db.Transaction(func(tx *gorm.DB) error { return insertAdminAuditUsageTx(tx, marker) }); err != nil {
		t.Fatal(err)
	}
	handler := adminAuditExportHandlers(store.db, nil)["admin.audit.delete"]
	request := httptest.NewRequest(http.MethodDelete, "/admin/v1/audit-events/protected-delete-marker", nil)
	request.SetPathValue("event_id", marker.EventID)
	request = request.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "global-operator", Domain: AdminGlobalDomain}))
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "audit-marker-protected") {
		t.Fatalf("marker deletion status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := PurgeAdminAuditEventsBefore(store.db, time.Now().UTC().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", marker.EventID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expired marker rows=%d, want retention to prune it", count)
	}
}

func TestAdminAuditChainMigrationBackfillsSQLiteInBatches(t *testing.T) {
	db := openAdminAuditMigrationSQLite(t)
	legacyUsageDDL := strings.Replace(usageAdminAuditDDL[0], `, previous_hash TEXT NOT NULL DEFAULT '', row_hash TEXT NOT NULL DEFAULT ''`, "", 1)
	if err := db.Exec(legacyUsageDDL).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < adminAuditChainMigrationBatchSize*2+1; i++ {
		at := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		if err := db.Exec(`INSERT INTO router_admin_audit_events (event_id,at,domain,outcome,detail) VALUES (?,?,?,?,?)`, fmt.Sprintf("usage-%04d", i), at, "team/migration", "committed", "legacy").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyAdminAuditChainUsage(db); err != nil {
		t.Fatal(err)
	}
	var missing int64
	if err := db.Table(usageAdminAuditTable).Where("row_hash = '' OR previous_hash IS NULL").Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("unbackfilled usage rows=%d err=%v", missing, err)
	}
	control := openAdminAuditMigrationSQLite(t)
	legacyControlDDL := `CREATE TABLE router_config_change_events (event_id TEXT PRIMARY KEY, at TEXT NOT NULL, actor TEXT NOT NULL DEFAULT '', actor_kind TEXT NOT NULL DEFAULT '', actor_credential_id TEXT NOT NULL DEFAULT '', actor_issuer TEXT NOT NULL DEFAULT '', authz_domain TEXT NOT NULL DEFAULT '', object TEXT NOT NULL DEFAULT '', authz_action TEXT NOT NULL DEFAULT '', operation_id TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', before_hash TEXT NOT NULL DEFAULT '', after_hash TEXT NOT NULL DEFAULT '', config_set_id TEXT NOT NULL DEFAULT '', runtime_scope TEXT NOT NULL DEFAULT '', ticket TEXT NOT NULL DEFAULT '', action TEXT NOT NULL DEFAULT '', idempotency_key TEXT NOT NULL DEFAULT '', request_hash TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', prev_config_set_id TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '', forced INTEGER NOT NULL DEFAULT 0, config_revision BIGINT NOT NULL DEFAULT 0)`
	if err := control.Exec(legacyControlDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := control.Exec(strings.Replace(adminCredentialEventsDDL[0], `, previous_hash TEXT NOT NULL DEFAULT '', row_hash TEXT NOT NULL DEFAULT ''`, "", 1)).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < adminAuditChainMigrationBatchSize*2+1; i++ {
		at := time.Date(2026, 3, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		if err := control.Exec(`INSERT INTO router_config_change_events (event_id,at,authz_domain,action,request_hash,outcome) VALUES (?,?,?,?,?,?)`, fmt.Sprintf("control-%04d", i), at, "team/migration", "write", "request-fingerprint", "committed").Error; err != nil {
			t.Fatal(err)
		}
		if err := control.Exec(`INSERT INTO router_admin_credential_events (event_id,at,kind,credential_id,subject,outcome,lifecycle,count) VALUES (?,?,?,?,?,?,?,1)`, fmt.Sprintf("credential-%04d", i), at, "adminkey", "cred", "subject", "success", "auth").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyAdminAuditChainControl(control); err != nil {
		t.Fatal(err)
	}
	if err := control.Table("router_config_change_events").Where("row_hash = ''").Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("unbackfilled control rows=%d err=%v", missing, err)
	}
	if err := control.Table(adminCredentialEventsTable).Where("row_hash = ''").Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("unbackfilled credential rows=%d err=%v", missing, err)
	}
}

func openAdminAuditMigrationSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit-migration.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestAdminAuditChainMigrationBackfillsPostgresInBatches(t *testing.T) {
	if strings.TrimSpace(os.Getenv("ROUTER_TEST_PG_DSN")) == "" {
		t.Skip("set ROUTER_TEST_PG_DSN to a disposable PostgreSQL database")
	}
	control, cleanup, schema := configAdminPostgresTestDB(t)
	defer cleanup()
	setID, err := ImportCanonicalYAML(control, "prod", "audit-migration", []byte(configAdminTestYAML), "migration-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Exec(`DELETE FROM router_config_change_events`).Error; err != nil {
		t.Fatal(err)
	}
	if err := control.Exec(`DELETE FROM router_admin_credential_events`).Error; err != nil {
		t.Fatal(err)
	}
	if err := control.Exec(`ALTER TABLE router_config_change_events DROP COLUMN previous_hash, DROP COLUMN row_hash`).Error; err != nil {
		t.Fatal(err)
	}
	if err := control.Exec(`ALTER TABLE router_admin_credential_events DROP COLUMN previous_hash, DROP COLUMN row_hash`).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < adminAuditChainMigrationBatchSize+1; i++ {
		at := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		if err := control.Exec(`INSERT INTO router_config_change_events (event_id,runtime_scope,config_set_id,action,actor,at,authz_domain,request_hash,outcome) VALUES (?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("pg-control-%04d", i), "prod", setID, "write", "actor", at, "team/pg", "request-fingerprint", "committed").Error; err != nil {
			t.Fatal(err)
		}
		if err := control.Exec(`INSERT INTO router_admin_credential_events (event_id,at,kind,credential_id,subject,outcome,lifecycle,count) VALUES (?,?,?,?,?,?,?,1)`, fmt.Sprintf("pg-credential-%04d", i), at, "adminkey", "cred", "subject", "success", "auth").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyAdminAuditChainControl(control); err != nil {
		t.Fatalf("control schema %s: %v", schema, err)
	}
	if err := verifyAdminAuditChainControl(control); err != nil {
		t.Fatal(err)
	}
	var missing int64
	if err := control.Table("router_config_change_events").Where("row_hash = ''").Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("unbackfilled PostgreSQL control rows=%d err=%v", missing, err)
	}
	if err := control.Table(adminCredentialEventsTable).Where("row_hash = ''").Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("unbackfilled PostgreSQL credential rows=%d err=%v", missing, err)
	}
	// Exercise the usage migration in the same disposable PostgreSQL schema.
	usageDB, err := gorm.Open(postgres.Open(pgDSNWithSearchPath(os.Getenv("ROUTER_TEST_PG_DSN"), schema)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	usageSQL, err := usageDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer usageSQL.Close()
	legacyUsageDDL := strings.Replace(usageAdminAuditDDL[0], `, previous_hash TEXT NOT NULL DEFAULT '', row_hash TEXT NOT NULL DEFAULT ''`, "", 1)
	if err := usageDB.Exec(legacyUsageDDL).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < adminAuditChainMigrationBatchSize+1; i++ {
		at := time.Date(2026, 2, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		if err := usageDB.Exec(`INSERT INTO router_admin_audit_events (event_id,at,domain,outcome) VALUES (?,?,?,?)`, fmt.Sprintf("pg-usage-%04d", i), at, "team/pg", "committed").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyAdminAuditChainUsage(usageDB); err != nil {
		t.Fatal(err)
	}
	if err := usageDB.Table(usageAdminAuditTable).Where("row_hash = ''").Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("unbackfilled PostgreSQL usage rows=%d err=%v", missing, err)
	}
}

func TestConcurrentPostgresControlAuditWritersProduceOneChain(t *testing.T) {
	if strings.TrimSpace(os.Getenv("ROUTER_TEST_PG_DSN")) == "" {
		t.Skip("set ROUTER_TEST_PG_DSN to a disposable PostgreSQL database")
	}
	db, cleanup, _ := configAdminPostgresTestDB(t)
	defer cleanup()
	configSetID, err := ImportCanonicalYAML(db, "pg", "concurrent-audit", []byte(configAdminTestYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := chainedConfigChangeEvent{EventID: fmt.Sprintf("concurrent-pg-%02d", i), RuntimeScope: "pg", ConfigSetID: configSetID, Action: "test", Actor: "test", At: time.Date(2026, 11, 16, 12, 0, i, 0, time.UTC).Format(time.RFC3339Nano), AuthzDomain: "team/concurrent-pg", Object: "admin:config", AuthzAction: "write", Outcome: "committed"}
			errCh <- db.Transaction(func(tx *gorm.DB) error { return insertChainedConfigChangeEventTx(tx, event) })
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	handler := adminAuditExportHandlers(nil, db)["admin.audit.export"]
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export?source=control&domain=team%2Fconcurrent-pg", nil)
	ctx := WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/concurrent-pg"})
	response := httptest.NewRecorder()
	handler(response, req.WithContext(ctx))
	if response.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", response.Code, response.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(response.Body.String())); err != nil {
		t.Fatalf("concurrent Postgres audit chain: %v", err)
	}
}
