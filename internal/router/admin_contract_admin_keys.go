// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// #815 admin-key lifecycle routes on the #811 contract: policy-owned
// authorization on the shared global domain with the canonical `admin:keys`
// object, ETag/If-Match preconditions on mutations, pagination on list, and
// handler-owned idempotency for the one-time secret responses.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func writeAdminJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func contextWithoutCancel() context.Context { return context.Background() }

func adminHandlerRequestHash(method, path string) string {
	digest := sha256.Sum256([]byte(method + "\x00" + path))
	return hex.EncodeToString(digest[:])
}

func (s *Service) adminKeyMaxLifetime() time.Duration {
	if s != nil && s.adminKeyCfgMaxLifetime > 0 {
		return s.adminKeyCfgMaxLifetime
	}
	return adminKeyDefaultMaxLife
}

func (s *Service) adminKeyDefaultOverlap() time.Duration {
	if s != nil && s.adminKeyCfgDefaultOverlap >= 0 && s.adminKeyCfgDefaultOverlapSet {
		return s.adminKeyCfgDefaultOverlap
	}
	return adminKeyDefaultOverlap
}

func (s *Service) adminKeyMaxRotationOverlap() time.Duration {
	if s != nil && s.adminKeyCfgMaxOverlap > 0 {
		return s.adminKeyCfgMaxOverlap
	}
	return adminKeyMaxOverlap
}

const (
	adminKeyObject       = "admin:keys"
	adminKeyActionIssue  = "issue"
	adminKeyActionList   = "list"
	adminKeyActionGet    = "get"
	adminKeyActionRotate = "rotate"
	adminKeyActionRevoke = "revoke"
)

const (
	adminKeyProblemNotFound      = "admin-key-not-found"
	adminKeyProblemGone          = "admin-key-revoked"
	adminKeyProblemOverlapActive = "admin-key-overlap-active"
	adminKeyProblemSecretUnavail = "one-time-secret-unavailable"
	adminKeyProblemIdempotency   = "admin-key-idempotency-conflict"
	adminKeyProblemInvalid       = "admin-key-invalid-request"
)

func adminKeyOperations() []Operation {
	return []Operation{
		{ID: "issueAdminKey", Method: "POST", Path: "/admin/v1/admin-keys",
			Summary: "Issue an admin API key", Object: adminKeyObject, Action: adminKeyActionIssue, Domain: AdminDomainGlobal,
			Request: adminKeyIssueBody{}, Response: adminOpenObject{Members: []string{"id", "subject", "generation", "secret", "expires_at", "revision"}},
			Errors: []int{409, 412, 428}, Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
			SensitiveResponse: true, SuccessStatus: 201, RateClass: AdminRateClassWrite, HandlerKey: "admin.keys.issue"},
		{ID: "listAdminKeys", Method: "GET", Path: "/admin/v1/admin-keys",
			Summary: "List admin API keys", Object: adminKeyObject, Action: adminKeyActionList, Domain: AdminDomainGlobal,
			Response:   adminOpenObject{Members: []string{"keys", "pagination", "revision"}},
			Pagination: &AdminPaginationSpec{Sorts: []string{"id", "created_at", "expires_at", "status"}, DefaultSort: "id"}, RateClass: AdminRateClassRead, HandlerKey: "admin.keys.list"},
		{ID: "getAdminKey", Method: "GET", Path: "/admin/v1/admin-keys/{id}",
			Summary: "Read one admin API key", Object: adminKeyObject, Action: adminKeyActionGet, Domain: AdminDomainGlobal,
			Response: adminOpenObject{Members: []string{"id", "subject", "status", "expires_at", "created_at", "revoked_at", "generations", "revision"}},
			Errors:   []int{404}, RateClass: AdminRateClassRead, HandlerKey: "admin.keys.get"},
		{ID: "rotateAdminKey", Method: "POST", Path: "/admin/v1/admin-keys/{id}/rotate",
			Summary: "Rotate an admin API key with overlap", Object: adminKeyObject, Action: adminKeyActionRotate, Domain: AdminDomainGlobal,
			Request: adminKeyRotateBody{}, Response: adminOpenObject{Members: []string{"id", "subject", "generation", "secret", "expires_at", "overlap_until", "revision"}},
			Errors: []int{404, 409, 412, 428}, Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
			SensitiveResponse: true, SuccessStatus: 201, RateClass: AdminRateClassWrite, HandlerKey: "admin.keys.rotate"},
		{ID: "revokeAdminKey", Method: "POST", Path: "/admin/v1/admin-keys/{id}/revoke",
			Summary: "Revoke an admin API key", Object: adminKeyObject, Action: adminKeyActionRevoke, Domain: AdminDomainGlobal,
			Response: adminOpenObject{Members: []string{"id", "status", "revoked_at", "revision"}},
			Errors:   []int{404, 409, 412, 428}, Concurrency: AdminConcurrencyRevision,
			Idempotency: AdminIdempotencyStore, RateClass: AdminRateClassWrite, HandlerKey: "admin.keys.revoke"},
	}
}

