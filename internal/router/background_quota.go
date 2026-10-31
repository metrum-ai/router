// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"
)

type backgroundQuotaTotals struct{ Day, Month, Lifetime, Minute int64 }

func backgroundQuotaError() admission {
	return admission{Status: http.StatusServiceUnavailable, Reason: "quota-state-error", QuotaState: "error", KeyState: "error"}
}

// effectiveQuotaState combines the existing durable file counters with the
// database-backed background ledger. It never mutates the file counters, so
// every replica can observe the same aggregate without double-applying it.
// Call only while holding q.mu.
func (q *quotaStore) effectiveQuotaState(callerID string, state *callerState, now time.Time) (*callerState, int64, error) {
	copyState := *state
	if q.backgroundDB == nil {
		return &copyState, 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var totals backgroundQuotaTotals
	query := `SELECT COALESCE(SUM(tokens),0) AS lifetime,
 COALESCE(SUM(CASE WHEN day >= ? THEN tokens ELSE 0 END),0) AS month,
 COALESCE(SUM(CASE WHEN day >= ? THEN tokens ELSE 0 END),0) AS day,
 (SELECT COALESCE(SUM(tokens),0) FROM response_background_quota_second WHERE caller_id = ? AND second >= ?) AS minute
 FROM response_background_quota_daily WHERE caller_id = ?`
	day := now.UTC().Format("2006-01-02")
	month := monthStart(now.UTC()).Format("2006-01-02")
	if err := q.backgroundDB.WithContext(ctx).Raw(query, month, day, callerID, now.Add(-time.Minute).Unix(), callerID).Scan(&totals).Error; err != nil {
		return nil, 0, err
	}
	add := func(base, extra int64) (int64, error) {
		if base < 0 || extra < 0 || base > math.MaxInt64-extra {
			return 0, errors.New("invalid background quota aggregate")
		}
		return base + extra, nil
	}
	var err error
	if copyState.DayTokens, err = add(copyState.DayTokens, totals.Day); err != nil {
		return nil, 0, err
	}
	if copyState.MonthTokens, err = add(copyState.MonthTokens, totals.Month); err != nil {
		return nil, 0, err
	}
	if copyState.LifetimeTokens, err = add(copyState.LifetimeTokens, totals.Lifetime); err != nil {
		return nil, 0, err
	}
	if totals.Minute < 0 {
		return nil, 0, errors.New("invalid background rate aggregate")
	}
	return &copyState, totals.Minute, nil
}
