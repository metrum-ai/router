// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

func TestProfilerProducesCPUAndHeapProfiles(t *testing.T) {
	dir := t.TempDir()
	p := &profiler{dir: dir}
	defer p.close()
	if err := p.stop(); err == nil {
		t.Fatal("stop before start succeeded")
	}
	if err := p.start(); err != nil {
		t.Fatal(err)
	}
	if err := p.start(); err == nil {
		t.Fatal("second start succeeded")
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = strings.Repeat("synthetic", 100)
	}
	if err := p.stop(); err != nil {
		t.Fatal(err)
	}
	if err := p.stop(); err == nil {
		t.Fatal("second stop succeeded")
	}
	for _, name := range []string{"cpu.pprof", "heap.pprof"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("profile %s mode/stat: %v, %v", name, info, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := profile.ParseData(data)
		if err != nil || len(parsed.SampleType) == 0 {
			t.Fatalf("profile %s invalid: %v", name, err)
		}
	}
}

func TestControlSocketRejectsUnknownCommands(t *testing.T) {
	dir := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	p := &profiler{dir: dir}
	done := make(chan error, 1)
	g := &loadGauge{}
	go func() { done <- serveControl(listener, p, g) }()
	t.Cleanup(func() {
		listener.Close()
		p.close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	command := func(value string) map[string]any {
		t.Helper()
		conn, err := net.DialTimeout("unix", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if command("stop\n")["ok"] != false || command("unknown\n")["ok"] != false || command(strings.Repeat("x", 32) + "\n")["ok"] != false {
		t.Fatal("control accepted invalid command")
	}
	if command("start\n")["ok"] != true {
		t.Fatal("start rejected")
	}
	if got := command("status\n"); got["ok"] != true || got["active"] != float64(0) {
		t.Fatalf("unexpected initial status: %v", got)
	}
	if command("stop32\n")["ok"] != false {
		t.Fatal("stop32 accepted without load")
	}
	if command("stop\n")["ok"] != true {
		t.Fatal("stop rejected")
	}
}

func TestLoadGaugeMonitorsLowestActive(t *testing.T) {
	g := &loadGauge{}
	if g.begin(32) {
		t.Fatal("accepted too few active requests")
	}
	for i := 0; i < 32; i++ {
		g.enter()
	}
	if !g.begin(32) || g.begin(32) {
		t.Fatal("capture tracking did not begin exclusively")
	}
	g.leave()
	g.enter()
	if got := g.end(); got != 31 {
		t.Fatalf("minimum active = %d, want 31", got)
	}
}

func TestGaugeCountsOnlyChatHandlers(t *testing.T) {
	g := &loadGauge{}
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := g.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			close(entered)
			<-release
		}
	}))
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
		close(done)
	}()
	<-entered
	if got := g.current(); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got := g.current(); got != 1 {
		t.Fatalf("readiness changed active to %d", got)
	}
	close(release)
	<-done
	if got := g.current(); got != 0 {
		t.Fatalf("active after completion = %d", got)
	}
}

func TestRunRefusesExistingDirectories(t *testing.T) {
	parent := t.TempDir()
	if err := run("unused.yaml", parent, filepath.Join(parent, "profiles")); err == nil {
		t.Fatal("accepted existing control directory")
	}
}
