// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"testing"
)

// The #866 inventory is answered from the prepared generation: configured keys,
// mTLS SAN mappings, oauth/federated subject maps and DB-store keys count as
// enabled administrators.
func TestGenerationInventoryCountsPreparedMachinePrincipals(t *testing.T) {
	key := generateTestAdminKey(t, "inv-key")
	t.Setenv("TEST_INV_HASH", key.TokenHash)
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "TEST_INV_HASH", Subject: "adminkey:inv", Domain: "default", Permissions: []string{"admin:callers:list"}, Status: "active"}}
	rt := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1), lookup: os.LookupEnv}
	compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := rt.prepare(cfg, compatible)
	if err != nil {
		t.Fatal(err)
	}
	// A prepared JWT issuer contributes its issuer-qualified subject mappings.
	generation.jwt = &adminMachineJWTDispatcher{issuers: map[string]adminMachineJWTBinding{
		"https://issuer.example": {Authenticator: &adminMachineJWTAuthenticator{cfg: adminMachineJWTConfig{
			Issuer: "https://issuer.example", Kind: "federated",
			Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "system:serviceaccount:ops:router", Subject: "workload:router"}},
		}}},
	}}
	inventory, err := generationInventory{generation: generation}.enabledPolicyAdmins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]string{}
	for _, principal := range inventory {
		byKind[principal.Kind] = append(byKind[principal.Kind], principal.Subject)
	}
	if len(byKind["adminkey"]) == 0 || len(byKind["federated"]) == 0 {
		t.Fatalf("inventory missing prepared principals: %+v", byKind)
	}
	for _, subject := range byKind["adminkey"] {
		if subject == "" {
			t.Fatal("empty admin-key subject in inventory")
		}
	}
	if byKind["federated"][0] != "workload:router" {
		t.Fatalf("federated subject inventory = %v", byKind["federated"])
	}
}
