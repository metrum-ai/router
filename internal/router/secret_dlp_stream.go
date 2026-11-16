// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/metrum-ai/router/internal/stream"
)

const (
	// Keep at least the maximum built-in credential candidate pending. The
	// longest JWT detector is three bounded 1000-byte segments plus separators.
	secretStreamTailBytes = 3*1000 + 2
	secretStreamPEMMax    = 64 << 10
)

var errSecretStreamWindowExceeded = errors.New("secret DLP stream inspection window exceeded")

var secretStreamOversizeCandidate = regexp.MustCompile(`(?i)(?:\b(?:gh[pousr]_|github_pat_|xox[baprs]-|sk_(?:live|test)_|sk-proj-|sk-svcacct-|sk-ant-api03-|sk-or-v1-|sk-|gsk_|AIza|rtr_metrum_)[A-Za-z0-9_-]{257}|\beyJ[A-Za-z0-9_-]{1000}[A-Za-z0-9_-]+|\beyJ[A-Za-z0-9_-]{8,1000}\.[A-Za-z0-9_-]{1000}[A-Za-z0-9_-]+|\beyJ[A-Za-z0-9_-]{8,1000}\.[A-Za-z0-9_-]{8,1000}\.[A-Za-z0-9_-]{1000}[A-Za-z0-9_-]+|\b(?:api[_-]?key|access[_-]?token|client[_-]?secret)\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{257}|\bpassword\s*[:=]\s*["'][A-Za-z0-9_./+=-]{257}["']|\bAuthorization\s*:\s*Bearer\s+[A-Za-z0-9._~+/-]{257})`)

// secretStreamDLP is request-local state shared by stream text and held tool
// call inspection. A holder invokes redactCandidate only after the complete
// candidate is available and before releasing it downstream.
type secretStreamDLP struct {
	state       *piiRedactionState
	mode        string
	failOnMatch bool
	pending     string
	lastEvent   stream.Event
	lastKind    string
	sequenceAdd int
}

func (d *secretStreamDLP) redactCandidate(value string) (string, error) {
	if d == nil || d.state == nil || d.state.result == nil {
		return value, nil
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
			return d.flushBefore(event, nil)
		}
		return []stream.Event{event}, nil
	}
	eventType := event.Type
	if eventType == "" {
		eventType = stringValue(payload["type"])
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
		if safe == "" {
			event.Data = nil
		}
	}
	snapshot := eventType == "response.output_text.done" || eventType == "response.content_part.done" || eventType == "response.output_item.done" || eventType == "response.completed"
	terminal := event.Name == "message_stop" || event.Name == "content_block_stop" && d.lastKind == "anthropic" || eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" || secretStreamChatTerminal(payload)
	chatContentAndFinish := ok && secretStreamChatTerminal(payload)
	if snapshot {
		before := d.state.result.Replacements
		snapshotChanged := redactSecretStreamSnapshot(payload, d.state)
		if err := d.failIfMatched(before); err != nil {
			return nil, err
		}
		if !snapshotChanged && d.pending == "" && d.sequenceAdd == 0 && !ok {
			return []stream.Event{event}, nil
		}
	}
	if terminal || snapshot {
		if terminal && !snapshot && d.pending == "" {
			return []stream.Event{event}, nil
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
		return out, nil
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
		tail := d.state.redact(d.pending)
		if err := d.failIfMatched(before); err != nil {
			return nil, err
		}
		var previous map[string]any
		if json.Unmarshal(d.lastEvent.Data, &previous) == nil {
			setSecretStreamTextDelta(previous, d.lastKind, tail)
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
			flushed := d.lastEvent
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
	case "response.output_text.delta":
		value, ok = payload["delta"].(string)
		return "responses", value, ok
	case "content_block_delta":
		delta, _ := payload["delta"].(map[string]any)
		if delta != nil && stringValue(delta["type"]) == "text_delta" {
			value, ok = delta["text"].(string)
			return "anthropic", value, ok
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
	case "anthropic":
		if delta, ok := payload["delta"].(map[string]any); ok {
			delta["text"] = value
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

func redactSecretStreamSnapshot(value any, state *piiRedactionState) bool {
	changed := false
	switch x := value.(type) {
	case map[string]any:
		for key, child := range x {
			switch v := child.(type) {
			case string:
				if key == "text" || key == "content" || key == "output_text" {
					redacted := state.redact(v)
					x[key] = redacted
					changed = changed || redacted != v
				}
			default:
				changed = redactSecretStreamSnapshot(child, state) || changed
			}
		}
	case []any:
		for _, child := range x {
			changed = redactSecretStreamSnapshot(child, state) || changed
		}
	}
	return changed
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
		return state.redact(combined), "", nil
	}
	if begin := strings.Index(combined, "-----BEGIN "); begin >= 0 {
		end := strings.Index(combined[begin:], "-----END ")
		if end < 0 {
			if len(combined)-begin > secretStreamPEMMax {
				return "", "", errSecretStreamWindowExceeded
			}
			boundary := utf8SafeBoundary(combined, begin)
			return state.redact(combined[:boundary]), combined[boundary:], nil
		}
		end += begin
		endLabel := end + len("-----END ")
		endLine := strings.Index(combined[endLabel:], "-----")
		if endLine < 0 {
			if len(combined)-begin > secretStreamPEMMax {
				return "", "", errSecretStreamWindowExceeded
			}
			boundary := utf8SafeBoundary(combined, begin)
			return state.redact(combined[:boundary]), combined[boundary:], nil
		}
		pemEnd := endLabel + endLine + len("-----")
		if pemEnd-begin > secretStreamPEMMax {
			return "", "", errSecretStreamWindowExceeded
		}
		if boundary := len(combined) - secretStreamTailBytes; begin < boundary && boundary < pemEnd {
			boundary = utf8SafeBoundary(combined, begin)
			return state.redact(combined[:boundary]), combined[boundary:], nil
		}
	}
	boundary := len(combined) - secretStreamTailBytes
	if boundary <= 0 {
		return "", combined, nil
	}
	// The tail size bounds ordinary candidates, but some provider formats have
	// longer valid matches. Never split a full detector match at the release
	// edge: move the cut back to that match's start and inspect it as one value.
	for _, rule := range state.rules {
		for _, span := range rule.re.FindAllStringIndex(combined, -1) {
			if span[0] < boundary && boundary < span[1] {
				boundary = span[0]
			}
		}
	}
	boundary = utf8SafeBoundary(combined, boundary)
	return state.redact(combined[:boundary]), combined[boundary:], nil
}

func hasSecretStreamOversizeCandidate(value string) bool {
	for {
		begin := strings.Index(value, "-----BEGIN ")
		if begin < 0 {
			return secretStreamOversizeCandidate.MatchString(value)
		}
		if secretStreamOversizeCandidate.MatchString(value[:begin]) {
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
