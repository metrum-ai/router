// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestOpenAIJSONSchemaMapsToAnthropic(t *testing.T) {
	for _, tc := range []struct{ dialect, body string }{
		{"openai-chat", `{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","strict":true,"schema":{"type":"object","additionalProperties":false}}}}`},
		{"openai-responses", `{"input":"hi","text":{"format":{"type":"json_schema","name":"x","strict":true,"schema":{"type":"object","additionalProperties":false}}}}`},
	} {
		req, err := decodeRequest(tc.dialect, []byte(tc.body), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := encodeUpstreamForTarget("anthropic", "m", req, Target{StructuredOutput: true})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		config, _ := body["output_config"].(map[string]any)
		format, _ := config["format"].(map[string]any)
		schema, _ := format["schema"].(map[string]any)
		if format["type"] != "json_schema" || schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s schema lost: %s", tc.dialect, b)
		}
	}
}

func TestAnthropicTargetRejectsUnsupportedOpenAIShapes(t *testing.T) {
	for _, tc := range []struct{ body, reason string }{
		{`{"messages":[{"role":"user","content":"hi"}],"logprobs":true}`, "logprobs-unsupported"},
		{`{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"},"tools":[{"type":"function","function":{"name":"x","parameters":{"type":"object"}}}]}`, "structured-output-unsupported"},
		{`{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}},"tools":[{"type":"function","function":{"name":"x","parameters":{"type":"object"}}}]}`, "structured-output-unsupported"},
	} {
		req, err := decodeRequest("openai-chat", []byte(tc.body), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		got := requestShapeFilterReason(Target{}, req, "openai-chat", "anthropic", requestTokenEstimateLogRecord{}, targetRequestShapeFit{})
		if got != tc.reason {
			t.Fatalf("body %s: got %q want %q", tc.body, got, tc.reason)
		}
	}
}
