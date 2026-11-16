// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// adminImplicitPermissionsAcrossDomains works around Casbin's implicit
// permission API requiring an explicit domain for domain-scoped role links.
func adminImplicitPermissionsAcrossDomains(enforcer interface {
	GetNamedPolicy(string) ([][]string, error)
	GetNamedGroupingPolicy(string) ([][]string, error)
	GetImplicitPermissionsForUser(string, ...string) ([][]string, error)
}, subject string) ([][]string, error) {
	domains := map[string]struct{}{AdminGlobalDomain: {}}
	policies, err := enforcer.GetNamedPolicy("p")
	if err != nil {
		return nil, err
	}
	for _, row := range policies {
		if len(row) > 1 && row[1] != "" {
			domains[row[1]] = struct{}{}
		}
	}
	links, err := enforcer.GetNamedGroupingPolicy("g")
	if err != nil {
		return nil, err
	}
	for _, row := range links {
		if len(row) > 2 && row[2] != "" {
			domains[row[2]] = struct{}{}
		}
	}
	out := [][]string{}
	seen := map[string]struct{}{}
	for domain := range domains {
		permissions, e := enforcer.GetImplicitPermissionsForUser(subject, domain)
		if e != nil {
			return nil, e
		}
		for _, permission := range permissions {
			key := strings.Join(permission, "\x00")
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				out = append(out, permission)
			}
		}
	}
	return out, nil
}

const adminClientsObject = "admin:clients"

func adminClientOperations() []Operation {
	return []Operation{
		{ID: "listAdminClients", Method: "GET", Path: "/admin/v1/clients", Summary: "List machine clients", Object: adminClientsObject, Action: "list", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"clients"}}, RateClass: AdminRateClassRead, HandlerKey: "admin.clients.list"},
		{ID: "getAdminClient", Method: "GET", Path: "/admin/v1/clients/{client}", Summary: "Read one machine client", Object: adminClientsObject, Action: "get", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"client"}}, Errors: []int{404}, RateClass: AdminRateClassRead, HandlerKey: "admin.clients.get"},
		{ID: "createAdminClient", Method: "POST", Path: "/admin/v1/clients", Summary: "Register a machine client", Object: adminClientsObject, Action: "create", Domain: AdminDomainGlobal, Request: adminClientCreateBody{}, Response: adminOpenObject{Members: []string{"client"}}, SuccessStatus: 201, Errors: []int{400, 409}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.create"},
		{ID: "getAdminClientAccessReview", Method: "GET", Path: "/admin/v1/clients/access-review", Summary: "Export client credentials and effective grants", Object: adminClientsObject, Action: "review", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"clients", "generatedAt"}}, RateClass: AdminRateClassRead, HandlerKey: "admin.clients.review"},
		{ID: "updateAdminClient", Method: "PUT", Path: "/admin/v1/clients/{client}", Summary: "Update a machine client", Object: adminClientsObject, Action: "update", Domain: AdminDomainGlobal, Request: adminClientUpdateBody{}, Response: adminOpenObject{Members: []string{"status"}}, Errors: []int{400, 404, 409}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.update"},
		{ID: "disableAdminClient", Method: "POST", Path: "/admin/v1/clients/{client}/disable", Summary: "Disable a machine client and revoke its credentials", Object: adminClientsObject, Action: "disable", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"status"}}, Errors: []int{404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.disable"},
		{ID: "enableAdminClient", Method: "POST", Path: "/admin/v1/clients/{client}/enable", Summary: "Re-enable a client to register replacement credentials", Object: adminClientsObject, Action: "enable", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"status"}}, Errors: []int{404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.enable"},
		{ID: "deleteAdminClient", Method: "DELETE", Path: "/admin/v1/clients/{client}", Summary: "Delete a machine client", Object: adminClientsObject, Action: "delete", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"status"}}, Errors: []int{404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.delete"},
		{ID: "registerAdminClientCredential", Method: "POST", Path: "/admin/v1/clients/{client}/credentials", Summary: "Register credential metadata for a machine client", Object: adminClientsObject, Action: "register", Domain: AdminDomainGlobal, Request: adminClientCredentialBody{}, Response: adminOpenObject{Members: []string{"credential"}}, SuccessStatus: 201, Errors: []int{400, 404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.credentials.register"},
		{ID: "revokeAdminClientCredential", Method: "DELETE", Path: "/admin/v1/clients/{client}/credentials/{credential}", Summary: "Revoke one client credential", Object: adminClientsObject, Action: "revoke", Domain: AdminDomainGlobal, Response: adminOpenObject{Members: []string{"status"}}, Errors: []int{404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.clients.credentials.revoke"},
	}
}

func init() { adminV1OperationSpecs = append(adminV1OperationSpecs, adminClientOperations()...) }

func init() {
	for _, spec := range []struct {
		code   string
		status int
		title  string
	}{
		{"admin-client-invalid-request", http.StatusBadRequest, "Invalid admin client request"},
		{"admin-client-not-found", http.StatusNotFound, "Admin client not found"},
		{"admin-client-conflict", http.StatusConflict, "Admin client conflict"},
		{"admin-client-store-unavailable", http.StatusServiceUnavailable, "Admin client store unavailable"},
		{"admin-client-guard-unavailable", http.StatusServiceUnavailable, "Admin client policy guard unavailable"},
		{"admin-client-review-unavailable", http.StatusServiceUnavailable, "Admin client access review unavailable"},
	} {
		RegisterAdminProblemCode(spec.code, spec.status, spec.title)
	}
}

type adminClientCreateBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type adminClientUpdateBody struct {
	Name string `json:"name"`
}
type adminClientCredentialBody struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Identity  string    `json:"identity"`
	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) adminClientHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"admin.clients.list": s.handleAdminClientsList, "admin.clients.get": s.handleAdminClientGet, "admin.clients.create": s.handleAdminClientCreate, "admin.clients.review": s.handleAdminClientReview,
		"admin.clients.update": s.handleAdminClientUpdate, "admin.clients.disable": s.handleAdminClientDisable, "admin.clients.enable": s.handleAdminClientEnable, "admin.clients.delete": s.handleAdminClientDelete,
		"admin.clients.credentials.register": s.handleAdminClientCredentialRegister,
		"admin.clients.credentials.revoke":   s.handleAdminClientCredentialRevoke,
	}
}

