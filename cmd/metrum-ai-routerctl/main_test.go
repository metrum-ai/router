// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
	"gopkg.in/yaml.v3"
)

const offlineLockTestPathEnv = "ROUTERCTL_OFFLINE_LOCK_TEST_DB"
const offlineLockTestCommandEnv = "ROUTERCTL_OFFLINE_LOCK_TEST_COMMAND"

func TestRouterctlOfflineConfigLockChild(t *testing.T) {
	path := os.Getenv(offlineLockTestPathEnv)
	if path == "" {
		return
	}
	args, _ := os.LookupEnv(offlineLockTestCommandEnv)
	commandArgs := strings.Fields(args)
	if len(commandArgs) == 0 {
		commandArgs = []string{"export"}
	}
	configCommand(append(commandArgs, "--offline", "--driver", "sqlite", "--db", path))
}

func TestRouterctlOfflineImportAndActivateRefuseLiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	shared, err := router.AcquireUsageRuntimeLock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	yamlPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(yamlPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{"import --yaml " + yamlPath + " --name test", "activate --set-id test"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRouterctlOfflineConfigLockChild$")
		cmd.Env = append(os.Environ(), offlineLockTestPathEnv+"="+path, offlineLockTestCommandEnv+"="+args)
		output, err := cmd.CombinedOutput()
		if err == nil || !offlineRefusal(strings.ToLower(string(output))) {
			t.Fatalf("offline %s did not refuse the live writer: err=%v output=%s", args, err, output)
		}
	}
}

