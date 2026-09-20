// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/metrum-ai/router/internal/router"
	"gopkg.in/yaml.v3"
)

// RenderResult summarizes files emitted by blueprint render.
type RenderResult struct {
	Schema            string   `json:"schema"`
	OutDir            string   `json:"out_dir"`
	Files             []string `json:"files"`
	ProviderCount     int      `json:"provider_count"`
	ModelGroupCount   int      `json:"model_group_count"`
	ServingModelCount int      `json:"serving_model_count"`
	GPUOperator       string   `json:"gpu_operator"`
	KVCacheEnabled    bool     `json:"kv_cache_enabled"`
	RouterRequestsGPU bool     `json:"router_requests_gpu"`
	HelmChartEmitted  bool     `json:"helm_chart_emitted"`
	OperatorEmitted   bool     `json:"operator_emitted"`
}

// RenderBlueprint emits architecture.md, config.yaml, overlay snippets, and inventory.
func RenderBlueprint(intent *StackIntent, outDir string) (*RenderResult, error) {
	if intent == nil {
		return nil, fmt.Errorf("intent is required")
	}
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(outDir) == "" {
		return nil, fmt.Errorf("out dir is required")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	overlayName := overlayProfileName(intent.Profile)
	overlayDir := filepath.Join(outDir, "overlays", overlayName)
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return nil, err
	}
	servingDir := filepath.Join(overlayDir, "serving")
	if err := os.MkdirAll(servingDir, 0o755); err != nil {
		return nil, err
	}
	llmdDir := filepath.Join(overlayDir, "llm-d")
	if intent.Profile == "nvidia-llmd-compat" {
		if err := os.MkdirAll(llmdDir, 0o755); err != nil {
			return nil, err
		}
	}

	cfg, err := buildLocalConfig(intent)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(outDir, "config.yaml")
	if err := writeYAML(configPath, cfg); err != nil {
		return nil, err
	}

	archPath := filepath.Join(outDir, "architecture.md")
	if err := writeArchitecture(archPath, intent); err != nil {
		return nil, err
	}
	invPath := filepath.Join(outDir, "inventory.yaml")
	if err := writeInventory(invPath, intent); err != nil {
		return nil, err
	}
	valuesPath := filepath.Join(overlayDir, "gpu-operator-values.yaml")
	if err := writeGPUOperatorValues(valuesPath, intent); err != nil {
		return nil, err
	}
	kustomizePath := filepath.Join(overlayDir, "kustomization.yaml")
	if err := writeOverlayKustomization(kustomizePath, intent); err != nil {
		return nil, err
	}
	npPath := filepath.Join(overlayDir, "networkpolicy-patch.yaml")
	if err := writeNetworkPolicyPatch(npPath, intent); err != nil {
		return nil, err
	}
	for _, model := range intent.ServingModels {
		if strings.ToLower(strings.TrimSpace(model.Backend)) == "llm-d" {
			continue
		}
		if err := writeServingManifests(servingDir, intent.Namespace, model); err != nil {
			return nil, err
		}
	}
	if intent.Profile == "nvidia-llmd-compat" && intent.LLMD != nil {
		servedID := intent.ServingModels[0].ServedModelID
		if err := writeLLMDModelServerManifests(servingDir, intent.Namespace, intent.LLMD.ModelServer, servedID); err != nil {
			return nil, err
		}
		llmdValuesPath := filepath.Join(llmdDir, "helm-values.generated.yaml")
		if err := writeLLMDHelmValues(llmdValuesPath, intent); err != nil {
			return nil, err
		}
	}

	files := []string{
		"architecture.md",
		"inventory.yaml",
		"config.yaml",
		filepath.Join("overlays", overlayName, "kustomization.yaml"),
		filepath.Join("overlays", overlayName, "gpu-operator-values.yaml"),
		filepath.Join("overlays", overlayName, "networkpolicy-patch.yaml"),
	}
	for _, model := range intent.ServingModels {
		if strings.ToLower(strings.TrimSpace(model.Backend)) == "llm-d" {
			continue
		}
		files = append(files,
			filepath.Join("overlays", overlayName, "serving", model.Name+"-deployment.yaml"),
			filepath.Join("overlays", overlayName, "serving", model.Name+"-service.yaml"),
		)
	}
	if intent.Profile == "nvidia-llmd-compat" && intent.LLMD != nil {
		ms := intent.LLMD.ModelServer
		files = append(files,
			filepath.Join("overlays", overlayName, "serving", ms.Name+"-deployment.yaml"),
			filepath.Join("overlays", overlayName, "serving", ms.Name+"-service.yaml"),
			filepath.Join("overlays", overlayName, "llm-d", "helm-values.generated.yaml"),
			filepath.Join("overlays", overlayName, "llm-d", "install.example.sh"),
		)
		installPath := filepath.Join(llmdDir, "install.example.sh")
		if err := writeLLMDInstallScript(installPath, intent); err != nil {
			return nil, err
		}
	}

	helmEmitted := false
	if intent.Packaging.HelmEnabled() {
		helmFiles, err := writeHelmChart(outDir, intent)
		if err != nil {
			return nil, err
		}
		files = append(files, helmFiles...)
		helmEmitted = true
	}
	operatorEmitted := false
	if intent.Packaging.Operator {
		opFiles, err := writeOperatorScaffold(outDir, intent)
		if err != nil {
			return nil, err
		}
		files = append(files, opFiles...)
		operatorEmitted = true
	}
	sort.Strings(files)

	gpuVer := ""
	if intent.GPUOperator.Enabled {
		gpuVer = intent.GPUOperator.Version
	}
	return &RenderResult{
		Schema:            "metrum.ai/smartrouter-blueprint-render/v1",
		OutDir:            outDir,
		Files:             files,
		ProviderCount:     len(cfg.Provider),
		ModelGroupCount:   len(cfg.Models),
		ServingModelCount: len(intent.ServingModels),
		GPUOperator:       gpuVer,
		KVCacheEnabled:    intent.KVCache.Enabled,
		RouterRequestsGPU: false,
		HelmChartEmitted:  helmEmitted,
		OperatorEmitted:   operatorEmitted,
	}, nil
}

