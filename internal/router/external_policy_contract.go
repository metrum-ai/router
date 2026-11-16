// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"strings"
)

func externalPolicyHostAllowed(host string, allowHosts []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range allowHosts {
		allowed = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(allowed), "."))
		if allowed != "" && host == allowed {
			return true
		}
	}
	return false
}

func externalPolicyConfigHeaderAllowed(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "content-type" || name == "accept" || name == "authorization" || strings.HasPrefix(name, "x-")
}

type externalPolicyCaller struct {
	ID             string   `json:"id"`
	User           string   `json:"user"`
	OwnerUser      string   `json:"ownerUser"`
	Username       string   `json:"username"`
	Project        string   `json:"project"`
	Environment    string   `json:"environment"`
	TokenID        string   `json:"tokenId"`
	KeyStatus      string   `json:"keyStatus"`
	MembershipRole string   `json:"membershipRole,omitempty"`
	Allow          []string `json:"allow"`
}

type externalPolicyTarget struct {
	Provider                           string                          `json:"provider"`
	Model                              string                          `json:"model"`
	ModelRef                           string                          `json:"modelRef,omitempty"`
	DisplayName                        string                          `json:"displayName,omitempty"`
	Dialect                            string                          `json:"dialect"`
	BaseURL                            string                          `json:"baseUrl"`
	Weight                             int                             `json:"weight"`
	RPM                                int                             `json:"rpm,omitempty"`
	Tier                               string                          `json:"tier,omitempty"`
	Cost                               int                             `json:"cost,omitempty"`
	InputPricePerMillionUSD            float64                         `json:"inputPricePerMillionUsd,omitempty"`
	OutputPricePerMillionUSD           float64                         `json:"outputPricePerMillionUsd,omitempty"`
	CachedInputPricePerMillionUSD      *float64                        `json:"cachedInputPricePerMillionUsd,omitempty"`
	ImageInputPricePerMillionTokensUSD float64                         `json:"imageInputPricePerMillionTokensUsd,omitempty"`
	ImageInputPricePerImageUSD         float64                         `json:"imageInputPricePerImageUsd,omitempty"`
	PricingSource                      string                          `json:"pricingSource,omitempty"`
	PricingUpdatedAt                   string                          `json:"pricingUpdatedAt,omitempty"`
	PricingNotes                       string                          `json:"pricingNotes,omitempty"`
	ToolSupport                        ToolSupport                     `json:"toolSupport,omitempty"`
	Reasoning                          ReasoningSupport                `json:"reasoning,omitempty"`
	InputModalities                    []string                        `json:"inputModalities,omitempty"`
	OutputModalities                   []string                        `json:"outputModalities,omitempty"`
	HonorsMaxTokens                    *bool                           `json:"honorsMaxTokens,omitempty"`
	ForceStoreFalse                    bool                            `json:"forceStoreFalse,omitempty"`
	OutputTokenField                   string                          `json:"outputTokenField,omitempty"`
	Validation                         *externalPolicyTargetValidation `json:"validation,omitempty"`
	KeyID                              string                          `json:"keyId,omitempty"`
	APIKeyEnv                          string                          `json:"apiKeyEnv,omitempty"`
	KeyConfigured                      bool                            `json:"keyConfigured"`
}

type externalPolicyContract struct {
	DisplayName        string                       `json:"displayName,omitempty"`
	CallerVisibleNotes string                       `json:"callerVisibleNotes,omitempty"`
	IntendedWorkloads  []string                     `json:"intendedWorkloads,omitempty"`
	SupportedAPIShapes []string                     `json:"supportedApiShapes,omitempty"`
	RequiredCaps       ContractRequiredCapabilities `json:"requiredCapabilities,omitempty"`
	QualityFloor       ContractQualityFloor         `json:"qualityFloor,omitempty"`
	OperationalTargets ContractOperationalTargets   `json:"operationalTargets,omitempty"`
	Reporting          ContractReporting            `json:"reporting,omitempty"`
}

type externalPolicyTargetValidation struct {
	Status       string  `json:"status,omitempty"`
	Workload     string  `json:"workload,omitempty"`
	ValidatedAt  string  `json:"validatedAt,omitempty"`
	QualityScore float64 `json:"qualityScore,omitempty"`
	PassRate     float64 `json:"passRate,omitempty"`
	Harness      string  `json:"harness,omitempty"`
	Notes        string  `json:"notes,omitempty"`
}

type policyDecisionOutput struct {
	Target             any    `json:"target"`
	TargetIndex        int    `json:"targetIndex"`
	HasTargetIndex     bool   `json:"-"`
	Fallbacks          []any  `json:"fallbacks"`
	HasFallbacks       bool   `json:"-"`
	FallbackIndexes    []int  `json:"fallbackIndexes"`
	HasFallbackIndexes bool   `json:"-"`
	ClassLabel         string `json:"classLabel"`
}

