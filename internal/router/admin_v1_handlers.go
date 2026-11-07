// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Handler binding for the /admin/v1 contract (ACP-011).
//
// Every registered operation is bound to the handler that already serves the
// legacy route. The handler objects are the same ones the serving port uses: the
// only difference is where the identity and the authorization decision come
// from, which the caller supplies through ConfigAdminDeps/CallerAdminDeps.
//
// The binding is dependency-explicit (rather than reading Service fields) so it
// is exercised directly by the conformance and acceptance tests, and so the
// Service wiring is a one-line call once the #814 authenticator and #812
// authorizer seams are installed on Service.

import (
	"net/http"
	"net/url"
)

// AdminV1Handlers builds the operation→handler map from the config and caller
// handler dependencies plus the service-backed operations (training, metrics,
// content-capture maintenance).
func AdminV1Handlers(configDeps ConfigAdminDeps, callerDeps CallerAdminDeps, extra map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	handlers := make(map[string]http.HandlerFunc, 32)
	for key, handler := range newConfigAdminHandler(configDeps).handlerKeys() {
		handlers[key] = handler
	}
	for key, handler := range newCallerAdminHandler(callerDeps).handlerKeys() {
		handlers[key] = handler
	}
	for key, handler := range extra {
		if handler != nil {
			handlers[key] = handler
		}
	}
	return handlers
}

// AdminV1ServiceHandlers composes the complete operation→handler map: the config
// and caller-directory handlers, the reports adapter over the existing reports
// handler, and the service-backed operations (training, metrics, content-capture
// maintenance). This is the binding the admin listener installs.
func AdminV1ServiceHandlers(registry *AdminV1Registry, configDeps ConfigAdminDeps, callerDeps CallerAdminDeps, reports http.HandlerFunc, extra map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	composed := map[string]http.HandlerFunc{}
	for key, handler := range extra {
		composed[key] = handler
	}
	for key, handler := range AdminV1ReportsHandlers(registry, reports) {
		composed[key] = handler
	}
	return AdminV1Handlers(configDeps, callerDeps, composed)
}

// AdminV1ReportsHandlers binds every reports operation to the existing reports
// handler, rewriting the request path to the operation's legacy path so the
// handler's own branch selection, per-domain authorization and pagination are
// reused verbatim. The reports UI itself is never mounted under /admin/v1.
func AdminV1ReportsHandlers(registry *AdminV1Registry, handler http.HandlerFunc) map[string]http.HandlerFunc {
	out := map[string]http.HandlerFunc{}
	if registry == nil || handler == nil {
		return out
	}
	for _, op := range registry.Operations() {
		if op.Adapter != "reports" || op.LegacyPath == "" {
			continue
		}
		legacyPath := op.LegacyPath
		out[op.HandlerKey] = func(w http.ResponseWriter, r *http.Request) {
			rewritten := r.Clone(r.Context())
			rewritten.URL = &url.URL{Path: legacyPath, RawQuery: r.URL.RawQuery, ForceQuery: r.URL.ForceQuery}
			handler(w, rewritten)
		}
	}
	return out
}

// AdminV1ConfigAdminDeps derives the config handler dependencies for the
// /admin/v1 mount from a base set: Authenticate reads the principal the contract
// layer resolved, and Authorize sends the handler's secondary checks (force,
// shadow) to the single Authorizer.
func AdminV1ConfigAdminDeps(base ConfigAdminDeps, principalOf func(*http.Request) (Principal, string, bool), authorize func(Principal, string, string, string) bool) ConfigAdminDeps {
	out := base
	out.Authenticate = func(w http.ResponseWriter, r *http.Request) (adminAuthSubject, bool) {
		principal, domain, ok := principalOf(r)
		if !ok {
			writeAdminProblemCode(w, r, http.StatusUnauthorized, "authentication_required", "")
			return adminAuthSubject{}, false
		}
		if domain == "" {
			domain = "default"
		}
		return adminAuthSubject{subject: principal.Subject, domain: domain, source: principal.Kind, principal: &principal, ctx: r.Context()}, true
	}
	out.Authorize = func(subject adminAuthSubject, permission string) bool {
		object, action, ok := adminPermissionObjectAction(permission)
		if !ok || subject.principal == nil {
			return false
		}
		if adminV1DecisionCovers(subject.ctx, object, action) {
			// The contract layer already took this exact decision for the request.
			return true
		}
		return authorize(*subject.principal, subject.domain, object, action)
	}
	return out
}

// AdminV1CallerAdminDeps derives the caller directory dependencies for the
// /admin/v1 mount.
func AdminV1CallerAdminDeps(base CallerAdminDeps, principalOf func(*http.Request) (Principal, string, bool), authorize func(Principal, string, string, string) bool) CallerAdminDeps {
	out := base
	out.Authorize = func(r *http.Request, object, action string) bool {
		principal, domain, ok := principalOf(r)
		if !ok {
			return false
		}
		if domain == "" {
			domain = "default"
		}
		if adminV1DecisionCovers(r.Context(), object, action) {
			// The contract layer already took this exact decision for the request.
			return true
		}
		return authorize(principal, domain, object, action)
	}
	return out
}
