// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

// supportedModelStrategies is the shared dispatch and validation allow-list.
// Keep it aligned with pickTarget's cases; both validation and runtime use
// normalizeModelStrategy so the accepted configuration surface cannot drift.
var supportedModelStrategies = [...]string{
	"static",
	"failover",
	"weighted",
	"latency",
	"cost",
	"semantic",
	"tool_aware",
	"dynamic_score",
	"external",
	"intelligent",
}

func normalizeModelStrategy(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, supported := range supportedModelStrategies {
		if normalized == supported {
			return normalized
		}
	}
	return ""
}
