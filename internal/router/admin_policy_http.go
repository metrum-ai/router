// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

type policyRuleInput struct {
	SubjectOrRole string `json:"subject_or_role"`
	Domain        string `json:"domain"`
	Object        string `json:"object"`
	Action        string `json:"action"`
}
type policyMembershipInput struct {
	Subject string `json:"subject"`
	Role    string `json:"role"`
	Domain  string `json:"domain"`
}
type policySetReplaceRequest struct {
	Rules           []policyRuleInput       `json:"rules"`
	RoleMemberships []policyMembershipInput `json:"role_memberships"`
}
type policySetCreateRequest struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Version         int                     `json:"version"`
	Rules           []policyRuleInput       `json:"rules"`
	RoleMemberships []policyMembershipInput `json:"role_memberships"`
}
type policyRuleDocument struct {
	ID            uint   `json:"id"`
	SubjectOrRole string `json:"subject_or_role"`
	Domain        string `json:"domain"`
	Object        string `json:"object"`
	Action        string `json:"action"`
}
type policyMembershipDocument struct {
	ID      uint   `json:"id"`
	Subject string `json:"subject"`
	Role    string `json:"role"`
	Domain  string `json:"domain"`
}
type policySetDocument struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Version         int                        `json:"version"`
	Status          string                     `json:"status"`
	Revision        int64                      `json:"revision"`
	CreatedBy       string                     `json:"created_by"`
	CreatedAt       string                     `json:"created_at"`
	Rules           []policyRuleDocument       `json:"rules"`
	RoleMemberships []policyMembershipDocument `json:"role_memberships"`
}
type policyMutationDocument struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	RecordID uint   `json:"record_id,omitempty"`
}

