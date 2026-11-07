// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Schema only: accepting these approved keys is not evidence that production
// validators/factories, event adapters or reload installation consume them.
func TestRuntimeMachineApprovedAuthenticationSchema(t *testing.T) {
	text := `api_keys:
  enabled: false
issuers:
  - issuer_url: https://issuer.example
    audience: router-admin
    kind: federated
    algorithms: [RS256]
    max_lifetime: 1h
    clock_skew: 30s
    subjects:
      - claim: sub
        value: system:serviceaccount:ops:router
        subject: workload:router
    sender_constraint: dpop
jwks:
  cache_ttl: 5m
  refresh_min_interval: 30s
  request_timeout: 5s
dpop:
  proof_max_age: 1m
  replay_capacity: 10000
emergency:
  credentials: []
`
	var auth AdminAuthenticationConfig
	decoder := yaml.NewDecoder(strings.NewReader(text))
	decoder.KnownFields(true)
	if err := decoder.Decode(&auth); err != nil {
		t.Fatal("approved authentication schema rejected by actual listener DTO", err)
	}
	encoded, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatal(err)
	}
	var keys struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(roundtrip["apiKeys"], &keys); err != nil {
		t.Fatal(err)
	}
	if keys.Enabled == nil || *keys.Enabled {
		t.Fatal("explicit API-key disable lost in config roundtrip")
	}
}

func TestRuntimeMachineProductionSchemaRejectsTestTransportHook(t *testing.T) {
	var auth AdminAuthenticationConfig
	decoder := yaml.NewDecoder(strings.NewReader("issuers:\n  - issuer_url: https://issuer.example\n    allow_insecure_loopback: true\n"))
	decoder.KnownFields(true)
	if err := decoder.Decode(&auth); err == nil || !strings.Contains(err.Error(), "allow_insecure_loopback") {
		t.Fatal("production schema must specifically reject test-only HTTP-loopback key", err)
	}
}
