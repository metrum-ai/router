// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnthropicCacheLocalhostE2E(t *testing.T) {
	for _, pair := range [][2]string{{"anthropic", "anthropic"}, {"anthropic", "openai-chat"}, {"openai-chat", "anthropic"}} {
		t.Run(pair[0]+"_to_"+pair[1], func(t *testing.T) {
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["stream"] != true {
					w.Header().Set("Content-Type", "application/json")
					if pair[1] == "anthropic" {
						fmt.Fprint(w, `{"id":"msg_cache","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":25,"output_tokens":5,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":300,"ephemeral_1h_input_tokens":0}}}`)
					} else {
						fmt.Fprint(w, `{"id":"chat_cache","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2325,"completion_tokens":5,"total_tokens":2330,"prompt_tokens_details":{"cached_tokens":2000,"cache_write_tokens":300}}}`)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if pair[1] == "anthropic" {
					for _, s := range []string{`{"type":"message_start","message":{"id":"msg_cache","usage":{"input_tokens":25,"output_tokens":0,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":25,"output_tokens":5,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300}}`, `{"type":"message_stop"}`} {
						fmt.Fprintf(w, "data: %s\n\n", s)
					}
				} else {
					fmt.Fprint(w, "data: {\"id\":\"chat_cache\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2325,\"completion_tokens\":5,\"total_tokens\":2330,\"prompt_tokens_details\":{\"cached_tokens\":2000,\"cache_write_tokens\":300}}}\n\ndata: [DONE]\n\n")
				}
			}))
			var err error
			up.Listener, err = net.Listen("tcp", "127.0.0.1:18475")
			if err != nil {
				t.Fatal(err)
			}
			up.Start()
			defer up.Close()
			cached, write := 1.0, 12.5
			target := anthropicToolTarget()
			target.InputPricePerMillionUSD = 10
			target.OutputPricePerMillionUSD = 20
			target.CachedInputPricePerMillionUSD = &cached
			target.CacheWritePricePerMillionUSD = &write
			svc := nativeStreamTestService(t, up.URL, pair[1], []Target{target}, func(c *Config) {
				c.Callers[0].Project = "local"
				c.Callers[0].Environment = "test"
				c.Server.Cache.Enabled = false
				c.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 7, CaptureRequest: true, CaptureResponse: true, CaptureToolCalls: true, Encryption: testContentCaptureEncryption(t)}
				c.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
				t.Setenv("CACHE_E2E_ADMIN_HASH", mustBcryptHash(t, "fixture-password"))
				c.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "CACHE_E2E_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
				c.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
				c.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
			})
			listener, err := net.Listen("tcp", "127.0.0.1:18474")
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: svc.Handler()}
			go server.Serve(listener)
			defer server.Close()
			client := &http.Client{Timeout: 15 * time.Second}
			for _, streaming := range []bool{false, true} {
				endpoint := "/v1/chat/completions"
				if pair[0] == "anthropic" {
					endpoint = "/anthropic/v1/messages"
				}
				options := ""
				if streaming && pair[0] == "openai-chat" {
					options = `,"stream_options":{"include_usage":true}`
				}
				req, _ := http.NewRequest("POST", "http://127.0.0.1:18474"+endpoint, strings.NewReader(fmt.Sprintf(`{"model":"native-stream","stream":%v,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]%s}`, streaming, options)))
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				input := `"prompt_tokens":2325`
				if pair[0] == "anthropic" {
					input = `"input_tokens":25`
				}
				if resp.StatusCode != 200 || !strings.Contains(string(raw), input) {
					t.Fatalf("stream=%v status=%d body=%s", streaming, resp.StatusCode, raw)
				}
			}
			var rows []usageRecord
			deadline := time.Now().Add(5 * time.Second)
			for {
				if err := svc.usage.db.Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				if len(rows) == 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("missing usage rows")
				}
				time.Sleep(10 * time.Millisecond)
			}
			var captures int64
			if err := svc.usage.db.Model(&contentCaptureRecord{}).Count(&captures).Error; err != nil || captures == 0 {
				t.Fatalf("content capture rows=%d err=%v", captures, err)
			}
			t.Logf("governed encrypted content_capture rows=%d; raw upstream usage replayed by local fixture", captures)
			for _, row := range rows {
				t.Logf("stored %s->%s stream=%v input=%d cached=%v write=%v output=%d input_cost=%g total_cost=%g", pair[0], pair[1], row.Stream, row.InputTokens, row.CachedInputTokens, row.CacheWriteInputTokens, row.OutputTokens, row.InputCostUSD, row.TotalCostUSD)
				if row.InputTokens != 2325 || row.CachedInputTokens == nil || *row.CachedInputTokens != 2000 || row.CacheWriteInputTokens == nil || *row.CacheWriteInputTokens != 300 || row.InputCostUSD != 0.006 || row.TotalCostUSD != 0.0061 {
					t.Fatalf("stored=%+v", row)
				}
				req, _ := http.NewRequest("GET", "http://127.0.0.1:18474/admin/reports/api/request/"+row.RequestID, nil)
				req.SetBasicAuth("admin", "fixture-password")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || !strings.Contains(string(raw), `"cacheWriteInputTokens":300`) {
					t.Fatalf("detail %d %s", resp.StatusCode, raw)
				}
			}
		})
	}
}
