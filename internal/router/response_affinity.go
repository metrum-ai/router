// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// responseAffinityRecord stores routing and ownership metadata only, never
// request/response content or credentials. Times are Unix milliseconds so
// comparisons are identical on SQLite and PostgreSQL.
type responseAffinityRecord struct {
	ResponseID         string `gorm:"column:response_id;primaryKey;type:text"`
	RequestID          string `gorm:"column:request_id;type:text;not null;default:''"`
	UpstreamResponseID string `gorm:"column:upstream_response_id;type:text;not null"`
	TargetKey          string `gorm:"column:target_key;type:text;not null"`
	ModelGroup         string `gorm:"column:model_group;type:text;not null"`
	CallerID           string `gorm:"column:caller_id;type:text;not null;index:idx_response_affinity_owner"`
	Project            string `gorm:"column:project;type:text;not null;default:''"`
	Tenant             string `gorm:"column:tenant;type:text;not null;default:''"`
	Background         bool   `gorm:"column:background;not null;default:false"`
	Status             string `gorm:"column:status;type:text;not null"`
	UsageSettled       bool   `gorm:"column:usage_settled;not null;default:false"`
	BillingSnapshot    string `gorm:"column:billing_snapshot;type:text;not null;default:''"`
	CreatedAt          int64  `gorm:"column:created_at;not null"`
	CreatedTS          string `gorm:"column:created_ts;type:text;not null;default:''"`
	CreatedEndTS       string `gorm:"column:created_end_ts;type:text;not null;default:''"`
	ExpiresAt          int64  `gorm:"column:expires_at;not null;index:idx_response_affinity_expiry"`
}

func (responseAffinityRecord) TableName() string { return "response_affinity" }

var errResponseAffinityCollision = errors.New("response affinity collision")

func responseStoreRetention(cfg ResponsesStoreConfig) (time.Duration, error) {
	raw := strings.ToLower(strings.TrimSpace(cfg.Retention))
	if raw == "" {
		raw = "30d"
	}
	var value time.Duration
	var err error
	if strings.HasSuffix(raw, "d") {
		days, parseErr := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if parseErr != nil || days <= 0 || days > 3660 {
			return 0, errors.New("server responses_store.retention must be positive and at most 3660 days")
		}
		value = time.Duration(days) * 24 * time.Hour
	} else {
		value, err = time.ParseDuration(raw)
	}
	if err != nil || value < time.Millisecond || value > 3660*24*time.Hour {
		return 0, errors.New("server responses_store.retention must be at least 1ms and at most 3660 days")
	}
	return value, nil
}

// responseAffinityTargetKey hashes exact provider/account/model coordinates.
// Missing key IDs fail closed: credential rotation cannot safely identify an
// account without an explicit operator-managed identity.
func responseAffinityTargetKey(providerName string, provider ProviderConfig, target Target) (string, error) {
	if providerName == "" || strings.TrimSpace(provider.KeyID) == "" || target.Model == "" {
		return "", errors.New("response affinity target identity unavailable")
	}
	u, err := url.Parse(provider.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", errors.New("response affinity target identity unavailable")
	}
	encoded, err := json.Marshal([]string{providerName, provider.KeyID, u.String(), target.Model})
	if err != nil {
		return "", errors.New("response affinity target identity unavailable")
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func applyResponseAffinityMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("response affinity migration requires database")
	}
	if !db.Migrator().HasTable(&responseAffinityRecord{}) {
		if err := db.Migrator().CreateTable(&responseAffinityRecord{}); err != nil {
			return err
		}
		// The later retention migration owns request_id; freeze this original
		// migration at its immutable version-12 schema on fresh installs.
		if err := db.Exec("ALTER TABLE response_affinity DROP COLUMN request_id").Error; err != nil {
			return err
		}
		if err := db.Exec("ALTER TABLE response_affinity DROP COLUMN created_ts").Error; err != nil {
			return err
		}
		if err := db.Exec("ALTER TABLE response_affinity DROP COLUMN created_end_ts").Error; err != nil {
			return err
		}
		if err := db.Exec("ALTER TABLE response_affinity DROP COLUMN billing_snapshot").Error; err != nil {
			return err
		}
	}
	for _, index := range []string{"idx_response_affinity_owner", "idx_response_affinity_expiry"} {
		if !db.Migrator().HasIndex(&responseAffinityRecord{}, index) {
			if err := db.Migrator().CreateIndex(&responseAffinityRecord{}, index); err != nil {
				return err
			}
		}
	}
	return verifyResponseAffinityMigration(db)
}

func verifyResponseAffinityMigration(db *gorm.DB) error {
	// The migration runner verifies the endpoint dependency separately.
	// Rechecking the entire usage schema here magnifies startup costs.
	return verifyUsageRelationalModelExcept(db, &responseAffinityRecord{}, map[string]map[string]struct{}{"response_affinity": {"request_id": {}, "created_ts": {}, "created_end_ts": {}, "billing_snapshot": {}}})
}

func validResponseAffinityStatus(status string) bool {
	switch status {
	case "in_progress", "completed", "cancelled", "deleted":
		return true
	}
	return false
}

func insertResponseAffinity(db *gorm.DB, row responseAffinityRecord) error {
	if db == nil || row.ResponseID == "" || len(row.ResponseID) > 2048 || row.UpstreamResponseID == "" || len(row.UpstreamResponseID) > 2048 || row.TargetKey == "" || row.ModelGroup == "" || row.CallerID == "" || !validResponseAffinityStatus(row.Status) || row.Status == "deleted" || row.CreatedAt <= 0 || row.ExpiresAt <= row.CreatedAt {
		return errors.New("invalid response affinity metadata")
	}
	row.CreatedTS = formatUsageTime(time.UnixMilli(row.CreatedAt))
	row.CreatedEndTS = formatUsageTime(time.UnixMilli(row.CreatedAt).Add(time.Millisecond))
	// A reused upstream ID may now refer to another caller's content. Revoke
	// the original mapping atomically rather than leave it readable.
	result := db.Clauses(
		clause.OnConflict{Columns: []clause.Column{{Name: "response_id"}}, DoUpdates: clause.Assignments(map[string]any{"status": "deleted"})},
		clause.Returning{Columns: []clause.Column{{Name: "status"}}},
	).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("response affinity insert did not return a row")
	}
	if row.Status == "deleted" {
		return errResponseAffinityCollision
	}
	return nil
}

// lookupOwnedResponseAffinity deliberately uses one query for nonexistent,
// expired, deleted, and other-owner IDs; all return the same nil result.
func lookupOwnedResponseAffinity(db *gorm.DB, responseID, callerID string, now time.Time) (*responseAffinityRecord, error) {
	if db == nil {
		return nil, errors.New("response affinity database unavailable")
	}
	if responseID == "" || callerID == "" {
		return nil, nil
	}
	var row responseAffinityRecord
	err := db.Where("response_id = ? AND caller_id = ? AND expires_at > ? AND status IN ?", responseID, callerID, now.UnixMilli(), []string{"in_progress", "completed", "cancelled"}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func updateOwnedResponseAffinityStatus(db *gorm.DB, responseID, callerID, status string, now time.Time) (bool, error) {
	if db == nil || !validResponseAffinityStatus(status) {
		return false, errors.New("invalid response affinity update")
	}
	result := db.Model(&responseAffinityRecord{}).Where("response_id = ? AND caller_id = ? AND expires_at > ? AND status IN ?", responseID, callerID, now.UnixMilli(), []string{"in_progress", "completed", "cancelled"}).Update("status", status)
	return result.RowsAffected == 1, result.Error
}
