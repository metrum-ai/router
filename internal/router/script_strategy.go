// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

const (
	scriptDefaultMaxConcurrent = 16
	scriptMaxConcurrentLimit   = 256
)

type scriptStrategy struct {
	path           string
	program        *goja.Program
	httpConfig     ScriptHTTPConfig
	slots          chan struct{}
	runtimeFactory func() *goja.Runtime
}

type scriptInput struct {
	Group           string          `json:"group"`
	Request         *IRRequest      `json:"request"`
	Context         requestSummary  `json:"context"`
	Contract        *scriptContract `json:"contract,omitempty"`
	Targets         []scriptTarget  `json:"targets"`
	Caller          *scriptCaller   `json:"caller,omitempty"`
	Text            string          `json:"text"`
	InputModalities []string        `json:"inputModalities"`
	Now             string          `json:"now"`
}

type scriptCaller struct {
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

type scriptTarget struct {
	Provider                           string                  `json:"provider"`
	Model                              string                  `json:"model"`
	ModelRef                           string                  `json:"modelRef,omitempty"`
	DisplayName                        string                  `json:"displayName,omitempty"`
	Dialect                            string                  `json:"dialect"`
	BaseURL                            string                  `json:"baseUrl"`
	Weight                             int                     `json:"weight"`
	RPM                                int                     `json:"rpm,omitempty"`
	Tier                               string                  `json:"tier,omitempty"`
	Cost                               int                     `json:"cost,omitempty"`
	InputPricePerMillionUSD            float64                 `json:"inputPricePerMillionUsd,omitempty"`
	OutputPricePerMillionUSD           float64                 `json:"outputPricePerMillionUsd,omitempty"`
	CachedInputPricePerMillionUSD      *float64                `json:"cachedInputPricePerMillionUsd,omitempty"`
	ImageInputPricePerMillionTokensUSD float64                 `json:"imageInputPricePerMillionTokensUsd,omitempty"`
	ImageInputPricePerImageUSD         float64                 `json:"imageInputPricePerImageUsd,omitempty"`
	PricingSource                      string                  `json:"pricingSource,omitempty"`
	PricingUpdatedAt                   string                  `json:"pricingUpdatedAt,omitempty"`
	PricingNotes                       string                  `json:"pricingNotes,omitempty"`
	ToolSupport                        ToolSupport             `json:"toolSupport,omitempty"`
	Reasoning                          ReasoningSupport        `json:"reasoning,omitempty"`
	InputModalities                    []string                `json:"inputModalities,omitempty"`
	OutputModalities                   []string                `json:"outputModalities,omitempty"`
	HonorsMaxTokens                    *bool                   `json:"honorsMaxTokens,omitempty"`
	ForceStoreFalse                    bool                    `json:"forceStoreFalse,omitempty"`
	OutputTokenField                   string                  `json:"outputTokenField,omitempty"`
	Validation                         *scriptTargetValidation `json:"validation,omitempty"`
	KeyID                              string                  `json:"keyId,omitempty"`
	APIKeyEnv                          string                  `json:"apiKeyEnv,omitempty"`
	KeyConfigured                      bool                    `json:"keyConfigured"`
}

type scriptContract struct {
	DisplayName        string                       `json:"displayName,omitempty"`
	CallerVisibleNotes string                       `json:"callerVisibleNotes,omitempty"`
	IntendedWorkloads  []string                     `json:"intendedWorkloads,omitempty"`
	SupportedAPIShapes []string                     `json:"supportedApiShapes,omitempty"`
	RequiredCaps       ContractRequiredCapabilities `json:"requiredCapabilities,omitempty"`
	QualityFloor       ContractQualityFloor         `json:"qualityFloor,omitempty"`
	OperationalTargets ContractOperationalTargets   `json:"operationalTargets,omitempty"`
	Reporting          ContractReporting            `json:"reporting,omitempty"`
}

type scriptTargetValidation struct {
	Status       string  `json:"status,omitempty"`
	Workload     string  `json:"workload,omitempty"`
	ValidatedAt  string  `json:"validatedAt,omitempty"`
	QualityScore float64 `json:"qualityScore,omitempty"`
	PassRate     float64 `json:"passRate,omitempty"`
	Harness      string  `json:"harness,omitempty"`
	Notes        string  `json:"notes,omitempty"`
}

type scriptOutput struct {
	Target             any    `json:"target"`
	TargetIndex        int    `json:"targetIndex"`
	HasTargetIndex     bool   `json:"-"`
	Fallbacks          []any  `json:"fallbacks"`
	HasFallbacks       bool   `json:"-"`
	FallbackIndexes    []int  `json:"fallbackIndexes"`
	HasFallbackIndexes bool   `json:"-"`
	ClassLabel         string `json:"classLabel"`
}

func loadScriptStrategy(baseDir, scriptPath string, maxConcurrent int, httpConfig ScriptHTTPConfig) (*scriptStrategy, error) {
	if scriptPath == "" {
		return nil, fmt.Errorf("missing script path")
	}
	resolved := scriptPath
	if !filepath.IsAbs(resolved) {
		if baseDir == "" {
			baseDir = "."
		}
		resolved = filepath.Join(baseDir, scriptPath)
	}
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{resolved},
		Bundle:            true,
		Write:             false,
		Format:            api.FormatIIFE,
		GlobalName:        "routerScript",
		Target:            api.ES2018,
		Sourcemap:         api.SourceMapNone,
		LegalComments:     api.LegalCommentsNone,
		Platform:          api.PlatformNeutral,
		TreeShaking:       api.TreeShakingTrue,
		KeepNames:         true,
		MinifyWhitespace:  false,
		MinifyIdentifiers: false,
		MinifySyntax:      false,
		AbsWorkingDir:     filepath.Dir(resolved),
		SourceRoot:        filepath.Dir(resolved),
		LogLevel:          api.LogLevelSilent,
	})
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("typescript transform: %s", result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return nil, fmt.Errorf("typescript transform produced no output")
	}
	program, err := goja.Compile(resolved, string(result.OutputFiles[0].Contents), false)
	if err != nil {
		return nil, err
	}
	if maxConcurrent <= 0 {
		maxConcurrent = scriptDefaultMaxConcurrent
	}
	return &scriptStrategy{
		path:           resolved,
		program:        program,
		httpConfig:     httpConfig,
		slots:          make(chan struct{}, maxConcurrent),
		runtimeFactory: goja.New,
	}, nil
}

