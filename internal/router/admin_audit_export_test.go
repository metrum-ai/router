// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminAuditExportPagesVerifyAsOneChainAndFilterDomain(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []AdminAuditRecord{
		{EventID: "event-a", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Domain: "team/prod", Outcome: "committed", Object: "admin:policy", Issuer: "issuer-a", Ticket: "INC-42", ConfigSetID: "set-42", Detail: "deleted_event=old-event"},
		{EventID: "event-b", At: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Domain: "team/prod", Outcome: "denied", Object: "admin:config"},
		{EventID: "event-other", At: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), Domain: "other/prod", Outcome: "committed"},
	} {
		if err := insertAdminAuditUsageTx(store.db, row); err != nil {
			t.Fatal(err)
		}
	}
	handler := adminAuditExportHandlers(store.db, nil)["admin.audit.export"]
	codec := NewAdminCursorCodec([]byte("admin-audit-export-test-key-0001"))
	firstWriter := httptest.NewRecorder()
	firstRequest := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil)
	firstCtx := WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"})
	firstCtx = WithAdminPage(firstCtx, &AdminPage{Limit: 1, Sort: "at", Direction: "asc", writer: firstWriter, request: firstRequest, codec: codec, op: Operation{ID: "exportAdminAudit"}})
	handler(firstWriter, firstRequest.WithContext(firstCtx))
	if firstWriter.Code != 200 {
		t.Fatalf("first page status=%d body=%s", firstWriter.Code, firstWriter.Body.String())
	}
	var first adminAuditExportDocument
	if err := json.Unmarshal(firstWriter.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].EventID != "event-a" || first.Items[0].PreviousHash != "" {
		t.Fatalf("first page=%+v", first)
	}
	if first.Items[0].Issuer != "issuer-a" || first.Items[0].Ticket != "INC-42" || first.Items[0].ConfigSetID != "set-42" || first.Items[0].Detail != "deleted_event=old-event" {
		t.Fatalf("full audit fields missing: %+v", first.Items[0])
	}
	calculated, err := adminAuditChainHash(first.Items[0].PreviousHash, first.Items[0].adminAuditExportRow)
	if err != nil || calculated != first.Items[0].Hash || first.Checksum != calculated {
		t.Fatalf("first chain hash=%s checksum=%s err=%v", calculated, first.Checksum, err)
	}
	if !strings.Contains(firstWriter.Header().Get("Link"), "cursor=") {
		t.Fatalf("next page link missing: %s", firstWriter.Header().Get("Link"))
	}
	if err := VerifyAdminAuditExport(bytes.NewReader(firstWriter.Body.Bytes())); err != nil {
		t.Fatalf("verify first page: %v", err)
	}
	secondWriter := httptest.NewRecorder()
	secondRequest := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil)
	page := &AdminPage{Limit: 1, Sort: "at", Direction: "asc", HasCursor: true, Cursor: adminCursorPayload{Values: []string{first.Items[0].At, first.Items[0].EventID, first.Items[0].Source, first.ChainTo}}, writer: secondWriter, request: secondRequest, codec: codec, op: Operation{ID: "exportAdminAudit"}}
	secondCtx := WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"})
	secondCtx = WithAdminPage(secondCtx, page)
	handler(secondWriter, secondRequest.WithContext(secondCtx))
	if secondWriter.Code != 200 {
		t.Fatalf("second page status=%d body=%s", secondWriter.Code, secondWriter.Body.String())
	}
	var second adminAuditExportDocument
	if err := json.Unmarshal(secondWriter.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].EventID != "event-b" || second.Items[0].PreviousHash != first.ChainTo {
		t.Fatalf("second page=%+v", second)
	}
	calculated, err = adminAuditChainHash(first.ChainTo, second.Items[0].adminAuditExportRow)
	if err != nil || calculated != second.Items[0].Hash || second.Checksum != calculated {
		t.Fatalf("second chain hash=%s checksum=%s err=%v", calculated, second.Checksum, err)
	}
	if err := VerifyAdminAuditExport(bytes.NewReader(secondWriter.Body.Bytes())); err != nil {
		t.Fatalf("verify second page: %v", err)
	}
	if err := VerifyAdminAuditExportPages(bytes.NewReader(firstWriter.Body.Bytes()), bytes.NewReader(secondWriter.Body.Bytes())); err != nil {
		t.Fatalf("verify complete paginated chain: %v", err)
	}
	if err := VerifyAdminAuditExportPages(bytes.NewReader(firstWriter.Body.Bytes())); err == nil {
		t.Fatal("incomplete page sequence verified")
	}
}

