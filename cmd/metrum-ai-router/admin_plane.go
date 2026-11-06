// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log"
	"strings"

	"github.com/metrum-ai/router/internal/router"
)

// startAdminPlane builds and starts the admin control-plane listener (#809).
// It returns (nil, nil) when the plane is disabled, which keeps v4.7
// configurations unchanged: no second listener and no new failure mode.
func startAdminPlane(cfg *router.Config, svc *router.Service, listenOverride string) (*router.AdminPlane, error) {
	if cfg == nil || !cfg.Admin.Enabled {
		return nil, nil
	}
	if override := strings.TrimSpace(listenOverride); override != "" {
		cfg.Admin.Listen = override
	}
	handler := svc.AdminHandler()
	if handler == nil {
		log.Fatalf("admin plane is enabled but no admin handler was built")
	}
	plane, err := router.NewAdminPlane(&cfg.Admin, handler)
	if err != nil {
		return nil, err
	}
	// #814's client-CA source owns rotation; the listener folds every reload
	// into the snapshot its handshake verifies against.
	if source := svc.AdminClientCASource(); source != nil {
		plane.SetClientCASource(source)
	}
	if err := plane.Start(); err != nil {
		return nil, err
	}
	log.Printf("Metrum AI Router admin plane listening on %s (legacy admin routes on the serving port: %t)",
		plane.Addr(), cfg.Admin.LegacyRoutesOnServingPort == nil || *cfg.Admin.LegacyRoutesOnServingPort)
	go func() {
		if serveErr, ok := <-plane.Err(); ok && serveErr != nil {
			// An admin accept-loop failure must never stop inference (ACP-006).
			log.Printf("admin plane accept loop stopped: %v", serveErr)
		}
	}()
	return plane, nil
}

// shutdownAdminPlane drains the admin listener with its own budget. A drain
// failure is logged and never fails serving shutdown.
func shutdownAdminPlane(plane *router.AdminPlane) {
	if plane == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), plane.ShutdownTimeout())
	defer cancel()
	if err := plane.Shutdown(ctx); err != nil {
		log.Printf("admin plane shutdown did not complete cleanly: %v", err)
	}
}