func TestRouterctlOfflineConfigExportRefusesLiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, closeDB, err := router.OpenConfigControlPlaneDB(router.UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	const setID = "live-lock-export"
	if err := db.Exec(`CREATE TABLE router_config_documents (config_set_id TEXT PRIMARY KEY, canonical_yaml TEXT NOT NULL, content_sha256 TEXT NOT NULL, updated_at TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO router_config_documents (config_set_id, canonical_yaml, content_sha256, updated_at) VALUES (?, ?, ?, ?)`, setID, "server:\n  listen: \":8080\"\n", "fixture", "2026-01-01T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}
	shared, err := router.AcquireUsageRuntimeLock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRouterctlOfflineConfigLockChild$")
	cmd.Env = append(os.Environ(), offlineLockTestPathEnv+"="+path, offlineLockTestCommandEnv+"=export --set-id "+setID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read-only export should work with the router's shared runtime lock held: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "listen:") {
		t.Fatalf("export did not return the selected config set: %s", output)
	}
}

func offlineRefusal(message string) bool {
	return strings.Contains(message, "holds the runtime lock") || strings.Contains(message, "resource temporarily unavailable") || strings.Contains(message, "shared file locks") || strings.Contains(message, "unavailable on windows")
}

// assertV48CallerValidation ports the caller-verifier branch of v4.8.0's
// Config.Validate so migration tests prove rollback compatibility without
// claiming to have run the v4.8 release binary.
func assertV48CallerValidation(t *testing.T, cfg *router.Config) {
	t.Helper()
	for _, caller := range cfg.Callers {
		if caller.TokenHash != "" {
			if caller.TokenSHA256 != "" {
				t.Fatalf("v4.8 rejects caller %s with both token verifiers", caller.ID)
			}
			if err := router.ValidateCallerTokenHash(caller.TokenHash); err != nil {
				t.Fatalf("v4.8 rejects caller %s token_hash: %v", caller.ID, err)
			}
			if strings.TrimSpace(caller.TokenID) == "" {
				t.Fatalf("v4.8 rejects caller %s token_hash without token_id", caller.ID)
			}
			continue
		}
		if len(caller.TokenSHA256) != sha256.Size*2 {
			t.Fatalf("v4.8 rejects caller %s without a valid token_sha256", caller.ID)
		}
		if _, err := hex.DecodeString(caller.TokenSHA256); err != nil {
			t.Fatalf("v4.8 rejects caller %s token_sha256: %v", caller.ID, err)
		}
	}
}

func TestCallerGenerateWritesTokenOnceAndNeverPrintsIt(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "caller.token")
	run := func() ([]byte, error) {
		return exec.Command("go", "run", ".", "callers", "generate", "--owner-user", "operator", "--project", "example", "--allow", "default", "--token-out", tokenPath).CombinedOutput()
	}
	output, err := run()
	if err != nil {
		t.Fatalf("generate: %v: %s", err, output)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode=%o, want 600", info.Mode().Perm())
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil || !strings.HasPrefix(string(token), "rtr_metrum_") {
		t.Fatalf("token not written once: %v %q", err, token)
	}
	if strings.Contains(string(output), strings.TrimSpace(string(token))) || !strings.Contains(string(output), "configuration-controller-required") {
		t.Fatalf("unsafe or incomplete generate output: %s", output)
	}
	if second, secondErr := run(); secondErr == nil || !strings.Contains(string(second), "file exists") {
		t.Fatalf("second generate did not fail closed: %v %s", secondErr, second)
	}
}

func TestAdminKeyGenerateWritesKeyOnceAndNeverPrintsIt(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "admin.key")
	run := func() ([]byte, error) {
		return exec.Command("go", "run", ".", "adminkeys", "generate", "--key-id", "ops-primary", "--permissions", "config:read,config:write", "--token-out", keyPath).CombinedOutput()
	}
	output, err := run()
	if err != nil {
		t.Fatalf("generate: %v: %s", err, output)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o, want 600", info.Mode().Perm())
	}
	key, err := os.ReadFile(keyPath)
	if err != nil || !strings.HasPrefix(string(key), "rtr_admin_ops-primary_") {
		t.Fatalf("key not written once: %v", err)
	}
	if strings.Contains(string(output), strings.TrimSpace(string(key))) {
		t.Fatalf("raw admin key printed: %s", output)
	}
	var decoded struct {
		KeyID     string `json:"key_id"`
		TokenHash string `json:"token_hash"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatalf("output not JSON: %v: %s", err, output)
	}
	if decoded.KeyID != "ops-primary" || !strings.HasPrefix(decoded.TokenHash, "pbkdf2-sha3-512:") {
		t.Fatalf("unexpected generate output: %s", output)
	}
	if matched, err := router.VerifyCallerTokenHash(decoded.TokenHash, strings.TrimSpace(string(key))); err != nil || !matched {
		t.Fatalf("verifier does not match key: %v %v", matched, err)
	}
	if second, secondErr := run(); secondErr == nil || !strings.Contains(string(second), "file exists") {
		t.Fatalf("second generate did not fail closed: %v %s", secondErr, second)
	}
}

func TestCustomerConfigAndModelReadCommands(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"config.example.yaml", "env.example.json"} {
		body, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		target := name
		if name == "config.example.yaml" {
			target = "config.yaml"
			text := string(body)
			for _, name := range []string{"STANDARD", "CODING", "METRICS_ADMIN", "CONTENT_ADMIN"} {
				tokenHash, err := router.HashCallerToken("rtr_example_"+strings.ToLower(name)+"_test", nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				text = strings.ReplaceAll(text, "REPLACE_WITH_TOKEN_HASH_OF_"+name+"_ROUTER_TOKEN", tokenHash)
			}
			body = []byte(text)
		} else {
			target = "env.json"
			// Example env keeps the transform key empty; fill a valid 64-byte
			// hex value so rewrite-mode validation matches production loading.
			body = []byte(strings.Replace(string(body), `"ROUTER_ID_TRANSFORM_KEY": ""`, `"ROUTER_ID_TRANSFORM_KEY": "`+strings.Repeat("ab", 64)+`"`, 1))
		}
		if err := os.WriteFile(filepath.Join(dir, target), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	for _, command := range [][]string{{"config", "validate"}, {"config", "diff", "--from", configPath, "--to", configPath}, {"status"}, {"models", "list"}} {
		args := append([]string{"run", "."}, command...)
		if !(command[0] == "config" && command[1] == "diff") {
			args = append(args, "--config", configPath)
		}
		output, err := exec.Command("go", args...).CombinedOutput()
		if err != nil || !strings.Contains(string(output), "\"schema\"") {
			t.Fatalf("%v failed: %v %s", command, err, output)
		}
	}
}

func TestBlueprintRenderCLI(t *testing.T) {
	out := filepath.Join(t.TempDir(), "blueprint")
	intent := filepath.Join("..", "..", "deploy", "kubernetes", "intents", "shadeform-nvidia-local-models.example.yaml")
	output, err := exec.Command("go", "run", ".", "blueprint", "render", "--intent", intent, "--out", out).CombinedOutput()
	if err != nil {
		t.Fatalf("blueprint render: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "metrum.ai/smartrouter-blueprint-render/v1") {
		t.Fatalf("unexpected output: %s", output)
	}
	if _, err := os.Stat(filepath.Join(out, "architecture.md")); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(out, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "svc.cluster.local") {
		t.Fatal("expected cluster DNS in generated config")
	}
}

func TestCallerGenerateWriteMergesConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `
server:
  listen: ":8080"
  identifiers: {mode: passthrough}
  usage_db: {enabled: true, driver: sqlite, path: /tmp/x.sqlite, migration_policy: auto-safe}
providers:
  seed:
    base_url: http://seed.svc:8000/v1
    dialect: openai-chat
    api_key: "${SEED_KEY}"
    api_key_env: SEED_KEY
    models: {m1: {model: m1}}
models:
  default:
    strategy: static
    targets: [{provider: seed, model_ref: m1, weight: 100}]
users: [{id: op, name: op, type: service_account, status: active}]
projects: [{id: local, name: local, status: active}]
project_memberships: [{user_id: op, project: local, role: member, status: active}]
callers: []
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "caller.token")
	output, err := exec.Command("go", "run", ".", "callers", "generate",
		"--owner-user", "op", "--project", "local", "--allow", "default",
		"--token-out", tokenPath, "--config", cfgPath, "--write").CombinedOutput()
	if err != nil {
		t.Fatalf("generate --write: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "local-config-written-restart-required") {
		t.Fatalf("expected local write activation: %s", output)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "op-local-dev") || !strings.Contains(string(raw), "token_hash: pbkdf2-sha3-512:i=20000:") || strings.Contains(string(raw), "token_sha256") {
		t.Fatalf("caller not merged with token_hash: %s", raw)
	}
	if strings.Contains(string(output), strings.TrimSpace(string(mustRead(t, tokenPath)))) {
		t.Fatal("raw token must not appear in CLI output")
	}
	if strings.Contains(string(raw), strings.TrimSpace(string(mustRead(t, tokenPath)))) {
		t.Fatal("raw token must not be written to config")
	}

	// verify round-trips the generated token against the merged caller.
	verify := exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--config", cfgPath, "--id", "op-local-dev")
	if out, err := verify.CombinedOutput(); err != nil || !strings.Contains(string(out), `"match": true`) {
		t.Fatalf("verify generated token: %v: %s", err, out)
	}
	wrongPath := filepath.Join(dir, "wrong.token")
	if err := os.WriteFile(wrongPath, append(mustRead(t, tokenPath)[:20], []byte("not-the-secret\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", wrongPath, "--config", cfgPath, "--id", "op-local-dev").CombinedOutput()
	if err == nil || !strings.Contains(string(out), `"match": false`) {
		t.Fatalf("verify accepted a wrong token: %v: %s", err, out)
	}

	// hash converts an existing token to token_hash without rotating it.
	out, err = exec.Command("go", "run", ".", "callers", "hash", "--token-file", tokenPath).CombinedOutput()
	if err != nil {
		t.Fatalf("hash: %v: %s", err, out)
	}
	var hashed struct {
		TokenHash string `json:"token_hash"`
		TokenID   string `json:"token_id"`
	}
	if err := json.Unmarshal(out, &hashed); err != nil || !strings.HasPrefix(hashed.TokenHash, "pbkdf2-sha3-512:") || hashed.TokenID != strings.Join(strings.Split(strings.TrimSpace(string(mustRead(t, tokenPath))), "_")[:6], "_") {
		t.Fatalf("hash output: %v: %s", err, out)
	}
	if strings.Contains(string(out), strings.TrimSpace(string(mustRead(t, tokenPath)))) {
		t.Fatal("hash must not echo the raw token")
	}

	// Apply the documented v4 migration: old config fails with guidance,
	// then the same token verifies after installing the salted hash.
	legacyConfigPath := filepath.Join(dir, "legacy-config.yaml")
	rawToken := strings.TrimSpace(string(mustRead(t, tokenPath)))
	legacyDigest := sha256.Sum256([]byte(rawToken))
	legacyConfig := strings.Replace(string(raw), "token_hash:", "token_sha256: "+hex.EncodeToString(legacyDigest[:])+"\n    # token_hash:", 1)
	if err := os.WriteFile(legacyConfigPath, []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--config", legacyConfigPath, "--id", "op-local-dev").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "callers migrate-legacy") || !strings.Contains(string(out), "caller op-local-dev") {
		t.Fatalf("v4 config did not fail with migration guidance: %v: %s", err, out)
	}
	migrate := exec.Command("go", "run", ".", "callers", "migrate-legacy", "--config", legacyConfigPath, "--id", "op-local-dev", "--token-file", "-")
	migrate.Stdin = strings.NewReader(rawToken + "\n")
	out, err = migrate.CombinedOutput()
	if err != nil {
		t.Fatalf("migrate-legacy: %v: %s", err, out)
	}
	migratedCfg, err := router.LoadConfig(legacyConfigPath)
	if err != nil {
		t.Fatalf("load migrated caller: %v", err)
	}
	if migratedCfg.Callers[0].TokenFormatVersion != 0 || migratedCfg.Callers[0].TokenEntropyBits != 0 {
		t.Fatalf("legacy token was stamped with v2 entropy metadata: %+v", migratedCfg.Callers[0])
	}
	migratedCfg.Security.KeyHygiene = router.KeyHygieneConfig{MinEntropyBits: 128, Enforce: true}
	if err := migratedCfg.Validate(); err == nil || !strings.Contains(err.Error(), "token entropy") {
		t.Fatalf("enforce accepted migrated token with unknown entropy: %v", err)
	}
	out, err = exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--config", legacyConfigPath, "--id", "op-local-dev").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"match": true`) || strings.Contains(string(mustRead(t, legacyConfigPath)), "token_sha256") {
		t.Fatalf("migrated v4 config did not verify: %v: %s", err, out)
	}
	assertV48CallerValidation(t, migratedCfg)

	// A legacy token that cannot map to a token_id is rotated from its v4 file.
	freeLegacyPath := filepath.Join(dir, "freeform-legacy.yaml")
	freeLegacyToken := "legacy-free-form-router-key"
	freeDigest := sha256.Sum256([]byte(freeLegacyToken))
	freeConfig := strings.Replace(string(raw), "token_hash:", "token_sha256: "+hex.EncodeToString(freeDigest[:])+"\n    # token_hash:", 1)
	if err := os.WriteFile(freeLegacyPath, []byte(freeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	freeRotated := filepath.Join(dir, "freeform-rotated.token")
	out, err = exec.Command("go", "run", ".", "callers", "rotate", "--config", freeLegacyPath, "--id", "op-local-dev", "--token-out", freeRotated).CombinedOutput()
	if err != nil {
		t.Fatalf("rotate legacy file: %v: %s", err, out)
	}
	if strings.Contains(string(mustRead(t, freeLegacyPath)), "token_sha256") {
		t.Fatal("rotating legacy file left token_sha256 behind")
	}
	if out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", freeRotated, "--config", freeLegacyPath, "--id", "op-local-dev").CombinedOutput(); err != nil {
		t.Fatalf("verify token rotated from legacy file: %v: %s", err, out)
	}
	freeRotatedCfg, err := router.LoadConfig(freeLegacyPath)
	if err != nil {
		t.Fatalf("load rotated legacy config: %v", err)
	}
	assertV48CallerValidation(t, freeRotatedCfg)

	// Disabled legacy callers are cleaned without reading a token or issuing a replacement.
	disabledLegacyPath := filepath.Join(dir, "disabled-legacy.yaml")
	var disabledConfig router.Config
	if err := yaml.Unmarshal(raw, &disabledConfig); err != nil {
		t.Fatal(err)
	}
	disabledConfig.Callers[0].Status = "disabled"
	disabledConfig.Callers[0].TokenHash = ""
	disabledConfig.Callers[0].TokenSHA256 = hex.EncodeToString(legacyDigest[:])
	disabledYAML, err := yaml.Marshal(&disabledConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disabledLegacyPath, disabledYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("go", "run", ".", "callers", "migrate-legacy", "--config", disabledLegacyPath, "--id", "op-local-dev").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--remove-disabled") {
		t.Fatalf("disabled migration must require explicit removal: %v: %s", err, out)
	}
	out, err = exec.Command("go", "run", ".", "callers", "migrate-legacy", "--config", disabledLegacyPath, "--id", "op-local-dev", "--remove-disabled").CombinedOutput()
	if err != nil {
		t.Fatalf("remove disabled legacy caller: %v: %s", err, out)
	}
	disabledCfg, err := router.LoadConfig(disabledLegacyPath)
	if err != nil || len(disabledCfg.Callers) != 0 {
		t.Fatalf("disabled caller was not removed: cfg=%+v err=%v", disabledCfg, err)
	}
	assertV48CallerValidation(t, disabledCfg)

	// A mixed v4-to-v5 migration must remain readable by v4.8 after active
	// known tokens are migrated, an unknown active token is rotated, and the
	// disabled legacy caller is explicitly removed from the same file.
	mixedConfigPath := filepath.Join(dir, "mixed-callers.yaml")
	mixedCfg := *migratedCfg
	knownCaller := migratedCfg.Callers[0]
	knownCaller.ID = "active-known"
	rotatedCaller := freeRotatedCfg.Callers[0]
	rotatedCaller.ID = "active-rotated"
	disabledCaller := disabledConfig.Callers[0]
	disabledCaller.ID = "disabled-removed"
	mixedCfg.Callers = []router.CallerConfig{knownCaller, rotatedCaller, disabledCaller}
	mixedCfg.Security.KeyHygiene = router.KeyHygieneConfig{}
	mixedYAML, err := yaml.Marshal(&mixedCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mixedConfigPath, mixedYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("go", "run", ".", "callers", "migrate-legacy", "--config", mixedConfigPath, "--id", "disabled-removed", "--remove-disabled").CombinedOutput()
	if err != nil {
		t.Fatalf("remove disabled caller from mixed config: %v: %s", err, out)
	}
	mixedMigratedCfg, err := router.LoadConfig(mixedConfigPath)
	if err != nil || len(mixedMigratedCfg.Callers) != 2 {
		t.Fatalf("load mixed migrated config: callers=%d err=%v", len(mixedMigratedCfg.Callers), err)
	}
	assertV48CallerValidation(t, mixedMigratedCfg)

	revokedLegacyPath := filepath.Join(dir, "revoked-legacy.yaml")
	disabledConfig.Callers[0].Status = "active"
	disabledConfig.Callers[0].RevokedTokenHash = hex.EncodeToString(legacyDigest[:])
	disabledConfig.Callers[0].TokenSHA256 = hex.EncodeToString(legacyDigest[:])
	revokedYAML, err := yaml.Marshal(&disabledConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(revokedLegacyPath, revokedYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	revokedRotatePath := filepath.Join(dir, "revoked-rotate.yaml")
	if err := os.WriteFile(revokedRotatePath, revokedYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	rotatedRevokedToken := filepath.Join(dir, "revoked-rotated.token")
	out, err = exec.Command("go", "run", ".", "callers", "rotate", "--config", revokedRotatePath, "--id", "op-local-dev", "--token-out", rotatedRevokedToken).CombinedOutput()
	if err != nil {
		t.Fatalf("rotate revoked legacy caller: %v: %s", err, out)
	}
	revokedCfg, err := router.LoadConfig(revokedRotatePath)
	if err != nil || revokedCfg.Callers[0].Status != "disabled" || revokedCfg.Callers[0].RevokedTokenHash != hex.EncodeToString(legacyDigest[:]) || revokedCfg.Callers[0].TokenHash == "" {
		t.Fatalf("rotation re-enabled revoked caller or lost its marker: cfg=%+v err=%v", revokedCfg, err)
	}
	assertV48CallerValidation(t, revokedCfg)
	out, err = exec.Command("go", "run", ".", "callers", "migrate-legacy", "--config", revokedLegacyPath, "--id", "op-local-dev", "--remove-disabled").CombinedOutput()
	if err != nil {
		t.Fatalf("remove revoked legacy caller: %v: %s", err, out)
	}
	removedRevokedCfg, err := router.LoadConfig(revokedLegacyPath)
	if err != nil || len(removedRevokedCfg.Callers) != 0 {
		t.Fatalf("revoked caller was not removed: cfg=%+v err=%v", removedRevokedCfg, err)
	}
	assertV48CallerValidation(t, removedRevokedCfg)

	// A token without the <token_id>_<secret> shape cannot be found by
	// token_hash lookup, so hash refuses it instead of locking the caller out.
	freeForm := filepath.Join(dir, "freeform.token")
	if err := os.WriteFile(freeForm, []byte("legacy-free-form-caller-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refuse := exec.Command("go", "run", ".", "callers", "hash", "--token-file", freeForm)
	out, err = refuse.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 || !strings.Contains(string(out), "callers rotate") {
		t.Fatalf("hash of a non-prefixed token must refuse with exit 1: %v: %s", err, out)
	}
	if strings.Contains(string(out), "legacy-free-form-caller-secret-value") || strings.Contains(string(out), "pbkdf2-sha3-512:") {
		t.Fatalf("refusal must not echo the token or emit a hash: %s", out)
	}
	if out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--token-hash", hashed.TokenHash).CombinedOutput(); err != nil {
		t.Fatalf("verify --token-hash: %v: %s", err, out)
	}
	if out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--token-hash", "pbkdf2-sha3-512:i=1:x:y").CombinedOutput(); err == nil || !strings.Contains(string(out), "invalid token_hash") {
		t.Fatalf("malformed token_hash accepted: %v: %s", err, out)
	}

	// verify --config passes only when the router would authenticate: the
	// configured token_id must prefix the token even if the hash matches.
	mismatchCfg := filepath.Join(dir, "mismatch.yaml")
	merged := string(mustRead(t, cfgPath))
	tokenID := strings.Join(strings.Split(strings.TrimSpace(string(mustRead(t, tokenPath))), "_")[:6], "_")
	if !strings.Contains(merged, "token_id: "+tokenID) {
		t.Fatalf("merged config missing token_id %s", tokenID)
	}
	if err := os.WriteFile(mismatchCfg, []byte(strings.Replace(merged, "token_id: "+tokenID, "token_id: rtr_metrum_someone_else_dev_k1", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--config", mismatchCfg, "--id", "op-local-dev").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "token does not start with rtr_metrum_someone_else_dev_k1_") || !strings.Contains(string(out), `"match": false`) {
		t.Fatalf("verify accepted a token the router cannot find: %v: %s", err, out)
	}
	if strings.Contains(string(out), strings.TrimSpace(string(mustRead(t, tokenPath)))) {
		t.Fatal("verify must not echo the raw token")
	}

	// rotate writes a fresh token_hash; the old token no longer verifies.
	rotatedPath := filepath.Join(dir, "rotated.token")
	if out, err := exec.Command("go", "run", ".", "callers", "rotate", "--config", cfgPath, "--id", "op-local-dev", "--token-out", rotatedPath).CombinedOutput(); err != nil || !strings.Contains(string(out), `"token_hash_format": "pbkdf2-sha3-512"`) {
		t.Fatalf("rotate: %v: %s", err, out)
	}
	if out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", rotatedPath, "--config", cfgPath, "--id", "op-local-dev").CombinedOutput(); err != nil {
		t.Fatalf("verify rotated token: %v: %s", err, out)
	}
	if out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", tokenPath, "--config", cfgPath, "--id", "op-local-dev").CombinedOutput(); err == nil {
		t.Fatalf("old token still verifies after rotate: %s", out)
	}
}

func TestCallerGenerateRejectsRemovedHashFormat(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "caller.token")
	for _, hashFormat := range []string{"legacy-sha256", "md5"} {
		out, err := exec.Command("go", "run", ".", "callers", "generate", "--owner-user", "op", "--project", "local", "--allow", "default",
			"--token-out", tokenPath, "--hash-format", hashFormat).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "flag provided but not defined: -hash-format") {
			t.Fatalf("removed hash format %q accepted: %v: %s", hashFormat, err, out)
		}
	}
}

func TestCustomerCLIFailsClosedForRemoteAuthority(t *testing.T) {
	for _, command := range [][]string{{"deploy"}, {"config", "activate"}, {"keys", "rotate"}} {
		args := append([]string{"run", "."}, command...)
		output, err := exec.Command("go", args...).CombinedOutput()
		if err == nil {
			t.Fatalf("%v did not fail closed: %v %s", command, err, output)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// routerctlSecretSentinel is a fake credential. It is not a valid environment
// variable name, so env-name fields holding it are treated as misconfigured.
const routerctlSecretSentinel = "sk-metrum-sentinel-DO-NOT-LOG-c41d02"

const routerctlRedactionConfig = `server:
  identifiers:
    mode: passthrough
  usage_db:
    migration_policy: auto-safe
  logging:
    path: LOGDIR/requests.jsonl
state_path: LOGDIR/state.json
providers:
  mock:
    base_url: https://svc:SENTINEL@upstream.example.test/v1
    dialect: openai-chat
    api_key: ${ROUTERCTL_TEST_API_KEY}
    api_key_env: SENTINEL
callers:
  - id: alice
    token_hash: pbkdf2-sha3-512:i=10000:c3RhcnR1cC10ZXN0LXNhbHQxNg:w9K-aqgc1Dil4f71Pwhzu4Ui9AhbKY42pZq1ZVnJji5V0164n8bY3SpZYNqRE4L_M_6OZ2ywFpPqtZ1TlQjMtw
    token_id: rtr_test_alice
    allow: [default]
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
        bridges:
          chat_to_responses:
            enabled: true
            stateful_sessions:
              enabled: true
              backend: redis
              redis:
                address: 127.0.0.1:1
                password_env: ${ROUTERCTL_TEST_SECRET}
`

// TestRouterctlNeverPrintsSecretValues feeds a config whose secret env vars,
// api_key_env and base_url userinfo hold a sentinel through the read path
// (providers list), the validating load path (config validate) and the
// write path (providers upsert), and checks the sentinel never
// reaches stdout or stderr.
func TestRouterctlNeverPrintsSecretValues(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "metrum-ai-routerctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build routerctl: %v: %s", err, out)
	}
	configPath := filepath.Join(dir, "config.yaml")
	body := strings.NewReplacer("LOGDIR", dir, "SENTINEL", routerctlSecretSentinel).Replace(routerctlRedactionConfig)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"ROUTERCTL_TEST_SECRET="+routerctlSecretSentinel,
			"ROUTERCTL_TEST_API_KEY="+routerctlSecretSentinel+"-api",
		)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	check := func(label, stdout, stderr string) {
		t.Helper()
		for stream, text := range map[string]string{"stdout": stdout, "stderr": stderr} {
			if strings.Contains(text, routerctlSecretSentinel) {
				t.Fatalf("%s %s leaked the secret sentinel: %s", label, stream, text)
			}
		}
	}

	stdout, stderr, err := run("providers", "list", "--config", configPath)
	check("providers list", stdout, stderr)
	if err != nil || !strings.Contains(stdout, `"api_key_env": "[redacted`) || !strings.Contains(stdout, "upstream.example.test") {
		t.Fatalf("providers list: %v stdout=%s stderr=%s", err, stdout, stderr)
	}

	stdout, stderr, err = run("config", "validate", "--config", configPath)
	check("config validate", stdout, stderr)
	if err == nil || !strings.Contains(stderr, "password_env") || !strings.Contains(stderr, "$ROUTERCTL_TEST_SECRET") {
		t.Fatalf("config validate: %v stdout=%s stderr=%s", err, stdout, stderr)
	}

	// Upserting an existing provider without --base-url keeps the stored
	// base_url (with userinfo) and echoes it; the credential must be stripped.
	stdout, stderr, err = run("providers", "upsert", "--config", configPath, "--name", "mock", "--model-ref", "extra")
	check("providers upsert", stdout, stderr)
	if err != nil || !strings.Contains(stdout, "https://redacted@upstream.example.test/v1") {
		t.Fatalf("providers upsert: %v stdout=%s stderr=%s", err, stdout, stderr)
	}
	// A literal credential can also be quoted (truncated) by a YAML type
	// error elsewhere in the document. Raw read commands must scrub it too.
	literal := "FAKE-SECRET-literal-routerctl-credential"
	bad := "providers:\n  mock:\n    api_key_env: " + literal + "\nserver:\n  admin_reports:\n    max_rows: " + literal + "\n"
	if err := os.WriteFile(configPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = run("providers", "list", "--config", configPath)
	if err == nil || !strings.Contains(stderr, "parse config") || !strings.Contains(stderr, "[redacted:") {
		t.Fatalf("literal type error: %v stdout=%s stderr=%s", err, stdout, stderr)
	}
	for _, text := range []string{stdout, stderr} {
		if strings.Contains(text, literal) || strings.Contains(text, literal[:7]) {
			t.Fatalf("literal type error leaked credential bytes: %s", text)
		}
	}
}

func TestRedactURLCredentialsAndEnvReference(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "",
		"http://127.0.0.1:8000/v1":            "http://127.0.0.1:8000/v1",
		"https://user:pw-secret@host.test/v1": "https://redacted@host.test/v1",
		"https://token-secret@host.test/v1":   "https://redacted@host.test/v1",
	} {
		if got := redactURLCredentials(in); got != want {
			t.Errorf("redactURLCredentials(%q)=%q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"":               "",
		"OPENAI_API_KEY": "OPENAI_API_KEY",
		" LOCAL_KEY ":    "LOCAL_KEY",
		"sk-live-abc123": "[redacted: not a valid environment variable name]",
		"9STARTS_DIGIT":  "[redacted: not a valid environment variable name]",
	} {
		if got := redactEnvReference(in); got != want {
			t.Errorf("redactEnvReference(%q)=%q want %q", in, got, want)
		}
	}
}

func TestHoneytokenIssueWritesPrivateTokenWithoutPrintingIt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	tokenPath := filepath.Join(t.TempDir(), "honeytoken")
	output, err := exec.Command("go", "run", ".", "honeytoken", "issue", "--label", "old config example", "--owner-user", "security", "--project", "decoys", "--token-out", tokenPath).CombinedOutput()
	if err != nil {
		t.Fatalf("issue: %v: %s", err, output)
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil || !strings.HasPrefix(string(raw), "rtr_metrum_security_decoys_dev_honeytoken_") {
		t.Fatalf("token file: %v", err)
	}
	info, err := os.Stat(tokenPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode=%v err=%v, want 0600", info, err)
	}
	if strings.Contains(string(output), strings.TrimSpace(string(raw))) || !strings.Contains(string(output), "old config example") {
		t.Fatalf("unsafe or incomplete output: %s", output)
	}
	var result struct {
		CallerRecord struct {
			TokenHash   string `json:"token_hash"`
			TokenSHA256 string `json:"token_sha256"`
		} `json:"caller_record"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("issue output is not JSON: %v: %s", err, output)
	}
	if result.CallerRecord.TokenHash == "" || result.CallerRecord.TokenSHA256 != "" {
		t.Fatalf("issue output must include only the verifier record: %+v", result.CallerRecord)
	}
}

func TestHoneytokenIssueUsesDatabaseCallerAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	const token = "rtr_metrum_security_decoys_prod_honeytoken_dbsecret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/v1/callers/issue" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer admin-test" || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("missing admin authorization or idempotency key")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode issue request: %v", err)
		}
		if request["honeytoken_label"] != "db planted key" {
			t.Errorf("request omitted honeytoken metadata: %+v", request)
		}
		allow, _ := request["allow"].([]any)
		if len(allow) != 1 || allow[0] != "canary" {
			t.Errorf("database issue omitted canary group grants: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"caller":{"id":"security-decoys-prod","token_hash":"pbkdf2-sha3-512$1$ab$cd","honeytoken_label":"db planted key"},"token":%q,"token_id":"rtr_metrum_security_decoys_prod_honeytoken"}`, token)
	}))
	defer server.Close()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "admin.key")
	if err := os.WriteFile(keyFile, []byte("admin-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "honeytoken")
	output, err := exec.Command("go", "run", ".", "honeytoken", "issue", "--label", "db planted key", "--owner-user", "security", "--project", "decoys", "--env", "prod", "--canary-groups", "canary", "--token-out", tokenPath, "--router-url", server.URL, "--admin-key-file", keyFile).CombinedOutput()
	if err != nil {
		t.Fatalf("database issue: %v: %s", err, output)
	}
	contents, err := os.ReadFile(tokenPath)
	if err != nil || string(contents) != token+"\n" {
		t.Fatalf("database token file=%q err=%v", contents, err)
	}
	if strings.Contains(string(output), token) || !strings.Contains(string(output), "token_hash") {
		t.Fatalf("unsafe or incomplete database issue output: %s", output)
	}
}

func TestHoneytokenReportUsesDatabaseCallerAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/admin/v1/callers" {
			t.Errorf("unexpected database report request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer admin-test" {
			t.Errorf("missing database report authorization")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"callers": []any{
			map[string]any{"id": "decoy-a", "honeytoken_label": "db planted", "honeytoken_planted_at": "2026-11-16T00:00:00Z", "honeytoken_last_seen": "2026-11-16T01:00:00Z"},
			map[string]any{"id": "real-caller"},
		}})
	}))
	defer server.Close()
	t.Setenv("ROUTER_ADMIN_KEY", "admin-test")
	output, err := exec.Command("go", "run", ".", "honeytoken", "report", "--router-url", server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("database report: %v: %s", err, output)
	}
	var result struct {
		Schema string `json:"schema"`
		Data   []struct {
			CallerID  string `json:"caller_id"`
			Label     string `json:"label"`
			PlantedAt string `json:"planted_at"`
			LastSeen  string `json:"last_seen"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("database report output is not JSON: %v: %s", err, output)
	}
	if result.Schema != "metrum.ai/router-honeytoken-report/v1" || len(result.Data) != 1 || result.Data[0].CallerID != "decoy-a" || result.Data[0].Label != "db planted" || result.Data[0].LastSeen == "" {
		t.Fatalf("incomplete database honeytoken report: %+v", result)
	}
}

func TestHoneytokenIssueDoesNotWriteTokenWhenConfigMergeFails(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	configPath := filepath.Join(t.TempDir(), "router.yaml")
	config := `server:
  listen: ':8080'
  default_model_group: default
models:
  canary:
    strategy: static
    canary:
      callers: [security-decoys-prod]
callers:
  - id: security-decoys-prod
    owner_user: security
    project: decoys
    environment: prod
    token_sha256: ` + strings.Repeat("a", 64) + `
    token_id: rtr_metrum_security_decoys_prod_existing
    allow: [canary]
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "honeytoken")
	output, err := exec.Command("go", "run", ".", "honeytoken", "issue", "--label", "config merge failure", "--owner-user", "security", "--project", "decoys", "--env", "prod", "--canary-groups", "canary", "--config", configPath, "--write", "--token-out", tokenPath).CombinedOutput()
	if err == nil {
		t.Fatalf("duplicate caller merge unexpectedly succeeded: %s", output)
	}
	if _, statErr := os.Stat(tokenPath); !os.IsNotExist(statErr) {
		t.Fatalf("token output exists after failed config merge (err=%v): %s", statErr, output)
	}
}
