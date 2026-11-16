// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/router"
	"gopkg.in/yaml.v3"
)

func TestSecurityDoctorConfigFindings(t *testing.T) {
	cfg := &router.Config{
		Admin:   router.AdminPlaneConfig{Enabled: true, Listen: "0.0.0.0:8081", TLS: router.AdminTLSConfig{ClientAuth: "optional"}},
		Callers: []router.CallerConfig{{ID: "caller", TokenSHA256: "verifier"}},
		Models:  map[string]router.ModelGroup{"medical": {Targets: []router.Target{{Tags: []string{"sensitive"}}}}},
	}
	findings := securityDoctorConfigFindings(cfg)
	want := map[string]string{"serving-tls": "pass", "provider-key-source": "pass", "provider-spend-ceiling": "pass", "admin-listener": "fail", "admin-plaintext": "pass", "security-reports": "warn", "caller-limits": "fail", "caller-key-format": "warn", "caller-key-environment": "pass", "caller-key-age": "warn", "sensitive-group-dlp": "fail"}
	for _, f := range findings {
		if status, ok := want[f.Check]; ok {
			if f.Status != status {
				t.Errorf("%s status = %s, want %s", f.Check, f.Status, status)
			}
			delete(want, f.Check)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing checks: %v", want)
	}
}

func TestSecurityDoctorEngineAdvisoryResults(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "router.yaml")
	image := "ghcr.io/vllm-project/vllm-openai:v0.6.6"
	if err := os.WriteFile(config, []byte("runtime:\n  image: "+image+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshot := func(t *testing.T, snapshot engineAdvisorySnapshot) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "advisories.json")
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := engineAdvisorySnapshot{Schema: engineAdvisorySchema, CheckedImages: 1, Images: []engineAdvisoryImage{{Image: image, Engine: "vllm", Package: "vllm", Version: "0.6.6"}}}
	for _, tc := range []struct {
		name       string
		snapshot   engineAdvisorySnapshot
		wantStatus string
		wantDetail string
	}{
		{name: "clean", snapshot: base, wantStatus: "pass", wantDetail: "match the advisory snapshot"},
		{name: "unexcepted", snapshot: engineAdvisorySnapshot{Schema: engineAdvisorySchema, CheckedImages: 1, Advisories: 1, Unexcepted: 1, Images: base.Images, Findings: []engineAdvisoryRecord{{Image: image, Advisory: "GHSA-test"}}}, wantStatus: "fail", wantDetail: "unexcepted"},
		{name: "scoped exception", snapshot: engineAdvisorySnapshot{Schema: engineAdvisorySchema, CheckedImages: 1, Advisories: 1, Images: base.Images, Findings: []engineAdvisoryRecord{{Image: image, Advisory: "GHSA-test", Exception: "reviewed"}}}, wantStatus: "warn", wantDetail: "scoped exceptions"},
		{name: "inconsistent snapshot counts", snapshot: engineAdvisorySnapshot{Schema: engineAdvisorySchema, CheckedImages: 1, Advisories: 1, Images: base.Images, Findings: []engineAdvisoryRecord{{Image: image, Advisory: "GHSA-test"}}}, wantStatus: "fail", wantDetail: "invalid schema"},
		{name: "pin absent", snapshot: engineAdvisorySnapshot{Schema: engineAdvisorySchema, CheckedImages: 1, Images: []engineAdvisoryImage{{Image: "other/vllm:1.2.3", Engine: "vllm", Package: "vllm", Version: "1.2.3"}}}, wantStatus: "warn", wantDetail: "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := engineAdvisoryFinding(config, writeSnapshot(t, tc.snapshot))
			if finding.Status != tc.wantStatus || !strings.Contains(finding.Detail, tc.wantDetail) {
				t.Fatalf("finding=%+v, want %s containing %q", finding, tc.wantStatus, tc.wantDetail)
			}
		})
	}
	defaultedConfig := filepath.Join(root, "defaulted.yaml")
	if err := os.WriteFile(defaultedConfig, []byte("runtime:\n  image: ${VLLM_IMAGE:-"+image+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if finding := engineAdvisoryFinding(defaultedConfig, writeSnapshot(t, base)); finding.Status != "pass" {
		t.Fatalf("defaulted engine image finding = %+v, want pass", finding)
	}
}

func TestSecurityDoctorNoSnapshotDoesNotExecuteConfigAncestor(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "operator")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "router.yaml")
	if err := os.WriteFile(config, []byte("runtime:\n  image: vllm/vllm-openai:v0.6.6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "executed")
	scriptDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatal(err)
	}
	script := "import pathlib\npathlib.Path(" + fmt.Sprintf("%q", sentinel) + ").write_text('ran')\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "engine_advisory_gate.py"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	finding := engineAdvisoryFinding(config, "")
	if finding.Status != "not checked" || finding.Severity != "info" {
		t.Fatalf("missing snapshot finding = %+v", finding)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("config ancestor script executed; sentinel stat err=%v", err)
	}
}

func TestSecurityDoctorCallerKeyEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment string
		want        string
	}{
		{name: "development", environment: "dev", want: "warn"},
		{name: "test", environment: "testing", want: "warn"},
		{name: "production", environment: "prod", want: "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &router.Config{Callers: []router.CallerConfig{{ID: "caller", Environment: tc.environment, TokenHash: "verifier"}}}
			for _, finding := range securityDoctorConfigFindings(cfg) {
				if finding.Check == "caller-key-environment" {
					if finding.Status != tc.want {
						t.Fatalf("caller-key-environment status=%s, want %s", finding.Status, tc.want)
					}
					return
				}
			}
			t.Fatal("caller-key-environment finding missing")
		})
	}
}

