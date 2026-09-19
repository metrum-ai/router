// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

func proxyResponsesSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, dialect, model string, maxBytes int64, rc *requestContext, transforms ...IdentifierTransform) (nativeStreamResult, error) {
	t := &stream.Responses{}
	if len(transforms) > 0 {
		t.IDs = transforms[0]
	}
	defer func() {
		if rc != nil {
			rc.rec.StreamUnknownEvents += t.UnknownEvents
		}
	}()
	return runResponsesStream(ctx, w, body, model, maxBytes, rc, t)
}

// runResponsesStream owns the lifecycle, including panic recovery and exactly one Finish.
func runResponsesStream(ctx context.Context, w http.ResponseWriter, body io.Reader, model string, maxBytes int64, rc *requestContext, t stream.StreamTranslator) (result nativeStreamResult, retErr error) {
	result.Response = &IRResponse{Model: model, Streamed: true}
	f := stream.NewFramer(body, maxBytes)
	reason := stream.StopError
	var started time.Time
	emit := func(events []stream.Event) error {
		for _, e := range events {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Only terminal metadata is retained, never deltas or complete output items.
			if e.Type == "response.completed" || e.Type == "response.failed" || e.Type == "response.incomplete" {
				accumulateNativeSSE(result.Response, "openai-responses", e.Frame())
			}
			if !result.Committed {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
			}
			// A failed/partial Write can still put bytes on the wire: forbid fallback.
			result.Committed = true
			if _, err := w.Write(e.Frame()); err != nil {
				return upstreamError{Class: "downstream_write_error", Message: "downstream stream write failed", Canceled: true, Committed: true, Err: err}
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return upstreamError{Class: "downstream_write_error", Message: "downstream stream flush failed", Canceled: true, Committed: true, Err: err}
			}
			if started.IsZero() {
				started = time.Now()
				if rc != nil {
					ms := time.Since(rc.start).Milliseconds()
					rc.rec.TTFBMS = &ms
				}
			}
		}
		return nil
	}
	defer func() {
		if recover() != nil {
			retErr = upstreamError{Class: "stream_translator_error", Message: "stream translator panicked", Committed: result.Committed}
		}
		if ctx.Err() != nil {
			reason = stream.StopCancel
		}
		var events []stream.Event
		var finishErr error
		func() {
			defer func() {
				if recover() != nil {
					finishErr = upstreamError{Class: "stream_translator_error", Message: "stream translator Finish panicked"}
				}
			}()
			u := result.Response.Usage
			events, finishErr = t.Finish(reason, stream.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens, ReasoningTokens: u.ReasoningTokens, CachedInputTokens: u.CachedInputTokens})
		}()
		if finishErr == nil && ctx.Err() == nil {
			finishErr = emit(events)
		}
		if retErr == nil {
			retErr = finishErr
		}
		result.Bytes = f.Bytes
		if retErr != nil {
			var ue upstreamError
			if !errors.As(retErr, &ue) {
				ue = classifyContextOrNetworkError(ctx, ctx, retErr)
			}
			ue.Committed = result.Committed
			retErr = ue
		}
		if rc != nil && !started.IsZero() {
			ms := durationMillis(time.Since(started))
			rc.rec.DownstreamMS = &ms
		}
	}()
	estimate := stream.TokenEstimate{}
	if rc != nil && rc.rec.TokenEstimate != nil {
		u := rc.rec.TokenEstimate
		estimate = stream.TokenEstimate{InputTokens: u.EstimatedTotalInputTokens, TotalReserved: u.TotalReservedTokens, OutputCapTokens: u.RequestedOutputCapTokens, ToolSchemaTokens: u.EstimatedToolSchemaTokens, ImageTokens: u.EstimatedImageTokens}
	}
	events, err := t.Begin(estimate)
	if err != nil {
		return result, err
	}
	if err = emit(events); err != nil {
		return result, err
	}
	for {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		var up stream.Event
		up, err = f.Next()
		if err != nil {
			if errors.Is(err, stream.ErrLimit) {
				return result, upstreamError{Class: "upstream_response_too_large", Message: "upstream response exceeded configured size limit"}
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				class := "empty_stream"
				if result.Committed {
					class = "stream_interrupted"
				}
				return result, upstreamError{Class: class, Message: "upstream stream ended before terminal event", Retryable: true}
			}
			return result, err
		}
		events, err = t.Next(up)
		if accounting, ok := t.(interface{ UsageSnapshot() stream.Usage }); ok {
			u := accounting.UsageSnapshot()
			mergeStreamUsage(&result.Response.Usage, Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens, ReasoningTokens: u.ReasoningTokens, CachedInputTokens: u.CachedInputTokens})
		}
		if err != nil && !errors.Is(err, stream.ErrTerminal) {
			return result, upstreamError{Class: "stream_translator_error", Message: "invalid upstream stream event", Err: err}
		}
		if writeErr := emit(events); writeErr != nil {
			return result, writeErr
		}
		if errors.Is(err, stream.ErrTerminal) {
			result.Done = true
			result.Terminal = true
			reason = stream.StopComplete
			for _, e := range events {
				if e.Type == "error" || e.Type == "response.failed" {
					reason = stream.StopError
				}
			}
			return result, nil
		}
	}
}

func proxyChatUpstreamToResponsesCallerSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, dialect, model string, maxBytes int64, rc *requestContext, transforms ...IdentifierTransform) (nativeStreamResult, error) {
	t := &stream.ChatUpstreamToResponsesCaller{ResponseID: "resp_" + requestID(), Model: model, CreatedAt: time.Now().Unix()}
	if len(transforms) > 0 {
		t.IDs = transforms[0]
	}
	return runResponsesStream(ctx, w, body, model, maxBytes, rc, t)
}

func proxyResponsesUpstreamToChatCallerSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, dialect, model string, maxBytes int64, rc *requestContext, transforms ...IdentifierTransform) (nativeStreamResult, error) {
	t := &stream.ResponsesUpstreamToChatCaller{Model: model}
	if len(transforms) > 0 {
		t.IDs = transforms[0]
	}
	result, err := runResponsesStream(ctx, w, body, model, maxBytes, rc, t)
	result.Response.ID = t.ResponseID
	return result, err
}
