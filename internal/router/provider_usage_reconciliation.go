// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ProviderUsageSource retrieves a provider's aggregate USD cost for one UTC window.
// Implementations must not include credentials or response bodies in returned errors.
type ProviderUsageSource interface {
	ProviderCost(context.Context, string, time.Time, time.Time) (float64, error)
}

var errProviderUsageSourceFailed = errors.New("provider usage source request failed")

type providerUsageGap struct {
	Provider    string
	WindowFrom  time.Time
	WindowTo    time.Time
	RouterUSD   float64
	ProviderUSD float64
	GapUSD      float64
}

type providerUsageReconciler struct {
	usage     *usageStore
	source    ProviderUsageSource
	threshold float64
	onGap     func(providerUsageGap) error
}

func (r *providerUsageReconciler) reconcile(ctx context.Context, provider string, from, to time.Time) (bool, error) {
	if r == nil || r.usage == nil || r.usage.db == nil || r.source == nil {
		return false, errors.New("provider usage reconciliation is not configured")
	}
	provider = strings.TrimSpace(provider)
	from, to = from.UTC(), to.UTC()
	if provider == "" || from.IsZero() || !to.After(from) || math.IsNaN(r.threshold) || math.IsInf(r.threshold, 0) || r.threshold < 0 {
		return false, errors.New("provider usage reconciliation window or threshold is invalid")
	}
	var routerUSD float64
	if err := r.usage.db.WithContext(ctx).Model(&usageRecord{}).
		Select("COALESCE(SUM(total_cost_usd), 0)").
		Where("target_provider = ? AND ts >= ? AND ts < ?", provider, formatUsageTime(from), formatUsageTime(to)).
		Scan(&routerUSD).Error; err != nil {
		return false, fmt.Errorf("read router usage aggregate: %w", err)
	}
	providerUSD, err := r.source.ProviderCost(ctx, provider, from, to)
	if err != nil {
		return false, errProviderUsageSourceFailed
	}
	if math.IsNaN(providerUSD) || math.IsInf(providerUSD, 0) || providerUSD < 0 || math.IsNaN(routerUSD) || math.IsInf(routerUSD, 0) || routerUSD < 0 {
		return false, errors.New("provider usage source returned an invalid cost")
	}
	gap := providerUSD - routerUSD
	if gap <= r.threshold {
		return false, nil
	}
	if r.onGap == nil {
		return false, errors.New("provider usage gap signal is unavailable")
	}
	err = r.onGap(providerUsageGap{Provider: provider, WindowFrom: from, WindowTo: to, RouterUSD: routerUSD, ProviderUSD: providerUSD, GapUSD: gap})
	return err == nil, err
}

func (s *Service) recordProviderUsageGap(gap providerUsageGap) error {
	if s == nil || s.usage == nil || s.usage.db == nil {
		return errors.New("provider usage signal store is unavailable")
	}
	if gap.Provider == "" || gap.GapUSD <= 0 {
		return errors.New("provider usage gap is invalid")
	}
	// The event carries only bounded provider/window identifiers. Cost details stay
	// out of the general security event until the incident contract is versioned.
	s.usage.EmitSecurityAccessEvent(securityAccessEvent{
		TS:             time.Now().UTC(),
		EventType:      "provider_usage_gap",
		Surface:        "provider_usage_reconciliation",
		HTTPMethod:     "scheduled",
		PathTemplate:   "daily",
		StatusCode:     200,
		Outcome:        "reported",
		ReasonCode:     "provider_usage_above_router_usage",
		ModelGroup:     gap.Provider,
		RequestedModel: formatUsageTime(gap.WindowFrom) + "/" + formatUsageTime(gap.WindowTo),
	})
	return nil
}
