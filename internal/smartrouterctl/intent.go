// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Package smartrouterctl implements customer-local file-owned operations and
// Kubernetes architecture blueprint rendering for Metrum AI Router.
// It never calls AWS, EKS, RDS, or the Kubernetes API.
package smartrouterctl

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const IntentSchema = "metrum.ai/smartrouter-stack-intent/v1"

// StackIntent is a non-Kubernetes input that describes a deployable stack.
type StackIntent struct {
	Schema            string               `yaml:"schema" json:"schema"`
	Profile           string               `yaml:"profile" json:"profile"`
	HardwareProfile   string               `yaml:"hardware_profile" json:"hardware_profile"`
	Namespace         string               `yaml:"namespace" json:"namespace"`
	RouterImage       string               `yaml:"router_image" json:"router_image"`
	Edge              string               `yaml:"edge" json:"edge"`
	UsageDriver       string               `yaml:"usage_driver" json:"usage_driver"`
	GPUOperator       GPUOperatorIntent    `yaml:"gpu_operator" json:"gpu_operator"`
	KVCache           KVCacheIntent        `yaml:"kv_cache" json:"kv_cache"`
	Packaging         PackagingIntent      `yaml:"packaging" json:"packaging"`
	ServingModels     []ServingModelIntent `yaml:"serving_models" json:"serving_models"`
	LLMD              *LLMDCompatIntent    `yaml:"llmd,omitempty" json:"llmd,omitempty"`
	DefaultModelGroup string               `yaml:"default_model_group" json:"default_model_group"`
	CallerAllow       []string             `yaml:"caller_allow" json:"caller_allow"`
}

// LLMDCompatIntent describes upstream llm-d standalone/gateway install inputs for
// nvidia-llmd-compat. Metrum AI Router never owns llm-d; this block documents Helm inputs only.
type LLMDCompatIntent struct {
	ChartOCI        string            `yaml:"chart_oci" json:"chart_oci"`
	ChartVersion    string            `yaml:"chart_version" json:"chart_version"`
	ReleaseName     string            `yaml:"release_name" json:"release_name"`
	FrontendService string            `yaml:"frontend_service" json:"frontend_service"`
	FrontendPort    int               `yaml:"frontend_port" json:"frontend_port"`
	ModelServer     ModelServerIntent `yaml:"model_server" json:"model_server"`
}

// ModelServerIntent is the in-cluster vLLM backend selected by llm-d's InferencePool.
type ModelServerIntent struct {
	Name            string `yaml:"name" json:"name"`
	HuggingFaceID   string `yaml:"huggingface_id" json:"huggingface_id"`
	Image           string `yaml:"image" json:"image"`
	GPUCount        int    `yaml:"gpu_count" json:"gpu_count"`
	Port            int    `yaml:"port" json:"port"`
	MaxModelLen     int    `yaml:"max_model_len" json:"max_model_len"`
	MatchLabelKey   string `yaml:"match_label_key" json:"match_label_key"`
	MatchLabelValue string `yaml:"match_label_value" json:"match_label_value"`
}

// PackagingIntent controls generated Helm chart and optional CRD operator scaffolds.
type PackagingIntent struct {
	// Helm defaults to true so Level-1 chart generation is part of blueprint render.
	Helm *bool `yaml:"helm" json:"helm"`
	// Operator defaults to false; set true when Level-2 CRD scaffold is required.
	Operator bool `yaml:"operator" json:"operator"`
}

func (p PackagingIntent) HelmEnabled() bool {
	if p.Helm == nil {
		return true
	}
	return *p.Helm
}