func (s *scriptStrategy) Pick(ctx context.Context, group string, req *IRRequest, contract *ModelGroupContract, targets []Target, providers map[string]ProviderConfig, caller *callerRuntime, tokenID string) (decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return decision{}, fmt.Errorf("script routing canceled before execution: %w", ctx.Err())
	}
	vm := s.runtimeFactory()
	timer := time.AfterFunc(s.timeout(), func() {
		vm.Interrupt("script routing timed out")
	})
	defer timer.Stop()
	stopContextInterrupt := context.AfterFunc(ctx, func() {
		vm.Interrupt("script routing canceled")
	})
	defer stopContextInterrupt()
	if err := s.installRouterAPI(ctx, vm); err != nil {
		return decision{}, err
	}
	if _, err := vm.RunProgram(s.program); err != nil {
		return decision{}, fmt.Errorf("run %s: %w", s.path, err)
	}
	mod := vm.Get("routerScript").ToObject(vm)
	fn, ok := goja.AssertFunction(mod.Get("route"))
	if !ok {
		return decision{}, fmt.Errorf("%s must export function route(ctx)", s.path)
	}
	input := scriptInput{
		Group:           group,
		Request:         req,
		Context:         buildRequestSummary(req, ""),
		Contract:        buildScriptContract(contract),
		Targets:         buildScriptTargets(targets, providers),
		Caller:          buildScriptCaller(caller, tokenID),
		Text:            requestText(req),
		InputModalities: requestInputModalities(req),
		Now:             time.Now().UTC().Format(time.RFC3339),
	}
	ctxValue, err := jsonValue(input)
	if err != nil {
		return decision{}, err
	}
	val, err := fn(goja.Undefined(), vm.ToValue(ctxValue))
	if err != nil {
		return decision{}, fmt.Errorf("route(ctx): %w", err)
	}
	out, err := exportScriptOutput(val)
	if err != nil {
		return decision{}, err
	}
	primary, err := resolveScriptTarget(out, targets)
	if err != nil {
		return decision{}, err
	}
	fallbacks, err := resolveScriptFallbacks(out, primary, targets)
	if err != nil {
		return decision{}, err
	}
	var classLabel *string
	if out.ClassLabel != "" {
		classLabel = safePolicyClassLabel(out.ClassLabel)
	}
	durationMS := time.Since(start).Milliseconds()
	return decision{
		Target:      targets[primary],
		Fallbacks:   fallbacks,
		ClassLabel:  classLabel,
		Strategy:    "script",
		GroupName:   group,
		TargetIndex: primary,
		PolicyExecutions: []policyExecutionLogRecord{{
			Seq:                    1,
			Strategy:               "script",
			PolicyKind:             "typescript",
			Outcome:                "selected",
			DurationMS:             durationMS,
			EligibleTargetCount:    len(targets),
			SelectedCandidateIndex: primary,
			FallbackCount:          len(fallbacks),
			ClassLabel:             classLabel,
		}},
	}, nil
}

