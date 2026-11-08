// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

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
		Enabled:         s.enabledPolicyAdminInventory,
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

// enabledPolicyAdminInventory lists the ENABLED normal administrator credentials
// of one published configuration: configured admin keys that resolve to a usable
// verifier and are not disabled. Basic users are included when they authenticate
// without a token verifier; OIDC subjects cannot be enumerated before login and
// are therefore not counted (documented conservative limit of the reverse guard).
func (s *Service) enabledPolicyAdminInventory(cfg *Config) ([]Principal, error) {
	if cfg == nil {
		return nil, ErrAuthorizationUnavailable
	}
	out := make([]Principal, 0, len(cfg.Server.AdminAuth.APIKeys)+len(cfg.Server.AdminAuth.Basic.Users))
	for keyID, runtime := range buildAdminAPIKeyRuntimes(cfg, os.LookupEnv) {
		subject := runtime.subject
		if subject == "" {
			subject = "adminkey:" + keyID
		}
		out = append(out, Principal{Subject: subject, Kind: "adminkey", CredentialID: keyID, Issuer: "local"})
	}
	for _, user := range cfg.Server.AdminAuth.Basic.Users {
		username := strings.TrimSpace(user.Username)
		if username == "" {
			continue
		}
		// A basic user whose password hash is not resolvable cannot authenticate,
		// so it must not count as an enabled administrator.
		if strings.TrimSpace(os.Getenv(strings.TrimSpace(user.PasswordHashEnv))) == "" {
			continue
		}
		subject := strings.TrimSpace(user.Subject)
		if subject == "" {
			subject = "basic:" + username
		}
		out = append(out, Principal{Subject: subject, Kind: "basic", CredentialID: username})
	}
	return out, nil
}

// guardConfigReloadPolicyAdministrators is the cross-store reverse guard: a
// configuration reload that would leave NO enabled normal administrator able to
// read/write/activate policy is refused before publication, so the fleet cannot
// lock itself out of its own policy plane through a config or credential change.
// It evaluates the candidate authorizer built from the reloaded configuration
// and the candidate's own enabled inventory.
func (s *Service) guardConfigReloadPolicyAdministrators(candidate *Service, cfg *Config) error {
	if candidate == nil || cfg == nil {
		return ErrAuthorizationUnavailable
	}
	inventory, err := s.enabledPolicyAdminInventory(cfg)
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
