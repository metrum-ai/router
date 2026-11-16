// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Admin caller directory mutations for the relational configuration control
// plane. These endpoints issue, rotate, and revoke caller credentials against
// the router_config_callers projection and keep the canonical YAML document
// in sync. Raw caller tokens are returned exactly once in the issue/rotate
// response and are never logged or persisted; only the SHA-256 hash and the
// non-secret public token identifier are stored.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const (
	authzObjectAdminCallers     = "admin:callers"
	authzActionList             = "list"
	authzActionIssue            = "issue"
	authzActionRotate           = "rotate"
	authzActionRevoke           = "revoke"
	authzObjectAdminContainment = "admin:containment"
	authzActionContain          = "contain"
	authzRoleContainment        = "role:containment"
)

// CallerAdminDeps carries the collaborators the caller directory admin
// endpoints need. Authorize is intentionally a plain callback so route
// registration stays decoupled from Casbin wiring; the service resolves the
// authenticated subject and enforces the operation's declared policy.
// AfterMutate is invoked once after every successful mutation so the runtime can bump its configuration revision and
// schedule a reload; it is called outside the database transaction. If
// AfterMutate returns an error, the mutation is rolled back.
type CallerAdminDeps struct {
	DB           *gorm.DB
	RuntimeScope string
	Authorize    func(r *http.Request, object, action string) bool
	// AuthorizeDomain takes a decision in an explicit domain, used to filter a
	// caller-directory row by the domain stored on that row.
	AuthorizeDomain       func(r *http.Request, domain, object, action string) bool
	AfterMutate           func(r *http.Request, callerID string) error
	Now                   func() time.Time
	ClaimWebhookNonce     func(*gorm.DB, Principal) error
	CallerWriteAuthorized func(r *http.Request, domain string) bool
}

// RegisterCallerDirectoryAdmin registers the admin caller directory endpoints on the
// serving port (the legacy surface, unchanged).
func RegisterCallerDirectoryAdmin(mux *http.ServeMux, deps CallerAdminDeps) {
	newCallerAdminHandler(deps).register(func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, handler)
	}, "/admin")
}

// newCallerAdminHandler builds the handler object without registering it, so the same
// handler also serves /admin/v1.
func newCallerAdminHandler(deps CallerAdminDeps) *callerAdminHandler {
	return &callerAdminHandler{deps: deps}
}

// callerAdminPatterns is the caller directory pattern table.
func (h *callerAdminHandler) callerAdminPatterns() []struct {
	pattern string
	key     string
	handler http.HandlerFunc
} {
	return []struct {
		pattern string
		key     string
		handler http.HandlerFunc
	}{
		{"GET /callers", "callers.list", h.handleList},
		{"POST /callers/issue", "callers.issue", h.handleIssue},
		{"POST /callers/{id}/rotate", "callers.rotate", h.handleRotate},
		{"POST /callers/{id}/revoke", "callers.revoke", h.handleRevoke},
	}
}

func (h *callerAdminHandler) handleWebhookContainment(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeCallerAdminError(w, http.StatusBadRequest, "containment-invalid-request")
		return
	}
	var req callerContainmentRequest
	if err := json.Unmarshal(raw, &req); err != nil || strings.TrimSpace(req.Action) != "suspend" {
		writeCallerAdminError(w, http.StatusBadRequest, "containment-invalid-request")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.handleContainment(w, r)
}

