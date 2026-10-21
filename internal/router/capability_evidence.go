// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
)

// CapabilityEvidence is an import-safe, persistence-free DTO for the
// deterministic capability-smoke contract. It intentionally has no GORM tags
// or runtime wiring: synthetic evidence is non-promotable test input only.
type CapabilityEvidence struct {
	SchemaVersion  string                     `json:"schema_version"`
	Identity       CapabilityEvidenceIdentity `json:"identity"`
	CapabilityCase string                     `json:"capability_case"`
	Status         string                     `json:"status"`
}

type CapabilityEvidenceIdentity struct {
	Provider             string `json:"provider"`
	AccountIdentityClass string `json:"account_identity_class"`
	EndpointFingerprint  string `json:"endpoint_fingerprint"`
	EndpointPath         string `json:"endpoint_path"`
	APISkin              string `json:"api_skin"`
	Model                string `json:"model"`
	ModelSuffix          string `json:"model_suffix"`
	InboundDialect       string `json:"inbound_dialect"`
	BridgeDirection      string `json:"bridge_direction"`
	RequestShape         string `json:"request_shape"`
	ProfileVersion       string `json:"profile_version"`
}

type capabilitySurface struct {
	target              Target
	accountIdentity     string
	endpointFingerprint string
	endpointPath        string
	apiSkin             string
	model               string
	modelSuffix         string
	inbound             string
	bridge              string
	capabilities        []string
}

const anthropicTranslationBridgePrefix = "anthropic_to_"

// VerifyCapabilityClaims checks resolved (catalog plus target override)
// metadata against matching, passing evidence. It does not mutate routing,
// configuration, weights, or provider catalogs.
func (c *Config) VerifyCapabilityClaims(group string, claims []CapabilityEvidence, expected []CapabilityEvidenceIdentity) []string {
	modelGroup, ok := c.Models[group]
	if !ok {
		return []string{"unknown model group " + group}
	}
	var failures []string
	for _, claim := range claims {
		if claim.SchemaVersion != "capability-smoke/v1" || claim.Status != "passed" || !capabilityIdentityComplete(claim.Identity) {
			failures = append(failures, "capability evidence is not a passing v1 result")
			continue
		}
		if !identityIsExpected(claim.Identity, expected) {
			failures = append(failures, "capability evidence identity is not approved")
			continue
		}
		matched := false
		for _, raw := range modelGroup.Targets {
			target, err := c.resolveTarget(group, raw)
			if err != nil {
				continue
			}
			provider := c.Provider[target.Provider]
			if !targetMatchesCapabilityClaimIdentity(provider, target, claim.Identity) {
				continue
			}
			surfaces, err := capabilitySurfaces(provider, target)
			if err != nil {
				failures = append(failures, "cannot derive capability endpoint for "+target.Provider+"/"+target.Model)
				continue
			}
			for _, surface := range surfaces {
				if !identityMatchesCapabilitySurface(claim.Identity, claim.CapabilityCase, surface) {
					continue
				}
				matched = true
				if !surfaceAdvertisesCapability(surface, claim.CapabilityCase) {
					failures = append(failures, "target metadata does not advertise "+claim.CapabilityCase+" for "+target.Provider+"/"+target.Model)
				}
			}
		}
		if !matched {
			failures = append(failures, "no matching resolved target for "+claim.Identity.Provider+"/"+claim.Identity.Model)
		}
	}
	return failures
}

// targetMatchesCapabilityClaimIdentity compares every target identity component
// that can be resolved without deriving capability surfaces. Inbound dialect,
// bridge direction, and request shape intentionally remain surface checks below:
// one target can expose several of those surfaces. Keeping this prefilter free
// of capability-shape validation lets an unrelated unrepresentable target be
// skipped, while the matching target and whole-group verification still fail
// closed during capabilitySurfaces.
func targetMatchesCapabilityClaimIdentity(provider ProviderConfig, target Target, identity CapabilityEvidenceIdentity) bool {
	if target.Provider != identity.Provider || strings.TrimSpace(provider.KeyID) != identity.AccountIdentityClass {
		return false
	}
	model, suffix := capabilityModelIdentity(target.Model)
	if model != identity.Model || suffix != identity.ModelSuffix {
		return false
	}
	apiSkin := targetDialect(provider, target)
	if apiSkin != identity.APISkin {
		return false
	}
	endpointPath, endpointFingerprint, err := capabilityEndpointIdentity(provider, target, apiSkin)
	return err == nil && endpointPath == identity.EndpointPath && endpointFingerprint == identity.EndpointFingerprint
}

