// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"strings"
	"time"
)

func (s *Service) securityReportsEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Server.AdminReports.Security.Enabled && s.usage != nil
}

func (s *Service) recordSecurityAccessEvent(event securityAccessEvent) {
	if !s.securityReportsEnabled() {
		return
	}
	if event.TS.IsZero() {
		event.TS = time.Now().UTC()
	}
	if event.EventType == "" {
		event.EventType = "access"
	}
	if event.Outcome == "" {
		event.Outcome = securityOutcome(event.StatusCode)
	}
	retentionDays := s.cfg.Server.AdminReports.Security.RetentionDays
	if retentionDays > 0 {
		s.usage.PurgeSecurityAccessEventsBefore(event.TS.Add(-time.Duration(retentionDays) * 24 * time.Hour))
	}
	s.usage.EmitSecurityAccessEvent(event)
}

func (s *Service) recordRequestSecurityAccess(rc *requestContext, status int, code string) {
	if !s.securityReportsEnabled() || rc == nil {
		return
	}
	rec := rc.rec
	eventType := "api_request"
	if status == http.StatusUnauthorized {
		eventType = "api_auth_failed"
	} else if status == http.StatusForbidden {
		eventType = "api_authz_failed"
	}
	authSubject := ""
	authSource := "none"
	if rc.caller != nil {
		authSubject = authzSubjectForCaller(rc.caller).subject
		authSource = "caller_token"
	}
	reason := code
	if status == http.StatusUnauthorized && (rec.TokenID == "missing-token" || rec.TokenID == "invalid-token") {
		reason = rec.TokenID
	}
	if rc.securityReason != "" {
		reason = rc.securityReason
	}
	s.recordSecurityAccessEvent(securityAccessEvent{
		TS:                  time.Now().UTC(),
		RequestID:           rc.id,
		EventType:           eventType,
		Surface:             securitySurfaceForDialect(rc.dialect),
		HTTPMethod:          rc.method,
		PathTemplate:        rc.pathTemplate,
		StatusCode:          status,
		Outcome:             securityOutcome(status),
		ReasonCode:          reason,
		AuthSubject:         authSubject,
		AuthSource:          authSource,
		CallerID:            rec.CallerID,
		CallerUser:          rec.CallerUser,
		CallerProject:       rec.CallerProject,
		CallerEnvironment:   rec.CallerEnvironment,
		TokenID:             rec.TokenID,
		Client:              rec.Client,
		IPAddress:           rc.clientIP.Address,
		IPVersion:           rc.clientIP.Version,
		IPSource:            rc.clientIP.Source,
		TrustedProxyApplied: rc.clientIP.TrustedProxyApplied,
		RequestIsPrivate:    rc.clientIP.IsPrivate,
		RequestIsLoopback:   rc.clientIP.IsLoopback,
		RequestIsReserved:   rc.clientIP.IsReserved,
		ModelGroup:          defaultString(rec.ResolvedGroup, rec.RequestedModel),
		RequestedModel:      rec.RequestedModel,
		ResolvedGroup:       rec.ResolvedGroup,
		InputTokens:         rec.Usage.InputTokens,
		OutputTokens:        rec.Usage.OutputTokens,
		TotalTokens:         totalTokens(rec.Usage),
	})
}

func (s *Service) recordAdminSecurityAccess(r *http.Request, subject adminAuthSubject, status int, reason, object, action string) {
	if !s.securityReportsEnabled() {
		return
	}
	ipInfo := resolveClientIP(r, s.cfg.Server.ClientIP)
	authSubject := ""
	authSource := "none"
	adminSubject := ""
	adminDomain := ""
	if subject.subject != "" {
		authSubject = subject.source + ":" + subject.subject
		authSource = subject.source
		adminSubject = subject.subject
		adminDomain = subject.domain
	}
	s.recordSecurityAccessEvent(securityAccessEvent{
		TS:                  time.Now().UTC(),
		EventType:           "admin_access",
		Surface:             adminSecuritySurface(r),
		HTTPMethod:          safeMethod(r),
		PathTemplate:        adminPathTemplate(r),
		StatusCode:          status,
		Outcome:             securityOutcome(status),
		ReasonCode:          reason,
		AuthSubject:         authSubject,
		AuthSource:          authSource,
		AdminSubject:        adminSubject,
		AdminDomain:         adminDomain,
		Client:              inferClient(r),
		UserAgentFamily:     userAgentFamily(r.UserAgent()),
		IPAddress:           ipInfo.Address,
		IPVersion:           ipInfo.Version,
		IPSource:            ipInfo.Source,
		TrustedProxyApplied: ipInfo.TrustedProxyApplied,
		RequestIsPrivate:    ipInfo.IsPrivate,
		RequestIsLoopback:   ipInfo.IsLoopback,
		RequestIsReserved:   ipInfo.IsReserved,
		ModelGroup:          object,
		RequestedModel:      action,
		ResolvedGroup:       object + ":" + action,
	})
}

func securityOutcome(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusForbidden:
		return "forbidden"
	case status >= 500:
		return "error"
	case status >= 400:
		return "denied"
	default:
		return "allowed"
	}
}

func securitySurfaceForDialect(dialect string) string {
	raw := strings.ToLower(strings.TrimSpace(dialect))
	switch raw {
	case "usage":
		return "v1_usage"
	case "metrics":
		return "metrics"
	case "content-admin":
		return "content_capture"
	}
	switch normalizeDialect(dialect) {
	case "openai-responses":
		return "v1_responses"
	case "anthropic":
		return "v1_messages"
	case "openai-chat":
		return "v1_chat_completions"
	default:
		return defaultString(strings.ReplaceAll(dialect, "-", "_"), "api")
	}
}

func adminSecuritySurface(r *http.Request) string {
	if r == nil {
		return "admin"
	}
	path := r.URL.Path
	switch {
	case strings.Contains(path, "/api/security/"):
		return "admin_security_reports"
	case strings.Contains(path, "/admin/reports"):
		return "admin_reports"
	case strings.Contains(path, "/admin/auth"):
		return "admin_auth"
	default:
		return "admin"
	}
}

func adminPathTemplate(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	path := r.URL.Path
	if strings.Contains(path, "/api/request/") {
		return "/admin/reports/api/request/{request_id}"
	}
	if strings.Contains(path, "/api/request-evidence") {
		return "/admin/reports/api/request-evidence"
	}
	return path
}

func requestPathTemplate(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens", "/v1/models", "/v1/usage", "/metrics":
		return r.URL.Path
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			return "/v1/{resource}"
		}
		return r.URL.Path
	}
}

func safeMethod(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Method
}

func userAgentFamily(ua string) string {
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "curl"):
		return "command-line"
	case strings.Contains(lower, "mozilla"):
		return "browser"
	case strings.TrimSpace(ua) == "":
		return "unknown"
	default:
		return "other"
	}
}