func (s *scriptStrategy) timeout() time.Duration {
	if !s.httpConfig.Enabled || s.httpConfig.TimeoutMS <= 0 {
		return 50 * time.Millisecond
	}
	timeout := time.Duration(s.httpConfig.TimeoutMS)*time.Millisecond + 50*time.Millisecond
	if timeout > 5050*time.Millisecond {
		return 5050 * time.Millisecond
	}
	return timeout
}

func (s *scriptStrategy) installRouterAPI(ctx context.Context, vm *goja.Runtime) error {
	routerAPI := vm.NewObject()
	if err := routerAPI.Set("fetchJSON", func(call goja.FunctionCall) goja.Value {
		if !s.httpConfig.Enabled {
			panic(vm.NewTypeError("router.fetchJSON is disabled for this model group"))
		}
		rawURL := call.Argument(0).String()
		options := map[string]any{}
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) && !goja.IsNull(call.Argument(1)) {
			if err := vm.ExportTo(call.Argument(1), &options); err != nil {
				panic(vm.NewTypeError("router.fetchJSON options must be an object"))
			}
		}
		resp, err := s.fetchJSON(ctx, rawURL, options)
		if err != nil {
			panic(vm.NewTypeError("router.fetchJSON: %s", err.Error()))
		}
		return vm.ToValue(resp)
	}); err != nil {
		return err
	}
	return vm.Set("router", routerAPI)
}

func (s *scriptStrategy) fetchJSON(ctx context.Context, rawURL string, options map[string]any) (map[string]any, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if err := validateEgressURL(u, s.httpConfig.AllowHosts, s.httpConfig.AllowHTTP, "router.fetchJSON"); err != nil {
		return nil, err
	}
	method := "GET"
	if rawMethod, ok := options["method"].(string); ok && rawMethod != "" {
		method = strings.ToUpper(rawMethod)
	}
	if method != "GET" && method != "POST" {
		return nil, fmt.Errorf("method %s is not allowed", method)
	}
	var body io.Reader
	if rawBody, ok := options["body"]; ok && rawBody != nil {
		b, err := json.Marshal(rawBody)
		if err != nil {
			return nil, fmt.Errorf("body must be JSON-serializable")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rawHeaders, ok := options["headers"].(map[string]any); ok {
		for name, value := range rawHeaders {
			if !scriptHeaderAllowed(name) {
				return nil, fmt.Errorf("header %s is not allowed", name)
			}
			req.Header.Set(name, fmt.Sprint(value))
		}
	}
	for name, value := range s.httpConfig.Headers {
		if !scriptConfigHeaderAllowed(name) {
			return nil, fmt.Errorf("configured header %s is not allowed", name)
		}
		req.Header.Set(name, value)
	}
	timeout := time.Duration(s.httpConfig.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	client := newEgressHTTPClient(timeout, s.httpConfig.AllowHosts, s.httpConfig.AllowHTTP, "router.fetchJSON")
	resp, err := client.Do(req)
	if err != nil {
		return nil, auditSafeHTTPError(err, "router.fetchJSON request failed")
	}
	defer resp.Body.Close()
	limit := s.httpConfig.MaxResponseBytes
	if limit <= 0 {
		limit = 64 << 10
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response exceeds max_response_bytes")
	}
	var data any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("response is not JSON")
		}
	}
	return map[string]any{
		"ok":     resp.StatusCode >= 200 && resp.StatusCode < 300,
		"status": resp.StatusCode,
		"body":   data,
	}, nil
}

func scriptHostAllowed(host string, allowHosts []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range allowHosts {
		allowed = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(allowed), "."))
		if allowed != "" && host == allowed {
			return true
		}
	}
	return false
}

func scriptHeaderAllowed(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "content-type" || name == "accept" || strings.HasPrefix(name, "x-")
}

func scriptConfigHeaderAllowed(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return scriptHeaderAllowed(name) || name == "authorization"
}

