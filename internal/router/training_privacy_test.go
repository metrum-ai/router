// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrainingPrivacyNamedJSONSecrets(t *testing.T) {
	input := map[string]any{
		"arguments":      `{"api_key":"opaque-test-value","ticket":1}`,
		"content":        `JSON example: {"password":"opaque-password-test"}`,
		"expected_calls": []any{map[string]any{"arguments": map[string]any{"client_secret": "opaque-checker-test"}}},
	}
	safe := trainingPrivacy(input).(map[string]any)
	encoded, _ := json.Marshal(safe)
	for _, secret := range []string{"opaque-test-value", "opaque-password-test", "opaque-checker-test"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("named secret survived export privacy filter")
		}
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(safe["arguments"].(string)), &arguments); err != nil {
		t.Fatalf("redaction corrupted function argument JSON: %v", err)
	}
	if arguments["ticket"] != float64(1) {
		t.Fatal("nonsecret argument changed")
	}
}