func TestAdminAuditOfflineVerifierDetectsTamperDeleteInsertAndReorder(t *testing.T) {
	rows := []adminAuditExportRow{{Source: "usage", EventID: "a", At: "2026-01-01T00:00:00Z", Domain: "team/prod", Outcome: "committed"}, {Source: "usage", EventID: "b", At: "2026-01-02T00:00:00Z", Domain: "team/prod", Outcome: "committed"}, {Source: "usage", EventID: "c", At: "2026-01-03T00:00:00Z", Domain: "team/prod", Outcome: "committed"}}
	doc := adminAuditExportDocument{Items: make([]adminAuditExportItem, 0, len(rows)), ChainStarts: map[string]string{"usage": ""}, ChainHeads: map[string]string{}, HasMore: false}
	previous := ""
	for _, row := range rows {
		hash, err := adminAuditChainHash(previous, row)
		if err != nil {
			t.Fatal(err)
		}
		doc.Items = append(doc.Items, adminAuditExportItem{adminAuditExportRow: row, PreviousHash: previous, Hash: hash})
		previous = hash
	}
	doc.ChainHeads["usage"] = previous
	doc.ChainTo = previous
	doc.Checksum = previous
	encode := func(d adminAuditExportDocument) []byte {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := VerifyAdminAuditExport(bytes.NewReader(encode(doc))); err != nil {
		t.Fatalf("valid export rejected: %v", err)
	}
	tampered := doc
	tampered.Items = append([]adminAuditExportItem(nil), doc.Items...)
	tampered.Items[1].Outcome = "denied"
	deleted := doc
	deleted.Items = append([]adminAuditExportItem(nil), doc.Items[:2]...)
	deletedFirst := doc
	deletedFirst.Items = append([]adminAuditExportItem(nil), doc.Items[1:]...)
	deletedMiddle := doc
	deletedMiddle.Items = []adminAuditExportItem{doc.Items[0], doc.Items[2]}
	inserted := doc
	inserted.Items = append([]adminAuditExportItem(nil), doc.Items...)
	inserted.Items = append(inserted.Items, doc.Items[1])
	reordered := doc
	reordered.Items = append([]adminAuditExportItem(nil), doc.Items...)
	reordered.Items[0], reordered.Items[1] = reordered.Items[1], reordered.Items[0]
	for name, bad := range map[string]adminAuditExportDocument{"tampered": tampered, "deleted-tail": deleted, "deleted-head": deletedFirst, "deleted-middle": deletedMiddle, "inserted": inserted, "reordered": reordered} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyAdminAuditExport(bytes.NewReader(encode(bad))); err == nil {
				t.Fatal("invalid export verified")
			}
		})
	}
}

