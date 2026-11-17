// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/metrum-ai/router/internal/stream"
)

const (
	// Ordinary bounded credential patterns fit in this rolling tail. Longer JWT
	// and PEM candidates are held from their start by format-specific scanners.
	secretStreamTailBytes    = 512
	secretStreamPEMMax       = 64 << 10
	maxResponsesTextChannels = 128
)

var errSecretStreamWindowExceeded = errors.New("secret DLP stream inspection window exceeded")

var secretStreamOversizeCandidate = regexp.MustCompile(`(?i)(?:\b(?:gh[pousr]_|github_pat_|xox[baprs]-|sk_(?:live|test)_|sk-proj-|sk-svcacct-|sk-ant-api03-|sk-or-v1-|sk-|gsk_|AIza|rtr_metrum_)[A-Za-z0-9_-]{257}|\b(?:api[_-]?key|access[_-]?token|client[_-]?secret|password)\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{257}|\bAuthorization\s*:\s*Bearer\s+[A-Za-z0-9._~+/-]{257})`)

var secretStreamMatcherOnce sync.Once
var secretStreamMatcher *regexp.Regexp
var secretStreamKeywordStart = regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|client[_-]?secret|password|Authorization)\b`)

func compiledSecretStreamMatcher() *regexp.Regexp {
	secretStreamMatcherOnce.Do(func() {
		var alternatives []string
		for _, rule := range builtinSecretRules() {
			if rule.Name == "private_key" {
				continue // PEM candidates have a dedicated bounded scanner.
			}
			alternatives = append(alternatives, "(?:"+rule.Expression+")")
		}
		secretStreamMatcher = regexp.MustCompile(strings.Join(alternatives, "|"))
		secretStreamMatcher.Longest()
	})
	return secretStreamMatcher
}

// secretStreamDLP is request-local state shared by stream text and held tool
// call inspection. A holder invokes redactCandidate only after the complete
// candidate is available and before releasing it downstream.
type secretStreamDLP struct {
	state                *piiRedactionState
	mode                 string
	failOnMatch          bool
	pending              string
	lastEvent            stream.Event
	lastKind             string
	sequenceAdd          int
	responsesSequence    int
	hasResponsesSequence bool
	chatChoices          map[int]*secretChatChoiceState
	anthropicThinking    map[int]*secretAnthropicThinkingState
	responsesText        map[string]*secretResponsesTextState
}

type secretResponsesTextState struct {
	pending          string
	forwardedBytes   int
	lastEvent        stream.Event
	lastSnapshotHash [32]byte
	hasSnapshot      bool
}

type secretChatChoiceState struct {
	pending   map[string]string
	lastEvent stream.Event
	choice    map[string]any
}

type secretAnthropicThinkingState struct {
	pending   string
	lastEvent stream.Event
}

func (d *secretStreamDLP) redactCandidate(value string) (string, error) {
	if d == nil || d.state == nil || d.state.result == nil {
		return value, nil
	}
	if hasSecretStreamOversizeCandidate(value) {
		return "", errSecretStreamWindowExceeded
	}
	before := d.state.result.Replacements
	redacted := d.state.redact(value)
	if err := d.failIfMatched(before); err != nil {
		return "", err
	}
	return redacted, nil
}

func (d *secretStreamDLP) failIfMatched(before int) error {
	if d.state == nil || d.state.result == nil {
		return nil
	}
	if d.state.result.LimitExceeded || (d.state.result.Replacements > before && (d.mode == "fail_on_match" || d.failOnMatch)) {
		return piiFilterBlockedError{}
	}
	return nil
}

// filterEvent redacts streamed text deltas with a rolling tail. The tail is
// emitted immediately before a terminal or full-text snapshot event so clients
// receive the complete redacted text without seeing a split credential.
func (d *secretStreamDLP) filterEvent(event stream.Event) ([]stream.Event, error) {
	if d == nil || d.state == nil || d.state.result == nil {
		return []stream.Event{event}, nil
	}
	var payload map[string]any
	if json.Unmarshal(event.Data, &payload) != nil {
		if strings.TrimSpace(string(event.Data)) == "[DONE]" {
			if out, ok, err := d.flushAnthropicThinkingAll(event); ok || err != nil {
				return out, err
			}
			if out, ok, err := d.flushChatBefore(event); ok || err != nil {
				return out, err
			}
			return d.flushBefore(event, nil)
		}
		return []stream.Event{event}, nil
	}
	eventType := event.Type
	if eventType == "" {
		eventType = stringValue(payload["type"])
	}
	if eventType == "content_block_delta" {
		delta, _ := payload["delta"].(map[string]any)
		if delta != nil && stringValue(delta["type"]) == "thinking_delta" {
			return d.filterAnthropicThinkingDelta(event, payload)
		}
	}
	if eventType == "content_block_start" {
		block, _ := payload["content_block"].(map[string]any)
		if block != nil && stringValue(block["type"]) == "thinking" {
			return d.filterAnthropicThinkingStart(event, payload)
		}
	}
	if eventType == "" || eventType == "chat.completion.chunk" {
		if _, ok := payload["choices"].([]any); ok {
			return d.filterChatEvent(event, payload)
		}
	}
	if strings.HasPrefix(eventType, "response.") {
		if out, handled, err := d.filterResponsesTextEvent(event, payload, eventType); handled || err != nil {
			return out, err
		}
	}
	thinkingPrefix, flushThinking, thinkingErr := d.flushAnthropicThinkingForTerminal(event, payload, eventType)
	if thinkingErr != nil {
		return nil, thinkingErr
	}
	kind, delta, ok := secretStreamTextDelta(payload, eventType)
	if ok {
		before := d.state.result.Replacements
		safe, pending, err := redactSecretStreamChunk(d.state, d.pending, delta, false)
		if err != nil {
			return nil, err
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, err
		}
		d.pending = pending
		setSecretStreamTextDelta(payload, kind, safe)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		event.Data = encoded
		d.lastEvent = event
		d.lastKind = kind
		if safe == "" && kind != "anthropic_start" {
			event.Data = nil
		}
	}
	snapshot := eventType == "response.output_text.done" || eventType == "response.content_part.done" || eventType == "response.output_item.done" || eventType == "response.completed"
	terminal := event.Name == "message_stop" || event.Name == "content_block_stop" && strings.HasPrefix(d.lastKind, "anthropic") || flushThinking || eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" || secretStreamChatTerminal(payload)
	chatContentAndFinish := ok && secretStreamChatTerminal(payload)
	if snapshot {
		before := d.state.result.Replacements
		snapshotChanged, snapshotErr := redactSecretStreamSnapshot(payload, d.state)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, err
		}
		if !snapshotChanged && d.pending == "" && d.sequenceAdd == 0 && !ok {
			return []stream.Event{event}, nil
		}
	}
	if terminal || snapshot {
		if terminal && !snapshot && d.pending == "" {
			return append(thinkingPrefix, event), nil
		}
		if chatContentAndFinish {
			// The held suffix belongs before finish_reason. Keep the saved content
			// event as a content delta, then emit this frame as terminal-only.
			if d.lastEvent.Data != nil {
				var previous map[string]any
				if json.Unmarshal(d.lastEvent.Data, &previous) == nil {
					clearSecretStreamChatFinish(previous)
					encoded, marshalErr := json.Marshal(previous)
					if marshalErr != nil {
						return nil, marshalErr
					}
					d.lastEvent.Data = encoded
				}
			}
			clearSecretStreamChatContent(payload)
		}
		var out []stream.Event
		var err error
		out, err = d.flushBefore(event, payload)
		if err != nil {
			return nil, err
		}
		return append(thinkingPrefix, out...), nil
	}
	if event.Data == nil {
		return nil, nil
	}
	if !ok && d.sequenceAdd == 0 {
		return []stream.Event{event}, nil
	}
	addSecretStreamSequence(payload, d.sequenceAdd)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	event.Data = encoded
	return []stream.Event{event}, nil
}

func (d *secretStreamDLP) filterAnthropicThinkingDelta(event stream.Event, payload map[string]any) ([]stream.Event, error) {
	delta, _ := payload["delta"].(map[string]any)
	value, _ := delta["thinking"].(string)
	index, _ := numberAsInt(payload["index"])
	if d.anthropicThinking == nil {
		d.anthropicThinking = make(map[int]*secretAnthropicThinkingState)
	}
	state := d.anthropicThinking[index]
	if state == nil {
		state = &secretAnthropicThinkingState{}
		d.anthropicThinking[index] = state
	}
	before := d.state.result.Replacements
	safe, pending, err := redactSecretStreamChunk(d.state, state.pending, value, false)
	if err != nil {
		return nil, err
	}
	if err := d.failIfMatched(before); err != nil {
		return nil, err
	}
	state.pending, state.lastEvent = pending, event
	if safe == "" {
		return nil, nil
	}
	delta["thinking"] = safe
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	event.Data = encoded
	return []stream.Event{event}, nil
}

func setAnthropicThinkingText(payload map[string]any, value string) bool {
	if delta, ok := payload["delta"].(map[string]any); ok {
		delta["thinking"] = value
		return true
	}
	if block, ok := payload["content_block"].(map[string]any); ok && stringValue(block["type"]) == "thinking" {
		block["thinking"] = value
		return true
	}
	return false
}

func flushAnthropicThinkingEvent(event stream.Event, payload map[string]any, value string) (stream.Event, error) {
	if stringValue(payload["type"]) == "content_block_start" {
		delete(payload, "content_block")
		payload["type"] = "content_block_delta"
		payload["delta"] = map[string]any{"type": "thinking_delta", "thinking": value}
		event.Type, event.Name = "content_block_delta", "content_block_delta"
	} else if !setAnthropicThinkingText(payload, value) {
		return event, fmt.Errorf("unable to flush Anthropic thinking text")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return event, err
	}
	event.Data = encoded
	return event, nil
}

func (d *secretStreamDLP) filterAnthropicThinkingStart(event stream.Event, payload map[string]any) ([]stream.Event, error) {
	block, _ := payload["content_block"].(map[string]any)
	value, ok := block["thinking"].(string)
	if !ok {
		return []stream.Event{event}, nil
	}
	index, _ := numberAsInt(payload["index"])
	if d.anthropicThinking == nil {
		d.anthropicThinking = make(map[int]*secretAnthropicThinkingState)
	}
	state := d.anthropicThinking[index]
	if state == nil {
		state = &secretAnthropicThinkingState{}
		d.anthropicThinking[index] = state
	}
	before := d.state.result.Replacements
	safe, pending, err := redactSecretStreamChunk(d.state, state.pending, value, false)
	if err != nil {
		return nil, err
	}
	if err := d.failIfMatched(before); err != nil {
		return nil, err
	}
	block["thinking"] = safe
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	event.Data = encoded
	state.pending, state.lastEvent = pending, event
	return []stream.Event{event}, nil
}

func (d *secretStreamDLP) flushAnthropicThinkingForTerminal(event stream.Event, payload map[string]any, eventType string) ([]stream.Event, bool, error) {
	if len(d.anthropicThinking) == 0 {
		return nil, false, nil
	}
	all := event.Name == "message_stop" || eventType == "message_stop"
	if !all && event.Name != "content_block_stop" && eventType != "content_block_stop" {
		return nil, false, nil
	}
	var indices []int
	if all {
		for index := range d.anthropicThinking {
			indices = append(indices, index)
		}
	} else if index, ok := numberAsInt(payload["index"]); ok {
		if _, exists := d.anthropicThinking[index]; exists {
			indices = append(indices, index)
		}
	}
	sort.Ints(indices)
	out := make([]stream.Event, 0, len(indices))
	for _, index := range indices {
		state := d.anthropicThinking[index]
		before := d.state.result.Replacements
		tail, _, err := redactSecretStreamChunk(d.state, state.pending, "", true)
		if err != nil {
			return nil, true, err
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, true, err
		}
		if tail != "" {
			var p map[string]any
			if json.Unmarshal(state.lastEvent.Data, &p) == nil {
				if e, marshalErr := flushAnthropicThinkingEvent(state.lastEvent, p, tail); marshalErr == nil {
					out = append(out, e)
				}
			}
		}
		delete(d.anthropicThinking, index)
	}
	return out, true, nil
}

func (d *secretStreamDLP) flushAnthropicThinkingAll(event stream.Event) ([]stream.Event, bool, error) {
	if len(d.anthropicThinking) == 0 {
		return nil, false, nil
	}
	indices := make([]int, 0, len(d.anthropicThinking))
	for index := range d.anthropicThinking {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	var out []stream.Event
	for _, index := range indices {
		state := d.anthropicThinking[index]
		before := d.state.result.Replacements
		tail, _, err := redactSecretStreamChunk(d.state, state.pending, "", true)
		if err != nil {
			return nil, true, err
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, true, err
		}
		if tail != "" {
			var p map[string]any
			if json.Unmarshal(state.lastEvent.Data, &p) == nil {
				if e, marshalErr := flushAnthropicThinkingEvent(state.lastEvent, p, tail); marshalErr == nil {
					out = append(out, e)
				}
			}
		}
		delete(d.anthropicThinking, index)
	}
	return append(out, event), true, nil
}

// filterChatEvent keeps independent rolling windows for every choice and each
// text-bearing channel. Chat chunks with multiple choices are split into one
// output chunk per choice so one choice cannot release another choice's tail.
func (d *secretStreamDLP) filterChatEvent(event stream.Event, payload map[string]any) ([]stream.Event, error) {
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return []stream.Event{event}, nil
	}
	if d.chatChoices == nil {
		d.chatChoices = make(map[int]*secretChatChoiceState)
	}
	var out []stream.Event
	for position, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		choiceIndex := position
		if index, ok := numberAsInt(choice["index"]); ok {
			choiceIndex = index
		}
		choiceState := d.chatChoices[choiceIndex]
		if choiceState == nil {
			choiceState = &secretChatChoiceState{pending: make(map[string]string)}
			d.chatChoices[choiceIndex] = choiceState
		}
		choiceState.lastEvent = event
		choiceState.choice = cloneSecretStreamMap(choice)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			delta = make(map[string]any)
			choice["delta"] = delta
		}
		for _, field := range []string{"content", "reasoning_content", "thinking"} {
			value, exists := delta[field].(string)
			if !exists {
				continue
			}
			before := d.state.result.Replacements
			safe, pending, err := redactSecretStreamChunk(d.state, choiceState.pending[field], value, false)
			if err != nil {
				return nil, err
			}
			if err := d.failIfMatched(before); err != nil {
				return nil, err
			}
			choiceState.pending[field] = pending
			if safe == "" {
				delete(delta, field)
			} else {
				delta[field] = safe
			}
		}
		finishReason := choice["finish_reason"]
		terminal := finishReason != nil && strings.TrimSpace(stringValue(finishReason)) != ""
		if terminal {
			for _, field := range []string{"content", "reasoning_content", "thinking"} {
				pending := choiceState.pending[field]
				if pending == "" {
					continue
				}
				before := d.state.result.Replacements
				tail, _, err := redactSecretStreamChunk(d.state, pending, "", true)
				if err != nil {
					return nil, err
				}
				if err := d.failIfMatched(before); err != nil {
					return nil, err
				}
				current, _ := delta[field].(string)
				delta[field] = current + tail
				delete(choiceState.pending, field)
			}
			contentChoice := cloneSecretStreamMap(choice)
			contentChoice["finish_reason"] = nil
			contentDelta, _ := contentChoice["delta"].(map[string]any)
			if len(contentDelta) > 0 {
				if encoded, err := secretChatChoiceEvent(event, payload, contentChoice); err != nil {
					return nil, err
				} else {
					out = append(out, encoded)
				}
			}
			terminalChoice := cloneSecretStreamMap(choice)
			terminalChoice["delta"] = map[string]any{}
			if encoded, err := secretChatChoiceEvent(event, payload, terminalChoice); err != nil {
				return nil, err
			} else {
				out = append(out, encoded)
			}
			delete(d.chatChoices, choiceIndex)
			continue
		}
		if len(delta) > 0 {
			if encoded, err := secretChatChoiceEvent(event, payload, choice); err != nil {
				return nil, err
			} else {
				out = append(out, encoded)
			}
		}
		choiceState.choice = cloneSecretStreamMap(choice)
	}
	return out, nil
}

func (d *secretStreamDLP) flushChatBefore(event stream.Event) ([]stream.Event, bool, error) {
	if len(d.chatChoices) == 0 {
		return nil, false, nil
	}
	indices := make([]int, 0, len(d.chatChoices))
	for index := range d.chatChoices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	var out []stream.Event
	for _, index := range indices {
		choiceState := d.chatChoices[index]
		choice := cloneSecretStreamMap(choiceState.choice)
		delta := make(map[string]any)
		for _, field := range []string{"content", "reasoning_content", "thinking"} {
			pending := choiceState.pending[field]
			if pending == "" {
				continue
			}
			before := d.state.result.Replacements
			tail, _, err := redactSecretStreamChunk(d.state, pending, "", true)
			if err != nil {
				return nil, true, err
			}
			if err := d.failIfMatched(before); err != nil {
				return nil, true, err
			}
			delta[field] = tail
		}
		if len(delta) > 0 {
			choice["delta"] = delta
			choice["finish_reason"] = nil
			encoded, err := secretChatChoiceEvent(choiceState.lastEvent, nil, choice)
			if err != nil {
				return nil, true, err
			}
			out = append(out, encoded)
		}
	}
	d.chatChoices = nil
	if len(out) > 0 {
		out = append(out, event)
		return out, true, nil
	}
	return nil, false, nil
}

func secretChatChoiceEvent(event stream.Event, source map[string]any, choice map[string]any) (stream.Event, error) {
	var payload map[string]any
	if source != nil {
		payload = cloneSecretStreamMap(source)
	} else if json.Unmarshal(event.Data, &payload) != nil {
		return stream.Event{}, errors.New("invalid held Chat stream event")
	}
	payload["choices"] = []any{choice}
	data, err := json.Marshal(payload)
	if err != nil {
		return stream.Event{}, err
	}
	event.Data = data
	return event, nil
}

func cloneSecretStreamMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(source)
	if err != nil {
		return map[string]any{}
	}
	var copy map[string]any
	if json.Unmarshal(data, &copy) != nil || copy == nil {
		return map[string]any{}
	}
	return copy
}

func clearSecretStreamChatFinish(payload map[string]any) {
	choices, _ := payload["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if choice != nil {
			choice["finish_reason"] = nil
		}
	}
}

func clearSecretStreamChatContent(payload map[string]any) {
	choices, _ := payload["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if choice == nil {
			continue
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			delete(delta, "content")
		}
	}
}

func responseTextChannelKey(payload map[string]any) (string, bool) {
	contentIndex, hasContentIndex := numberAsInt(payload["content_index"])
	summaryIndex, hasSummaryIndex := numberAsInt(payload["summary_index"])
	outputIndex, hasOutputIndex := numberAsInt(payload["output_index"])
	itemID := stringValue(payload["item_id"])
	if hasContentIndex {
		if hasOutputIndex {
			return fmt.Sprintf("output:%d/content:%d", outputIndex, contentIndex), true
		}
		return fmt.Sprintf("item:%s/content:%d", itemID, contentIndex), true
	}
	if hasSummaryIndex {
		if hasOutputIndex {
			return fmt.Sprintf("output:%d/summary:%d", outputIndex, summaryIndex), true
		}
		if itemID != "" {
			return fmt.Sprintf("item:%s/summary:%d", itemID, summaryIndex), true
		}
		return fmt.Sprintf("summary:%d", summaryIndex), true
	}
	if hasOutputIndex {
		return fmt.Sprintf("output:%d", outputIndex), true
	}
	if itemID != "" {
		return "item:" + itemID, true
	}
	return "response:default", true
}

func responseTextPart(part map[string]any) string {
	if text, ok := part["text"].(string); ok {
		return text
	}
	text, _ := part["refusal"].(string)
	return text
}

func responseTextPartChannel(payload map[string]any, part map[string]any, reasoning bool) (string, bool) {
	channel, ok := responseTextChannelKey(payload)
	if !ok {
		return "", false
	}
	payloadType := stringValue(payload["type"])
	if stringValue(part["type"]) == "refusal" || payloadType == "response.refusal.delta" || payloadType == "response.refusal.done" {
		channel = "refusal/" + channel
	} else if reasoning {
		channel = "reasoning/" + channel
	}
	return channel, true
}

type responseTextSnapshotPart struct {
	channel        string
	channelPayload map[string]any
	part           map[string]any
	textKey        string
	text           string
}

func responseTextSnapshotParts(payload map[string]any, eventType string) []responseTextSnapshotPart {
	var parts []responseTextSnapshotPart
	appendPart := func(channelPayload, part map[string]any, reasoning bool) {
		if part == nil {
			return
		}
		channel, ok := responseTextPartChannel(channelPayload, part, reasoning)
		if !ok {
			return
		}
		textKey := "text"
		if _, ok := part[textKey].(string); !ok {
			textKey = "refusal"
		}
		parts = append(parts, responseTextSnapshotPart{channel: channel, channelPayload: channelPayload, part: part, textKey: textKey, text: responseTextPart(part)})
	}
	appendItem := func(item map[string]any, outputIndex any) {
		content, _ := item["content"].([]any)
		itemID := stringValue(item["id"])
		for index, raw := range content {
			part, _ := raw.(map[string]any)
			appendPart(map[string]any{
				"output_index":  outputIndex,
				"item_id":       itemID,
				"content_index": index,
			}, part, false)
		}
		if summary, ok := item["summary"].([]any); ok {
			for index, raw := range summary {
				part, _ := raw.(map[string]any)
				appendPart(map[string]any{
					"output_index":  outputIndex,
					"item_id":       itemID,
					"summary_index": index,
				}, part, true)
			}
		}
	}
	if eventType == "response.output_item.added" || eventType == "response.output_item.done" {
		item, _ := payload["item"].(map[string]any)
		appendItem(item, payload["output_index"])
		return parts
	}
	switch eventType {
	case "response.content_part.added", "response.reasoning_summary_part.added", "response.content_part.done", "response.reasoning_summary_part.done":
		part, _ := payload["part"].(map[string]any)
		appendPart(payload, part, eventType == "response.reasoning_summary_part.added" || eventType == "response.reasoning_summary_part.done")
	case "response.output_text.done", "response.reasoning_summary_text.done", "response.refusal.done":
		appendPart(payload, payload, eventType == "response.reasoning_summary_text.done")
	case "response.completed", "response.failed", "response.incomplete":
		response, _ := payload["response"].(map[string]any)
		output, _ := response["output"].([]any)
		for index, raw := range output {
			item, _ := raw.(map[string]any)
			appendItem(item, index)
		}
	}
	return parts
}

func (d *secretStreamDLP) prepareResponsesTextSnapshot(payload map[string]any, eventType string) (map[string]bool, error) {
	duplicates := make(map[string]bool)
	for _, part := range responseTextSnapshotParts(payload, eventType) {
		if part.text == "" {
			continue
		}
		state := d.responsesText[part.channel]
		if state == nil {
			if len(d.responsesText) >= maxResponsesTextChannels {
				return nil, errSecretStreamWindowExceeded
			}
			state = &secretResponsesTextState{}
			d.responsesText[part.channel] = state
		}
		hash := sha256.Sum256([]byte(part.text))
		if state.hasSnapshot && state.lastSnapshotHash == hash {
			part.part[part.textKey] = ""
			duplicates[part.channel] = true
			continue
		}
		state.lastSnapshotHash = hash
		state.hasSnapshot = true
	}
	return duplicates, nil
}

func (d *secretStreamDLP) seedResponsesTextSnapshot(payload map[string]any, eventType string, duplicates map[string]bool) error {
	for _, part := range responseTextSnapshotParts(payload, eventType) {
		if duplicates[part.channel] || part.text == "" {
			continue
		}
		state := d.responsesText[part.channel]
		if state == nil {
			return errSecretStreamWindowExceeded
		}
		before := d.state.result.Replacements
		safe, pending, err := redactSecretStreamChunk(d.state, state.pending, part.text, false)
		if err != nil {
			return err
		}
		if err := d.failIfMatched(before); err != nil {
			return err
		}
		state.pending = pending
		state.forwardedBytes += len(safe)
		part.part[part.textKey] = safe
		deltaType := "response.output_text.delta"
		if strings.HasPrefix(part.channel, "refusal/") {
			deltaType = "response.refusal.delta"
		} else if strings.HasPrefix(part.channel, "reasoning/") {
			deltaType = "response.reasoning_summary_text.delta"
		}
		deltaPayload := make(map[string]any, 6)
		for _, key := range []string{"output_index", "item_id", "content_index", "summary_index"} {
			if value, ok := part.channelPayload[key]; ok {
				deltaPayload[key] = value
			}
		}
		deltaPayload["type"] = deltaType
		deltaPayload["delta"] = safe
		encoded, err := json.Marshal(deltaPayload)
		if err != nil {
			return err
		}
		state.lastEvent = stream.Event{Name: deltaType, Type: deltaType, Data: encoded}
	}
	return nil
}

// redactResponsesAddedSnapshot redacts other snapshot fields while leaving
// added text raw for the rolling Responses text inspector.
func redactResponsesAddedSnapshot(payload map[string]any, state *piiRedactionState, eventType string) (bool, error) {
	type savedText struct {
		part  map[string]any
		key   string
		value any
	}
	var saved []savedText
	for _, part := range responseTextSnapshotParts(payload, eventType) {
		if value, ok := part.part[part.textKey]; ok {
			saved = append(saved, savedText{part: part.part, key: part.textKey, value: value})
			delete(part.part, part.textKey)
		}
	}
	changed, err := redactSecretStreamSnapshot(payload, state)
	for _, text := range saved {
		text.part[text.key] = text.value
	}
	return changed, err
}

func (d *secretStreamDLP) filterResponsesTextEvent(event stream.Event, payload map[string]any, eventType string) ([]stream.Event, bool, error) {
	if d.responsesText == nil {
		d.responsesText = make(map[string]*secretResponsesTextState)
	}
	if eventType == "response.output_text.delta" || eventType == "response.refusal.delta" || eventType == "response.reasoning_summary_text.delta" {
		channel, ok := responseTextChannelKey(payload)
		if !ok {
			return []stream.Event{event}, true, nil
		}
		if eventType == "response.refusal.delta" {
			channel = "refusal/" + channel
		} else if eventType == "response.reasoning_summary_text.delta" {
			channel = "reasoning/" + channel
		}
		state := d.responsesText[channel]
		if state == nil {
			if len(d.responsesText) >= maxResponsesTextChannels {
				return nil, true, errSecretStreamWindowExceeded
			}
			state = &secretResponsesTextState{}
			d.responsesText[channel] = state
		}
		delta, _ := payload["delta"].(string)
		before := d.state.result.Replacements
		safe, pending, err := redactSecretStreamChunk(d.state, state.pending, delta, false)
		if err != nil {
			return nil, true, err
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, true, err
		}
		state.forwardedBytes += len(safe)
		state.pending = pending
		payload["delta"] = safe
		addSecretStreamSequence(payload, d.sequenceAdd)
		d.recordResponsesSequence(payload)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, true, err
		}
		event.Data = encoded
		state.lastEvent = event
		if safe == "" {
			return nil, true, nil
		}
		return []stream.Event{event}, true, nil
	}

	var channels []string
	if eventType == "response.output_item.added" || eventType == "response.content_part.added" || eventType == "response.reasoning_summary_part.added" {
		// Added events can carry populated snapshots before any text deltas.
	} else if eventType == "response.output_text.done" || eventType == "response.content_part.done" || eventType == "response.reasoning_summary_text.done" || eventType == "response.reasoning_summary_part.done" || eventType == "response.refusal.done" {
		if channel, ok := responseTextChannelKey(payload); ok {
			if eventType == "response.reasoning_summary_text.done" || eventType == "response.reasoning_summary_part.done" {
				channel = "reasoning/" + channel
			} else if eventType == "response.refusal.done" {
				channel = "refusal/" + channel
			}
			channels = append(channels, channel)
		}
	} else if eventType == "response.output_item.done" {
		outputIndex, hasOutputIndex := numberAsInt(payload["output_index"])
		prefix := ""
		if hasOutputIndex {
			prefix = fmt.Sprintf("output:%d", outputIndex)
		} else {
			itemID := stringValue(payload["item_id"])
			if item, ok := payload["item"].(map[string]any); ok && itemID == "" {
				itemID = stringValue(item["id"])
			}
			if itemID != "" {
				prefix = "item:" + itemID
			}
		}
		for key := range d.responsesText {
			channelKey := strings.TrimPrefix(strings.TrimPrefix(key, "refusal/"), "reasoning/")
			if prefix != "" && (channelKey == prefix || strings.HasPrefix(channelKey, prefix+"/")) {
				channels = append(channels, key)
			}
		}
	} else if eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" {
		for key := range d.responsesText {
			channels = append(channels, key)
		}
	} else {
		return nil, false, nil
	}
	sort.Strings(channels)
	addedSnapshot := eventType == "response.output_item.added" || eventType == "response.content_part.added" || eventType == "response.reasoning_summary_part.added"
	inspectSnapshot := addedSnapshot || eventType == "response.output_text.done" || eventType == "response.content_part.done" || eventType == "response.reasoning_summary_text.done" || eventType == "response.reasoning_summary_part.done" || eventType == "response.refusal.done" || eventType == "response.output_item.done" || eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete"
	terminalSnapshotText := make(map[string]string)
	duplicates := map[string]bool(nil)
	if addedSnapshot {
		var err error
		duplicates, err = d.prepareResponsesTextSnapshot(payload, eventType)
		if err != nil {
			return nil, true, err
		}
	}
	if inspectSnapshot {
		before := d.state.result.Replacements
		var err error
		if addedSnapshot {
			_, err = redactResponsesAddedSnapshot(payload, d.state, eventType)
		} else {
			_, err = redactSecretStreamSnapshot(payload, d.state)
		}
		if err != nil {
			return nil, true, err
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, true, err
		}
		if addedSnapshot {
			if err := d.seedResponsesTextSnapshot(payload, eventType, duplicates); err != nil {
				return nil, true, err
			}
		} else {
			for _, part := range responseTextSnapshotParts(payload, eventType) {
				terminalSnapshotText[part.channel] = part.text
			}
		}
	}

	var out []stream.Event
	for _, channel := range channels {
		state := d.responsesText[channel]
		if state == nil {
			continue
		}
		if state.pending == "" || state.lastEvent.Data == nil {
			delete(d.responsesText, channel)
			continue
		}
		tail, hasSnapshot := terminalSnapshotText[channel]
		if hasSnapshot {
			if state.forwardedBytes > len(tail) {
				delete(d.responsesText, channel)
				continue
			}
			boundary := utf8SafeBoundary(tail, state.forwardedBytes)
			if boundary != state.forwardedBytes {
				return nil, true, errSecretStreamWindowExceeded
			}
			tail = tail[boundary:]
		} else {
			before := d.state.result.Replacements
			var err error
			tail, _, err = redactSecretStreamChunk(d.state, state.pending, "", true)
			if err != nil {
				return nil, true, err
			}
			if err := d.failIfMatched(before); err != nil {
				return nil, true, err
			}
		}
		var previous map[string]any
		if err := json.Unmarshal(state.lastEvent.Data, &previous); err != nil {
			return nil, true, err
		}
		previous["delta"] = tail
		if sequence, ok := numberAsInt(payload["sequence_number"]); ok {
			previous["sequence_number"] = sequence + d.sequenceAdd
			d.responsesSequence = sequence + d.sequenceAdd
			d.hasResponsesSequence = true
			d.sequenceAdd++
		} else if d.hasResponsesSequence {
			d.responsesSequence++
			previous["sequence_number"] = d.responsesSequence
			d.sequenceAdd++
		}
		encoded, err := json.Marshal(previous)
		if err != nil {
			return nil, true, err
		}
		flushed := state.lastEvent
		flushed.Data = encoded
		out = append(out, flushed)
		state.pending = ""
		state.lastEvent = stream.Event{}
		delete(d.responsesText, channel)
	}
	if len(out) == 0 {
		addSecretStreamSequence(payload, d.sequenceAdd)
		d.recordResponsesSequence(payload)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, true, err
		}
		event.Data = encoded
		out = append(out, event)
	} else if event.Data != nil {
		addSecretStreamSequence(payload, d.sequenceAdd)
		d.recordResponsesSequence(payload)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, true, err
		}
		event.Data = encoded
		out = append(out, event)
	}
	return out, true, nil
}

func (d *secretStreamDLP) recordResponsesSequence(payload map[string]any) {
	if sequence, ok := numberAsInt(payload["sequence_number"]); ok {
		d.responsesSequence = sequence
		d.hasResponsesSequence = true
	}
}

func (d *secretStreamDLP) filterEvents(events []stream.Event) ([]stream.Event, error) {
	if d == nil {
		return events, nil
	}
	var filtered []stream.Event
	for _, event := range events {
		out, err := d.filterEvent(event)
		if err != nil {
			return nil, err
		}
		filtered = append(filtered, out...)
	}
	return filtered, nil
}

func (d *secretStreamDLP) flushBefore(event stream.Event, payload map[string]any) ([]stream.Event, error) {
	var out []stream.Event
	if d.pending != "" && d.lastEvent.Data != nil {
		before := d.state.result.Replacements
		tail, _, err := redactSecretStreamChunk(d.state, d.pending, "", true)
		if err != nil {
			return nil, err
		}
		if err := d.failIfMatched(before); err != nil {
			return nil, err
		}
		var previous map[string]any
		if json.Unmarshal(d.lastEvent.Data, &previous) == nil {
			flushed := d.lastEvent
			flushSecretStreamTextEvent(&flushed, previous, d.lastKind, tail)
			if _, ok := previous["sequence_number"]; ok && payload != nil {
				if sequence, ok := numberAsInt(payload["sequence_number"]); ok {
					previous["sequence_number"] = sequence + d.sequenceAdd
				}
				d.sequenceAdd++
			}
			encoded, err := json.Marshal(previous)
			if err != nil {
				return nil, err
			}
			flushed.Data = encoded
			out = append(out, flushed)
		}
	}
	d.pending = ""
	if payload != nil {
		addSecretStreamSequence(payload, d.sequenceAdd)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		event.Data = encoded
	}
	if event.Data != nil {
		out = append(out, event)
	}
	return out, nil
}

func secretStreamTextDelta(payload map[string]any, eventType string) (kind, value string, ok bool) {
	switch eventType {
	case "response.output_text.delta", "response.reasoning_summary_text.delta":
		value, ok = payload["delta"].(string)
		return "responses", value, ok
	case "content_block_delta":
		delta, _ := payload["delta"].(map[string]any)
		if delta != nil {
			switch stringValue(delta["type"]) {
			case "text_delta":
				value, ok = delta["text"].(string)
				return "anthropic", value, ok
			case "thinking_delta":
				value, ok = delta["thinking"].(string)
				return "anthropic_thinking", value, ok
			}
		}
	case "content_block_start":
		block, _ := payload["content_block"].(map[string]any)
		if block != nil && stringValue(block["type"]) == "text" {
			value, ok = block["text"].(string)
			return "anthropic_start", value, ok
		}
	case "", "chat.completion.chunk":
		choices, _ := payload["choices"].([]any)
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if delta != nil {
				value, ok = delta["content"].(string)
				return "chat", value, ok
			}
		}
	}
	return "", "", false
}

func setSecretStreamTextDelta(payload map[string]any, kind, value string) {
	switch kind {
	case "responses":
		payload["delta"] = value
	case "anthropic", "anthropic_thinking":
		if delta, ok := payload["delta"].(map[string]any); ok {
			if kind == "anthropic_thinking" {
				delta["thinking"] = value
			} else {
				delta["text"] = value
			}
		}
	case "anthropic_start":
		if block, ok := payload["content_block"].(map[string]any); ok {
			block["text"] = value
		}
	case "chat":
		if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if delta, ok := choice["delta"].(map[string]any); ok {
					delta["content"] = value
				}
			}
		}
	}
}

func flushSecretStreamTextEvent(event *stream.Event, payload map[string]any, kind, value string) {
	if kind == "anthropic_start" {
		delete(payload, "content_block")
		payload["type"] = "content_block_delta"
		payload["delta"] = map[string]any{"type": "text_delta", "text": value}
		event.Type, event.Name = "content_block_delta", "content_block_delta"
		return
	}
	setSecretStreamTextDelta(payload, kind, value)
}

func secretStreamChatTerminal(payload map[string]any) bool {
	choices, _ := payload["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if stringValue(choice["finish_reason"]) != "" {
			return true
		}
	}
	return false
}

func addSecretStreamSequence(payload map[string]any, add int) {
	if add <= 0 {
		return
	}
	if sequence, ok := numberAsInt(payload["sequence_number"]); ok {
		payload["sequence_number"] = sequence + add
	}
}

func redactSecretStreamSnapshot(value any, state *piiRedactionState) (bool, error) {
	changed := false
	switch x := value.(type) {
	case map[string]any:
		for key, child := range x {
			if key == "input" && strings.EqualFold(stringValue(x["type"]), "custom_tool_call") {
				switch v := child.(type) {
				case string:
					redacted, inputChanged, err := redactToolArgumentJSON(v, state)
					if err != nil {
						return changed, err
					}
					x[key] = redacted
					changed = changed || inputChanged
				case map[string]any, []any:
					redacted, inputChanged := redactToolArgumentValue(v, "", state)
					x[key] = redacted
					changed = changed || inputChanged
				}
				continue
			}
			switch v := child.(type) {
			case string:
				if key == "text" || key == "content" || key == "output_text" || key == "refusal" {
					redacted, _, err := redactSecretStreamChunk(state, "", v, true)
					if err != nil {
						return changed, err
					}
					x[key] = redacted
					changed = changed || redacted != v
				} else if key == "arguments" && strings.EqualFold(stringValue(x["type"]), "function_call") {
					redacted, argumentChanged, err := redactToolArgumentJSON(v, state)
					if err != nil {
						return changed, err
					}
					x[key] = redacted
					changed = changed || argumentChanged
				}
			default:
				childChanged, err := redactSecretStreamSnapshot(child, state)
				if err != nil {
					return changed, err
				}
				changed = childChanged || changed
			}
		}
	case []any:
		for _, child := range x {
			childChanged, err := redactSecretStreamSnapshot(child, state)
			if err != nil {
				return changed, err
			}
			changed = childChanged || changed
		}
	}
	return changed, nil
}

func filterNativeSecretStreamFrame(frame []byte, d *secretStreamDLP) ([][]byte, error) {
	framer := stream.NewFramer(bytes.NewReader(frame), int64(len(frame))+1)
	event, err := framer.Next()
	if err != nil {
		return [][]byte{frame}, nil
	}
	events, err := d.filterEvent(event)
	if err != nil {
		return nil, err
	}
	if len(events) == 1 && events[0].Name == event.Name && bytes.Equal(events[0].Data, event.Data) {
		return [][]byte{frame}, nil
	}
	frames := make([][]byte, 0, len(events))
	for i, filtered := range events {
		if i == len(events)-1 {
			frames = append(frames, replaceNativeSSEData(frame, filtered))
			continue
		}
		frames = append(frames, encodeNativeSSEEvent(filtered))
	}
	return frames, nil
}

func encodeNativeSSEEvent(event stream.Event) []byte {
	var encoded bytes.Buffer
	if event.Name != "" {
		fmt.Fprintf(&encoded, "event: %s\n", event.Name)
	}
	for _, line := range bytes.Split(event.Data, []byte{'\n'}) {
		encoded.WriteString("data: ")
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	encoded.WriteByte('\n')
	return encoded.Bytes()
}

func replaceNativeSSEData(frame []byte, event stream.Event) []byte {
	lines := bytes.Split(frame, []byte{'\n'})
	var out bytes.Buffer
	inserted := false
	for _, line := range lines {
		trimmed := bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			if !inserted {
				writeNativeSSEDataLines(&out, event.Data)
				inserted = true
			}
			continue
		}
		if len(trimmed) == 0 {
			continue
		}
		out.Write(trimmed)
		out.WriteByte('\n')
	}
	if !inserted {
		writeNativeSSEDataLines(&out, event.Data)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func writeNativeSSEDataLines(out *bytes.Buffer, data []byte) {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		out.WriteString("data: ")
		out.Write(line)
		out.WriteByte('\n')
	}
}

// redactSecretStreamChunk keeps a bounded suffix so built-in credentials split
// across adjacent text deltas are matched before any part reaches the caller.
// A private-key block is held from its BEGIN marker through its END marker.
func redactSecretStreamChunk(state *piiRedactionState, pending, chunk string, final bool) (safe, remainder string, err error) {
	combined := pending + chunk
	if hasSecretStreamOversizeCandidate(combined) {
		return "", "", errSecretStreamWindowExceeded
	}
	if final {
		if _, err := secretStreamPEMReleaseBoundary(combined, len(combined), true); err != nil {
			return "", "", err
		}
		return redactSecretStreamPrefix(state, combined), "", nil
	}
	boundary := len(combined) - secretStreamTailBytes
	boundary, err = secretStreamJWTReleaseBoundary(combined, boundary)
	if err != nil {
		return "", "", err
	}
	boundary, err = secretStreamKeywordReleaseBoundary(combined, boundary)
	if err != nil {
		return "", "", err
	}
	boundary, err = secretStreamPEMReleaseBoundary(combined, boundary, false)
	if err != nil {
		return "", "", err
	}
	if boundary <= 0 {
		return "", combined, nil
	}
	// The tail size bounds ordinary candidates, but some provider formats have
	// longer valid matches. Never split a full detector match at the release
	// edge: move the cut back to that match's start and inspect it as one value.
	var matches = compiledSecretStreamMatcher().FindAllStringIndex(combined, -1)
	for _, span := range matches {
		if span[0] < boundary && boundary < span[1] {
			boundary = span[0]
		}
	}
	boundary = utf8SafeBoundary(combined, boundary)
	safe = combined[:boundary]
	if strings.Contains(safe, "eyJ") || strings.Contains(safe, "-----BEGIN ") {
		safe = state.redact(safe)
	} else {
		for _, span := range matches {
			if span[1] <= boundary {
				safe = state.redact(safe)
				break
			}
		}
	}
	return safe, combined[boundary:], nil
}

func redactSecretStreamPrefix(state *piiRedactionState, value string) string {
	if value == "" || compiledSecretStreamMatcher().FindStringIndex(value) == nil && !strings.Contains(value, "eyJ") && !strings.Contains(value, "-----BEGIN ") {
		return value
	}
	return state.redact(value)
}

// secretStreamJWTReleaseBoundary holds a plausible multi-segment JWT from its
// eyJ start until a separator proves the candidate complete or invalid. This
// supports long Azure identity claims without retaining a 12 KiB tail for
// every ordinary text delta.
func secretStreamJWTReleaseBoundary(value string, boundary int) (int, error) {
	for search := 0; ; {
		rel := strings.Index(value[search:], "eyJ")
		if rel < 0 {
			return boundary, nil
		}
		start := search + rel
		if start > 0 && isJWTNameByte(value[start-1]) {
			search = start + 3
			continue
		}
		cursor := start
		valid := true
		for segment := 0; segment < 3; segment++ {
			segmentStart := cursor
			if segment == 0 {
				cursor += 3 // The first segment begins with eyJ.
			}
			for cursor < len(value) && isJWTSegmentByte(value[cursor]) {
				cursor++
			}
			segmentLength := cursor - segmentStart
			if segmentLength > 4096 {
				return 0, errSecretStreamWindowExceeded
			}
			if cursor == len(value) {
				if valid {
					if start < boundary {
						boundary = start
					}
					return boundary, nil
				}
				// Invalid candidates may contain a later real eyJ marker;
				// keep scanning from just past this marker even at the current
				// buffer edge.
				search = start + 3
				if search >= len(value) {
					return boundary, nil
				}
				break
			}
			minimum := 8
			if segment == 0 {
				minimum = 11
			}
			if segmentLength < minimum {
				valid = false
			}
			if segment < 2 && value[cursor] == '.' {
				cursor++
				continue
			}
			if segment < 2 {
				valid = false
			}
			break
		}
		if valid {
			if cursor == len(value) && start < boundary {
				boundary = start
			} else if start < boundary && boundary < cursor {
				boundary = start
			}
		}
		// An invalid candidate can contain another real eyJ start (for
		// example, a short malformed segment followed by a hyphen and JWT).
		// Resume just after the current marker so nested candidates are scanned.
		search = start + 3
		if search > len(value) {
			return boundary, nil
		}
	}
}

func isJWTNameByte(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
}

// secretStreamKeywordReleaseBoundary holds credential labels while a value
// marker or value may still follow them. This preserves assignments with long
// whitespace padding and bearer headers whose token starts far after Bearer.
func secretStreamKeywordReleaseBoundary(value string, boundary int) (int, error) {
	for _, span := range secretStreamKeywordStart.FindAllStringIndex(value, -1) {
		start, cursor := span[0], span[1]
		authorization := strings.EqualFold(value[start:cursor], "Authorization")
		var incomplete bool
		var err error
		cursor, incomplete, err = skipSecretKeywordSpace(value, start, cursor)
		if err != nil {
			return 0, err
		}
		if incomplete {
			return min(boundary, start), nil
		}
		if authorization {
			if value[cursor] != ':' {
				continue
			}
			cursor++
			cursor, incomplete, err = skipSecretKeywordSpace(value, start, cursor)
			if err != nil {
				return 0, err
			}
			if incomplete {
				return min(boundary, start), nil
			}
			remaining := value[cursor:]
			const bearer = "Bearer"
			if len(remaining) < len(bearer) {
				if strings.EqualFold(remaining, bearer[:len(remaining)]) {
					return min(boundary, start), nil
				}
				continue
			}
			if !strings.EqualFold(remaining[:len(bearer)], bearer) || len(remaining) > len(bearer) && isJWTNameByte(remaining[len(bearer)]) {
				continue
			}
			cursor += len(bearer)
			if cursor == len(value) {
				return min(boundary, start), nil
			}
			if !isSecretKeywordSpace(value[cursor]) {
				continue
			}
			cursor, incomplete, err = skipSecretKeywordSpace(value, start, cursor)
			if err != nil {
				return 0, err
			}
			if incomplete {
				return min(boundary, start), nil
			}
			tokenStart := cursor
			for cursor < len(value) && isBearerTokenByte(value[cursor]) {
				cursor++
			}
			if cursor == len(value) {
				if len(value)-start >= secretStreamPEMMax {
					return 0, errSecretStreamWindowExceeded
				}
				return min(boundary, start), nil
			}
			if cursor == tokenStart {
				continue
			}
			continue // A complete value is handled by the compiled detector.
		}

		if cursor != len(value) && value[cursor] != ':' && value[cursor] != '=' {
			continue
		}
		if cursor == len(value) {
			return min(boundary, start), nil
		}
		cursor++
		cursor, incomplete, err = skipSecretKeywordSpace(value, start, cursor)
		if err != nil {
			return 0, err
		}
		if incomplete {
			return min(boundary, start), nil
		}
		if value[cursor] == '"' || value[cursor] == '\'' {
			cursor++
			if cursor == len(value) {
				return min(boundary, start), nil
			}
		}
		tokenStart := cursor
		for cursor < len(value) && isCredentialAssignmentByte(value[cursor]) {
			cursor++
		}
		if cursor == len(value) {
			if len(value)-start >= secretStreamPEMMax {
				return 0, errSecretStreamWindowExceeded
			}
			return min(boundary, start), nil
		}
		if cursor == tokenStart {
			continue
		}
	}
	return boundary, nil
}

func skipSecretKeywordSpace(value string, start, cursor int) (int, bool, error) {
	for cursor < len(value) && isSecretKeywordSpace(value[cursor]) {
		cursor++
		if cursor-start >= secretStreamPEMMax {
			return 0, false, errSecretStreamWindowExceeded
		}
	}
	return cursor, cursor == len(value), nil
}

func isSecretKeywordSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\f'
}

func isCredentialAssignmentByte(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("_./+=-", rune(value))
}

func isBearerTokenByte(value byte) bool {
	return isCredentialAssignmentByte(value) || value == '~'
}

// secretStreamPEMReleaseBoundary scans all BEGIN markers, including markers
// after an already completed certificate. The earliest unresolved block stays
// held; an unterminated or oversized block fails closed and is never released.
func secretStreamPEMReleaseBoundary(combined string, boundary int, final bool) (int, error) {
	for search := 0; ; {
		beginRel := strings.Index(combined[search:], "-----BEGIN ")
		if beginRel < 0 {
			break
		}
		begin := search + beginRel
		endRel := strings.Index(combined[begin+len("-----BEGIN "):], "-----END ")
		if endRel < 0 {
			if final || len(combined)-begin >= secretStreamPEMMax {
				return 0, errSecretStreamWindowExceeded
			}
			if begin < boundary {
				boundary = begin
			}
			break
		}
		end := begin + len("-----BEGIN ") + endRel
		endLabel := end + len("-----END ")
		closeRel := strings.Index(combined[endLabel:], "-----")
		if closeRel < 0 {
			if final || len(combined)-begin >= secretStreamPEMMax {
				return 0, errSecretStreamWindowExceeded
			}
			if begin < boundary {
				boundary = begin
			}
			break
		}
		pemEnd := endLabel + closeRel + len("-----")
		if pemEnd-begin > secretStreamPEMMax {
			return 0, errSecretStreamWindowExceeded
		}
		if begin < boundary && boundary < pemEnd {
			boundary = begin
		}
		search = pemEnd
		if search >= len(combined) {
			break
		}
	}
	return boundary, nil
}

func hasSecretStreamOversizeCandidate(value string) bool {
	for {
		begin := strings.Index(value, "-----BEGIN ")
		if begin < 0 {
			return secretStreamOversizeCandidate.MatchString(value) || hasOversizeJWT(value)
		}
		if secretStreamOversizeCandidate.MatchString(value[:begin]) || hasOversizeJWT(value[:begin]) {
			return true
		}
		end := strings.Index(value[begin:], "-----END ")
		if end < 0 {
			return false
		}
		end += begin
		endLabel := end + len("-----END ")
		close := strings.Index(value[endLabel:], "-----")
		if close < 0 {
			return false
		}
		pemEnd := endLabel + close + len("-----")
		value = value[:begin] + value[pemEnd:]
	}
}

func hasOversizeJWT(value string) bool {
	for search := 0; ; {
		rel := strings.Index(value[search:], "eyJ")
		if rel < 0 {
			return false
		}
		start := search + rel
		if start > 0 && isJWTNameByte(value[start-1]) {
			search = start + 3
			continue
		}
		firstDot := start + 3
		for firstDot < len(value) && isJWTSegmentByte(value[firstDot]) {
			firstDot++
		}
		if firstDot >= len(value) || value[firstDot] != '.' {
			if firstDot >= len(value) {
				return false
			}
			search = max(start+3, firstDot+1)
			continue
		}
		secondDot := firstDot + 1
		for secondDot < len(value) && isJWTSegmentByte(value[secondDot]) {
			secondDot++
		}
		if secondDot >= len(value) || value[secondDot] != '.' {
			if secondDot >= len(value) {
				return false
			}
			search = max(start+3, secondDot+1)
			continue
		}
		end := secondDot + 1
		for end < len(value) && isJWTSegmentByte(value[end]) {
			end++
		}
		segments := []string{value[start:firstDot], value[firstDot+1 : secondDot], value[secondDot+1 : end]}
		valid := true
		for _, segment := range segments {
			if len(segment) < 8 {
				valid = false
				break
			}
			for i := 0; i < len(segment); i++ {
				if !isJWTSegmentByte(segment[i]) {
					valid = false
					break
				}
			}
		}
		if valid && (len(segments[0]) > 4096 || len(segments[1]) > 4096 || len(segments[2]) > 4096) {
			return true
		}
		search = start + 3
	}
}

func isJWTSegmentByte(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '-'
}

func utf8SafeBoundary(value string, boundary int) int {
	if boundary < 0 {
		return 0
	}
	if boundary > len(value) {
		return len(value)
	}
	for boundary > 0 && boundary < len(value) && !utf8.RuneStart(value[boundary]) {
		boundary--
	}
	return boundary
}
