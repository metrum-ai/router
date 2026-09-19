// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

const responseDelta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_test\",\"delta\":\"café\"}\n\n"
const responseEnd = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n"

func TestResponsesProxyFaults(t *testing.T) {
	for _, tc := range []struct {
		name, raw, class string
		committed        bool
		limit            int64
	}{
		{"empty", "", "empty_stream", false, 0},
		{"unknown-only", "data: {\"type\":\"vendor.future\"}\n\n", "empty_stream", false, 0},
		{"malformed", "data: {\n\n", "stream_translator_error", false, 0},
		{"truncated-json", "data: {", "empty_stream", false, 0},
		{"eof-after-delta", responseDelta, "stream_interrupted", true, 0},
		{"malformed-after-delta", responseDelta + "data: {\n\n", "stream_translator_error", true, 0},
		{"limit", responseDelta, "upstream_response_too_large", false, 10},
		{"success", responseDelta + responseEnd, "", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			rc := &requestContext{start: time.Now()}
			result, err := proxyResponsesSSE(context.Background(), rr, strings.NewReader(tc.raw), "openai-responses", "model", tc.limit, rc, testIdentifierTransform(t))
			if result.Committed != tc.committed {
				t.Fatal(result)
			}
			if tc.class == "" {
				if err != nil {
					t.Fatal(err)
				}
				if result.Response.Text != "" || result.Response.Usage.TotalTokens != 5 {
					t.Fatal(result.Response)
				}
				if strings.Contains(rr.Body.String(), "msg_test") || strings.Contains(rr.Body.String(), "resp_test") {
					t.Fatal("raw identifiers")
				}
			} else {
				var ue upstreamError
				if !errors.As(err, &ue) || ue.Class != tc.class || ue.Committed != tc.committed {
					t.Fatal(err)
				}
			}
			if tc.name == "unknown-only" && (rr.Body.Len() != 0 || rc.rec.StreamUnknownEvents != 1) {
				t.Fatal(rr.Body.String(), rc.rec.StreamUnknownEvents)
			}
		})
	}
}

type lifecycleTranslator struct {
	panicAt  string
	finishes int
}

func (t *lifecycleTranslator) Begin(stream.TokenEstimate) ([]stream.Event, error) {
	if t.panicAt == "begin" {
		panic("test")
	}
	return nil, nil
}
func (t *lifecycleTranslator) Next(stream.Event) ([]stream.Event, error) {
	if t.panicAt == "next" {
		panic("test")
	}
	return []stream.Event{{Data: []byte(`{"type":"response.completed"}`)}}, stream.ErrTerminal
}
func (t *lifecycleTranslator) Finish(stream.StopReason, stream.Usage) ([]stream.Event, error) {
	t.finishes++
	if t.panicAt == "finish" {
		panic("test")
	}
	return nil, nil
}

type brokenWriter struct{ header http.Header }

