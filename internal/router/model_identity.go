// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Caller-facing model identity (issue #254), Metrum AI Router.
//
// IRResponse.Model and Target.Model stay the upstream provider model so usage
// rows, attempt rows, logs, metrics and cache keys keep recording what served
// the request. The public name is applied only where a protocol `model` field
// is written to the caller.

// publicModel returns the router model group to report to the caller when
// server.responses.model_identity is requested_group, or "" when caller-facing
// bodies should keep the upstream model unchanged.
func (rc *requestContext) publicModel() string {
	if rc == nil || rc.modelIdentity != ModelIdentityRequestedGroup {
		return ""
	}
	return rc.rec.RequestedModel
}

// callerModel returns the caller-facing name for a response produced by the
// given upstream model.
func (rc *requestContext) callerModel(upstream string) string {
	if public := rc.publicModel(); public != "" {
		return public
	}
	return upstream
}

// withPublicModel returns raw with its top-level model set to public. raw is
// shallow-copied because it can be shared with the response cache and logs.
func withPublicModel(raw map[string]any, public string) map[string]any {
	if public == "" || raw == nil {
		return raw
	}
	out := cloneMap(raw)
	out["model"] = public
	return out
}

// callerSafeAttemptTargets replaces the provider/model target list in
// caller-facing upstream failure details with non-identifying entries when
// the request reports the requested group (issue #257).
func callerSafeAttemptTargets(rc *requestContext, attempts int) []map[string]any {
	out := make([]map[string]any, 0, attempts)
	for i := 0; i < attempts; i++ {
		entry := map[string]any{"attempt": i + 1}
		if rc != nil && i < len(rc.rec.AttemptsDetail) {
			if class := rc.rec.AttemptsDetail[i].ErrorClass; class != "" {
				entry["error_class"] = class
			}
		}
		out = append(out, entry)
	}
	return out
}

// callerSafeErrorText removes upstream response bodies, provider names,
// provider hosts and upstream model IDs from caller-facing error text in
// requested_group mode. Operator records keep the full sanitized text.
func (s *Service) callerSafeErrorText(text, group string) string {
	if isUpstreamStatusBodyDiagnostic(text) {
		text = sanitizeDiagnosticText(text, false, s.diagnosticMaxErrorBytes())
	} else {
		text = s.sanitizeDiagnosticError(text)
	}
	if s == nil || s.cfg == nil || text == "" {
		return text
	}
	return redactUpstreamIdentity(text, upstreamIdentityTerms(s.cfg, group))
}

// upstreamIdentityTerms lists provider names, provider hosts and upstream
// model IDs from the catalog, longest first, excluding the public group name.
func upstreamIdentityTerms(cfg *Config, group string) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(term string) {
		term = strings.TrimSpace(term)
		if len(term) < 3 || strings.EqualFold(term, group) || seen[strings.ToLower(term)] {
			return
		}
		seen[strings.ToLower(term)] = true
		terms = append(terms, term)
	}
	for name, provider := range cfg.Provider {
		add(name)
		if u, err := url.Parse(provider.BaseURL); err == nil {
			add(u.Hostname())
		}
		for id := range provider.Models {
			add(id)
		}
	}
	for _, g := range cfg.Models {
		for _, target := range g.Targets {
			add(target.Provider)
			add(target.Model)
		}
	}
	sort.Slice(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	return terms
}

func redactUpstreamIdentity(text string, terms []string) string {
	for _, term := range terms {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(term))
		text = re.ReplaceAllString(text, "[upstream]")
	}
	return text
}
