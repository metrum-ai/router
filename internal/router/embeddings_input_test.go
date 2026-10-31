// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"testing"
)

func TestEmbeddingInputShapes(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		count       int
		param       string
	}{
		{"string", `{"model":"m","input":"abcde"}`, 1, ""},
		{"strings", `{"input":["first","second"],"encoding_format":"base64","dimensions":512}`, 2, ""},
		{"tokens", `{"input":[0,1,2]}`, 1, ""},
		{"token batches", `{"input":[[0,1],[2,3]]}`, 2, ""},
		{"empty", `{"input":[]}`, 0, "input"},
		{"empty text", `{"input":""}`, 0, "input"},
		{"empty tokens", `{"input":[[]]}`, 0, "input"},
		{"mixed strings", `{"input":["x",4]}`, 0, "input"},
		{"mixed tokens", `{"input":[3,"x"]}`, 0, "input"},
		{"fractional token", `{"input":[1.5]}`, 0, "input"},
		{"negative token", `{"input":[-1]}`, 0, "input"},
		{"huge token", `{"input":[1e30]}`, 0, "input"},
		{"zero dimensions", `{"input":"x","dimensions":0}`, 0, "dimensions"},
		{"fractional dimensions", `{"input":"x","dimensions":5.5}`, 0, "dimensions"},
		{"bad encoding", `{"input":"x","encoding_format":"raw"}`, 0, "encoding_format"},
		{"bad model", `{"input":"x","model":null}`, 0, "model"},
		{"not object", `[]`, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, param, err := decodeEmbeddingInput([]byte(tc.input))
			if tc.count > 0 {
				if err != nil || len(req.Items) != tc.count {
					t.Fatalf("items=%v param=%s err=%v", req.Items, param, err)
				}
				return
			}
			if err == nil || param != tc.param {
				t.Fatalf("param=%s err=%v", param, err)
			}
		})
	}
}

func TestEmbeddingInputBatchLimit(t *testing.T) {
	items := make([]string, maxEmbeddingInputs)
	for i := range items {
		items[i] = "x"
	}
	body, _ := json.Marshal(map[string]any{"input": items})
	if _, _, err := decodeEmbeddingInput(body); err != nil {
		t.Fatal(err)
	}
	items = append(items, "x")
	body, _ = json.Marshal(map[string]any{"input": items})
	if _, param, err := decodeEmbeddingInput(body); err == nil || param != "input" {
		t.Fatalf("param=%s err=%v", param, err)
	}
}
