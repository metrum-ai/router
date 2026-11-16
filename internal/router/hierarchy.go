// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	hierarchyPolicyPath        = "/internal/v1/governance/policy"
	hierarchyPolicyMaxBytes    = 64 << 10
	hierarchyPolicyMaxChildren = 1024
	hierarchyPolicyMaxTools    = 128
	hierarchyPolicyMaxTTL      = 10 * time.Minute
	hierarchyPolicyDefaultTTL  = 30 * time.Second
	hierarchyPolicyFailureTTL  = 2 * time.Second
	hierarchyPolicyStaleGrace  = 30 * time.Second
	hierarchyPolicyMaxDepth    = 2
	hierarchyPolicyDepthHeader = "X-Router-Hierarchy-Depth"
)

type hierarchyDepthContextKey struct{}

// HierarchyConfig configures a router that publishes policy to children, pulls
// policy from a parent, or both.
type HierarchyConfig struct {
	Parent HierarchyParentConfig `yaml:"parent" json:"parent,omitempty"`
	Child  HierarchyChildConfig  `yaml:"child" json:"child,omitempty"`
}

// HierarchyParentConfig maps child machine credentials to inherited policy.
type HierarchyParentConfig struct {
	Children []HierarchyChildPolicyConfig `yaml:"children" json:"children,omitempty"`
}

// HierarchyChildPolicyConfig binds a child credential to a caller whose normal
// allow list and quotas are authoritative at this parent.
type HierarchyChildPolicyConfig struct {
	ID             string          `yaml:"id" json:"id"`
	CallerID       string          `yaml:"caller_id" json:"callerId"`
	OrganizationID string          `yaml:"organization_id" json:"organizationId"`
	TeamID         string          `yaml:"team_id" json:"teamId"`
	AllowedGroups  []string        `yaml:"allowed_model_groups" json:"allowedModelGroups,omitempty"`
	ToolAllowList  []string        `yaml:"tool_allow_list" json:"toolAllowList,omitempty"`
	PIIFilter      PIIFilterConfig `yaml:"pii_filter" json:"piiFilter,omitempty"`
}

// HierarchyChildConfig configures a child router's bounded policy cache.
type HierarchyChildConfig struct {
	ID            string `yaml:"id" json:"id,omitempty"`
	ParentURL     string `yaml:"parent_url" json:"parentUrl,omitempty"`
	CredentialEnv string `yaml:"credential_env" json:"credentialEnv,omitempty"`
	CacheTTL      string `yaml:"cache_ttl" json:"cacheTtl,omitempty"`
	TimeoutMS     int    `yaml:"timeout_ms" json:"timeoutMs,omitempty"`
}

// HierarchyPolicy is the small validated snapshot a child may enforce. It has
// no credentials, prompts, or provider data.
type HierarchyPolicy struct {
	Version        int             `json:"version"`
	ChildID        string          `json:"child_id"`
	OrganizationID string          `json:"organization_id"`
	TeamID         string          `json:"team_id"`
	Budget         QuotaConfig     `json:"budget"`
	AllowedGroups  []string        `json:"allowed_model_groups"`
	ToolAllowList  []string        `json:"tool_allow_list"`
	PIIFilter      PIIFilterConfig `json:"pii_filter,omitempty"`
}

func hierarchyChildConfigEmpty(child HierarchyChildConfig) bool {
	return child.ID == "" && child.ParentURL == "" && child.CredentialEnv == "" && child.CacheTTL == "" && child.TimeoutMS == 0
}

