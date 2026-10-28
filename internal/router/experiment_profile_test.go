// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func experimentProfileBool(v bool) *bool { return &v }

func TestValidateExperimentProfiles(t *testing.T) {
	scoped := ExperimentProfileConfig{
		Enabled: experimentProfileBool(true), Callers: []string{"alice"},
		TTFTDelayMS: 100,
	}
	if err := validateExperimentProfiles(map[string]ExperimentProfileConfig{"ttft-100": scoped}); err != nil {
		t.Fatalf("scoped profile must validate: %v", err)
	}

	unscoped := scoped
	unscoped.Callers = nil
	if err := validateExperimentProfiles(map[string]ExperimentProfileConfig{"global": unscoped}); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("unscoped enabled profile must be rejected, got %v", err)
	}

	bothRates := scoped
	bothRates.OutputTokensPerSec = 10
	bothRates.EventsPerSec = 5
	if err := validateExperimentProfiles(map[string]ExperimentProfileConfig{"both": bothRates}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("two pacing rates must be rejected, got %v", err)
	}

	// A disabled profile may be incomplete or unscoped: it never runs.
	disabled := ExperimentProfileConfig{Enabled: experimentProfileBool(false)}
	if err := validateExperimentProfiles(map[string]ExperimentProfileConfig{"off": disabled}); err != nil {
		t.Fatalf("disabled profile must validate regardless of scope: %v", err)
	}

	empty := scoped
	empty.TTFTDelayMS = 0
	if err := validateExperimentProfiles(map[string]ExperimentProfileConfig{"noop": empty}); err == nil {
		t.Fatal("enabled profile with no delay and no pacing must be rejected")
	}
}

func TestResolveExperimentProfileScoping(t *testing.T) {
	profiles := map[string]ExperimentProfileConfig{
		"by-caller": {Enabled: experimentProfileBool(true), Callers: []string{"alice"}, TTFTDelayMS: 50},
		"by-group":  {Enabled: experimentProfileBool(true), Groups: []string{"bench"}, TTFTDelayMS: 25},
		"by-target": {Enabled: experimentProfileBool(true), Targets: []string{"mock/model-a"}, TTFTDelayMS: 75},
		"disabled":  {Enabled: experimentProfileBool(false), Callers: []string{"alice"}, TTFTDelayMS: 999},
	}
	if _, name, ok := resolveExperimentProfile(profiles, "alice", "other", "mock/x"); !ok || name != "by-caller" {
		t.Fatalf("caller scope match = %q %v", name, ok)
	}
	if _, name, ok := resolveExperimentProfile(profiles, "bob", "bench", "mock/x"); !ok || name != "by-group" {
		t.Fatalf("group scope match = %q %v", name, ok)
	}
	if _, name, ok := resolveExperimentProfile(profiles, "bob", "other", "mock/model-a"); !ok || name != "by-target" {
		t.Fatalf("target scope match = %q %v", name, ok)
	}
	if _, _, ok := resolveExperimentProfile(profiles, "bob", "other", "mock/x"); ok {
		t.Fatal("non-matching request must not resolve a profile")
	}

	// Multiple scope lists must all match.
	narrow := map[string]ExperimentProfileConfig{
		"both": {Enabled: experimentProfileBool(true), Callers: []string{"alice"}, Groups: []string{"bench"}, TTFTDelayMS: 10},
	}
	if _, _, ok := resolveExperimentProfile(narrow, "alice", "other", "mock/x"); ok {
		t.Fatal("caller-only match must not satisfy a caller+group scope")
	}

	// The profile hash is stable across calls and changes with the profile.
	h1 := experimentProfileHash(profiles["by-caller"])
	h2 := experimentProfileHash(profiles["by-caller"])
	if h1 == "" || h1 != h2 {
		t.Fatalf("profile hash not stable: %q vs %q", h1, h2)
	}
	if h1 == experimentProfileHash(profiles["by-group"]) {
		t.Fatal("different profiles must hash differently")
	}
}

