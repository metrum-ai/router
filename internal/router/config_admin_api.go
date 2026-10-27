// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Admin API HTTP surface for configuration control-plane group and set
// operations (metrum-ai/router#395). All routes require the dedicated admin
// credential (#350) or an equivalent browser-admin/OIDC subject with the
// fine-grained permissions config:read, config:write, config:activate, and
// config:force. Writes fail closed in file mode with 409
// config_source_mode_file; reads work in every mode.
//
// Nothing secret crosses this API: requests carry catalog references only,
// credential-shaped fields are rejected before parsing, bodies are never
// logged, and responses contain configuration metadata only.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const (
	// maxAdminConfigBodyBytes bounds Admin API request bodies.
	maxAdminConfigBodyBytes = 1 << 20
	// maxAdminIdempotencyKeyBytes bounds the Idempotency-Key header.
	maxAdminIdempotencyKeyBytes = 128
)

// ConfigAdminDeps wires the Admin API config handlers into the service.
// DB is nil in file mode. Authorize checks one fine-grained permission for
// the authenticated subject. Reload is invoked after a successful write so
// the serving replica picks up the new active set immediately; other
// replicas converge through the revision poller/notifier.
type ConfigAdminDeps struct {
	DB                      *gorm.DB
	RuntimeScope            string
	FileMode                bool
	Driver                  string
	RequireValidationStatus string
	InstanceID              string
	Authenticate            func(w http.ResponseWriter, r *http.Request) (adminAuthSubject, bool)
	Authorize               func(subject adminAuthSubject, permission string) bool
	CurrentConfig           func() *Config
	LoadedRevision          func() (revision int64, setID string, at time.Time)
	Reload                  func() error
	ReloadShadows           func()
	UsageDB                 func() *gorm.DB
	RecordAccess            func(r *http.Request, subject adminAuthSubject, status int, reason, object, action string)
	Now                     func() time.Time
}

// RegisterConfigAdminAPI registers every /admin/config/* route.
func RegisterConfigAdminAPI(mux *http.ServeMux, deps ConfigAdminDeps) {
	h := &configAdminHandler{deps: deps}
	mux.HandleFunc("GET /admin/config/groups", h.handleListGroups)
	mux.HandleFunc("GET /admin/config/groups/{group}", h.handleGetGroup)
	mux.HandleFunc("PUT /admin/config/groups/{group}/targets", h.handleReplaceTargets)
	mux.HandleFunc("POST /admin/config/groups/{group}/targets", h.handleAddTarget)
	mux.HandleFunc("DELETE /admin/config/groups/{group}/targets/{target_id...}", h.handleRemoveTarget)
	mux.HandleFunc("POST /admin/config/groups/{group}/targets:validate", h.handleValidateTargets)
	mux.HandleFunc("POST /admin/config/groups/{group}/rollback", h.handleGroupRollback)
	mux.HandleFunc("GET /admin/config/groups/{group}/history", h.handleGroupHistory)
	mux.HandleFunc("POST /admin/config/groups/{group}/shadow", h.handleShadowStart)
	mux.HandleFunc("GET /admin/config/groups/{group}/shadow", h.handleShadowStatus)
	mux.HandleFunc("POST /admin/config/groups/{group}/shadow/promote", h.handleShadowPromote)
	mux.HandleFunc("DELETE /admin/config/groups/{group}/shadow", h.handleShadowStop)
	mux.HandleFunc("GET /admin/config/status", h.handleConfigStatus)
	mux.HandleFunc("POST /admin/config/sets", h.handleImportSet)
	mux.HandleFunc("GET /admin/config/sets", h.handleListSets)
	mux.HandleFunc("GET /admin/config/sets/{set_id}", h.handleExportSet)
	mux.HandleFunc("POST /admin/config/sets/{set_id}/activate", h.handleActivateSet)
	mux.HandleFunc("POST /admin/config/rollback", h.handleSetRollback)
	mux.HandleFunc("GET /admin/config/diff", h.handleConfigDiff)
}

type configAdminHandler struct {
	deps ConfigAdminDeps
}

func (h *configAdminHandler) now() time.Time {
	if h.deps.Now != nil {
		return h.deps.Now()
	}
	return time.Now().UTC()
}

func writeConfigAdminError(w http.ResponseWriter, status int, errType, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": errType, "message": message}})
}