func validateHierarchyConfig(cfg HierarchyConfig) error {
	if len(cfg.Parent.Children) > hierarchyPolicyMaxChildren {
		return fmt.Errorf("server.hierarchy.parent.children exceeds %d entries", hierarchyPolicyMaxChildren)
	}
	seenIDs := make(map[string]bool, len(cfg.Parent.Children))
	seenCallers := make(map[string]bool, len(cfg.Parent.Children))
	for i, child := range cfg.Parent.Children {
		prefix := fmt.Sprintf("server.hierarchy.parent.children[%d]", i)
		if !validAdminKeyID(child.ID) || seenIDs[child.ID] {
			return fmt.Errorf("%s id must be unique and valid", prefix)
		}
		seenIDs[child.ID] = true
		if !safeDetectorID(child.CallerID) || seenCallers[child.CallerID] || !validAdminKeyID(child.OrganizationID) || !validAdminKeyID(child.TeamID) {
			return fmt.Errorf("%s must name a unique valid caller, organization, and team", prefix)
		}
		seenCallers[child.CallerID] = true
		if err := validatePIIFilter(prefix+" pii_filter", child.PIIFilter); err != nil {
			return err
		}
		if err := validateHierarchyTools(prefix+" tool_allow_list", child.ToolAllowList); err != nil {
			return err
		}
		if len(child.AllowedGroups) > 256 {
			return fmt.Errorf("%s allowed_model_groups exceeds 256 entries", prefix)
		}
		seenGroups := make(map[string]bool, len(child.AllowedGroups))
		for groupIndex, group := range child.AllowedGroups {
			if !validHierarchyModelGroupName(group) || seenGroups[group] {
				return fmt.Errorf("%s allowed_model_groups[%d] is invalid or duplicated", prefix, groupIndex)
			}
			seenGroups[group] = true
		}
	}
	child := cfg.Child
	if child.ParentURL == "" && child.ID == "" && child.CredentialEnv == "" && child.CacheTTL == "" && child.TimeoutMS == 0 {
		return nil
	}
	if !validAdminKeyID(child.ID) || !validEnvName(child.CredentialEnv) {
		return fmt.Errorf("server.hierarchy.child requires a valid id and credential_env")
	}
	if strings.TrimSpace(os.Getenv(child.CredentialEnv)) == "" {
		return fmt.Errorf("server.hierarchy.child credential_env must resolve to a non-empty value")
	}
	parentURL, err := url.Parse(strings.TrimSpace(child.ParentURL))
	if err != nil || parentURL.Host == "" || parentURL.User != nil || parentURL.RawQuery != "" || parentURL.Fragment != "" || (parentURL.Scheme != "https" && !(parentURL.Scheme == "http" && hierarchyLoopbackHost(parentURL.Hostname()))) {
		return fmt.Errorf("server.hierarchy.child parent_url must be https (http is allowed only for loopback tests)")
	}
	if child.CacheTTL != "" {
		ttl, err := time.ParseDuration(child.CacheTTL)
		if err != nil || ttl <= 0 || ttl > hierarchyPolicyMaxTTL {
			return fmt.Errorf("server.hierarchy.child cache_ttl must be greater than zero and at most %s", hierarchyPolicyMaxTTL)
		}
	}
	if child.TimeoutMS < 0 || child.TimeoutMS > 10000 {
		return fmt.Errorf("server.hierarchy.child timeout_ms must be between 1 and 10000 when set")
	}
	return nil
}

func validateHierarchyCallers(hierarchy HierarchyConfig, callers []CallerConfig) error {
	byID := make(map[string]CallerConfig, len(callers))
	orgBudgets := make(map[string]QuotaConfig, len(hierarchy.Parent.Children))
	for _, caller := range callers {
		byID[caller.ID] = caller
	}
	for _, child := range hierarchy.Parent.Children {
		caller, ok := byID[child.CallerID]
		if !ok {
			return fmt.Errorf("hierarchy child %s maps to unknown parent caller %s", child.ID, child.CallerID)
		}
		allowed := make(map[string]bool, len(caller.Allow))
		for _, group := range caller.Allow {
			if !validHierarchyModelGroupName(group) {
				return fmt.Errorf("parent caller %s has invalid model group name %q", caller.ID, group)
			}
			allowed[group] = true
		}
		for _, group := range child.AllowedGroups {
			if !allowed[group] {
				return fmt.Errorf("hierarchy child %s cannot widen parent caller %s model groups", child.ID, caller.ID)
			}
		}
		if previous, ok := orgBudgets[child.OrganizationID]; ok && !reflect.DeepEqual(previous, caller.Quota) {
			return fmt.Errorf("hierarchy organization %s callers must use the same parent quota", child.OrganizationID)
		}
		orgBudgets[child.OrganizationID] = caller.Quota
	}
	return nil
}

func hierarchyQuotaScopes(hierarchy HierarchyConfig) map[string]string {
	scopes := make(map[string]string, len(hierarchy.Parent.Children))
	for _, child := range hierarchy.Parent.Children {
		scopes[child.CallerID] = "hierarchy-org:" + child.OrganizationID
	}
	return scopes
}

func validHierarchyModelGroupName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_.:", r) {
			continue
		}
		return false
	}
	return true
}