func TestExperimentPacerSeededJitterDeterministic(t *testing.T) {
	profile := ExperimentProfileConfig{
		Enabled: experimentProfileBool(true), Callers: []string{"alice"},
		TTFTDelayMS: 100, TTFTJitterMS: 20, Seed: "study-a",
	}
	a := newExperimentPacer(profile, "p", "req-1")
	b := newExperimentPacer(profile, "p", "req-1")
	if a.ttftMS != b.ttftMS {
		t.Fatalf("same seed and request must give the same TTFT: %v vs %v", a.ttftMS, b.ttftMS)
	}
	if a.ttftMS < 80 || a.ttftMS > 120 {
		t.Fatalf("TTFT outside jitter band: %v", a.ttftMS)
	}
	c := newExperimentPacer(profile, "p", "req-2")
	_ = c // different request IDs may differ; determinism is the contract
	d := newExperimentPacer(profile, "p", "req-2")
	if c.ttftMS != d.ttftMS {
		t.Fatal("same seed and different request ID must still be deterministic per request")
	}
}

func TestExperimentPacerNeverAcceleratesAndCapsDelay(t *testing.T) {
	ctx := context.Background()
	profile := ExperimentProfileConfig{
		Enabled: experimentProfileBool(true), Callers: []string{"alice"},
		EventsPerSec: 1000, Burst: 1, MaxExtraDelayMS: 5,
	}
	p := newExperimentPacer(profile, "p", "req-x")
	// Frames that arrive slowly (already late) get no delay.
	p.nextRelease = time.Now().Add(-time.Second)
	if got := p.paceFrame(ctx, 1); got != 0 {
		t.Fatalf("late frame must not be delayed, got %v ms", got)
	}
	// A burst of fast frames is capped by max_extra_delay_ms.
	p2 := newExperimentPacer(profile, "p", "req-y")
	total := 0.0
	for i := 0; i < 100; i++ {
		total += p2.paceFrame(ctx, 1)
	}
	if total > 5.5 {
		t.Fatalf("injected delay exceeded the budget: %.2f ms", total)
	}
}

const experimentMockSSEPrefix = "data: {\"id\":\"chatcmpl-exp\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"

func experimentMockChunk(content string) string {
	return fmt.Sprintf("data: {\"id\":\"chatcmpl-exp\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", content)
}

const experimentMockSSESuffix = "data: {\"id\":\"chatcmpl-exp\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":12,\"total_tokens\":17}}\n\ndata: [DONE]\n\n"

