// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxTranslationFieldEventsPerAttempt = 32

func (s *Service) recordRequestShapeTelemetry(rc *requestContext, req *IRRequest, callerDialect string, totalRequestBytes int) {
	if rc == nil || req == nil {
		return
	}
	rec := requestShapeFromIR(s, req, callerDialect, rc.rec.Client, totalRequestBytes)
	rc.rec.RequestShape = &rec
}

func requestShapeFromIR(s *Service, req *IRRequest, callerDialect, client string, totalRequestBytes int) requestShapeLogRecord {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	raw := req.Raw
	inputItems, messages := countInputItemsAndMessages(callerDialect, raw, req)
	roleCounts := countRoles(raw, req)
	toolResultCount, functionOutputCount := countToolOutputs(raw, req)
	inputTextBytes := requestInputTextBytes(req)
	toolSchemaBytes := jsonValueLen(req.Tools)
	estimatedInput := estimateTokens(req)
	reasoningBudget := req.Reasoning.BudgetTokens
	if reasoningBudget == 0 {
		reasoningBudget, _ = numberAsInt(nestedValue(raw, "thinking", "budget_tokens"))
	}
	shape := requestShapeLogRecord{
		TS:                         now,
		InboundDialect:             callerDialect,
		RequestedModel:             req.Model,
		ResolvedGroup:              req.Model,
		Client:                     client,
		Stream:                     req.Stream,
		InputItemCount:             inputItems,
		MessageCount:               messages,
		SystemMessageCount:         roleCounts["system"],
		DeveloperMessageCount:      roleCounts["developer"],
		UserMessageCount:           roleCounts["user"],
		AssistantMessageCount:      roleCounts["assistant"],
		ToolResultCount:            toolResultCount,
		FunctionCallOutputCount:    functionOutputCount,
		ToolCount:                  len(req.Tools),
		ToolChoiceMode:             toolChoiceMode(raw["tool_choice"]),
		ParallelToolCallsPresent:   rawValuePresent(raw, "parallel_tool_calls"),
		ResponseFormatPresent:      rawValuePresent(raw, "response_format") || nestedValuePresent(raw, "text", "format"),
		StructuredOutputPresent:    requestHasStructuredOutput(req),
		ReasoningPresent:           requestRequiresReasoning(req) || rawValuePresent(raw, "reasoning") || rawValuePresent(raw, "thinking") || rawValuePresent(raw, "reasoning_effort"),
		ReasoningEffortBucket:      reasoningEffortBucket(req.Reasoning.Effort),
		ReasoningBudgetBucket:      countBucket(reasoningBudget),
		IncludePresent:             rawValuePresent(raw, "include"),
		TruncationPresent:          rawValuePresent(raw, "truncation"),
		MetadataPresent:            rawValuePresent(raw, "metadata"),
		StorePresent:               rawValuePresent(raw, "store"),
		PreviousResponseIDPresent:  rawValuePresent(raw, "previous_response_id"),
		ImageCount:                 requestImageCount(req),
		AudioPresent:               rawContainsModality(raw, "audio"),
		VideoPresent:               rawContainsModality(raw, "video"),
		InputTextBytesBucket:       byteBucket(inputTextBytes),
		ToolSchemaBytesBucket:      byteBucket(toolSchemaBytes),
		TotalRequestBytesBucket:    byteBucket(totalRequestBytes),
		EstimatedInputTokensBucket: contextBucket(estimatedInput),
		RequestedOutputCapField:    req.MaxTokensField,
		RequestedOutputCapBucket:   maxTokenBucket(req.MaxTokens),
	}
	shape.ToolSchemaFingerprint = s.shapeHMAC("tool-schema", sanitizedShapePayload(req.Tools))
	shape.RequestShapeFingerprint = s.shapeHMAC("request-shape", map[string]any{
		"inbound_dialect":                 shape.InboundDialect,
		"stream":                          shape.Stream,
		"input_item_count":                shape.InputItemCount,
		"message_count":                   shape.MessageCount,
		"role_counts":                     roleCounts,
		"tool_result_count":               shape.ToolResultCount,
		"function_call_output_count":      shape.FunctionCallOutputCount,
		"tool_count":                      shape.ToolCount,
		"tool_choice_mode":                shape.ToolChoiceMode,
		"parallel_tool_calls_present":     shape.ParallelToolCallsPresent,
		"response_format_present":         shape.ResponseFormatPresent,
		"structured_output_present":       shape.StructuredOutputPresent,
		"reasoning_present":               shape.ReasoningPresent,
		"reasoning_effort_bucket":         shape.ReasoningEffortBucket,
		"reasoning_budget_bucket":         shape.ReasoningBudgetBucket,
		"include_present":                 shape.IncludePresent,
		"truncation_present":              shape.TruncationPresent,
		"metadata_present":                shape.MetadataPresent,
		"store_present":                   shape.StorePresent,
		"previous_response_id_present":    shape.PreviousResponseIDPresent,
		"image_count":                     shape.ImageCount,
		"audio_present":                   shape.AudioPresent,
		"video_present":                   shape.VideoPresent,
		"input_text_bytes_bucket":         shape.InputTextBytesBucket,
		"tool_schema_bytes_bucket":        shape.ToolSchemaBytesBucket,
		"total_request_bytes_bucket":      shape.TotalRequestBytesBucket,
		"estimated_input_tokens_bucket":   shape.EstimatedInputTokensBucket,
		"requested_output_cap_field":      shape.RequestedOutputCapField,
		"requested_output_cap_bucket":     shape.RequestedOutputCapBucket,
		"tool_schema_fingerprint_present": shape.ToolSchemaFingerprint != "",
	})
	return shape
}

