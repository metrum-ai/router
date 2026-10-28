// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
)

// Gated on ROUTER_TEST_PG_DSN via configAdminPostgresTestDB. This exercises the
// real Admin group-write path rather than calling ActivateConfigSet directly.
func TestConfigAdminGroupWriteNotifiesPostgres(t *testing.T) {
	db, cleanup, _ := configAdminPostgresTestDB(t)
	defer cleanup()
	setID, err := ImportCanonicalYAML(db, "prod", "seed", []byte(configAdminTestYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(db, "prod", setID, "test"); err != nil {
		t.Fatal(err)
	}
	dialector, ok := db.Dialector.(*postgres.Dialector)
	if !ok || dialector.Config == nil {
		t.Fatal("postgres dialector required")
	}
	cfg, err := pgconn.ParseConfig(dialector.Config.DSN)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 1)
	cfg.OnNotification = func(_ *pgconn.PgConn, n *pgconn.Notification) {
		if n.Channel == configControlPlaneNotifyChannel {
			select {
			case received <- n.Payload:
			default:
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+configControlPlaneNotifyChannel).ReadAll(); err != nil {
		t.Fatal(err)
	}
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")
	body := map[string]any{"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}}, "reason": "test"}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("group write status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := conn.WaitForNotification(ctx); err != nil {
		t.Fatalf("committed group write did not notify: %v", err)
	}
	select {
	case scope := <-received:
		if scope != "prod" {
			t.Fatalf("wrong notification scope %q", scope)
		}
	default:
		t.Fatal("no notification callback")
	}
}