func TestSecurityDoctorCallerKeyAge(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		issued  string
		want    string
		contain string
	}{
		{name: "fresh", issued: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339), want: "pass", contain: "within the 90-day"},
		{name: "stale", issued: now.Add(-91 * 24 * time.Hour).Format(time.RFC3339), want: "fail", contain: "exceed the 90-day"},
		{name: "missing timestamp", want: "warn", contain: "unknown"},
		{name: "future timestamp", issued: now.Add(time.Hour).Format(time.RFC3339), want: "warn", contain: "future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := callerKeyAgeFinding([]router.CallerConfig{{ID: "caller", TokenHash: "verifier", TokenIssuedAt: tc.issued}}, now, defaultCallerKeyMaxAgeDays)
			if finding.Status != tc.want || !strings.Contains(finding.Detail, tc.contain) {
				t.Fatalf("finding=%+v, want status %q containing %q", finding, tc.want, tc.contain)
			}
		})
	}
}

func TestSecurityDoctorCallerKeyAgeConfigurable(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cfg := &router.Config{
		Callers:  []router.CallerConfig{{ID: "caller", Status: "active", TokenHash: "verifier", TokenIssuedAt: now.Add(-45 * 24 * time.Hour).Format(time.RFC3339)}},
		Security: router.RouterSecurityConfig{KeyHygiene: router.KeyHygieneConfig{MaxAgeDays: 30}},
	}
	findings := securityDoctorConfigFindings(cfg)
	for _, finding := range findings {
		if finding.Check == "caller-key-age" {
			if finding.Status != "fail" || !strings.Contains(finding.Detail, "exceed the 30-day") {
				t.Fatalf("configurable caller age finding=%+v", finding)
			}
			return
		}
	}
	t.Fatal("caller-key-age finding missing")
}