// VerifyAdvertisedCapabilities fails closed when any resolved target callable
// surface lacks matching, complete, passing synthetic evidence. Direct and
// translated surfaces are separate requirements. No evidence is persisted or
// trusted for promotion.
func (c *Config) VerifyAdvertisedCapabilities(group string, evidence []CapabilityEvidence, expected []CapabilityEvidenceIdentity) []string {
	modelGroup, ok := c.Models[group]
	if !ok {
		return []string{"unknown model group " + group}
	}
	var failures []string
	for _, raw := range modelGroup.Targets {
		target, err := c.resolveTarget(group, raw)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		surfaces, err := capabilitySurfaces(c.Provider[target.Provider], target)
		if err != nil {
			failures = append(failures, "cannot derive capability endpoint for "+target.Provider+"/"+target.Model)
			continue
		}
		for _, surface := range surfaces {
			for _, capability := range surface.capabilities {
				matched := false
				for _, row := range evidence {
					if row.SchemaVersion != "capability-smoke/v1" || row.Status != "passed" || row.CapabilityCase != capability || !capabilityIdentityComplete(row.Identity) {
						continue
					}
					if identityMatchesCapabilitySurface(row.Identity, capability, surface) && identityIsExpected(row.Identity, expected) {
						matched = true
						break
					}
				}
				if !matched {
					failures = append(failures, "missing passing evidence for "+capability+" on "+target.Provider+"/"+target.Model+" ("+surface.inbound+"/"+surface.bridge+")")
				}
			}
		}
	}
	return failures
}

func capabilitySurfaces(provider ProviderConfig, target Target) ([]capabilitySurface, error) {
	accountIdentity := strings.TrimSpace(provider.KeyID)
	if accountIdentity == "" {
		return nil, fmt.Errorf("provider key_id is required")
	}
	apiSkin := targetDialect(provider, target)
	if err := validateRepresentableCapabilityShapes(target, apiSkin); err != nil {
		return nil, err
	}
	endpointPath, endpointFingerprint, err := capabilityEndpointIdentity(provider, target, apiSkin)
	if err != nil {
		return nil, err
	}
	model, modelSuffix := capabilityModelIdentity(target.Model)
	direct := capabilitySurface{
		target:              target,
		accountIdentity:     accountIdentity,
		endpointFingerprint: endpointFingerprint,
		endpointPath:        endpointPath,
		apiSkin:             apiSkin,
		model:               model,
		modelSuffix:         modelSuffix,
		inbound:             apiSkin,
		bridge:              "none",
		capabilities:        advertisedCapabilities(target, apiSkin),
	}
	var surfaces []capabilitySurface
	if evidenceInboundAllowed(target, apiSkin) && len(direct.capabilities) > 0 {
		surfaces = append(surfaces, direct)
	}
	if evidenceInboundAllowed(target, "openai-chat") && isChatToResponsesBridge("openai-chat", apiSkin, target) {
		capabilities := chatToResponsesEvidenceCapabilities(target, apiSkin)
		if len(capabilities) > 0 {
			translated := direct
			translated.inbound = "openai-chat"
			translated.bridge = chatToResponsesBridgeDirection
			translated.capabilities = capabilities
			surfaces = append(surfaces, translated)
		}
	}
	if evidenceInboundAllowed(target, "openai-responses") && isResponsesToChatBridge("openai-responses", apiSkin, target) {
		capabilities := responsesToChatEvidenceCapabilities(target, apiSkin)
		if len(capabilities) > 0 {
			translated := direct
			translated.inbound = "openai-responses"
			translated.bridge = responsesToChatBridgeDirection
			translated.capabilities = capabilities
			surfaces = append(surfaces, translated)
		}
	}
	if apiSkin != "anthropic" && anthropicInboundDialectFilterReason(target, "anthropic", apiSkin) == "" {
		capabilities := anthropicTranslationEvidenceCapabilities(target, apiSkin)
		if len(capabilities) > 0 {
			translated := direct
			translated.inbound = "anthropic"
			translated.bridge = anthropicTranslationBridgePrefix + apiSkin
			translated.capabilities = capabilities
			surfaces = append(surfaces, translated)
		}
	}
	genericInbounds := target.RequestShapeSupport.SupportedInboundDialects
	if len(genericInbounds) == 0 {
		genericInbounds = []string{"openai-chat", "openai-responses"}
	}
	for _, configuredInbound := range genericInbounds {
		inbound := normalizeDialect(configuredInbound)
		if !genericCapabilityTranslationDirection(inbound, apiSkin) {
			continue
		}
		capabilities := genericTranslationEvidenceCapabilities(target)
		if len(capabilities) == 0 {
			continue
		}
		translated := direct
		translated.inbound = inbound
		translated.bridge = inbound + "_to_" + apiSkin
		translated.capabilities = capabilities
		surfaces = append(surfaces, translated)
	}
	if len(surfaces) == 0 {
		return nil, fmt.Errorf("target has no verifiable runtime capability surface")
	}
	return surfaces, nil
}