func buildLocalConfig(intent *StackIntent) (*router.Config, error) {
	enabled := true
	cfg := &router.Config{
		Server: router.ServerConfig{
			Listen:            ":8080",
			DefaultModelGroup: intent.DefaultModelGroup,
			Logging: router.LoggingConfig{
				Path: "/app/logs/requests.jsonl",
			},
			UsageDB: router.UsageDBConfig{
				Enable:          &enabled,
				Driver:          intent.UsageDriver,
				Path:            "/app/state/usage.sqlite",
				MigrationPolicy: "auto-safe",
			},
			Upstream: router.UpstreamConfig{
				TimeoutMS:        120000,
				MaxResponseBytes: 52428800,
			},
		},
		StatePath: "/app/state/router-state.json",
		Provider:  map[string]router.ProviderConfig{},
		Models:    map[string]router.ModelGroup{},
		Users: []router.UserConfig{{
			ID: "local-operator", Name: "Local Operator", Type: "service_account", Status: "active",
		}},
		Projects: []router.ProjectConfig{{
			ID: "local", Name: "Local", Status: "active",
		}},
		ProjectMemberships: []router.ProjectMembershipConfig{{
			UserID: "local-operator", Project: "local", Role: "member", Status: "active",
		}},
		Callers: []router.CallerConfig{{
			ID:          "local-operator-local-dev",
			OwnerUser:   "local-operator",
			Project:     "local",
			Environment: "dev",
			Status:      "active",
			// Placeholder hash; replace with callers generate --write before serving.
			TokenSHA256: strings.Repeat("0", 64),
			TokenID:     "rtr_metrum_local-operator_local_dev_k1",
			Allow:       append([]string(nil), intent.CallerAllow...),
			Rate:        router.RateConfig{RPM: 60, TPM: 100000, Concurrent: 4},
		}},
	}
	if intent.UsageDriver == "postgres" {
		cfg.Server.UsageDB.Path = ""
		cfg.Server.UsageDB.DSN = "${ROUTER_USAGE_DB_DSN}"
	}
	for _, model := range intent.ServingModels {
		providerName := "local-" + model.Name
		cfg.Provider[providerName] = router.ProviderConfig{
			BaseURL:    model.ServiceDNS,
			Dialect:    "openai-chat",
			APIKey:     "${" + model.APIKeyEnv + "}",
			APIKeyEnv:  model.APIKeyEnv,
			AuthScheme: "bearer",
			Models: map[string]router.ProviderModel{
				model.Name: {
					Model:                    model.ServedModelID,
					InputPricePerMillionUSD:  0,
					OutputPricePerMillionUSD: 0,
					PricingNotes:             "in-cluster GPU allocation; set chargeback values if reports need allocated cost",
					InputModalities:          []string{"text"},
					OutputModalities:         []string{"text"},
				},
			},
		}
		cfg.Models[model.ModelGroup] = router.ModelGroup{
			Strategy: "static",
			Targets: []router.Target{{
				Provider: providerName,
				ModelRef: model.Name,
				Weight:   100,
			}},
		}
	}
	return cfg, nil
}