func writeConfigAdminTypedError(w http.ResponseWriter, err *ConfigAdminError) {
	body := map[string]any{"error": map[string]any{"type": err.Type, "message": err.Message}}
	if err.Revision != 0 {
		body["error"].(map[string]any)["current_revision"] = err.Revision
	}
	if err.Reports != nil {
		body["error"].(map[string]any)["targets"] = err.Reports
	}
	writeJSON(w, err.Status, body)
}

// authenticate runs the admin credential check and records denials in the
// security event log.
func (h *configAdminHandler) authenticate(w http.ResponseWriter, r *http.Request) (adminAuthSubject, bool) {
	if h.deps.Authenticate == nil {
		writeConfigAdminError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return adminAuthSubject{}, false
	}
	subject, ok := h.deps.Authenticate(w, r)
	if !ok {
		if h.deps.RecordAccess != nil {
			h.deps.RecordAccess(r, adminAuthSubject{}, http.StatusUnauthorized, "unauthorized", authzObjectAdminConfig, authzActionRead)
		}
		return adminAuthSubject{}, false
	}
	return subject, true
}

// authorize checks one fine-grained permission and writes 403 on denial.
func (h *configAdminHandler) authorize(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, permission string) bool {
	if h.deps.Authorize == nil || !h.deps.Authorize(subject, permission) {
		if h.deps.RecordAccess != nil {
			h.deps.RecordAccess(r, subject, http.StatusForbidden, "config-forbidden", authzObjectAdminConfig, permission)
		}
		writeConfigAdminError(w, http.StatusForbidden, "config-forbidden", "the admin credential lacks the "+permission+" permission")
		return false
	}
	return true
}

// writeGate refuses writes in file mode with the documented 409 code.
func (h *configAdminHandler) writeGate(w http.ResponseWriter) bool {
	if h.deps.FileMode || h.deps.DB == nil {
		writeConfigAdminError(w, http.StatusConflict, "config_source_mode_file", "the router serves configuration from a YAML file; writes need server.config_source.mode: database with the current YAML imported")
		return false
	}
	return true
}

// decodeConfigAdminBody parses a JSON body with unknown-field rejection and a
// size cap, then refuses credential-shaped scalars anywhere in the payload.
func decodeConfigAdminBody(w http.ResponseWriter, r *http.Request, value any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdminConfigBodyBytes+1))
	if err != nil || len(body) > maxAdminConfigBodyBytes {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "request body is unreadable or too large")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "request body is required")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "request body is not a valid request document")
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "request body has trailing content")
		return false
	}
	// Fail closed on credential-shaped material anywhere in the payload. The
	// body is scanned, never echoed, never logged.
	var generic any
	if err := json.Unmarshal(body, &generic); err == nil && jsonContainsSecretShape(generic) {
		writeConfigAdminError(w, http.StatusBadRequest, "config-secret-refused", "the request contains credential-shaped material; admin API writes reference catalog providers and api_key_env entries only")
		return false
	}
	return true
}

// jsonContainsSecretShape walks a decoded JSON document for strings matching
// known live-secret shapes.
func jsonContainsSecretShape(value any) bool {
	switch typed := value.(type) {
	case string:
		return configControlPlaneSecretPattern.MatchString(typed)
	case []any:
		for _, item := range typed {
			if jsonContainsSecretShape(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if jsonContainsSecretShape(item) {
				return true
			}
		}
	}
	return false
}

// parseIfMatch reads the optional If-Match revision precondition.
func parseIfMatch(r *http.Request) (*int64, bool) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	if header == "" {
		return nil, true
	}
	header = strings.Trim(header, "\"")
	revision, err := strconv.ParseInt(header, 10, 64)
	if err != nil || revision < 0 {
		return nil, false
	}
	return &revision, true
}

// parseIdempotencyKey validates the optional Idempotency-Key header.
func parseIdempotencyKey(r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", true
	}
	if len(key) > maxAdminIdempotencyKeyBytes || strings.ContainsAny(key, "\r\n\t") {
		return "", false
	}
	return key, true
}

func (h *configAdminHandler) policy() GroupWritePolicy {
	return GroupWritePolicy{RequireValidationStatus: h.deps.RequireValidationStatus}
}

