// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/x509"
	"net/http"
	"testing"
	"time"
)

type generationReloadCapabilitySource struct {
	pool   *x509.CertPool
	called chan struct{}
}

func (s *generationReloadCapabilitySource) Snapshot() *x509.CertPool           { return s.pool }
func (s *generationReloadCapabilitySource) OnReload(hook func(*x509.CertPool)) { hook(s.pool) }
func (s *generationReloadCapabilitySource) Reload() error {
	select {
	case s.called <- struct{}{}:
	default:
	}
	return nil
}

// Installing a dynamic generation source must preserve its reload capability,
// not lose it when its initial snapshot is folded into a fixed TLS pool.
func TestRuntimeMachineInstalledCAReloadCapability(t *testing.T) {
	_, _, roots := certificateBindingCA(t)
	source := &generationReloadCapabilitySource{pool: roots, called: make(chan struct{}, 1)}
	cfg := AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:0"}
	defaultAdminPlaneConfig(&cfg)
	cfg.Authentication.MTLS.ReloadInterval = "1s"
	plane, err := NewAdminPlane(&cfg, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	plane.SetClientCASource(source)
	if err := plane.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plane.Shutdown(ctx)
	}()
	select {
	case <-source.called:
	case <-time.After(5 * time.Second):
		t.Fatal("installed generation CA source lost its Reload capability")
	}
}