// Prototype operation set is explicit and uses the shared contract modes.
// It is composed into the production registry only when all handlers exist.
func policyOperationSpecs() []Operation {
	operations := []Operation{
		{ID: "listPolicySets", Method: "GET", Path: "/admin/v1/policy-sets", NoSideEffect: true, Summary: "List policy sets", Object: "admin:policy", Action: "read", Domain: AdminDomainGlobal, Response: policySetListDocument{}, Errors: []int{503}, RateClass: AdminRateClassRead, HandlerKey: "policy.sets.list", Pagination: &AdminPaginationSpec{Sorts: []string{"id"}, DefaultSort: "id"}},
		{ID: "createPolicySet", Method: "POST", SuccessStatus: 201, Path: "/admin/v1/policy-sets", Summary: "Create a guarded policy draft", Object: "admin:policy", Action: "write", Domain: AdminDomainGlobal, Request: policySetCreateRequest{}, Response: policyMutationDocument{}, Errors: []int{409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore, RateClass: AdminRateClassWrite, HandlerKey: "policy.sets.create"},
		{ID: "getPolicySet", Method: "GET", NoSideEffect: true, Path: "/admin/v1/policy-sets/{set_id}", Summary: "Read a policy set", Object: "admin:policy", Action: "read", Domain: AdminDomainGlobal, Response: policySetDocument{}, Errors: []int{404, 503}, RateClass: AdminRateClassRead, HandlerKey: "policy.set.get"},
		{ID: "replacePolicySet", Method: "PUT", Path: "/admin/v1/policy-sets/{set_id}", Summary: "Replace a guarded policy draft", Object: "admin:policy", Action: "write", Domain: AdminDomainGlobal, Request: policySetReplaceRequest{}, Response: policyMutationDocument{}, Errors: []int{404, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore, RateClass: AdminRateClassWrite, HandlerKey: "policy.set.replace"},
		{ID: "deletePolicySet", Method: "DELETE", Path: "/admin/v1/policy-sets/{set_id}", Summary: "Delete a guarded policy draft", Object: "admin:policy", Action: "write", Domain: AdminDomainGlobal, Response: policyMutationDocument{}, Errors: []int{404, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore, RateClass: AdminRateClassWrite, HandlerKey: "policy.set.delete"},
	}
	operations = append(operations, policyRecordOperationSpecs()...)
	operations = append(operations, policyLifecycleOperationSpecs()...)
	return append(operations, policyPreviewOperationSpecs()...)
}

// policyAuditFactory resolves the transactional audit writer for one request.
// The write happens inside the same transaction as the policy change (#869), so
// a rejected audit insert aborts the change and no revision is published.
type policyAuditFactory func(context.Context) (func(*gorm.DB, int64, policyResourceResult) error, error)

func staticPolicyAuditFactory(audit func(*gorm.DB, int64, policyResourceResult) error) policyAuditFactory {
	return func(context.Context) (func(*gorm.DB, int64, policyResourceResult) error, error) {
		return audit, nil
	}
}

type policyAdminHandler struct {
	store *usageStore
	deps  policyMutationDeps
	// auditFor is the production audit binding (request metadata -> one usage
	// audit row in the change's transaction). It never defaults to a no-op.
	auditFor policyAuditFactory
}

// newPolicyAdminHandlers keeps the test-facing static-audit constructor.
func newPolicyAdminHandlers(store *usageStore, deps policyMutationDeps, audit func(*gorm.DB, int64, policyResourceResult) error) map[string]http.HandlerFunc {
	return newPolicyAdminHandlersWithAudit(store, deps, staticPolicyAuditFactory(audit))
}

func newPolicyAdminHandlersWithAudit(store *usageStore, deps policyMutationDeps, auditFor policyAuditFactory) map[string]http.HandlerFunc {
	h := &policyAdminHandler{store: store, deps: deps, auditFor: auditFor}
	handlers := map[string]http.HandlerFunc{"policy.set.validate": h.validate, "policy.set.diff": h.diff, "policy.set.activate": h.activate, "policy.set.rollback": h.rollback, "policy.sets.list": h.listSets, "policy.sets.create": h.create, "policy.set.get": h.get, "policy.set.replace": h.replace, "policy.set.delete": h.remove}
	for key, handler := range h.recordHandlers() {
		handlers[key] = handler
	}
	return handlers
}
func policyHTTPDecode(w http.ResponseWriter, r *http.Request, out any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdminConfigBodyBytes+1))
	if err != nil {
		writeAdminProblemCode(w, r, 400, "invalid-body", "")
		return false
	}
	if len(body) > maxAdminConfigBodyBytes {
		writeAdminProblemCode(w, r, 413, "body-too-large", "")
		return false
	}
	if !utf8.Valid(body) {
		writeAdminProblemCode(w, r, 400, "invalid-body", "")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeAdminProblemCode(w, r, 400, "invalid-body", "")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeAdminProblemCode(w, r, 400, "invalid-body", "")
		return false
	}
	return true
}
func policyHTTPProblem(w http.ResponseWriter, r *http.Request, err error) {
	status, code := adminAuthorizationProblem(err)
	writeAdminProblemCode(w, r, status, code, "")
}
func policyHTTPJSON(w http.ResponseWriter, status int, revision int64, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", AdminETagForRevision(revision))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func policyRequestLines(rules []policyRuleInput, memberships []policyMembershipInput) ([]string, error) {
	if len(rules)+len(memberships) > maxPolicyGuardCandidateRows || len(memberships) > maxPolicyGuardMemberships {
		return nil, ErrInvalidPolicy
	}
	lines := make([]string, 0, len(rules)+len(memberships))
	for _, p := range rules {
		line, err := formatCasbinPolicyLine("p", p.SubjectOrRole, p.Domain, p.Object, p.Action)
		if err != nil {
			return nil, ErrInvalidPolicy
		}
		lines = append(lines, line)
	}
	for _, g := range memberships {
		line, err := formatCasbinPolicyLine("g", g.Subject, g.Role, g.Domain)
		if err != nil {
			return nil, ErrInvalidPolicy
		}
		lines = append(lines, line)
	}
	return lines, nil
}
func (h *policyAdminHandler) mutate(w http.ResponseWriter, r *http.Request, cmd policyResourceCommand, status int) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		policyHTTPProblem(w, r, ErrAdminForbidden)
		return
	}
	// The shared mount validates the strong tag then translates it to a revision.
	revision, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("If-Match")), 10, 64)
	if err != nil || revision < 0 {
		writeAdminProblemCode(w, r, 428, "precondition-required", "")
		return
	}
	cmd.ExpectedRevision = revision
	audit, err := h.auditFor(r.Context())
	if err != nil {
		// No authenticated audit context means no audit row, so no change. The
		// fixed v1 problem carries no audit detail.
		writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
		return
	}
	result, err := h.store.mutatePolicyResource(r.Context(), p, h.deps, cmd, audit)
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	policyHTTPJSON(w, status, result.Revision, policyMutationDocument{ID: result.SetID, Revision: result.Revision, RecordID: result.RecordID})
}
func (h *policyAdminHandler) create(w http.ResponseWriter, r *http.Request) {
	var body policySetCreateRequest
	if !policyHTTPDecode(w, r, &body) {
		return
	}
	lines, err := policyRequestLines(body.Rules, body.RoleMemberships)
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	h.mutate(w, r, policyResourceCommand{Kind: "create-set", SetID: body.ID, Name: body.Name, Version: body.Version, Policy: lines}, 201)
}
func (h *policyAdminHandler) replace(w http.ResponseWriter, r *http.Request) {
	var body policySetReplaceRequest
	if !policyHTTPDecode(w, r, &body) {
		return
	}
	lines, err := policyRequestLines(body.Rules, body.RoleMemberships)
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	h.mutate(w, r, policyResourceCommand{Kind: "replace-set", SetID: r.PathValue("set_id"), Policy: lines}, 200)
}
func (h *policyAdminHandler) remove(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, policyResourceCommand{Kind: "delete-set", SetID: r.PathValue("set_id")}, 200)
}
func (h *policyAdminHandler) get(w http.ResponseWriter, r *http.Request) {
	view, err := h.store.getPolicyResourceContext(r.Context(), r.PathValue("set_id"))
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	doc := policySetDocument{ID: view.Set.ID, Name: view.Set.Name, Version: view.Set.Version, Status: view.Set.Status, Revision: view.Revision, CreatedBy: view.Set.CreatedBy, CreatedAt: view.Set.CreatedAt, Rules: []policyRuleDocument{}, RoleMemberships: []policyMembershipDocument{}}
	for _, p := range view.Rules {
		doc.Rules = append(doc.Rules, policyRuleDocument{p.ID, p.SubjectOrRole, p.Domain, p.Object, p.Action})
	}
	for _, g := range view.Memberships {
		doc.RoleMemberships = append(doc.RoleMemberships, policyMembershipDocument{g.ID, g.Subject, g.Role, g.Domain})
	}
	if adminIfNoneMatchHit(r, AdminETagForRevision(view.Revision)) {
		w.Header().Set("ETag", AdminETagForRevision(view.Revision))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	policyHTTPJSON(w, 200, view.Revision, doc)
}
