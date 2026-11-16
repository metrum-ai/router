// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	toolFirewallOff                 = "off"
	toolFirewallShadow              = "shadow"
	toolFirewallEnforce             = "enforce"
	defaultFirewallMaxConversations = 10000
	defaultFirewallConversationTTL  = 30 * time.Minute
	toolFirewallCacheSweepPerCall   = 32
)

type compiledToolFirewallRule struct {
	toolRE *regexp.Regexp
	argsRE *regexp.Regexp
	raw    ToolFirewallDenyRule
}

type compiledToolFirewallSequence struct {
	firstRE *regexp.Regexp
	raw     ToolFirewallSequenceRule
}

type toolFirewallRuntime struct {
	mu                     sync.Mutex
	conversations          map[string]*toolFirewallConversation
	sequenceFingerprint    [32]byte
	hasSequenceFingerprint bool
	now                    func() time.Time
}

type toolApprovalRuntime struct {
	mu      sync.Mutex
	pending map[string]chan string
	max     int
}

type toolFirewallConversation struct {
	turn       uint64
	firstTurns map[int]uint64
	expires    time.Time
}

type toolFirewallOutcome struct {
	Mode     string
	Action   string
	RuleCode string
}

var (
	firewallDangerousShell = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|\n]{0,1000}\|\s*(?:ba)?sh\b`)
	firewallRemoveRoot     = regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*\b[^\n]{0,128}\s/`)
	firewallURL            = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{0,31}://[^\s"'<>]{1,1000}`)
	firewallSensitivePath  = regexp.MustCompile(`(?i)(?:~/|/home/[^/]+/|/root/)(?:\.aws|\.ssh|\.env)(?:/|\b)`)
)

func validateToolFirewallConfig(cfg ToolFirewallConfig, adminEnabled bool) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", toolFirewallOff, toolFirewallShadow, toolFirewallEnforce:
	default:
		return fmt.Errorf("server.tool_firewall.mode must be off, shadow, or enforce")
	}
	if cfg.ApprovalTimeoutMS < 0 || cfg.ApprovalTimeoutMS > 24*60*60*1000 {
		return fmt.Errorf("server.tool_firewall.approval_timeout_ms must be between 1 and 86400000 when set")
	}
	if cfg.MaxPendingApprovals < 0 || cfg.MaxPendingApprovals > 10000 || cfg.MaxConversations < 0 || cfg.MaxConversations > 100000 || cfg.ConversationTTLSeconds < 0 || cfg.ConversationTTLSeconds > 86400 {
		return fmt.Errorf("server.tool_firewall cache limits are outside supported bounds")
	}
	if len(cfg.Deny) > 128 || len(cfg.Sequences) > 64 || len(cfg.URLAllowHosts) > 128 {
		return fmt.Errorf("server.tool_firewall rule and host counts exceed supported bounds")
	}
	for i, rule := range cfg.Deny {
		if len(rule.Tool) > 128 || len(rule.ToolRegex) > 512 || len(rule.ArgsRegex) > 512 || len(rule.AnyArgContains) > 256 {
			return fmt.Errorf("server.tool_firewall.deny[%d] exceeds supported matcher length", i)
		}
		if strings.TrimSpace(rule.Tool) == "" && strings.TrimSpace(rule.ToolRegex) == "" && strings.TrimSpace(rule.ArgsRegex) == "" && strings.TrimSpace(rule.AnyArgContains) == "" {
			return fmt.Errorf("server.tool_firewall.deny[%d] must configure a tool or argument matcher", i)
		}
		for label, expression := range map[string]string{"tool_regex": rule.ToolRegex, "args_regex": rule.ArgsRegex} {
			if strings.TrimSpace(expression) != "" {
				if _, err := regexp.Compile(expression); err != nil {
					return fmt.Errorf("server.tool_firewall.deny[%d].%s is invalid: %w", i, label, err)
				}
			}
		}
	}
	for i, rule := range cfg.Sequences {
		if len(rule.FirstTool) > 128 || len(rule.ThenTool) > 128 || len(rule.FirstArgsRegex) > 512 {
			return fmt.Errorf("server.tool_firewall.sequences[%d] exceeds supported matcher length", i)
		}
		if rule.FirstTool == "" || rule.ThenTool == "" || rule.WithinTurns < 1 || rule.WithinTurns > 100 || (rule.Action != "deny" && rule.Action != "hold") {
			return fmt.Errorf("server.tool_firewall.sequences[%d] requires first_tool, then_tool, within_turns 1..100, and action deny or hold", i)
		}
		if rule.FirstArgsRegex != "" {
			if _, err := regexp.Compile(rule.FirstArgsRegex); err != nil {
				return fmt.Errorf("server.tool_firewall.sequences[%d].first_args_regex is invalid: %w", i, err)
			}
		}
	}
	if len(cfg.Sequences) > 0 && mode == toolFirewallEnforce && !adminEnabled && hasHoldSequence(cfg.Sequences) {
		return fmt.Errorf("server.tool_firewall hold action requires the admin plane")
	}
	for _, host := range cfg.URLAllowHosts {
		if strings.TrimSpace(host) == "" || len(host) > 253 || strings.ContainsAny(host, "/:@?#") {
			return fmt.Errorf("server.tool_firewall.url_allow_hosts must contain hostnames only")
		}
	}
	return nil
}

