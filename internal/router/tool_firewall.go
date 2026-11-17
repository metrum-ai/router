// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	toolFirewallOff                 = "off"
	toolFirewallShadow              = "shadow"
	toolFirewallEnforce             = "enforce"
	defaultFirewallMaxConversations = 10000
	defaultFirewallMaxPerCaller     = 100
	defaultFirewallConversationTTL  = 30 * time.Minute
	maxFirewallArgumentDepth        = 32
	maxFirewallArgumentNodes        = 8192
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
	lastSyncedMode         string
	compiledRules          []compiledToolFirewallRule
	compiledSequences      []compiledToolFirewallSequence
	compileErr             error
	sequenceClock          uint64
	now                    func() time.Time
}

type toolApprovalRuntime struct {
	mu            sync.Mutex
	records       map[string]*toolApprovalRecord
	byFingerprint map[string]string
	max           int
	now           func() time.Time
}

type toolFirewallConversation struct {
	turn       uint64
	firstTurns map[int]uint64
	expires    time.Time
	callerID   string
	lastUsed   uint64
}

type toolApprovalRecord struct {
	fingerprint string
	decision    string
	expires     time.Time
	created     time.Time
}

type toolFirewallOutcome struct {
	Mode     string
	Action   string
	RuleCode string
}

var (
	firewallDangerousShell = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|\n]{0,1000}\|\s*(?:ba)?sh\b`)
	firewallRemoveRoot     = regexp.MustCompile(`(?i)\brm\s+(?:-[a-z]*r[a-z]*|--recursive)\s+(?:--\s+)?/(?:\s|$|["';&|])`)
	firewallURL            = regexp.MustCompile(`(?i)(?:\b[a-z][a-z0-9+.-]{0,31}://[^\s"'<>]{1,1000}|\b(?:(?:[a-z0-9-]+\.)+[a-z]{2,}|(?:\d{1,3}\.){3}\d{1,3}|\[[0-9a-f:]+\])(?::\d{1,5}(?:/[^\s"'<>]{0,1000})?|/[^\s"'<>]{1,1000}))`)
	firewallSensitivePath  = regexp.MustCompile(`(?i)(?:~/|\$\{?HOME\}?/|/home/[^/]+/|/Users/[^/]+/|/root/)(?:\.aws|\.ssh|\.env)(?:/|\b)`)
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
	for name := range cfg.BuiltInRules {
		switch name {
		case "dangerous_shell", "remove_root", "sensitive_path", "cloud_metadata_ip", "url_egress":
		default:
			return fmt.Errorf("server.tool_firewall.built_in_rules contains unknown rule %q", name)
		}
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
			rule.toolRE, err = regexp.Compile("(?i)(?:" + raw.ToolRegex + ")")
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

func newToolFirewallRuntime(config ...ToolFirewallConfig) *toolFirewallRuntime {
	runtime := &toolFirewallRuntime{conversations: map[string]*toolFirewallConversation{}, now: time.Now}
	if len(config) > 0 {
		runtime.syncPolicy(config[0])
	}
	return runtime
}

func firewallToolCalls(resp *IRResponse, dialects ...string) ([]IRToolCall, bool) {
	if resp == nil {
		return nil, false
	}
	if len(resp.ToolCalls) > 0 {
		return resp.ToolCalls, resp.ToolCallsTruncated
	}
	if resp.Raw == nil {
		return nil, resp.ToolCallsTruncated
	}
	seen := map[string]bool{}
	for _, dialect := range dialects {
		dialect = normalizeDialect(dialect)
		if dialect == "" || seen[dialect] {
			continue
		}
		seen[dialect] = true
		calls, truncated := toolCallsFromResponse(dialect, resp.Raw)
		if len(calls) > 0 {
			return calls, bool(truncated) || resp.ToolCallsTruncated
		}
		if truncated {
			return calls, true
		}
	}
	return nil, resp.ToolCallsTruncated
}

func newToolApprovalRuntime(max int) *toolApprovalRuntime {
	if max <= 0 {
		max = 1000
	}
	return &toolApprovalRuntime{records: map[string]*toolApprovalRecord{}, byFingerprint: map[string]string{}, max: max, now: time.Now}
}

// request records a hold without keeping the inference request open. An
// approved fingerprint is consumed once when the agent retries the same call.
func (s *toolApprovalRuntime) request(id, fingerprint string, ttl time.Duration) (string, string, bool) {
	if s == nil || id == "" || fingerprint == "" {
		return "", "capacity", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if existingID := s.byFingerprint[fingerprint]; existingID != "" {
		record := s.records[existingID]
		if record != nil {
			switch record.decision {
			case "":
				return existingID, "pending", false
			default:
				return existingID, record.decision, false
			}
		}
	}
	if len(s.records) >= s.max {
		return "", "capacity", false
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := s.now()
	s.records[id] = &toolApprovalRecord{fingerprint: fingerprint, expires: now.Add(ttl), created: now}
	s.byFingerprint[fingerprint] = id
	return id, "pending", true
}

func (s *toolApprovalRuntime) lookup(fingerprint string) (string, string) {
	if s == nil || fingerprint == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	id := s.byFingerprint[fingerprint]
	if id == "" || s.records[id] == nil {
		return "", ""
	}
	return id, s.records[id].decision
}

func (s *toolApprovalRuntime) consume(fingerprint string) (string, string) {
	if s == nil || fingerprint == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	id := s.byFingerprint[fingerprint]
	if id == "" || s.records[id] == nil {
		return "", ""
	}
	record := s.records[id]
	if record.decision == "" {
		return id, "pending"
	}
	decision := record.decision
	s.deleteLocked(id)
	return id, decision
}

func (s *toolApprovalRuntime) resolve(id, decision string) bool {
	if s == nil || (decision != "release" && decision != "deny") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	record := s.records[id]
	if record == nil || record.decision != "" {
		return false
	}
	record.decision = decision
	record.expires = s.now().Add(time.Minute)
	return true
}

func (s *toolApprovalRuntime) invalidateAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = map[string]*toolApprovalRecord{}
	s.byFingerprint = map[string]string{}
}

func (s *toolApprovalRuntime) setMax(max int) {
	if s == nil {
		return
	}
	if max <= 0 {
		max = 1000
	}
	s.mu.Lock()
	s.max = max
	s.pruneLocked()
	for len(s.records) > s.max {
		oldestID := ""
		var oldest time.Time
		for id, record := range s.records {
			if oldestID == "" || record.created.Before(oldest) {
				oldestID, oldest = id, record.created
			}
		}
		if oldestID == "" {
			break
		}
		s.deleteLocked(oldestID)
	}
	s.mu.Unlock()
}

func (s *toolApprovalRuntime) pruneLocked() {
	now := s.now()
	for id, record := range s.records {
		if !record.expires.After(now) {
			if record.decision == "" {
				record.decision = "timeout"
				record.expires = now.Add(time.Minute)
			} else {
				s.deleteLocked(id)
			}
		}
	}
}

func (s *toolApprovalRuntime) deleteLocked(id string) {
	record := s.records[id]
	if record == nil {
		return
	}
	delete(s.records, id)
	if s.byFingerprint[record.fingerprint] == id {
		delete(s.byFingerprint, record.fingerprint)
	}
}

func (s *toolFirewallRuntime) check(cfg ToolFirewallConfig, callerID, group string, req *IRRequest, calls []IRToolCall) toolFirewallOutcome {
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != toolFirewallOff {
		s.syncPolicy(cfg)
	}
	return s.checkWithTruncation(cfg, callerID, group, req, calls, false, false)
}

func (s *toolFirewallRuntime) checkWithTruncation(cfg ToolFirewallConfig, callerID, group string, req *IRRequest, calls []IRToolCall, truncated, approved bool) toolFirewallOutcome {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = toolFirewallOff
	}
	out := toolFirewallOutcome{Mode: mode}
	if mode == toolFirewallOff {
		return out
	}
	if mode != toolFirewallOff {
		s.mu.Lock()
		initialized := s.hasSequenceFingerprint
		s.mu.Unlock()
		if !initialized {
			s.syncPolicy(cfg)
		}
	}
	if truncated {
		return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "tool_calls_truncated"}
	}
	if len(calls) == 0 {
		return out
	}
	s.mu.Lock()
	rules := append([]compiledToolFirewallRule(nil), s.compiledRules...)
	sequences := append([]compiledToolFirewallSequence(nil), s.compiledSequences...)
	compileErr := s.compileErr
	s.mu.Unlock()
	if compileErr != nil {
		return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "invalid_config"}
	}
	validatedCalls := make([]IRToolCall, len(calls))
	builtinCalls := make([]IRToolCall, len(calls))
	copy(validatedCalls, calls)
	copy(builtinCalls, calls)
	for i := range validatedCalls {
		if validatedCalls[i].Name == "" {
			return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "invalid_tool_call"}
		}
		kind := strings.ToLower(validatedCalls[i].Kind)
		if validatedCalls[i].Dialect == "openai-responses" && strings.HasSuffix(kind, "_call") && kind != "function_call" && kind != "local_shell_call" && kind != "custom_tool_call" && !isHostedResponsesTool(validatedCalls[i]) {
			return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "unsupported_tool_call"}
		}
		if validatedCalls[i].Arguments == "" {
			continue
		}
		if strings.EqualFold(validatedCalls[i].Kind, "custom_tool_call") {
			// Responses custom tools carry freeform text, not JSON. Keep the
			// decoded string as one matchable value rather than rejecting it.
			// Codex apply_patch input is patch content, so shell/URL built-ins
			// skip it while path, root-delete, and metadata checks still inspect it.
			builtinCalls[i].Arguments = validatedCalls[i].Arguments
			if strings.EqualFold(validatedCalls[i].Name, "apply_patch") {
				builtinCalls[i].Arguments = ""
			}
			continue
		}
		decoded, err := decodeToolFirewallArguments(validatedCalls[i].Arguments)
		if err != nil {
			return toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "invalid_arguments"}
		}
		validatedCalls[i].Arguments = decoded
		if filtered, ok := builtInToolArgumentText(builtinCalls[i]); ok {
			builtinCalls[i].Arguments = filtered
		} else {
			builtinCalls[i].Arguments = decoded
		}
	}
	for i, call := range validatedCalls {
		if !isHostedResponsesTool(call) {
			if code := builtInToolFirewallMatchWithContent(call, builtinCalls[i].Arguments, cfg); code != "" {
				out = toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: code}
				break
			}
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
		key := firewallSequenceKey(callerID, req)
		if key != "" {
			out = s.checkSequence(cfg, key, callerID, validatedCalls, sequences, mode, approved)
		} else if sequenceRelevant(validatedCalls, sequences) {
			out = toolFirewallOutcome{Mode: mode, Action: "deny", RuleCode: "sequence_identity_missing"}
		}
	}
	if mode == toolFirewallShadow && out.Action != "" {
		out.Action = "shadow"
	}
	return out
}

