// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"gorm.io/gorm"
)

// adminV1RegistryWithPolicy composes the guarded policy operations into the
// production registry. The private policy table is added here, once, so the
// published contract, the mount and the handlers cannot drift.
func adminV1RegistryWithPolicy() (*AdminV1Registry, error) {
	specs := make([]Operation, 0, len(adminV1OperationSpecs)+len(policyOperationSpecs()))
	specs = append(specs, adminV1OperationSpecs...)
	specs = append(specs, policyOperationSpecs()...)
	return NewAdminV1Registry(specs)
}

// policyAdminDeps builds the production mutation dependencies. The published
// configuration always comes from the frozen config snapshot (never a request),
// and the enabled inventory is derived from that same snapshot while the caller
// holds the governance lock.
func (s *Service) policyAdminDeps() policyMutationDeps {
	return policyMutationDeps{
		PublishedConfig: func() *Config { return s.configSnapshot.Load() },
		Enabled:         s.enabledPolicyAdmins,
		Current: func(tx *gorm.DB, cfg *Config) (*Service, error) {
			return s.policyAuthorizerService(tx, cfg, nil)
		},
		Build: func(tx *gorm.DB, cfg *Config, lines []string) (*Service, error) {
			return s.policyAuthorizerService(tx, cfg, lines)
		},
	}
}

// policyAuthorizerService builds the machine authorizer for a policy mutation
// from the transaction and the published configuration. lines overrides the
// candidate policy; nil reads the current authoritative policy through tx.
func (s *Service) policyAuthorizerService(tx *gorm.DB, cfg *Config, lines []string) (*Service, error) {
	if s == nil || tx == nil || cfg == nil {
		return nil, ErrAuthorizationUnavailable
	}
	authz := cfg.Server.AdminAuth.Authorization
	var callers map[string]*callerRuntime
	if s.quota != nil {
		callers = s.quota.callers
	}
	if lines != nil {
		authz = AdminAuthorizationConfig{Enabled: true, Source: "inline", Policy: lines}
	}
	built, err := newAuthorizer(authz, callers, &usageStore{db: tx}, cfg.Server.AdminAuth)
	if err != nil {
		return nil, ErrAuthorizationUnavailable
	}
	view := &Service{
		authorizer:         built,
		authorizationAudit: s.authorizationAudit,
		configSnapshot:     s.configSnapshot,
		policyFreshness:    s.policyFreshness,
		usage:              s.usage,
	}
	return view, nil
}

// policyAuthorizerView wraps a prepared authorizer so a guard can evaluate the
// exact object a publication is about to install, instead of building a second
// candidate that could disagree with it.
func (s *Service) policyAuthorizerView(built *authorizer) *Service {
	return &Service{
		authorizer:         built,
		authorizationAudit: s.authorizationAudit,
		configSnapshot:     s.configSnapshot,
		policyFreshness:    s.policyFreshness,
		usage:              s.usage,
	}
}

// policyAdminInventory enumerates the enabled normal machine administrators of a
// published configuration. #871 supplies its machine generation (configured keys
// plus mtls/oauth/federated subjects) through this seam, so the reverse guard
// never has to know how credentials are produced.
type policyAdminInventory interface {
	enabledPolicyAdmins(snapshot *Config) ([]Principal, error)
}

// configuredPolicyAdminInventory counts the machine credentials that exist in
// configuration on main today: enabled admin API keys whose verifier resolves.
// Basic users cannot count (they are not machine principals), and OIDC browser
// users have no enumerable subject before login. Until #871 merges, those kinds
// are therefore NOT part of the inventory — the reverse guard only sees
// configured admin keys.
type configuredPolicyAdminInventory struct{}

func (configuredPolicyAdminInventory) enabledPolicyAdmins(cfg *Config) ([]Principal, error) {
	if cfg == nil {
		return nil, ErrAuthorizationUnavailable
	}
	// No pre-sized capacity: the sum of configured lengths can overflow the
	// allocation hint, and the inventory is small either way.
	var out []Principal
	for keyID, runtime := range buildAdminAPIKeyRuntimes(cfg, os.LookupEnv) {
		subject := runtime.subject
		if subject == "" {
			subject = "adminkey:" + keyID
		}
		out = append(out, Principal{Subject: subject, Kind: "adminkey", CredentialID: keyID, Issuer: "local"})
	}
	return out, nil
}

