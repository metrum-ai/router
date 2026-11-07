// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// ErrPolicyAuditUnavailable reports a guarded policy mutation that arrived
// without the authenticated audit context #869 requires. The change is refused
// before any policy work: a policy write never commits without its audit row.
var ErrPolicyAuditUnavailable = errors.New("admin policy mutation requires an authenticated audit context")

// policyMutationAuditWriter binds one guarded policy mutation to the usage-side
// audit row written in the SAME transaction (#869). The actor comes from the
// authenticated principal recorded by the /admin/v1 mount, never from a request
// header, and the row carries the policy revision and immutable hashes the
// mutation produced.
func policyMutationAuditWriter(ctx context.Context) (func(*gorm.DB, int64, policyResourceResult) error, error) {
	if ctx == nil {
		return nil, ErrPolicyAuditUnavailable
	}
	meta, ok := AdminAuditMetaFrom(ctx)
	if !ok || adminAuditFieldValue(meta.Subject) == "" {
		return nil, ErrPolicyAuditUnavailable
	}
	return func(tx *gorm.DB, revision int64, result policyResourceResult) error {
		record := auditRecordFromMeta(meta, "")
		record.ConfigRevision = revision
		record.ConfigSetID = adminAuditFieldValue(result.SetID)
		record.AfterHash = adminAuditFieldValue(result.AfterHash)
		return insertAdminAuditUsageTx(tx, record)
	}, nil
}
