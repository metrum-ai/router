// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const adminMachineRequestTimeout = 60 * time.Second

// One immutable machine publication. Disabled mechanisms have nil catalogs.
// Every authenticator's sources must close over THIS bundle's immutable
// catalogs/pools, never reload a global source mid-request. Mutable replay/KDF
// budgets retain their independently owned node lifetimes across generations.
// A published trust store is frozen: reload builds another complete store.
type adminMachineGeneration struct {
	config                *Config // immutable origin used to prepare complete CA replacements
	auth                  Authenticator
	authorizer            *authorizer
	keys                  *adminConfiguredKeyCatalog
	emergencyKeys         *adminEmergencyKeyCatalog
	emergencyCertificates *adminEmergencyCertificateCatalog
	trust                 *AdminMTLSTrustStore
	jwt                   *adminMachineJWTDispatcher
}
type adminMachineGenerationContextKey struct{}
type capturedAdminMachineGeneration struct {
	owner      *Service
	generation *adminMachineGeneration
}

// Contract rechecks may consume only a successful identity from the same
// service-owned capture. They cannot capture another generation or repeat
// emergency Use. This adapter is not a standalone credential verifier.
type adminCapturedMachineAuthenticator struct{ owner *Service }

func (a *adminCapturedMachineAuthenticator) Authenticate(ctx context.Context, _ *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	capture, ok := ctx.Value(adminMachineGenerationContextKey{}).(capturedAdminMachineGeneration)
	if !ok || capture.owner != a.owner || capture.generation == nil {
		return Principal{}, ErrNoCredential
	}
	principal, ok := PrincipalFrom(ctx)
	if !ok || !validMachinePrincipal(principal) {
		return Principal{}, ErrNoCredential
	}
	return principal, nil
}

// The original unauthenticated state-only health probes stay outside proof
// validation. All other routes use one capture within existing outer gates.
func (s *Service) adminMachineGenerationListenerAuthentication() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		authenticated := s.adminMachineGenerationMiddleware()(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
				next.ServeHTTP(w, r)
				return
			}
			authenticated.ServeHTTP(w, r)
		})
	}
}

func validateAdminMachineGeneration(next *adminMachineGeneration) error {
	if next == nil || next.auth == nil || next.authorizer == nil || next.authorizer.enforcer == nil {
		return errors.New("complete machine authentication and authorization generation required")
	}
	return nil
}

// The ONLY publication site for machine generations. No independent machine
// credential/grant stores, request locks, leases or dispatch-admission boundary.
func (s *Service) publishAdminMachineGeneration(next *adminMachineGeneration) error {
	if s == nil {
		return errors.New("machine generation owner required")
	}
	if err := validateAdminMachineGeneration(next); err != nil {
		return err
	}
	// Serialize publishers and immediate hook registration, never requests.
	// Otherwise a delayed N hook could overwrite handshake state after N+1.
	s.machinePublicationMu.Lock()
	defer s.machinePublicationMu.Unlock()
	if s.machineGeneration.Load() == nil {
		// Installed once before serving: subsequent contract rechecks use a
		// stable adapter and never load the publication pointer themselves.
		s.SetAdminAuthenticator(&adminCapturedMachineAuthenticator{owner: s})
		s.SetAdminClientCASource(s.machineClientCASource())
	}
	s.machineGeneration.Store(next)
	pool := adminMachineGenerationPool(next)
	for _, hook := range s.machineCAHooks {
		hook(pool)
	}
	return nil
}

// The factory is installed before serving. A running machine plane cannot
// silently fall back to serving-port credential/grant publication on reload.
func (s *Service) prepareAdminMachineConfigGeneration(cfg *Config, compatible *authorizer) (*adminMachineGeneration, error) {
	if s.machineGenerationBuilder == nil {
		if s.machineGeneration.Load() != nil {
			return nil, errors.New("machine generation reload factory unavailable")
		}
		return nil, nil // machine plane is not installed; preserve legacy behavior
	}
	next, err := s.machineGenerationBuilder(cfg, compatible)
	if err != nil {
		return nil, err
	}
	if err := validateAdminMachineGeneration(next); err != nil {
		return nil, err
	}
	if next.authorizer != compatible {
		return nil, errors.New("machine generation authorizer is not compatible with prepared config")
	}
	if next == s.machineGeneration.Load() {
		return nil, errors.New("machine preparation must return an unpublished replacement")
	}
	next.config = cfg
	return next, nil
}
func (s *Service) replaceAdminMachineGeneration(prepare func() (*adminMachineGeneration, error)) error {
	if s == nil || prepare == nil {
		return errors.New("machine generation preparation required")
	}
	s.machineReloadMu.Lock()
	defer s.machineReloadMu.Unlock()
	next, err := prepare()
	if err != nil {
		return err
	}
	return s.publishAdminMachineGeneration(next)
}

// Captured N remains valid for this request even when N+1 publishes. Emergency
// Use still performs its independent authoritative durable check per request.
func (s *Service) adminMachineGenerationMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s == nil {
				WriteAdminProblem(w, r, AdminProblemFor(r, http.StatusServiceUnavailable, "authorization-unavailable"))
				return
			}
			generation := s.machineGeneration.Load() // exactly ONCE per request
			if generation == nil {
				WriteAdminProblem(w, r, AdminProblemFor(r, http.StatusServiceUnavailable, "authorization-unavailable"))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), adminMachineRequestTimeout)
			defer cancel()
			ctx = context.WithValue(ctx, adminMachineGenerationContextKey{}, capturedAdminMachineGeneration{s, generation})
			ctx = s.PinAuthorization(ctx)
			adminMachineAuthenticationMiddleware(generation.auth)(next).ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
