// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPolicyAuthoritativeSyncPublishesAndFailuresKeepClock(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, set := range []struct{ id, subject string }{{"first", "operator"}, {"second", "other"}} {
		if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: set.id, Name: set.id, Version: 1, Policy: []string{"p, " + set.subject + ", *, admin:config, read"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ActivateAuthzPolicySet("first", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.db.AutoMigrate(&policySyncRevisionRow{}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&policySyncRevisionRow{ID: 1, Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
	s := freshnessGateService(t, "db", "sqlite")
	s.usage = store
	s.quota = &quotaStore{callers: map[string]*callerRuntime{}}
	if err := s.syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	desired, applied, at, ok := s.PolicyFreshness()
	if desired != 1 || applied != 1 || !ok || at.IsZero() {
		t.Fatalf("sync state=%d %d %v %v", desired, applied, at, ok)
	}
	p := Principal{Subject: "operator", Kind: "mtls"}
	ctx := WithPrincipal(context.Background(), p)
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "read"); err != nil {
		t.Fatal(err)
	}
	pinned := s.PinAuthorization(ctx)
	if err := store.ActivateAuthzPolicySet("second", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&policySyncRevisionRow{}).Where("id = ?", 1).Update("revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatalf("revocation after publication=%v", err)
	}
	if err := s.Authorize(pinned, p, AdminGlobalDomain, "admin:config", "read"); err != nil {
		t.Fatalf("in-flight pin lost coherence=%v", err)
	}
	_, _, beforeFailure, _ := s.PolicyFreshness()
	previous := s.currentAuthorizer()
	if err := store.db.Model(&policySyncRevisionRow{}).Where("id = ?", 1).Update("revision", 3).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&authzPolicyRuleRecord{}).Where("policy_set_id = ?", "second").Update("ptype", "unsupported").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.syncPolicyAuthorization(context.Background()); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("failed build=%v", err)
	}
	d, a, at, _ := s.PolicyFreshness()
	if d != 3 || a != 2 || !at.Equal(beforeFailure) || s.currentAuthorizer() != previous {
		t.Fatal("failed build acknowledged publication or refreshed clock")
	}
	if err := store.db.Model(&authzPolicyRuleRecord{}).Where("policy_set_id = ?", "second").Update("ptype", "p").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&policySyncRevisionRow{}).Where("id = ?", 1).Update("revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.syncPolicyAuthorization(context.Background()); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("revision rollback=%v", err)
	}
	if err := store.db.Model(&policySyncRevisionRow{}).Where("id = ?", 1).Update("revision", 3).Error; err != nil {
		t.Fatal(err)
	}
	stop, err := s.startPolicySynchronization(context.Background(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, _, started, _ := s.PolicyFreshness()
	deadline := time.NewTimer(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	refreshed := false
	for !refreshed {
		select {
		case <-deadline.C:
			stop()
			t.Fatal("unchanged authoritative poll did not refresh successful-sync clock")
		case <-tick.C:
			_, _, now, _ := s.PolicyFreshness()
			refreshed = now.After(started)
		}
	}
	stop()
	_, _, before, _ := s.PolicyFreshness()
	snapshot := s.currentAuthorizer()
	if err := store.db.Migrator().DropTable(&policySyncRevisionRow{}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPolicyAuthorization(context.Background()); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("failed authoritative read=%v", err)
	}
	_, _, after, _ := s.PolicyFreshness()
	if !after.Equal(before) || s.currentAuthorizer() != snapshot {
		t.Fatal("failed sync replaced snapshot or refreshed successful-sync time")
	}
}