func TestSecurityDoctorCallerKeyFormatStrengthAndInactiveCallers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callers []router.CallerConfig
		want    string
	}{
		{name: "strong v2 active key", callers: []router.CallerConfig{{Status: "active", TokenHash: "hash", TokenFormatVersion: 2, TokenEntropyBits: 192}}, want: "pass"},
		{name: "legacy format", callers: []router.CallerConfig{{Status: "active", TokenSHA256: "hash"}}, want: "warn"},
		{name: "low entropy v2", callers: []router.CallerConfig{{Status: "active", TokenHash: "hash", TokenFormatVersion: 2, TokenEntropyBits: 64}}, want: "warn"},
		{name: "unknown format and entropy", callers: []router.CallerConfig{{Status: "active", TokenHash: "hash"}}, want: "warn"},
		{name: "disabled weak key ignored", callers: []router.CallerConfig{{Status: "disabled", TokenHash: "hash"}}, want: "pass"},
		{name: "revoked weak key ignored", callers: []router.CallerConfig{{Status: "revoked", TokenSHA256: "hash"}}, want: "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			for _, finding := range securityDoctorConfigFindings(&router.Config{Callers: tc.callers}) {
				if finding.Check == "caller-key-format" {
					got = finding.Status
					break
				}
			}
			if got != tc.want {
				t.Fatalf("caller-key-format status=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecurityDoctorMetricsAuthAndDatabaseCallerWarning(t *testing.T) {
	if finding := metricsAuthConfigFinding([]router.CallerConfig{{Status: "disabled", MetricsAdmin: true}}); finding.Status != "warn" {
		t.Fatalf("disabled metrics caller should not count: %+v", finding)
	}
	if finding := metricsAuthConfigFinding([]router.CallerConfig{{Status: "active", MetricsAdmin: true, TokenHash: "verifier"}}); finding.Status != "pass" {
		t.Fatalf("active metrics caller should pass: %+v", finding)
	}
	cfg := &router.Config{
		Server: router.ServerConfig{ConfigSource: router.ConfigSourceConfig{Mode: "database"}},
		Callers: []router.CallerConfig{{
			ID: "bootstrap-only", Status: "active", TokenHash: "verifier", TokenFormatVersion: 2,
			TokenEntropyBits: 256, TokenIssuedAt: time.Now().UTC().Format(time.RFC3339),
			Rate: router.RateConfig{RPM: 10},
		}},
	}
	checks := map[string]string{}
	for _, finding := range securityDoctorConfigFindings(cfg) {
		checks[finding.Check] = finding.Status
	}
	if checks["db-stored-callers"] != "warn" || checks["metrics-auth-config"] != "not checked" ||
		checks["caller-limits"] != "not checked" || checks["caller-key-format"] != "not checked" ||
		checks["caller-key-environment"] != "not checked" || checks["caller-key-age"] != "not checked" {
		t.Fatalf("database-config checks=%v", checks)
	}
}

func TestSecurityDoctorServingTLSAndProviderSettings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		server     router.ServerConfig
		providers  map[string]router.ProviderConfig
		wantChecks map[string]string
	}{
		{
			name:   "public plaintext serving and literal provider key",
			server: router.ServerConfig{Listen: ":8080"},
			providers: map[string]router.ProviderConfig{
				"provider": {APIKey: "placeholder", SpendBreaker: router.ProviderSpendBreakerConfig{}},
			},
			wantChecks: map[string]string{"serving-tls": "warn", "provider-key-source": "warn", "provider-spend-ceiling": "warn"},
		},
		{
			name:   "loopback TLS and secret-backed capped provider",
			server: router.ServerConfig{Listen: "127.0.0.1:8080", TLS: router.ServerTLSConfig{ListenerTLSConfig: router.ListenerTLSConfig{Mode: "router"}}},
			providers: map[string]router.ProviderConfig{
				"provider": {APIKeyEnv: "PROVIDER_KEY", SpendBreaker: router.ProviderSpendBreakerConfig{HourlyUSD: 25}},
			},
			wantChecks: map[string]string{"serving-tls": "pass", "provider-key-source": "pass", "provider-spend-ceiling": "pass"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings := securityDoctorConfigFindings(&router.Config{Server: tc.server, Provider: tc.providers})
			for _, f := range findings {
				if want, ok := tc.wantChecks[f.Check]; ok {
					if f.Status != want {
						t.Errorf("%s status=%s, want %s", f.Check, f.Status, want)
					}
					delete(tc.wantChecks, f.Check)
				}
			}
			if len(tc.wantChecks) != 0 {
				t.Errorf("missing checks: %v", tc.wantChecks)
			}
		})
	}
}