// activeConfigForRead returns the serving configuration for read endpoints:
// the active canonical document in DB mode, the live config in file mode.
func (h *configAdminHandler) activeConfigForRead() (*Config, string, int64, error) {
	if h.deps.FileMode || h.deps.DB == nil {
		if h.deps.CurrentConfig == nil {
			return nil, "", 0, errors.New("no live configuration")
		}
		cfg := h.deps.CurrentConfig()
		if cfg == nil {
			return nil, "", 0, errors.New("no live configuration")
		}
		revision, setID, _ := h.loadedRevision()
		return cfg, setID, revision, nil
	}
	var revision int64
	if rev, err := currentRevisionForScope(h.deps.DB, h.deps.RuntimeScope); err == nil {
		revision = rev
	}
	var sets []configSetRow
	if err := h.deps.DB.Where("runtime_scope = ? AND status = ?", h.deps.RuntimeScope, "active").Limit(1).Find(&sets).Error; err != nil {
		return nil, "", 0, err
	}
	if len(sets) == 0 {
		return nil, "", 0, configAdminError(409, "config-no-active-set", "no active configuration set for the runtime scope")
	}
	cfg, err := LoadActiveCanonicalConfig(h.deps.DB, h.deps.RuntimeScope)
	if err != nil {
		return nil, "", 0, err
	}
	return cfg, sets[0].ID, revision, nil
}

func (h *configAdminHandler) loadedRevision() (int64, string, time.Time) {
	if h.deps.LoadedRevision == nil {
		return 0, "", time.Time{}
	}
	return h.deps.LoadedRevision()
}

type configAdminTargetJSON struct {
	Provider         string `json:"provider"`
	Model            string `json:"model,omitempty"`
	ModelRef         string `json:"model_ref,omitempty"`
	Weight           int    `json:"weight,omitempty"`
	Region           string `json:"region,omitempty"`
	ValidationStatus string `json:"validation_status,omitempty"`
}

func configAdminTargetViews(group ModelGroup) []configAdminTargetJSON {
	out := make([]configAdminTargetJSON, 0, len(group.Targets))
	for _, target := range group.Targets {
		view := configAdminTargetJSON{Provider: target.Provider, Model: target.Model, ModelRef: target.ModelRef, Weight: target.Weight, Region: target.Region}
		if target.Validation != nil {
			view.ValidationStatus = target.Validation.Status
		}
		out = append(out, view)
	}
	return out
}

func contractSummaryJSON(contract *ModelGroupContract) map[string]any {
	if contract == nil {
		return nil
	}
	return map[string]any{
		"supported_api_shapes": append([]string(nil), contract.SupportedAPIShapes...),
		"intended_workloads":   append([]string(nil), contract.IntendedWorkloads...),
	}
}

func (h *configAdminHandler) handleListGroups(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusConflict, "config-unavailable", "the active configuration is unavailable")
		return
	}
	mode := "database"
	if h.deps.FileMode || h.deps.DB == nil {
		mode = "file"
	}
	groups := make([]map[string]any, 0, len(cfg.Models))
	for _, name := range sortedGroupNames(cfg.Models) {
		group := cfg.Models[name]
		groups = append(groups, map[string]any{
			"name":     name,
			"strategy": group.Strategy,
			"targets":  configAdminTargetViews(group),
			"contract": contractSummaryJSON(group.Contract),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": mode, "runtime_scope": h.deps.RuntimeScope,
		"set_id": setID, "revision": revision, "groups": groups,
	})
}