func overlayProfileName(profile string) string {
	switch strings.TrimSpace(profile) {
	case "nvidia-llmd-compat":
		return "nvidia-llmd-compat"
	default:
		return "nvidia-local-serving"
	}
}

func writeArchitecture(path string, intent *StackIntent) error {
	kv := "omitted (optional; not enabled for this blueprint)"
	if intent.KVCache.Enabled {
		kv = "enabled (operator-owned; not managed by Metrum AI Router)"
	}
	gpuLine := "disabled"
	if intent.GPUOperator.Enabled {
		gpuLine = fmt.Sprintf("NVIDIA GPU Operator %s (driver_owned_by_ami=%v)", intent.GPUOperator.Version, intent.GPUOperator.DriverOwnedByAMI)
	}
	var modelLines strings.Builder
	for _, model := range intent.ServingModels {
		backend := strings.ToLower(strings.TrimSpace(model.Backend))
		if backend == "" {
			backend = "vllm"
		}
		fmt.Fprintf(&modelLines, "- `%s` served as `%s` at `%s` (backend=%s)\n", model.Name, model.ServedModelID, model.ServiceDNS, backend)
	}
	servingLayer := "In-cluster OpenAI-compatible vLLM Deployments"
	routerNotes := "4. Point router providers only at in-cluster Service DNS names from this blueprint."
	if intent.Profile == "nvidia-llmd-compat" && intent.LLMD != nil {
		servingLayer = "llm-d standalone router in front of a labeled vLLM model server (Metrum AI Router does not own llm-d)"
		routerNotes = fmt.Sprintf(`4. Install llm-d with `+"`overlays/nvidia-llmd-compat/llm-d/install.example.sh`"+` after Gateway API Inference Extension CRDs and the model server are Ready.
5. Point router providers only at the llm-d frontend Service DNS (`+"`%s`"+`).`, intent.LLMD.FrontendService)
	}
	body := fmt.Sprintf(`# Metrum AI Router architecture blueprint

**Profile:** %s  
**Namespace:** %s  
**Edge:** %s  
**Usage store:** %s  
**KV cache:** %s  

## Layer boundary

| Layer | Role in this blueprint |
|---|---|
| vLLM Semantic Router | Optional / off for this profile |
| Metrum AI Router | Caller auth, quotas, model-group selection, usage; ClusterIP on port 8080 |
| Serving replicas | %s |
| NVIDIA GPU Operator | %s |
| LMCache / Mooncake | %s |
| llm-d | %s |

The router Pod does **not** request `+"`nvidia.com/gpu`"+`. Serving workloads own accelerator requests.

## Local upstreams

%s
## Operator notes

1. Authenticate to the cluster before apply. Cloud/cluster login is a prerequisite, never a CLI step.
2. Install the GPU Operator using `+"`gpu-operator-values.yaml`"+` when nodes do not already own drivers.
3. Apply the generated serving manifests, then the router overlay with a runtime Secret containing `+"`config.yaml`"+`, `+"`env.json`"+`, and `+"`license.json`"+`.
%s
6. Keep LMCache and Mooncake off unless a later intent enables them.
`, intent.Profile, intent.Namespace, intent.Edge, intent.UsageDriver, kv, servingLayer, gpuLine, kv, llmdLine(intent), modelLines.String(), routerNotes)
	return os.WriteFile(path, []byte(body), 0o644)
}

