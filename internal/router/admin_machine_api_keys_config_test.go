// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

// #815 lifetime bounds are validated by the same startup validator as the rest
// of admin.authentication, so a bad value is a configuration error, never a
// request-time surprise.
func TestRuntimeMachineAPIKeyLifetimeBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  AdminMachineAPIKeysConfig
		ok   bool
	}{
		{"defaults", AdminMachineAPIKeysConfig{}, true},
		{"explicit bounds", AdminMachineAPIKeysConfig{MaxLifetime: "24h", DefaultRotationOverlap: "10m", MaxRotationOverlap: "1h"}, true},
		{"lifetime below the floor", AdminMachineAPIKeysConfig{MaxLifetime: "1m"}, false},
		{"lifetime above the ceiling", AdminMachineAPIKeysConfig{MaxLifetime: "1000h"}, false},
		{"overlap above the ceiling", AdminMachineAPIKeysConfig{MaxRotationOverlap: "2h"}, false},
		{"default overlap above maximum", AdminMachineAPIKeysConfig{DefaultRotationOverlap: "30m", MaxRotationOverlap: "10m"}, false},
		{"malformed duration", AdminMachineAPIKeysConfig{MaxLifetime: "soon"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := prepareAdminMachineRuntimeSettings(AdminMachineAuthenticationConfig{APIKeys: tc.cfg}, "")
			if tc.ok && err != nil {
				t.Fatalf("valid bounds rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("invalid API-key lifetime bounds accepted")
			}
		})
	}
}
