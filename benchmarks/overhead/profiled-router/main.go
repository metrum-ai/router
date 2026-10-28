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

// loadGauge counts only chat generation handlers, not readiness or admin traffic.
// It also records the minimum active count during CPU stop and heap capture.
type loadGauge struct {
	mu       sync.Mutex
	active   int
	minimum  int
	tracking bool
}

func (g *loadGauge) enter() {
	g.mu.Lock()
	g.active++
	g.mu.Unlock()
}

func (g *loadGauge) leave() {
	g.mu.Lock()
	g.active--
	if g.tracking && g.active < g.minimum {
		g.minimum = g.active
	}
	g.mu.Unlock()
}

func (g *loadGauge) current() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

func (g *loadGauge) begin(minimum int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tracking || g.active < minimum {
		return false
	}
	g.minimum, g.tracking = g.active, true
	return true
}

func (g *loadGauge) end() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tracking = false
	return g.minimum
}

func (g *loadGauge) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" {
			g.enter()
			defer g.leave()
		}
		next.ServeHTTP(w, r)
	})
}

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

// serveControl accepts only fixed commands, never caller-provided paths.
// The socket must live in a newly created mode-0700 directory.
func serveControl(listener net.Listener, p *profiler, g *loadGauge) error {
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
				case "status\n":
				case "stop32\n", "stop128\n":
					minimum := 32
					if line == "stop128\n" {
						minimum = 128
					}
					if !g.begin(minimum) {
						err = errors.New("insufficient active chat requests")
					} else {
						err = p.stop()
						observed := g.end()
						if err == nil && observed < minimum {
							err = fmt.Errorf("invalid profile: active chat requests fell to %d during capture", observed)
						}
					}
				default:
					err = errors.New("unknown command")
				}
			}
			result := map[string]any{"ok": err == nil, "active": g.current()}
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
	g := &loadGauge{}
	defer p.close()
	defer listener.Close()
	controlErr := make(chan error, 1)
	go func() { controlErr <- serveControl(listener, p, g) }()

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
	srv := &http.Server{Addr: cfg.Server.Listen, Handler: g.handler(svc.Handler()), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
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