func (s *Service) adminClientStoreRuntime() *adminClientStore {
	if s == nil {
		return nil
	}
	return s.adminClients
}

func readAdminClientBody[T any](w http.ResponseWriter, r *http.Request, out *T) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		adminKeyProblem(w, r, http.StatusBadRequest, "admin-client-invalid-request")
		return false
	}
	return true
}

func adminClientStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAdminClientInvalidRequest):
		adminKeyProblem(w, r, http.StatusBadRequest, "admin-client-invalid-request")
	case errors.Is(err, errAdminClientNotFound):
		adminKeyProblem(w, r, http.StatusNotFound, "admin-client-not-found")
	case errors.Is(err, errAdminClientConflict):
		adminKeyProblem(w, r, http.StatusConflict, "admin-client-conflict")
	default:
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
	}
}

func adminClientAuditActor(r *http.Request) (string, string) {
	principal, _ := PrincipalFrom(r.Context())
	requestID := AdminRequestIDFrom(r)
	if requestID == "" {
		requestID = AdminRequestID(r)
	}
	return adminAuditFieldValue(principal.Subject), requestID
}

func (s *Service) handleAdminClientsList(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	clients, err := store.list(r.Context())
	if err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

func (s *Service) handleAdminClientGet(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	client, err := store.get(r.Context(), r.PathValue("client"))
	if err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"client": client})
}

func (s *Service) handleAdminClientCreate(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	var body adminClientCreateBody
	if !readAdminClientBody(w, r, &body) {
		return
	}
	client := adminClient{ID: body.ID, Name: body.Name}
	actor, requestID := adminClientAuditActor(r)
	if err := store.create(r.Context(), client, actor, requestID); err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusCreated, map[string]any{"client": client})
}

func (s *Service) handleAdminClientUpdate(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	var body adminClientUpdateBody
	if !readAdminClientBody(w, r, &body) {
		return
	}
	actor, requestID := adminClientAuditActor(r)
	if err := store.update(r.Context(), r.PathValue("client"), body.Name, actor, requestID); err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"status": "active"})
}

