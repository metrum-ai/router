// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import "net/http"

func policyLifecycleOperationSpecs() []Operation {
	var out []Operation
	for _, operation := range []string{"activate", "rollback"} {
		out = append(out, Operation{ID: operation + "PolicySet", Method: "POST", Path: "/admin/v1/policy-sets/{set_id}/" + operation, Summary: operation + " a guarded policy version", Object: "admin:policy", Action: operation, Domain: AdminDomainGlobal, Request: struct{}{}, Response: policyMutationDocument{}, Errors: []int{403, 404, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore, RateClass: AdminRateClassWrite, HandlerKey: "policy.set." + operation})
	}
	return out
}
func (h *policyAdminHandler) lifecycle(w http.ResponseWriter, r *http.Request, operation string) {
	if r.ContentLength != 0 {
		var body struct{}
		if !policyHTTPDecode(w, r, &body) {
			return
		}
	}
	h.mutate(w, r, policyResourceCommand{Kind: operation, SetID: r.PathValue("set_id")}, http.StatusOK)
}
func (h *policyAdminHandler) activate(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "activate")
}
func (h *policyAdminHandler) rollback(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "rollback")
}