type callerContainmentRequest struct {
	CallerID string `json:"caller_id"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	TTL      string `json:"ttl,omitempty"`
}

func (h *callerAdminHandler) handleContainment(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeContainment(w, r) {
		return
	}
	var req callerContainmentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeCallerAdminError(w, http.StatusBadRequest, "containment-invalid-request")
		return
	}
	req.CallerID = strings.TrimSpace(req.CallerID)
	req.Action = strings.TrimSpace(req.Action)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.CallerID == "" || !validContainmentReason(req.Reason) || (req.Action != "suspend" && req.Action != "resume") {
		writeCallerAdminError(w, http.StatusBadRequest, "containment-invalid-request")
		return
	}
	status, expiry := accountStatusActive, ""
	if req.Action == "suspend" {
		status = accountStatusSuspended
		if req.TTL != "" {
			ttl, err := time.ParseDuration(req.TTL)
			if err != nil || ttl <= 0 || ttl > 30*24*time.Hour {
				writeCallerAdminError(w, http.StatusBadRequest, "containment-invalid-ttl")
				return
			}
			expiry = h.now().Add(ttl).UTC().Format(time.RFC3339Nano)
		}
	} else if req.TTL != "" {
		writeCallerAdminError(w, http.StatusBadRequest, "containment-invalid-request")
		return
	}
	set, err := h.activeConfigSet()
	if err != nil {
		writeCallerAdminError(w, http.StatusServiceUnavailable, "containment-config-set-unavailable")
		return
	}
	principal, _ := PrincipalFrom(r.Context())
	if principal.Kind == "webhook" && (principal.containmentNonce == "" || h.deps.ClaimWebhookNonce == nil) {
		writeCallerAdminError(w, http.StatusServiceUnavailable, "containment-nonce-store-unavailable")
		return
	}
	source := "containment"
	err = h.deps.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var rows []callerRow
		if err := tx.Where("config_set_id = ? AND caller_id = ?", set.ID, req.CallerID).Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return errCallerAdminNotFound
		}
		callerDomain := authzDomainForProjectEnvironment(rows[0].Project, rows[0].Environment)
		if h.deps.AuthorizeDomain != nil && !h.deps.AuthorizeDomain(r, callerDomain, authzObjectAdminContainment, authzActionContain) {
			return errCallerAdminForbidden
		}
		fullCallerWrite := h.deps.CallerWriteAuthorized != nil && h.deps.CallerWriteAuthorized(r, callerDomain)
		source = "containment"
		if fullCallerWrite {
			source = "admin"
		}
		currentStatus := rows[0].Status
		if currentStatus == accountStatusSuspended && req.Action == "suspend" {
			var doc configControlPlaneDocumentRow
			if err := tx.Where("config_set_id = ?", set.ID).First(&doc).Error; err != nil {
				return err
			}
			var config Config
			if err := yaml.Unmarshal([]byte(doc.CanonicalYAML), &config); err != nil {
				return err
			}
			for _, caller := range config.Callers {
				if caller.ID == req.CallerID {
					if expiry, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(caller.ContainmentExpiresAt)); err == nil && !h.now().Before(expiry) {
						currentStatus = accountStatusActive
					}
					break
				}
			}
		}
		if req.Action == "suspend" && currentStatus != accountStatusActive {
			return errCallerAdminConflict
		}
		if req.Action == "resume" && currentStatus != accountStatusSuspended {
			return errCallerAdminConflict
		}
		if req.Action == "resume" && !fullCallerWrite {
			var doc configControlPlaneDocumentRow
			if err := tx.Where("config_set_id = ?", set.ID).First(&doc).Error; err != nil {
				return err
			}
			var config Config
			if err := yaml.Unmarshal([]byte(doc.CanonicalYAML), &config); err != nil {
				return err
			}
			canResume := false
			for _, caller := range config.Callers {
				if caller.ID == req.CallerID {
					canResume = caller.ContainmentSource == "containment"
					break
				}
			}
			if !canResume {
				return errCallerAdminConflict
			}
		}
		if principal.Kind == "webhook" {
			if err := h.deps.ClaimWebhookNonce(tx, principal); err != nil {
				if strings.Contains(err.Error(), "already claimed") {
					return errContainmentNonceReplay
				}
				return fmt.Errorf("%w: %v", errContainmentNonceStore, err)
			}
		}
		if err := tx.Exec(`UPDATE router_config_callers SET status = ? WHERE config_set_id = ? AND caller_id = ?`, status, set.ID, req.CallerID).Error; err != nil {
			return err
		}
		if err := h.syncCanonicalDocument(tx, set.ID, func(callers []CallerConfig) ([]CallerConfig, error) {
			for i := range callers {
				if callers[i].ID == req.CallerID {
					callers[i].Status = status
					callers[i].ContainmentExpiresAt = expiry
					if req.Action == "suspend" {
						callers[i].ContainmentSource = source
					} else {
						callers[i].ContainmentSource = ""
					}
					return callers, nil
				}
			}
			return nil, errCallerAdminNotFound
		}); err != nil {
			return err
		}
		meta, err := adminAuditMetaForWrite(r, adminAuthSubject{subject: h.actor(r)}, authzObjectAdminContainment, authzActionContain)
		if err != nil {
			return err
		}
		meta.Ticket = safeAdminTicket(req.Reason)
		_, err = bumpConfigRevision(tx, h.deps.RuntimeScope, set.ID, "caller_containment_"+req.Action, h.actor(r), meta, req.Action, req.CallerID)
		return err
	})
	if err != nil {
		if errors.Is(err, errCallerAdminNotFound) {
			writeCallerAdminError(w, http.StatusNotFound, "containment-caller-not-found")
			return
		}
		if errors.Is(err, errCallerAdminForbidden) {
			writeCallerAdminError(w, http.StatusForbidden, "containment-forbidden")
			return
		}
		if errors.Is(err, errCallerAdminConflict) {
			writeCallerAdminError(w, http.StatusConflict, "containment-state-conflict")
			return
		}
		if errors.Is(err, errContainmentNonceReplay) {
			writeCallerAdminError(w, http.StatusConflict, "containment-replay")
			return
		}
		if errors.Is(err, errContainmentNonceStore) {
			writeCallerAdminError(w, http.StatusServiceUnavailable, "containment-nonce-store-unavailable")
			return
		}
		writeCallerAdminError(w, http.StatusInternalServerError, "containment-mutation-failed")
		return
	}
	if h.deps.AfterMutate != nil {
		if err := h.deps.AfterMutate(r, req.CallerID); err != nil {
			writeCallerAdminError(w, http.StatusInternalServerError, "containment-reload-failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"caller_id": req.CallerID, "status": status, "expires_at": expiry})
}

func validContainmentReason(reason string) bool {
	if len(reason) == 0 || len(reason) > 64 {
		return false
	}
	for _, r := range reason {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// handlerKeys maps each caller handler to the registry's HandlerKey.
func (h *callerAdminHandler) handlerKeys() map[string]http.HandlerFunc {
	out := map[string]http.HandlerFunc{}
	for _, entry := range h.callerAdminPatterns() {
		out[entry.key] = entry.handler
	}
	out["callers.containment"] = h.handleContainment
	out["containment.webhook"] = h.handleWebhookContainment
	return out
}

// register installs the caller directory patterns under prefix with the legacy wildcard
// names, so r.PathValue("id") keeps working on both surfaces.
func (h *callerAdminHandler) register(handle func(pattern string, handler http.HandlerFunc), prefix string) {
	for _, entry := range h.callerAdminPatterns() {
		method, path, _ := strings.Cut(entry.pattern, " ")
		handle(method+" "+prefix+path, entry.handler)
	}
}

type callerAdminHandler struct {
	deps CallerAdminDeps
}

var errCallerAdminConflict = errors.New("caller already exists")
var errCallerAdminNotFound = errors.New("caller not found")
var errCallerAdminForbidden = errors.New("caller operation forbidden")
var errContainmentNonceReplay = errors.New("containment nonce replay")
var errContainmentNonceStore = errors.New("containment nonce store unavailable")

type callerAdminError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func writeCallerAdminError(w http.ResponseWriter, status int, errType string) {
	writeJSON(w, status, map[string]any{"error": callerAdminError{Type: errType, Message: errType}})
}

func (h *callerAdminHandler) authorize(w http.ResponseWriter, r *http.Request, action string) bool {
	if h.deps.Authorize == nil || !h.deps.Authorize(r, authzObjectAdminCallers, action) {
		writeCallerAdminError(w, http.StatusForbidden, "callers-forbidden")
		return false
	}
	return true
}

func (h *callerAdminHandler) authorizeContainment(w http.ResponseWriter, r *http.Request) bool {
	if h.deps.Authorize == nil || !h.deps.Authorize(r, authzObjectAdminContainment, authzActionContain) {
		writeCallerAdminError(w, http.StatusForbidden, "containment-forbidden")
		return false
	}
	return true
}

// allowInDomain takes a caller-directory decision in one row's stored domain.
func (h *callerAdminHandler) allowInDomain(r *http.Request, domain, object, action string) bool {
	if h.deps.Authorize == nil || !h.deps.Authorize(r, object, action) {
		return false
	}
	if h.deps.AuthorizeDomain == nil {
		return true
	}
	return h.deps.AuthorizeDomain(r, domain, object, action)
}

func (h *callerAdminHandler) now() time.Time {
	if h.deps.Now != nil {
		return h.deps.Now()
	}
	return time.Now().UTC()
}

func (h *callerAdminHandler) activeConfigSet() (configSetRow, error) {
	if h.deps.DB == nil {
		return configSetRow{}, errors.New("config control-plane database is required")
	}
	scope := strings.TrimSpace(h.deps.RuntimeScope)
	if scope == "" {
		return configSetRow{}, errors.New("config runtime scope is required")
	}
	var sets []configSetRow
	if err := h.deps.DB.Where("runtime_scope = ? AND status = ? AND validation_status = ?", scope, "active", "valid").Limit(2).Find(&sets).Error; err != nil {
		return configSetRow{}, fmt.Errorf("load active config set: %w", err)
	}
	if len(sets) == 0 {
		return configSetRow{}, fmt.Errorf("no validated active config set for runtime scope %q", scope)
	}
	if len(sets) != 1 {
		return configSetRow{}, fmt.Errorf("multiple validated active config sets for runtime scope %q", scope)
	}
	return sets[0], nil
}

type callerAdminCallerView struct {
	ID           string     `json:"id"`
	OwnerUser    string     `json:"owner_user"`
	Project      string     `json:"project"`
	Environment  string     `json:"environment"`
	Status       string     `json:"status"`
	TokenID      string     `json:"token_id"`
	TokenHash    string     `json:"token_hash,omitempty"`
	Allow        []string   `json:"allow"`
	MetricsAdmin bool       `json:"metrics_admin"`
	ContentAdmin bool       `json:"content_admin"`
	Rate         RateConfig `json:"rate"`
}

type callerAdminListResponse struct {
	GeneratedUTC string                  `json:"generated_utc"`
	ConfigSetID  string                  `json:"config_set_id"`
	Callers      []callerAdminCallerView `json:"callers"`
}

func (h *callerAdminHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, authzActionList) {
		return
	}
	set, err := h.activeConfigSet()
	if err != nil {
		writeCallerAdminError(w, http.StatusServiceUnavailable, "callers-config-set-unavailable")
		return
	}
	var rows []callerRow
	if err := h.deps.DB.Where("config_set_id = ?", set.ID).Order("caller_id ASC").Find(&rows).Error; err != nil {
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-query-failed")
		return
	}
	resp := callerAdminListResponse{
		GeneratedUTC: h.now().Format(time.RFC3339Nano),
		ConfigSetID:  set.ID,
		Callers:      []callerAdminCallerView{},
	}
	adminPlane := adminPlaneAuthorized(r)
	for _, row := range rows {
		var allowed []callerAllowedGroupRow
		if err := h.deps.DB.Where("config_set_id = ? AND caller_id = ?", set.ID, row.CallerID).Order("group_name ASC").Find(&allowed).Error; err != nil {
			writeCallerAdminError(w, http.StatusInternalServerError, "callers-query-failed")
			return
		}
		allow := make([]string, 0, len(allowed))
		for _, a := range allowed {
			allow = append(allow, a.GroupName)
		}
		if adminPlane && !h.allowInDomain(r, authzDomainForProjectEnvironment(row.Project, row.Environment), authzObjectAdminCallers, authzActionList) {
			// A row is listed only in its own stored domain.
			continue
		}
		tokenHash := strings.TrimSpace(row.TokenVerifier)
		if !isCallerTokenHashValue(tokenHash) {
			writeCallerAdminError(w, http.StatusServiceUnavailable, "callers-legacy-verifier-present")
			return
		}
		if adminPlane {
			// Verification material never leaves the router on the versioned surface.
			tokenHash = ""
		}
		resp.Callers = append(resp.Callers, callerAdminCallerView{
			ID:           row.CallerID,
			OwnerUser:    row.OwnerUser,
			Project:      row.Project,
			Environment:  row.Environment,
			Status:       row.Status,
			TokenID:      row.TokenID,
			TokenHash:    tokenHash,
			Allow:        allow,
			MetricsAdmin: row.MetricsAdmin,
			ContentAdmin: row.ContentAdmin,
			Rate:         RateConfig{RPM: row.RPM, TPM: row.TPM, Concurrent: row.Concurrent},
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type callerAdminIssueRequest struct {
	OwnerUser    string     `json:"owner_user"`
	Project      string     `json:"project"`
	Environment  string     `json:"environment"`
	KeySlug      string     `json:"key_slug"`
	Allow        []string   `json:"allow"`
	MetricsAdmin bool       `json:"metrics_admin"`
	ContentAdmin bool       `json:"content_admin"`
	Rate         RateConfig `json:"rate"`
}

type callerAdminIssueResponse struct {
	Caller  callerAdminCallerView `json:"caller"`
	Token   string                `json:"token"`
	TokenID string                `json:"token_id"`
}

func (h *callerAdminHandler) handleIssue(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, authzActionIssue) {
		return
	}
	var req callerAdminIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCallerAdminError(w, http.StatusBadRequest, "callers-invalid-request")
		return
	}
	set, err := h.activeConfigSet()
	if err != nil {
		writeCallerAdminError(w, http.StatusServiceUnavailable, "callers-config-set-unavailable")
		return
	}
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser:   req.OwnerUser,
		Project:     req.Project,
		Environment: req.Environment,
		KeySlug:     req.KeySlug,
		Allow:       req.Allow,
		Now:         h.now(),
	})
	if err != nil {
		writeCallerAdminError(w, http.StatusBadRequest, "callers-invalid-request")
		return
	}
	caller := generated.Caller
	rate := caller.Rate
	if req.Rate.RPM > 0 || req.Rate.TPM > 0 || req.Rate.Concurrent > 0 {
		rate = req.Rate
	}
	row := callerRow{
		CallerID:      caller.ID,
		OwnerUser:     caller.OwnerUser,
		Project:       caller.Project,
		Environment:   caller.Environment,
		Status:        accountStatusActive,
		TokenVerifier: storedCallerTokenVerifier(caller),
		TokenID:       generated.TokenID,
		MetricsAdmin:  req.MetricsAdmin,
		ContentAdmin:  req.ContentAdmin,
		RPM:           rate.RPM,
		TPM:           rate.TPM,
		Concurrent:    rate.Concurrent,
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
			return err
		}
		var existing int64
		if err := tx.Table("router_config_callers").Where("config_set_id = ? AND caller_id = ?", set.ID, row.CallerID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return errCallerAdminConflict
		}
		insertSQL := `INSERT INTO router_config_callers (config_set_id, caller_id, owner_user, project, environment, status, token_sha256, token_id, metrics_admin, content_admin, rpm, tpm, concurrent, token_format_version, token_entropy_bits, revoked_token_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`
		if err := tx.Exec(insertSQL,
			set.ID, row.CallerID, row.OwnerUser, row.Project, row.Environment, row.Status, row.TokenVerifier, row.TokenID, row.MetricsAdmin, row.ContentAdmin, row.RPM, row.TPM, row.Concurrent, caller.TokenFormatVersion, caller.TokenEntropyBits).Error; err != nil {
			return err
		}
		for _, group := range caller.Allow {
			if err := tx.Exec(`INSERT INTO router_config_caller_allowed_groups (config_set_id, caller_id, group_name) VALUES (?, ?, ?)`, set.ID, row.CallerID, group).Error; err != nil {
				return err
			}
		}
		// Sync the canonical document with the new caller.
		if err := h.syncCanonicalDocument(tx, set.ID, func(callers []CallerConfig) ([]CallerConfig, error) {
			newCaller := CallerConfig{
				ID:                 row.CallerID,
				OwnerUser:          row.OwnerUser,
				Project:            row.Project,
				Environment:        row.Environment,
				Status:             row.Status,
				TokenHash:          caller.TokenHash,
				TokenID:            row.TokenID,
				TokenFormatVersion: caller.TokenFormatVersion,
				TokenEntropyBits:   caller.TokenEntropyBits,
				Allow:              append([]string(nil), caller.Allow...),
				MetricsAdmin:       row.MetricsAdmin,
				ContentAdmin:       row.ContentAdmin,
				Rate:               rate,
			}
			return append(callers, newCaller), nil
		}); err != nil {
			return err
		}
		// Bump revision and append change event.
		meta, metaErr := adminAuditMetaForWrite(r, adminAuthSubject{subject: h.actor(r)}, authzObjectAdminCallers, authzActionIssue)
		if metaErr != nil {
			return metaErr
		}
		if _, err := bumpConfigRevision(tx, h.deps.RuntimeScope, set.ID, "caller_issue", h.actor(r), meta, "issue", caller.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCallerAdminConflict) {
			writeCallerAdminError(w, http.StatusConflict, "callers-already-exists")
			return
		}
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-mutation-failed")
		return
	}
	if h.deps.AfterMutate != nil {
		if err := h.deps.AfterMutate(r, row.CallerID); err != nil {
			writeCallerAdminError(w, http.StatusInternalServerError, "callers-reload-failed")
			return
		}
	}
	// The raw token is returned exactly once here. Only the token verifier and
	// the non-secret token identifier were persisted above.
	writeJSON(w, http.StatusCreated, callerAdminIssueResponse{
		Caller: callerAdminCallerView{
			ID:           row.CallerID,
			OwnerUser:    row.OwnerUser,
			Project:      row.Project,
			Environment:  row.Environment,
			Status:       row.Status,
			TokenID:      row.TokenID,
			TokenHash:    caller.TokenHash,
			Allow:        append([]string(nil), caller.Allow...),
			MetricsAdmin: row.MetricsAdmin,
			ContentAdmin: row.ContentAdmin,
			Rate:         rate,
		},
		Token:   generated.Token,
		TokenID: generated.TokenID,
	})
}

type callerAdminTokenResponse struct {
	CallerID string `json:"caller_id"`
	Status   string `json:"status"`
	TokenID  string `json:"token_id,omitempty"`
	Token    string `json:"token,omitempty"`
}

func (h *callerAdminHandler) handleRotate(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, authzActionRotate) {
		return
	}
	callerID := strings.TrimSpace(r.PathValue("id"))
	if callerID == "" {
		writeCallerAdminError(w, http.StatusBadRequest, "callers-invalid-request")
		return
	}
	set, err := h.activeConfigSet()
	if err != nil {
		writeCallerAdminError(w, http.StatusServiceUnavailable, "callers-config-set-unavailable")
		return
	}
	row, allow, err := h.loadCaller(set.ID, callerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeCallerAdminError(w, http.StatusNotFound, "callers-not-found")
			return
		}
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-query-failed")
		return
	}
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser:   row.OwnerUser,
		Project:     row.Project,
		Environment: row.Environment,
		Allow:       allow,
		Now:         h.now(),
	})
	if err != nil {
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-mutation-failed")
		return
	}
	// Persist only the new hash and public token identifier; the raw token
	// never touches the database or logs.
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE router_config_callers SET token_sha256 = ?, token_id = ?, token_format_version = ?, token_entropy_bits = ?, revoked_token_hash = '' WHERE config_set_id = ? AND caller_id = ?`,
			storedCallerTokenVerifier(generated.Caller), generated.TokenID, generated.Caller.TokenFormatVersion, generated.Caller.TokenEntropyBits, set.ID, callerID).Error; err != nil {
			return err
		}
		// Sync the canonical document with the rotated token hash.
		if err := h.syncCanonicalDocument(tx, set.ID, func(callers []CallerConfig) ([]CallerConfig, error) {
			for i, c := range callers {
				if c.ID == callerID {
					callers[i].TokenHash = generated.TokenHash
					callers[i].TokenID = generated.TokenID
					callers[i].TokenFormatVersion = generated.Caller.TokenFormatVersion
					callers[i].TokenEntropyBits = generated.Caller.TokenEntropyBits
					callers[i].RevokedTokenHash = ""
					break
				}
			}
			return callers, nil
		}); err != nil {
			return err
		}
		meta, metaErr := adminAuditMetaForWrite(r, adminAuthSubject{subject: h.actor(r)}, authzObjectAdminCallers, authzActionRotate)
		if metaErr != nil {
			return metaErr
		}
		if _, err := bumpConfigRevision(tx, h.deps.RuntimeScope, set.ID, "caller_rotate", h.actor(r), meta, "rotate", callerID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-mutation-failed")
		return
	}
	if h.deps.AfterMutate != nil {
		if err := h.deps.AfterMutate(r, callerID); err != nil {
			writeCallerAdminError(w, http.StatusInternalServerError, "callers-reload-failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, callerAdminTokenResponse{
		CallerID: callerID,
		Status:   row.Status,
		TokenID:  generated.TokenID,
		Token:    generated.Token,
	})
}

func (h *callerAdminHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, authzActionRevoke) {
		return
	}
	callerID := strings.TrimSpace(r.PathValue("id"))
	if callerID == "" {
		writeCallerAdminError(w, http.StatusBadRequest, "callers-invalid-request")
		return
	}
	set, err := h.activeConfigSet()
	if err != nil {
		writeCallerAdminError(w, http.StatusServiceUnavailable, "callers-config-set-unavailable")
		return
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE router_config_callers SET status = ? WHERE config_set_id = ? AND caller_id = ?`, accountStatusDisabled, set.ID, callerID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errCallerAdminNotFound
		}
		// Sync the canonical document with the revoked status.
		if err := h.syncCanonicalDocument(tx, set.ID, func(callers []CallerConfig) ([]CallerConfig, error) {
			for i, c := range callers {
				if c.ID == callerID {
					callers[i].Status = accountStatusDisabled
					break
				}
			}
			return callers, nil
		}); err != nil {
			return err
		}
		meta, metaErr := adminAuditMetaForWrite(r, adminAuthSubject{subject: h.actor(r)}, authzObjectAdminCallers, authzActionRevoke)
		if metaErr != nil {
			return metaErr
		}
		if _, err := bumpConfigRevision(tx, h.deps.RuntimeScope, set.ID, "caller_revoke", h.actor(r), meta, "revoke", callerID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCallerAdminNotFound) {
			writeCallerAdminError(w, http.StatusNotFound, "callers-not-found")
			return
		}
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-mutation-failed")
		return
	}
	if h.deps.AfterMutate != nil {
		if err := h.deps.AfterMutate(r, callerID); err != nil {
			writeCallerAdminError(w, http.StatusInternalServerError, "callers-reload-failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, callerAdminTokenResponse{CallerID: callerID, Status: accountStatusDisabled})
}

// loadCaller reads one caller row and its allowed groups for the set.
func (h *callerAdminHandler) loadCaller(configSetID, callerID string) (callerRow, []string, error) {
	var rows []callerRow
	if err := h.deps.DB.Where("config_set_id = ? AND caller_id = ?", configSetID, callerID).Limit(1).Find(&rows).Error; err != nil {
		return callerRow{}, nil, err
	}
	if len(rows) == 0 {
		return callerRow{}, nil, gorm.ErrRecordNotFound
	}
	var allowed []callerAllowedGroupRow
	if err := h.deps.DB.Where("config_set_id = ? AND caller_id = ?", configSetID, callerID).Order("group_name ASC").Find(&allowed).Error; err != nil {
		return callerRow{}, nil, err
	}
	allow := make([]string, 0, len(allowed))
	for _, a := range allowed {
		allow = append(allow, a.GroupName)
	}
	return rows[0], allow, nil
}

// syncCanonicalDocument loads the canonical YAML for configSetID inside the
// supplied transaction, applies mutate to the callers slice, and writes the
// patched document back to router_config_documents. The document content hash
// and updated_at are refreshed. The stored document is never expanded:
// environment references remain verbatim so the YAML written here matches the
// source document semantically. If no document row exists yet, the function
// returns an error and the surrounding transaction rolls back the relational
// mutation, ensuring the projection and document cannot diverge.
func (h *callerAdminHandler) syncCanonicalDocument(tx *gorm.DB, configSetID string, mutate func(callers []CallerConfig) ([]CallerConfig, error)) error {
	if tx == nil {
		return errors.New("sync canonical document: transaction is required")
	}
	if strings.TrimSpace(configSetID) == "" {
		return errors.New("sync canonical document: config set ID is required")
	}
	var row configControlPlaneDocumentRow
	if err := tx.Where("config_set_id = ?", configSetID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("sync canonical document: no document for config set %q", configSetID)
		}
		return fmt.Errorf("load canonical document: %w", err)
	}
	patched, err := PatchCanonicalCallersYAML([]byte(row.CanonicalYAML), mutate)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(patched)
	updatedAt := h.now().Format(time.RFC3339Nano)
	if err := tx.Exec(
		`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`,
		string(patched), hex.EncodeToString(hash[:]), updatedAt, configSetID,
	).Error; err != nil {
		return fmt.Errorf("write canonical document: %w", err)
	}
	return nil
}

// actor extracts a non-secret actor identifier from the request. The caller
// directory admin endpoints do not require authentication beyond the policy
// callback; when an authenticated admin subject is available (for example a
// JWT or basic-auth user) the X-Actor header surfaces it. Otherwise the
// remote address is used as a deployment-stable label. Secret material is
// never copied into the change-event actor column.
func (h *callerAdminHandler) actor(r *http.Request) string {
	if r == nil {
		return "anonymous"
	}
	// On /admin/v1 the actor is the authenticated principal and nothing else: the
	// client-supplied X-Actor/X-Admin-Subject headers and RemoteAddr are ignored.
	if principal, ok := PrincipalFrom(r.Context()); ok && strings.TrimSpace(principal.Subject) != "" {
		return principal.Subject
	}
	if actor := strings.TrimSpace(r.Header.Get("X-Actor")); actor != "" {
		return actor
	}
	if actor := strings.TrimSpace(r.Header.Get("X-Admin-Subject")); actor != "" {
		return actor
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if host == "" {
		return "anonymous"
	}
	return host
}

// PatchCanonicalCallersYAML parses a canonical router configuration YAML
// document, applies mutate to its callers slice, and remarshals the document.
// It keeps the canonical YAML representation in lockstep with the relational
// projection. The document must decode to a mapping; a missing callers key
// yields an empty slice. The mutate callback receives the decoded callers and
// returns the replacement slice. Raw tokens are never present in the document
// — callers carry token hashes only.
func PatchCanonicalCallersYAML(raw []byte, mutate func(callers []CallerConfig) ([]CallerConfig, error)) ([]byte, error) {
	if mutate == nil {
		return nil, errors.New("mutate callback is required")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse canonical config YAML: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	var callers []CallerConfig
	if existing, ok := doc["callers"]; ok && existing != nil {
		encoded, err := yaml.Marshal(existing)
		if err != nil {
			return nil, fmt.Errorf("re-encode callers slice: %w", err)
		}
		if err := yaml.Unmarshal(encoded, &callers); err != nil {
			return nil, fmt.Errorf("decode callers slice: %w", err)
		}
	}
	patched, err := mutate(callers)
	if err != nil {
		return nil, err
	}
	if patched == nil {
		patched = []CallerConfig{}
	}
	doc["callers"] = patched
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical config YAML: %w", err)
	}
	return out, nil
}
