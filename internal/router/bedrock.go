// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bt "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// bedrockMaxTokens converts a decoded token limit to int32 without
// truncating: values outside the int32 range are clamped so a hostile or
// malformed max_tokens can never wrap to a negative or small positive value.
func bedrockMaxTokens(n int) *int32 {
	if n < 0 {
		n = 0
	}
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	return aws.Int32(int32(n))
}

// bedrockInput consumes the router's canonical Chat bridge, preserving tool IDs.
func bedrockInput(body []byte) (*bedrockruntime.ConverseInput, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	in := &bedrockruntime.ConverseInput{ModelId: aws.String(stringValue(raw["model"])), InferenceConfig: &bt.InferenceConfiguration{}}
	if n, ok := numberAsInt(raw["max_tokens"]); ok {
		in.InferenceConfig.MaxTokens = bedrockMaxTokens(n)
	}
	if n, ok := numberAsInt(raw["max_completion_tokens"]); ok {
		in.InferenceConfig.MaxTokens = bedrockMaxTokens(n)
	}
	if n, ok := numberAsFloat(raw["temperature"]); ok {
		in.InferenceConfig.Temperature = aws.Float32(float32(n))
	}
	in.InferenceConfig.StopSequences = decodeStopSequences(raw)
	msgs, _ := raw["messages"].([]any)
	for _, v := range msgs {
		m, _ := v.(map[string]any)
		role := stringValue(m["role"])
		if role == "system" || role == "developer" {
			in.System = append(in.System, &bt.SystemContentBlockMemberText{Value: contentToText(m["content"])})
			continue
		}
		msg := bt.Message{Role: bt.ConversationRole(role)}
		if role == "tool" {
			msg.Role = bt.ConversationRoleUser
			msg.Content = append(msg.Content, &bt.ContentBlockMemberToolResult{Value: bt.ToolResultBlock{ToolUseId: aws.String(stringValue(m["tool_call_id"])), Content: []bt.ToolResultContentBlock{&bt.ToolResultContentBlockMemberText{Value: contentToText(m["content"])}}}})
		} else {
			text := contentToText(m["content"])
			if text != "" {
				msg.Content = append(msg.Content, &bt.ContentBlockMemberText{Value: text})
			}
			// Reject images explicitly rather than silently dropping them.
			if parts, ok := m["content"].([]any); ok {
				for _, p := range parts {
					x, _ := p.(map[string]any)
					if stringValue(x["type"]) != "text" {
						return nil, fmt.Errorf("Bedrock content type %q is not supported", x["type"])
					}
				}
			}
			calls, _ := m["tool_calls"].([]any)
			for _, c := range calls {
				call, _ := c.(map[string]any)
				f, _ := call["function"].(map[string]any)
				var args any
				if err := json.Unmarshal([]byte(stringValue(f["arguments"])), &args); err != nil {
					return nil, fmt.Errorf("invalid tool arguments: %w", err)
				}
				msg.Content = append(msg.Content, &bt.ContentBlockMemberToolUse{Value: bt.ToolUseBlock{ToolUseId: aws.String(stringValue(call["id"])), Name: aws.String(stringValue(f["name"])), Input: document.NewLazyDocument(args)}})
			}
		}
		if len(in.Messages) > 0 && in.Messages[len(in.Messages)-1].Role == msg.Role {
			in.Messages[len(in.Messages)-1].Content = append(in.Messages[len(in.Messages)-1].Content, msg.Content...)
		} else {
			in.Messages = append(in.Messages, msg)
		}
	}
	tools, _ := raw["tools"].([]any)
	if len(tools) > 0 {
		in.ToolConfig = &bt.ToolConfiguration{}
		for _, v := range tools {
			t, _ := v.(map[string]any)
			f, _ := t["function"].(map[string]any)
			if f == nil {
				return nil, fmt.Errorf("Bedrock supports function tools only")
			}
			schema := f["parameters"]
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			spec := bt.ToolSpecification{Name: aws.String(stringValue(f["name"])), InputSchema: &bt.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(schema)}}
			if description := stringValue(f["description"]); description != "" {
				spec.Description = aws.String(description)
			}
			in.ToolConfig.Tools = append(in.ToolConfig.Tools, &bt.ToolMemberToolSpec{Value: spec})
		}
		switch c := raw["tool_choice"].(type) {
		case string:
			switch c {
			case "auto":
				in.ToolConfig.ToolChoice = &bt.ToolChoiceMemberAuto{Value: bt.AutoToolChoice{}}
			case "required":
				in.ToolConfig.ToolChoice = &bt.ToolChoiceMemberAny{Value: bt.AnyToolChoice{}}
			case "none":
				in.ToolConfig = nil
			}
		case map[string]any:
			switch stringValue(c["type"]) {
			case "auto":
				in.ToolConfig.ToolChoice = &bt.ToolChoiceMemberAuto{Value: bt.AutoToolChoice{}}
			case "required":
				in.ToolConfig.ToolChoice = &bt.ToolChoiceMemberAny{Value: bt.AnyToolChoice{}}
			case "none":
				in.ToolConfig = nil
			case "function":
				f, _ := c["function"].(map[string]any)
				in.ToolConfig.ToolChoice = &bt.ToolChoiceMemberTool{Value: bt.SpecificToolChoice{Name: aws.String(stringValue(f["name"]))}}
			default:
				return nil, fmt.Errorf("unsupported Bedrock tool_choice")
			}
		}
	}
	return in, nil
}