func (s *toolFirewallRuntime) syncPolicy(cfg ToolFirewallConfig) bool {
	return s.syncPolicyWithApprovals(cfg, nil)
}

// syncPolicyWithApprovals invalidates old sequence and approval state under
// the same policy lock so concurrent requests cannot observe the new policy
// while an approval from the old policy remains releasable.
func (s *toolFirewallRuntime) syncPolicyWithApprovals(cfg ToolFirewallConfig, approvals *toolApprovalRuntime) bool {
	if s == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = toolFirewallOff
	}
	if mode == toolFirewallOff {
		s.mu.Lock()
		unchangedOff := s.hasSequenceFingerprint && s.lastSyncedMode == toolFirewallOff
		s.mu.Unlock()
		if unchangedOff {
			return false
		}
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
	builtinNames := make([]string, 0, len(cfg.BuiltInRules))
	for name := range cfg.BuiltInRules {
		builtinNames = append(builtinNames, name)
	}
	sort.Strings(builtinNames)
	for _, name := range builtinNames {
		fmt.Fprintf(&fingerprintInput, "builtin=%q/%t\n", name, cfg.BuiltInRules[name])
	}
	fmt.Fprintf(&fingerprintInput, "approval=%d/%d/conversation=%d/%d\n", cfg.ApprovalTimeoutMS, cfg.MaxPendingApprovals, cfg.ConversationTTLSeconds, cfg.MaxConversations)
	fingerprint := sha256.Sum256([]byte(fingerprintInput.String()))
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSequenceFingerprint || s.sequenceFingerprint != fingerprint {
		var rules []compiledToolFirewallRule
		var sequences []compiledToolFirewallSequence
		var err error
		if !strings.EqualFold(strings.TrimSpace(cfg.Mode), toolFirewallOff) {
			rules, sequences, err = compileToolFirewall(cfg)
		}
		s.conversations = map[string]*toolFirewallConversation{}
		s.compiledRules = rules
		s.compiledSequences = sequences
		s.compileErr = err
		s.sequenceFingerprint = fingerprint
		s.hasSequenceFingerprint = true
		s.lastSyncedMode = mode
		if approvals != nil {
			approvals.invalidateAll()
		}
		return true
	}
	return false
}

