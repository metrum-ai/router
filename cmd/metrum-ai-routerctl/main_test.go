// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
			body = []byte(strings.NewReplacer(
				"REPLACE_WITH_SHA256_HEX_OF_STANDARD_ROUTER_TOKEN", strings.Repeat("a", 64),
				"REPLACE_WITH_SHA256_HEX_OF_CODING_ROUTER_TOKEN", strings.Repeat("b", 64),
				"REPLACE_WITH_SHA256_HEX_OF_METRICS_ADMIN_ROUTER_TOKEN", strings.Repeat("c", 64),
				"REPLACE_WITH_SHA256_HEX_OF_CONTENT_ADMIN_ROUTER_TOKEN", strings.Repeat("d", 64),
			).Replace(string(body)))
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
  license: {enabled: false}
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
	if !strings.Contains(string(raw), "op-local-dev") || !strings.Contains(string(raw), "token_sha256") {
		t.Fatalf("caller not merged: %s", raw)
	}
	if strings.Contains(string(output), strings.TrimSpace(string(mustRead(t, tokenPath)))) {
		t.Fatal("raw token must not appear in CLI output")
	}
}

func TestCustomerCLIFailsClosedForFleetAuthority(t *testing.T) {
	for _, command := range [][]string{{"deploy"}, {"config", "activate"}, {"license", "sign"}, {"keys", "rotate"}} {
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