func TestAdminAuditStoredAnchorDetectsDeletionAndTampering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gorm.DB) error
	}{{"delete-head", func(db *gorm.DB) error {
		return db.Exec(`DELETE FROM ` + usageAdminAuditTable + ` WHERE event_id='a'`).Error
	}}, {"delete-tail", func(db *gorm.DB) error {
		return db.Exec(`DELETE FROM ` + usageAdminAuditTable + ` WHERE event_id='c'`).Error
	}}, {"edit-row", func(db *gorm.DB) error {
		return db.Exec(`UPDATE ` + usageAdminAuditTable + ` SET detail='forged' WHERE event_id='b'`).Error
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			store := adminUsageAuditTestStore(t)
			if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
				t.Fatal(err)
			}
			if err := ensureAdminAuditChainAnchors(store.db); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b", "c"} {
				if err := insertAdminAuditUsageTx(store.db, AdminAuditRecord{EventID: id, At: time.Date(2026, 1, int(id[0]-'a'+1), 0, 0, 0, 0, time.UTC), Domain: "team/prod", Outcome: "committed"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.mutate(store.db); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"}))
			adminAuditExportHandlers(store.db, nil)["admin.audit.export"](w, r)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("tampered chain exported with %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminAuditDeleteRequiresMatchingDomainAndWritesAudit(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := insertAdminAuditUsageTx(store.db, AdminAuditRecord{EventID: "target", At: time.Now().UTC(), Domain: "team/prod", Outcome: "committed"}); err != nil {
		t.Fatal(err)
	}
	handler := adminAuditExportHandlers(store.db, nil)["admin.audit.delete"]
	for _, tc := range []struct {
		domain string
		status int
	}{{"other/prod", 404}, {"team/prod", 200}} {
		r := httptest.NewRequest("DELETE", "/admin/v1/audit-events/target", nil)
		r.SetPathValue("event_id", "target")
		r = r.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Domain: tc.domain, Subject: "operator", Kind: "mtls", OperationID: "deleteAdminAuditEvent"}))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != tc.status {
			t.Fatalf("domain=%s status=%d body=%s", tc.domain, w.Code, w.Body.String())
		}
	}
	var eventCount, deletionAudits int64
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", "target").Count(&eventCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Table(usageAdminAuditTable).Where("operation_id = ? AND action = ?", "deleteAdminAuditEvent", "delete").Count(&deletionAudits).Error; err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || deletionAudits != 1 {
		t.Fatalf("target rows=%d deletion audit rows=%d", eventCount, deletionAudits)
	}
	var deletion adminAuditExportRecord
	if err := store.db.Where("operation_id = ? AND action = ?", "deleteAdminAuditEvent", "delete").First(&deletion).Error; err != nil {
		t.Fatal(err)
	}
	if deletion.Domain != AdminGlobalDomain {
		t.Fatalf("deletion audit domain=%q", deletion.Domain)
	}
	globalExport := adminAuditExportHandlers(store.db, nil)["admin.audit.export"]
	globalRequest := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "global-operator", Domain: AdminGlobalDomain}))
	globalResponse := httptest.NewRecorder()
	globalExport(globalResponse, globalRequest)
	if globalResponse.Code != http.StatusOK || !strings.Contains(globalResponse.Body.String(), deletion.EventID) {
		t.Fatalf("global export omitted deletion marker: status=%d body=%s", globalResponse.Code, globalResponse.Body.String())
	}
	retry := httptest.NewRequest("DELETE", "/admin/v1/audit-events/"+deletion.EventID, nil)
	retry.SetPathValue("event_id", deletion.EventID)
	retry = retry.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Domain: "team/prod", Subject: "operator"}))
	retryWriter := httptest.NewRecorder()
	handler(retryWriter, retry)
	if retryWriter.Code != http.StatusNotFound {
		t.Fatalf("delete privilege removed its own audit row: status=%d body=%s", retryWriter.Code, retryWriter.Body.String())
	}
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", deletion.EventID).Count(&deletionAudits).Error; err != nil || deletionAudits != 1 {
		t.Fatalf("deletion audit rows=%d err=%v", deletionAudits, err)
	}
}