func hasHoldSequence(rules []ToolFirewallSequenceRule) bool {
	for _, rule := range rules {
		if rule.Action == "hold" {
			return true
		}
	}
	return false
}

func compileToolFirewall(cfg ToolFirewallConfig) ([]compiledToolFirewallRule, []compiledToolFirewallSequence, error) {
	rules := make([]compiledToolFirewallRule, 0, len(cfg.Deny))
	for _, raw := range cfg.Deny {
		rule := compiledToolFirewallRule{raw: raw}
		var err error
		if raw.ToolRegex != "" {
			rule.toolRE, err = regexp.Compile(raw.ToolRegex)
			if err != nil {
				return nil, nil, err
			}
		}
		if raw.ArgsRegex != "" {
			rule.argsRE, err = regexp.Compile(raw.ArgsRegex)
			if err != nil {
				return nil, nil, err
			}
		}
		rules = append(rules, rule)
	}
	sequences := make([]compiledToolFirewallSequence, 0, len(cfg.Sequences))
	for _, raw := range cfg.Sequences {
		rule := compiledToolFirewallSequence{raw: raw}
		var err error
		if raw.FirstArgsRegex != "" {
			rule.firstRE, err = regexp.Compile(raw.FirstArgsRegex)
			if err != nil {
				return nil, nil, err
			}
		}
		sequences = append(sequences, rule)
	}
	return rules, sequences, nil
}

func newToolFirewallRuntime() *toolFirewallRuntime {
	return &toolFirewallRuntime{conversations: map[string]*toolFirewallConversation{}, now: time.Now}
}

func firewallToolCalls(resp *IRResponse, dialects ...string) []IRToolCall {
	if resp == nil {
		return nil
	}
	if len(resp.ToolCalls) > 0 {
		return resp.ToolCalls
	}
	if resp.Raw == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, dialect := range dialects {
		dialect = normalizeDialect(dialect)
		if dialect == "" || seen[dialect] {
			continue
		}
		seen[dialect] = true
		calls, _ := toolCallsFromResponse(dialect, resp.Raw)
		if len(calls) > 0 {
			return calls
		}
	}
	return nil
}

func newToolApprovalRuntime(max int) *toolApprovalRuntime {
	if max <= 0 {
		max = 1000
	}
	return &toolApprovalRuntime{pending: map[string]chan string{}, max: max}
}

func (s *toolApprovalRuntime) wait(ctx context.Context, id string, timeout time.Duration, onPending ...func()) (string, bool) {
	if s == nil || id == "" {
		return "", false
	}
	result := make(chan string, 1)
	s.mu.Lock()
	if len(s.pending) >= s.max {
		s.mu.Unlock()
		return "", false
	}
	s.pending[id] = result
	s.mu.Unlock()
	for _, notify := range onPending {
		if notify != nil {
			notify()
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case decision := <-result:
		return decision, true
	case <-ctx.Done():
	case <-timer.C:
	}
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
	return "deny", false
}

func (s *toolApprovalRuntime) resolve(id, decision string) bool {
	if s == nil || (decision != "release" && decision != "deny") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.pending[id]
	if ch == nil {
		return false
	}
	delete(s.pending, id)
	ch <- decision
	return true
}

func (s *toolApprovalRuntime) denyAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, result := range s.pending {
		delete(s.pending, id)
		result <- "deny"
	}
}

func (s *toolFirewallRuntime) check(cfg ToolFirewallConfig, callerID, group string, req *IRRequest, calls []IRToolCall) toolFirewallOutcome {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = toolFirewallOff
	}
	s.syncPolicy(cfg)
	out := toolFirewallOutcome{Mode: mode}
	if mode == toolFirewallOff {
		return out
	}
	rules, sequences, err := compileToolFirewall(cfg)
	if err != nil {
		return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "invalid_config"}
	}
	for _, call := range calls {
		if code := builtInToolFirewallMatch(call, cfg.URLAllowHosts); code != "" {
			out = toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: code}
			break
		}
		for _, rule := range rules {
			if ruleMatchesToolCall(rule, call) {
				out = toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "configured_deny"}
				break
			}
		}
		if out.Action != "" {
			break
		}
	}
	if out.Action == "" && len(sequences) > 0 {
		key := ""
		if callerConversationIdentity(req) != "" {
			key = buildExternalPolicyConversationKey(ExternalPolicyConversationKeyConfig{}, callerID, group, req)
		}
		if key != "" {
			out = s.checkSequence(cfg, key, calls, sequences, mode)
		} else if len(calls) > 0 {
			out = toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "sequence_identity_missing"}
		}
	}
	if mode == toolFirewallShadow && out.Action != "" {
		out.Action = "shadow"
	}
	return out
}

