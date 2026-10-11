// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBridgeSessionStoreExpiryIsolationPruningAndConcurrency(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	store := newBridgeSessionStore()
	store.now = func() time.Time { return now }

	targetA := Target{Provider: "provider-a", Model: "model-a", Dialect: "openai-responses"}
	targetB := Target{Provider: "provider-a", Model: "model-b", Dialect: "openai-responses"}
	keyA := bridgeSessionKey(nil, "smoke-a", targetA, "raw-session-alpha")
	keyB := bridgeSessionKey(nil, "smoke-a", targetB, "raw-session-alpha")
	if keyA == keyB || keyA == "raw-session-alpha" || keyB == "raw-session-alpha" {
		t.Fatalf("session keys not isolated or hashed: keyA=%q keyB=%q", keyA, keyB)
	}

	ctx := context.Background()
	if err := store.Set(ctx, keyA, "resp_a", time.Minute, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, keyB, "resp_b", time.Minute, 10); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.Get(ctx, keyA); err != nil || !ok || got.PreviousResponseID != "resp_a" {
		t.Fatalf("keyA lookup=%#v ok=%v", got, ok)
	}
	if got, ok, err := store.Get(ctx, keyB); err != nil || !ok || got.PreviousResponseID != "resp_b" {
		t.Fatalf("keyB lookup=%#v ok=%v", got, ok)
	}

	now = now.Add(2 * time.Minute)
	if _, ok, err := store.Get(ctx, keyA); err != nil || ok {
		t.Fatal("expired session entry was returned")
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("expired entries not pruned, len=%d", got)
	}

	now = time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := store.Set(ctx, fmt.Sprintf("prune-%d", i), fmt.Sprintf("resp_%d", i), time.Hour, 3); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	if got := store.Len(); got != 3 {
		t.Fatalf("max-entry pruning len=%d, want 3", got)
	}
	for _, oldKey := range []string{"prune-0", "prune-1"} {
		if _, ok, err := store.Get(ctx, oldKey); err != nil || ok {
			t.Fatalf("oldest key %s was not pruned", oldKey)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("concurrent-%d", i%8)
			_ = store.Set(ctx, key, fmt.Sprintf("resp_concurrent_%d", i), time.Hour, 100)
			_, _, _ = store.Get(ctx, key)
		}(i)
	}
	wg.Wait()

	if err := store.Delete(ctx, "concurrent-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(ctx, "concurrent-1"); err != nil || ok {
		t.Fatal("deleted session entry was returned")
	}
}

func TestRedisBridgeSessionKeyHashesSessionMaterialAndNamespaces(t *testing.T) {
	rawSessionMaterial := "raw-session-alpha"
	target := Target{Provider: "provider-a", Model: "model-a", Dialect: "openai-responses"}
	hashed := bridgeSessionKey(nil, "smoke-a", target, rawSessionMaterial)
	keyA := bridgeRedisSessionKey("prod-router", hashed)
	keyB := bridgeRedisSessionKey("staging-router", hashed)
	if keyA == keyB {
		t.Fatalf("redis keys were not namespace-isolated: %q", keyA)
	}
	for _, forbidden := range []string{rawSessionMaterial, "provider-a", "model-a", "smoke-a"} {
		if strings.Contains(keyA, forbidden) || strings.Contains(keyB, forbidden) {
			t.Fatalf("redis key leaked raw namespace material %q: %q %q", forbidden, keyA, keyB)
		}
	}
	if !strings.Contains(keyA, "prod-router") || !strings.Contains(keyB, "staging-router") {
		t.Fatalf("redis keys do not include configured deployment namespaces: %q %q", keyA, keyB)
	}
}

