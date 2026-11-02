// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"sort"
	"strings"
	"time"
)

// cfoBudgetSnapshot reports the live token-quota state, not USD spending or
// utilization for the historical report window.
type cfoBudgetSnapshot struct {
	Available  bool           `json:"available"`
	Truncated  bool           `json:"truncated"`
	CapturedAt string         `json:"capturedAt"`
	Rows       []cfoBudgetRow `json:"rows"`
}

type cfoBudgetRow struct {
	CallerID string   `json:"callerId"`
	Project  string   `json:"project"`
	Window   string   `json:"window"`
	Used     int64    `json:"usedTokens"`
	Limit    int64    `json:"limitTokens"`
	UtilPct  *float64 `json:"utilizationPct"`
}

// cfoUsageTokenID mirrors the authentication path for internal exact filtering.
// It must never be serialized: legacy IDs can contain credentials.
func cfoUsageTokenID(c CallerConfig) string {
	if c.TokenID != "" {
		return publicTokenID(c.TokenID)
	}
	hash := strings.ToLower(strings.TrimSpace(c.TokenSHA256))
	if len(hash) == 64 {
		return "sha256:" + hash[:12]
	}
	return ""
}

func (s *Service) cfoBudgets(subject adminAuthSubject, global bool, opts UsageReportOptions) cfoBudgetSnapshot {
	now := time.Now().UTC()
	result := cfoBudgetSnapshot{CapturedAt: formatUsageTime(now), Rows: []cfoBudgetRow{}}
	if s.quota == nil || opts.TokenIDPrefix != "" {
		return result
	}
	result.Available = true
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	ids := make([]string, 0, len(s.quota.callers))
	for id := range s.quota.callers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	matched := 0
	for _, id := range ids {
		caller := s.quota.callers[id]
		if caller == nil {
			continue
		}
		project, environment, user := callerProject(caller.cfg), callerEnvironment(caller.cfg), callerUser(caller.cfg)
		tokenID := cfoUsageTokenID(caller.cfg)
		if !global && !adminDomainAllowsCaller(subject.domain, project, environment) {
			continue
		}
		if (opts.CallerID != "" && opts.CallerID != caller.cfg.ID) || (opts.TokenID != "" && opts.TokenID != tokenID) || (opts.CallerProject != "" && opts.CallerProject != project) || (opts.CallerEnvironment != "" && opts.CallerEnvironment != environment) || (opts.CallerUser != "" && opts.CallerUser != user) {
			continue
		}
		if matched == 500 {
			result.Truncated = true
			break
		}
		matched++
		state := s.quota.stateFor(caller.cfg.ID, now)
		s.quota.resetWindows(state, now)
		for _, row := range []cfoBudgetRow{
			{CallerID: caller.cfg.ID, Project: project, Window: "day", Used: state.DayTokens, Limit: caller.cfg.Quota.Day.Tokens},
			{CallerID: caller.cfg.ID, Project: project, Window: "month", Used: state.MonthTokens, Limit: caller.cfg.Quota.Month.Tokens},
			{CallerID: caller.cfg.ID, Project: project, Window: "lifetime", Used: state.LifetimeTokens, Limit: caller.cfg.Key.LifetimeTokens},
		} {
			if row.Limit > 0 {
				percent := float64(row.Used) * 100 / float64(row.Limit)
				row.UtilPct = &percent
			}
			result.Rows = append(result.Rows, row)
		}
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].CallerID != result.Rows[j].CallerID {
			return result.Rows[i].CallerID < result.Rows[j].CallerID
		}
		return result.Rows[i].Window < result.Rows[j].Window
	})
	return result
}
