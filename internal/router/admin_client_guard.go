// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

type adminClientBindingRow struct {
	ID               string
	ClientID         string
	ClientStatus     string
	CredentialStatus string
	Kind             string
	Identity         string
	Subject          string
	ExpiresAt        time.Time
}

type adminClientPolicyInventory struct {
	base              generationInventory
	store             *adminClientStore
	now               time.Time
	clientID          string
	credentialID      string
	candidate         *adminClientCredential
	minimumExpiryTime time.Time
}

func (i adminClientPolicyInventory) enabledPolicyAdmins(cfg *Config) ([]Principal, error) {
	admins, err := i.base.enabledPolicyAdmins(cfg)
	if err != nil {
		return nil, err
	}
	var bindings []adminClientBindingRow
	if err := i.store.db.WithContext(context.Background()).Raw("SELECT x.id,x.client_id,c.status AS client_status,x.status AS credential_status,x.kind,x.identity,x.subject,x.expires_at FROM "+adminClientCredentialTable+" x JOIN "+adminClientTable+" c ON c.id=x.client_id ORDER BY CASE WHEN c.status='active' AND x.status='active' THEN 0 ELSE 1 END,x.client_id,x.id LIMIT ?", adminClientMaxClients*adminClientMaxCredentials).Scan(&bindings).Error; err != nil {
		return nil, err
	}
	remaining := make([]Principal, 0, len(admins))
	for _, principal := range admins {
		managed, survives := false, false
		for _, binding := range bindings {
			if adminPrincipalManagedByClientBinding(principal, binding) {
				managed = true
			}
			if !adminPrincipalMatchesClientBinding(principal, binding.Kind, binding.ID, binding.Identity, binding.Subject) {
				continue
			}
			if binding.ClientStatus != "active" || binding.CredentialStatus != "active" || !binding.ExpiresAt.After(i.now) {
				continue
			}
			target := i.candidate == nil && binding.ClientID == i.clientID && (i.credentialID == "" || binding.ID == i.credentialID)
			if !target && (i.minimumExpiryTime.IsZero() || binding.ExpiresAt.After(i.minimumExpiryTime)) {
				survives = true
			}
		}
		// Registration makes the subject managed immediately. Because authentication
		// fails closed for every unregistered credential on that subject, the guard
		// must not count those credentials as surviving the candidate registration.
		if i.candidate != nil && principal.Subject == i.candidate.Subject {
			managed = true
		}
		if managed && !survives {
			continue
		}
		remaining = append(remaining, principal)
	}
	return remaining, nil
}

// adminPrincipalManagedByClientBinding mirrors the subject/identity fallback
// in adminClientStore.authenticate. Once a client claims a subject (or mTLS
// identity), unregistered credentials for that managed identity are refused
// and cannot remain counted as working administrators.
func adminPrincipalManagedByClientBinding(p Principal, binding adminClientBindingRow) bool {
	if p.Subject == binding.Subject {
		return true
	}
	if p.Kind != binding.Kind {
		return false
	}
	if p.Kind == "mtls" {
		if p.Subject == binding.Subject {
			return true
		}
		for _, identity := range adminPrincipalMTLSIdentities(p) {
			if identity == binding.Identity {
				return true
			}
		}
		return false
	}
	return p.Subject == binding.Subject || p.CredentialID == binding.ID
}

func adminPrincipalMatchesClientBinding(p Principal, kind, id, identity, subject string) bool {
	if p.Kind != kind || p.Subject != subject {
		return false
	}
	if kind == "mtls" {
		for _, matched := range adminPrincipalMTLSIdentities(p) {
			if matched == identity {
				return true
			}
		}
		return false
	}
	return p.CredentialID == id
}

func adminPrincipalMTLSIdentities(p Principal) []string {
	var identities []string
	if p.Identities != "" {
		identities = strings.Split(p.Identities, "\x00")
	}
	if p.Identity != "" {
		found := false
		for _, identity := range identities {
			if identity == p.Identity {
				found = true
				break
			}
		}
		if !found {
			identities = append(identities, p.Identity)
		}
	}
	return identities
}

func (s *Service) adminClientCredentialExists(credential adminClientCredential) bool {
	if s == nil {
		return false
	}
	generation := s.machineGeneration.Load()
	if generation == nil {
		return false
	}
	admins, err := (generationInventory{generation: generation}).enabledPolicyAdmins(s.configSnapshot.Load())
	if err != nil {
		return false
	}
	for _, principal := range admins {
		if adminPrincipalMatchesClientBinding(principal, credential.Kind, credential.ID, credential.Identity, credential.Subject) {
			return true
		}
	}
	return false
}