func advertisedCapabilities(target Target, dialect string) []string {
	var capabilities []string
	if !target.ToolOnly {
		capabilities = append(capabilities, "text")
	}
	if targetSupportsClientTools(target, dialect, false) {
		capabilities = append(capabilities, "tools-omitted")
	}
	if targetSupportsExplicitAutoToolChoice(target, dialect) {
		capabilities = append(capabilities, "tools-auto")
	}
	if targetSupportsClientTools(target, dialect, true) {
		capabilities = append(capabilities, "tools-forced")
	}
	if !target.ToolOnly &&
		!supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "image") &&
		targetSupportsInputModalities(target, []string{"image"}) {
		capabilities = append(capabilities, "image-input")
	}
	if !target.ToolOnly &&
		!supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "structured_output", "response_format") &&
		targetSupportsStructuredOutput(target, dialect, dialect, true) {
		capabilities = append(capabilities, "structured-outputs")
	}
	return filterCapabilitiesByRequiredModalities(target, capabilities)
}

func anthropicTranslationEvidenceCapabilities(target Target, dialect string) []string {
	var capabilities []string
	if !target.ToolOnly {
		capabilities = append(capabilities, "text")
	}
	if anthropicToolsBridgeEnabled(target, dialect) {
		capabilities = append(capabilities, "tools-omitted")
		if targetSupportsClientTools(target, "anthropic", true) && targetSupportsClientTools(target, dialect, true) {
			capabilities = append(capabilities, "tools-auto", "tools-forced")
		}
	}
	if !supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "image") &&
		targetSupportsInputModalities(target, []string{"image"}) {
		capabilities = append(capabilities, "image-input")
	}
	return filterCapabilitiesByRequiredModalities(target, capabilities)
}

func evidenceInboundAllowed(target Target, inbound string) bool {
	return len(target.RequestShapeSupport.SupportedInboundDialects) == 0 ||
		stringSliceContainsNormalizedDialect(target.RequestShapeSupport.SupportedInboundDialects, inbound)
}

func validateRepresentableCapabilityShapes(target Target, dialect string) error {
	image := !supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "image") &&
		targetSupportsInputModalities(target, []string{"image"})
	structured := !supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "structured_output", "response_format") &&
		targetSupportsStructuredOutput(target, dialect, dialect, true)
	if target.ToolOnly && (image || structured) {
		return fmt.Errorf("tool-only image or structured capability requires an unsupported composite evidence shape")
	}
	if len(target.RequestShapeSupport.RequiredInputModalities) > 0 &&
		!stringSliceContainsAll([]string{"text"}, target.RequestShapeSupport.RequiredInputModalities) &&
		(targetSupportsClientTools(target, dialect, false) || structured) {
		return fmt.Errorf("required input modalities with tools or structured output require an unsupported composite evidence shape")
	}
	return nil
}

func genericCapabilityTranslationDirection(inbound, out string) bool {
	if inbound == "" || inbound == out || inbound == "anthropic" {
		return false
	}
	if (inbound == "openai-chat" && out == "openai-responses") ||
		(inbound == "openai-responses" && out == "openai-chat") {
		return false
	}
	switch inbound {
	case "openai-chat", "openai-responses":
		return out == "anthropic" || out == "replicate"
	default:
		return false
	}
}