func writeInventory(path string, intent *StackIntent) error {
	type inventory struct {
		Schema              string   `yaml:"schema"`
		Profile             string   `yaml:"profile"`
		Namespace           string   `yaml:"namespace"`
		RouterImage         string   `yaml:"router_image"`
		GPUOperatorVersion  string   `yaml:"gpu_operator_version,omitempty"`
		GPUOperatorEnabled  bool     `yaml:"gpu_operator_enabled"`
		DriverOwnedByAMI    bool     `yaml:"driver_owned_by_ami"`
		KVCacheEnabled      bool     `yaml:"kv_cache_enabled"`
		UsageDriver         string   `yaml:"usage_driver"`
		Edge                string   `yaml:"edge"`
		ServingModelNames   []string `yaml:"serving_model_names"`
		RouterRequestsGPU   bool     `yaml:"router_requests_gpu"`
		KubernetesFloorNote string   `yaml:"kubernetes_floor_note"`
		HelmChartEmitted    bool     `yaml:"helm_chart_emitted"`
		OperatorEmitted     bool     `yaml:"operator_emitted"`
	}
	names := make([]string, 0, len(intent.ServingModels))
	for _, model := range intent.ServingModels {
		names = append(names, model.Name)
	}
	sort.Strings(names)
	inv := inventory{
		Schema:              "metrum.ai/smartrouter-blueprint-inventory/v1",
		Profile:             intent.Profile,
		Namespace:           intent.Namespace,
		RouterImage:         intent.RouterImage,
		GPUOperatorVersion:  intent.GPUOperator.Version,
		GPUOperatorEnabled:  intent.GPUOperator.Enabled,
		DriverOwnedByAMI:    intent.GPUOperator.DriverOwnedByAMI,
		KVCacheEnabled:      intent.KVCache.Enabled,
		UsageDriver:         intent.UsageDriver,
		Edge:                intent.Edge,
		ServingModelNames:   names,
		RouterRequestsGPU:   false,
		KubernetesFloorNote: "Conservative integrated profile targets Kubernetes 1.35-class clusters; NVIDIA GPU Operator floor is v26.7.0.",
		HelmChartEmitted:    intent.Packaging.HelmEnabled(),
		OperatorEmitted:     intent.Packaging.Operator,
	}
	return writeYAML(path, inv)
}

func writeGPUOperatorValues(path string, intent *StackIntent) error {
	driver := !intent.GPUOperator.DriverOwnedByAMI
	toolkit := !intent.GPUOperator.DriverOwnedByAMI
	body := fmt.Sprintf(`# NVIDIA GPU Operator values for profile %s (pin %s).
# When the node image already owns drivers/toolkit, keep both disabled.
driver:
  enabled: %v
toolkit:
  enabled: %v
devicePlugin:
  enabled: true
# Do not enable DRA and the device plugin for the same device on the same node.
# LMCache / Mooncake are intentionally omitted from this values file.
`, intent.Profile, intent.GPUOperator.Version, driver, toolkit)
	return os.WriteFile(path, []byte(body), 0o644)
}

func writeOverlayKustomization(path string, intent *StackIntent) error {
	resources := make([]string, 0, len(intent.ServingModels)*2+1)
	for _, model := range intent.ServingModels {
		if strings.ToLower(strings.TrimSpace(model.Backend)) == "llm-d" {
			continue
		}
		resources = append(resources,
			"serving/"+model.Name+"-deployment.yaml",
			"serving/"+model.Name+"-service.yaml",
		)
	}
	if intent.Profile == "nvidia-llmd-compat" && intent.LLMD != nil {
		ms := intent.LLMD.ModelServer
		resources = append(resources,
			"serving/"+ms.Name+"-deployment.yaml",
			"serving/"+ms.Name+"-service.yaml",
		)
	}
	resources = append(resources, "networkpolicy-patch.yaml")
	doc := map[string]any{
		"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind":       "Kustomization",
		"namespace":  intent.Namespace,
		"resources":  resources,
	}
	return writeYAML(path, doc)
}

