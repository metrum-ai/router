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

	"gorm.io/gorm"
)

// One service-owned resource lifetime; immutable generations may overlap.
// Shared DPoP retention policy remains a pending design amendment; no rollout
// of this runtime is claimed until that policy and final integration are done.
type adminMachineRuntime struct {
	replay         *adminDPoPReplayState
	emergencySlots chan struct{}
	lookup         func(string) (string, bool)
	httpClient     *http.Client // private test dependency; production uses system trust
	// Durable credential lifecycle events (#869). Nil on planes without a
	// control-plane database; emergency enrollment then fails closed.
	events     *adminMachineCredentialEventSink
	activation *adminEmergencyActivationStore
	replicas   int
}

func (s *Service) initAdminMachineRuntime() error {
	if !s.cfg.Admin.Enabled {
		return nil
	}
	runtime := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1), lookup: os.LookupEnv, replicas: 1}
	if s.configControlPlane != nil && s.cfg.Admin.Enabled {
		sink, err := newAdminMachineCredentialEventSink(s.adminCredentialEventSink())
		if err != nil {
			return err
		}
		activation, err := newAdminEmergencyActivationStore(s.configControlPlane, runtime.replicas, time.Now, func(reason string) string {
			return adminRedactThenTrim(reason, 512)
		})
		if err != nil {
			return err
		}
		runtime.events, runtime.activation = sink, activation
	}
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
	if len(cfg.Admin.Authentication.Emergency.Credentials) != 0 && (rt.events == nil || rt.activation == nil) {
		// Never substitute test tables/no-op events for authoritative Use.
		return nil, errors.New("machine emergency durable event/activation adapters unavailable")
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
	if err := rt.prepareEmergencyCatalogs(cfg, ordinaryIDs, g); err != nil {
		return nil, err
	}
	var emergencyKeyAuth, emergencyCertificateAuth Authenticator
	if len(g.emergencyKeys.keys) != 0 {
		emergencyKeyAuth, err = newAdminEmergencyKeyMachineAuthenticator(func() *adminEmergencyKeyCatalog { return g.emergencyKeys }, rt.activation, rt.recordEmergencyUse)
		if err != nil {
			return nil, err
		}
	}
	if len(g.emergencyCertificates.byID) != 0 {
		emergencyCertificateAuth, err = newAdminEmergencyCertificateMachineAuthenticator(func() *adminEmergencyCertificateCatalog { return g.emergencyCertificates },
			func() *x509.CertPool { return adminMachineGenerationPool(g) }, rt.activation, rt.recordEmergencyUse)
		if err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	keyAuth, err := newAdminOrdinaryKeyMachineAuthenticator(func() *adminConfiguredKeyCatalog { return g.keys }, func() *adminEmergencyKeyCatalog { return g.emergencyKeys })
	if err != nil {
		return nil, err
	}
	chain := []Authenticator{keyAuth}
	if emergencyKeyAuth != nil {
		chain = append(chain, emergencyKeyAuth)
	}
	if emergencyCertificateAuth != nil {
		chain = append(chain, emergencyCertificateAuth)
	}
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

// prepareEmergencyCatalogs builds the immutable emergency namespaces from the
// trusted offline descriptors, with the authoritative activation store and a
// transaction-bound credential-event write for every accepted Use.
func (rt *adminMachineRuntime) prepareEmergencyCatalogs(cfg *Config, ordinaryIDs map[string]bool, g *adminMachineGeneration) error {
	var keyCfgs []adminEmergencyKeyVerifierConfig
	var certCfgs []adminEmergencyCertificateConfig
	reserved := map[string]bool{}
	for id := range ordinaryIDs {
		reserved[id] = true
	}
	for _, credential := range cfg.Admin.Authentication.Emergency.Credentials {
		issuedAt, err := time.Parse(time.RFC3339Nano, credential.IssuedAt)
		if err != nil {
			return errAdminEmergencyInvalid
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, credential.ExpiresAt)
		if err != nil {
			return errAdminEmergencyInvalid
		}
		switch credential.Kind {
		case "api_key":
			hash, ok := rt.lookup(credential.KeyHashEnv)
			if !ok || strings.TrimSpace(hash) == "" {
				return errors.New("emergency verifier reference unavailable")
			}
			keyCfgs = append(keyCfgs, adminEmergencyKeyVerifierConfig{id: credential.ID, subject: credential.Subject,
				issuer: credential.Issuer, tokenHash: hash, issuedAt: issuedAt, expiresAt: expiresAt})
		case "certificate":
			certCfgs = append(certCfgs, adminEmergencyCertificateConfig{id: credential.ID, subject: credential.Subject,
				issuer: credential.Issuer, thumbprint: credential.CertificateSHA256, issuedAt: issuedAt, expiresAt: expiresAt})
		default:
			return errAdminEmergencyInvalid
		}
		reserved[credential.ID] = true
	}
	catalog, err := newAdminEmergencyKeyCatalog(keyCfgs, ordinaryIDs, rt.emergencySlots, time.Now)
	if err != nil {
		return err
	}
	g.emergencyKeys = catalog
	if g.emergencyCertificates, err = newAdminEmergencyCertificateCatalog(certCfgs, reserved, time.Now); err != nil {
		return err
	}
	return nil
}

// recordEmergencyUse writes the mandatory durable event inside the caller's
// activation transaction; an error rolls the Use back so no identity escapes
// without an audited activation record.
func (rt *adminMachineRuntime) recordEmergencyUse(tx *gorm.DB, principal Principal, activation adminEmergencyActivation) error {
	event := AdminCredentialEvent{
		At:           rt.nowUTC(),
		Kind:         "emergency",
		Lifecycle:    "use",
		CredentialID: principal.CredentialID,
		Subject:      adminRedactThenTrim(principal.Subject, 1024),
		Issuer:       adminRedactThenTrim(principal.Issuer, 2048),
		Outcome:      "success",
		ReasonCode:   "",
	}
	return insertAdminCredentialEventTx(tx, event)
}

func (rt *adminMachineRuntime) nowUTC() time.Time { return time.Now().UTC() }