func ruleMatchesToolCall(rule compiledToolFirewallRule, call IRToolCall) bool {
	if rule.raw.Tool != "" && !strings.EqualFold(rule.raw.Tool, call.Name) {
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

func isHostedResponsesTool(call IRToolCall) bool {
	if call.Dialect != "openai-responses" {
		return false
	}
	switch strings.ToLower(call.Kind) {
	case "web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call", "browser_search_call", "vector_store_search_call", "mcp_call":
		return true
	default:
		return false
	}
}

func (s *toolFirewallRuntime) hostedToolNamedByPolicy(cfg ToolFirewallConfig, name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.syncPolicy(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.compileErr != nil && strings.EqualFold(strings.TrimSpace(cfg.Mode), toolFirewallEnforce) {
		return true
	}
	for _, rule := range s.compiledRules {
		if rule.raw.Tool == "" && rule.toolRE == nil {
			return true
		}
		if (rule.raw.Tool != "" && strings.EqualFold(rule.raw.Tool, name)) || (rule.toolRE != nil && rule.toolRE.MatchString(name)) {
			return true
		}
	}
	for _, sequence := range s.compiledSequences {
		if strings.EqualFold(sequence.raw.FirstTool, name) || strings.EqualFold(sequence.raw.ThenTool, name) {
			return true
		}
	}
	return false
}

func builtInToolFirewallMatch(call IRToolCall, cfg ToolFirewallConfig) string {
	args := call.Arguments
	return builtInToolFirewallMatchWithContent(call, args, cfg)
}

// builtInToolFirewallMatchWithContent applies content-field exclusions only
// to the URL and shell checks. All other built-ins inspect complete arguments.
func builtInToolFirewallMatchWithContent(call IRToolCall, contentFiltered string, cfg ToolFirewallConfig) string {
	args := call.Arguments
	enabled := func(name string) bool { value, configured := cfg.BuiltInRules[name]; return !configured || value }
	if enabled("dangerous_shell") && firewallDangerousShell.MatchString(contentFiltered) {
		return "dangerous_shell"
	}
	if enabled("remove_root") && firewallRemoveRoot.MatchString(args) {
		return "remove_root"
	}
	if enabled("sensitive_path") && firewallSensitivePath.MatchString(args) {
		return "sensitive_path"
	}
	if enabled("cloud_metadata_ip") && strings.Contains(args, "169.254.169.254") {
		return "cloud_metadata_ip"
	}
	if !enabled("url_egress") {
		return ""
	}
	urls := firewallURL.FindAllString(contentFiltered, 17)
	if len(urls) > 16 {
		return "too_many_urls"
	}
	for _, raw := range urls {
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || validateEgressURL(u, cfg.URLAllowHosts, false, "tool_firewall") != nil {
			return "url_not_allowed"
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLinkLocalUnicast() {
			return "cloud_metadata_ip"
		}
	}
	return ""
}

func firewallSequenceKey(callerID string, req *IRRequest) string {
	if strings.TrimSpace(callerID) == "" {
		return ""
	}
	first := firstUserTurnText(req)
	if first == "" {
		return ""
	}
	material := strings.TrimSpace(callerID) + "\x00" + first
	sum := sha256.Sum256([]byte(material))
	return fmt.Sprintf("%x", sum[:16])
}

func builtInToolArgumentText(call IRToolCall) (string, bool) {
	name := strings.ToLower(call.Name)
	fields := map[string]bool{}
	switch name {
	case "write_file":
		fields["content"] = true
	case "apply_patch":
		fields["input"], fields["patch"] = true, true
	case "edit":
		fields["new_string"] = true
	default:
		return call.Arguments, false
	}
	var value any
	if json.Unmarshal([]byte(call.Arguments), &value) != nil {
		return call.Arguments, false
	}
	if object, ok := value.(map[string]any); ok {
		for key := range fields {
			delete(object, key)
		}
	}
	var b strings.Builder
	budget := maxFirewallArgumentNodes
	if appendFirewallArgumentStrings(&b, value, 0, &budget) != nil {
		return call.Arguments, false
	}
	return b.String(), true
}

func decodeToolFirewallArguments(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	value, err := parseToolFirewallArguments(raw)
	if err != nil {
		return "", err
	}
	var decoded strings.Builder
	budget := maxFirewallArgumentNodes
	if err := appendFirewallArgumentStrings(&decoded, value, 0, &budget); err != nil {
		return "", err
	}
	return decoded.String(), nil
}

func parseToolFirewallArguments(raw string) (any, error) {
	if len(raw) > maxToolCallArgumentBytes {
		return nil, fmt.Errorf("tool arguments exceed the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("tool arguments contain trailing JSON")
		}
		return nil, err
	}
	budget := maxFirewallArgumentNodes
	if err := appendFirewallArgumentStrings(&strings.Builder{}, value, 0, &budget); err != nil {
		return nil, err
	}
	return value, nil
}

func toolCallApprovalFingerprint(callerID string, calls []IRToolCall) (string, bool) {
	if strings.TrimSpace(callerID) == "" || len(calls) == 0 || len(calls) > maxToolCallsPerResponse {
		return "", false
	}
	type fingerprintCall struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	}
	canonical := make([]fingerprintCall, 0, len(calls))
	for _, call := range calls {
		var value any
		if call.Arguments == "" {
			value = map[string]any{}
		} else if strings.EqualFold(call.Kind, "custom_tool_call") {
			value = call.Arguments
		} else {
			var err error
			value, err = parseToolFirewallArguments(call.Arguments)
			if err != nil {
				return "", false
			}
		}
		canonical = append(canonical, fingerprintCall{Name: strings.ToLower(call.Name), Arguments: value})
	}
	encoded, err := json.Marshal(struct {
		Caller string            `json:"caller"`
		Calls  []fingerprintCall `json:"calls"`
	}{Caller: strings.TrimSpace(callerID), Calls: canonical})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:]), true
}

func approvalTTL(cfg ToolFirewallConfig) time.Duration {
	if cfg.ApprovalTimeoutMS > 0 {
		return time.Duration(cfg.ApprovalTimeoutMS) * time.Millisecond
	}
	return time.Minute
}

func (s *Service) inspectToolCallsForFirewall(callerID string, req *IRRequest, calls []IRToolCall, truncated, approved bool) toolFirewallOutcome {
	if s == nil || s.toolFirewall == nil || s.cfg == nil {
		return toolFirewallOutcome{Mode: toolFirewallOff}
	}
	return s.toolFirewall.checkWithTruncation(s.cfg.Server.ToolFirewall, callerID, "", req, calls, truncated, approved)
}

type toolFirewallDisposition struct {
	pending    bool
	blocked    bool
	approvalID string
	ruleCode   string
}

func (s *Service) resolveToolFirewallDecision(rc *requestContext, callerID, fingerprint, approvalID, approvalState string, outcome toolFirewallOutcome, policy ToolFirewallConfig) toolFirewallDisposition {
	disposition := toolFirewallDisposition{approvalID: approvalID, ruleCode: outcome.RuleCode}
	if outcome.Mode == toolFirewallShadow && outcome.Action != "" {
		rc.trace("tool_firewall_decision", "", Target{}, 0, http.StatusOK, outcome.RuleCode, false, 0)
		s.recordToolFirewallDecisionEvent(rc, "", "shadow", outcome.RuleCode)
		return disposition
	}
	if outcome.Mode != toolFirewallEnforce {
		return disposition
	}
	if outcome.Action == "deny" {
		disposition.blocked = true
		disposition.ruleCode = outcome.RuleCode
	} else {
		switch approvalState {
		case "pending":
			disposition.pending = true
			if disposition.ruleCode == "" {
				disposition.ruleCode = "sequence_rule"
			}
		case "release":
			_, decision := s.toolApprovals.consume(fingerprint)
			if decision != "release" || outcome.Action == "hold" {
				disposition.blocked = true
				disposition.ruleCode = "approval_retry_invalid"
				break
			}
			rc.trace("tool_firewall_approval_resolved", "", Target{}, 0, http.StatusOK, "approval_released", false, 0)
			s.recordToolFirewallDecisionEvent(rc, approvalID, "released", "sequence_rule")
		case "deny", "timeout":
			disposition.blocked = true
			disposition.ruleCode = "sequence_rule"
			rc.trace("tool_firewall_approval_resolved", "", Target{}, 0, http.StatusOK, "approval_"+approvalState, false, 0)
			s.recordToolFirewallDecisionEvent(rc, approvalID, approvalState, disposition.ruleCode)
		default:
			if outcome.Action != "hold" {
				return disposition
			}
			newID := "approval_" + strings.TrimPrefix(requestID(), "req_")
			var created bool
			disposition.approvalID, approvalState, created = s.toolApprovals.request(newID, fingerprint, approvalTTL(policy))
			if disposition.approvalID == "" {
				disposition.approvalID = newID
			}
			switch approvalState {
			case "pending":
				disposition.pending = true
				if created {
					rc.trace("tool_firewall_approval_pending", "approval_id="+disposition.approvalID, Target{}, 0, http.StatusOK, "approval_pending", false, 0)
					s.recordToolFirewallDecisionEvent(rc, disposition.approvalID, "pending", outcome.RuleCode)
				}
			case "release":
				_, consumedDecision := s.toolApprovals.consume(fingerprint)
				if consumedDecision != "release" {
					disposition.blocked = true
					disposition.ruleCode = "approval_retry_invalid"
					break
				}
				rc.trace("tool_firewall_approval_resolved", "", Target{}, 0, http.StatusOK, "approval_released", false, 0)
				s.recordToolFirewallDecisionEvent(rc, disposition.approvalID, "released", outcome.RuleCode)
			case "deny", "timeout":
				disposition.blocked = true
				disposition.ruleCode = "sequence_rule"
				s.recordToolFirewallDecisionEvent(rc, disposition.approvalID, approvalState, disposition.ruleCode)
			default:
				disposition.blocked = true
				disposition.ruleCode = "approval_capacity"
			}
		}
	}
	if disposition.blocked {
		rc.trace("tool_firewall_decision", "", Target{}, 0, http.StatusOK, disposition.ruleCode, false, 0)
		s.recordToolFirewallDecisionEvent(rc, disposition.approvalID, "denied", disposition.ruleCode)
	}
	return disposition
}

func appendFirewallArgumentStrings(out *strings.Builder, value any, depth int, budget *int) error {
	if depth > maxFirewallArgumentDepth {
		return fmt.Errorf("tool arguments exceed maximum nesting depth")
	}
	*budget--
	if *budget < 0 {
		return fmt.Errorf("tool arguments exceed maximum node count")
	}
	appendString := func(value string) {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(value)
	}
	switch typed := value.(type) {
	case string:
		appendString(typed)
	case bool:
		appendString(fmt.Sprint(typed))
	case json.Number:
		appendString(string(typed))
	case []any:
		for _, child := range typed {
			if err := appendFirewallArgumentStrings(out, child, depth+1, budget); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := typed[key]
			appendString(key)
			if err := appendFirewallArgumentStrings(out, child, depth+1, budget); err != nil {
				return err
			}
		}
	}
	return nil
}

func sequenceRelevant(calls []IRToolCall, rules []compiledToolFirewallSequence) bool {
	for _, call := range calls {
		for _, rule := range rules {
			if strings.EqualFold(call.Name, rule.raw.FirstTool) || strings.EqualFold(call.Name, rule.raw.ThenTool) {
				return true
			}
		}
	}
	return false
}

func (s *toolFirewallRuntime) checkSequence(cfg ToolFirewallConfig, key, callerID string, calls []IRToolCall, rules []compiledToolFirewallSequence, mode string, approved bool) toolFirewallOutcome {
	if s == nil || key == "" || len(calls) == 0 {
		return toolFirewallOutcome{Mode: mode}
	}
	out := toolFirewallOutcome{Mode: mode}
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
	for existing, entry := range s.conversations {
		if !entry.expires.After(now) {
			delete(s.conversations, existing)
		}
	}
	entry := s.conversations[key]
	if entry != nil && !entry.expires.After(now) {
		delete(s.conversations, key)
		entry = nil
	}
	turn := uint64(1)
	firstTurns := map[int]uint64{}
	if entry != nil {
		turn = entry.turn + 1
		for k, v := range entry.firstTurns {
			firstTurns[k] = v
		}
	}
	for _, call := range calls {
		for i, rule := range rules {
			if strings.EqualFold(call.Name, rule.raw.ThenTool) {
				if first, ok := firstTurns[i]; ok && turn-first <= uint64(rule.raw.WithinTurns) {
					if rule.raw.Action == "hold" {
						// Keep the hold trigger live until a retry consumes its own
						// approval. Refresh it so pending retries cannot age out of
						// the sequence window and bypass the hold.
						firstTurns[i] = turn
						if approved {
							continue
						}
						out = toolFirewallOutcome{Mode: mode, Action: "hold", RuleCode: "sequence_rule"}
						break
					}
					out = toolFirewallOutcome{Mode: mode, Action: rule.raw.Action, RuleCode: "sequence_rule"}
					break
				}
			}
		}
		if out.Action != "" {
			break
		}
		for i, rule := range rules {
			if strings.EqualFold(call.Name, rule.raw.FirstTool) && (rule.firstRE == nil || rule.firstRE.MatchString(call.Arguments)) {
				firstTurns[i] = turn
			}
		}
	}
	if entry == nil && len(firstTurns) == 0 {
		return toolFirewallOutcome{Mode: mode}
	}
	if entry == nil {
		perCallerMax := defaultFirewallMaxPerCaller
		if perCallerMax > max {
			perCallerMax = max
		}
		callerEntries := 0
		oldestKey := ""
		var oldestUse uint64
		for k, e := range s.conversations {
			if e.callerID == callerID {
				callerEntries++
				if oldestKey == "" || e.lastUsed < oldestUse {
					oldestKey, oldestUse = k, e.lastUsed
				}
			}
		}
		if callerEntries >= perCallerMax && oldestKey != "" {
			delete(s.conversations, oldestKey)
		} else if len(s.conversations) >= max {
			for k, e := range s.conversations {
				if oldestKey == "" || e.lastUsed < oldestUse {
					oldestKey, oldestUse = k, e.lastUsed
				}
			}
			if oldestKey != "" {
				delete(s.conversations, oldestKey)
			}
		}
		entry = &toolFirewallConversation{callerID: callerID}
		s.conversations[key] = entry
	}
	s.sequenceClock++
	entry.lastUsed = s.sequenceClock
	entry.turn = turn
	entry.firstTurns = firstTurns
	entry.expires = now.Add(ttl)
	return out
}