// lockAdminClientPolicyChange holds the same local governance lock as policy
// mutation while it verifies that a client lifecycle change leaves an enabled
// policy administrator. The caller must release the returned function after
// the store transaction completes.
func (s *Service) lockAdminClientPolicyChange(ctx context.Context, clientID, credentialID string, candidate *adminClientCredential, minimumExpiry time.Time) (func(), error) {
	if s == nil || s.usage == nil || s.usage.db == nil || s.configSnapshot == nil || s.configSnapshot.Load() == nil {
		return nil, ErrAuthorizationUnavailable
	}
	s.usage.policyMutationMu.Lock()
	unlock := func() { s.usage.policyMutationMu.Unlock() }
	if candidate == nil {
		principal, ok := PrincipalFrom(ctx)
		if !ok || strings.TrimSpace(principal.Subject) == "" {
			unlock()
			return nil, ErrAuthorizationUnavailable
		}
		if err := s.adminClientActorCoversClientPermissions(ctx, principal.Subject, clientID, nil); err != nil {
			unlock()
			return nil, err
		}
	}
	cfg := s.configSnapshot.Load()
	candidateAuth, err := s.policyAuthorizerService(s.usage.db, cfg, nil)
	if err != nil {
		unlock()
		return nil, err
	}
	generation := s.machineGeneration.Load()
	if generation == nil {
		unlock()
		return nil, ErrAuthorizationUnavailable
	}
	inventory := adminClientPolicyInventory{
		base: generationInventory{generation: generation}, store: s.adminClients, now: time.Now().UTC(),
		clientID: clientID, credentialID: credentialID, candidate: candidate, minimumExpiryTime: minimumExpiry,
	}
	if err := s.guardPolicyAdministrationLockoutAgainst(candidateAuth, cfg, inventory); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// adminClientActorCoversClientPermissions asks the active authorizer to compare
// every registered, working principal on the target client, and an optional
// credential being registered, with the actor for each versioned operation.
// A narrower actor cannot change a client's effective access.
func (s *Service) adminClientActorCoversClientPermissions(ctx context.Context, actorSubject, clientID string, candidate *adminClientCredential) error {
	if s == nil || s.adminClients == nil || s.machineGeneration.Load() == nil {
		return ErrAuthorizationUnavailable
	}
	authorizer := s.currentAuthorizer()
	if authorizer == nil || authorizer.enforcer == nil {
		return ErrAuthorizationUnavailable
	}
	type credential struct {
		ID       string
		Kind     string
		Identity string
		Subject  string
	}
	var credentials []credential
	if err := s.adminClients.db.WithContext(ctx).Raw("SELECT x.id,x.kind,x.identity,x.subject FROM "+adminClientCredentialTable+" x JOIN "+adminClientTable+" c ON c.id=x.client_id WHERE x.client_id=? AND c.status='active' AND x.status='active' AND x.expires_at>? ORDER BY x.subject,x.id", clientID, time.Now().UTC()).Scan(&credentials).Error; err != nil {
		return err
	}
	generation := s.machineGeneration.Load()
	principals, err := (generationInventory{generation: generation}).enabledPolicyAdmins(s.configSnapshot.Load())
	if err != nil {
		return err
	}
	checked := make(map[string]bool)
	permissionDomains := map[string]struct{}{AdminGlobalDomain: {}}
	for _, credential := range credentials {
		working := false
		for _, principal := range principals {
			if adminPrincipalMatchesClientBinding(principal, credential.Kind, credential.ID, credential.Identity, credential.Subject) {
				working = true
				break
			}
		}
		if !working || checked[credential.Subject] {
			continue
		}
		checked[credential.Subject] = true
	}
	if candidate != nil {
		for _, principal := range principals {
			if adminPrincipalMatchesClientBinding(principal, candidate.Kind, candidate.ID, candidate.Identity, candidate.Subject) {
				checked[principal.Subject] = true
			}
		}
	}
	for subject := range checked {
		permissions, err := adminImplicitPermissionsAcrossDomains(authorizer.enforcer, subject)
		if err != nil {
			return err
		}
		for _, permission := range permissions {
			if len(permission) == 4 && permission[1] != "" {
				permissionDomains[permission[1]] = struct{}{}
			}
		}
	}
	for subject := range checked {
		for _, operation := range AdminV1Operations() {
			checks := append([]AdminCheck{{Object: operation.Object, Action: operation.Action}}, operation.ExtraChecks...)
			for _, check := range checks {
				domains := []string{AdminGlobalDomain}
				if operation.Domain != AdminDomainGlobal && operation.Domain != AdminDomainGlobalWildcard {
					domains = domains[:0]
					for domain := range permissionDomains {
						domains = append(domains, domain)
					}
				}
				for _, domain := range domains {
					targetAllowed := authorizer.permits(subject, domain, check.Object, check.Action)
					if !targetAllowed {
						continue
					}
					actorAllowed := authorizer.permits(actorSubject, domain, check.Object, check.Action)
					if !actorAllowed {
						return ErrAdminForbidden
					}
				}
			}
		}
	}
	return nil
}

func adminClientGuardFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrAdminForbidden) {
		adminKeyProblem(w, r, http.StatusForbidden, "admin-forbidden")
		return
	}
	if errors.Is(err, ErrLastPolicyAdmin) {
		adminKeyProblem(w, r, http.StatusConflict, "last-policy-administrator")
		return
	}
	adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-guard-unavailable")
}
