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

// policyAdminInventory enumerates the enabled normal machine administrators of a
// published configuration. #871 supplies its prepared machine generation
// (mtls/oauth/federated subjects plus DB-store keys) through this seam, so the
// reverse guard never has to know how credentials are produced.
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

// enabledPolicyAdmins answers the guard's inventory question through the
// configured seam, defaulting to main's configuration-only inventory.
func (s *Service) enabledPolicyAdmins(cfg *Config) ([]Principal, error) {
	inventory := s.policyAdmins
	if inventory == nil {
		inventory = configuredPolicyAdminInventory{}
	}
	return inventory.enabledPolicyAdmins(cfg)
}

// GuardPolicyAdministrationLockout is the cross-store reverse guard: a change
// that would leave NO enabled normal administrator able to read/write/activate
// policy is refused, so the fleet cannot lock itself out of its own policy plane
// through a configuration or credential change. It evaluates the candidate
// authorizer built from the not-yet-published snapshot against that snapshot's
// own enabled inventory.
//
// #871 calls this from key revoke/disable and from its single publish path. The
// caller MUST hold s.usage.policyMutationMu, exactly as the reload path does, so
// the verdict and the publication it guards cannot interleave with another
// local policy change. Cross-replica interleaving: the inventory and candidate
// are read through the pool, so another replica committing a credential change
// between this verdict and this process's publication is not excluded by the
// local lock; that writer is itself responsible for running this guard (which is
// why #871 calls it from its single publish path). A stale read can therefore
// refuse spuriously, never admit a lockout this process could observe.
func (s *Service) GuardPolicyAdministrationLockout(candidate *Service, cfg *Config) error {
	if candidate == nil || cfg == nil {
		return ErrAuthorizationUnavailable
	}
	inventory, err := s.enabledPolicyAdmins(cfg)
	if err != nil {
		return err
	}
	if err := guardLastNormalPolicyAdministrator(candidate, inventory); err != nil {
		if errors.Is(err, ErrLastPolicyAdmin) {
			log.Printf("config control-plane reload refused: it would leave no enabled normal policy administrator")
		}
		return err
	}
	return nil
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