func hierarchyPolicyErrorCode(err error) string {
	if err != nil && strings.HasPrefix(err.Error(), "hierarchy model group mismatch:") {
		return "hierarchy-model-group-mismatch"
	}
	return "hierarchy-policy-unavailable"
}

func validateHierarchyTools(label string, tools []string) error {
	if len(tools) > hierarchyPolicyMaxTools {
		return fmt.Errorf("%s exceeds %d entries", label, hierarchyPolicyMaxTools)
	}
	seen := make(map[string]bool, len(tools))
	for i, tool := range tools {
		name := strings.TrimSpace(tool)
		if name == "" || len(name) > 128 || name != tool || seen[name] {
			return fmt.Errorf("%s[%d] must be unique, non-empty, and at most 128 bytes", label, i)
		}
		seen[name] = true
	}
	return nil
}

type hierarchyPolicyRuntime struct {
	child      HierarchyChildConfig
	credential string
	ttl        time.Duration
	client     *http.Client
	mu         sync.Mutex
	policy     HierarchyPolicy
	fetchedAt  time.Time
	failedAt   time.Time
	lastErr    error
	refreshes  singleflight.Group
}

func newHierarchyPolicyRuntime(child HierarchyChildConfig) (*hierarchyPolicyRuntime, error) {
	credential := os.Getenv(strings.TrimSpace(child.CredentialEnv))
	if strings.TrimSpace(credential) == "" {
		return nil, fmt.Errorf("hierarchy child credential is unavailable")
	}
	ttl := hierarchyPolicyDefaultTTL
	if child.CacheTTL != "" {
		parsed, err := time.ParseDuration(child.CacheTTL)
		if err != nil || parsed <= 0 || parsed > hierarchyPolicyMaxTTL {
			return nil, fmt.Errorf("invalid hierarchy child cache TTL")
		}
		ttl = parsed
	}
	timeout := 2 * time.Second
	if child.TimeoutMS > 0 {
		timeout = time.Duration(child.TimeoutMS) * time.Millisecond
	}
	return &hierarchyPolicyRuntime{
		child:      child,
		credential: credential,
		ttl:        ttl,
		client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (r *hierarchyPolicyRuntime) current(now time.Time) (HierarchyPolicy, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fetchedAt.IsZero() || now.Sub(r.fetchedAt) >= r.ttl {
		return HierarchyPolicy{}, false
	}
	return cloneHierarchyPolicy(r.policy), true
}

func (s *Service) handleHierarchyPolicy(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot.Load()
	if cfg == nil || len(cfg.Server.Hierarchy.Parent.Children) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	rc, ok := s.begin(w, r, "hierarchy")
	if !ok {
		return
	}
	depth := 0
	if rawDepth := strings.TrimSpace(r.Header.Get(hierarchyPolicyDepthHeader)); rawDepth != "" {
		parsed, parseErr := strconv.Atoi(rawDepth)
		if parseErr != nil || parsed < 1 || parsed > hierarchyPolicyMaxDepth {
			s.finish(rc, http.StatusBadRequest, nil)
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_hierarchy_depth"})
			return
		}
		depth = parsed
	}
	mapped := false
	for _, child := range cfg.Server.Hierarchy.Parent.Children {
		if child.CallerID == rc.caller.cfg.ID {
			mapped = true
			break
		}
	}
	if !mapped {
		s.finish(rc, http.StatusForbidden, nil)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "hierarchy_child_not_configured"})
		return
	}
	ctx := context.WithValue(r.Context(), hierarchyDepthContextKey{}, depth)
	policy, err := s.effectiveHierarchyPolicy(ctx, rc.caller)
	if err != nil {
		s.finish(rc, http.StatusServiceUnavailable, nil)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "policy_unavailable"})
		return
	}
	if policy == nil {
		s.finish(rc, http.StatusForbidden, nil)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "hierarchy_child_not_configured"})
		return
	}
	s.finish(rc, http.StatusOK, nil)
	writeJSON(w, http.StatusOK, policy)
}

func cloneHierarchyPolicy(policy HierarchyPolicy) HierarchyPolicy {
	if policy.AllowedGroups != nil {
		policy.AllowedGroups = append([]string{}, policy.AllowedGroups...)
	}
	if policy.ToolAllowList != nil {
		policy.ToolAllowList = append([]string{}, policy.ToolAllowList...)
	}
	if policy.PIIFilter.Rules != nil {
		policy.PIIFilter.Rules = append([]PIIFilterRule{}, policy.PIIFilter.Rules...)
	}
	return policy
}

