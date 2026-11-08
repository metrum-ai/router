// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"crypto/rand"
	"encoding/hex"
	"gorm.io/gorm"
	"math"
	"time"
)

// Called only under the singleton runtime write lock. Historical metadata and
// record IDs remain untouched; the complete cloned content is guarded later.
func clonePolicyHistory(tx *gorm.DB, source policyResourceView, actor Principal) (policyResourceView, error) {
	if source.Set.Status != authzPolicyStatusRetired {
		return policyResourceView{}, errPolicySetImmutable
	}
	var latest int
	if err := tx.Model(&authzPolicySetRecord{}).Where("name = ?", source.Set.Name).Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil || latest < 0 || latest == math.MaxInt {
		return policyResourceView{}, ErrAuthorizationUnavailable
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return policyResourceView{}, ErrAuthorizationUnavailable
	}
	source.Set = authzPolicySetRecord{ID: hex.EncodeToString(id[:]), Name: source.Set.Name, Version: latest + 1, Status: authzPolicyStatusDraft, CreatedBy: safeAuthzAuditField(actor.Subject), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for i := range source.Rules {
		source.Rules[i].ID = 0
		source.Rules[i].PolicySetID = source.Set.ID
	}
	for i := range source.Memberships {
		source.Memberships[i].ID = 0
		source.Memberships[i].PolicySetID = source.Set.ID
	}
	return source, nil
}
