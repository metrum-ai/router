// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func nativeStreamEligible(req *IRRequest, callerDialect, upstreamDialect string) bool {
	if req == nil || !req.Stream || normalizeDialect(callerDialect) != normalizeDialect(upstreamDialect) {
		return false
	}
	switch normalizeDialect(upstreamDialect) {
	case "openai-chat", "openai-responses", "anthropic":
		return true
	default:
		return false
	}
}

func enableNativeUpstreamStream(raw []byte, req *IRRequest, dialect string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	body["stream"] = true
	if normalizeDialect(dialect) == "openai-chat" && req != nil && req.Raw != nil {
		if streamOptions, ok := req.Raw["stream_options"]; ok {
			body["stream_options"] = streamOptions
		}
	}
	return json.Marshal(body)
}

type nativeStreamResult struct {
	Response  *IRResponse
	Committed bool
	Done      bool
	Terminal  bool
	Bytes     int64
}

func proxyNativeSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, dialect, model string, maxBytes int64, rc *requestContext, transforms ...IdentifierTransform) (nativeStreamResult, error) {
	result := nativeStreamResult{Response: &IRResponse{Model: model, Streamed: true}}
	rewriter := nativeIDRewriter{}
	if len(transforms) > 0 {
		rewriter.transform = transforms[0]
	}
	var downstreamStart time.Time
	defer func() {
		if rc != nil && !downstreamStart.IsZero() {
			duration := durationMillis(time.Since(downstreamStart))
			rc.rec.DownstreamMS = &duration
		}
	}()
	reader := bufio.NewReader(body)
	var frame bytes.Buffer
	for {
		if err := ctx.Err(); err != nil {
			upErr := classifyContextError(err)
			upErr.Committed = result.Committed
			return result, upErr
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			result.Bytes += int64(len(line))
			if maxBytes > 0 && result.Bytes > maxBytes {
				return result, upstreamError{Class: "upstream_response_too_large", Message: "upstream response exceeded configured size limit", Committed: result.Committed, ResponseLen: result.Bytes}
			}
			_, _ = frame.Write(line)
		}
		frameComplete := isSSEBlankLine(line)
		if frameComplete || (err == io.EOF && frame.Len() > 0) {
			rawFrame := append([]byte(nil), frame.Bytes()...)
			frame.Reset()
			done, terminal := accumulateNativeSSE(result.Response, dialect, rawFrame)
			if done {
				result.Done = true
			}
			if terminal {
				result.Terminal = true
			}
			if !result.Committed {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.WriteHeader(http.StatusOK)
				result.Committed = true
				downstreamStart = time.Now()
				if rc != nil {
					ttfb := time.Since(rc.start).Milliseconds()
					rc.rec.TTFBMS = &ttfb
				}
			}
			rawFrame = rewriter.rewrite(rawFrame, dialect)
			if _, writeErr := w.Write(rawFrame); writeErr != nil {
				return result, upstreamError{Class: "downstream_write_error", Message: "downstream stream write failed", Canceled: true, Committed: true, Err: writeErr}
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if result.Done {
				return result, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !result.Committed {
					return result, upstreamError{Class: "empty_stream", Message: "upstream stream ended before an event", Retryable: true}
				}
				if !result.Done && !result.Terminal {
					return result, upstreamError{Class: "stream_interrupted", Message: "upstream stream ended before terminal event", Retryable: true, Committed: true}
				}
				return result, nil
			}
			upErr := classifyContextOrNetworkError(ctx, ctx, err)
			upErr.Committed = result.Committed
			return result, upErr
		}
	}
}

func isSSEBlankLine(line []byte) bool {
	return len(bytes.TrimRight(line, "\r\n")) == 0 && len(line) > 0
}

func accumulateNativeSSE(resp *IRResponse, dialect string, frame []byte) (done, terminal bool) {
	if resp == nil {
		return false, false
	}
	data := sseFrameData(frame)
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		isChat := normalizeDialect(dialect) == "openai-chat"
		return isChat, isChat
	}
	if len(data) == 0 {
		return false, false
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return false, false
	}
	if id := strings.TrimSpace(stringValue(payload["id"])); id != "" {
		resp.ID = id
	}
	if upstreamModel := strings.TrimSpace(stringValue(payload["model"])); upstreamModel != "" {
		resp.Model = upstreamModel
	}
	switch normalizeDialect(dialect) {
	case "openai-chat":
		mergeStreamUsage(&resp.Usage, usageFromMap(payload["usage"]))
		for _, choiceValue := range valueAsSlice(payload["choices"]) {
			choice, _ := choiceValue.(map[string]any)
			if choice == nil {
				continue
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				resp.Text += contentToText(delta["content"])
			}
			if finishReason := stringValue(choice["finish_reason"]); finishReason != "" {
				resp.StopReason = finishReason
				terminal = true
			}
		}
	case "openai-responses":
		switch stringValue(payload["type"]) {
		case "response.output_text.delta":
			resp.Text += stringValue(payload["delta"])
		case "response.completed", "response.failed", "response.incomplete":
			if response, ok := payload["response"].(map[string]any); ok {
				if id := stringValue(response["id"]); id != "" {
					resp.ID = id
				}
				if upstreamModel := stringValue(response["model"]); upstreamModel != "" {
					resp.Model = upstreamModel
				}
				if status := stringValue(response["status"]); status != "" {
					resp.StopReason = status
				}
				mergeStreamUsage(&resp.Usage, usageFromMap(response["usage"]))
			}
			return true, true
		}
	case "anthropic":
		switch stringValue(payload["type"]) {
		case "message_start":
			if message, ok := payload["message"].(map[string]any); ok {
				if id := stringValue(message["id"]); id != "" {
					resp.ID = id
				}
				if upstreamModel := stringValue(message["model"]); upstreamModel != "" {
					resp.Model = upstreamModel
				}
				mergeStreamUsage(&resp.Usage, usageFromMap(message["usage"]))
			}
		case "content_block_delta":
			if delta, ok := payload["delta"].(map[string]any); ok && stringValue(delta["type"]) == "text_delta" {
				resp.Text += stringValue(delta["text"])
			}
		case "message_delta":
			if delta, ok := payload["delta"].(map[string]any); ok {
				resp.StopReason = stringValue(delta["stop_reason"])
			}
			mergeStreamUsage(&resp.Usage, usageFromMap(payload["usage"]))
		case "message_stop":
			return true, true
		}
	}
	return false, terminal
}

func sseFrameData(frame []byte) []byte {
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(frame))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func mergeStreamUsage(dst *Usage, src Usage) {
	if dst == nil {
		return
	}
	if src.InputTokens != 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens != 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.TotalTokens != 0 {
		dst.TotalTokens = src.TotalTokens
	}
	if src.ReasoningTokens != nil {
		dst.ReasoningTokens = src.ReasoningTokens
	}
	if src.CachedInputTokens != nil {
		dst.CachedInputTokens = src.CachedInputTokens
	}
	if src.InputImageTokens != 0 {
		dst.InputImageTokens = src.InputImageTokens
	}
	if dst.InputTokens != 0 || dst.OutputTokens != 0 {
		dst.TotalTokens = dst.InputTokens + dst.OutputTokens
	}
}

func committedStreamError(err error) bool {
	var upstream upstreamError
	return errors.As(err, &upstream) && upstream.Committed
}

func streamErrorCode(err error) string {
	var upstream upstreamError
	if errors.As(err, &upstream) {
		if upstream.Canceled {
			return "client-canceled"
		}
		if upstream.Class != "" {
			return upstream.Class
		}
	}
	return fmt.Sprintf("%T", err)
}
