// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Safe deactivation-event metadata. Original activation history is preserved;
// its protected credential binding/verifier is never exposed here.
type adminEmergencyDeactivation struct {
	Activation    adminEmergencyActivation `json:"activation"`
	Reason        string                   `json:"reason"`
	DeactivatedAt time.Time                `json:"deactivated_at"`
}

// Deactivate is an OFFLINE control primitive for routerctl, not an HTTP route
// or a credential proof. It closes the currently active history row, including
// expired/removed enrollment, without requiring possession of that credential.
// The caller must supply the actual #816 transaction-bound event adapter. A
// successful return acknowledges BOTH durable state and event commit. Missing
// or already-closed activations return inactive without creating another event.
// A later explicit Activate can create a new activation; this is not permanent
// revocation of the enrollment or a ban on subsequent operator activation.
func (s *adminEmergencyActivationStore) Deactivate(ctx context.Context, id, reason string, record func(*gorm.DB, adminEmergencyDeactivation) error) (adminEmergencyDeactivation, error) {
	var zero adminEmergencyDeactivation
	if s == nil || s.db == nil || s.now == nil || s.redactReason == nil || ctx == nil || ctx.Err() != nil || record == nil || len(id) == 0 || len(id) > 128 || len(reason) > 512 || strings.TrimSpace(reason) == "" {
		return zero, errAdminEmergencyInvalid
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return zero, errAdminEmergencyInvalid
		}
	}
	reason = strings.TrimSpace(s.redactReason(reason))
	if reason == "" || len(reason) > 512 {
		return zero, errAdminEmergencyInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	// Zero is the active-row sentinel. Never publish a "deactivation" that would
	// leave that sentinel intact, even with a misconfigured injected clock.
	if now.UnixMilli() <= 0 {
		return zero, errAdminEmergencyInvalid
	}
	var stopped adminEmergencyDeactivation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row adminEmergencyActivationRow
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("credential_id = ? AND deactivated_at_ms = 0", id).Limit(1).Find(&row)
		if query.Error != nil {
			return errAdminEmergencyPersistence
		}
		if query.RowsAffected == 0 {
			return errAdminEmergencyInactive
		}
		result := tx.Model(&adminEmergencyActivationRow{}).Where("activation_id = ? AND deactivated_at_ms = 0", row.ActivationID).Update("deactivated_at_ms", now.UnixMilli())
		if result.Error != nil || result.RowsAffected != 1 {
			return errAdminEmergencyPersistence
		}
		stopped = adminEmergencyDeactivation{Activation: row.metadata(), Reason: reason, DeactivatedAt: now}
		if err := record(tx, stopped); err != nil {
			return errAdminEmergencyPersistence
		}
		if ctx.Err() != nil {
			return errAdminEmergencyPersistence
		}
		return nil
	})
	if errors.Is(err, errAdminEmergencyInactive) {
		return zero, errAdminEmergencyInactive
	}
	if err != nil {
		return zero, errAdminEmergencyPersistence
	}
	return stopped, nil
}