func writeNetworkPolicyPatch(path string, intent *StackIntent) error {
	type egressTarget struct {
		name string
		port int
	}
	targets := make([]egressTarget, 0, len(intent.ServingModels)+2)
	for _, model := range intent.ServingModels {
		if strings.ToLower(strings.TrimSpace(model.Backend)) == "llm-d" {
			if intent.LLMD != nil {
				targets = append(targets, egressTarget{name: intent.LLMD.FrontendService, port: intent.LLMD.FrontendPort})
			}
			continue
		}
		targets = append(targets, egressTarget{name: model.Name, port: model.Port})
	}
	if intent.Profile == "nvidia-llmd-compat" && intent.LLMD != nil {
		targets = append(targets, egressTarget{name: intent.LLMD.ModelServer.Name, port: intent.LLMD.ModelServer.Port})
	}
	egressPeers := make([]map[string]any, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		key := fmt.Sprintf("%s:%d", target.name, target.port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		egressPeers = append(egressPeers, map[string]any{
			"to": []map[string]any{{
				"podSelector": map[string]any{
					"matchLabels": map[string]string{
						"app.kubernetes.io/name": target.name,
					},
				},
			}},
			"ports": []map[string]any{{
				"protocol": "TCP",
				"port":     target.port,
			}},
		})
	}
	doc := map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata": map[string]any{
			"name": "smart-llmrouter-restrict",
			"labels": map[string]string{
				"app.kubernetes.io/name": "smart-llmrouter",
			},
		},
		"spec": map[string]any{
			"podSelector": map[string]any{
				"matchLabels": map[string]string{
					"app.kubernetes.io/name": "smart-llmrouter",
				},
			},
			"policyTypes": []string{"Egress"},
			"egress": append([]map[string]any{
				{
					"to": []map[string]any{{"namespaceSelector": map[string]any{}}},
					"ports": []map[string]any{
						{"protocol": "TCP", "port": 53},
						{"protocol": "UDP", "port": 53},
					},
				},
			}, egressPeers...),
		},
	}
	return writeYAML(path, doc)
}

func llmdLine(intent *StackIntent) string {
	if intent.Profile != "nvidia-llmd-compat" || intent.LLMD == nil {
		return "omitted (not selected for this profile)"
	}
	return fmt.Sprintf("standalone chart %s @ %s; frontend Service `%s`", intent.LLMD.ChartOCI, intent.LLMD.ChartVersion, intent.LLMD.FrontendService)
}

func writeLLMDModelServerManifests(dir, namespace string, model ModelServerIntent, servedModelID string) error {
	serving := ServingModelIntent{
		Name:          model.Name,
		ServedModelID: servedModelID,
		HuggingFaceID: model.HuggingFaceID,
		Image:         model.Image,
		GPUCount:      model.GPUCount,
		Port:          model.Port,
		MaxModelLen:   model.MaxModelLen,
	}
	if err := writeServingManifestsWithLabels(dir, namespace, serving, map[string]string{
		model.MatchLabelKey: model.MatchLabelValue,
	}); err != nil {
		return err
	}
	return nil
}

func writeLLMDHelmValues(path string, intent *StackIntent) error {
	if intent.LLMD == nil {
		return fmt.Errorf("llmd block is required")
	}
	ms := intent.LLMD.ModelServer
	body := fmt.Sprintf(`# Generated llm-d standalone Helm values for profile %s.
# Install only after Gateway API Inference Extension CRDs are present in the cluster.
# Metrum AI Router does not install or manage llm-d; apply with install.example.sh.
router:
  modelServers:
    matchLabels:
      %s: %s
    port: %d
  inferencePool:
    create: true
provider:
  name: none
`, intent.Profile, ms.MatchLabelKey, ms.MatchLabelValue, ms.Port)
	return os.WriteFile(path, []byte(body), 0o644)
}