func TestExperimentProfileStreamTTFTAndPacing(t *testing.T) {
	toolFrame := "data: {\"id\":\"chatcmpl-exp\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"run\",\"arguments\":\"{}\"}}]}}]}\n\n"
	reasoningFrame := "data: {\"id\":\"chatcmpl-exp\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n"
	frames := []string{experimentMockSSEPrefix, toolFrame, reasoningFrame}
	for i := 0; i < 6; i++ {
		frames = append(frames, experimentMockChunk(fmt.Sprintf("tok%d ", i)))
	}
	frames = append(frames, experimentMockSSESuffix)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprint(w, f)
			w.(http.Flusher).Flush()
		}
	}))
	defer up.Close()

	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{{Provider: "mock", Model: "mock-model"}}, func(cfg *Config) {
		cfg.Server.ExperimentProfiles = map[string]ExperimentProfileConfig{
			"study-ttft": {
				Enabled: experimentProfileBool(true), Callers: []string{"alice"}, Seed: "study-a",
				TTFTDelayMS: 150, EventsPerSec: 30, Burst: 1, MaxExtraDelayMS: 5000,
			},
		}
	})
	server := httptest.NewServer(svc.Handler())
	defer server.Close()

	start := time.Now()
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := io.Reader(resp.Body)
	var firstChunkAt time.Duration
	var raw strings.Builder
	tee := io.TeeReader(reader, &raw)
	buf := make([]byte, 4096)
	first := true
	for {
		n, err := tee.Read(buf)
		if first && n > 0 {
			firstChunkAt = time.Since(start)
			first = false
		}
		if err != nil {
			break
		}
	}
	total := time.Since(start)

	// TTFT: the first client byte arrives only after the injected delay.
	// Tolerance: measurement starts at request send; documented tolerance is
	// -10% on the injected delay (docs-site experiment-profiles page).
	if firstChunkAt < 130*time.Millisecond {
		t.Fatalf("TTFT %.0fms below the injected 150ms delay band", float64(firstChunkAt.Milliseconds()))
	}
	// Pacing: 7 frames at 30 events/s with burst 1 => at least ~6/30s of
	// release spacing after the TTFT delay (tolerance -20%).
	if total < 330*time.Millisecond {
		t.Fatalf("stream finished in %.0fms; pacing at 30 events/s should take longer", float64(total.Milliseconds()))
	}
	rec := lastJSONLRecord(t, svc.cfg.Server.Logging.Path)
	upstreamFirst := findTrace(t, rec.TraceEvents, "upstream_first_byte")
	downstreamFirst := findTrace(t, rec.TraceEvents, "downstream_first_frame")
	proxyWall := findTrace(t, rec.TraceEvents, "paced_upstream_proxy_wall")
	_ = findTrace(t, rec.TraceEvents, "experiment_profile_pacing")
	if rec.TTFBMS == nil || *rec.TTFBMS != downstreamFirst.DurationMS {
		t.Fatalf("TTFB = %v, downstream first frame = %dms", rec.TTFBMS, downstreamFirst.DurationMS)
	}
	if downstreamFirst.DurationMS-upstreamFirst.DurationMS < 130 {
		t.Fatalf("upstream first byte=%dms, downstream first frame=%dms; missing TTFT delay", upstreamFirst.DurationMS, downstreamFirst.DurationMS)
	}
	if diff := firstChunkAt.Milliseconds() - *rec.TTFBMS; diff < -50 || diff > 250 {
		t.Fatalf("client first chunk=%dms, router first frame=%dms", firstChunkAt.Milliseconds(), *rec.TTFBMS)
	}
	if rec.UpstreamMS != nil || rec.UpstreamOutputTPS != nil || rec.UpstreamTotalTPS != nil {
		t.Fatalf("paced proxy timing must not masquerade as upstream generation timing: %+v", rec)
	}
	if proxyWall.DurationMS < 330 {
		t.Fatalf("paced proxy wall time = %dms", proxyWall.DurationMS)
	}
	// Protocol fidelity: frames pass through unchanged (tool/reasoning shape
	// is a passthrough here: every data line must appear verbatim upstream).
	for _, f := range frames {
		for _, line := range strings.Split(f, "\n") {
			if line != "" && !strings.Contains(raw.String(), line) {
				t.Fatalf("client stream is missing the verbatim frame line %q", line)
			}
		}
	}
}