func buildScriptCaller(caller *callerRuntime, tokenID string) *scriptCaller {
	if caller == nil {
		return nil
	}
	allow := append([]string(nil), caller.cfg.Allow...)
	user := callerUser(caller.cfg)
	return &scriptCaller{
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

func buildScriptTargets(targets []Target, providers map[string]ProviderConfig) []scriptTarget {
	out := make([]scriptTarget, 0, len(targets))
	for _, target := range targets {
		provider := providers[target.Provider]
		weight := target.Weight
		if weight == 0 {
			weight = 1
		}
		out = append(out, scriptTarget{
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
			Validation:                         buildScriptTargetValidation(target.Validation),
			KeyID:                              provider.KeyID,
			APIKeyEnv:                          provider.APIKeyEnv,
			KeyConfigured:                      provider.APIKey != "",
		})
	}
	return out
}

func buildScriptContract(contract *ModelGroupContract) *scriptContract {
	if contract == nil {
		return nil
	}
	return &scriptContract{
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

func buildScriptTargetValidation(validation *TargetValidation) *scriptTargetValidation {
	if validation == nil {
		return nil
	}
	return &scriptTargetValidation{
		Status:       validation.Status,
		Workload:     validation.Workload,
		ValidatedAt:  validation.ValidatedAt,
		QualityScore: validation.QualityScore,
		PassRate:     validation.PassRate,
		Harness:      validation.Harness,
		Notes:        validation.Notes,
	}
}

func exportScriptOutput(val goja.Value) (scriptOutput, error) {
	return exportDecisionOutput(val.Export(), "route(ctx)")
}

func exportDecisionOutput(exported any, source string) (scriptOutput, error) {
	raw, err := json.Marshal(exported)
	if err != nil {
		return scriptOutput{}, fmt.Errorf("%s returned invalid decision: %w", source, err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return scriptOutput{}, fmt.Errorf("%s returned invalid decision: %w", source, err)
	}
	var out scriptOutput
	if rawTargetIndex, ok := fields["targetIndex"]; ok {
		if err := json.Unmarshal(rawTargetIndex, &out.TargetIndex); err != nil {
			return scriptOutput{}, fmt.Errorf("%s targetIndex must be a number", source)
		}
		out.HasTargetIndex = true
	}
	if rawTarget, ok := fields["target"]; ok {
		if err := json.Unmarshal(rawTarget, &out.Target); err != nil {
			return scriptOutput{}, fmt.Errorf("%s target is invalid", source)
		}
	}
	if rawFallbackIndexes, ok := fields["fallbackIndexes"]; ok {
		if err := json.Unmarshal(rawFallbackIndexes, &out.FallbackIndexes); err != nil {
			return scriptOutput{}, fmt.Errorf("%s fallbackIndexes must be numbers", source)
		}
		out.HasFallbackIndexes = true
	}
	if rawFallbacks, ok := fields["fallbacks"]; ok {
		if err := json.Unmarshal(rawFallbacks, &out.Fallbacks); err != nil {
			return scriptOutput{}, fmt.Errorf("%s fallbacks are invalid", source)
		}
		out.HasFallbacks = true
	}
	if rawClassLabel, ok := fields["classLabel"]; ok {
		if err := json.Unmarshal(rawClassLabel, &out.ClassLabel); err != nil {
			return scriptOutput{}, fmt.Errorf("%s classLabel must be a string", source)
		}
	}
	return out, nil
}

func jsonValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func resolveScriptTarget(out scriptOutput, targets []Target) (int, error) {
	if out.HasTargetIndex {
		return validateTargetIndex(out.TargetIndex, targets)
	}
	if out.Target != nil {
		return targetSelectorIndex(out.Target, targets)
	}
	return 0, fmt.Errorf("script route(ctx) must return targetIndex or target")
}

func resolveScriptFallbacks(out scriptOutput, primary int, targets []Target) ([]Target, error) {
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
		return 0, fmt.Errorf("script selected target index %d outside configured targets", idx)
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
		return 0, fmt.Errorf("unsupported script target selector %T", selector)
	}
}

func findTargetByString(selector string, targets []Target) (int, error) {
	for i, target := range targets {
		if selector == target.Provider || selector == target.Provider+":"+target.Model {
			return i, nil
		}
	}
	return 0, fmt.Errorf("script selected unknown target %q", selector)
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
	return 0, fmt.Errorf("script selected target outside configured list: %s", strings.TrimSpace(string(raw)))
}
