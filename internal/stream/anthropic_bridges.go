// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"encoding/json"
	"errors"
)

func typedEvent(typ string, p map[string]any) Event {
	p["type"] = typ
	raw, _ := json.Marshal(p)
	return Event{Name: typ, Type: typ, Data: raw}
}

// AnthropicUpstreamToChatCaller accepts text and tool blocks only. Arguments are
// opaque strings throughout; thinking remains on the native Anthropic route.
type AnthropicUpstreamToChatCaller struct {
	IDs                         IdentifierEncoder
	Model                       string
	CreatedAt                   int64
	chat                        ResponsesUpstreamToChatCaller
	blocks                      map[int]string
	tools                       map[int]int
	argumentsSeen               map[int]bool
	started, terminal, finished bool
	stop                        string
	usage                       Usage
}

func (t *AnthropicUpstreamToChatCaller) Begin(TokenEstimate) ([]Event, error) { return nil, nil }
func (t *AnthropicUpstreamToChatCaller) UsageSnapshot() Usage                 { return t.usage }
func (t *AnthropicUpstreamToChatCaller) Next(up Event) ([]Event, error) {
	if t.terminal || t.finished {
		return nil, ErrTerminal
	}
	var p struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID    string `json:"id"`
			Usage struct {
				Input  int  `json:"input_tokens"`
				Output int  `json:"output_tokens"`
				Cached *int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Block struct {
			Type, ID, Name, Text string
			Input                json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type, Text string
			JSON       string `json:"partial_json"`
			Stop       string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(up.Data, &p); err != nil {
		return nil, err
	}
	if p.Type == "" || (up.Name != "" && up.Name != p.Type) {
		return nil, errors.New("stream: invalid Anthropic event type")
	}
	if p.Type == "ping" {
		return nil, nil
	}
	if p.Type == "error" {
		return nil, errors.New("stream: Anthropic upstream error")
	}
	if p.Type == "message_start" {
		if t.started || p.Message.ID == "" {
			return nil, errors.New("stream: invalid message_start")
		}
		t.started = true
		t.blocks = map[int]string{}
		t.tools = map[int]int{}
		t.argumentsSeen = map[int]bool{}
		t.chat = ResponsesUpstreamToChatCaller{IDs: t.IDs, ResponseID: p.Message.ID, Model: t.Model, CreatedAt: t.CreatedAt}
		t.usage = Usage{InputTokens: p.Message.Usage.Input, OutputTokens: p.Message.Usage.Output, CachedInputTokens: p.Message.Usage.Cached}
		t.usage.TotalTokens = t.usage.InputTokens + t.usage.OutputTokens
		return []Event{t.chat.chunk(map[string]any{"role": "assistant"}, nil)}, nil
	}
	if !t.started {
		return nil, errors.New("stream: event before message_start")
	}
	switch p.Type {
	case "content_block_start":
		if t.stop != "" {
			return nil, errors.New("stream: block after message_delta")
		}
		if _, ok := t.blocks[p.Index]; ok {
			return nil, errors.New("stream: duplicate Anthropic block")
		}
		t.blocks[p.Index] = p.Block.Type
		switch p.Block.Type {
		case "text":
			if p.Block.Text != "" {
				return []Event{t.chat.chunk(map[string]any{"content": p.Block.Text}, nil)}, nil
			}
		case "tool_use":
			if p.Block.ID == "" || p.Block.Name == "" {
				return nil, errors.New("stream: tool_use missing identity")
			}
			index := len(t.tools)
			t.tools[p.Index] = index
			id := p.Block.ID
			if t.IDs != nil {
				id = t.IDs.Encode(id)
			}
			args := ""
			if len(p.Block.Input) > 0 && string(p.Block.Input) != "{}" && string(p.Block.Input) != "null" {
				args = string(p.Block.Input)
				t.argumentsSeen[p.Index] = true
			}
			return []Event{t.chat.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": p.Block.Name, "arguments": args}}}}, nil)}, nil
		default:
			return nil, errors.New("stream: unsupported Anthropic content block (text/tools only)")
		}
	case "content_block_delta":
		kind := t.blocks[p.Index]
		if kind == "text" && p.Delta.Type == "text_delta" {
			return []Event{t.chat.chunk(map[string]any{"content": p.Delta.Text}, nil)}, nil
		}
		if kind == "tool_use" && p.Delta.Type == "input_json_delta" {
			if p.Delta.JSON != "" {
				t.argumentsSeen[p.Index] = true
			}
			return []Event{t.chat.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": t.tools[p.Index], "function": map[string]any{"arguments": p.Delta.JSON}}}}, nil)}, nil
		}
		return nil, errors.New("stream: invalid Anthropic block delta")
	case "content_block_stop":
		if t.blocks[p.Index] == "" || t.blocks[p.Index] == "closed" {
			return nil, errors.New("stream: stop for unknown or closed block")
		}
		emptyTool := t.blocks[p.Index] == "tool_use" && !t.argumentsSeen[p.Index]
		t.blocks[p.Index] = "closed"
		if emptyTool {
			return []Event{t.chat.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": t.tools[p.Index], "function": map[string]any{"arguments": "{}"}}}}, nil)}, nil
		}
	case "message_delta":
		for _, kind := range t.blocks {
			if kind != "closed" {
				return nil, errors.New("stream: message_delta before block stop")
			}
		}
		if t.stop != "" || p.Delta.Stop == "" {
			return nil, errors.New("stream: invalid message_delta")
		}
		switch p.Delta.Stop {
		case "end_turn", "stop_sequence":
			t.stop = "stop"
		case "tool_use":
			t.stop = "tool_calls"
		case "max_tokens":
			t.stop = "length"
		default:
			return nil, errors.New("stream: unsupported Anthropic stop reason")
		}
		t.usage.OutputTokens = p.Usage.Output
		t.usage.TotalTokens = t.usage.InputTokens + t.usage.OutputTokens
	case "message_stop":
		if t.stop == "" {
			return nil, errors.New("stream: message_stop without stop reason")
		}
		t.terminal = true
		t.chat.usage = map[string]any{"prompt_tokens": t.usage.InputTokens, "completion_tokens": t.usage.OutputTokens, "total_tokens": t.usage.TotalTokens}
		if t.usage.CachedInputTokens != nil {
			t.chat.usage["prompt_tokens_details"] = map[string]any{"cached_tokens": *t.usage.CachedInputTokens}
		}
		return []Event{t.chat.chunk(map[string]any{}, t.stop)}, ErrTerminal
	default:
		return nil, errors.New("stream: unsupported Anthropic event")
	}
	return nil, nil
}
func (t *AnthropicUpstreamToChatCaller) Finish(reason StopReason, _ Usage) ([]Event, error) {
	if t.finished {
		return nil, nil
	}
	t.finished = true
	if reason == StopComplete && t.terminal {
		return []Event{{Data: []byte("[DONE]")}}, nil
	}
	return nil, nil
}

// ResponsesUpstreamToAnthropicCaller reuses the P3 Responses reader and converts
// its incremental Chat fragments into Anthropic blocks. Only the final boundary
// encodes identifiers, so composition never encodes a tool id twice.
type ResponsesUpstreamToAnthropicCaller struct {
	ChatUpstreamToAnthropicCaller
	reader ResponsesUpstreamToChatCaller
}

func (t *ResponsesUpstreamToAnthropicCaller) UsageSnapshot() Usage { return t.reader.UsageSnapshot() }

func (t *ResponsesUpstreamToAnthropicCaller) Next(up Event) ([]Event, error) {
	events, err := t.reader.Next(up)
	if err != nil && !errors.Is(err, ErrTerminal) {
		return nil, err
	}
	if errors.Is(err, ErrTerminal) {
		tail, e := t.reader.Finish(StopComplete, Usage{})
		if e != nil {
			return nil, e
		}
		events = append(events, tail...)
	}
	return feedTranslator(&t.ChatUpstreamToAnthropicCaller, events)
}

// ChatUpstreamToAnthropicCaller emits a message preamble and indexed text/tool
// blocks as soon as each first fragment arrives. Open blocks close at choice end.
type ChatUpstreamToAnthropicCaller struct {
	IDs                       IdentifierEncoder
	ResponseID, Model         string
	reader                    ChatUpstreamToResponsesCaller
	begun, finished, terminal bool
	blocks                    map[int]int
	nextIndex                 int
}

func (t *ChatUpstreamToAnthropicCaller) Begin(est TokenEstimate) ([]Event, error) {
	if t.begun {
		return nil, nil
	}
	t.begun = true
	t.blocks = map[int]int{}
	t.reader = ChatUpstreamToResponsesCaller{ResponseID: t.ResponseID, Model: t.Model}
	_, err := t.reader.Begin(est)
	id := t.ResponseID
	if t.IDs != nil {
		id = t.IDs.Encode(id)
	}
	return []Event{typedEvent("message_start", map[string]any{"message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": t.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": est.InputTokens, "output_tokens": 0}}})}, err
}
func (t *ChatUpstreamToAnthropicCaller) UsageSnapshot() Usage { return t.reader.UsageSnapshot() }
func (t *ChatUpstreamToAnthropicCaller) Next(up Event) ([]Event, error) {
	if t.finished || t.terminal {
		return nil, ErrTerminal
	}
	// Anthropic has one text channel. Preserve OpenAI refusal text in it,
	// including fragments produced by the Responses reader.
	if !bytes.Equal(bytes.TrimSpace(up.Data), []byte("[DONE]")) {
		var payload map[string]any
		if err := json.Unmarshal(up.Data, &payload); err != nil {
			return nil, err
		}
		changed := false
		choices, _ := payload["choices"].([]any)
		for _, value := range choices {
			choice, _ := value.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if refusal, _ := delta["refusal"].(string); refusal != "" {
				content, _ := delta["content"].(string)
				delta["content"] = content + refusal
				delete(delta, "refusal")
				changed = true
			}
		}
		if changed {
			up.Data, _ = json.Marshal(payload)
		}
	}
	events, err := t.reader.Next(up)
	if err != nil && !errors.Is(err, ErrTerminal) {
		return nil, err
	}
	var out []Event
	for _, e := range events {
		var p struct {
			OutputIndex int `json:"output_index"`
			Delta       string
			Item        struct {
				Type, Name string
				CallID     string `json:"call_id"`
			}
		}
		if e2 := json.Unmarshal(e.Data, &p); e2 != nil {
			return nil, e2
		}
		index := t.blocks[p.OutputIndex]
		switch e.Type {
		case "response.output_item.added":
			index = t.nextIndex
			t.nextIndex++
			t.blocks[p.OutputIndex] = index
			block := map[string]any{"type": "text", "text": ""}
			if p.Item.Type == "function_call" {
				id := p.Item.CallID
				if t.IDs != nil {
					id = t.IDs.Encode(id)
				}
				block = map[string]any{"type": "tool_use", "id": id, "name": p.Item.Name, "input": map[string]any{}}
			}
			out = append(out, typedEvent("content_block_start", map[string]any{"index": index, "content_block": block}))
		case "response.output_text.delta", "response.function_call_arguments.delta":
			delta := map[string]any{"type": "text_delta", "text": p.Delta}
			if e.Type == "response.function_call_arguments.delta" {
				delta = map[string]any{"type": "input_json_delta", "partial_json": p.Delta}
			}
			out = append(out, typedEvent("content_block_delta", map[string]any{"index": index, "delta": delta}))
		case "response.output_item.done":
			out = append(out, typedEvent("content_block_stop", map[string]any{"index": index}))
		}
	}
	if errors.Is(err, ErrTerminal) {
		t.terminal = true
	}
	return out, err
}
func (t *ChatUpstreamToAnthropicCaller) Finish(reason StopReason, _ Usage) ([]Event, error) {
	if t.finished {
		return nil, nil
	}
	t.finished = true
	if reason != StopComplete || !t.terminal {
		return nil, nil
	}
	stop := "end_turn"
	switch t.reader.finishReason {
	case "tool_calls":
		stop = "tool_use"
	case "length":
		stop = "max_tokens"
	}
	u := t.UsageSnapshot()
	usage := map[string]any{"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens}
	if u.CachedInputTokens != nil {
		usage["cache_read_input_tokens"] = *u.CachedInputTokens
	}
	return []Event{typedEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": usage}), typedEvent("message_stop", map[string]any{})}, nil
}

// AnthropicUpstreamToResponsesCaller composes incremental translators through
// Chat fragments, sharing P2's Responses sequence and done-snapshot rules.
type AnthropicUpstreamToResponsesCaller struct {
	ChatUpstreamToResponsesCaller
	reader AnthropicUpstreamToChatCaller
}

func (t *AnthropicUpstreamToResponsesCaller) Next(up Event) ([]Event, error) {
	events, err := t.reader.Next(up)
	if err != nil && !errors.Is(err, ErrTerminal) {
		return nil, err
	}
	if errors.Is(err, ErrTerminal) {
		tail, e := t.reader.Finish(StopComplete, Usage{})
		if e != nil {
			return nil, e
		}
		events = append(events, tail...)
	}
	return feedTranslator(&t.ChatUpstreamToResponsesCaller, events)
}
func (t *AnthropicUpstreamToResponsesCaller) UsageSnapshot() Usage { return t.reader.UsageSnapshot() }
func feedTranslator(t StreamTranslator, events []Event) ([]Event, error) {
	var out []Event
	for _, e := range events {
		next, err := t.Next(e)
		out = append(out, next...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

var (
	_ StreamTranslator = (*AnthropicUpstreamToChatCaller)(nil)
	_ StreamTranslator = (*AnthropicUpstreamToResponsesCaller)(nil)
	_ StreamTranslator = (*ChatUpstreamToAnthropicCaller)(nil)
	_ StreamTranslator = (*ResponsesUpstreamToAnthropicCaller)(nil)
)