func buildExternalPolicyCaller(caller *callerRuntime, tokenID string) *externalPolicyCaller {
	if caller == nil {
		return nil
	}
	allow := append([]string(nil), caller.cfg.Allow...)
	user := callerUser(caller.cfg)
	return &externalPolicyCaller{
		ID:             caller.cfg.ID,
		User:           user,
		OwnerUser:      user,
		Username:       user,
		Project:        callerProject(caller.cfg),
		Environment:    callerEnvironment(caller.cfg),
		TokenID:        tokenID,
		KeyStatus:      normalizeStatusDefault(caller.keyStatus),
		MembershipRole: caller.membershipRole,
		Allow:          allow,
	}
}

func buildExternalPolicyTargets(targets []Target, providers map[string]ProviderConfig) []externalPolicyTarget {
	out := make([]externalPolicyTarget, 0, len(targets))
	for _, target := range targets {
		provider := providers[target.Provider]
		weight := target.Weight
		if weight == 0 {
			weight = 1
		}
		out = append(out, externalPolicyTarget{
			Provider:                           target.Provider,
			Model:                              target.Model,
			ModelRef:                           target.ModelRef,
			DisplayName:                        target.DisplayName,
			Dialect:                            targetDialect(provider, target),
			BaseURL:                            provider.BaseURL,
			Weight:                             weight,
			RPM:                                target.RPM,
			Tier:                               target.Tier,
			Cost:                               target.Cost,
			InputPricePerMillionUSD:            target.InputPricePerMillionUSD,
			OutputPricePerMillionUSD:           target.OutputPricePerMillionUSD,
			CachedInputPricePerMillionUSD:      target.CachedInputPricePerMillionUSD,
			ImageInputPricePerMillionTokensUSD: target.ImageInputPricePerMillionTokensUSD,
			ImageInputPricePerImageUSD:         target.ImageInputPricePerImageUSD,
			PricingSource:                      target.PricingSource,
			PricingUpdatedAt:                   target.PricingUpdatedAt,
			PricingNotes:                       target.PricingNotes,
			ToolSupport:                        target.ToolSupport,
			Reasoning:                          target.Reasoning,
			InputModalities:                    target.InputModalities,
			OutputModalities:                   target.OutputModalities,
			HonorsMaxTokens:                    target.HonorsMaxTokens,
			ForceStoreFalse:                    target.ForceStoreFalse,
			OutputTokenField:                   target.OutputTokenField,
			Validation:                         buildExternalPolicyTargetValidation(target.Validation),
			KeyID:                              provider.KeyID,
			APIKeyEnv:                          provider.APIKeyEnv,
			KeyConfigured:                      provider.APIKey != "",
		})
	}
	return out
}

func buildExternalPolicyContract(contract *ModelGroupContract) *externalPolicyContract {
	if contract == nil {
		return nil
	}
	return &externalPolicyContract{
		DisplayName:        contract.DisplayName,
		CallerVisibleNotes: contract.CallerVisibleNotes,
		IntendedWorkloads:  append([]string(nil), contract.IntendedWorkloads...),
		SupportedAPIShapes: append([]string(nil), contract.SupportedAPIShapes...),
		RequiredCaps:       contract.RequiredCaps,
		QualityFloor:       contract.QualityFloor,
		OperationalTargets: contract.OperationalTargets,
		Reporting:          contract.Reporting,
	}
}

func buildExternalPolicyTargetValidation(validation *TargetValidation) *externalPolicyTargetValidation {
	if validation == nil {
		return nil
	}
	return &externalPolicyTargetValidation{
		Status:       validation.Status,
		Workload:     validation.Workload,
		ValidatedAt:  validation.ValidatedAt,
		QualityScore: validation.QualityScore,
		PassRate:     validation.PassRate,
		Harness:      validation.Harness,
		Notes:        validation.Notes,
	}
}