// enabledPolicyAdmins answers the guard's inventory question for the CURRENTLY
// published machine generation. It reads the atomic publication pointer and
// keeps no separate inventory field: a plain field written by a publisher and
// read by the policy-mutation guard would be a data race (startup and CA
// publication do not hold the governance lock), and a cached inventory could
// disagree with the generation actually published.
func (s *Service) enabledPolicyAdmins(cfg *Config) ([]Principal, error) {
	if s == nil {
		return nil, ErrAuthorizationUnavailable
	}
	if generation := s.machineGeneration.Load(); generation != nil {
		return generationInventory{generation: generation}.enabledPolicyAdmins(cfg)
	}
	return configuredPolicyAdminInventory{}.enabledPolicyAdmins(cfg)
}

// GuardPolicyAdministrationLockout is the cross-store reverse guard: a change
// that would leave NO enabled normal administrator able to read/write/activate
// policy is refused, so the fleet cannot lock itself out of its own policy plane
// through a configuration or credential change. It evaluates the candidate
// authorizer built from the not-yet-published snapshot against that snapshot's
// own enabled inventory.
//
// #871 calls this from its single publish path. The caller MUST hold
// s.usage.policyMutationMu, exactly as the reload path does, so the verdict and
// the publication it guards cannot interleave with another local policy change. Cross-replica interleaving: the inventory and candidate
// are read through the pool, so another replica committing a credential change
// between this verdict and this process's publication is not excluded by the
// local lock; that writer is itself responsible for running this guard (which is
// why #871 calls it from its single publish path). A stale read can therefore
// refuse spuriously, never admit a lockout this process could observe.
func (s *Service) GuardPolicyAdministrationLockout(candidate *Service, cfg *Config) error {
	return s.guardPolicyAdministrationLockoutAgainst(candidate, cfg, nil)
}

// guardPolicyAdministrationLockoutAgainst evaluates the same guard against an
// EXPLICIT inventory. A publication uses it with the inventory of the generation
// it is about to install, so the verdict is about the candidate's administrators
// rather than the ones still published.
func (s *Service) guardPolicyAdministrationLockoutAgainst(candidate *Service, cfg *Config, inventory policyAdminInventory) error {
	if candidate == nil || cfg == nil {
		return ErrAuthorizationUnavailable
	}
	admins, err := inventoryOrPublished(s, inventory).enabledPolicyAdmins(cfg)
	if err != nil {
		return err
	}
	if err := guardLastNormalPolicyAdministrator(candidate, admins); err != nil {
		if errors.Is(err, ErrLastPolicyAdmin) {
			log.Printf("config control-plane reload refused: it would leave no enabled normal policy administrator")
		}
		return err
	}
	return nil
}

// inventoryOrPublished prefers an explicit candidate inventory over the one in
// the published generation.
func inventoryOrPublished(s *Service, explicit policyAdminInventory) policyAdminInventory {
	if explicit != nil {
		return explicit
	}
	return publishedGenerationInventory{owner: s}
}

// publishedGenerationInventory reads the inventory from the atomic publication
// pointer at call time, so it can never disagree with the generation a request
// would capture.
type publishedGenerationInventory struct{ owner *Service }

func (p publishedGenerationInventory) enabledPolicyAdmins(cfg *Config) ([]Principal, error) {
	return p.owner.enabledPolicyAdmins(cfg)
}

// policyCurrentETag is the strong validator of the POLICY revision for the
// guarded policy operations. It is deliberately separate from the configuration
// revision validator so a policy write is never gated on a config tag.
func (s *Service) policyCurrentETag(_ *http.Request) (string, int64, error) {
	if s == nil || s.usage == nil || s.usage.db == nil {
		return "", 0, ErrAuthorizationUnavailable
	}
	var row policyMutationRevision
	if err := s.usage.db.First(&row, 1).Error; err != nil {
		return "", 0, fmt.Errorf("policy revision: %w", err)
	}
	return AdminETagForRevision(row.Revision), row.Revision, nil
}