func genericTranslationEvidenceCapabilities(target Target) []string {
	if target.ToolOnly {
		return nil
	}
	capabilities := []string{"text"}
	if !supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "image") &&
		targetSupportsInputModalities(target, []string{"image"}) {
		capabilities = append(capabilities, "image-input")
	}
	return filterCapabilitiesByRequiredModalities(target, capabilities)
}

func filterCapabilitiesByRequiredModalities(target Target, capabilities []string) []string {
	if len(target.RequestShapeSupport.RequiredInputModalities) == 0 {
		return capabilities
	}
	filtered := capabilities[:0]
	for _, capability := range capabilities {
		modalities := []string{"text"}
		if capability == "image-input" || capability == "router-selected-ocr" {
			modalities = append(modalities, "image")
		}
		if stringSliceContainsAll(modalities, target.RequestShapeSupport.RequiredInputModalities) {
			filtered = append(filtered, capability)
		}
	}
	return filtered
}

func chatToResponsesEvidenceCapabilities(target Target, dialect string) []string {
	bridge := target.Bridges.ChatToResponses
	if bridge.Text != nil && !*bridge.Text {
		return nil
	}
	var capabilities []string
	if !target.ToolOnly {
		capabilities = append(capabilities, "text")
	}
	if bridge.Tools && targetSupportsClientTools(target, dialect, false) {
		capabilities = append(capabilities, "tools-omitted")
	}
	if bridge.Tools && bridge.ToolChoice && targetSupportsExplicitAutoToolChoice(target, dialect) {
		capabilities = append(capabilities, "tools-auto")
	}
	if bridge.Tools && bridge.ToolChoice && targetSupportsClientTools(target, dialect, true) {
		capabilities = append(capabilities, "tools-forced")
	}
	if !target.ToolOnly && bridge.Images &&
		!supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "image") &&
		targetSupportsInputModalities(target, []string{"image"}) {
		capabilities = append(capabilities, "image-input")
	}
	if !target.ToolOnly &&
		!supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "structured_output", "response_format") &&
		targetSupportsStructuredOutput(target, "openai-chat", dialect, true) {
		capabilities = append(capabilities, "structured-outputs")
	}
	return filterCapabilitiesByRequiredModalities(target, capabilities)
}

func responsesToChatEvidenceCapabilities(target Target, dialect string) []string {
	bridge := target.ResponsesToChat
	var capabilities []string
	if !target.ToolOnly && bridge.Text {
		capabilities = append(capabilities, "text")
	}
	if bridge.FunctionTools && targetSupportsClientTools(target, dialect, false) {
		capabilities = append(capabilities, "tools-omitted")
	}
	if bridge.FunctionTools && bridge.ToolChoice && targetSupportsExplicitAutoToolChoice(target, dialect) {
		capabilities = append(capabilities, "tools-auto")
	}
	if bridge.FunctionTools && bridge.ToolChoice && targetSupportsClientTools(target, dialect, true) {
		capabilities = append(capabilities, "tools-forced")
	}
	if !target.ToolOnly && bridge.Text && bridge.Images &&
		!supportsAnyCapability(target.RequestShapeSupport.UnsupportedRequestFeatures, "image") &&
		targetSupportsInputModalities(target, []string{"image"}) {
		capabilities = append(capabilities, "image-input")
	}
	return filterCapabilitiesByRequiredModalities(target, capabilities)
}

