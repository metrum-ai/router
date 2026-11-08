// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
)

// TestConfigReloadRefusedWhenItWouldLockOutPolicyAdministration drives a REAL
// reloadActiveConfigFromDB: the reloaded document disables the only enabled
// administrator that can administer policy, so the reload is refused, the old
// configuration is kept, and the refusal is counted.
func TestConfigReloadRefusedWhenItWouldLockOutPolicyAdministration(t *testing.T) {
	dir := t.TempDir()
	usagePath := filepath.Join(dir, "usage.sqlite")
	seed, err := openTestUsageStorePath(usagePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// The security-admin role carries the immutable management capability; the
	// subject is a normal basic administrator that can write and activate policy.
	if err := seed.CreateAuthzPolicySet(authzPolicySetInput{
		ID: "policy-set", Name: "main", Version: 1,
		Policy: []string{
			"p, adminkey:policy-admin, *, admin:policy, write|activate",
			"g, adminkey:policy-admin, " + securityAdministratorRole + ", *",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := seed.ActivateAuthzPolicySet("policy-set", "tester", "req-guard", "guard fixture", now); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	db := callerAdminTestDB(t)
	key, err := GenerateAdminAPIKey("policy-admin", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_POLICY_ADMIN_HASH", key.TokenHash)
	basicHash, err := bcrypt.GenerateFromPassword([]byte("guard-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_POLICY_ADMIN_BASIC_HASH", string(basicHash))

	// The reloaded document has NO administrator at all: applying it would leave
	// zero enabled normal policy administrators.
	lockedOutYAML := `server:
  listen: ':8080'
  default_model_group: default
  admin_auth:
    authorization:
      enabled: true
      source: db
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: http://127.0.0.1:1/v1
    dialect: openai
    api_key_env: TEST_API_KEY
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers: []
`
	hash := sha256.Sum256([]byte(lockedOutYAML))
	if err := db.Exec(
		`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`,
		lockedOutYAML, hex.EncodeToString(hash[:]), time.Now().UTC().Format(time.RFC3339Nano), "set-1",
	).Error; err != nil {
		t.Fatal(err)
	}

	usageEnabled := true
	cfg := &Config{
		Server: ServerConfig{
			Identifiers:       IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "default",
			AdminAuth: AdminAuthConfig{
				APIKeys: []AdminAPIKeyConfig{{
					KeyID: "policy-admin", KeyHashEnv: "TEST_POLICY_ADMIN_HASH",
					Domain: "*", Permissions: []string{"config:read"},
				}},
				Authorization: AdminAuthorizationConfig{Enabled: true, Source: "db"},
			},
			UsageDB: UsageDBConfig{
				Enable: &usageEnabled, Driver: "sqlite", Path: usagePath,
				MigrationPolicy: usageDBMigrationPolicyAutoSafe,
			},
			ConfigSource: ConfigSourceConfig{
				Mode: "database", Driver: "sqlite",
				Path: db.Config.Dialector.(*sqlite.Dialector).DSN, RuntimeScope: "staging",
			},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"mock": {BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai", APIKey: "test-key"},
		},
		Models: map[string]ModelGroup{
			"default": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model"}}},
		},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)

	if err := svc.reloadActiveConfigFromDB(); !errors.Is(err, ErrLastPolicyAdmin) {
		t.Fatalf("reload that locks out policy administration = %v, want ErrLastPolicyAdmin", err)
	}
	// The old configuration is kept: the administrator is still configured.
	if len(svc.cfg.Server.AdminAuth.APIKeys) != 1 || svc.cfg.Server.AdminAuth.APIKeys[0].KeyID != "policy-admin" {
		t.Fatalf("old configuration was replaced: %+v", svc.cfg.Server.AdminAuth.APIKeys)
	}
	metrics := svc.adminMetrics()
	if metrics == nil {
		t.Fatal("no metrics store")
	}
	if got := metrics.policyGuardRefusals; got != 1 {
		t.Fatalf("policy guard refusals = %d, want 1", got)
	}
	scrape := metrics.Prometheus(nil)
	if !strings.Contains(scrape, "router_admin_policy_guard_refusals_total 1") {
		t.Fatalf("metrics do not expose the refusal:\n%s", scrape)
	}

	// Positive control: the SAME reload machinery accepts a document that keeps
	// an enabled policy administrator, so the refusal above is the guard.
	keptYAML := strings.Replace(lockedOutYAML, "    authorization:\n      enabled: true\n      source: db\n", `    api_keys:
      - key_id: policy-admin
        key_hash_env: TEST_POLICY_ADMIN_HASH
        domain: '*'
        permissions: ['config:read']
    authorization:
      enabled: true
      source: db
`, 1)
	keptHash := sha256.Sum256([]byte(keptYAML))
	if err := db.Exec(
		`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`,
		keptYAML, hex.EncodeToString(keptHash[:]), time.Now().UTC().Format(time.RFC3339Nano), "set-1",
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("reload keeping an enabled policy administrator = %v, want success", err)
	}
	if got := svc.adminMetrics().policyGuardRefusals; got != 1 {
		t.Fatalf("successful reload changed the refusal counter: %d", got)
	}
}

// TestConfiguredPolicyAdminInventoryCountsOnlyMachineKeys pins today's inventory
// contract: configured admin keys whose verifier resolves count, disabled keys do
// not, and basic (non-machine) administrators never count until #871 supplies the
// prepared machine generation through the seam.
func TestConfiguredPolicyAdminInventoryCountsOnlyMachineKeys(t *testing.T) {
	key, err := GenerateAdminAPIKey("inventory-admin", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_INVENTORY_KEY_HASH", key.TokenHash)
	basicHash, err := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_INVENTORY_BASIC_HASH", string(basicHash))
	cfg := &Config{}
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{
		{KeyID: "inventory-admin", KeyHashEnv: "TEST_INVENTORY_KEY_HASH", Domain: "*"},
		{KeyID: "disabled-admin", KeyHashEnv: "TEST_INVENTORY_KEY_HASH", Domain: "*", Status: "disabled"},
	}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, Users: []AdminBasicAuthUser{{
		Username: "human", Subject: "basic:human", PasswordHashEnv: "TEST_INVENTORY_BASIC_HASH", Domain: "*",
	}}}
	inventory, err := configuredPolicyAdminInventory{}.enabledPolicyAdmins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 1 {
		t.Fatalf("inventory = %+v, want exactly the one enabled machine key", inventory)
	}
	if inventory[0].Kind != "adminkey" || inventory[0].CredentialID != "inventory-admin" {
		t.Fatalf("unexpected inventory entry: %+v", inventory[0])
	}
	if inventory[0].Subject != key.KeyID && inventory[0].Subject != "adminkey:inventory-admin" {
		t.Fatalf("unexpected subject: %q", inventory[0].Subject)
	}
}
