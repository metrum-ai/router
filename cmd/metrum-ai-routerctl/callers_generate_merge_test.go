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

// cliCallerConfig is a minimal valid config with one existing caller that must
// survive every later mint.
const cliCallerConfig = `
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
callers:
- id: keep-me
  owner_user: ops
  project: keep
  environment: run
  status: active
  token_hash: pbkdf2-sha3-512:i=1:c2FsdA:a2V5
  token_id: rtr_metrum_keep_me_run_k1
  allow: [local-tiny]
`

func writeCLICallerConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cliCallerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCallersGenerate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	argv := append([]string{"run", ".", "callers", "generate"}, args...)
	cmd := exec.Command("go", argv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCallersGenerateWriteRefusesDuplicateCallerID covers issue #755 at the CLI
// boundary: a second mint with the same derived caller id used to replace the
// first caller, so the earlier token stopped authenticating (401). It must refuse
// loudly, keep the table byte-for-byte, and only replace with --replace.
func TestCallersGenerateWriteRefusesDuplicateCallerID(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cliCallerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	firstToken := filepath.Join(dir, "k1.token")
	out, err := runCallersGenerate(t, "--owner-user", "ops", "--project", "p", "--env", "run", "--key", "k1",
		"--allow", "local-tiny", "--config", cfgPath, "--write", "--token-out", firstToken)
	if err != nil {
		t.Fatalf("first mint: %v: %s", err, out)
	}
	afterFirst, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterFirst), "token_id: rtr_metrum_ops_p_run_k1") {
		t.Fatalf("first mint did not merge: %s", afterFirst)
	}
	if !strings.Contains(string(afterFirst), "id: keep-me") {
		t.Fatalf("first mint dropped an existing caller: %s", afterFirst)
	}

	// Same owner/project/env derives the same caller id, so this mint collides.
	secondToken := filepath.Join(dir, "k2.token")
	out, err = runCallersGenerate(t, "--owner-user", "ops", "--project", "p", "--env", "run", "--key", "k2",
		"--allow", "local-tiny", "--config", cfgPath, "--write", "--token-out", secondToken)
	if err == nil {
		t.Fatalf("duplicate caller id was accepted: %s", out)
	}
	// `go run` reports the child's non-zero status as its own exit 1, so assert a
	// refusal exit rather than a specific code.
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() == 0 {
		t.Fatalf("refusal must exit non-zero: %v %s", err, out)
	}
	if !strings.Contains(out, "ops-p-run") || !strings.Contains(out, "--replace") {
		t.Fatalf("refusal must name the caller and --replace: %s", out)
	}
	afterRefusal, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRefusal) != string(afterFirst) {
		t.Fatalf("refused mint changed the config\nbefore: %s\nafter:  %s", afterFirst, afterRefusal)
	}
	if _, err := os.Stat(secondToken); !os.IsNotExist(err) {
		t.Fatalf("refused mint wrote a token file for a caller that does not exist: %v", err)
	}

	// An explicit --replace may replace that one caller and nothing else.
	thirdToken := filepath.Join(dir, "k3.token")
	out, err = runCallersGenerate(t, "--owner-user", "ops", "--project", "p", "--env", "run", "--key", "k3",
		"--allow", "local-tiny", "--config", cfgPath, "--write", "--replace", "--token-out", thirdToken)
	if err != nil {
		t.Fatalf("mint --replace: %v: %s", err, out)
	}
	afterReplace, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterReplace), "token_id: rtr_metrum_ops_p_run_k3") || strings.Contains(string(afterReplace), "token_id: rtr_metrum_ops_p_run_k1") {
		t.Fatalf("replace did not swap the caller verifier: %s", afterReplace)
	}
	if !strings.Contains(string(afterReplace), "id: keep-me") || !strings.Contains(string(afterReplace), "token_id: rtr_metrum_keep_me_run_k1") {
		t.Fatalf("replace dropped or rewrote another caller: %s", afterReplace)
	}
	if out, err := exec.Command("go", "run", ".", "callers", "verify", "--token-file", thirdToken, "--config", cfgPath, "--id", "ops-p-run").CombinedOutput(); err != nil || !strings.Contains(string(out), `"match": true`) {
		t.Fatalf("replaced caller's token does not verify: %v: %s", err, out)
	}
}

// TestCallersGenerateWriteAppendsDistinctCaller keeps the merge contract: a new
// caller id is added and every existing caller is untouched.
func TestCallersGenerateWriteAppendsDistinctCaller(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cliCallerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "k.token")
	out, err := runCallersGenerate(t, "--owner-user", "ops", "--project", "other", "--env", "run", "--key", "k1",
		"--allow", "local-tiny", "--config", cfgPath, "--write", "--token-out", tokenPath)
	if err != nil {
		t.Fatalf("mint distinct caller: %v: %s", err, out)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id: keep-me", "token_id: rtr_metrum_keep_me_run_k1", "id: ops-other-run", "token_id: rtr_metrum_ops_other_run_k1"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("merged config missing %q: %s", want, raw)
		}
	}
}