func intersectHierarchyAllowList(local, inherited []string) []string {
	if local == nil {
		if inherited == nil {
			return nil
		}
		return append([]string{}, inherited...)
	}
	if inherited == nil {
		return append([]string{}, local...)
	}
	allowed := make(map[string]bool, len(inherited))
	for _, value := range inherited {
		allowed[value] = true
	}
	result := make([]string, 0, len(local))
	for _, value := range local {
		if allowed[value] {
			result = append(result, value)
		}
	}
	return result
}

func (s *Service) currentHierarchyPolicy(ctx context.Context) (*HierarchyPolicy, error) {
	if s == nil {
		return nil, nil
	}
	runtime := s.hierarchyPolicy.Load()
	if runtime == nil {
		return nil, nil
	}
	if policy, ok := runtime.current(time.Now()); ok {
		return &policy, nil
	}
	policy, err := runtime.refresh(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

func (s *Service) effectiveHierarchyPolicy(ctx context.Context, caller *callerRuntime) (*HierarchyPolicy, error) {
	var local *HierarchyPolicy
	if caller != nil && s != nil {
		cfg := s.configSnapshot.Load()
		if cfg != nil {
			for _, child := range cfg.Server.Hierarchy.Parent.Children {
				if child.CallerID != caller.cfg.ID {
					continue
				}
				allowed := append([]string{}, caller.cfg.Allow...)
				if child.AllowedGroups != nil {
					allowed = intersectHierarchyAllowList(allowed, child.AllowedGroups)
				}
				local = &HierarchyPolicy{
					Version: 1, ChildID: child.ID, OrganizationID: child.OrganizationID, TeamID: child.TeamID,
					Budget: caller.cfg.Quota, AllowedGroups: allowed, ToolAllowList: child.ToolAllowList, PIIFilter: child.PIIFilter,
				}
				break
			}
		}
	}
	inherited, err := s.currentHierarchyPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if inherited != nil {
		for _, group := range inherited.AllowedGroups {
			if _, ok := s.cfg.Models[group]; !ok {
				return nil, fmt.Errorf("hierarchy model group mismatch: parent allows %q, which is not configured on the child", group)
			}
		}
	}
	if local == nil {
		if inherited == nil {
			return nil, nil
		}
		copy := cloneHierarchyPolicy(*inherited)
		return &copy, nil
	}
	if inherited == nil {
		copy := cloneHierarchyPolicy(*local)
		return &copy, nil
	}
	if local.OrganizationID != inherited.OrganizationID {
		return nil, fmt.Errorf("hierarchy organization scope mismatch")
	}
	merged := cloneHierarchyPolicy(*local)
	merged.AllowedGroups = intersectHierarchyAllowList(local.AllowedGroups, inherited.AllowedGroups)
	merged.ToolAllowList = intersectHierarchyAllowList(local.ToolAllowList, inherited.ToolAllowList)
	merged.OrganizationID = inherited.OrganizationID
	merged.Budget = mergeHierarchyQuotas(local.Budget, inherited.Budget)
	merged.PIIFilter, err = mergeHierarchyPIIFilters(local.PIIFilter, inherited.PIIFilter)
	if err != nil {
		return nil, fmt.Errorf("invalid effective hierarchy policy")
	}
	return &merged, nil
}

func mergeHierarchyQuotas(local, inherited QuotaConfig) QuotaConfig {
	return QuotaConfig{
		Day:     BudgetConfig{Requests: narrowQuotaLimit(local.Day.Requests, inherited.Day.Requests), Tokens: narrowQuotaLimit(local.Day.Tokens, inherited.Day.Tokens)},
		Month:   BudgetConfig{Requests: narrowQuotaLimit(local.Month.Requests, inherited.Month.Requests), Tokens: narrowQuotaLimit(local.Month.Tokens, inherited.Month.Tokens)},
		SoftPct: local.SoftPct,
	}
}

func narrowQuotaLimit(local, inherited int64) int64 {
	if local == 0 {
		return inherited
	}
	if inherited == 0 || local < inherited {
		return local
	}
	return inherited
}

func hierarchyPolicyAllowsGroup(policy *HierarchyPolicy, group string) bool {
	if policy == nil {
		return true
	}
	for _, allowed := range policy.AllowedGroups {
		if allowed == group {
			return true
		}
	}
	return false
}

func hierarchyPolicyAllowsTools(policy *HierarchyPolicy, tools []map[string]any) bool {
	if policy == nil || policy.ToolAllowList == nil {
		return true
	}
	allowed := make(map[string]bool, len(policy.ToolAllowList))
	for _, name := range policy.ToolAllowList {
		allowed[name] = true
	}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if name == "" {
			if fn, ok := tool["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
		}
		if name == "" || !allowed[name] {
			return false
		}
	}
	return true
}

func mergeHierarchyPIIFilters(local, inherited PIIFilterConfig) (PIIFilterConfig, error) {
	secretsEnabled := local.SecretsEnabled || inherited.SecretsEnabled
	if !inherited.Enabled {
		local.SecretsEnabled = secretsEnabled
		return local, nil
	}
	if !local.Enabled {
		inherited.SecretsEnabled = secretsEnabled
		return inherited, nil
	}
	out := local
	out.SecretsEnabled = secretsEnabled
	out.Enabled = true
	out.FailOnMatch = local.FailOnMatch || inherited.FailOnMatch || normalizePIIFilterMode(local) == "fail_on_match" || normalizePIIFilterMode(inherited) == "fail_on_match"
	if out.FailOnMatch {
		out.Mode = "fail_on_match"
	} else {
		out.Mode = "redact_only"
	}
	localApply, inheritedApply := normalizePIIApplyTo(local.ApplyTo), normalizePIIApplyTo(inherited.ApplyTo)
	out.ApplyTo = PIIFilterApplyTo{
		System:         boolPtr(localApply.system || inheritedApply.system),
		Messages:       boolPtr(localApply.messages || inheritedApply.messages),
		ResponsesInput: boolPtr(localApply.responsesInput || inheritedApply.responsesInput),
		ToolResults:    boolPtr(localApply.toolResults || inheritedApply.toolResults),
		ImageURLs:      localApply.imageURLs || inheritedApply.imageURLs,
	}
	if inherited.MaxReplacementsPerRequest > 0 && (out.MaxReplacementsPerRequest == 0 || inherited.MaxReplacementsPerRequest < out.MaxReplacementsPerRequest) {
		out.MaxReplacementsPerRequest = inherited.MaxReplacementsPerRequest
	}
	// Capacity from one input avoids overflowing on the sum of two lengths.
	names := make(map[string]bool, len(local.Rules))
	out.Rules = append([]PIIFilterRule(nil), local.Rules...)
	for _, rule := range local.Rules {
		names[rule.Name] = true
	}
	for _, rule := range inherited.Rules {
		if names[rule.Name] {
			rule.Name = "parent_" + rule.Name
		}
		if names[rule.Name] {
			return PIIFilterConfig{}, fmt.Errorf("parent and child PII rules use colliding names")
		}
		names[rule.Name] = true
		out.Rules = append(out.Rules, rule)
	}
	if err := validatePIIFilter("effective hierarchy policy", out); err != nil {
		return PIIFilterConfig{}, fmt.Errorf("invalid effective hierarchy PII policy")
	}
	return out, nil
}

func (r *hierarchyPolicyRuntime) refresh(ctx context.Context, now time.Time) (HierarchyPolicy, error) {
	depth, _ := ctx.Value(hierarchyDepthContextKey{}).(int)
	if depth >= hierarchyPolicyMaxDepth {
		return HierarchyPolicy{}, fmt.Errorf("hierarchy policy depth exceeded")
	}
	r.mu.Lock()
	if !r.fetchedAt.IsZero() && now.Sub(r.fetchedAt) < r.ttl {
		policy := cloneHierarchyPolicy(r.policy)
		r.mu.Unlock()
		return policy, nil
	}
	if !r.failedAt.IsZero() && now.Sub(r.failedAt) < hierarchyPolicyFailureTTL {
		if policy, ok := r.stalePolicyLocked(now); ok {
			r.mu.Unlock()
			return policy, nil
		}
		err := r.lastErr
		r.mu.Unlock()
		return HierarchyPolicy{}, err
	}
	r.mu.Unlock()
	result := r.refreshes.DoChan("policy", func() (any, error) {
		return r.fetch(context.WithoutCancel(ctx), now)
	})
	select {
	case <-ctx.Done():
		return HierarchyPolicy{}, ctx.Err()
	case fetched := <-result:
		if fetched.Err != nil {
			r.mu.Lock()
			defer r.mu.Unlock()
			if policy, ok := r.stalePolicyLocked(now); ok {
				return policy, nil
			}
			return HierarchyPolicy{}, fetched.Err
		}
		return cloneHierarchyPolicy(fetched.Val.(HierarchyPolicy)), nil
	}
}

func (r *hierarchyPolicyRuntime) stalePolicyLocked(now time.Time) (HierarchyPolicy, bool) {
	if r.fetchedAt.IsZero() || now.Sub(r.fetchedAt) >= r.ttl+hierarchyPolicyStaleGrace {
		return HierarchyPolicy{}, false
	}
	return cloneHierarchyPolicy(r.policy), true
}

func (r *hierarchyPolicyRuntime) fetch(ctx context.Context, now time.Time) (HierarchyPolicy, error) {
	depth, _ := ctx.Value(hierarchyDepthContextKey{}).(int)
	if depth >= hierarchyPolicyMaxDepth {
		return HierarchyPolicy{}, fmt.Errorf("hierarchy policy depth exceeded")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(r.child.ParentURL), "/") + hierarchyPolicyPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return HierarchyPolicy{}, r.markFailure(now, fmt.Errorf("invalid hierarchy parent request"))
	}
	req.Header.Set("Authorization", "Bearer "+r.credential)
	req.Header.Set(hierarchyPolicyDepthHeader, strconv.Itoa(depth+1))
	resp, err := r.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return HierarchyPolicy{}, err
		}
		return HierarchyPolicy{}, r.markFailure(now, fmt.Errorf("hierarchy parent unavailable"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HierarchyPolicy{}, r.markFailure(now, fmt.Errorf("hierarchy parent refused policy request"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, hierarchyPolicyMaxBytes+1))
	if err != nil || len(body) > hierarchyPolicyMaxBytes {
		return HierarchyPolicy{}, r.markFailure(now, fmt.Errorf("invalid hierarchy policy response"))
	}
	var policy HierarchyPolicy
	if err := json.Unmarshal(body, &policy); err != nil || policy.Version != 1 || policy.ChildID != r.child.ID || !validAdminKeyID(policy.ChildID) || strings.TrimSpace(policy.OrganizationID) == "" || strings.TrimSpace(policy.TeamID) == "" {
		return HierarchyPolicy{}, r.markFailure(now, fmt.Errorf("invalid hierarchy policy response"))
	}
	if err := validateHierarchyPolicy(policy); err != nil {
		return HierarchyPolicy{}, r.markFailure(now, err)
	}
	r.mu.Lock()
	r.policy, r.fetchedAt = cloneHierarchyPolicy(policy), now
	r.failedAt, r.lastErr = time.Time{}, nil
	r.mu.Unlock()
	return cloneHierarchyPolicy(policy), nil
}

func (r *hierarchyPolicyRuntime) markFailure(now time.Time, err error) error {
	r.mu.Lock()
	r.failedAt, r.lastErr = now, err
	r.mu.Unlock()
	return err
}

func validateHierarchyPolicy(policy HierarchyPolicy) error {
	if !validAdminKeyID(policy.OrganizationID) || !validAdminKeyID(policy.TeamID) {
		return fmt.Errorf("hierarchy policy organization or team is invalid")
	}
	if policy.Budget.Day.Requests < 0 || policy.Budget.Day.Tokens < 0 || policy.Budget.Month.Requests < 0 || policy.Budget.Month.Tokens < 0 || policy.Budget.SoftPct < 0 || policy.Budget.SoftPct > 100 {
		return fmt.Errorf("hierarchy policy budget is invalid")
	}
	if len(policy.AllowedGroups) > 256 {
		return fmt.Errorf("hierarchy policy exceeds model group limit")
	}
	if err := validateHierarchyTools("hierarchy policy tool_allow_list", policy.ToolAllowList); err != nil {
		return err
	}
	seen := make(map[string]bool, len(policy.AllowedGroups))
	for i, group := range policy.AllowedGroups {
		if !validHierarchyModelGroupName(group) || seen[group] {
			return fmt.Errorf("hierarchy policy allowed_model_groups[%d] is invalid", i)
		}
		seen[group] = true
	}
	return validatePIIFilter("hierarchy policy pii_filter", policy.PIIFilter)
}

func hierarchyLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