func TestRedisBridgeSessionStoreIntegrationOptional(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("SMART_ROUTER_REDIS_TEST_ADDR"))
	if addr == "" {
		t.Skip("set SMART_ROUTER_REDIS_TEST_ADDR to run Redis bridge session integration test")
	}
	cfg := BridgeStatefulSessionsRedisConfig{
		Address:          addr,
		Namespace:        "test-smart-router",
		PasswordEnv:      "SMART_ROUTER_REDIS_TEST_PASSWORD",
		ConnectTimeoutMS: 500,
		ReadTimeoutMS:    500,
		WriteTimeoutMS:   500,
	}
	if os.Getenv("SMART_ROUTER_REDIS_TEST_PASSWORD") == "" {
		cfg.PasswordEnv = ""
	}
	store, err := newRedisBridgeSessionStore(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := store.Set(context.Background(), key, "resp_redis", 50*time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.Get(context.Background(), key); err != nil || !ok || got.PreviousResponseID != "resp_redis" {
		t.Fatalf("redis get=%#v ok=%v err=%v", got, ok, err)
	}
	time.Sleep(100 * time.Millisecond)
	if got, ok, err := store.Get(context.Background(), key); err != nil || ok {
		t.Fatalf("redis ttl get=%#v ok=%v err=%v", got, ok, err)
	}
	if err := store.Set(context.Background(), key, "resp_redis_delete", time.Minute, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.Get(context.Background(), key); err != nil || ok {
		t.Fatalf("redis delete get=%#v ok=%v err=%v", got, ok, err)
	}
}

func TestChatResponsesBridgeStaleStateRetryRequiresSpecificContinuationError(t *testing.T) {
	lookup := bridgeSessionLookup{
		Requested:          true,
		Key:                "hashed-session-key",
		PreviousResponseID: "resp_previous",
	}
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "previous response id field",
			body: `{"error":{"message":"previous_response_id was not found"}}`,
			want: true,
		},
		{
			name: "previous response prose",
			body: `{"error":{"message":"The previous response expired."}}`,
			want: true,
		},
		{
			name: "conversation stale state",
			body: `{"error":{"message":"conversation state is stale"}}`,
			want: true,
		},
		{
			name: "generic invalid tool schema",
			body: `{"error":{"message":"invalid tool schema: missing required field"}}`,
			want: false,
		},
		{
			name: "generic model not found",
			body: `{"error":{"message":"model not found"}}`,
			want: false,
		},
		{
			name: "generic unsupported reasoning",
			body: `{"error":{"message":"invalid reasoning effort"}}`,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chatResponsesBridgeStaleStateRetryAllowed(true, lookup, http.StatusBadRequest, []byte(tc.body), false)
			if got != tc.want {
				t.Fatalf("retryAllowed=%v, want %v for %s", got, tc.want, tc.body)
			}
		})
	}
	if chatResponsesBridgeStaleStateRetryAllowed(true, lookup, http.StatusBadRequest, []byte(`{"error":{"message":"previous_response_id was not found"}}`), true) {
		t.Fatal("already retried request should not retry again")
	}
}

func TestRedisBridgeBackendKeyIsSHA3AndNeverIncludesSecretValues(t *testing.T) {
	cfg := BridgeStatefulSessionsRedisConfig{
		Address:     "127.0.0.1:6379",
		Namespace:   "prod-router",
		UsernameEnv: "ROUTER_TEST_REDIS_USERNAME",
		PasswordEnv: "ROUTER_TEST_REDIS_PASSWORD",
	}
	t.Setenv("ROUTER_TEST_REDIS_USERNAME", "redis-user-value")
	t.Setenv("ROUTER_TEST_REDIS_PASSWORD", "redis-password-value-1")
	first := bridgeRedisBackendKey(cfg)
	if len(first) != 64 {
		t.Fatalf("fingerprint should be SHA3-512 truncated to 32 bytes of hex, got %d chars", len(first))
	}
	t.Setenv("ROUTER_TEST_REDIS_PASSWORD", "redis-password-value-2")
	if got := bridgeRedisBackendKey(cfg); got != first {
		t.Fatal("fingerprint changed when only the password environment value changed; it must not read secret values")
	}
	withInline := cfg
	withInline.Username = "inline-user"
	withInline.Password = "inline-password"
	if got := bridgeRedisBackendKey(withInline); got != first {
		t.Fatal("fingerprint must not include inline username/password values")
	}
	renamed := cfg
	renamed.PasswordEnv = "ROUTER_TEST_REDIS_PASSWORD_OTHER"
	if bridgeRedisBackendKey(renamed) == first {
		t.Fatal("fingerprint must distinguish backends that use different credential references")
	}
	legacy := sha256.Sum256([]byte(strings.Join([]string{cfg.Address, cfg.Namespace, "0", cfg.UsernameEnv, cfg.PasswordEnv, "false", "", "false", "0", "0", "0", "0"}, "\x00")))
	if first == hex.EncodeToString(legacy[:]) {
		t.Fatal("fingerprint still uses SHA-256")
	}
}

