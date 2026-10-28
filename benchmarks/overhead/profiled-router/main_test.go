// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"net"
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
	go func() { done <- serveControl(listener, p) }()
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
	if command("stop\n")["ok"] != true {
		t.Fatal("stop rejected")
	}
}

func TestRunRefusesExistingDirectories(t *testing.T) {
	parent := t.TempDir()
	if err := run("unused.yaml", parent, filepath.Join(parent, "profiles")); err == nil {
		t.Fatal("accepted existing control directory")
	}
}
