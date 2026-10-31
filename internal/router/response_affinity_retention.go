// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"time"

	"gorm.io/gorm"
)

// An affinity record is metadata in the usage-detail retention class. A
// request-specific hold of any class also protects its affinity record, as it
// protects the parent request_usage row. Missing correlation data is treated
// conservatively: an unknown timestamp is never deleted and an unknown
// request ID matches any active hold in the time range.
const responseAffinityUnheld = `response_affinity.created_ts <> '' AND response_affinity.created_end_ts <> '' AND NOT EXISTS (
 SELECT 1 FROM legal_holds h WHERE h.active = ?
 AND (h.data_class = ? OR h.request_id <> '' OR response_affinity.request_id = '')
 AND (h.request_id = '' OR h.request_id = response_affinity.request_id OR response_affinity.request_id = '')
 AND (h.start_ts = '' OR response_affinity.created_end_ts > h.start_ts)
 AND (h.end_ts = '' OR response_affinity.created_ts < h.end_ts)
)`

func runResponseAffinityRetention(tx *gorm.DB, class RetentionClassConfig, cutoff time.Time, dryRun bool) (RetentionTableStatus, error) {
	now := cutoff.Add(time.Duration(class.RetentionDays) * 24 * time.Hour)
	// Expired IDs are eligible even when the usage-detail retention interval is
	// longer. A shorter usage-detail policy can delete them earlier.
	candidate := "(response_affinity.expires_at <= ? OR response_affinity.created_at < ?)"
	query := func() *gorm.DB {
		return tx.Model(&responseAffinityRecord{}).Where(candidate, now.UnixMilli(), cutoff.UnixMilli())
	}
	var candidates, eligible int64
	if err := query().Count(&candidates).Error; err != nil {
		return RetentionTableStatus{}, err
	}
	if err := query().Where(responseAffinityUnheld, true, retentionDataClassUsageDetail).Count(&eligible).Error; err != nil {
		return RetentionTableStatus{}, err
	}
	result := RetentionTableStatus{DataClass: retentionDataClassUsageDetail, TableName: "response_affinity", Cutoff: cutoff, RetentionDays: class.RetentionDays, BatchSize: class.BatchSize, CandidateRows: candidates, HeldRows: candidates - eligible, EligibleRows: eligible, Status: "dry_run", Message: "dry run only; no rows deleted"}
	if dryRun {
		return result, nil
	}
	var ids []string
	if err := query().Where(responseAffinityUnheld, true, retentionDataClassUsageDetail).Order("response_id").Limit(class.BatchSize).Pluck("response_id", &ids).Error; err != nil {
		return RetentionTableStatus{}, err
	}
	if len(ids) == 0 {
		result.Status = "no_eligible_rows"
		result.Message = "no eligible rows deleted"
		return result, nil
	}
	deleted := tx.Where("response_id IN ?", ids).Where(candidate, now.UnixMilli(), cutoff.UnixMilli()).Where(responseAffinityUnheld, true, retentionDataClassUsageDetail).Delete(&responseAffinityRecord{})
	if deleted.Error != nil {
		return RetentionTableStatus{}, deleted.Error
	}
	result.DeletedRows = deleted.RowsAffected
	result.EligibleRows -= deleted.RowsAffected
	result.Status = "purged"
	result.Message = "deleted one eligible retention batch"
	if deleted.RowsAffected == 0 {
		result.Status = "no_eligible_rows"
		result.Message = "no eligible rows deleted"
	}
	return result, nil
}