func (s *Service) handleAdminClientDisable(w http.ResponseWriter, r *http.Request) {
	s.setAdminClientStatus(w, r, "disabled")
}
func (s *Service) handleAdminClientEnable(w http.ResponseWriter, r *http.Request) {
	s.setAdminClientStatus(w, r, "active")
}
func (s *Service) handleAdminClientDelete(w http.ResponseWriter, r *http.Request) {
	s.setAdminClientStatus(w, r, "deleted")
}
func (s *Service) setAdminClientStatus(w http.ResponseWriter, r *http.Request, status string) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	clientID := r.PathValue("client")
	if status == "disabled" || status == "deleted" {
		unlock, err := s.lockAdminClientPolicyChange(r.Context(), clientID, "", nil, time.Time{})
		if err != nil {
			adminClientGuardFailure(w, r, err)
			return
		}
		defer unlock()
	}
	actor, requestID := adminClientAuditActor(r)
	if err := store.setStatus(r.Context(), clientID, status, actor, requestID); err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (s *Service) handleAdminClientCredentialRegister(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	var body adminClientCredentialBody
	if !readAdminClientBody(w, r, &body) {
		return
	}
	credential := adminClientCredential{ID: body.ID, Kind: body.Kind, Identity: body.Identity, Subject: body.Subject, ExpiresAt: body.ExpiresAt}
	if !s.adminClientCredentialExists(credential) {
		adminClientStoreError(w, r, errAdminClientInvalidRequest)
		return
	}
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		adminClientGuardFailure(w, r, ErrAuthorizationUnavailable)
		return
	}
	if err := s.adminClientActorCoversClientPermissions(r.Context(), principal.Subject, r.PathValue("client"), &credential); err != nil {
		adminClientGuardFailure(w, r, err)
		return
	}
	if credential.ExpiresAt.Before(time.Now().UTC().Add(30 * 24 * time.Hour)) {
		unlock, err := s.lockAdminClientPolicyChange(r.Context(), r.PathValue("client"), "", &credential, credential.ExpiresAt)
		if err != nil {
			adminClientGuardFailure(w, r, err)
			return
		}
		defer unlock()
	}
	actor, requestID := adminClientAuditActor(r)
	if err := store.registerCredentialWithAudit(r.Context(), r.PathValue("client"), credential, actor, requestID); err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusCreated, map[string]any{"credential": credential})
}

func (s *Service) handleAdminClientCredentialRevoke(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	unlock, err := s.lockAdminClientPolicyChange(r.Context(), r.PathValue("client"), r.PathValue("credential"), nil, time.Time{})
	if err != nil {
		adminClientGuardFailure(w, r, err)
		return
	}
	defer unlock()
	actor, requestID := adminClientAuditActor(r)
	if err := store.revokeCredential(r.Context(), r.PathValue("client"), r.PathValue("credential"), actor, requestID); err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (s *Service) handleAdminClientReview(w http.ResponseWriter, r *http.Request) {
	store := s.adminClientStoreRuntime()
	if store == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-store-unavailable")
		return
	}
	clients, err := store.list(r.Context())
	if err != nil {
		adminClientStoreError(w, r, err)
		return
	}
	generation := s.machineGeneration.Load()
	if generation == nil || generation.authorizer == nil || generation.authorizer.enforcer == nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-review-unavailable")
		return
	}
	now := time.Now().UTC()
	grantCache := make(map[string][][]string)
	for i := range clients {
		for j := range clients[i].Credentials {
			cred := &clients[i].Credentials[j]
			cred.Unused = cred.LastUsedAt == nil
			cred.ExpiresSoon = cred.ExpiresAt.After(time.Now().UTC()) && cred.ExpiresAt.Before(time.Now().UTC().Add(30*24*time.Hour))
			if clients[i].Status != "active" || cred.Status != "active" || !cred.ExpiresAt.After(now) {
				cred.EffectiveGrants = [][]string{}
				continue
			}
			grants, ok := grantCache[cred.Subject]
			if !ok {
				var e error
				grants, e = adminImplicitPermissionsAcrossDomains(generation.authorizer.enforcer, cred.Subject)
				if e != nil {
					adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-client-review-unavailable")
					return
				}
				grantCache[cred.Subject] = grants
			}
			cred.EffectiveGrants = grants
		}
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"clients": clients, "generatedAt": time.Now().UTC()})
}
