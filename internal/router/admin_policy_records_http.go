// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"net/http"
	"sort"
	"strconv"
)

type policyRuleListDocument struct {
	Items    []policyRuleDocument `json:"items"`
	Revision int64                `json:"revision"`
}
type policyMembershipListDocument struct {
	Items    []policyMembershipDocument `json:"items"`
	Revision int64                      `json:"revision"`
}

func policyRecordOperationSpecs() []Operation {
	var out []Operation
	for _, kind := range []struct {
		route, key, label             string
		request, response, collection any
	}{
		{"rules", "rules", "PolicyRule", policyRuleInput{}, policyRuleDocument{}, policyRuleListDocument{}},
		{"role-memberships", "memberships", "PolicyRoleMembership", policyMembershipInput{}, policyMembershipDocument{}, policyMembershipListDocument{}},
	} {
		path := "/admin/v1/policy-sets/{set_id}/" + kind.route
		for _, verb := range []struct {
			method, suffix, action, description string
			collection                          bool
		}{
			{"GET", "list", "read", "List", true}, {"GET", "get", "read", "Get", false}, {"POST", "create", "write", "Create", true}, {"PUT", "replace", "write", "Replace", false}, {"DELETE", "delete", "write", "Delete", false},
		} {
			op := Operation{ID: verb.suffix + kind.label, Method: verb.method, Path: path, Object: "admin:policy", Action: verb.action, Domain: AdminDomainGlobal, Summary: verb.description + " " + kind.label, HandlerKey: "policy." + kind.key + "." + verb.suffix, Errors: []int{404, 503}}
			if !verb.collection {
				op.Path += "/{record_id}"
			}
			if verb.action == "read" {
				op.NoSideEffect = true
				op.RateClass = AdminRateClassRead
				op.Response = kind.response
				if verb.collection {
					op.ID += "s"
					op.Response = kind.collection
					op.Pagination = &AdminPaginationSpec{Sorts: []string{"id"}, DefaultSort: "id"}
				}
			} else {
				op.RateClass = AdminRateClassWrite
				if verb.method == "POST" {
					op.SuccessStatus = http.StatusCreated
				}
				op.Concurrency = AdminConcurrencyRevision
				op.Idempotency = AdminIdempotencyStore
				op.Response = policyMutationDocument{}
				op.Errors = []int{403, 404, 409, 412, 422, 428, 503}
				if verb.method != "DELETE" {
					op.Request = kind.request
				}
			}
			out = append(out, op)
		}
	}
	return out
}

func (h *policyAdminHandler) recordHandlers() map[string]http.HandlerFunc {
	out := map[string]http.HandlerFunc{}
	for _, kind := range []struct {
		key, command string
		rule         bool
	}{{"rules", "rule", true}, {"memberships", "membership", false}} {
		for _, action := range []string{"list", "get", "create", "replace", "delete"} {
			out["policy."+kind.key+"."+action] = func(w http.ResponseWriter, r *http.Request) {
				var id uint
				if action == "get" || action == "replace" || action == "delete" {
					parsed, err := strconv.ParseUint(r.PathValue("record_id"), 10, strconv.IntSize)
					if err != nil || parsed == 0 {
						policyHTTPProblem(w, r, errPolicyResourceNotFound)
						return
					}
					id = uint(parsed)
				}
				if action == "list" || action == "get" {
					h.readRecords(w, r, kind.rule, action == "list", id)
					return
				}
				var fields []string
				if action != "delete" {
					if kind.rule {
						var body policyRuleInput
						if !policyHTTPDecode(w, r, &body) {
							return
						}
						fields = []string{"p", body.SubjectOrRole, body.Domain, body.Object, body.Action}
					} else {
						var body policyMembershipInput
						if !policyHTTPDecode(w, r, &body) {
							return
						}
						fields = []string{"g", body.Subject, body.Role, body.Domain}
					}
				}
				status := http.StatusOK
				if action == "create" {
					status = http.StatusCreated
				}
				h.mutate(w, r, policyResourceCommand{Kind: action + "-" + kind.command, SetID: r.PathValue("set_id"), RecordID: id, Fields: fields}, status)
			}
		}
	}
	return out
}

func policyRecordPage[T any](w http.ResponseWriter, r *http.Request, items []T, id func(T) uint) ([]T, bool) {
	page, _ := AdminPageFrom(r.Context())
	descending := page.Descending()
	sort.Slice(items, func(i, j int) bool {
		if descending {
			return id(items[i]) > id(items[j])
		}
		return id(items[i]) < id(items[j])
	})
	values := page.CursorValues()
	var cursor uint64
	if len(values) > 0 {
		var err error
		cursor, err = strconv.ParseUint(values[0], 10, 64)
		if err != nil {
			writeAdminProblemCode(w, r, 400, "invalid-cursor", "")
			return nil, false
		}
	}
	filtered := make([]T, 0, len(items))
	for _, item := range items {
		n := uint64(id(item))
		if len(values) > 0 && ((!descending && n <= cursor) || (descending && n >= cursor)) {
			continue
		}
		filtered = append(filtered, item)
	}
	limit := page.LimitOr(100)
	if len(filtered) > limit {
		filtered = filtered[:limit]
		if page != nil {
			page.PublishNext(strconv.FormatUint(uint64(id(filtered[len(filtered)-1])), 10))
		}
	}
	return filtered, true
}
func (h *policyAdminHandler) readRecords(w http.ResponseWriter, r *http.Request, rule, list bool, id uint) {
	view, err := h.store.getPolicyResourceContext(r.Context(), r.PathValue("set_id"))
	if err != nil {
		policyHTTPProblem(w, r, err)
		return
	}
	if rule {
		items := make([]policyRuleDocument, 0, len(view.Rules))
		for _, p := range view.Rules {
			doc := policyRuleDocument{p.ID, p.SubjectOrRole, p.Domain, p.Object, p.Action}
			if !list && p.ID == id {
				policyHTTPJSON(w, 200, view.Revision, doc)
				return
			}
			items = append(items, doc)
		}
		if list {
			items, ok := policyRecordPage(w, r, items, func(p policyRuleDocument) uint { return p.ID })
			if ok {
				policyHTTPJSON(w, 200, view.Revision, policyRuleListDocument{items, view.Revision})
			}
			return
		}
	} else {
		items := make([]policyMembershipDocument, 0, len(view.Memberships))
		for _, g := range view.Memberships {
			doc := policyMembershipDocument{g.ID, g.Subject, g.Role, g.Domain}
			if !list && g.ID == id {
				policyHTTPJSON(w, 200, view.Revision, doc)
				return
			}
			items = append(items, doc)
		}
		if list {
			items, ok := policyRecordPage(w, r, items, func(g policyMembershipDocument) uint { return g.ID })
			if ok {
				policyHTTPJSON(w, 200, view.Revision, policyMembershipListDocument{items, view.Revision})
			}
			return
		}
	}
	policyHTTPProblem(w, r, errPolicyResourceNotFound)
}