func TestRedisBridgeSessionKeyMigratesFromLegacyLayout(t *testing.T) {
	hashed := bridgeSessionKey(nil, "smoke-a", Target{Provider: "p", Model: "m"}, "session-alpha")
	v2 := bridgeRedisSessionKey("prod-router", hashed)
	legacy := bridgeRedisLegacySessionKey("prod-router", hashed)
	if !strings.HasPrefix(v2, "metrum-ai-router:bridge-session-v2:prod-router:") || len(v2) != len("metrum-ai-router:bridge-session-v2:prod-router:")+128 {
		t.Fatalf("v2 key should carry a full SHA3-512 digest: %q", v2)
	}
	if !strings.HasPrefix(legacy, "metrum-ai-router:bridge-session:prod-router:") || v2 == legacy {
		t.Fatalf("legacy key layout changed: %q", legacy)
	}
	if bridgeRedisSessionKey("", hashed) != bridgeRedisSessionKey(defaultBridgeStatefulSessionRedisNamespace, hashed) {
		t.Fatal("empty namespace must use the default namespace")
	}

	fake := startFakeRedis(t)
	ctx := context.Background()
	store, err := newRedisBridgeSessionStore(ctx, BridgeStatefulSessionsRedisConfig{Address: fake.addr, Namespace: "prod-router", ConnectTimeoutMS: 1000, ReadTimeoutMS: 1000, WriteTimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.extendLegacyKeyWindow(time.Hour)

	// A session written by a pre-upgrade replica is still readable.
	fake.set(legacy, "resp_legacy")
	if got, ok, err := store.Get(ctx, hashed); err != nil || !ok || got.PreviousResponseID != "resp_legacy" {
		t.Fatalf("legacy dual-read get=%#v ok=%v err=%v", got, ok, err)
	}
	// Inside the window, writes land in both layouts so old replicas keep
	// reading sessions created by upgraded ones.
	if err := store.Set(ctx, hashed, "resp_new", time.Minute, 0); err != nil {
		t.Fatal(err)
	}
	if fake.get(v2) != "resp_new" || fake.get(legacy) != "resp_new" {
		t.Fatalf("dual-write missing: v2=%q legacy=%q", fake.get(v2), fake.get(legacy))
	}
	if got, ok, err := store.Get(ctx, hashed); err != nil || !ok || got.PreviousResponseID != "resp_new" {
		t.Fatalf("v2 get=%#v ok=%v err=%v", got, ok, err)
	}
	if err := store.Delete(ctx, hashed); err != nil {
		t.Fatal(err)
	}
	if fake.has(v2) || fake.has(legacy) {
		t.Fatal("delete must clear both layouts inside the window")
	}

	// After the bounded window, legacy keys are ignored and not written.
	now = now.Add(2*time.Hour + bridgeRedisLegacyKeyGrace)
	fake.set(legacy, "resp_stale")
	if _, ok, err := store.Get(ctx, hashed); err != nil || ok {
		t.Fatalf("legacy key read after window: ok=%v err=%v", ok, err)
	}
	if err := store.Set(ctx, hashed, "resp_after", time.Minute, 0); err != nil {
		t.Fatal(err)
	}
	if fake.get(v2) != "resp_after" || fake.get(legacy) != "resp_stale" {
		t.Fatalf("after window only v2 should be written: v2=%q legacy=%q", fake.get(v2), fake.get(legacy))
	}
}

// fakeRedis is a minimal in-process RESP2 server covering the commands the
// bridge session store uses (PING, GET, SET, DEL, MULTI/EXEC). Unknown
// commands, such as go-redis's HELLO/CLIENT handshake, get an error reply.
type fakeRedis struct {
	addr string
	mu   sync.Mutex
	data map[string]string
}

func startFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{addr: ln.Addr().String(), data: map[string]string{}}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeRedis) set(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = value
}

func (f *fakeRedis) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[key]
}

func (f *fakeRedis) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok
}

func (f *fakeRedis) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	var queued [][]string
	inMulti := false
	for {
		args, err := readRESPCommand(r)
		if err != nil {
			return
		}
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(args[0])
		var reply string
		switch {
		case cmd == "MULTI":
			inMulti, queued = true, nil
			reply = "+OK\r\n"
		case cmd == "EXEC":
			reply = "*" + strconv.Itoa(len(queued)) + "\r\n"
			for _, q := range queued {
				reply += f.exec(q)
			}
			inMulti, queued = false, nil
		case inMulti:
			queued = append(queued, args)
			reply = "+QUEUED\r\n"
		default:
			reply = f.exec(args)
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func (f *fakeRedis) exec(args []string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch strings.ToUpper(args[0]) {
	case "PING":
		return "+PONG\r\n"
	case "SET":
		f.data[args[1]] = args[2]
		return "+OK\r\n"
	case "GET":
		value, ok := f.data[args[1]]
		if !ok {
			return "$-1\r\n"
		}
		return "$" + strconv.Itoa(len(value)) + "\r\n" + value + "\r\n"
	case "DEL":
		n := 0
		for _, key := range args[1:] {
			if _, ok := f.data[key]; ok {
				delete(f.data, key)
				n++
			}
		}
		return ":" + strconv.Itoa(n) + "\r\n"
	default:
		return "-ERR unknown command\r\n"
	}
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return strings.Fields(line), nil
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimRight(header, "\r\n")[1:])
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}
