// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #759 (review #760): the server's absolute WriteTimeout used to cut long
// streamed answers. These tests run a real net/http server with real deadlines
// (not a recorder and not a mocked clock): a stream that keeps producing data
// must outlive the server write timeout, the upstream idle window must start at
// the first data byte, a stall after the first chunk must be ended with a clear
// record, and unary responses must keep their absolute bound.

// startRouterServer serves the router handler through a real http.Server with
// the given write deadline, so net/http's write-deadline machinery is exercised.
func startRouterServer(t *testing.T, svc *Service, writeTimeout time.Duration) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return "http://" + listener.Addr().String()
}

func streamingServerService(t *testing.T, upstream http.Handler, streamIdleMS int) *Service {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
	cfg.Server.Streaming.Translator = "incremental"
	cfg.Server.StreamIdleTimeoutMS = streamIdleMS
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func postStream(t *testing.T, baseURL string, timeout time.Duration) (int, string, error, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	body := `{"model":"default","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err, time.Since(started)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), readErr, time.Since(started)
}

// TestLongStreamedResponseSurvivesAbsoluteWriteTimeout is the #759 failure: the
// server WriteTimeout fires while tokens are still flowing.
func TestLongStreamedResponseSurvivesAbsoluteWriteTimeout(t *testing.T) {
	var chunks atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl_stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chunk-%d \"},\"finish_reason\":null}]}\n\n", i)
			chunks.Add(1)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	// The progress deadline is far away; only the absolute server write
	// deadline (500ms) could cut this ~1.2s stream.
	svc := streamingServerService(t, upstream, 5000)
	baseURL := startRouterServer(t, svc, 500*time.Millisecond)

	status, body, err, elapsed := postStream(t, baseURL, 10*time.Second)
	if err != nil {
		t.Fatalf("a stream that keeps producing data must not be cut by the absolute write timeout: %v after %s (chunks sent=%d)", err, elapsed, chunks.Load())
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%q", status, body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream ended without [DONE] after %s (chunks sent=%d): %q", elapsed, chunks.Load(), body)
	}
	if !strings.Contains(body, "chunk-5") {
		t.Fatalf("last chunk missing after %s: %q", elapsed, body)
	}
}

// TestSlowFirstTokenSurvivesStreamIdleWindow is review item #760-1: an upstream
// can send response headers and then think for a long time (high reasoning
// effort, long prefill on an edge box). The idle window must start at the first
// data byte, not at the headers; time to the first byte stays bounded by the
// attempt/upstream timeout.
func TestSlowFirstTokenSurvivesStreamIdleWindow(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Headers only: the caller sees the response start, then no data.
		w.WriteHeader(http.StatusOK)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(700 * time.Millisecond)
		for i := 0; i < 4; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl_slow\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"late-%d \"},\"finish_reason\":null}]}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(150 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	// The first data byte arrives ~700ms after the headers, well past the 300ms
	// window; that window must not start before the first token arrives.
	svc := streamingServerService(t, upstream, 300)
	baseURL := startRouterServer(t, svc, 30*time.Second)

	status, body, err, elapsed := postStream(t, baseURL, 10*time.Second)
	if err != nil {
		t.Fatalf("a slow first token must be bounded by the attempt/upstream timeout, not by the stream idle window: %v after %s", err, elapsed)
	}
	if status != http.StatusOK || !strings.Contains(body, "[DONE]") || !strings.Contains(body, "late-3") {
		t.Fatalf("slow-first-token stream incomplete after %s: status=%d body=%q", elapsed, status, body)
	}
}

// TestStalledStreamEndsAtProgressDeadline: a stall after the first chunk ends at
// the progress deadline, and the stored record names a clear timeout class
// (review item #760-4).
func TestStalledStreamEndsAtProgressDeadline(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl_stall\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first \"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(2500 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	dir := t.TempDir()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	cfg := testConfig(t, up.URL, "provider-key", dir)
	cfg.Server.Streaming.Translator = "incremental"
	cfg.Server.StreamIdleTimeoutMS = 300
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	// The absolute deadline is generous; only the 300ms progress deadline can
	// end this stream.
	baseURL := startRouterServer(t, svc, 30*time.Second)

	_, body, err, elapsed := postStream(t, baseURL, 5*time.Second)
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("a stalled stream must be ended by the progress deadline, got the whole stream after %s: %q", elapsed, body)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("stalled stream lasted %s, want it ended near the 300ms progress deadline (err=%v)", elapsed, err)
	}
	flushUsageForTest(t, svc)

	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows []usageRecord
	if err := store.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage rows=%d, want 1", len(rows))
	}
	if rows[0].Error != "upstream_timeout" {
		t.Fatalf("usage row error=%q, want upstream_timeout", rows[0].Error)
	}
	var attempts []requestAttemptRecord
	if err := store.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempt rows=%d, want 1", len(attempts))
	}
	if attempts[0].ErrorClass != "upstream_timeout" || !attempts[0].TimedOut {
		t.Fatalf("attempt error_class=%q timed_out=%t, want an upstream_timeout attempt", attempts[0].ErrorClass, attempts[0].TimedOut)
	}
	// The stored message keeps the router-owned cause prefix; provider body text
	// is never persisted.
	if !strings.Contains(attempts[0].ErrorMessage, "stream idle timeout") {
		t.Fatalf("attempt message=%q, want the stream idle cause named", attempts[0].ErrorMessage)
	}
}

// TestUnaryResponseUnchangedByStreamDeadline: the progress deadline applies to
// event streams only; a unary response keeps the absolute server write bound.
func TestUnaryResponseUnchangedByStreamDeadline(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(900 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_slow","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
	// A 300ms stream progress deadline must not rescue a unary response.
	svc := streamingServerService(t, upstream, 300)
	baseURL := startRouterServer(t, svc, 400*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		raw, readErr := io.ReadAll(resp.Body)
		if readErr == nil && strings.Contains(string(raw), "chatcmpl_slow") {
			t.Fatalf("a unary response over the absolute write timeout must stay bounded, got %q", string(raw))
		}
	}
}

// TestStreamProgressDeadlineKeepsResponseWriterInterfaces is review item #760-5:
// the streaming wrapper must not hide the interfaces the streaming paths assert.
// Every live assertion in the router is `http.Flusher`; the wrapper provides it
// and also unwraps for http.ResponseController.
func TestStreamProgressDeadlineKeepsResponseWriterInterfaces(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapped := &streamProgressDeadline{ResponseWriter: underlying, idle: 50 * time.Millisecond}
	flusher, ok := any(wrapped).(http.Flusher)
	if !ok {
		t.Fatal("streamProgressDeadline must keep the http.Flusher assertion working")
	}
	wrapped.Header().Set("Content-Type", "text/event-stream")
	wrapped.WriteHeader(http.StatusOK)
	if _, err := wrapped.Write([]byte("data: one\n\n")); err != nil {
		t.Fatal(err)
	}
	flusher.Flush()
	if got := underlying.Body.String(); !strings.Contains(got, "data: one") {
		t.Fatalf("flush did not reach the underlying writer: %q", got)
	}
	// ResponseController reaches the underlying writer through Unwrap, so a
	// handler that flushes through the controller (responses_stream.go) works.
	if err := http.NewResponseController(wrapped).Flush(); err != nil {
		t.Fatalf("ResponseController.Flush through the wrapper: %v", err)
	}
	if unwrapped, ok := any(wrapped).(interface{ Unwrap() http.ResponseWriter }); !ok || unwrapped.Unwrap() != underlying {
		t.Fatal("streamProgressDeadline must unwrap to the underlying writer")
	}
}

// TestNativeStreamThroughProgressDeadline proves the real native (same-dialect)
// SSE path streams through a real server and the wrapper: every chunk arrives and
// the usage row records the native stream mode.
func TestNativeStreamThroughProgressDeadline(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("upstream was not asked to stream: %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl_native\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"native-%d \"},\"finish_reason\":null}]}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	dir := t.TempDir()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	cfg := testConfig(t, up.URL, "provider-key", dir)
	cfg.Server.Streaming.Translator = "incremental"
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	baseURL := startRouterServer(t, svc, 5*time.Second)

	status, body, err, _ := postStream(t, baseURL, 10*time.Second)
	if err != nil || status != http.StatusOK {
		t.Fatalf("native stream status=%d err=%v", status, err)
	}
	for _, want := range []string{"native-0", "native-2", "[DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("native stream missing %q: %q", want, body)
		}
	}
	flushUsageForTest(t, svc)
	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows []usageRecord
	if err := store.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != http.StatusOK || rows[0].Error != "" {
		t.Fatalf("usage rows=%+v, want one successful row for the native stream", rows)
	}
}