func bedrockFinish(r bt.StopReason) string {
	switch r {
	case bt.StopReasonToolUse:
		return "tool_calls"
	case bt.StopReasonMaxTokens:
		return "length"
	default:
		return "stop"
	}
}
func bedrockUsage(u *bt.TokenUsage) map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{"prompt_tokens": aws.ToInt32(u.InputTokens), "completion_tokens": aws.ToInt32(u.OutputTokens), "total_tokens": aws.ToInt32(u.TotalTokens)}
}
func bedrockError(err error) *http.Response {
	status := http.StatusBadGateway
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "ThrottlingException":
			status = 429
		case "ValidationException":
			status = 400
		case "AccessDeniedException":
			status = 403
		case "ModelNotReadyException", "ServiceUnavailableException":
			status = 503
		}
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": err.Error(), "type": "bedrock_error"}})
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}
}

// bedrockDo uses the SDK's default credential chain and SigV4, never provider API keys.
// It exposes Chat JSON/SSE internally so all existing dialect bridges and accounting apply.
func bedrockDo(ctx context.Context, region string, body []byte, clients ...*http.Client) (*http.Response, error) {
	in, err := bedrockInput(body)
	if err != nil {
		return bedrockError(&smithy.GenericAPIError{Code: "ValidationException", Message: err.Error()}), nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, func(o *awsconfig.LoadOptions) error {
		if region != "" {
			o.Region = region
		}
		if len(clients) > 0 {
			o.HTTPClient = clients[0]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	model := aws.ToString(in.ModelId)
	known := strings.TrimPrefix(model, "us.")
	switch known {
	case "meta.llama3-3-70b-instruct-v1:0", "mistral.mistral-large-3-675b-instruct", "qwen.qwen3-32b-v1:0", "deepseek.v3.2", "openai.gpt-oss-20b-1:0":
	default:
		log.Printf("Bedrock model %q is not in the validated catalog; configure capabilities and region-specific pricing explicitly", model)
	}
	id := responseID()
	client := bedrockruntime.NewFromConfig(cfg)
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	stream, _ := raw["stream"].(bool)
	h := make(http.Header)
	if !stream {
		out, err := client.Converse(ctx, in)
		if err != nil {
			return bedrockError(err), nil
		}
		msg := map[string]any{"role": "assistant"}
		text := ""
		calls := []any{}
		if m, ok := out.Output.(*bt.ConverseOutputMemberMessage); ok {
			for _, b := range m.Value.Content {
				switch b := b.(type) {
				case *bt.ContentBlockMemberText:
					text += b.Value
				case *bt.ContentBlockMemberToolUse:
					args, _ := b.Value.Input.MarshalSmithyDocument()
					calls = append(calls, map[string]any{"id": aws.ToString(b.Value.ToolUseId), "type": "function", "function": map[string]any{"name": aws.ToString(b.Value.Name), "arguments": string(args)}})
				}
			}
		}
		msg["content"] = text
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		b, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion", "model": aws.ToString(in.ModelId), "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": bedrockFinish(out.StopReason)}}, "usage": bedrockUsage(out.Usage)})
		h.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(b))}, nil
	}
	out, err := client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{ModelId: in.ModelId, Messages: in.Messages, System: in.System, InferenceConfig: in.InferenceConfig, ToolConfig: in.ToolConfig})
	if err != nil {
		return bedrockError(err), nil
	}
	reader, writer := io.Pipe()
	h.Set("Content-Type", "text/event-stream")
	go func() {
		defer writer.Close()
		s := out.GetStream()
		defer s.Close()
		emit := func(delta map[string]any, finish any, usage any) error {
			event := map[string]any{"id": id, "object": "chat.completion.chunk", "model": aws.ToString(in.ModelId), "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			if usage != nil {
				event["usage"] = usage
			}
			b, _ := json.Marshal(event)
			_, err := fmt.Fprintf(writer, "data: %s\n\n", b)
			return err
		}
		for e := range s.Events() {
			var delta map[string]any
			var finish, usage any
			switch e := e.(type) {
			case *bt.ConverseStreamOutputMemberMessageStart:
				delta = map[string]any{"role": "assistant"}
			case *bt.ConverseStreamOutputMemberContentBlockStart:
				if t, ok := e.Value.Start.(*bt.ContentBlockStartMemberToolUse); ok {
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": aws.ToInt32(e.Value.ContentBlockIndex), "id": aws.ToString(t.Value.ToolUseId), "type": "function", "function": map[string]any{"name": aws.ToString(t.Value.Name), "arguments": ""}}}}
				}
			case *bt.ConverseStreamOutputMemberContentBlockDelta:
				switch d := e.Value.Delta.(type) {
				case *bt.ContentBlockDeltaMemberText:
					delta = map[string]any{"content": d.Value}
				case *bt.ContentBlockDeltaMemberToolUse:
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": aws.ToInt32(e.Value.ContentBlockIndex), "function": map[string]any{"arguments": aws.ToString(d.Value.Input)}}}}
				}
			case *bt.ConverseStreamOutputMemberMessageStop:
				finish = bedrockFinish(e.Value.StopReason)
			case *bt.ConverseStreamOutputMemberMetadata:
				usage = bedrockUsage(e.Value.Usage)
			}
			if delta != nil || finish != nil || usage != nil {
				if err := emit(delta, finish, usage); err != nil {
					return
				}
			}
		}
		if err := s.Err(); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}()
	return &http.Response{StatusCode: 200, Header: h, Body: reader}, nil
}

func isBedrock(p ProviderConfig) bool { return normalizeAuthScheme(p.AuthScheme) == "bedrock" }
