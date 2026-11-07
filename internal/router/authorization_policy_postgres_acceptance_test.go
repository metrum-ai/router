// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
//go:build authzpostgres

package router

import (
	"context"
	"errors"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"os"
	"testing"
	"time"
)

// Run only against a disposable, locally owned database. Missing prerequisites
// fail this explicit acceptance target; it never substitutes SQLite or skips.
func TestPolicyPostgresLockCancellationAndPolling(t *testing.T) {
	dsn := os.Getenv("AUTHZ_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("AUTHZ_TEST_POSTGRES_DSN required for explicit postgres acceptance target")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("open disposable PostgreSQL database failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(12)
	defer pool.Close()
	// Test-only setup of legacy tables; runtime metadata uses the explicit handler.
	if err := db.AutoMigrate(&authzPolicySetRecord{}, &authzPolicyRuleRecord{}, &authzRoleLinkRecord{}, &authzPolicyAuditEventRecord{}); err != nil {
		t.Fatal(err)
	}
	store := &usageStore{db: db}
	for _, set := range []struct{ id, subject string }{{"first", "operator"}, {"second", "other"}} {
		if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: set.id, Name: set.id, Version: 1, Policy: []string{"p, " + set.subject + ", *, admin:config, read"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ActivateAuthzPolicySet("first", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := applyPolicyRuntimeMigration(db); err != nil {
		t.Fatal(err)
	}
	replicas := make([]*Service, 3)
	for i := range replicas {
		replicas[i] = freshnessGateService(t, "db", "postgres")
		replicas[i].usage = &usageStore{db: db}
		replicas[i].quota = &quotaStore{callers: map[string]*callerRuntime{}}
		if err := replicas[i].syncPolicyAuthorization(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	p := Principal{Subject: "operator", Kind: "mtls"}
	ctx := WithPrincipal(context.Background(), p)
	pinned := replicas[0].PinAuthorization(ctx)
	previous := replicas[0].currentAuthorizer()
	_, _, observed, _ := replicas[0].PolicyFreshness()
	stops := []func(){}
	for _, replica := range replicas[1:] {
		stop, err := replica.startPolicySynchronization(context.Background(), 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		stops = append(stops, stop)
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	var row policyMutationRevision
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Model(&authzPolicySetRecord{}).Where("id = ?", "first").Update("status", authzPolicyStatusRetired).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Model(&authzPolicySetRecord{}).Where("id = ?", "second").Update("status", authzPolicyStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Model(&policyMutationRevision{}).Where("id = ?", 1).Updates(map[string]any{"revision": 1, "active_set_id": "second"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, read := range []func(context.Context) error{replicas[0].syncPolicyAuthorization, func(c context.Context) error { _, err := store.getPolicyResourceContext(c, "first"); return err }} {
		deadline, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		start := time.Now()
		err := read(deadline)
		cancel()
		if !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("locked read cancellation=%v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("canceled SQL read did not terminate promptly")
		}
	}
	_, _, afterFailure, _ := replicas[0].PolicyFreshness()
	if !afterFailure.Equal(observed) || replicas[0].currentAuthorizer() != previous {
		t.Fatal("failed locked sync published or acknowledged")
	}
	// Intentionally no notification: real database polling must converge alone.
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := replicas[0].syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		converged := true
		for _, replica := range replicas {
			desired, applied, _, ok := replica.PolicyFreshness()
			if !ok || desired != 1 || applied != 1 {
				converged = false
			}
		}
		if converged {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("three real PostgreSQL replicas did not converge without notification")
		case <-tick.C:
		}
	}
	for _, replica := range replicas {
		if err := replica.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
			t.Fatalf("old grant survived convergence: %v", err)
		}
	}
	if err := replicas[0].Authorize(pinned, p, AdminGlobalDomain, "admin:config", "read"); err != nil {
		t.Fatalf("old in-flight snapshot changed: %v", err)
	}
}