func (s *Service) recordTranslationShapeTelemetry(rc *requestContext, req *IRRequest, target Target, provider ProviderConfig, outDialect, endpoint string, attemptIndex int, body []byte) {
	if rc == nil || req == nil {
		return
	}
	var translated map[string]any
	_ = json.Unmarshal(body, &translated)
	endpointPath := ""
	if parsed, err := url.Parse(endpoint); err == nil {
		endpointPath = parsed.Path
	}
	shape := translationShapeFromBody(req, target, provider, outDialect, endpointPath, attemptIndex, translated, len(body))
	if isResponsesToChatBridge(rc.dialect, outDialect, target) {
		shape.BridgeDirection = responsesToChatBridgeDirection
	}
	if isChatToResponsesBridge(rc.dialect, outDialect, target) {
		shape.BridgeDirection = chatToResponsesBridgeDirection
	}
	if rc.rec.RequestShape != nil {
		shape.RequestShapeFingerprint = rc.rec.RequestShape.RequestShapeFingerprint
		shape.ToolSchemaFingerprint = rc.rec.RequestShape.ToolSchemaFingerprint
	}
	events := translationFieldEvents(req.Raw, translated, attemptIndex)
	shape.FieldsStrippedCount = countFieldEvents(events, "stripped")
	shape.FieldsRewrittenCount = countFieldEvents(events, "rewritten")
	shape.UnsupportedFieldsPresent = countFieldEvents(events, "unsupported") > 0
	shape.TranslationWarningCount = shape.FieldsStrippedCount + shape.FieldsRewrittenCount + countFieldEvents(events, "unsupported")
	rc.rec.TranslationShapes = append(rc.rec.TranslationShapes, shape)
	rc.rec.TranslationFieldEvents = append(rc.rec.TranslationFieldEvents, events...)
}

func translationShapeFromBody(req *IRRequest, target Target, provider ProviderConfig, outDialect, endpointPath string, attemptIndex int, translated map[string]any, bodyBytes int) translationShapeLogRecord {
	outputField, outputCap := outputCapFromMap(translated)
	return translationShapeLogRecord{
		TS:                           time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		AttemptIndex:                 attemptIndex,
		Provider:                     target.Provider,
		Model:                        targetModelLabel(target, provider),
		Dialect:                      outDialect,
		EndpointPath:                 endpointPath,
		TranslatedStream:             boolValue(translated["stream"]),
		TranslatedToolCount:          translatedToolCount(translated),
		TranslatedToolChoiceMode:     toolChoiceMode(translated["tool_choice"]),
		TranslatedOutputCapField:     outputField,
		TranslatedOutputCapBucket:    maxTokenBucket(outputCap),
		TranslatedReasoningControl:   translatedReasoningControl(translated),
		TranslatedRequestBytesBucket: byteBucket(bodyBytes),
	}
}