func exportPolicyDecisionOutput(exported any, source string) (policyDecisionOutput, error) {
	raw, err := json.Marshal(exported)
	if err != nil {
		return policyDecisionOutput{}, fmt.Errorf("%s returned invalid decision: %w", source, err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return policyDecisionOutput{}, fmt.Errorf("%s returned invalid decision: %w", source, err)
	}
	var out policyDecisionOutput
	if rawTargetIndex, ok := fields["targetIndex"]; ok {
		if err := json.Unmarshal(rawTargetIndex, &out.TargetIndex); err != nil {
			return policyDecisionOutput{}, fmt.Errorf("%s targetIndex must be a number", source)
		}
		out.HasTargetIndex = true
	}
	if rawTarget, ok := fields["target"]; ok {
		if err := json.Unmarshal(rawTarget, &out.Target); err != nil {
			return policyDecisionOutput{}, fmt.Errorf("%s target is invalid", source)
		}
	}
	if rawFallbackIndexes, ok := fields["fallbackIndexes"]; ok {
		if err := json.Unmarshal(rawFallbackIndexes, &out.FallbackIndexes); err != nil {
			return policyDecisionOutput{}, fmt.Errorf("%s fallbackIndexes must be numbers", source)
		}
		out.HasFallbackIndexes = true
	}
	if rawFallbacks, ok := fields["fallbacks"]; ok {
		if err := json.Unmarshal(rawFallbacks, &out.Fallbacks); err != nil {
			return policyDecisionOutput{}, fmt.Errorf("%s fallbacks are invalid", source)
		}
		out.HasFallbacks = true
	}
	if rawClassLabel, ok := fields["classLabel"]; ok {
		if err := json.Unmarshal(rawClassLabel, &out.ClassLabel); err != nil {
			return policyDecisionOutput{}, fmt.Errorf("%s classLabel must be a string", source)
		}
	}
	return out, nil
}

func resolvePolicyTarget(out policyDecisionOutput, targets []Target) (int, error) {
	if out.HasTargetIndex {
		return validateTargetIndex(out.TargetIndex, targets)
	}
	if out.Target != nil {
		return targetSelectorIndex(out.Target, targets)
	}
	return 0, fmt.Errorf("external policy must return targetIndex or target")
}

func resolvePolicyFallbacks(out policyDecisionOutput, primary int, targets []Target) ([]Target, error) {
	seen := map[int]bool{primary: true}
	indexes := []int{}
	explicitFallbacks := out.HasFallbackIndexes || out.HasFallbacks
	for _, idx := range out.FallbackIndexes {
		valid, err := validateTargetIndex(idx, targets)
		if err != nil {
			return nil, err
		}
		if !seen[valid] {
			indexes = append(indexes, valid)
			seen[valid] = true
		}
	}
	for _, selector := range out.Fallbacks {
		idx, err := targetSelectorIndex(selector, targets)
		if err != nil {
			return nil, err
		}
		if !seen[idx] {
			indexes = append(indexes, idx)
			seen[idx] = true
		}
	}
	if !explicitFallbacks {
		for i := range targets {
			if !seen[i] {
				indexes = append(indexes, i)
			}
		}
	}
	fallbacks := make([]Target, 0, len(indexes))
	for _, idx := range indexes {
		fallbacks = append(fallbacks, targets[idx])
	}
	return fallbacks, nil
}

func validateTargetIndex(idx int, targets []Target) (int, error) {
	if idx < 0 || idx >= len(targets) {
		return 0, fmt.Errorf("external policy selected target index %d outside configured targets", idx)
	}
	return idx, nil
}

func targetSelectorIndex(selector any, targets []Target) (int, error) {
	switch v := selector.(type) {
	case int:
		return validateTargetIndex(v, targets)
	case int64:
		return validateTargetIndex(int(v), targets)
	case float64:
		return validateTargetIndex(int(v), targets)
	case string:
		return findTargetByString(v, targets)
	case map[string]any:
		return findTargetByMap(v, targets)
	default:
		raw, _ := json.Marshal(selector)
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err == nil && len(m) > 0 {
			return findTargetByMap(m, targets)
		}
		return 0, fmt.Errorf("unsupported external policy target selector %T", selector)
	}
}

func findTargetByString(selector string, targets []Target) (int, error) {
	for i, target := range targets {
		if selector == target.Provider || selector == target.Provider+":"+target.Model {
			return i, nil
		}
	}
	return 0, fmt.Errorf("external policy selected unknown target %q", selector)
}

func findTargetByMap(selector map[string]any, targets []Target) (int, error) {
	provider, _ := selector["provider"].(string)
	model, _ := selector["model"].(string)
	modelRef, _ := selector["modelRef"].(string)
	if modelRef == "" {
		modelRef, _ = selector["model_ref"].(string)
	}
	for i, target := range targets {
		if provider != "" && provider != target.Provider {
			continue
		}
		if model != "" && model != target.Model {
			continue
		}
		if modelRef != "" && modelRef != target.ModelRef {
			continue
		}
		if provider != "" || model != "" || modelRef != "" {
			return i, nil
		}
	}
	raw, _ := json.Marshal(selector)
	return 0, fmt.Errorf("external policy selected target outside configured list: %s", strings.TrimSpace(string(raw)))
}
