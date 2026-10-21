// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzIRRoundTrip exercises the canonical text Chat subset. Arbitrary Chat
// objects are not lossless through the translation IR (see docs/ops-fuzz.md).
func FuzzIRRoundTrip(f *testing.F) {
	f.Add("hello")
	f.Fuzz(func(t *testing.T, content string) {
		// JSON replaces invalid UTF-8; compare decoded wire objects, not Go strings.
		body, _ := json.Marshal(map[string]any{"model": "test", "stream": false, "messages": []map[string]any{{"role": "user", "content": content}}})
		req, err := decodeRequest("openai-chat", body, http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := encodeUpstream("openai-chat", "test", req)
		if err != nil {
			t.Fatal(err)
		}
		var want, got any
		if err := json.Unmarshal(body, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("Chat IR non-identity: %s -> %s", body, encoded)
		}
	})
}

// TestIRRoundTripReportedNonIdentity keeps the reported arbitrary-Chat
// limitation reproducible without blocking the canonical-subset fuzz campaign.
func TestIRRoundTripReportedNonIdentity(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"developer","content":"x"}],"stream":true}`)
	req, err := decodeRequest("openai-chat", body, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeUpstream("openai-chat", "test", req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reported non-identity (docs/ops-fuzz.md): %s -> %s", body, encoded)
}

func FuzzConfigLoader(f *testing.F) {
	f.Add([]byte("server:\n  listen: 127.0.0.1:18123\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		// Parse, expansion, defaults, validation and redaction must never panic.
		_, _ = LoadConfig(path)
	})
}