func init() { adminV1OperationSpecs = append(adminV1OperationSpecs, adminKeyOperations()...) }

type adminKeyIssueBody struct {
	ExpiresAt string `json:"expires_at"`
}
type adminKeyRotateBody struct {
	Overlap string `json:"overlap,omitempty"`
}

func (s *Service) adminKeyStoreRuntime() (*adminKeyStore, error) {
	if s == nil || s.configControlPlane == nil {
		return nil, errors.New("admin key store requires the control-plane database")
	}
	if s.adminKeys == nil {
		return nil, errors.New("admin key store unavailable")
	}
	return s.adminKeys, nil
}

// adminKeyHandlers returns the contract handler bindings.
func (s *Service) adminKeyHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"admin.keys.issue":  s.handleAdminKeyIssue,
		"admin.keys.list":   s.handleAdminKeyList,
		"admin.keys.get":    s.handleAdminKeyGet,
		"admin.keys.rotate": s.handleAdminKeyRotate,
		"admin.keys.revoke": s.handleAdminKeyRevoke,
	}
}

func adminKeyProblem(w http.ResponseWriter, r *http.Request, status int, code string) {
	WriteAdminProblem(w, r, AdminProblemFor(r, status, code))
}

func (s *Service) handleAdminKeyIssue(w http.ResponseWriter, r *http.Request) {
	store, err := s.adminKeyStoreRuntime()
	if err != nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-key-store-unavailable")
		return
	}
	var body adminKeyIssueBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		adminKeyProblem(w, r, http.StatusBadRequest, adminKeyProblemInvalid)
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		adminKeyProblem(w, r, http.StatusBadRequest, adminKeyProblemInvalid)
		return
	}
	idempotencyKey, requestHash := adminHandlerIdempotency(r)
	issued, err := store.issueIdempotent(r.Context(), idempotencyKey, requestHash,
		adminKeyIssueRequest{ExpiresAt: expiresAt, MaxLifetime: s.adminKeyMaxLifetime()})
	if err != nil {
		adminKeyStoreProblem(w, r, err)
		return
	}
	writeAdminKeySecret(w, http.StatusCreated, issued, store)
}

func (s *Service) handleAdminKeyRotate(w http.ResponseWriter, r *http.Request) {
	store, err := s.adminKeyStoreRuntime()
	if err != nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-key-store-unavailable")
		return
	}
	var body adminKeyRotateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil && err.Error() != "EOF" {
		adminKeyProblem(w, r, http.StatusBadRequest, adminKeyProblemInvalid)
		return
	}
	overlap := s.adminKeyDefaultOverlap()
	if body.Overlap != "" {
		parsed, err := time.ParseDuration(body.Overlap)
		if err != nil || parsed < 0 || parsed > s.adminKeyMaxRotationOverlap() {
			adminKeyProblem(w, r, http.StatusBadRequest, adminKeyProblemInvalid)
			return
		}
		overlap = parsed
	}
	rotated, err := store.rotate(r.Context(), r.PathValue("id"), adminKeyRotateRequest{Overlap: overlap})
	if err != nil {
		adminKeyStoreProblem(w, r, err)
		return
	}
	writeAdminKeySecret(w, http.StatusCreated, rotated, store)
}

