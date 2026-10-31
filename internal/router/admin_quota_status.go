// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"strings"
	"time"
)

func (s *Service) handleAdminQuotaStatus(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, globalReports bool) {
	if s.quota == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "quota-unavailable", "message": "quota-unavailable"}})
		return
	}
	q := r.URL.Query()
	callerFilter := setTrimmed(q["caller_id"])
	ownerFilter := setTrimmed(q["owner_user"])
	tokenFilter := setTrimmed(q["token_id"])

	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	now := time.Now().UTC()
	rows := make([]map[string]any, 0, len(s.quota.callers))
	for _, c := range s.quota.callers {
		if c == nil {
			continue
		}
		owner := c.ownerUser
		if owner == "" {
			owner = callerUser(c.cfg)
		}
		if len(callerFilter) > 0 && !callerFilter[strings.ToLower(c.cfg.ID)] {
			continue
		}
		if len(ownerFilter) > 0 && !ownerFilter[strings.ToLower(owner)] {
			continue
		}
		if len(tokenFilter) > 0 && !tokenFilter[strings.ToLower(c.cfg.TokenID)] {
			continue
		}
		if !globalReports && !adminDomainAllowsCaller(subject.domain, c.cfg.Project, c.cfg.Environment) {
			continue
		}
		st := s.quota.stateFor(c.cfg.ID, now)
		s.quota.resetWindows(st, now)
		effective, _, err := s.quota.effectiveQuotaState(c.cfg.ID, st, now)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "quota-unavailable", "message": "quota-unavailable"}})
			return
		}
		st = effective
		quotaState, _ := s.quota.quotaState(c.cfg, st, 0)
		keyState := s.quota.keyState(c.cfg, st, 0)
		rows = append(rows, map[string]any{
			"caller_id":   c.cfg.ID,
			"token_id":    c.cfg.TokenID,
			"owner_user":  owner,
			"project":     c.cfg.Project,
			"environment": c.cfg.Environment,
			"status":      c.cfg.Status,
			"quota_state": quotaState,
			"key_state":   keyState,
			"exhausted":   st.Disabled || keyState == "exhausted",
			"rate": map[string]any{
				"rpm":        c.cfg.Rate.RPM,
				"tpm":        c.cfg.Rate.TPM,
				"concurrent": c.cfg.Rate.Concurrent,
			},
			"quota": map[string]any{
				"day": map[string]any{
					"requests":            st.DayRequests,
					"tokens":              st.DayTokens,
					"configured_requests": c.cfg.Quota.Day.Requests,
					"configured_tokens":   c.cfg.Quota.Day.Tokens,
					"remaining_requests":  remaining(c.cfg.Quota.Day.Requests, st.DayRequests),
					"remaining_tokens":    remaining(c.cfg.Quota.Day.Tokens, st.DayTokens),
				},
				"month": map[string]any{
					"requests":            st.MonthRequests,
					"tokens":              st.MonthTokens,
					"configured_requests": c.cfg.Quota.Month.Requests,
					"configured_tokens":   c.cfg.Quota.Month.Tokens,
					"remaining_requests":  remaining(c.cfg.Quota.Month.Requests, st.MonthRequests),
					"remaining_tokens":    remaining(c.cfg.Quota.Month.Tokens, st.MonthTokens),
				},
				"soft_pct": c.cfg.Quota.SoftPct,
			},
			"key": map[string]any{
				"lifetime_tokens":   st.LifetimeTokens,
				"remaining_tokens":  remaining(c.cfg.Key.LifetimeTokens, st.LifetimeTokens),
				"configured_budget": c.cfg.Key.LifetimeTokens,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"callers": rows,
		"count":   len(rows),
	})
}

func setTrimmed(in []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func adminDomainAllowsCaller(domain, project, environment string) bool {
	wantProject, wantEnvironment := splitAuthzDomain(domain)
	if wantProject != "" && project != wantProject {
		return false
	}
	if wantEnvironment != "" && environment != wantEnvironment {
		return false
	}
	return wantProject != "" || wantEnvironment != ""
}