func TestAdminAuditVerifyAfterPurgeKeepsMarkerChain(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-200 * 24 * time.Hour)
	for i, id := range []string{"global-a", "global-b", "global-c"} {
		if err := store.db.Transaction(func(tx *gorm.DB) error {
			return insertAdminAuditUsageTx(tx, AdminAuditRecord{EventID: id, At: base.Add(time.Duration(i) * time.Second), Domain: AdminGlobalDomain, Outcome: "committed"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	deleteHandler := adminAuditExportHandlers(store.db, nil)["admin.audit.delete"]
	request := httptest.NewRequest(http.MethodDelete, "/admin/v1/audit-events/global-a", nil)
	request.SetPathValue("event_id", "global-a")
	request = request.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: AdminGlobalDomain}))
	deleted := httptest.NewRecorder()
	deleteHandler(deleted, request)
	if deleted.Code != http.StatusOK {
		t.Fatalf("real audit deletion status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		return insertAdminAuditUsageTx(tx, AdminAuditRecord{EventID: "global-after-marker", At: time.Now().UTC().Add(time.Second), Domain: AdminGlobalDomain, Outcome: "committed"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PurgeAdminAuditEventsBefore(store.db, time.Now().UTC().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	exportHandler := adminAuditExportHandlers(store.db, nil)["admin.audit.export"]
	exportRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: AdminGlobalDomain}))
	export := httptest.NewRecorder()
	exportHandler(export, exportRequest)
	if export.Code != http.StatusOK {
		t.Fatalf("export after purge=%d body=%s", export.Code, export.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(export.Body.String())); err != nil {
		t.Fatalf("verify after retention re-anchor: %v", err)
	}
	if !strings.Contains(export.Body.String(), "global-after-marker") || !strings.Contains(export.Body.String(), "deleted_event=global-a") {
		t.Fatalf("retained chain rows missing after retention re-anchor: %s", export.Body.String())
	}
}

func TestAdminAuditRetentionDeletesControlTargetsAndReanchors(t *testing.T) {
	db := configAdminTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	var runtime configControlPlaneRuntimeRow
	if err := db.Where("runtime_scope = ?", "prod").First(&runtime).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err := db.Exec(`INSERT INTO router_config_change_events (event_id, runtime_scope, config_set_id, action, at, actor, authz_domain, object, authz_action, operation_id, request_id, outcome, config_revision, previous_hash, row_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "retention-control", "prod", runtime.ActiveSetID, "replace", at, "operator", "team/prod", "admin:config", "write", "replace", "request", "committed", 1, "", "hash-retention").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO router_config_change_event_targets (event_id, phase, sequence, provider_name) VALUES (?, ?, ?, ?)`, "retention-control", "before", 0, "provider").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PurgeAdminAuditEventsBefore(db, time.Now().UTC().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var events, targets int64
	if err := db.Table("router_config_change_events").Where("event_id = ?", "retention-control").Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("router_config_change_event_targets").Where("event_id = ?", "retention-control").Count(&targets).Error; err != nil {
		t.Fatal(err)
	}
	if events != 0 || targets != 0 {
		t.Fatalf("retention left event/target rows: %d/%d", events, targets)
	}
}

func TestAdminAuditDeletePrunesOnlyFromCheckpointBoundary(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureAdminAuditChainAnchors(store.db); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"a", "b", "c"} {
		if err := insertAdminAuditUsageTx(store.db, AdminAuditRecord{EventID: id, At: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), Domain: "team/prod", Outcome: "committed"}); err != nil {
			t.Fatal(err)
		}
	}
	var first adminAuditExportRecord
	if err := store.db.Where("event_id = ?", "a").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	handler := adminAuditExportHandlers(store.db, nil)["admin.audit.delete"]
	deleteEvent := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("DELETE", "/admin/v1/audit-events/"+id, nil)
		r.SetPathValue("event_id", id)
		r = r.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Domain: "team/prod", Subject: "operator", OperationID: "deleteAdminAuditEvent"}))
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if w := deleteEvent("b"); w.Code != http.StatusConflict {
		t.Fatalf("middle-row delete=%d %s, want conflict", w.Code, w.Body.String())
	}
	if w := deleteEvent("a"); w.Code != http.StatusOK {
		t.Fatalf("oldest-row delete=%d %s", w.Code, w.Body.String())
	}
	var start string
	if err := store.db.Raw(`SELECT start_hash FROM ` + adminAuditChainAnchorsTable + ` WHERE source='usage' AND domain='team/prod'`).Row().Scan(&start); err != nil {
		t.Fatal(err)
	}
	if start != first.RowHash {
		t.Fatalf("prune checkpoint=%s, want deleted row hash %s", start, first.RowHash)
	}
	export := adminAuditExportHandlers(store.db, nil)["admin.audit.export"]
	r := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"}))
	w := httptest.NewRecorder()
	export(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("export after checkpointed prune=%d %s", w.Code, w.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(w.Body.String())); err != nil {
		t.Fatalf("checkpointed export did not verify: %v", err)
	}
}

func TestAdminAuditExportIncludesControlPlaneEvents(t *testing.T) {
	db := configAdminTestDB(t)
	stamp := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	var runtime configControlPlaneRuntimeRow
	if err := db.Where("runtime_scope = ?", "prod").First(&runtime).Error; err != nil {
		t.Fatal(err)
	}
	controlRow := adminAuditExportRow{Source: "control", EventID: "control-event", At: stamp, PrincipalSubject: "operator", PrincipalKind: "mtls", CredentialID: "credential-1", Domain: "team/prod", Object: "admin:config", Action: "write", OperationID: "replaceConfig", RequestID: "request-1", Outcome: "committed", ConfigRevision: 4, RuntimeScope: "prod", Issuer: "issuer", ConfigSetID: runtime.ActiveSetID, ControlAction: "replace"}
	controlHash, err := adminAuditChainHash("", controlRow)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO router_config_change_events (event_id, runtime_scope, config_set_id, action, at, actor, actor_kind, actor_credential_id, actor_issuer, authz_domain, object, authz_action, operation_id, request_id, outcome, reason, config_revision, previous_hash, row_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "control-event", "prod", runtime.ActiveSetID, "replace", stamp, "operator", "mtls", "credential-1", "issuer", "team/prod", "admin:config", "write", "replaceConfig", "request-1", "committed", "", 4, "", controlHash).Error; err != nil {
		t.Fatal(err)
	}
	if err := updateAdminAuditChainHead(db, "control", "team/prod", "", controlHash); err != nil {
		t.Fatal(err)
	}
	handler := adminAuditExportHandlers(nil, db)["admin.audit.export"]
	r := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"}))
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != 200 {
		t.Fatalf("export=%d %s", w.Code, w.Body.String())
	}
	var result adminAuditExportDocument
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Source != "control" || result.Items[0].PrincipalSubject != "operator" || result.Items[0].Issuer != "issuer" || result.Items[0].ConfigSetID != runtime.ActiveSetID || result.Items[0].ControlAction != "replace" {
		t.Fatalf("control event missing: %+v", result)
	}
}