func TestSecurityDoctorPlaintextAdminAndSecurityReports(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config router.AdminPlaneConfig
		status string
	}{
		{name: "plaintext remote admin", config: router.AdminPlaneConfig{Enabled: true, Listen: "0.0.0.0:8081", AllowPlaintextNonLoopback: true}, status: "fail"},
		{name: "loopback plaintext admin", config: router.AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:8081", AllowPlaintextNonLoopback: true}, status: "pass"},
		{name: "remote TLS admin", config: router.AdminPlaneConfig{Enabled: true, Listen: "0.0.0.0:8081", TLS: router.AdminTLSConfig{ListenerTLSConfig: router.ListenerTLSConfig{Mode: "router"}}}, status: "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &router.Config{Admin: tc.config, Server: router.ServerConfig{AdminReports: router.AdminReportsConfig{Enabled: true, Security: router.AdminSecurityReportsConfig{Enabled: true}}}}
			findings := securityDoctorConfigFindings(cfg)
			for _, f := range findings {
				if f.Check == "admin-plaintext" && f.Status != tc.status {
					t.Fatalf("admin-plaintext status=%s, want %s", f.Status, tc.status)
				}
				if f.Check == "security-reports" && f.Status != "pass" {
					t.Fatalf("security-reports status=%s, want pass", f.Status)
				}
			}
		})
	}
}

func TestSecurityDoctorReferenceConfigs(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate reference config fixtures")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	paths := []string{
		"config.example.yaml",
		"deploy/edge/config.template.yaml",
		"deploy/reference/amd-mi350p-datacenter/config.yaml",
		"deploy/reference/amd-strix-halo-edge/config.yaml",
		"deploy/reference/hierarchy/config/dc.yaml",
		"deploy/reference/hierarchy/config/edge.yaml",
		"deploy/reference/nvidia-datacenter/config.yaml",
		"deploy/reference/nvidia-gb10-edge/config.yaml",
		"deploy/kubernetes/overlays/nvidia-local-serving/config.example.yaml",
		"deploy/kubernetes/overlays/admin-plane/config.yaml",
	}
	acceptedWarnings := map[string]bool{
		"serving-tls": true, "provider-key-source": true, "provider-spend-ceiling": true,
		"security-reports": true, "metrics-auth-config": true, "caller-limits": true,
		"caller-key-format": true, "caller-key-environment": true, "caller-key-age": true,
		"engine-pin-advisories": true, "db-stored-callers": true,
	}
	acceptedFailures := map[string]map[string]bool{
		"deploy/kubernetes/overlays/admin-plane/config.yaml": {"admin-listener": true, "admin-plaintext": true},
	}
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(repo, name))
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := normalizeReferenceConfig(raw)
			if err != nil {
				t.Fatalf("parse reference YAML: %v", err)
			}
			normalized, err = addReferenceIdentifierTestKey(normalized)
			if err != nil {
				t.Fatalf("prepare reference config secrets: %v", err)
			}
			setReferencePasswordHashEnvs(t, normalized)
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, normalized, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := router.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("load reference config: %v", err)
			}
			for _, finding := range securityDoctorConfigFindings(cfg) {
				if finding.Status == "fail" && !acceptedFailures[name][finding.Check] {
					t.Errorf("reference config has unaccepted failing finding %s: %s", finding.Check, finding.Detail)
				}
				if finding.Status == "warn" && !acceptedWarnings[finding.Check] {
					t.Errorf("reference config has undocumented warning %s: %s", finding.Check, finding.Detail)
				}
			}
		})
	}
}

func addReferenceIdentifierTestKey(raw []byte) ([]byte, error) {
	var config map[string]any
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	server, _ := config["server"].(map[string]any)
	if server == nil {
		server = map[string]any{}
		config["server"] = server
	}
	identifiers, _ := server["identifiers"].(map[string]any)
	if identifiers == nil {
		identifiers = map[string]any{}
		server["identifiers"] = identifiers
	}
	transform, _ := identifiers["transform"].(map[string]any)
	if transform == nil {
		transform = map[string]any{}
		identifiers["transform"] = transform
	}
	current, _ := transform["current"].(map[string]any)
	if current == nil {
		current = map[string]any{}
		transform["current"] = current
	}
	// Reference examples require a deployment-specific identifier transform key.
	// Substitute a deterministic test-only key so LoadConfig can validate the
	// same config structure without importing deployment secrets.
	current["key_id"] = "reference-doctor-test"
	current["key"] = strings.Repeat("ab", 64)
	if callers, ok := config["callers"].([]any); ok {
		for index, value := range callers {
			caller, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if caller["id"] == "reference-placeholder" || strings.TrimSpace(fmt.Sprint(caller["id"])) == "" {
				caller["id"] = fmt.Sprintf("reference-caller-%d", index)
			}
			if caller["token_hash"] != nil {
				caller["token_hash"] = referenceTokenHash(index)
				caller["token_id"] = fmt.Sprintf("reference-token-%d", index)
			}
		}
	}
	providers, _ := config["providers"].(map[string]any)
	if len(providers) == 0 {
		// The admin-plane overlay documents only the control-plane pod. Add a
		// harmless provider to satisfy the serving router's full-config loader.
		config["providers"] = map[string]any{
			"reference-doctor-test": map[string]any{
				"base_url": "http://127.0.0.1:1/v1",
				"dialect":  "openai-chat",
			},
		}
	}
	models, _ := config["models"].(map[string]any)
	if len(models) == 0 {
		config["models"] = map[string]any{
			"reference-doctor-test": map[string]any{
				"strategy": "static",
				"targets": []any{map[string]any{
					"provider": "reference-doctor-test",
					"model":    "reference-doctor-test",
				}},
			},
		}
		server["default_model_group"] = "reference-doctor-test"
	}
	return yaml.Marshal(config)
}