type GPUOperatorIntent struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`
	Version          string `yaml:"version" json:"version"`
	DriverOwnedByAMI bool   `yaml:"driver_owned_by_ami" json:"driver_owned_by_ami"`
}

type KVCacheIntent struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

type ServingModelIntent struct {
	Name              string `yaml:"name" json:"name"`
	ServedModelID     string `yaml:"served_model_id" json:"served_model_id"`
	HuggingFaceID     string `yaml:"huggingface_id" json:"huggingface_id"`
	Image             string `yaml:"image" json:"image"`
	GPUCount          int    `yaml:"gpu_count" json:"gpu_count"`
	Port              int    `yaml:"port" json:"port"`
	MaxModelLen       int    `yaml:"max_model_len" json:"max_model_len"`
	ModelGroup        string `yaml:"model_group" json:"model_group"`
	ServiceDNS        string `yaml:"service_dns" json:"service_dns"`
	ModelSizeBillions int    `yaml:"model_size_billions" json:"model_size_billions"`
	APIKeyEnv         string `yaml:"api_key_env" json:"api_key_env"`
	// Backend is vllm (default) or llm-d for an OpenAI-compatible llm-d frontend URL.
	Backend string `yaml:"backend" json:"backend"`
}

// LoadIntent reads and validates a stack intent YAML file.
func LoadIntent(path string) (*StackIntent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var intent StackIntent
	if err := yaml.Unmarshal(raw, &intent); err != nil {
		return nil, fmt.Errorf("parse intent: %w", err)
	}
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	return &intent, nil
}

func (i *StackIntent) Validate() error {
	if i == nil {
		return fmt.Errorf("intent is required")
	}
	if strings.TrimSpace(i.Schema) == "" {
		i.Schema = IntentSchema
	}
	if i.Schema != IntentSchema {
		return fmt.Errorf("unsupported intent schema %q", i.Schema)
	}
	switch strings.TrimSpace(i.Profile) {
	case "minimal", "nvidia-local-serving", "nvidia-llmd-compat":
	default:
		return fmt.Errorf("profile must be minimal, nvidia-local-serving, or nvidia-llmd-compat")
	}
	if strings.TrimSpace(i.Namespace) == "" {
		i.Namespace = "smart-llmrouter"
	}
	if strings.TrimSpace(i.Edge) == "" {
		i.Edge = "ingress"
	}
	switch i.Edge {
	case "caddy", "ingress", "none":
	default:
		return fmt.Errorf("edge must be caddy, ingress, or none")
	}
	if strings.TrimSpace(i.UsageDriver) == "" {
		i.UsageDriver = "sqlite"
	}
	if i.UsageDriver != "sqlite" && i.UsageDriver != "postgres" {
		return fmt.Errorf("usage_driver must be sqlite or postgres")
	}
	if i.Profile == "nvidia-local-serving" {
		if !i.GPUOperator.Enabled {
			return fmt.Errorf("nvidia-local-serving requires gpu_operator.enabled")
		}
		if len(i.ServingModels) < 3 {
			return fmt.Errorf("nvidia-local-serving requires at least three serving_models")
		}
		switch strings.ToLower(strings.TrimSpace(i.HardwareProfile)) {
		case "b200", "h200", "l40s":
			i.HardwareProfile = strings.ToLower(strings.TrimSpace(i.HardwareProfile))
		default:
			return fmt.Errorf("nvidia-local-serving hardware_profile must be b200, h200, or l40s")
		}
	}
	if i.Profile == "nvidia-llmd-compat" {
		if !i.GPUOperator.Enabled {
			return fmt.Errorf("nvidia-llmd-compat requires gpu_operator.enabled")
		}
		if i.LLMD == nil {
			return fmt.Errorf("nvidia-llmd-compat requires llmd block")
		}
		if len(i.ServingModels) != 1 {
			return fmt.Errorf("nvidia-llmd-compat requires exactly one serving_models entry for the llm-d frontend")
		}
		switch strings.ToLower(strings.TrimSpace(i.HardwareProfile)) {
		case "b200", "h200", "l40s":
			i.HardwareProfile = strings.ToLower(strings.TrimSpace(i.HardwareProfile))
		default:
			return fmt.Errorf("nvidia-llmd-compat hardware_profile must be b200, h200, or l40s")
		}
		if err := i.LLMD.validate(i.Namespace); err != nil {
			return err
		}
	}
	if i.GPUOperator.Enabled && strings.TrimSpace(i.GPUOperator.Version) == "" {
		i.GPUOperator.Version = "v26.7.0"
	}
	seenGroups := map[string]struct{}{}
	for idx, model := range i.ServingModels {
		if strings.TrimSpace(model.Name) == "" {
			return fmt.Errorf("serving_models[%d].name is required", idx)
		}
		if strings.TrimSpace(model.ServedModelID) == "" {
			return fmt.Errorf("serving_models[%d].served_model_id is required", idx)
		}
		if model.GPUCount < 1 {
			i.ServingModels[idx].GPUCount = 1
		}
		if model.Port < 1 {
			i.ServingModels[idx].Port = 8000
		}
		if i.Profile == "nvidia-local-serving" && model.MaxModelLen < 1 {
			i.ServingModels[idx].MaxModelLen = 8192
		}
		if strings.TrimSpace(model.ModelGroup) == "" {
			i.ServingModels[idx].ModelGroup = model.Name
		}
		if strings.TrimSpace(model.ServiceDNS) == "" {
			if i.Profile == "nvidia-llmd-compat" && i.LLMD != nil {
				i.ServingModels[idx].ServiceDNS = fmt.Sprintf(
					"http://%s.%s.svc.cluster.local:%d/v1",
					i.LLMD.FrontendService,
					i.Namespace,
					i.LLMD.FrontendPort,
				)
			} else {
				i.ServingModels[idx].ServiceDNS = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/v1", model.Name, i.Namespace, i.ServingModels[idx].Port)
			}
		}
		if i.Profile == "nvidia-local-serving" || i.Profile == "nvidia-llmd-compat" {
			if i.Profile == "nvidia-local-serving" && model.ModelSizeBillions < 1 {
				return fmt.Errorf("serving_models[%d].model_size_billions is required", idx)
			}
			serviceURL, err := url.Parse(i.ServingModels[idx].ServiceDNS)
			if err != nil || serviceURL.Scheme != "http" || serviceURL.Hostname() == "" ||
				!strings.HasSuffix(strings.ToLower(serviceURL.Hostname()), ".svc.cluster.local") {
				return fmt.Errorf("serving_models[%d].service_dns must be an http *.svc.cluster.local URL", idx)
			}
		}
		if strings.TrimSpace(model.APIKeyEnv) == "" {
			i.ServingModels[idx].APIKeyEnv = "LOCAL_VLLM_API_KEY"
		}
		backend := strings.ToLower(strings.TrimSpace(model.Backend))
		if backend == "" {
			backend = "vllm"
			i.ServingModels[idx].Backend = backend
		}
		if backend != "vllm" && backend != "llm-d" {
			return fmt.Errorf("serving_models[%d].backend must be vllm or llm-d", idx)
		}
		if i.Profile == "nvidia-llmd-compat" && backend != "llm-d" {
			return fmt.Errorf("serving_models[%d].backend must be llm-d for nvidia-llmd-compat", idx)
		}
		if backend == "vllm" && strings.TrimSpace(model.Image) == "" {
			return fmt.Errorf("serving_models[%d].image is required", idx)
		}
		if backend == "llm-d" && i.Profile == "nvidia-llmd-compat" {
			if model.ModelSizeBillions < 1 {
				i.ServingModels[idx].ModelSizeBillions = 2
			}
		}
		seenGroups[i.ServingModels[idx].ModelGroup] = struct{}{}
	}
	if i.Profile == "nvidia-local-serving" {
		smallModels := 0
		primaryModels := 0
		for _, model := range i.ServingModels {
			if model.ModelSizeBillions <= 9 {
				smallModels++
			}
			if model.ModelSizeBillions >= 20 && model.ModelSizeBillions <= 40 {
				primaryModels++
			}
		}
		if i.HardwareProfile == "l40s" {
			for _, model := range i.ServingModels {
				if model.ModelSizeBillions > 9 {
					return fmt.Errorf("nvidia-local-serving on l40s requires models at or below 9B")
				}
			}
		} else {
			if primaryModels == 0 {
				return fmt.Errorf("nvidia-local-serving on %s requires a 20-40B primary model", i.HardwareProfile)
			}
			if smallModels < 2 {
				return fmt.Errorf("nvidia-local-serving on %s requires at least two models at or below 9B", i.HardwareProfile)
			}
		}
	}
	if strings.TrimSpace(i.DefaultModelGroup) == "" && len(i.ServingModels) > 0 {
		i.DefaultModelGroup = i.ServingModels[0].ModelGroup
	}
	if len(i.CallerAllow) == 0 {
		groups := make([]string, 0, len(seenGroups))
		for group := range seenGroups {
			groups = append(groups, group)
		}
		sort.Strings(groups)
		i.CallerAllow = groups
	}
	return nil
}

func (l *LLMDCompatIntent) validate(namespace string) error {
	if l == nil {
		return fmt.Errorf("llmd block is required")
	}
	if strings.TrimSpace(l.ChartOCI) == "" {
		l.ChartOCI = "oci://ghcr.io/llm-d/charts/llm-d-router-standalone"
	}
	if strings.TrimSpace(l.ChartVersion) == "" {
		l.ChartVersion = "v0.9.0"
	}
	if strings.TrimSpace(l.ReleaseName) == "" {
		l.ReleaseName = "llm-d-local"
	}
	if l.FrontendPort < 1 {
		l.FrontendPort = 8081
	}
	if strings.TrimSpace(l.FrontendService) == "" {
		l.FrontendService = l.ReleaseName + "-epp"
	}
	ms := l.ModelServer
	if strings.TrimSpace(ms.Name) == "" {
		l.ModelServer.Name = "vllm-llmd-backend"
		ms = l.ModelServer
	}
	if strings.TrimSpace(ms.HuggingFaceID) == "" {
		return fmt.Errorf("llmd.model_server.huggingface_id is required")
	}
	if strings.TrimSpace(ms.Image) == "" {
		return fmt.Errorf("llmd.model_server.image is required")
	}
	if ms.GPUCount < 1 {
		l.ModelServer.GPUCount = 1
	}
	if ms.Port < 1 {
		l.ModelServer.Port = 8000
	}
	if ms.MaxModelLen < 1 {
		l.ModelServer.MaxModelLen = 8192
	}
	if strings.TrimSpace(ms.MatchLabelKey) == "" {
		l.ModelServer.MatchLabelKey = "app"
	}
	if strings.TrimSpace(ms.MatchLabelValue) == "" {
		l.ModelServer.MatchLabelValue = ms.Name
	}
	if strings.TrimSpace(namespace) == "" {
		namespace = "smart-llmrouter"
	}
	_ = namespace
	return nil
}
