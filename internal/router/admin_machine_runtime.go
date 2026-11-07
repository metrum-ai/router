// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// One service-owned resource lifetime; immutable generations may overlap.
// Shared DPoP retention policy remains a pending design amendment; no rollout
// of this runtime is claimed until that policy and final integration are done.
type adminMachineRuntime struct {
	replay         *adminDPoPReplayState
	emergencySlots chan struct{}
	lookup         func(string) (string, bool)
	httpClient     *http.Client // private test dependency; production uses system trust
}

func (s *Service) initAdminMachineRuntime() error {
	if !s.cfg.Admin.Enabled {
		return nil
	}
	runtime := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1), lookup: os.LookupEnv}
	s.machineGenerationBuilder = runtime.prepare
	c := s.cfg.Admin.Authentication
	// Preserve the empty listener's existing trusted bootstrap setter seam. No
	// credential-empty default publication is needed to keep that plane closed.
	if len(s.cfg.Server.AdminAuth.APIKeys) == 0 && !adminMTLSTrustConfigured(c.MTLS) && len(c.Issuers) == 0 && len(c.Emergency.Credentials) == 0 {
		return nil
	}
	next, err := s.prepareAdminMachineConfigGeneration(s.cfg, s.currentAuthorizer())
	if err != nil {
		return err
	}
	return s.publishAdminMachineGeneration(next)
}

func (rt *adminMachineRuntime) prepare(cfg *Config, compatible *authorizer) (*adminMachineGeneration, error) {
	if cfg == nil || compatible == nil {
		return nil, errors.New("machine runtime configuration/authorizer required")
	}
	g := &adminMachineGeneration{config: cfg, authorizer: compatible}
	if !cfg.Admin.Enabled {
		g.auth = NewAuthenticatorChain()
		return g, nil
	}
	settings, err := prepareAdminMachineRuntimeSettings(cfg.Admin.Authentication, cfg.Admin.ExternalURL)
	if err != nil {
		return nil, err
	}
	if len(cfg.Admin.Authentication.Emergency.Credentials) != 0 {
		// Never substitute test tables/no-op events for authoritative production Use.
		return nil, errors.New("machine emergency production transaction/event adapters unavailable")
	}
	if err := validateAdminAPIKeys(cfg.Server.AdminAuth.APIKeys, false); err != nil {
		return nil, err
	}
	ordinaryIDs := make(map[string]bool, len(cfg.Server.AdminAuth.APIKeys))
	for _, key := range cfg.Server.AdminAuth.APIKeys {
		ordinaryIDs[key.KeyID] = true
	}
	keyCfg := cfg
	if !settings.apiKeysEnabled {
		keyCfg = &Config{}
	}
	g.keys, err = newConfiguredAdminKeyCatalog(keyCfg, rt.lookup)
	if err != nil {
		return nil, err
	}
	g.emergencyKeys, err = newAdminEmergencyKeyCatalog(nil, ordinaryIDs, rt.emergencySlots, time.Now)
	if err != nil {
		return nil, err
	}
	keyAuth, err := newAdminOrdinaryKeyMachineAuthenticator(func() *adminConfiguredKeyCatalog { return g.keys }, func() *adminEmergencyKeyCatalog { return g.emergencyKeys })
	if err != nil {
		return nil, err
	}
	chain := []Authenticator{keyAuth}
	var mtls Authenticator
	if adminMTLSTrustConfigured(cfg.Admin.Authentication.MTLS) {
		g.trust, err = NewAdminMTLSTrustStore(cfg.Admin.Authentication.MTLS)
		if err != nil {
			return nil, err
		}
		mtls, err = newAdminMTLSTrustAuthenticator(g.trust)
		if err != nil {
			return nil, err
		}
	}
	if len(settings.issuers) != 0 {
		// DPoP needs a trusted HTTPS base. A permitted loopback HTTP base simply
		// omits the optional validator; cnf-bearing tokens then fail closed.
		var dpop *adminDPoPValidator
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.Admin.ExternalURL)), "https://") {
			dpop, err = newAdminDPoPValidatorWithReplay(cfg.Admin.ExternalURL, settings.dpopProofMaxAge, settings.dpopReplayCapacity, rt.replay)
			if err != nil {
				return nil, err
			}
		}
		bindings := make([]adminMachineJWTBinding, 0, len(settings.issuers))
		ctx, cancel := context.WithTimeout(context.Background(), adminMachineRequestTimeout)
		defer cancel()
		for _, issuer := range settings.issuers {
			validator, err := newAdminMachineJWTAuthenticator(ctx, issuer.profile, rt.httpClient)
			if err != nil {
				return nil, err
			}
			bindings = append(bindings, adminMachineJWTBinding{Authenticator: validator, MinimumConstraint: issuer.minimumConstraint})
		}
		dispatcher, err := newAdminMachineJWTDispatcher(bindings, dpop, func() *x509.CertPool { return adminMachineGenerationPool(g) })
		if err != nil {
			return nil, err
		}
		g.jwt = dispatcher.(*adminMachineJWTDispatcher)
		chain = append(chain, dispatcher)
	}
	if mtls != nil {
		chain = append(chain, mtls)
	}
	g.auth = NewAuthenticatorChain(chain...)
	return g, nil
}
