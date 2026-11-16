// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/fergusstrange/embedded-postgres/v2/eptest"
)

// TestAutomaticContainmentPostgresNotifiesBetweenServicesAndPersistsOnRestart
// is run by the dedicated PostgreSQL CI lane. It has separate writer and
// listener service connections to one schema and starts no polling fallback.
func TestAutomaticContainmentPostgresNotifiesBetweenServicesAndPersistsOnRestart(t *testing.T) {
	if os.Getenv("ROUTER_TEST_PG_DSN") == "" {
		pg := eptest.Start(t)
		t.Setenv("ROUTER_TEST_PG_DSN", pg.ConnectionURL())
	}
	writerDB, cleanup, schema := configAdminPostgresTestDB(t)
	defer cleanup()
	base := os.Getenv("ROUTER_TEST_PG_DSN")
	listenerDB, err := gorm.Open(postgres.Open(pgDSNWithSearchPath(base, schema)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	listenerSQL, err := listenerDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer listenerSQL.Close()
	if err := applyUsageAdminAuditMigration(writerDB); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(writerDB); err != nil {
		t.Fatal(err)
	}

	caller := &callerRuntime{cfg: CallerConfig{ID: "replica-caller"}}
	listener := &Service{
		cfg:   &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{Mode: "enforce", UnlimitedThrottleLimit: 100}}},
		usage: &usageStore{db: listenerDB},
		quota: &quotaStore{callers: map[string]*callerRuntime{"replica-caller": caller}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := startAutomaticContainmentNotifier(ctx, listenerDB, listener.syncAutomaticContainment)
	if err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer stop()
	// Allow the dedicated LISTEN connection to subscribe before committing.
	time.Sleep(300 * time.Millisecond)

	writer := &Service{usage: &usageStore{db: writerDB}}
	createdAt := time.Now().UTC()
	row := automaticContainmentActionRecord{
		CallerID: "replica-caller", DetectorID: "test-detector", Action: "throttle", ThrottlePercent: 10,
		CreatedAt: formatUsageTime(createdAt), ExpiresAt: formatUsageTime(createdAt.Add(time.Minute)),
	}
	started := time.Now()
	if created, err := writer.usage.applyAutomaticContainment(row, 5, createdAt); err != nil || !created {
		t.Fatalf("writer persist containment: created=%t err=%v", created, err)
	}
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for caller.effectiveRateLimit(100) == 100 {
		select {
		case <-deadline.C:
			t.Fatalf("NOTIFY did not apply action within 5s (elapsed %v)", time.Since(started))
		case <-ticker.C:
		}
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("NOTIFY took %v, want at most 5s", elapsed)
	}

	// A fresh service runtime reads the persisted action after process restart.
	restartedCaller := &callerRuntime{cfg: CallerConfig{ID: "replica-caller"}}
	restarted := &Service{
		cfg:   &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{Mode: "enforce", UnlimitedThrottleLimit: 100}}},
		usage: &usageStore{db: listenerDB},
		quota: &quotaStore{callers: map[string]*callerRuntime{"replica-caller": restartedCaller}},
	}
	restarted.syncAutomaticContainment()
	if got := restartedCaller.effectiveRateLimit(100); got != 10 {
		t.Fatalf("restarted runtime RPM = %d, want persisted 10", got)
	}
}
