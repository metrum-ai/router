// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"strings"
)

func (s *Service) writeBetaFieldError(w http.ResponseWriter, rc *requestContext, field string) {
	const code = "unsupported_parameter"
	message := field + " requires a permitted anthropic-beta"
	if rc != nil {
		rc.trace("request_error", message, Target{}, 0, http.StatusBadRequest, code, false, 0)
		rc.rec.Status = http.StatusBadRequest
		c := code
		rc.rec.Error = &c
		rc.rec.ErrorClass = code
		rc.rec.ErrorMessage = message
		s.finish(rc, http.StatusBadRequest, &c)
	}
	if rc != nil && rc.dialect == "anthropic" {
		writeAnthropicError(w, rc, http.StatusBadRequest, message)
		return
	}
	writeAPIJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": code, "message": message, "param": field}})
}

func permittedBetas(caller string, allowed []string) string {
	var result []string
	for _, beta := range strings.Split(caller, ",") {
		beta = strings.TrimSpace(beta)
		if beta != "" && stringSliceContains(allowed, beta) && !stringSliceContains(result, beta) {
			result = append(result, beta)
		}
	}
	return strings.Join(result, ",")
}

// Match fields to their actual feature beta, not just any allowed beta.
func unpermittedBetaField(req *IRRequest, target Target) string {
	permitted := strings.Split(permittedBetas(req.CallerBetas, target.AllowedBetas), ",")
	for _, gate := range []struct{ field, beta string }{
		{"context_management", "context-management-2025-06-27"},
		{"container", "code-execution-2025-08-25"},
		{"mcp_servers", "mcp-client-2025-04-04"},
	} {
		if _, present := req.Raw[gate.field]; present && !stringSliceContains(permitted, gate.beta) {
			return gate.field
		}
	}
	return ""
}
