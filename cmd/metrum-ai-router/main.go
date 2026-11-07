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
	"strings"
	"syscall"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
	"github.com/metrum-ai/router/internal/router"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to router YAML config")
	listen := flag.String("listen", "", "override server.listen (for replicas sharing one database-backed configuration on one host)")
	adminListen := flag.String("admin-listen", "", "override admin.listen (for replicas sharing one database-backed configuration on one host); ignored when admin.enabled is false")
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
	if strings.TrimSpace(*listen) != "" {
		cfg.Server.Listen = strings.TrimSpace(*listen)
	}

	svc, err := router.New(cfg)
	if err != nil {
		log.Fatalf("initialize router: %v", err)
	}
	defer svc.Close()

	// The admin control plane gets its own listener, deadlines and drain
	// (#809). It is opt-in, so a v4.7 configuration starts exactly one server.
	adminPlane, err := startAdminPlane(cfg, svc, *adminListen)
	if err != nil {
		log.Fatalf("admin plane: %v", err)
	}

	timeouts := serverTimeoutsFor(cfg)
	srv := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: svc.Handler(),
	}
	applyServerTimeouts(srv, timeouts)

	errs := make(chan error, 1)
	if manager := svc.TLSManager(); manager != nil {
		// Router-native TLS (#810): obtain (or load) the certificate before
		// binding, so the listener never comes up in plaintext, then renew in
		// the background.
		tlsCtx, stopTLS := context.WithCancel(context.Background())
		defer stopTLS()
		if err := manager.EnsureCertificate(tlsCtx); err != nil {
			log.Fatalf("tls: %v", err)
		}
		go manager.Run(tlsCtx)
		srv.TLSConfig = manager.TLSConfig()
		go func() {
			log.Printf("Metrum AI Router listening on %s (TLS)", cfg.Server.Listen)
			errs <- srv.ListenAndServeTLS("", "")
		}()
	} else {
		go func() {
			log.Printf("Metrum AI Router listening on %s", cfg.Server.Listen)
			errs <- srv.ListenAndServe()
		}()
	}

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

	// Serving and admin drain independently: a stuck admin drain must never
	// change serving shutdown, and an admin outage never changes serving
	// readiness (ACP-006).
	adminDone := make(chan struct{})
	go func() {
		shutdownAdminPlane(adminPlane)
		close(adminDone)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	servingErr := srv.Shutdown(ctx)
	<-adminDone
	if servingErr != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", servingErr)
		os.Exit(1)
	}
}
