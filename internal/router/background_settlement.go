// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This immutable snapshot is routing/billing metadata only. It contains no
// upstream credentials, prompt, response content, or headers.
type backgroundBillingSnapshot struct {
	CallerUser        string   `json:"caller_user"`
	CallerEnvironment string   `json:"caller_environment"`
	TokenID           string   `json:"token_id"`
	Provider          string   `json:"provider"`
	Model             string   `json:"model"`
	Region            string   `json:"region"`
	InputPrice        float64  `json:"input_price"`
	OutputPrice       float64  `json:"output_price"`
	CachedInputPrice  *float64 `json:"cached_input_price,omitempty"`
	CacheWritePrice   *float64 `json:"cache_write_price,omitempty"`
	ImageTokenPrice   float64  `json:"image_token_price"`
	ImagePrice        float64  `json:"image_price"`
	InputHasImage     bool     `json:"input_has_image"`
	InputImageCount   int      `json:"input_image_count"`
	PricingSource     string   `json:"pricing_source"`
	PricingUpdatedAt  string   `json:"pricing_updated_at"`
}

func makeBackgroundBillingSnapshot(rc *requestContext, target Target) (string, error) {
	if rc == nil || rc.caller == nil {
		return "", errors.New("missing background billing caller")
	}
	snapshot := backgroundBillingSnapshot{CallerUser: callerUser(rc.caller.cfg), CallerEnvironment: callerEnvironment(rc.caller.cfg), TokenID: rc.rec.TokenID, Provider: target.Provider, Model: target.Model, Region: target.Region, InputPrice: target.InputPricePerMillionUSD, OutputPrice: target.OutputPricePerMillionUSD, CachedInputPrice: target.CachedInputPricePerMillionUSD, CacheWritePrice: target.CacheWritePricePerMillionUSD, ImageTokenPrice: target.ImageInputPricePerMillionTokensUSD, ImagePrice: target.ImageInputPricePerImageUSD, InputHasImage: rc.rec.InputHasImage, InputImageCount: rc.rec.InputImageCount, PricingSource: target.PricingSource, PricingUpdatedAt: target.PricingUpdatedAt}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func terminalBackgroundStatus(value string) (string, bool) {
	switch value {
	case "completed", "failed", "incomplete":
		return "completed", true
	case "cancelled", "canceled":
		return "cancelled", true
	default:
		return "", false
	}
}

// A missing, null or malformed usage object is unknown, not zero. This lets a
// later owner GET reconcile usage without silently erasing an upstream charge.
func reportedBackgroundUsage(response map[string]any) (Usage, bool) {
	object, ok := response["usage"].(map[string]any)
	if !ok {
		return Usage{}, false
	}
	for _, field := range []string{"input_tokens", "output_tokens"} {
		value, ok := object[field].(float64)
		if !ok || value < 0 || value > 1_000_000_000 || math.Trunc(value) != value {
			return Usage{}, false
		}
	}
	if raw, exists := object["total_tokens"]; exists {
		value, ok := raw.(float64)
		if !ok || value < 0 || value > 2_000_000_000 || math.Trunc(value) != value {
			return Usage{}, false
		}
	}
	usage := usageFromMap(object)
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if usage.TotalTokens < usage.InputTokens+usage.OutputTokens {
		return Usage{}, false
	}
	return usage, true
}

// settleBackgroundResponse atomically records one response's billed usage and
// updates durable quota aggregates. A repeated or concurrent observation is a
// no-op. The caller must have validated that the upstream explicitly supplied
// usage for a terminal response; missing usage is not silently treated as zero.
func settleBackgroundResponse(ctx context.Context, db *gorm.DB, responseID, owner, status string, usage Usage, now time.Time) (bool, error) {
	if db == nil || responseID == "" || owner == "" || (status != "completed" && status != "cancelled") || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.InputTokens > 1_000_000_000 || usage.OutputTokens > 1_000_000_000 || usage.TotalTokens > 2_000_000_000 {
		return false, errors.New("invalid background settlement")
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if usage.TotalTokens < usage.InputTokens+usage.OutputTokens {
		return false, errors.New("invalid background settlement usage")
	}
	now = now.UTC()
	if now.IsZero() {
		return false, errors.New("invalid background settlement time")
	}
	settled := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row responseAffinityRecord
		if err := tx.Where("response_id = ? AND caller_id = ? AND background = ? AND status <> ? AND expires_at > ?", responseID, owner, true, "deleted", now.UnixMilli()).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if row.UsageSettled {
			return nil
		}
		var snapshot backgroundBillingSnapshot
		if row.BillingSnapshot == "" || json.Unmarshal([]byte(row.BillingSnapshot), &snapshot) != nil || snapshot.Provider == "" || snapshot.Model == "" {
			return errors.New("background billing snapshot unavailable")
		}
		result := tx.Model(&responseAffinityRecord{}).Where("response_id = ? AND caller_id = ? AND background = ? AND usage_settled = ? AND status <> ? AND expires_at > ?", responseID, owner, true, false, "deleted", now.UnixMilli()).Updates(map[string]any{"usage_settled": true, "status": status})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		// The upstream may reuse an ID after an earlier affinity has expired
		// and been purged. Include this creation's request ID so historical
		// audit uniqueness does not prevent settlement of the new response.
		idHash := sha256.Sum256([]byte(responseID + "\x00" + row.RequestID))
		rec := logRecord{TS: formatUsageTime(now), RequestID: "background-" + hex.EncodeToString(idHash[:]), CallerID: row.CallerID, CallerUser: snapshot.CallerUser, CallerProject: row.Project, CallerEnvironment: snapshot.CallerEnvironment, TokenID: snapshot.TokenID, Endpoint: "responses.background_settlement", UsageSource: "reported", InboundDialect: "openai-responses", RequestedModel: row.ModelGroup, ResolvedGroup: row.ModelGroup, TargetProvider: snapshot.Provider, TargetModel: snapshot.Model, TargetRegion: snapshot.Region, TargetDialect: "openai-responses", Cache: "bypass", Status: 200, Usage: usage, InputPricePerMillionUSD: snapshot.InputPrice, OutputPricePerMillionUSD: snapshot.OutputPrice, CachedInputPricePerMillionUSD: snapshot.CachedInputPrice, CacheWritePricePerMillionUSD: snapshot.CacheWritePrice, ImageInputPricePerMillionTokensUSD: snapshot.ImageTokenPrice, ImageInputPricePerImageUSD: snapshot.ImagePrice, InputHasImage: snapshot.InputHasImage, InputImageCount: snapshot.InputImageCount, PricingSource: snapshot.PricingSource, PricingUpdatedAt: snapshot.PricingUpdatedAt}
		populateReasoningUsageCoverage(&rec)
		populateCosts(&rec)
		if err := tx.Create(recordFromRow(rowFromRecord(rec))).Error; err != nil {
			return err
		}
		day := now.Format("2006-01-02")
		daily := backgroundQuotaDaily{CallerID: owner, Day: day, Tokens: int64(usage.TotalTokens)}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "caller_id"}, {Name: "day"}}, DoUpdates: clause.Assignments(map[string]any{"tokens": gorm.Expr("response_background_quota_daily.tokens + ?", usage.TotalTokens)})}).Create(&daily).Error; err != nil {
			return err
		}
		second := backgroundQuotaSecond{CallerID: owner, Second: now.Unix(), Tokens: int64(usage.TotalTokens)}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "caller_id"}, {Name: "second"}}, DoUpdates: clause.Assignments(map[string]any{"tokens": gorm.Expr("response_background_quota_second.tokens + ?", usage.TotalTokens)})}).Create(&second).Error; err != nil {
			return err
		}
		if err := tx.Where("second < ?", now.Add(-10*time.Minute).Unix()).Delete(&backgroundQuotaSecond{}).Error; err != nil {
			return err
		}
		settled = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("background settlement transaction: %w", err)
	}
	return settled, nil
}
