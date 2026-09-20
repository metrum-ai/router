// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
	"github.com/metrum-ai/router/internal/smartrouterctl"
)

func TestRenderBlueprintNvidiaLLMDCompat(t *testing.T) {
	intentPath := filepath.Join("..", "..", "deploy", "kubernetes", "intents", "shadeform-nvidia-llmd-compat.example.yaml")
	intent, err := smartrouterctl.LoadIntent(intentPath)
	if err != nil {
		t.Fatalf("load intent: %v", err)
	}
	out := t.TempDir()
	result, err := smartrouterctl.RenderBlueprint(intent, out)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result.ServingModelCount != 1 {
		t.Fatalf("serving models=%d, want 1", result.ServingModelCount)
	}
	cfgRaw, err := os.ReadFile(filepath.Join(out, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfgText := string(cfgRaw)
	for _, needle := range []string{"local-llmd-chat", "svc.cluster.local", "LOCAL_VLLM_API_KEY"} {
		if !strings.Contains(cfgText, needle) {
			t.Fatalf("config missing %q", needle)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "overlays", "nvidia-llmd-compat", "llm-d", "helm-values.generated.yaml")); err != nil {
		t.Fatal(err)
	}
	dep, err := os.ReadFile(filepath.Join(out, "overlays", "nvidia-llmd-compat", "serving", "vllm-llmd-backend-deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dep), "app: \"vllm-llmd-backend\"") {
		t.Fatalf("model server missing llm-d match label: %s", dep)
	}
	if _, err := os.Stat(filepath.Join(out, "overlays", "nvidia-llmd-compat", "serving", "llm-d-frontend-deployment.yaml")); err == nil {
		t.Fatal("llm-d frontend must not emit a vLLM deployment")
	}
}

func TestRenderBlueprintNvidiaLocalServing(t *testing.T) {
	intentPath := filepath.Join("..", "..", "deploy", "kubernetes", "intents", "shadeform-nvidia-local-models.example.yaml")
	intent, err := smartrouterctl.LoadIntent(intentPath)
	if err != nil {
		t.Fatalf("load intent: %v", err)
	}
	out := t.TempDir()
	result, err := smartrouterctl.RenderBlueprint(intent, out)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result.ServingModelCount < 3 {
		t.Fatalf("serving models=%d, want >=3", result.ServingModelCount)
	}
	if result.KVCacheEnabled {
		t.Fatal("kv cache must be disabled by default")
	}
	if result.RouterRequestsGPU {
		t.Fatal("router must not request GPUs")
	}
	if !result.HelmChartEmitted {
		t.Fatal("helm chart must be emitted by default")
	}
	if !result.OperatorEmitted {
		t.Fatal("operator scaffold expected when packaging.operator is true")
	}
	if _, err := os.Stat(filepath.Join(out, "charts", "smart-llmrouter", "Chart.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "operator", "crds", "smartrouter.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"architecture.md",
		filepath.Join("charts", "smart-llmrouter", "Chart.yaml"),
		filepath.Join("charts", "smart-llmrouter", "templates", "NOTES.txt"),
		filepath.Join("operator", "README.md"),
	} {
		raw, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "Metrum AI Router") {
			t.Fatalf("%s missing canonical product name: %s", rel, raw)
		}
	}
	chartDeployment, err := os.ReadFile(filepath.Join(out, "charts", "smart-llmrouter", "templates", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chartDeployment), "{{- if .Values.config.existingSecretKey }}") {
		t.Fatal("chart must support mounting generated config.yaml from the runtime Secret")
	}
	valuesChart, err := os.ReadFile(filepath.Join(out, "charts", "smart-llmrouter", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(valuesChart), "nvidia-compatible") {
		t.Fatalf("chart values missing nvidia-compatible gpuProfile: %s", valuesChart)
	}
	if result.GPUOperator != "v26.7.0" {
		t.Fatalf("gpu operator=%q", result.GPUOperator)
	}
	cfgRaw, err := os.ReadFile(filepath.Join(out, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfgText := string(cfgRaw)
	for _, needle := range []string{
		"svc.cluster.local",
		"local-tiny",
		"local-small-chat",
		"local-small-coder",
		"LOCAL_VLLM_API_KEY",
		"${LOCAL_VLLM_API_KEY}",
	} {
		if !strings.Contains(cfgText, needle) {
			t.Fatalf("config missing %q", needle)
		}
	}
	for _, cloud := range []string{"openrouter.ai", "api.openai.com", "api.anthropic.com"} {
		if strings.Contains(cfgText, cloud) {
			t.Fatalf("config must not reference cloud upstream %q", cloud)
		}
	}
	dep, err := os.ReadFile(filepath.Join(out, "overlays", "nvidia-local-serving", "serving", "vllm-tiny-deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dep), "nvidia.com/gpu") {
		t.Fatal("serving deployment must request nvidia.com/gpu")
	}
	if !strings.Contains(string(dep), "--max-model-len") || !strings.Contains(string(dep), "\"8192\"") {
		t.Fatalf("serving deployment must bound its vLLM context length: %s", dep)
	}
	overlay, err := os.ReadFile(filepath.Join(out, "overlays", "nvidia-local-serving", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(overlay), "../") {
		t.Fatalf("generated serving overlay must not reference files outside its output: %s", overlay)
	}
	if !strings.Contains(string(overlay), "networkpolicy-patch.yaml") {
		t.Fatalf("generated serving overlay must include its local-only network policy: %s", overlay)
	}
	arch, err := os.ReadFile(filepath.Join(out, "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arch), "LMCache / Mooncake") || !strings.Contains(string(arch), "omitted") {
		t.Fatalf("architecture.md should mark KV cache omitted: %s", arch)
	}
	values, err := os.ReadFile(filepath.Join(out, "overlays", "nvidia-local-serving", "gpu-operator-values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "driver:") {
		t.Fatal("gpu operator values missing driver block")
	}
	if !strings.Contains(string(values), "enabled: false") {
		t.Fatalf("L40S example sets driver_owned_by_ami; rendered values must disable driver/toolkit: %s", values)
	}
}

func TestSQLiteBackupRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	if err := smartrouterctl.OpenEmptySQLite(dbPath); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgBody := `
server:
  listen: ":8080"
  usage_db:
    enabled: true
    driver: sqlite
    path: ` + dbPath + `
    migration_policy: auto-safe
  license:
    enabled: false
providers:
  local:
    base_url: http://127.0.0.1:8000/v1
    dialect: openai-chat
    api_key: "${LOCAL_VLLM_API_KEY}"
    api_key_env: LOCAL_VLLM_API_KEY
    models:
      chat:
        model: local-chat
models:
  local-chat:
    strategy: static
    targets:
      - provider: local
        model_ref: chat
        weight: 100
users:
  - {id: op, name: op, type: service_account, status: active}
projects:
  - {id: local, name: local, status: active}
project_memberships:
  - {user_id: op, project: local, role: member, status: active}
callers:
  - id: op-local-dev
    owner_user: op
    project: local
    environment: dev
    status: active
    token_sha256: ` + strings.Repeat("a", 64) + `
    token_id: rtr_metrum_op_local_dev_k1
    allow: [local-chat]
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := smartrouterctl.LoadConfigRaw(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "usage-backup.sqlite")
	if _, err := smartrouterctl.BackupSQLiteUsage(cfg, backup, false); err == nil {
		t.Fatal("backup without confirm must fail")
	}
	if _, err := smartrouterctl.BackupSQLiteUsage(cfg, backup, true); err != nil {
		t.Fatalf("backup: %v", err)
	}
	_ = os.Remove(dbPath)
	if _, err := smartrouterctl.RestoreSQLiteUsage(cfg, backup, true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal(err)
	}
}

func TestFileOwnedProviderAndModelUpsert(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `
server:
  listen: ":8080"
  usage_db:
    enabled: true
    driver: sqlite
    path: /tmp/unused.sqlite
    migration_policy: auto-safe
  license:
    enabled: false
providers:
  seed:
    base_url: http://seed.svc:8000/v1
    dialect: openai-chat
    api_key: "${SEED_KEY}"
    api_key_env: SEED_KEY
    models:
      m1:
        model: m1
models:
  g1:
    strategy: static
    targets:
      - provider: seed
        model_ref: m1
        weight: 100
users: [{id: op, name: op, type: service_account, status: active}]
projects: [{id: local, name: local, status: active}]
project_memberships: [{user_id: op, project: local, role: member, status: active}]
callers:
  - id: op-local-dev
    owner_user: op
    project: local
    environment: dev
    status: active
    token_sha256: ` + strings.Repeat("b", 64) + `
    token_id: rtr_metrum_op_local_dev_k1
    allow: [g1]
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := smartrouterctl.LoadConfigRaw(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := smartrouterctl.UpsertProvider(cfg, "vllm-a", router.ProviderConfig{
		BaseURL: "http://vllm-a.smart-llmrouter.svc.cluster.local:8000/v1",
		Dialect: "openai-chat", APIKey: "${LOCAL_VLLM_API_KEY}", APIKeyEnv: "LOCAL_VLLM_API_KEY",
		Models: map[string]router.ProviderModel{"chat": {Model: "local-chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := smartrouterctl.UpsertModelGroup(cfg, "local-a", "static", nil); err == nil {
		t.Fatal("empty targets must fail")
	}
	if err := smartrouterctl.UpsertModelGroup(cfg, "local-a", "static", []router.Target{
		{Provider: "vllm-a", ModelRef: "chat", Weight: 100},
	}); err != nil {
		t.Fatal(err)
	}
	backup, err := smartrouterctl.WriteConfigAtomic(cfgPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("expected sibling backup")
	}
	reloaded, err := smartrouterctl.LoadConfigRaw(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Provider["vllm-a"].APIKey != "${LOCAL_VLLM_API_KEY}" {
		t.Fatalf("api_key placeholder lost: %q", reloaded.Provider["vllm-a"].APIKey)
	}
	if _, ok := reloaded.Models["local-a"]; !ok {
		t.Fatal("model group not written")
	}
}
