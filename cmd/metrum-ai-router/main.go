// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
	"github.com/metrum-ai/router/internal/router"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to router YAML config")
	showVersion := flag.Bool("version", false, "print build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.Text())
		return
	}

	cfg, err := router.LoadConfigForServe(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	svc, err := router.New(cfg)
	if err != nil {
		log.Fatalf("initialize router: %v", err)
	}
	defer svc.Close()

	readTO, writeTO, idleTO := defaultHTTPServerTimeouts()
	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTO,
		WriteTimeout:      writeTO,
		IdleTimeout:       idleTO,
	}

	errs := make(chan error, 1)
	go func() {
		log.Printf("Metrum AI Router listening on %s", cfg.Server.Listen)
		errs <- srv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		log.Printf("received %s, shutting down", sig)
	case err := <-errs:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		os.Exit(1)
	}
}