func (h *configAdminHandler) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	groupName := r.PathValue("group")
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusConflict, "config-unavailable", "the active configuration is unavailable")
		return
	}
	group, exists := cfg.Models[groupName]
	if !exists {
		writeConfigAdminError(w, http.StatusNotFound, "group-not-found", "model group is not in the active configuration")
		return
	}
	// Eligible catalog models: the provider/model_ref inventory this group
	// could target. Catalog metadata only; no credentials.
	catalog := []map[string]any{}
	for _, providerName := range sortedKeys(cfg.Provider) {
		provider := cfg.Provider[providerName]
		for _, ref := range sortedKeys(provider.Models) {
			catalog = append(catalog, map[string]any{
				"provider": providerName, "model_ref": ref, "model": provider.Models[ref].Model,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": groupName, "strategy": group.Strategy, "targets": configAdminTargetViews(group),
		"contract": contractSummaryJSON(group.Contract), "set_id": setID, "revision": revision,
		"eligible_catalog_models": catalog,
	})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// configAdminTargetsRequest is the PUT swap body.
type configAdminTargetsRequest struct {
	Targets  []GroupTargetSpec `json:"targets"`
	Strategy string            `json:"strategy,omitempty"`
	Reason   string            `json:"reason,omitempty"`
	DryRun   bool              `json:"dry_run,omitempty"`
	Force    bool              `json:"force,omitempty"`
}

// configAdminAddTargetRequest is the POST add-target body.
type configAdminAddTargetRequest struct {
	Target   GroupTargetSpec `json:"target"`
	Position *int            `json:"position,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	DryRun   bool            `json:"dry_run,omitempty"`
	Force    bool            `json:"force,omitempty"`
}

func (h *configAdminHandler) writeResultWithPropagation(w http.ResponseWriter, status int, result *GroupTargetsWriteResult) {
	propagation := map[string]any{"revision": result.Revision}
	if !result.DryRun && !result.Replayed {
		if h.deps.Reload != nil {
			if err := h.deps.Reload(); err != nil {
				propagation["replica_reloaded"] = false
			} else {
				propagation["replica_reloaded"] = true
			}
		}
		loadedRevision, _, at := h.loadedRevision()
		propagation["replica"] = map[string]any{
			"instance_id": h.deps.InstanceID, "loaded_revision": loadedRevision, "loaded_at": at.UTC().Format(time.RFC3339Nano),
		}
	}
	body := map[string]any{
		"group":           result.Group,
		"set_id":          result.SetID,
		"previous_set_id": result.PreviousSetID,
		"revision":        result.Revision,
		"dry_run":         result.DryRun,
		"forced":          result.Forced,
		"replayed":        result.Replayed,
		"strategy":        result.Strategy,
		"diff":            result.Diff,
		"targets":         result.Targets,
		"propagation":     propagation,
	}
	if result.RestoredFromSet != "" {
		body["restored_from_set_id"] = result.RestoredFromSet
	}
	writeJSON(w, status, body)
}

func (h *configAdminHandler) mapWriteError(w http.ResponseWriter, err error) {
	var adminErr *ConfigAdminError
	if errors.As(err, &adminErr) {
		writeConfigAdminTypedError(w, adminErr)
		return
	}
	writeConfigAdminError(w, http.StatusInternalServerError, "config-write-failed", "the configuration write failed")
}

func (h *configAdminHandler) handleReplaceTargets(w http.ResponseWriter, r *http.Request) {
	h.handleTargetsWrite(w, r, configActionGroupTargetsReplace, http.StatusOK)
}

func (h *configAdminHandler) handleValidateTargets(w http.ResponseWriter, r *http.Request) {
	h.handleTargetsWrite(w, r, configActionGroupTargetsReplace, http.StatusOK)
}

func (h *configAdminHandler) handleTargetsWrite(w http.ResponseWriter, r *http.Request, action string, okStatus int) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body configAdminTargetsRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	expected, validIfMatch := parseIfMatch(r)
	idempotencyKey, validKey := parseIdempotencyKey(r)
	if !validIfMatch || !validKey {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match or Idempotency-Key header is invalid")
		return
	}
	if body.Force && !h.deps.Authorize(subject, adminPermissionConfigForce) {
		if h.deps.RecordAccess != nil {
			h.deps.RecordAccess(r, subject, http.StatusForbidden, "config-force-required", authzObjectAdminConfig, "force")
		}
		writeConfigAdminError(w, http.StatusForbidden, "config-force-required", "force requires the config:force permission")
		return
	}
	if strings.HasSuffix(r.URL.Path, ":validate") {
		body.DryRun = true
	}
	result, err := WriteGroupTargets(h.deps.DB, h.deps.RuntimeScope, GroupTargetsWriteRequest{
		Group: r.PathValue("group"), Targets: body.Targets, Strategy: body.Strategy,
		Reason: body.Reason, DryRun: body.DryRun, Force: body.Force,
		IdempotencyKey: idempotencyKey, ExpectedRevision: expected, Action: action,
	}, subject.subject, h.policy(), h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	h.writeResultWithPropagation(w, okStatus, result)
}

func (h *configAdminHandler) handleAddTarget(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body configAdminAddTargetRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	expected, validIfMatch := parseIfMatch(r)
	idempotencyKey, validKey := parseIdempotencyKey(r)
	if !validIfMatch || !validKey {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match or Idempotency-Key header is invalid")
		return
	}
	if body.Force && !h.deps.Authorize(subject, adminPermissionConfigForce) {
		writeConfigAdminError(w, http.StatusForbidden, "config-force-required", "force requires the config:force permission")
		return
	}
	groupName := r.PathValue("group")
	// Read the current group, insert the new target, and run the shared
	// write pipeline.
	cfg, _, _, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusConflict, "config-unavailable", "the active configuration is unavailable")
		return
	}
	group, exists := cfg.Models[groupName]
	if !exists {
		writeConfigAdminError(w, http.StatusNotFound, "group-not-found", "model group is not in the active configuration")
		return
	}
	targets := groupTargetSpecsFromGroup(group)
	insertAt := len(targets)
	if body.Position != nil {
		insertAt = *body.Position
		if insertAt < 0 || insertAt > len(targets) {
			writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "position is out of range")
			return
		}
	}
	targets = append(targets, GroupTargetSpec{})
	copy(targets[insertAt+1:], targets[insertAt:])
	targets[insertAt] = body.Target
	result, err := WriteGroupTargets(h.deps.DB, h.deps.RuntimeScope, GroupTargetsWriteRequest{
		Group: groupName, Targets: targets, Reason: body.Reason, DryRun: body.DryRun, Force: body.Force,
		IdempotencyKey: idempotencyKey, ExpectedRevision: expected, Action: configActionGroupTargetAdd,
	}, subject.subject, h.policy(), h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	h.writeResultWithPropagation(w, http.StatusOK, result)
}

func (h *configAdminHandler) handleRemoveTarget(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	expected, validIfMatch := parseIfMatch(r)
	idempotencyKey, validKey := parseIdempotencyKey(r)
	if !validIfMatch || !validKey {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match or Idempotency-Key header is invalid")
		return
	}
	groupName := r.PathValue("group")
	targetID := strings.TrimPrefix(r.PathValue("target_id"), "/")
	// target_id is "<provider>/<model_ref or model>"; the wildcard segment
	// keeps slashes in catalog model refs (for example hosted providers).
	provider, ref, found := strings.Cut(targetID, "/")
	if !found || provider == "" || ref == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "target_id must be <provider>/<model_ref or model>")
		return
	}
	cfg, _, _, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusConflict, "config-unavailable", "the active configuration is unavailable")
		return
	}
	group, exists := cfg.Models[groupName]
	if !exists {
		writeConfigAdminError(w, http.StatusNotFound, "group-not-found", "model group is not in the active configuration")
		return
	}
	targets := groupTargetSpecsFromGroup(group)
	kept := make([]GroupTargetSpec, 0, len(targets))
	matched := false
	for _, spec := range targets {
		specRef := spec.ModelRef
		if specRef == "" {
			specRef = spec.Model
		}
		if spec.Provider == provider && specRef == ref {
			matched = true
			continue
		}
		kept = append(kept, spec)
	}
	if !matched {
		writeConfigAdminError(w, http.StatusNotFound, "target-not-found", "the target is not in the group")
		return
	}
	var body struct {
		Reason string `json:"reason"`
		DryRun bool   `json:"dry_run"`
		Force  bool   `json:"force"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		if !decodeConfigAdminBody(w, r, &body) {
			return
		}
	}
	if body.Force && !h.deps.Authorize(subject, adminPermissionConfigForce) {
		writeConfigAdminError(w, http.StatusForbidden, "config-force-required", "force requires the config:force permission")
		return
	}
	result, err := WriteGroupTargets(h.deps.DB, h.deps.RuntimeScope, GroupTargetsWriteRequest{
		Group: groupName, Targets: kept, Reason: body.Reason, DryRun: body.DryRun, Force: body.Force,
		IdempotencyKey: idempotencyKey, ExpectedRevision: expected, Action: configActionGroupTargetRemove,
	}, subject.subject, h.policy(), h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	h.writeResultWithPropagation(w, http.StatusOK, result)
}

type configAdminRollbackRequest struct {
	SetID  string `json:"set_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (h *configAdminHandler) handleGroupRollback(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigActivate) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body configAdminRollbackRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeConfigAdminBody(w, r, &body) {
			return
		}
	}
	result, err := RollbackGroup(h.deps.DB, h.deps.RuntimeScope, r.PathValue("group"), body.SetID, body.Reason, subject.subject, h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	h.writeResultWithPropagation(w, http.StatusOK, result)
}

func (h *configAdminHandler) handleGroupHistory(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	if h.deps.FileMode || h.deps.DB == nil {
		// File mode has no audit store; an empty history is the honest answer.
		writeJSON(w, http.StatusOK, map[string]any{"group": r.PathValue("group"), "mode": "file", "events": []any{}})
		return
	}
	events, err := ListGroupHistory(h.deps.DB, h.deps.RuntimeScope, r.PathValue("group"), 100)
	if err != nil {
		writeConfigAdminError(w, http.StatusInternalServerError, "config-read-failed", "the group history could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": r.PathValue("group"), "mode": "database", "events": events})
}

func (h *configAdminHandler) handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	mode := "database"
	if h.deps.FileMode || h.deps.DB == nil {
		mode = "file"
	}
	loadedRevision, loadedSet, loadedAt := h.loadedRevision()
	status := map[string]any{
		"mode":          mode,
		"runtime_scope": h.deps.RuntimeScope,
		"driver":        h.deps.Driver,
		"replica": map[string]any{
			"instance_id":     h.deps.InstanceID,
			"loaded_revision": loadedRevision,
			"loaded_set_id":   loadedSet,
			"loaded_at":       loadedAt.UTC().Format(time.RFC3339Nano),
		},
	}
	if mode == "database" {
		revision, err := currentRevisionForScope(h.deps.DB, h.deps.RuntimeScope)
		if err == nil {
			status["revision"] = revision
			status["replica"].(map[string]any)["current"] = revision == loadedRevision
		}
		var runtimeRows []configControlPlaneRuntimeRow
		if err := h.deps.DB.Where("runtime_scope = ?", h.deps.RuntimeScope).Limit(1).Find(&runtimeRows).Error; err == nil && len(runtimeRows) == 1 {
			status["active_set_id"] = runtimeRows[0].ActiveSetID
		}
	}
	writeJSON(w, http.StatusOK, status)
}

type configAdminImportRequest struct {
	Name   string `json:"name"`
	YAML   string `json:"yaml"`
	Reason string `json:"reason,omitempty"`
}

func (h *configAdminHandler) handleImportSet(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigActivate) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body configAdminImportRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.YAML) == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "name and yaml are required")
		return
	}
	idempotencyKey, validKey := parseIdempotencyKey(r)
	if !validKey {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "Idempotency-Key header is invalid")
		return
	}
	setID, err := importConfigSetForAPI(h.deps.DB, h.deps.RuntimeScope, body.Name, []byte(body.YAML), subject.subject, body.Reason, idempotencyKey, h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"set_id": setID, "runtime_scope": h.deps.RuntimeScope, "name": body.Name, "status": "draft"})
}

// importConfigSetForAPI imports a canonical document as a draft set and
// records the set_import event with phase-8 metadata.
func importConfigSetForAPI(db *gorm.DB, runtimeScope, name string, yamlBytes []byte, actor, reason, idempotencyKey string, now time.Time) (string, error) {
	if err := rejectRawSecretsInYAML(yamlBytes); err != nil {
		return "", configAdminError(400, "config-secret-refused", "the document contains credential-shaped material; use environment variable references")
	}
	cleaned, hadDeprecatedLicense, err := discardDeprecatedServerLicenseYAML(yamlBytes)
	if err != nil {
		return "", configAdminError(400, "config-invalid-request", "the document is not valid YAML")
	}
	if hadDeprecatedLicense {
		return "", configAdminError(400, "config-invalid-request", "server.license is rejected; remove the block")
	}
	var cfg Config
	if err := yaml.Unmarshal(cleaned, &cfg); err != nil {
		return "", configAdminError(400, "config-invalid-request", "the document is not a valid router configuration")
	}
	cfg.setDefaults()
	if err := cfg.ValidateForImport(); err != nil {
		return "", configAdminError(422, "config-invalid", "the document fails validation: "+truncateSafeDetail(err.Error()))
	}
	var setID string
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigAdminWriteSchema(tx); err != nil {
			return err
		}
		if key := strings.TrimSpace(idempotencyKey); key != "" {
			replay, conflict, err := lookupIdempotentGroupWrite(tx, runtimeScope, key, "import:"+name, "")
			if err != nil {
				return err
			}
			if conflict {
				return configAdminError(409, "idempotency-key-conflict", "Idempotency-Key was already used with a different request")
			}
			if replay != nil {
				setID = replay.SetID
				return nil
			}
		}
		id, err := importCanonicalDocumentTx(tx, runtimeScope, name, cleaned, &cfg, actor, now)
		if err != nil {
			return err
		}
		revision, _ := currentRevisionForScope(tx, runtimeScope)
		if _, err := insertConfigChangeEventTx(tx, runtimeScope, id, configActionSetImport, actor, revision, configChangeEventMeta{
			Reason: reason, IdempotencyKey: idempotencyKey, RequestHash: "import:" + name,
		}, now); err != nil {
			return err
		}
		setID = id
		return nil
	})
	if err != nil {
		return "", err
	}
	return setID, nil
}

func (h *configAdminHandler) handleListSets(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	if h.deps.FileMode || h.deps.DB == nil {
		writeConfigAdminError(w, http.StatusConflict, "config_source_mode_file", "configuration sets exist only in database mode")
		return
	}
	var rows []struct {
		ID               string `gorm:"column:id"`
		Name             string `gorm:"column:name"`
		Status           string `gorm:"column:status"`
		ValidationStatus string `gorm:"column:validation_status"`
		CreatedBy        string `gorm:"column:created_by"`
		CreatedAt        string `gorm:"column:created_at"`
		ActivatedAt      string `gorm:"column:activated_at"`
	}
	if err := h.deps.DB.Table("router_config_sets").
		Select("id", "name", "status", "validation_status", "created_by", "created_at", "activated_at").
		Where("runtime_scope = ?", h.deps.RuntimeScope).
		Order("created_at DESC").Limit(200).Find(&rows).Error; err != nil {
		writeConfigAdminError(w, http.StatusInternalServerError, "config-read-failed", "the configuration sets could not be read")
		return
	}
	sets := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		sets = append(sets, map[string]any{
			"set_id": row.ID, "name": row.Name, "status": row.Status,
			"validation_status": row.ValidationStatus, "created_by": row.CreatedBy,
			"created_at": row.CreatedAt, "activated_at": row.ActivatedAt,
		})
	}
	revision, _ := currentRevisionForScope(h.deps.DB, h.deps.RuntimeScope)
	writeJSON(w, http.StatusOK, map[string]any{"sets": sets, "revision": revision, "runtime_scope": h.deps.RuntimeScope})
}

func (h *configAdminHandler) handleExportSet(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	if h.deps.FileMode || h.deps.DB == nil {
		writeConfigAdminError(w, http.StatusConflict, "config_source_mode_file", "configuration sets exist only in database mode")
		return
	}
	setID := r.PathValue("set_id")
	var raw []byte
	var err error
	if setID == "active" {
		raw, err = LoadActiveCanonicalYAML(h.deps.DB, h.deps.RuntimeScope)
	} else {
		raw, err = ExportCanonicalYAML(h.deps.DB, setID)
	}
	if err != nil {
		writeConfigAdminError(w, http.StatusNotFound, "config-set-not-found", "the configuration set does not exist")
		return
	}
	// The stored document is unexpanded: environment references appear
	// verbatim and no resolved secret is ever returned.
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// Stored canonical YAML config exported verbatim with an explicit application/yaml
	// content type and X-Content-Type-Options: nosniff; it is never interpreted
	// as HTML (metrum-ai/router#412).
	_, _ = w.Write(raw) // nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter
}

func (h *configAdminHandler) handleActivateSet(w http.ResponseWriter, r *http.Request) {
	h.handleSetActivation(w, r, configActionSetActivate)
}

func (h *configAdminHandler) handleSetRollback(w http.ResponseWriter, r *http.Request) {
	h.handleSetActivation(w, r, configActionSetRollback)
}

func (h *configAdminHandler) handleSetActivation(w http.ResponseWriter, r *http.Request, action string) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigActivate) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	setID := r.PathValue("set_id")
	var body configAdminRollbackRequest
	if r.Body != nil && r.ContentLength > 0 {
		if !decodeConfigAdminBody(w, r, &body) {
			return
		}
	}
	if action == configActionSetRollback {
		setID = strings.TrimSpace(body.SetID)
		if setID == "" {
			writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "set_id is required")
			return
		}
	}
	expected, validIfMatch := parseIfMatch(r)
	if !validIfMatch {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	revision, err := activateConfigSetForAPI(h.deps.DB, h.deps.RuntimeScope, setID, action, subject.subject, body.Reason, expected, h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		_ = h.deps.Reload()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"set_id": setID, "runtime_scope": h.deps.RuntimeScope, "status": "active", "action": action, "revision": revision,
	})
}

// activateConfigSetForAPI activates (or rolls back to) a set with the guarded
// runtime update and records the set_activate/set_rollback event.
func activateConfigSetForAPI(db *gorm.DB, runtimeScope, setID, action, actor, reason string, expected *int64, now time.Time) (int64, error) {
	var revision int64
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := lockConfigRuntimeTx(tx, runtimeScope); err != nil {
			return err
		}
		if err := ensureConfigAdminWriteSchema(tx); err != nil {
			return err
		}
		state, _, err := loadConfigAdminRuntime(tx, runtimeScope)
		if err != nil && expected == nil {
			// First activation for the scope: allow an empty runtime row.
			var adminErr *ConfigAdminError
			if !errors.As(err, &adminErr) {
				return err
			}
			state = configAdminRuntimeState{}
		}
		if expected != nil && *expected != state.Revision {
			return &ConfigAdminError{Status: 409, Type: "config-revision-conflict", Message: "If-Match revision is stale", Revision: state.Revision}
		}
		rev, err := activateConfigSetGuardedTx(tx, runtimeScope, setID, state.Revision, now)
		if err != nil {
			return err
		}
		if _, err := insertConfigChangeEventTx(tx, runtimeScope, setID, action, actor, rev, configChangeEventMeta{
			PrevSetID: state.ActiveSetID, Reason: reason,
		}, now); err != nil {
			return err
		}
		revision = rev
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

func (h *configAdminHandler) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	if h.deps.FileMode || h.deps.DB == nil {
		writeConfigAdminError(w, http.StatusConflict, "config_source_mode_file", "configuration diffs exist only in database mode")
		return
	}
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	if to == "" {
		to = "active"
	}
	load := func(setID string) (*Config, string, error) {
		raw, err := ExportCanonicalYAML(h.deps.DB, setID)
		if err != nil {
			return nil, "", err
		}
		var cfg Config
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, "", err
		}
		cfg.setDefaults()
		return &cfg, setID, nil
	}
	if from == "" {
		// Default: the set the current one superseded.
		var events []struct {
			PrevSetID string `gorm:"column:prev_config_set_id"`
		}
		if err := h.deps.DB.Table("router_config_change_events").
			Select("prev_config_set_id").
			Where("runtime_scope = ? AND prev_config_set_id != ''", h.deps.RuntimeScope).
			Order("at DESC").Limit(1).Find(&events).Error; err == nil && len(events) == 1 {
			from = events[0].PrevSetID
		}
		if from == "" {
			writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "from is required when no prior set is recorded")
			return
		}
	}
	if to == "active" {
		var sets []configSetRow
		if err := h.deps.DB.Where("runtime_scope = ? AND status = ?", h.deps.RuntimeScope, "active").Limit(1).Find(&sets).Error; err != nil || len(sets) != 1 {
			writeConfigAdminError(w, http.StatusConflict, "config-no-active-set", "no active configuration set for the runtime scope")
			return
		}
		to = sets[0].ID
	}
	fromCfg, _, err := load(from)
	if err != nil {
		writeConfigAdminError(w, http.StatusNotFound, "config-set-not-found", "the from configuration set does not exist")
		return
	}
	toCfg, _, err := load(to)
	if err != nil {
		writeConfigAdminError(w, http.StatusNotFound, "config-set-not-found", "the to configuration set does not exist")
		return
	}
	writeJSON(w, http.StatusOK, configAdminDiff(from, fromCfg, to, toCfg))
}

// configAdminDiff produces a group/provider/caller diff between two sets.
func configAdminDiff(fromID string, from *Config, toID string, to *Config) map[string]any {
	groups := map[string]any{}
	for _, name := range sortedGroupNames(from.Models) {
		before := groupTargetSpecsFromGroup(from.Models[name])
		afterGroup, ok := to.Models[name]
		if !ok {
			groups[name] = map[string]any{"removed": true}
			continue
		}
		after := groupTargetSpecsFromGroup(afterGroup)
		diff := diffGroupTargetSpecs(before, after)
		if len(diff.Added) > 0 || len(diff.Removed) > 0 || len(diff.Reordered) > 0 || from.Models[name].Strategy != afterGroup.Strategy {
			diff.StrategyFrom = from.Models[name].Strategy
			diff.StrategyTo = afterGroup.Strategy
			diff.StrategyChange = from.Models[name].Strategy != afterGroup.Strategy
			groups[name] = diff
		}
	}
	for _, name := range sortedGroupNames(to.Models) {
		if _, ok := from.Models[name]; !ok {
			groups[name] = map[string]any{"added": true}
		}
	}
	return map[string]any{
		"from": fromID, "to": toID, "groups": groups,
		"provider_count": map[string]int{"from": len(from.Provider), "to": len(to.Provider)},
		"caller_count":   map[string]int{"from": len(from.Callers), "to": len(to.Callers)},
	}
}
