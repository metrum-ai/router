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
	storeKeys             *adminStoreKeyCatalog
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

// machinePlaneSnapshot is what the single publisher observes under its lock:
// the configuration, synchronized authorizer and machine generation published
// at that moment.
type machinePlaneSnapshot struct {
	Config     *Config
	Authorizer *authorizer
	Generation *adminMachineGeneration
}

// machinePlanePublication is one complete, prepared replacement for the machine
// plane and the state a generation must stay consistent with. A nil field keeps
// the current value. The publisher installs every non-nil field under ONE lock,
// or none of them, and the installation itself cannot fail.
type machinePlanePublication struct {
	// Generation is the prepared machine generation (nil: keep the current one).
	Generation *adminMachineGeneration
	// Authorizer is the synchronized snapshot the generation is pinned to
	// (nil: keep the current one).
	Authorizer *authorizer
	// Config is a not-yet-published configuration (nil: unchanged).
	Config *Config
	// Callers is the prepared caller directory that belongs to Config.
	Callers map[string]*callerRuntime
	// APIKeys is the prepared serving-port key catalogue that belongs to Config.
	APIKeys map[string]*adminAPIKeyRuntime
	// MaxStaleness is the configured policy freshness window (zero: unchanged).
	MaxStaleness time.Duration
	// Policy acknowledges one authoritative policy read (nil: nothing to ack).
	Policy *policySyncAcknowledgement
}

// policySyncAcknowledgement is the revision proof of one successful
// authoritative read. Publication applies it after installing the bundle, so a
// freshness claim can never describe authority that was not published.
type policySyncAcknowledgement struct {
	Revision   int64
	ObservedAt time.Time
	Known      bool
}

// publishMachinePlane is the ONLY machine-plane publication path: startup, the
// authoritative policy synchronization, a configuration reload and a CA reload
// all install their replacement through it.
//
// It also owns the ONLY lock in the publication path (s.machinePublicationMu),
// and it runs prepare under that lock, so a generation is always built from the
// configuration and synchronized authorizer published at that instant. That is
// what keeps a superseded input from being published after a newer one - for
// example a CA reload resurrecting a key that a configuration reload removed.
// Requests never take this lock: they capture the published pointer once.
//
// Preparation is attempted exactly once. A preparation error publishes nothing;
// once a prepared bundle is accepted, the commit is infallible and installs the
// whole bundle or none of it.
func (s *Service) publishMachinePlane(prepare func(machinePlaneSnapshot) (*machinePlanePublication, error)) error {
	if s == nil || prepare == nil {
		return errors.New("machine plane publication requires an owner and a preparation")
	}
	s.machinePublicationMu.Lock()
	defer s.machinePublicationMu.Unlock()
	current := machinePlaneSnapshot{Config: s.cfg, Authorizer: s.currentAuthorizer(), Generation: s.machineGeneration.Load()}
	pub, err := prepare(current)
	if err != nil {
		return err
	}
	if pub == nil {
		return errors.New("machine plane publication required")
	}
	if pub.Generation != nil {
		if err := validateAdminMachineGeneration(pub.Generation); err != nil {
			return err
		}
		// The generation a caller hands over was prepared OUTSIDE this lock, so
		// the authorizer it is pinned to may already be superseded. Publishing it
		// would let a later synchronization see "no change" and leave machine
		// requests on revoked grants indefinitely. The only acceptable pin is the
		// authorizer this publication installs (or keeps).
		published := pub.Authorizer
		if published == nil {
			published = current.Authorizer
		}
		if published == nil || pub.Generation.authorizer != published {
			return errors.New("machine generation is not pinned to the authorizer being published")
		}
	} else if pub.Authorizer == nil && pub.Config == nil && pub.Callers == nil && pub.APIKeys == nil && pub.MaxStaleness == 0 && pub.Policy == nil {
		return errors.New("empty machine plane publication")
	}
	// Installation starts here. Nothing below returns an error: the reload,
	// policy-sync and CA paths have already validated everything they prepared.
	if pub.Callers != nil && s.quota != nil {
		s.quota.installPreparedCallers(pub.Callers)
	}
	if pub.Config != nil {
		s.cfg = pub.Config
		if s.configSnapshot != nil {
			s.configSnapshot.Store(pub.Config)
		}
		if s.callerAuth != nil {
			callers := pub.Callers
			if callers == nil && s.quota != nil {
				callers = s.quota.callers
			}
			s.callerAuth.rebuild(callerRuntimesInConfigOrder(pub.Config, callers))
		}
		if pub.APIKeys != nil {
			s.adminAPIKeys = pub.APIKeys
		}
	}
	if pub.MaxStaleness > 0 && s.policyFreshness != nil {
		s.policyFreshness.mu.Lock()
		s.policyFreshness.maxAge = pub.MaxStaleness
		s.policyFreshness.mu.Unlock()
	}
	if pub.Authorizer != nil {
		s.authorizerSnapshot.Store(pub.Authorizer)
	}
	if pub.Generation != nil {
		if s.machineGeneration.Load() == nil {
			// Installed once before serving: subsequent contract rechecks use a
			// stable adapter and never load the publication pointer themselves.
			s.SetAdminAuthenticator(&adminCapturedMachineAuthenticator{owner: s})
			s.SetAdminClientCASource(s.machineClientCASource())
		}
		s.machineGeneration.Store(pub.Generation)
		pool := adminMachineGenerationPool(pub.Generation)
		for _, hook := range s.machineCAHooks {
			hook(pool)
		}
	}
	if pub.Policy != nil && pub.Policy.Known && s.policyFreshness != nil {
		s.policyFreshness.observeSuccess(pub.Policy.Revision, pub.Policy.ObservedAt)
	}
	return nil
}

// The factory is installed before serving. A running machine plane cannot
// silently fall back to serving-port credential/grant publication on reload.
// reloadMachineCredentials republishes the complete generation from the CURRENT
// configuration and the CURRENT synchronized authorizer after an authoritative
// credential mutation (issue, rotate or revoke of an API-issued key), through the
// one publication path. A CA reload and a credential mutation therefore cannot
// publish each other's superseded state.
func (s *Service) reloadMachineCredentials() error {
	if s == nil || s.machineGenerationBuilder == nil {
		return nil
	}
	return s.publishMachinePlane(func(current machinePlaneSnapshot) (*machinePlanePublication, error) {
		if current.Config == nil || current.Authorizer == nil || current.Generation == nil {
			return nil, errors.New("machine credential publication requires a published generation")
		}
		next, err := s.prepareAdminMachineConfigGeneration(current.Config, current.Authorizer)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, errors.New("machine credential publication requires a generation")
		}
		return &machinePlanePublication{Generation: next}, nil
	})
}

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