func (s *toolFirewallRuntime) syncPolicy(cfg ToolFirewallConfig) bool {
	if s == nil {
		return false
	}
	var fingerprintInput strings.Builder
	fmt.Fprintf(&fingerprintInput, "mode=%q\n", strings.ToLower(strings.TrimSpace(cfg.Mode)))
	for _, host := range cfg.URLAllowHosts {
		fmt.Fprintf(&fingerprintInput, "host=%q\n", host)
	}
	for _, rule := range cfg.Deny {
		fmt.Fprintf(&fingerprintInput, "deny=%q/%q/%q/%q\n", rule.Tool, rule.ToolRegex, rule.ArgsRegex, rule.AnyArgContains)
	}
	for _, rule := range cfg.Sequences {
		fmt.Fprintf(&fingerprintInput, "%q/%q/%q/%d/%q\n", rule.FirstTool, rule.FirstArgsRegex, rule.ThenTool, rule.WithinTurns, rule.Action)
	}
	fmt.Fprintf(&fingerprintInput, "approval=%d/%d/conversation=%d/%d\n", cfg.ApprovalTimeoutMS, cfg.MaxPendingApprovals, cfg.ConversationTTLSeconds, cfg.MaxConversations)
	fingerprint := sha256.Sum256([]byte(fingerprintInput.String()))
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSequenceFingerprint || s.sequenceFingerprint != fingerprint {
		s.conversations = map[string]*toolFirewallConversation{}
		s.sequenceFingerprint = fingerprint
		s.hasSequenceFingerprint = true
		return true
	}
	return false
}

func ruleMatchesToolCall(rule compiledToolFirewallRule, call IRToolCall) bool {
	if rule.raw.Tool != "" && rule.raw.Tool != call.Name {
		return false
	}
	if rule.toolRE != nil && !rule.toolRE.MatchString(call.Name) {
		return false
	}
	if rule.argsRE != nil && !rule.argsRE.MatchString(call.Arguments) {
		return false
	}
	if rule.raw.AnyArgContains != "" && !strings.Contains(call.Arguments, rule.raw.AnyArgContains) {
		return false
	}
	return true
}

func builtInToolFirewallMatch(call IRToolCall, allowHosts []string) string {
	args := call.Arguments
	if firewallDangerousShell.MatchString(args) {
		return "dangerous_shell"
	}
	if firewallRemoveRoot.MatchString(args) {
		return "remove_root"
	}
	if firewallSensitivePath.MatchString(args) {
		return "sensitive_path"
	}
	if strings.Contains(args, "169.254.169.254") {
		return "cloud_metadata_ip"
	}
	urls := firewallURL.FindAllString(args, 17)
	if len(urls) > 16 {
		return "too_many_urls"
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || validateEgressURL(u, allowHosts, false, "tool_firewall") != nil {
			return "url_not_allowed"
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLinkLocalUnicast() {
			return "cloud_metadata_ip"
		}
	}
	return ""
}

func (s *toolFirewallRuntime) checkSequence(cfg ToolFirewallConfig, key string, calls []IRToolCall, rules []compiledToolFirewallSequence, mode string) toolFirewallOutcome {
	if s == nil || key == "" {
		return toolFirewallOutcome{Mode: mode}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	ttl := defaultFirewallConversationTTL
	if cfg.ConversationTTLSeconds > 0 {
		ttl = time.Duration(cfg.ConversationTTLSeconds) * time.Second
	}
	max := cfg.MaxConversations
	if max <= 0 {
		max = defaultFirewallMaxConversations
	}
	// Keep request work independent of the configured cache maximum. A bounded
	// map sample reclaims expired entries over time; if capacity is still full,
	// the caller fails closed instead of evicting live sequence history.
	scanned := 0
	for existing, entry := range s.conversations {
		if scanned >= toolFirewallCacheSweepPerCall {
			break
		}
		scanned++
		if !entry.expires.After(now) {
			delete(s.conversations, existing)
		}
	}
	entry := s.conversations[key]
	if entry != nil && !entry.expires.After(now) {
		delete(s.conversations, key)
		entry = nil
	}
	if entry == nil {
		if len(s.conversations) >= max {
			// Evicting another conversation would silently forget the first half
			// of a security sequence. Fail closed while state capacity is full.
			return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "sequence_state_capacity"}
		}
		entry = &toolFirewallConversation{firstTurns: map[int]uint64{}}
		s.conversations[key] = entry
	}
	entry.turn++
	entry.expires = now.Add(ttl)
	for _, call := range calls {
		for i, rule := range rules {
			if strings.EqualFold(call.Name, rule.raw.FirstTool) && (rule.firstRE == nil || rule.firstRE.MatchString(call.Arguments)) {
				entry.firstTurns[i] = entry.turn
			}
			if strings.EqualFold(call.Name, rule.raw.ThenTool) {
				if first, ok := entry.firstTurns[i]; ok && entry.turn-first <= uint64(rule.raw.WithinTurns) {
					delete(entry.firstTurns, i)
					return toolFirewallOutcome{Mode: mode, Action: rule.raw.Action, RuleCode: "sequence_rule"}
				}
			}
		}
	}
	return toolFirewallOutcome{Mode: mode}
}
