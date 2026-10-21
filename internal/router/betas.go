// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

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