func capabilityEndpointIdentity(provider ProviderConfig, target Target, dialect string) (string, string, error) {
	endpoint, err := upstreamEndpoint(provider.BaseURL, dialect, target)
	if err != nil {
		return "", "", fmt.Errorf("invalid upstream endpoint")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.EscapedPath() == "" || parsed.User != nil {
		return "", "", fmt.Errorf("invalid upstream endpoint")
	}
	fingerprintURL := *parsed
	fingerprintURL.Scheme = strings.ToLower(fingerprintURL.Scheme)
	fingerprintURL.Host = strings.ToLower(fingerprintURL.Host)
	fingerprintURL.Fragment = ""
	fingerprint := sha256.Sum256([]byte(fingerprintURL.String()))
	return parsed.EscapedPath(), fmt.Sprintf("sha256:%x", fingerprint), nil
}

func capabilityModelIdentity(model string) (string, string) {
	if index := strings.LastIndex(model, ":"); index > strings.LastIndex(model, "/") {
		return model[:index], model[index:]
	}
	return model, ""
}

func identityMatchesCapabilitySurface(identity CapabilityEvidenceIdentity, capability string, surface capabilitySurface) bool {
	requestShape := capabilityRequestShape(capability)
	return requestShape != "" &&
		identity.Provider == surface.target.Provider &&
		identity.AccountIdentityClass == surface.accountIdentity &&
		identity.EndpointFingerprint == surface.endpointFingerprint &&
		identity.Model == surface.model &&
		identity.ModelSuffix == surface.modelSuffix &&
		identity.APISkin == surface.apiSkin &&
		identity.EndpointPath == surface.endpointPath &&
		identity.InboundDialect == surface.inbound &&
		identity.BridgeDirection == surface.bridge &&
		identity.RequestShape == requestShape
}

func surfaceAdvertisesCapability(surface capabilitySurface, capability string) bool {
	for _, advertised := range surface.capabilities {
		if advertised == capability {
			return true
		}
	}
	return false
}

func capabilityRequestShape(capability string) string {
	switch capability {
	case "text", "openai-responses":
		return "text"
	case "tools-omitted":
		return "tools-omitted"
	case "tools-auto":
		return "tools-auto"
	case "tools-forced":
		return "tools-forced"
	case "image-input", "router-selected-ocr":
		return "image"
	case "structured-outputs":
		return "structured-outputs"
	default:
		return ""
	}
}

func identityIsExpected(identity CapabilityEvidenceIdentity, expected []CapabilityEvidenceIdentity) bool {
	for _, item := range expected {
		if identity == item {
			return true
		}
	}
	return false
}

func capabilityBridgeTupleValid(inbound, apiSkin, direction string) bool {
	switch direction {
	case "none":
		return inbound == apiSkin
	case chatToResponsesBridgeDirection:
		return inbound == "openai-chat" && apiSkin == "openai-responses"
	case responsesToChatBridgeDirection:
		return inbound == "openai-responses" && apiSkin == "openai-chat"
	case "anthropic_to_openai-chat":
		return inbound == "anthropic" && apiSkin == "openai-chat"
	case "anthropic_to_openai-responses":
		return inbound == "anthropic" && apiSkin == "openai-responses"
	case "anthropic_to_replicate":
		return inbound == "anthropic" && apiSkin == "replicate"
	case "openai-chat_to_anthropic":
		return inbound == "openai-chat" && apiSkin == "anthropic"
	case "openai-chat_to_replicate":
		return inbound == "openai-chat" && apiSkin == "replicate"
	case "openai-responses_to_anthropic":
		return inbound == "openai-responses" && apiSkin == "anthropic"
	case "openai-responses_to_replicate":
		return inbound == "openai-responses" && apiSkin == "replicate"
	default:
		return false
	}
}

func capabilityIdentityComplete(identity CapabilityEvidenceIdentity) bool {
	for _, value := range []string{identity.Provider, identity.AccountIdentityClass, identity.EndpointPath, identity.APISkin, identity.Model, identity.InboundDialect, identity.BridgeDirection, identity.RequestShape, identity.ProfileVersion} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	validBridge := capabilityBridgeTupleValid(identity.InboundDialect, identity.APISkin, identity.BridgeDirection)
	if !strings.HasPrefix(identity.EndpointPath, "/") ||
		normalizeDialect(identity.APISkin) != identity.APISkin ||
		normalizeDialect(identity.InboundDialect) != identity.InboundDialect ||
		!validBridge ||
		(identity.ModelSuffix != "" && !strings.HasPrefix(identity.ModelSuffix, ":")) {
		return false
	}
	const fingerprintPrefix = "sha256:"
	if !strings.HasPrefix(identity.EndpointFingerprint, fingerprintPrefix) || len(identity.EndpointFingerprint) != len(fingerprintPrefix)+64 {
		return false
	}
	for _, value := range identity.EndpointFingerprint[len(fingerprintPrefix):] {
		if !strings.ContainsRune("0123456789abcdef", value) {
			return false
		}
	}
	return true
}
