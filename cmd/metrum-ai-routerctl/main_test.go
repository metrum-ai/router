// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
)

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

	// A token without the <token_id>_<secret> shape cannot be found by
	// token_hash lookup, so hash refuses it instead of locking the caller out.
	freeForm := filepath.Join(dir, "freeform.token")
	if err := os.WriteFile(freeForm, []byte("legacy-free-form-caller-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refuse := exec.Command("go", "run", ".", "callers", "hash", "--token-file", freeForm)
	out, err = refuse.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 || !strings.Contains(string(out), "callers rotate") || !strings.Contains(string(out), "#281") {
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

func TestCallerGenerateLegacyHashFormat(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "caller.token")
	out, err := exec.Command("go", "run", ".", "callers", "generate", "--owner-user", "op", "--project", "local", "--allow", "default",
		"--token-out", tokenPath, "--hash-format", "legacy-sha256").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"token_hash_format": "legacy-sha256"`) {
		t.Fatalf("legacy generate: %v: %s", err, out)
	}
	if out, err := exec.Command("go", "run", ".", "callers", "generate", "--owner-user", "op", "--project", "local", "--allow", "default",
		"--token-out", filepath.Join(dir, "other.token"), "--hash-format", "md5").CombinedOutput(); err == nil {
		t.Fatalf("unknown hash format accepted: %s", out)
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
    token_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
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