func (w brokenWriter) Header() http.Header     { return w.header }
func (brokenWriter) WriteHeader(int)           {}
func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestResponsesLifecycle(t *testing.T) {
	for _, at := range []string{"begin", "next", "finish"} {
		t.Run(at, func(t *testing.T) {
			tr := &lifecycleTranslator{panicAt: at}
			_, err := runResponsesStream(context.Background(), httptest.NewRecorder(), strings.NewReader(responseDelta), "m", 0, nil, tr)
			if err == nil || tr.finishes != 1 {
				t.Fatal(err, tr.finishes)
			}
		})
	}
	tr := &lifecycleTranslator{}
	result, err := runResponsesStream(context.Background(), brokenWriter{http.Header{}}, strings.NewReader(responseDelta), "m", 0, nil, tr)
	if err == nil || !result.Committed || tr.finishes != 1 {
		t.Fatal(result, err, tr.finishes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr = &lifecycleTranslator{}
	result, err = runResponsesStream(ctx, httptest.NewRecorder(), strings.NewReader(responseDelta), "m", 0, nil, tr)
	if err == nil || result.Committed || tr.finishes != 1 {
		t.Fatal(result, err, tr.finishes)
	}
}

func TestResponsesFallbackCommit(t *testing.T) {
	for _, prelude := range []string{"", "data: {\"type\":\"vendor.future\"}\n\n", responseDelta} {
		t.Run(fmt.Sprint(len(prelude)), func(t *testing.T) {
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if b["model"] == "primary" {
					if prelude == "" {
						w.WriteHeader(503)
					} else {
						fmt.Fprint(w, prelude)
					}
					return
				}
				fmt.Fprint(w, responseDelta+responseEnd)
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{{Provider: "native", Model: "primary"}, {Provider: "native", Model: "fallback"}})
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"synthetic"}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			svc.Handler().ServeHTTP(rr, req)
			want := int64(2)
			if prelude == responseDelta {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want %d body=%s", calls.Load(), want, rr.Body.String())
			}
		})
	}
}
func TestResponsesCancelAttempt(t *testing.T) {
	canceled := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, responseDelta)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{{Provider: "native", Model: "m"}})
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"synthetic"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not canceled")
	}
}
func TestResponsesSynthesizedOption(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["stream"] == true {
			t.Error("native stream enabled")
		}
		fmt.Fprint(w, `{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"synthetic"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{{Provider: "native", Model: "m"}})
	svc.cfg.Server.Streaming.Translator = "synthesized"
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"synthetic"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	svc.Handler().ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "response.completed") {
		t.Fatal(rr.Body.String())
	}
}
func TestStreamingConfig(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "key", t.TempDir())
	cfg.Server.Streaming.Translator = "bad"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "streaming.translator") {
		t.Fatal(err)
	}
	cfg.Server.Streaming.Translator = ""
	cfg.setDefaults()
	if cfg.Server.Streaming.Translator != "incremental" {
		t.Fatal(cfg.Server.Streaming)
	}
}

// The reader blocks until the first downstream Flush, proving there is no
// response-wide lookahead, even when an unknown event precedes the first delta.
type gatedStreamReader struct {
	first   []byte
	rest    io.Reader
	flushed <-chan struct{}
}

func (r *gatedStreamReader) Read(p []byte) (int, error) {
	if len(r.first) > 0 {
		n := copy(p, r.first)
		r.first = r.first[n:]
		return n, nil
	}
	select {
	case <-r.flushed:
		return r.rest.Read(p)
	case <-time.After(time.Second):
		return 0, errors.New("read ahead before flush")
	}
}

type signalWriter struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    bool
}

func (w *signalWriter) Flush() {
	w.ResponseRecorder.Flush()
	if !w.once {
		w.once = true
		close(w.flushed)
	}
}
func TestResponsesFlushBeforeReadAhead(t *testing.T) {
	w := &signalWriter{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
	r := &gatedStreamReader{first: []byte("data: {\"type\":\"future\"}\n\n" + responseDelta), rest: strings.NewReader(responseEnd), flushed: w.flushed}
	result, err := proxyResponsesSSE(context.Background(), w, r, "openai-responses", "m", 0, nil)
	if err != nil || !result.Done {
		t.Fatal(result, err)
	}
}

type flushErrorWriter struct{ *httptest.ResponseRecorder }

func (w flushErrorWriter) FlushError() error { return io.ErrClosedPipe }
func TestResponsesFlushFailure(t *testing.T) {
	w := flushErrorWriter{httptest.NewRecorder()}
	result, err := proxyResponsesSSE(context.Background(), w, strings.NewReader(responseDelta+responseEnd), "openai-responses", "m", 0, nil)
	var ue upstreamError
	if !errors.As(err, &ue) || ue.Class != "downstream_write_error" || !result.Committed {
		t.Fatal(result, err)
	}
}

func TestResponsesTimeoutAttempt(t *testing.T) {
	canceled := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, responseDelta)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{{Provider: "native", Model: "m"}})
	// A request deadline exercises the same attempt context cancellation used by
	// the configured attempt timeout, including a blocked body read.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"synthetic"}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not cancel upstream")
	}
	if strings.Contains(rr.Body.String(), "response.completed") {
		t.Fatal("fabricated success")
	}
}

func TestResponsesUsageSurvivesWriteFailure(t *testing.T) {
	result, err := proxyResponsesSSE(context.Background(), brokenWriter{http.Header{}}, strings.NewReader(responseEnd), "openai-responses", "m", 0, nil)
	if err == nil || result.Response.Usage.TotalTokens != 5 {
		t.Fatal(result, err)
	}
}