func TestAdminAuditExportIncludesCredentialEventsAndFields(t *testing.T) {
	db := configAdminTestDB(t)
	event := AdminCredentialEvent{EventID: "credential-audit", At: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), Kind: "client", ClientID: "client-7", Actor: "operator", CredentialID: "credential-7", Subject: "operator", Issuer: "issuer-1", Outcome: "failure", ReasonCode: "invalid-token", RequestID: "request-7", ClientIPHash: "0123456789abcdef", Lifecycle: "auth"}
	if err := db.Transaction(func(tx *gorm.DB) error { return insertAdminCredentialEventTx(tx, event) }); err != nil {
		t.Fatal(err)
	}
	var stored adminAuditCredentialExportRecord
	if err := db.Where("event_id = ?", event.EventID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	handler := adminAuditExportHandlers(nil, db)["admin.audit.export"]
	r := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: string(AdminDomainCredential)}))
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("export=%d %s", w.Code, w.Body.String())
	}
	var document adminAuditExportDocument
	if err := json.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Items) != 1 {
		t.Fatalf("items=%+v", document.Items)
	}
	row := document.Items[0]
	if row.Source != "credential" || row.EventID != event.EventID || row.Issuer != event.Issuer || row.ClientIPHash != event.ClientIPHash || row.Count != 1 || row.Lifecycle != event.Lifecycle || row.ClientID != event.ClientID || row.Actor != event.Actor {
		t.Fatalf("credential event fields lost: %+v", row)
	}
	if err := VerifyAdminAuditExport(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatalf("verify credential export: %v", err)
	}
}

func TestAdminAuditExportFailsSafelyOnCanceledRequest(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"}))
	cancel()
	r := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	adminAuditExportHandlers(store.db, nil)["admin.audit.export"](w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "context canceled") {
		t.Fatalf("canceled export status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminAuditOperationsUseSeparateReadAndDeletePrivileges(t *testing.T) {
	registry, err := adminV1RegistryWithPolicy()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, object, action string
	}{{"exportAdminAudit", "admin:audit", "read"}, {"deleteAdminAuditEvent", "admin:audit", "delete"}} {
		op, ok := registry.Lookup(tc.id)
		if !ok || op.Object != tc.object || op.Action != tc.action || op.Domain != AdminDomainCredential {
			t.Fatalf("operation %s = %+v, found=%v", tc.id, op, ok)
		}
	}
}

func TestAdminAuditOperationsMountEnforcesReadAndDeletePrivileges(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := insertAdminAuditUsageTx(store.db, AdminAuditRecord{EventID: "mount-target", At: time.Now().UTC(), Domain: "team/prod", Outcome: "committed"}); err != nil {
		t.Fatal(err)
	}
	principal := Principal{Subject: "operator", Kind: "mtls", CredentialID: "credential-1", Issuer: "test"}
	policy, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, operator, team/prod, admin:audit, read"}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewAdminV1Registry(adminAuditExportOperationSpecs())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAdminV1(mux, AdminV1Deps{
		Registry: registry, Authenticate: &fakeAuthenticator{principal: principal},
		ResolveDomain: func(Principal) string { return "team/prod" },
		Authorize: func(_ context.Context, p Principal, domain, object, action string) error {
			allowed, err := policy.enforcer.Enforce(p.Subject, domain, object, action)
			if err != nil {
				return ErrAuthorizationUnavailable
			}
			if !allowed {
				return ErrAdminForbidden
			}
			return nil
		},
		CursorCodec: NewAdminCursorCodec([]byte("admin-audit-mounted-test-key-0001")),
		Handlers:    adminAuditExportHandlers(store.db, nil),
		Denials:     &recordingDenialSink{}, CredentialEvents: &recordingCredentialSink{},
	}); err != nil {
		t.Fatal(err)
	}
	export := httptest.NewRecorder()
	mux.ServeHTTP(export, httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil))
	if export.Code != http.StatusOK {
		t.Fatalf("export=%d %s", export.Code, export.Body.String())
	}
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, httptest.NewRequest("DELETE", "/admin/v1/audit-events/mount-target", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("delete without dedicated privilege=%d %s", denied.Code, denied.Body.String())
	}
	var remaining int64
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", "mount-target").Count(&remaining).Error; err != nil || remaining != 1 {
		t.Fatalf("denied delete changed row count=%d err=%v", remaining, err)
	}
	if _, err := policy.enforcer.AddPolicy("operator", "team/prod", "admin:audit", "delete"); err != nil {
		t.Fatal(err)
	}
	allowed := httptest.NewRecorder()
	mux.ServeHTTP(allowed, httptest.NewRequest("DELETE", "/admin/v1/audit-events/mount-target", nil))
	if allowed.Code != http.StatusOK {
		t.Fatalf("delete with dedicated privilege=%d %s", allowed.Code, allowed.Body.String())
	}
}

