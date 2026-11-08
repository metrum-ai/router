// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"sort"
	"strings"
)

type policyValidationDocument struct {
	Valid       bool   `json:"valid"`
	CanActivate bool   `json:"can_activate"`
	ReasonCode  string `json:"reason_code,omitempty"`
	Revision    int64  `json:"revision"`
	Hash        string `json:"hash,omitempty"`
}
type policyDiffDocument struct {
	Added      []string `json:"added"`
	Removed    []string `json:"removed"`
	BeforeHash string   `json:"before_hash"`
	AfterHash  string   `json:"after_hash"`
	Revision   int64    `json:"revision"`
}

func policyPreviewOperationSpecs() []Operation {
	return []Operation{
		{ID: "validatePolicySet", Method: "POST", Path: "/admin/v1/policy-sets/{set_id}/validate", Summary: "Validate a stored policy candidate without changing state", Object: "admin:policy", Action: "read", Domain: AdminDomainGlobal, NoSideEffect: true, Request: struct{}{}, Response: policyValidationDocument{}, Errors: []int{404, 422, 503}, RateClass: AdminRateClassRead, HandlerKey: "policy.set.validate"},
		{ID: "diffPolicySet", Method: "GET", Path: "/admin/v1/policy-sets/{set_id}/diff", Summary: "Compare normalized policy content", Object: "admin:policy", Action: "read", Domain: AdminDomainGlobal, NoSideEffect: true, Response: policyDiffDocument{}, Errors: []int{404, 422, 503}, Query: []adminQueryParameter{{Name: "against", Required: true, Description: "Base policy set ID"}}, RateClass: AdminRateClassRead, HandlerKey: "policy.set.diff"},
	}
}
func (s *usageStore) withPolicyResourceRead(ctx context.Context, read func(*gorm.DB, int64) error) error {
	if s == nil || s.db == nil || ctx == nil || read == nil || ctx.Err() != nil {
		return ErrAuthorizationUnavailable
	}
	s.policyMutationMu.Lock()
	defer s.policyMutationMu.Unlock()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row policyMutationRevision
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&row, 1).Error; err != nil {
			return ErrAuthorizationUnavailable
		}
		return read(tx, row.Revision)
	})
	return normalizePolicyMutationError(err)
}
func policyViewWithinBounds(view policyResourceView) bool {
	return len(view.Rules)+len(view.Memberships) <= maxPolicyGuardCandidateRows && len(view.Memberships) <= maxPolicyGuardMemberships
}

func (h *policyAdminHandler) validate(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength != 0 {
		var empty struct{}
		if !policyHTTPDecode(w, r, &empty) {
			return
		}
	}
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		policyHTTPProblem(w, r, ErrAdminForbidden)
		return
	}
	var result policyValidationDocument
	err := h.store.withPolicyResourceRead(r.Context(), func(tx *gorm.DB, revision int64) error {
		result.Revision = revision
		view, err := loadPolicyResource(tx, r.PathValue("set_id"))
		if err != nil {
			return err
		}
		if !policyViewWithinBounds(view) {
			result.ReasonCode = "invalid-policy"
			return nil
		}
		lines := view.lines()
		for _, line := range lines {
			fields, err := parseCasbinPolicyLine(line)
			if err != nil {
				result.ReasonCode = "invalid-policy"
				return nil
			}
			if err := validateCasbinPolicyMutationFields(fields, "policy candidate"); err != nil {
				result.ReasonCode = "invalid-policy"
				return nil
			}
		}
		if h.deps.PublishedConfig == nil || h.deps.Build == nil || h.deps.Current == nil || h.deps.Enabled == nil {
			return ErrAuthorizationUnavailable
		}
		cfg := h.deps.PublishedConfig()
		if cfg == nil {
			return ErrAuthorizationUnavailable
		}
		current, err := h.deps.Current(tx, cfg)
		if err != nil || current == nil {
			return ErrAuthorizationUnavailable
		}
		candidate, err := h.deps.Build(tx, cfg, lines)
		if err != nil {
			return normalizePolicyMutationError(err)
		}
		enabled, err := h.deps.Enabled(cfg)
		if err != nil {
			return ErrAuthorizationUnavailable
		}
		result.Valid = true
		result.Hash = view.hash()
		err = guardPolicyMutation(r.Context(), p, current, candidate, enabled, "activate")
		if err == nil {
			result.CanActivate = true
			return nil
		}
		if errors.Is(err, ErrAuthorizationUnavailable) || errors.Is(err, ErrPolicyStale) {
			return err
		}
		_, result.ReasonCode = adminAuthorizationProblem(normalizePolicyMutationError(err))
		if errors.Is(err, ErrInvalidPolicy) {
			result.Valid = false
		}
		return nil
	})
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	policyHTTPJSON(w, 200, result.Revision, result)
}
func (h *policyAdminHandler) diff(w http.ResponseWriter, r *http.Request) {
	against := r.URL.Query().Get("against")
	if strings.TrimSpace(against) == "" {
		writeAdminProblemCode(w, r, 400, "invalid-query", "")
		return
	}
	result := policyDiffDocument{Added: []string{}, Removed: []string{}}
	err := h.store.withPolicyResourceRead(r.Context(), func(tx *gorm.DB, revision int64) error {
		before, err := loadPolicyResource(tx, against)
		if err != nil {
			return err
		}
		after, err := loadPolicyResource(tx, r.PathValue("set_id"))
		if err != nil {
			return err
		}
		if !policyViewWithinBounds(before) || !policyViewWithinBounds(after) {
			return ErrInvalidPolicy
		}
		result.BeforeHash = before.hash()
		result.AfterHash = after.hash()
		result.Revision = revision
		counts := map[string]int{}
		for _, line := range before.lines() {
			counts[line]--
		}
		for _, line := range after.lines() {
			counts[line]++
		}
		for line, count := range counts {
			for count > 0 {
				result.Added = append(result.Added, line)
				count--
			}
			for count < 0 {
				result.Removed = append(result.Removed, line)
				count++
			}
		}
		sort.Strings(result.Added)
		sort.Strings(result.Removed)
		return nil
	})
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	policyHTTPJSON(w, 200, result.Revision, result)
}
