// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"
)

func TestPolicyMutationNotificationFollowsAudit(t *testing.T) {
	s, deps, p, _ := policyMutationFixture(t)
	notifications := 0
	ack := false
	var auditTx *gorm.DB
	ctx := WithPrincipal(context.Background(), p)
	ctx = context.WithValue(ctx, revisionNotificationObserverKey{}, func(tx *gorm.DB, scope string) {
		if scope != policyNotificationScope || !ack || tx != auditTx {
			t.Error("notification did not follow acknowledgement in same transaction")
		}
		notifications++
	})
	mutation := policyMutation{Operation: "write", ExpectedRevision: 0, Candidate: deps.Current, Apply: func(*gorm.DB) error { return nil }, Audit: func(tx *gorm.DB, rev int64) error { ack = true; auditTx = tx; return nil }}
	if _, err := s.withPolicyMutation(ctx, p, deps, mutation); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("notifications=%d", notifications)
	}
	mutation.ExpectedRevision = 1
	mutation.Audit = func(*gorm.DB, int64) error { return errors.New("audit unavailable") }
	if _, err := s.withPolicyMutation(ctx, p, deps, mutation); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("failed audit=%v", err)
	}
	if notifications != 1 {
		t.Fatal("failed audit queued notification")
	}
}