func TestExperimentProfileResponsesStreamTiming(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responseDelta+responseEnd)
		w.(http.Flusher).Flush()
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{{Provider: "native", Model: "mock-model"}}, func(cfg *Config) {
		cfg.Server.ExperimentProfiles = map[string]ExperimentProfileConfig{
			"study": {Enabled: experimentProfileBool(true), Callers: []string{"alice"}, TTFTDelayMS: 150, EventsPerSec: 30, MaxExtraDelayMS: 5000},
		}
	})
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	start := time.Now()
	req, _ := http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	first := time.Since(start)
	if n == 0 || err != nil {
		t.Fatalf("first read n=%d err=%v", n, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	rec := lastJSONLRecord(t, svc.cfg.Server.Logging.Path)
	upstream := findTrace(t, rec.TraceEvents, "upstream_first_byte")
	downstream := findTrace(t, rec.TraceEvents, "downstream_first_frame")
	if rec.TTFBMS == nil || *rec.TTFBMS != downstream.DurationMS || downstream.DurationMS-upstream.DurationMS < 130 || first < 130*time.Millisecond {
		t.Fatalf("client=%v ttfb=%v upstream=%d downstream=%d", first, rec.TTFBMS, upstream.DurationMS, downstream.DurationMS)
	}
	if rec.UpstreamMS != nil || rec.UpstreamOutputTPS != nil {
		t.Fatal("paced Responses proxy reported an independent upstream rate")
	}
	_ = findTrace(t, rec.TraceEvents, "paced_upstream_proxy_wall")
}

func TestExperimentProfileDoesNotAffectOtherCallers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, experimentMockSSEPrefix+experimentMockChunk("hi")+experimentMockSSESuffix)
		w.(http.Flusher).Flush()
	}))
	defer up.Close()

	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{{Provider: "mock", Model: "mock-model"}}, func(cfg *Config) {
		cfg.Server.ExperimentProfiles = map[string]ExperimentProfileConfig{
			"study-ttft": {
				Enabled: experimentProfileBool(true), Callers: []string{"someone-else"},
				TTFTDelayMS: 800, MaxExtraDelayMS: 5000,
			},
		}
	})
	server := httptest.NewServer(svc.Handler())
	defer server.Close()

	start := time.Now()
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("non-matching caller was paced (%v); profiles must not shape other traffic", took)
	}
	rec := lastJSONLRecord(t, svc.cfg.Server.Logging.Path)
	if rec.UpstreamMS == nil || rec.TTFBMS == nil {
		t.Fatalf("unpaced timing must remain available: %+v", rec)
	}
	_ = findTrace(t, rec.TraceEvents, "upstream_first_byte")
	_ = findTrace(t, rec.TraceEvents, "downstream_first_frame")
}

func TestExperimentProfileCancelDuringPacing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, experimentMockSSEPrefix+experimentMockChunk("first "))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the stream open until the caller goes away
	}))
	defer up.Close()

	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{{Provider: "mock", Model: "mock-model"}}, func(cfg *Config) {
		cfg.Server.ExperimentProfiles = map[string]ExperimentProfileConfig{
			"study-pace": {
				Enabled: experimentProfileBool(true), Callers: []string{"alice"},
				EventsPerSec: 0.2, Burst: 1, MaxExtraDelayMS: 60000, // 5s per frame after burst
			},
		}
	})
	server := httptest.NewServer(svc.Handler())
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 4096)
	_, _ = resp.Body.Read(first) // first burst frame

	cancel()
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Body)
		readErr <- err
	}()
	select {
	case <-readErr:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not end a paced stream within 3s")
	}
	resp.Body.Close()
}

func TestExperimentProfileNonStreamingHold(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":60,"total_tokens":65}}`)
	}))
	defer up.Close()

	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{{Provider: "mock", Model: "mock-model"}}, func(cfg *Config) {
		cfg.Server.ExperimentProfiles = map[string]ExperimentProfileConfig{
			"study-rate": {
				Enabled: experimentProfileBool(true), Callers: []string{"alice"},
				TTFTDelayMS: 50, OutputTokensPerSec: 300, MaxExtraDelayMS: 5000,
			},
		}
	})
	server := httptest.NewServer(svc.Handler())
	defer server.Close()

	start := time.Now()
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// 50ms TTFT + 60 tokens at 300/s (200ms) = 250ms target; tolerance -20%.
	if took := time.Since(start); took < 200*time.Millisecond {
		t.Fatalf("non-streaming hold too short: %v (expected about 250ms)", took)
	}
	if !strings.Contains(string(body), "done") {
		t.Fatalf("response body changed by pacing: %s", body)
	}
}
