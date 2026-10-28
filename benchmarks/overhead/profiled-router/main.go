// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Command profiled-router is a benchmark-only wrapper around the router service.
// Its Unix control socket starts and stops pprof during an external load phase.
// It is not a production replacement for cmd/metrum-ai-router.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"github.com/metrum-ai/router/internal/router"
)

type profiler struct {
	mu      sync.Mutex
	dir     string
	cpu     *os.File
	started bool
	stopped bool
}

func (p *profiler) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return errors.New("profile already started")
	}
	f, err := os.OpenFile(filepath.Join(p.dir, "cpu.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create CPU profile: %w", err)
	}
	if err = pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return fmt.Errorf("start CPU profile: %w", err)
	}
	p.cpu, p.started = f, true
	return nil
}

func (p *profiler) stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.stopped {
		return errors.New("profile is not running")
	}
	pprof.StopCPUProfile()
	p.stopped = true
	if err := p.cpu.Close(); err != nil {
		return fmt.Errorf("close CPU profile: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(p.dir, "heap.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create heap profile: %w", err)
	}
	// Stop CPU sampling first, so this forced GC is not included in CPU samples.
	// The caller must issue stop while the measured load remains in flight.
	runtime.GC()
	writeErr := pprof.WriteHeapProfile(f)
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}

func (p *profiler) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started && !p.stopped {
		pprof.StopCPUProfile()
		p.stopped = true
		_ = p.cpu.Close()
	}
}

// serveControl accepts only two fixed commands; neither accepts paths or data.
// The socket must live in a newly created mode-0700 directory.
func serveControl(listener net.Listener, p *profiler) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(conn, 17)).ReadString('\n')
			if err == nil {
				switch line {
				case "start\n":
					err = p.start()
				case "stop\n":
					err = p.stop()
				default:
					err = errors.New("unknown command")
				}
			}
			result := map[string]any{"ok": err == nil}
			if err != nil {
				result["error"] = err.Error()
			}
			_ = json.NewEncoder(conn).Encode(result)
		}()
	}
}

func run(configPath, controlDir, profileDir string) error {
	if configPath == "" || controlDir == "" || profileDir == "" {
		return errors.New("config, control-dir and profile-dir are required")
	}
	// Refuse existing paths so profiles and control sockets cannot clobber files.
	if err := os.Mkdir(controlDir, 0700); err != nil {
		return fmt.Errorf("create private control directory: %w", err)
	}
	defer os.Remove(controlDir)
	if err := os.Mkdir(profileDir, 0700); err != nil {
		return fmt.Errorf("create private profile directory: %w", err)
	}
	listener, err := net.Listen("unix", filepath.Join(controlDir, "control.sock"))
	if err != nil {
		return fmt.Errorf("listen on control socket: %w", err)
	}
	p := &profiler{dir: profileDir}
	defer p.close()
	defer listener.Close()
	controlErr := make(chan error, 1)
	go func() { controlErr <- serveControl(listener, p) }()

	cfg, err := router.LoadConfigForServe(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	svc, err := router.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize router: %w", err)
	}
	defer svc.Close()
	// Keep the HTTP timeout defaults aligned with cmd/metrum-ai-router.
	srv := &http.Server{Addr: cfg.Server.Listen, Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
	defer srv.Close()
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	select {
	case <-stop:
	case err = <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case err = <-controlErr:
		if err != nil {
			return fmt.Errorf("control socket: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func main() {
	config := flag.String("config", "", "router config path")
	control := flag.String("control-dir", "", "new private directory for Unix control socket")
	profiles := flag.String("profile-dir", "", "new private directory for profile output")
	flag.Parse()
	if err := run(*config, *control, *profiles); err != nil {
		log.Fatal(err)
	}
}