func TestAdminAuditRetentionUsesInjectedClock(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	control := configAdminTestDB(t)
	if err := store.db.Exec(usageAdminAuditDDL[0]).Error; err != nil {
		t.Fatal(err)
	}
	fakeNow := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for _, row := range []AdminAuditRecord{
		{EventID: "expired", At: fakeNow.Add(-31 * 24 * time.Hour), Domain: "team/prod", Outcome: "committed"},
		{EventID: "fresh", At: fakeNow.Add(-29 * 24 * time.Hour), Domain: "team/prod", Outcome: "committed"},
	} {
		if err := insertAdminAuditUsageTx(store.db, row); err != nil {
			t.Fatal(err)
		}
	}
	var runtime configControlPlaneRuntimeRow
	if err := control.Where("runtime_scope = ?", "prod").First(&runtime).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id string
		at time.Time
	}{{"control-expired", fakeNow.Add(-31 * 24 * time.Hour)}, {"control-fresh", fakeNow.Add(-29 * 24 * time.Hour)}} {
		if err := control.Exec(`INSERT INTO router_config_change_events (event_id, runtime_scope, config_set_id, action, at, actor, actor_kind, actor_credential_id, actor_issuer, authz_domain, object, authz_action, operation_id, request_id, outcome, reason, config_revision) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.id, "prod", runtime.ActiveSetID, "replace", row.at.UTC().Format(time.RFC3339Nano), "operator", "mtls", "credential-1", "issuer", "team/prod", "admin:config", "write", "replaceConfig", "request-1", "committed", "", 4).Error; err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	done := startAdminAuditJanitor(store.db, time.Hour, 30, stop, func() time.Time { return fakeNow })
	controlStop := make(chan struct{})
	controlDone := startAdminAuditJanitor(control, time.Hour, 30, controlStop, func() time.Time { return fakeNow })
	close(stop)
	close(controlStop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retention janitor did not stop")
	}
	select {
	case <-controlDone:
	case <-time.After(5 * time.Second):
		t.Fatal("control-plane retention janitor did not stop")
	}
	var expired, fresh int64
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", "expired").Count(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", "fresh").Count(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if expired != 0 || fresh != 1 {
		t.Fatalf("checkpoint=%d fresh=%d", expired, fresh)
	}
	usageExport := adminAuditExportHandlers(store.db, nil)["admin.audit.export"]
	usageRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil)
	usageResponse := httptest.NewRecorder()
	usageRequest = usageRequest.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: "team/prod"}))
	usageExport(usageResponse, usageRequest)
	if usageResponse.Code != http.StatusOK {
		t.Fatalf("usage export after retention=%d %s", usageResponse.Code, usageResponse.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(usageResponse.Body.String())); err != nil {
		t.Fatalf("verify usage chain after retention: %v", err)
	}
	var controlExpired, controlFresh int64
	if err := control.Table("router_config_change_events").Where("event_id = ?", "control-expired").Count(&controlExpired).Error; err != nil {
		t.Fatal(err)
	}
	if err := control.Table("router_config_change_events").Where("event_id = ?", "control-fresh").Count(&controlFresh).Error; err != nil {
		t.Fatal(err)
	}
	if controlExpired != 0 || controlFresh != 1 {
		t.Fatalf("control-plane checkpoint=%d fresh=%d", controlExpired, controlFresh)
	}
}
