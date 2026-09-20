// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

// Admission remains in the existing request-shape, tools and reasoning gates.
func anthropicStreamBridge(caller, upstream string) bool {
	caller, upstream = normalizeDialect(caller), normalizeDialect(upstream)
	return caller == "anthropic" && (upstream == "openai-chat" || upstream == "openai-responses") || upstream == "anthropic" && (caller == "openai-chat" || caller == "openai-responses")
}
func anthropicBridgeProxy(caller string) func(context.Context, http.ResponseWriter, io.Reader, string, string, int64, *requestContext, ...IdentifierTransform) (nativeStreamResult, error) {
	return func(ctx context.Context, w http.ResponseWriter, body io.Reader, upstream, model string, maxBytes int64, rc *requestContext, ids ...IdentifierTransform) (nativeStreamResult, error) {
		var encoder stream.IdentifierEncoder
		if len(ids) > 0 {
			encoder = ids[0]
		}
		var t stream.StreamTranslator
		switch normalizeDialect(caller) {
		case "anthropic":
			base := stream.ChatUpstreamToAnthropicCaller{IDs: encoder, ResponseID: "msg_" + requestID(), Model: model}
			if normalizeDialect(upstream) == "openai-responses" {
				t = &stream.ResponsesUpstreamToAnthropicCaller{ChatUpstreamToAnthropicCaller: base}
			} else {
				t = &base
			}
		case "openai-chat":
			t = &stream.AnthropicUpstreamToChatCaller{IDs: encoder, Model: model, CreatedAt: time.Now().Unix()}
		case "openai-responses":
			t = &stream.AnthropicUpstreamToResponsesCaller{ChatUpstreamToResponsesCaller: stream.ChatUpstreamToResponsesCaller{IDs: encoder, ResponseID: "resp_" + requestID(), Model: model, CreatedAt: time.Now().Unix()}}
		}
		return runResponsesStream(ctx, w, body, model, maxBytes, rc, t)
	}
}