func setReferencePasswordHashEnvs(t *testing.T, raw []byte) {
	t.Helper()
	var config any
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "password_hash_env" {
					if name, ok := child.(string); ok && name != "" {
						t.Setenv(name, "$2a$10$PEWHfUFcvCRVUkh/hggt6eUYdDlrFr0cfzXvIXtLDKpS5uTtYq65i")
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(config)
}

func referenceTokenHash(index int) string {
	salt := fmt.Sprintf("%s-%d", strings.Repeat("s", 32), index)
	return "pbkdf2-sha3-512:i=20000:" + base64.RawURLEncoding.EncodeToString([]byte(salt)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 64)))
}

var referencePlaceholder = regexp.MustCompile(`\$\{[^}]+\}|@@[A-Z0-9_]+@@|reference-placeholder`)

func normalizeReferenceConfig(raw []byte) ([]byte, error) {
	raw = []byte(regexp.MustCompile(`@@[A-Z0-9_]+@@`).ReplaceAllString(string(raw), `reference-placeholder`))
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	var walk func(*yaml.Node, string)
	walk = func(node *yaml.Node, key string) {
		if node.Kind == yaml.DocumentNode {
			for _, child := range node.Content {
				walk(child, key)
			}
			return
		}
		if node.Kind == yaml.MappingNode {
			for index := 0; index+1 < len(node.Content); index += 2 {
				walk(node.Content[index], key)
				walk(node.Content[index+1], node.Content[index].Value)
			}
			return
		}
		if node.Kind == yaml.SequenceNode {
			for _, child := range node.Content {
				walk(child, key)
			}
			return
		}
		if node.Kind != yaml.ScalarNode {
			return
		}
		if !referencePlaceholder.MatchString(node.Value) {
			return
		}
		if referenceNumericKey(key) {
			node.Value, node.Tag = "1", "!!int"
		} else {
			node.Value, node.Tag = "reference-placeholder", "!!str"
		}
	}
	walk(&document, "")
	return yaml.Marshal(&document)
}

func referenceNumericKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range []string{"_ms", "_rpm", "_tpm", "_usd", "_port", "_bytes", "_tokens", "_requests", "_concurrent", "_workers", "_seconds", "_minutes", "_hours", "_days", "_size", "_count", "_hops"} {
		if strings.HasSuffix(key, marker) {
			return true
		}
	}
	switch key {
	case "rpm", "tpm", "concurrent", "timeout", "port", "requests", "tokens", "limit", "burst", "retries":
		return true
	default:
		return false
	}
}

func TestSecurityDoctorMetricsProbe(t *testing.T) {
	remoteHTTP := probeSecurityTarget("http://203.0.113.12:8080")
	if len(remoteHTTP) != 1 || remoteHTTP[0].Check != "target-url" || remoteHTTP[0].Status != "fail" {
		t.Fatalf("remote plain HTTP target was not refused: %+v", remoteHTTP)
	}
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{name: "public", status: http.StatusOK, want: "fail"},
		{name: "unauthorized", status: http.StatusUnauthorized, want: "pass"},
		{name: "forbidden", status: http.StatusForbidden, want: "pass"},
		{name: "unknown", status: http.StatusNotFound, want: "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/metrics" || r.Method != http.MethodGet {
					t.Errorf("probe = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			got := probeSecurityTarget(server.URL)
			if len(got) != 1 || got[0].Status != tc.want {
				t.Fatalf("probe findings = %+v, want status %s", got, tc.want)
			}
		})
	}
}