func (s *Service) shapeHMAC(scope string, payload any) string {
	if isEmptyShapePayload(payload) {
		return ""
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	keyMaterial := "request-shape-telemetry|" + scope
	if s != nil && s.cfg != nil {
		keyMaterial += "|" + routingConfigFingerprint(s.cfg) + "|" + s.cfg.StatePath
	}
	sum := sha256.Sum256([]byte(keyMaterial))
	mac := hmac.New(sha256.New, sum[:])
	_, _ = mac.Write(raw)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func isEmptyShapePayload(payload any) bool {
	switch v := payload.(type) {
	case nil:
		return true
	case []map[string]any:
		return len(v) == 0
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

func countInputItemsAndMessages(dialect string, raw map[string]any, req *IRRequest) (int, int) {
	if raw == nil {
		return len(req.Messages), len(req.Messages)
	}
	switch dialect {
	case "openai-responses":
		if items, ok := raw["input"].([]any); ok {
			return len(items), len(req.Messages)
		}
		if rawValuePresent(raw, "input") {
			return 1, len(req.Messages)
		}
	default:
		if messages, ok := raw["messages"].([]any); ok {
			return len(messages), len(messages)
		}
	}
	return len(req.Messages), len(req.Messages)
}

func countRoles(raw map[string]any, req *IRRequest) map[string]int {
	counts := map[string]int{"system": 0, "developer": 0, "user": 0, "assistant": 0}
	if messages, ok := raw["messages"].([]any); ok {
		for _, item := range messages {
			if m, ok := item.(map[string]any); ok {
				role := strings.ToLower(strings.TrimSpace(stringValue(m["role"])))
				if _, ok := counts[role]; ok {
					counts[role]++
				}
			}
		}
		return counts
	}
	if req.System != "" {
		counts["system"]++
	}
	for _, msg := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if _, ok := counts[role]; ok {
			counts[role]++
		}
	}
	return counts
}

func countToolOutputs(raw map[string]any, req *IRRequest) (int, int) {
	toolResults := 0
	functionOutputs := 0
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			typ := strings.ToLower(strings.TrimSpace(stringValue(x["type"])))
			role := strings.ToLower(strings.TrimSpace(stringValue(x["role"])))
			if role == "tool" || typ == "tool_result" {
				toolResults++
			}
			if typ == "function_call_output" {
				functionOutputs++
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	if raw != nil {
		walk(raw["messages"])
		walk(raw["input"])
		return toolResults, functionOutputs
	}
	for _, msg := range req.Messages {
		if strings.EqualFold(msg.Role, "tool") {
			toolResults++
		}
	}
	return toolResults, functionOutputs
}

func requestInputTextBytes(req *IRRequest) int {
	if req == nil {
		return 0
	}
	total := len(req.System) + len(req.Input)
	for _, part := range req.InputParts {
		total += len(part.Text)
	}
	for _, msg := range req.Messages {
		total += len(msg.Content)
		for _, part := range msg.Parts {
			total += len(part.Text)
		}
	}
	return total
}

func byteBucket(n int) string {
	switch {
	case n <= 0:
		return "none"
	case n <= 1024:
		return "1b-1kb"
	case n <= 16*1024:
		return "1kb-16kb"
	case n <= 64*1024:
		return "16kb-64kb"
	case n <= 256*1024:
		return "64kb-256kb"
	case n <= 1024*1024:
		return "256kb-1mb"
	default:
		return "gt-1mb"
	}
}

func countBucket(n int) string {
	switch {
	case n <= 0:
		return "none"
	case n <= 64:
		return "tiny"
	case n <= 1024:
		return "small"
	case n <= 8192:
		return "medium"
	case n <= 32768:
		return "large"
	default:
		return "xlarge"
	}
}

func reasoningEffortBucket(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "", "none":
		return "none"
	case "minimal", "low", "medium", "high", "xhigh":
		return effort
	default:
		return "other"
	}
}

func rawValuePresent(raw map[string]any, key string) bool {
	if raw == nil {
		return false
	}
	value, ok := raw[key]
	return ok && value != nil
}

// rawKeyPresent reports JSON-object key presence, including an explicit null.
// Most request-shape checks intentionally care about usable non-null values and
// should continue to use rawValuePresent. tool_choice is different: capability
// evidence distinguishes an omitted key from every explicit caller choice, so a
// present null must not inherit tools-omitted eligibility.
func rawKeyPresent(raw map[string]any, key string) bool {
	if raw == nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

func nestedValuePresent(raw map[string]any, parent, child string) bool {
	return nestedValue(raw, parent, child) != nil
}

func nestedValue(raw map[string]any, parent, child string) any {
	if raw == nil {
		return nil
	}
	if m, ok := raw[parent].(map[string]any); ok {
		return m[child]
	}
	return nil
}

func rawContainsModality(v any, modality string) bool {
	modality = strings.ToLower(modality)
	var found bool
	var walk func(any)
	walk = func(value any) {
		if found {
			return
		}
		switch x := value.(type) {
		case map[string]any:
			typ := strings.ToLower(strings.TrimSpace(stringValue(x["type"])))
			if typ == modality || strings.Contains(typ, modality+"_") || strings.Contains(typ, "_"+modality) {
				found = true
				return
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	return found
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func toolChoiceMode(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		mode := strings.ToLower(strings.TrimSpace(x))
		switch mode {
		case "auto", "none", "required":
			return mode
		case "":
			return ""
		default:
			return "other"
		}
	case map[string]any:
		typ := strings.ToLower(strings.TrimSpace(stringValue(x["type"])))
		switch typ {
		case "auto", "none", "required":
			return typ
		case "function", "tool":
			return "forced"
		case "":
			if len(x) > 0 {
				return "object"
			}
			return ""
		default:
			return "object"
		}
	default:
		return "other"
	}
}

func outputCapFromMap(body map[string]any) (string, int) {
	for _, field := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if n, ok := numberAsInt(body[field]); ok {
			return field, n
		}
	}
	return "", 0
}

func translatedToolCount(body map[string]any) int {
	if tools, ok := body["tools"].([]any); ok {
		return len(tools)
	}
	return 0
}

func translatedReasoningControl(body map[string]any) string {
	if rawValuePresent(body, "reasoning") {
		return "reasoning"
	}
	if rawValuePresent(body, "reasoning_effort") {
		return "reasoning_effort"
	}
	if rawValuePresent(body, "thinking") {
		return "thinking"
	}
	return ""
}

func targetModelLabel(target Target, provider ProviderConfig) string {
	if strings.TrimSpace(target.Model) != "" {
		return target.Model
	}
	if strings.TrimSpace(target.ModelRef) != "" {
		if model, ok := provider.Models[target.ModelRef]; ok && strings.TrimSpace(model.Model) != "" {
			return model.Model
		}
		return target.ModelRef
	}
	return ""
}

func translationFieldEvents(raw, translated map[string]any, attemptIndex int) []translationFieldEventLogRecord {
	if raw == nil {
		return nil
	}
	events := make([]translationFieldEventLogRecord, 0)
	for _, key := range sortedMapKeys(raw) {
		field := safeTranslationFieldName(key)
		allowed := safeTranslatedFieldNameAllowed(key)
		if !allowed {
			field = "other"
		}
		if field == "model" {
			continue
		}
		_, inTranslated := translated[key]
		if !inTranslated {
			action := "stripped"
			reason := "not-forwarded"
			if !allowed {
				action = "unsupported"
				reason = "unsupported-field"
			}
			events = appendTranslationFieldEvent(events, attemptIndex, field, action, reason)
			continue
		}
		if translatedFieldRewritten(key, raw[key], translated[key]) {
			events = appendTranslationFieldEvent(events, attemptIndex, field, "rewritten", "router-translation")
		}
	}
	return events
}

func appendTranslationFieldEvent(events []translationFieldEventLogRecord, attemptIndex int, field, action, reason string) []translationFieldEventLogRecord {
	if len(events) >= maxTranslationFieldEventsPerAttempt {
		return events
	}
	return append(events, translationFieldEventLogRecord{
		AttemptIndex: attemptIndex,
		Seq:          len(events) + 1,
		FieldName:    field,
		Action:       action,
		Reason:       reason,
	})
}

func countFieldEvents(events []translationFieldEventLogRecord, action string) int {
	count := 0
	for _, event := range events {
		if event.Action == action {
			count++
		}
	}
	return count
}

func translatedFieldRewritten(key string, rawValue, translatedValue any) bool {
	switch key {
	case "stream", "store", "max_tokens", "max_completion_tokens", "max_output_tokens", "tool_choice", "thinking", "reasoning", "reasoning_effort":
		return !sameJSONSafeScalarOrShapeValue(rawValue, translatedValue)
	default:
		return false
	}
}

func sameJSONSafeScalarOrShapeValue(a, b any) bool {
	ar, aerr := json.Marshal(safeComparableShapeValue(a))
	br, berr := json.Marshal(safeComparableShapeValue(b))
	return aerr == nil && berr == nil && string(ar) == string(br)
}

func safeComparableShapeValue(v any) any {
	switch x := v.(type) {
	case bool, nil:
		return x
	case string:
		mode := toolChoiceMode(x)
		if mode != "" {
			return mode
		}
		return "string"
	case float64, float32, int, int64, int32, uint, uint64, uint32:
		if n, ok := numberAsInt(x); ok {
			return countBucket(n)
		}
		return "number"
	default:
		return sanitizedShapePayload(v)
	}
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func safeTranslationFieldName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if !safeTranslatedFieldNameAllowed(name) {
		return ""
	}
	return name
}

func safeTranslatedFieldNameAllowed(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "model", "messages", "input", "instructions", "system", "tools", "tool_choice", "parallel_tool_calls",
		"response_format", "text", "reasoning", "reasoning_effort", "thinking", "include", "truncation",
		"metadata", "store", "previous_response_id", "max_tokens", "max_completion_tokens", "max_output_tokens",
		"temperature", "top_p", "stop", "stream", "stream_options", "user", "n", "seed", "other":
		return true
	default:
		return false
	}
}

func sanitizedShapePayload(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for _, key := range sortedMapKeys(x) {
			out[key] = sanitizedShapePayload(x[key])
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, sanitizedShapePayload(item))
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, sanitizedShapePayload(item))
		}
		return out
	case string:
		return "string"
	case float64, float32, int, int64, int32, uint, uint64, uint32:
		return "number"
	case bool:
		return "bool"
	case nil:
		return nil
	default:
		return "value"
	}
}
