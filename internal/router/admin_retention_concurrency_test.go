// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestConcurrentAdminRetention(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			var db *gorm.DB
			var cfg UsageDBConfig
			if driver == "postgres" {
				pg, cleanup, schema := configAdminPostgresTestDB(t)
				t.Cleanup(cleanup)
				db = pg
				cfg = UsageDBConfig{Driver: driver, DSN: pgDSNWithSearchPath(os.Getenv("ROUTER_TEST_PG_DSN"), schema)}
			} else {
				db = configAdminTestDB(t)
				cfg.Driver = driver
				if err := db.Raw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&cfg.Path).Error; err != nil {
					t.Fatal(err)
				}
			}
			other, err := openUsageDB(cfg)
			if err != nil {
				t.Fatal(err)
			}
			pool, err := other.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := range 5 {
				e := AdminCredentialEvent{EventID: fmt.Sprintf("retention-%d", i), At: start.Add(time.Duration(i) * time.Hour), Kind: "adminkey", Subject: "operator", Outcome: "success"}
				if err := db.Transaction(func(tx *gorm.DB) error { return insertAdminCredentialEventTx(tx, e) }); err != nil {
					t.Fatal(err)
				}
			}
			var want string
			if err := db.Table(adminCredentialEventsTable).Select("row_hash").Where("event_id=?", "retention-3").Row().Scan(&want); err != nil {
				t.Fatal(err)
			}
			// Reject an unlocked prefix read before it can influence any deletion.
			// Each pool observes its own transactions on both database drivers.
			for _, conn := range []*gorm.DB{db, other} {
				var locked sync.Map
				if err := conn.Callback().Raw().After("gorm:raw").Register("test:remember-chain-lock", func(tx *gorm.DB) {
					sql := tx.Statement.SQL.String()
					if tx.Error == nil && (strings.Contains(sql, "pg_advisory_xact_lock") || strings.HasPrefix(sql, "UPDATE "+adminAuditChainAnchorsTable+" SET head_hash=head_hash WHERE 0")) {
						locked.Store(tx.Statement.ConnPool, true)
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := conn.Callback().Row().Before("gorm:row").Register("test:require-chain-lock", func(tx *gorm.DB) {
					if tx.Statement.Table == adminCredentialEventsTable && strings.Contains(strings.Join(tx.Statement.Selects, ","), " AS id") {
						if _, ok := locked.Load(tx.Statement.ConnPool); !ok {
							tx.AddError(fmt.Errorf("retention selected its prefix before locking the chain"))
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			type result struct {
				count int64
				err   error
			}
			results := make(chan result, 2)
			ready := make(chan struct{})
			for i, conn := range []*gorm.DB{db, other} {
				go func() {
					<-ready
					n, err := PurgeAdminAuditEventsBefore(conn, start.Add(time.Duration(2*i+1)*time.Hour+time.Minute))
					results <- result{n, err}
				}()
			}
			close(ready)
			var total int64
			for range 2 {
				r := <-results
				if r.err != nil {
					t.Error(r.err)
				}
				total += r.count
			}
			if total != 4 {
				t.Fatalf("deleted %d rows, want 4", total)
			}
			var got string
			if err := db.Table(adminAuditChainAnchorsTable).Select("start_hash").Where("source=? AND domain=?", "credential", string(AdminDomainCredential)).Row().Scan(&got); err != nil || got != want {
				t.Fatalf("checkpoint=%q want=%q err=%v", got, want, err)
			}
			req := httptest.NewRequest("GET", "/admin/v1/audit-events/export", nil).WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: string(AdminDomainCredential)}))
			resp := httptest.NewRecorder()
			adminAuditExportHandlers(nil, db)["admin.audit.export"](resp, req)
			if resp.Code != 200 {
				t.Fatalf("export status %d", resp.Code)
			}
			if err := VerifyAdminAuditExport(resp.Body); err != nil {
				t.Fatal(err)
			}
		})
	}
}