func writeLLMDInstallScript(path string, intent *StackIntent) error {
	if intent.LLMD == nil {
		return fmt.Errorf("llmd block is required")
	}
	body := fmt.Sprintf(`#!/usr/bin/env bash
# Example llm-d standalone install for profile %s. Authenticate to the cluster first.
# OpenAI-compatible frontend: http://llm-d-local-epp.%s.svc.cluster.local:8081/v1
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS=%q
echo "Applying Gateway API Inference Extension v1 CRDs (required for llm-d v0.9+)..."
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/v1.5.0/v1-manifests.yaml
helm upgrade --install %q \
  %q \
  -f "$ROOT/helm-values.generated.yaml" \
  --version %q \
  -n "$NS" --create-namespace
echo "llm-d frontend: http://llm-d-local-epp:${NS}.svc.cluster.local:8081/v1"
`, intent.Profile, intent.Namespace, intent.Namespace, intent.LLMD.ReleaseName, intent.LLMD.ChartOCI, intent.LLMD.ChartVersion)
	return os.WriteFile(path, []byte(body), 0o755)
}

func writeServingManifests(dir, namespace string, model ServingModelIntent) error {
	return writeServingManifestsWithLabels(dir, namespace, model, nil)
}

func writeServingManifestsWithLabels(dir, namespace string, model ServingModelIntent, extraLabels map[string]string) error {
	hf := model.HuggingFaceID
	if hf == "" {
		hf = model.ServedModelID
	}
	depTmpl := template.Must(template.New("dep").Parse(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
  labels:
    app.kubernetes.io/name: {{.Name}}
    app.kubernetes.io/component: local-serving
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: {{.Name}}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{.Name}}
        app.kubernetes.io/component: local-serving
    spec:
      containers:
        - name: vllm
          image: {{.Image}}
          args:
            - "--model"
            - "{{.HF}}"
            - "--served-model-name"
            - "{{.Served}}"
            - "--host"
            - "0.0.0.0"
            - "--port"
            - "{{.Port}}"
            - "--max-model-len"
            - "{{.MaxModelLen}}"
          ports:
            - name: http
              containerPort: {{.Port}}
          resources:
            limits:
              nvidia.com/gpu: "{{.GPU}}"
            requests:
              nvidia.com/gpu: "{{.GPU}}"
`))
	extraLabelLines := ""
	for key, value := range extraLabels {
		extraLabelLines += fmt.Sprintf("        %s: %q\n", key, value)
	}
	if extraLabelLines != "" {
		depTmpl = template.Must(template.New("dep").Parse(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
  labels:
    app.kubernetes.io/name: {{.Name}}
    app.kubernetes.io/component: local-serving
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: {{.Name}}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{.Name}}
        app.kubernetes.io/component: local-serving
` + extraLabelLines + `    spec:
      containers:
        - name: vllm
          image: {{.Image}}
          args:
            - "--model"
            - "{{.HF}}"
            - "--served-model-name"
            - "{{.Served}}"
            - "--host"
            - "0.0.0.0"
            - "--port"
            - "{{.Port}}"
            - "--max-model-len"
            - "{{.MaxModelLen}}"
          ports:
            - name: http
              containerPort: {{.Port}}
          resources:
            limits:
              nvidia.com/gpu: "{{.GPU}}"
            requests:
              nvidia.com/gpu: "{{.GPU}}"
`))
	}
	svcTmpl := template.Must(template.New("svc").Parse(`apiVersion: v1
kind: Service
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
  labels:
    app.kubernetes.io/name: {{.Name}}
    app.kubernetes.io/component: local-serving
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: {{.Name}}
  ports:
    - name: http
      port: {{.Port}}
      targetPort: http
`))
	data := map[string]any{
		"Name":        model.Name,
		"Namespace":   namespace,
		"Image":       model.Image,
		"HF":          hf,
		"Served":      model.ServedModelID,
		"Port":        model.Port,
		"MaxModelLen": model.MaxModelLen,
		"GPU":         model.GPUCount,
	}
	depPath := filepath.Join(dir, model.Name+"-deployment.yaml")
	svcPath := filepath.Join(dir, model.Name+"-service.yaml")
	if err := executeTemplateFile(depPath, depTmpl, data); err != nil {
		return err
	}
	return executeTemplateFile(svcPath, svcTmpl, data)
}

func executeTemplateFile(path string, tmpl *template.Template, data any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, data)
}

func writeYAML(path string, value any) error {
	body, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}