func (s *Service) handleAdminKeyList(w http.ResponseWriter, r *http.Request) {
	store, err := s.adminKeyStoreRuntime()
	if err != nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-key-store-unavailable")
		return
	}
	list, err := store.list(r.Context(), adminKeyListRequest{})
	if err != nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-key-store-unavailable")
		return
	}
	limit, offset := adminKeyPagination(r)
	keys := list.Keys
	if offset > len(keys) {
		offset = len(keys)
	}
	keys = keys[offset:]
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"keys":       keys,
		"revision":   list.Revision,
		"pagination": map[string]any{"limit": limit, "offset": offset, "total": len(list.Keys)},
	})
}

func (s *Service) handleAdminKeyGet(w http.ResponseWriter, r *http.Request) {
	store, err := s.adminKeyStoreRuntime()
	if err != nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-key-store-unavailable")
		return
	}
	key, err := store.get(r.Context(), r.PathValue("id"))
	if err != nil {
		adminKeyStoreProblem(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, key)
}

func (s *Service) handleAdminKeyRevoke(w http.ResponseWriter, r *http.Request) {
	store, err := s.adminKeyStoreRuntime()
	if err != nil {
		adminKeyProblem(w, r, http.StatusServiceUnavailable, "admin-key-store-unavailable")
		return
	}
	id := r.PathValue("id")
	if err := store.revoke(r.Context(), id); err != nil {
		adminKeyStoreProblem(w, r, err)
		return
	}
	key, err := store.get(r.Context(), id)
	if err != nil {
		adminKeyStoreProblem(w, r, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"id": key.ID, "status": key.Status, "revoked_at": key.RevokedAt, "revision": key.Revision})
}

func adminKeyStoreProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAdminKeyAbsent):
		adminKeyProblem(w, r, http.StatusNotFound, adminKeyProblemNotFound)
	case errors.Is(err, errAdminKeyRevoked):
		adminKeyProblem(w, r, http.StatusConflict, adminKeyProblemGone)
	case errors.Is(err, errAdminKeyOverlapActive):
		adminKeyProblem(w, r, http.StatusConflict, adminKeyProblemOverlapActive)
	case errors.Is(err, errAdminKeySecretUnavailable):
		adminKeyProblem(w, r, http.StatusConflict, adminKeyProblemSecretUnavail)
	case errors.Is(err, errAdminKeyIdempotencyConflict):
		adminKeyProblem(w, r, http.StatusConflict, adminKeyProblemIdempotency)
	default:
		adminKeyProblem(w, r, http.StatusBadRequest, adminKeyProblemInvalid)
	}
}

// writeAdminKeySecret emits the one-time secret exactly once; the same DTO is
// never used for reads, and the replay store never records this body.
func writeAdminKeySecret(w http.ResponseWriter, status int, issued adminKeySecret, store *adminKeyStore) {
	revision := int64(0)
	if key, err := store.get(contextWithoutCancel(), issued.ID); err == nil {
		revision = key.Revision
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAdminJSON(w, status, map[string]any{"id": issued.ID, "subject": issued.Subject, "generation": issued.Generation,
		"secret": issued.Secret, "expires_at": issued.ExpiresAt, "revision": revision})
}

func adminKeyPagination(r *http.Request) (limit, offset int) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

// adminHandlerIdempotency returns the caller's Idempotency-Key with a stable
// digest of the request body; an absent key disables replay protection.
func adminHandlerIdempotency(r *http.Request) (string, string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return "", ""
	}
	return key, adminHandlerRequestHash(r.Method, r.URL.Path)
}
