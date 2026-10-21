// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStreamPolicyE2E uses actual localhost sockets, including a tool-result continuation.
func TestStreamPolicyE2E(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		model := stringValue(b["model"])
		if model == "continuation" {
			t.Error("force_store_false target received a continuation")
		}
		if model == "bridge" {
			if _, ok := b["previous_response_id"]; ok || b["store"] != false {
				t.Error("bridge must be stateless", b)
			}
			writeJSON(w, 200, map[string]any{"id": "resp_bridge", "object": "response", "status": "completed", "model": model, "output_text": "ok"})
			return
		}
		if b["stream"] == true {
			if model == "json" {
				writeJSON(w, 200, map[string]any{"model": model})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if model == "large" {
				fmt.Fprint(w, "data: "+strings.Repeat("x", 65<<10)+"\n\n")
				return
			}
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl_test\",\"model\":\"stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(500 * time.Millisecond)
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		u := &miUpstream{}
		raw, _ := json.Marshal(b)
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		u.ServeHTTP(w, r)
	}))
	defer up.Close()
	cfg := testConfig(t, up.URL, "key", t.TempDir())
	cfg.Server.Responses.ModelIdentity = "requested_group"
	cfg.Provider = map[string]ProviderConfig{"chat": {BaseURL: up.URL + "/v1", Dialect: "openai-chat"}, "responses": {BaseURL: up.URL + "/v1", Dialect: "openai-responses"}}
	cfg.Models = map[string]ModelGroup{}
	for _, name := range []string{"stream", "json", "large", "tools", "continuation", "bridge"} {
		target := Target{Provider: "chat", Model: name, ToolSupport: miToolSupport}
		if name == "continuation" {
			target.Provider = "responses"
			target.ForceStoreFalse = true
		}
		if name == "bridge" {
			target.Provider = "responses"
			target.Bridges.ChatToResponses.Enabled = true
		}
		cfg.Models[name] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	}
	cfg.Models["mixed"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "responses", Model: "continuation", ForceStoreFalse: true}, {Provider: "responses", Model: "native"}}}
	cfg.Server.DefaultModelGroup = "stream"
	cfg.Callers[0].Allow = []string{"stream", "json", "large", "tools", "continuation", "bridge", "mixed"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	var listener net.Listener
	for port := 18174; port < 18900; port++ {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(svc.Handler())
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	post := func(path, body string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(raw)
	}
	for _, tc := range []struct {
		model, path, body, code string
		status                  int
	}{
		{"continuation", "/v1/responses", `"input":"hi","previous_response_id":"resp_prior"`, "continuation_unsupported", 400},
		{"json", "/v1/chat/completions", `"messages":[{"role":"user","content":"hi"}],"stream":true`, "upstream_protocol_error", 502},
		{"large", "/v1/chat/completions", `"messages":[{"role":"user","content":"hi"}],"stream":true`, "upstream_frame_too_large", 502},
	} {
		status, _, body := post(tc.path, `{"model":"`+tc.model+`",`+tc.body+`}`)
		var envelope struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(body), &envelope)
		if status != tc.status || envelope.Error.Type != tc.code {
			t.Fatalf("%s: %d %s", tc.model, status, body)
		}
		t.Logf("%s: %d %s", tc.model, status, tc.code)
	}
	status, _, body := post("/v1/responses", `{"model":"mixed","input":"hi","previous_response_id":"resp_prior"}`)
	if status != 200 {
		t.Fatalf("mixed continuation: %d %s", status, body)
	}
	status, h, body := post("/v1/chat/completions", `{"model":"stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 || h.Get("X-Accel-Buffering") != "no" || h.Get("Cache-Control") != "no-cache" || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream: %d %v %s", status, h, body)
	}
	status, _, body = post("/v1/chat/completions", `{"model":"bridge","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 {
		t.Fatalf("bridge: %d %s", status, body)
	}
	status, _, body = post("/v1/chat/completions", `{"model":"tools","messages":[{"role":"user","content":"write proof"}],"tools":[{"type":"function","function":{"name":"write","parameters":{"type":"object"}}}]}`)
	if status != 200 || !strings.Contains(body, "call_keep") {
		t.Fatalf("tool call: %d %s", status, body)
	}
	proof := filepath.Join(t.TempDir(), "proof.txt")
	if err := os.WriteFile(proof, []byte("tool executed"), 0600); err != nil {
		t.Fatal(err)
	}
	status, _, body = post("/v1/chat/completions", `{"model":"tools","messages":[{"role":"user","content":"write proof"},{"role":"assistant","tool_calls":[{"id":"call_keep","type":"function","function":{"name":"write","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_keep","content":"file created"}]}`)
	if status != 200 {
		t.Fatalf("tool continuation: %d %s", status, body)
	}
	t.Log("localhost tool invocation, file creation and tool-result continuation passed")
	if os.Getenv("ROUTER_NGINX_E2E") != "1" {
		return
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker unavailable: optional nginx h2 ingress")
	}
	dir, err := os.MkdirTemp("/workspace", "router-nginx-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	run := func(args ...string) string {
		t.Helper()
		out, e := exec.Command(args[0], args[1:]...).CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v %s", args[0], e, out)
		}
		return string(out)
	}
	run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", dir+"/key.pem", "-out", dir+"/cert.pem", "-subj", "/CN=localhost", "-days", "1")
	conf := `events {} http { server { listen 18774 ssl; http2 on; ssl_certificate /test/cert.pem; ssl_certificate_key /test/key.pem; location / { proxy_pass UPSTREAM; } } }`
	conf = strings.ReplaceAll(conf, "UPSTREAM", server.URL)
	if err := os.WriteFile(dir+"/nginx.conf", []byte(conf), 0644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("router-stream-%d", os.Getpid())
	network := "host"
	if hostname, err := os.Hostname(); err == nil {
		if exec.Command("docker", "inspect", hostname).Run() == nil {
			network = "container:" + hostname
		}
	}
	run("docker", "run", "--rm", "-d", "--name", name, "--network", network, "-v", dir+":/test:ro", "nginx:1.28-alpine", "nginx", "-c", "/test/nginx.conf", "-g", "daemon off;")
	defer exec.Command("docker", "rm", "-f", name).Run()
	time.Sleep(time.Second)
	out := run("curl", "-ksS", "--http2", "--max-time", "10", "-D", "-", "-w", "\nHTTP_VERSION=%{http_version}\n", "https://localhost:18774/v1/chat/completions", "-H", "Authorization: Bearer "+testToken, "-H", "Content-Type: application/json", "-d", `{"model":"stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(out, "HTTP_VERSION=2") || !strings.Contains(out, "[DONE]") || !strings.Contains(strings.ToLower(out), "cache-control: no-cache") {
		t.Fatal(out)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", "https://localhost:18774/v1/chat/completions", strings.NewReader(`{"model":"stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	start := time.Now()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, err = bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 || time.Since(start) > 400*time.Millisecond {
		t.Fatalf("buffered ingress: %s first event after %s", response.Proto, time.Since(start))
	}
	t.Log("nginx HTTP/2 ingress stream passed; first event arrived before upstream completion")
}
