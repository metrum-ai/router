// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultHTTPServerTimeoutsPositive(t *testing.T) {
	read, write, idle := defaultHTTPServerTimeouts()
	if read <= 0 || write <= 0 || idle <= 0 {
		t.Fatalf("timeouts must be positive: read=%v write=%v idle=%v", read, write, idle)
	}
}

// startupSecretSentinel is a fake credential. It is intentionally not a valid
// environment variable name so env-name validation paths would quote it.
const startupSecretSentinel = "sk-metrum-sentinel-DO-NOT-LOG-5e1b77"

const startupBaseConfig = `server:
  listen: 127.0.0.1:0
  identifiers:
    mode: passthrough
  cache:
    enabled: true
  usage_db:
    migration_policy: auto-safe
  logging:
    path: LOGDIR/requests.jsonl
state_path: LOGDIR/state.json
providers:
  mock:
    base_url: http://127.0.0.1:1/v1
    dialect: openai-chat
    api_key: ${ROUTER_MAIN_TEST_API_KEY}
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
`

const startupRedisBridge = `        bridges:
          chat_to_responses:
            enabled: true
            stateful_sessions:
              enabled: true
              backend: redis
              redis:
                address: 127.0.0.1:1
                connect_timeout_ms: 200
                username_env: ROUTER_MAIN_TEST_REDIS_USER
                password_env: PASSWORD_ENV_VALUE
`

// TestStartupErrorsNeverPrintSecretValues runs the router binary against
// configs whose secret env vars hold a sentinel and fail at load time and at
// initialization, and checks the sentinel never reaches stdout or stderr.
func TestStartupErrorsNeverPrintSecretValues(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the router binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "metrum-ai-router")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build router: %v: %s", err, out)
	}
	cases := []struct {
		name, config, want string
	}{
		{
			name:   "load error from expanded env in non-string field",
			config: strings.Replace(startupBaseConfig, "  cache:\n    enabled: true\n", "  cache:\n    enabled: true\n    max_bytes: ${ROUTER_MAIN_TEST_SECRET}\n", 1),
			want:   "load config:",
		},
		{
			name:   "load error from credential in password_env",
			config: startupBaseConfig + strings.Replace(startupRedisBridge, "PASSWORD_ENV_VALUE", "${ROUTER_MAIN_TEST_SECRET}", 1),
			want:   "password_env",
		},
		{
			name:   "init error from unreachable redis with secret credentials",
			config: startupBaseConfig + strings.Replace(startupRedisBridge, "PASSWORD_ENV_VALUE", "ROUTER_MAIN_TEST_SECRET", 1),
			want:   "initialize router:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := t.TempDir()
			configPath := filepath.Join(caseDir, "config.yaml")
			body := strings.ReplaceAll(tc.config, "LOGDIR", caseDir)
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "-config", configPath)
			cmd.Dir = caseDir
			cmd.Env = append(os.Environ(),
				"ROUTER_MAIN_TEST_SECRET="+startupSecretSentinel,
				"ROUTER_MAIN_TEST_API_KEY="+startupSecretSentinel+"-api",
				"ROUTER_MAIN_TEST_REDIS_USER="+startupSecretSentinel+"-user",
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err == nil {
				t.Fatalf("router started; stderr=%s", stderr.String())
			}
			for label, text := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
				if strings.Contains(text, startupSecretSentinel) {
					t.Fatalf("%s leaked the secret sentinel: %s", label, text)
				}
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr missing %q: %s", tc.want, stderr.String())
			}
		})
	}
}