func TestSecurityDoctorMetricsProbeFailureAndTimeout(t *testing.T) {
	t.Run("connection failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := server.URL
		server.Close()
		got := probeSecurityTarget(url)
		if len(got) != 1 || got[0].Status != "fail" || !strings.Contains(got[0].Detail, "failed or timed out") {
			t.Fatalf("findings = %+v", got)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		started := time.Now()
		got := probeSecurityTarget(server.URL)
		if len(got) != 1 || got[0].Status != "fail" || time.Since(started) > 4*time.Second {
			t.Fatalf("findings = %+v, elapsed=%s", got, time.Since(started))
		}
	})
}

func TestSecurityDoctorProbesRealRouterMetricsHandler(t *testing.T) {
	config := writeSecurityDoctorConfig(t)
	cfg, err := router.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	findings := probeSecurityTarget(server.URL)
	if len(findings) != 1 || findings[0].Check != "metrics-auth" || findings[0].Status != "pass" {
		t.Fatalf("real router metrics probe = %+v", findings)
	}
}

func TestSecurityDoctorCommandJSONAndThreshold(t *testing.T) {
	config := writeSecurityDoctorConfig(t)
	var stdout, stderr bytes.Buffer
	code := securityDoctorCommand([]string{"--security", "--config", config, "--format", "json", "--fail-on", "fail"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema":"metrum.ai/router-security-posture/v1"`) || !strings.Contains(stdout.String(), `"findings"`) {
		t.Fatalf("unexpected JSON output: %s", stdout.String())
	}
	for _, forbidden := range []string{"placeholder", strings.Repeat("ab", 32)} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("JSON output contains sensitive config material %q", forbidden)
		}
	}
	stdout.Reset()
	code = securityDoctorCommand([]string{"--security", "--config", config, "--fail-on", "warn"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("warn threshold exit=%d output=%q", code, stdout.String())
	}
}

func TestSecurityDoctorReportSubmissionSendsSafeSummary(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "admin-test-secret")
	var gotPath, gotAuthorization, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuthorization = r.URL.Path, r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	report := securityDoctorReport{
		Schema: "metrum.ai/router-security-posture/v1", Generated: "2026-11-16T06:00:00Z", Target: "https://router.example",
		Findings: []securityDoctorFinding{{Check: "admin-listener", Status: "warn", Severity: "medium", Detail: "private target 10.0.0.5", Fix: "rotate secret"}},
	}
	if err := submitSecurityDoctorReport(server.URL, "", "", report); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admin/v1/reports/security/posture/latest" || gotAuthorization != "Bearer admin-test-secret" {
		t.Fatalf("request path=%q authorization=%q", gotPath, gotAuthorization)
	}
	if strings.Contains(gotBody, "private target") || strings.Contains(gotBody, "rotate secret") || strings.Contains(gotBody, "router.example") || strings.Contains(gotBody, "admin-test-secret") {
		t.Fatalf("summary submission included detail or credentials: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"outcome":"warn"`) || !strings.Contains(gotBody, `"check":"admin-listener"`) {
		t.Fatalf("summary omitted safe check data: %s", gotBody)
	}
}

func writeSecurityDoctorConfig(t *testing.T) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "router.yaml")
	raw := fmt.Sprintf("state_path: %q\nserver:\n  identifiers:\n    mode: passthrough\n  usage_db:\n    enabled: true\n    driver: sqlite\n    path: %q\n    migration_policy: auto-safe\nproviders:\n  mock:\n    base_url: http://127.0.0.1:1/v1\n    dialect: openai-chat\n    api_key: placeholder\nmodels:\n  local:\n    strategy: weighted\n    targets:\n      - {provider: mock, model: example, weight: 1}\ncallers:\n  - id: alice\n    token_sha256: %s\n    allow: [local]\n    rate:\n      rpm: 30\n", filepath.Join(filepath.Dir(config), "router-state.json"), filepath.Join(filepath.Dir(config), "usage.db"), strings.Repeat("ab", 32))
	if err := os.WriteFile(config, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return config
}
