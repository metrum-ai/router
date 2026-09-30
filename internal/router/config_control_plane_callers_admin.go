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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const (
	authzObjectAdminCallers = "admin:callers"
	authzActionList         = "list"
	authzActionIssue        = "issue"
	authzActionRotate       = "rotate"
	authzActionRevoke       = "revoke"
)

// CallerAdminDeps carries the collaborators the caller directory admin
// endpoints need. Authorize is intentionally a plain callback so the route
// registration stays decoupled from the Casbin wiring inside Service; the
// service passes a closure that resolves the authenticated admin subject and
// enforces the admin:callers policy. AfterMutate is invoked once after every
// successful mutation so the runtime can bump its configuration revision and
// schedule a reload; it is called outside the database transaction.
type CallerAdminDeps struct {
	DB           *gorm.DB
	RuntimeScope string
	Authorize    func(r *http.Request, object, action string) bool
	AfterMutate  func(r *http.Request, callerID string)
	Now          func() time.Time
}

// RegisterCallerDirectoryAdmin registers the admin caller directory mutation
// endpoints on mux. It is exported so the service route map stays untouched
// while the control-plane wiring composes it.
func RegisterCallerDirectoryAdmin(mux *http.ServeMux, deps CallerAdminDeps) {
	h := &callerAdminHandler{deps: deps}
	mux.HandleFunc("GET /admin/callers", h.handleList)
	mux.HandleFunc("POST /admin/callers/issue", h.handleIssue)
	mux.HandleFunc("POST /admin/callers/{id}/rotate", h.handleRotate)
	mux.HandleFunc("POST /admin/callers/{id}/revoke", h.handleRevoke)
}

type callerAdminHandler struct {
	deps CallerAdminDeps
}

var errCallerAdminConflict = errors.New("caller already exists")

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
	TokenSHA256  string     `json:"token_sha256"`
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
		resp.Callers = append(resp.Callers, callerAdminCallerView{
			ID:           row.CallerID,
			OwnerUser:    row.OwnerUser,
			Project:      row.Project,
			Environment:  row.Environment,
			Status:       row.Status,
			TokenID:      row.TokenID,
			TokenSHA256:  row.TokenSHA256,
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
		CallerID:     caller.ID,
		OwnerUser:    caller.OwnerUser,
		Project:      caller.Project,
		Environment:  caller.Environment,
		Status:       accountStatusActive,
		TokenSHA256:  generated.TokenSHA256,
		TokenID:      generated.TokenID,
		MetricsAdmin: req.MetricsAdmin,
		ContentAdmin: req.ContentAdmin,
		RPM:          rate.RPM,
		TPM:          rate.TPM,
		Concurrent:   rate.Concurrent,
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Table("router_config_callers").Where("config_set_id = ? AND caller_id = ?", set.ID, row.CallerID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return errCallerAdminConflict
		}
		insertSQL := `INSERT INTO router_config_callers (config_set_id, caller_id, owner_user, project, environment, status, token_sha256, token_id, metrics_admin, content_admin, rpm, tpm, concurrent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		if err := tx.Exec(insertSQL,
			set.ID, row.CallerID, row.OwnerUser, row.Project, row.Environment, row.Status, row.TokenSHA256, row.TokenID, row.MetricsAdmin, row.ContentAdmin, row.RPM, row.TPM, row.Concurrent).Error; err != nil {
			return err
		}
		for _, group := range caller.Allow {
			if err := tx.Exec(`INSERT INTO router_config_caller_allowed_groups (config_set_id, caller_id, group_name) VALUES (?, ?, ?)`, set.ID, row.CallerID, group).Error; err != nil {
				return err
			}
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
		h.deps.AfterMutate(r, row.CallerID)
	}
	// The raw token is returned exactly once here. Only the SHA-256 hash and
	// the non-secret token identifier were persisted above.
	writeJSON(w, http.StatusCreated, callerAdminIssueResponse{
		Caller: callerAdminCallerView{
			ID:           row.CallerID,
			OwnerUser:    row.OwnerUser,
			Project:      row.Project,
			Environment:  row.Environment,
			Status:       row.Status,
			TokenID:      row.TokenID,
			TokenSHA256:  row.TokenSHA256,
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
	if err := h.deps.DB.Exec(`UPDATE router_config_callers SET token_sha256 = ?, token_id = ? WHERE config_set_id = ? AND caller_id = ?`,
		generated.TokenSHA256, generated.TokenID, set.ID, callerID).Error; err != nil {
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-mutation-failed")
		return
	}
	if h.deps.AfterMutate != nil {
		h.deps.AfterMutate(r, callerID)
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
	res := h.deps.DB.Exec(`UPDATE router_config_callers SET status = ? WHERE config_set_id = ? AND caller_id = ?`, accountStatusDisabled, set.ID, callerID)
	if res.Error != nil {
		writeCallerAdminError(w, http.StatusInternalServerError, "callers-mutation-failed")
		return
	}
	if res.RowsAffected == 0 {
		writeCallerAdminError(w, http.StatusNotFound, "callers-not-found")
		return
	}
	if h.deps.AfterMutate != nil {
		h.deps.AfterMutate(r, callerID)
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
