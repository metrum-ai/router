// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Behaviour this tree inherits from Go 1.27, pinned so a later toolchain change
// is visible instead of silent. Each case is one of the release-note items that
// changes observable behaviour rather than adding API surface:
//
//   - time: the asynctimerchan GODEBUG was removed, so timer channels are always
//     unbuffered. The polling, notifier and deadline code here only ever uses
//     `select`/`Stop`, never a non-blocking peek at a timer channel.
//   - net/http file content: a Range header with more than 200 ranges is ignored
//     (Go 1.27.2, httpservecontentmaxranges), so an abusive request gets the
//     whole body once instead of a multipart explosion. internal/router/docs.go
//     serves the embedded docs through http.ServeContent.
//   - net/http server: requests carrying more than DefaultMaxHeaderValueCount
//     (500) header values are refused with 431 by the server itself, before any
//     router middleware sees them.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGo127TimerChannelsAreUnbuffered(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	if cap(timer.C) != 0 {
		t.Fatalf("timer channel capacity = %d, want 0 (asynctimerchan semantics are gone)", cap(timer.C))
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	if cap(ticker.C) != 0 {
		t.Fatalf("ticker channel capacity = %d, want 0", cap(ticker.C))
	}
}

func TestGo127ServeContentIgnoresExcessiveRanges(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 40000)
	serve := func(ranges int) *httptest.ResponseRecorder {
		specs := make([]string, 0, ranges)
		for i := 0; i < ranges; i++ {
			specs = append(specs, "0-10")
		}
		request := httptest.NewRequest(http.MethodGet, "/docs", nil)
		request.Header.Set("Range", "bytes="+strings.Join(specs, ","))
		recorder := httptest.NewRecorder()
		http.ServeContent(recorder, request, "docs", time.Time{}, bytes.NewReader(body))
		return recorder
	}
	if recorder := serve(5); recorder.Code != http.StatusPartialContent || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "multipart/byteranges") {
		t.Fatalf("small range set = %d %q, want 206 multipart", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	// Over the limit the header is ignored: one whole-body response, never a
	// per-range multipart answer an attacker could amplify.
	if recorder := serve(201); recorder.Code != http.StatusOK || recorder.Body.Len() != len(body) {
		t.Fatalf("excessive range set = %d len=%d, want 200 with the whole body", recorder.Code, recorder.Body.Len())
	}
}

func TestGo127ListenerRefusesExcessiveHeaderValues(t *testing.T) {
	adminConfig := AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:0"}
	defaultAdminPlaneConfig(&adminConfig)
	mux := newAdminMux()
	mux.HandleFunc("GET /admin/v1/status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := httptest.NewServer(mux.handler(&adminConfig, AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{}, ErrNoCredential })))
	defer server.Close()
	request := func(values int) int {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/admin/v1/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < values; i++ {
			req.Header.Add("X-Probe", "v")
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("values=%d: %v", values, err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if status := request(400); status == http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("a 400-value request was refused with 431")
	}
	if status := request(501); status != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("a 501-value request = %d, want 431 from the server's header-value bound", status)
	}
}
