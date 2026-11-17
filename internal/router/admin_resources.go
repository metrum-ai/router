// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Resource administration routes are added through the shared contract and
// config revision path. This file owns safe resource representations.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

func (h *configAdminHandler) handleListProviders(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProvidersRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	names := make([]string, 0, len(cfg.Provider))
	for name := range cfg.Provider {
		names = append(names, name)
	}
	sort.Strings(names)
	providers := make([]map[string]any, 0, len(names))
	for _, name := range names {
		providers = append(providers, map[string]any{"id": name, "provider": safeAdminProvider(cfg.Provider[name])})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers, "set_id": setID, "revision": revision})
}

func (h *configAdminHandler) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProvidersRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	name := r.PathValue("provider")
	provider, found := cfg.Provider[name]
	if !found {
		writeConfigAdminError(w, http.StatusNotFound, "provider-not-found", "provider is not in the active configuration")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"id": name, "provider": safeAdminProvider(provider), "set_id": setID, "revision": revision})
}

func (h *configAdminHandler) handleListModelCatalog(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionCatalogRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	providers := make([]string, 0, len(cfg.Provider))
	for name := range cfg.Provider {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	models := make([]map[string]any, 0)
	for _, providerName := range providers {
		provider := cfg.Provider[providerName]
		references := make([]string, 0, len(provider.Models))
		for reference := range provider.Models {
			references = append(references, reference)
		}
		sort.Strings(references)
		for _, reference := range references {
			model := provider.Models[reference]
			dialect := model.Dialect
			if dialect == "" {
				dialect = provider.Dialect
			}
			models = append(models, map[string]any{"provider": providerName, "model_ref": reference, "model": model, "dialect": dialect})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "set_id": setID, "revision": revision})
}

type adminProviderWriteRequest struct {
	BaseURL      string                   `json:"base_url"`
	Dialect      string                   `json:"dialect"`
	UpstreamKind string                   `json:"upstream_kind,omitempty"`
	APIKeyEnv    string                   `json:"api_key_env,omitempty"`
	KeyID        string                   `json:"key_id,omitempty"`
	AuthScheme   string                   `json:"auth_scheme,omitempty"`
	Headers      map[string]string        `json:"headers,omitempty"`
	Models       map[string]ProviderModel `json:"models,omitempty"`
}

type adminCallerQuotaRequest struct {
	Quota QuotaConfig `json:"quota"`
}

type adminRetentionWriteRequest struct {
	Retention RetentionConfig `json:"retention"`
	Force     bool            `json:"force,omitempty"`
}

type adminProjectWriteRequest struct {
	Name         string                     `json:"name,omitempty"`
	Status       string                     `json:"status,omitempty"`
	Description  string                     `json:"description,omitempty"`
	Environments []ProjectEnvironmentConfig `json:"environments,omitempty"`
	Budget       *ProjectBudgetConfig       `json:"budget,omitempty"`
}

type adminMembershipWriteRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	Status string `json:"status,omitempty"`
}

func (h *configAdminHandler) handleListProjects(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProjectsRead) || !h.authorize(w, r, subject, adminPermissionBudgetsRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"projects": cfg.Projects, "set_id": setID, "revision": revision})
}

func (h *configAdminHandler) handleGetProject(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProjectsRead) || !h.authorize(w, r, subject, adminPermissionBudgetsRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	id := normalizeAccountID(r.PathValue("project"))
	for _, project := range cfg.Projects {
		if project.ID == id {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"project": project, "set_id": setID, "revision": revision})
			return
		}
	}
	writeConfigAdminError(w, http.StatusNotFound, "project-not-found", "project is not in the active configuration")
}

func (h *configAdminHandler) handlePutProject(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProjectsWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	id := normalizeAccountID(r.PathValue("project"))
	if id == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "project id is invalid")
		return
	}
	var body adminProjectWriteRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	if body.Budget != nil && !h.authorize(w, r, subject, adminPermissionBudgetsWrite) {
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminProjects, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	project := ProjectConfig{ID: id, Name: strings.TrimSpace(body.Name), Status: normalizeStatusDefault(body.Status), Description: strings.TrimSpace(body.Description), Environments: body.Environments}
	if body.Budget != nil {
		project.Budget = *body.Budget
	}
	for i := range project.Environments {
		project.Environments[i].ID = normalizeAccountID(project.Environments[i].ID)
		project.Environments[i].Status = normalizeStatusDefault(project.Environments[i].Status)
	}
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:project:"+id, h.now(), func(cfg *Config) error {
		materializeSyntheticAccounts(cfg)
		for i := range cfg.Projects {
			if normalizeAccountID(cfg.Projects[i].ID) == id {
				if body.Budget == nil {
					project.Budget = cfg.Projects[i].Budget
				}
				project.ForbidLongLivedKeys = cfg.Projects[i].ForbidLongLivedKeys
				cfg.Projects[i] = project
				return nil
			}
		}
		cfg.Projects = append(cfg.Projects, project)
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "revision": result.Revision, "set_id": result.SetID})
}

func (h *configAdminHandler) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProjectsWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	projectID := normalizeAccountID(r.PathValue("project"))
	if projectID == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "project id is invalid")
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminProjects, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	deletedMemberships := 0
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:project-delete:"+projectID, h.now(), func(cfg *Config) error {
		materializeSyntheticAccounts(cfg)
		for _, caller := range cfg.Callers {
			if normalizeAccountID(caller.Project) == projectID {
				return configAdminError(http.StatusConflict, "project-in-use", "project is referenced by an active caller")
			}
		}
		index := -1
		for i := range cfg.Projects {
			if normalizeAccountID(cfg.Projects[i].ID) == projectID {
				index = i
				break
			}
		}
		if index < 0 {
			return configAdminError(http.StatusNotFound, "project-not-found", "project is not in the active configuration")
		}
		cfg.Projects = append(cfg.Projects[:index], cfg.Projects[index+1:]...)
		kept := cfg.ProjectMemberships[:0]
		for _, membership := range cfg.ProjectMemberships {
			if normalizeAccountID(membership.Project) == projectID {
				deletedMemberships++
				continue
			}
			kept = append(kept, membership)
		}
		cfg.ProjectMemberships = kept
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted_project": projectID, "deleted_memberships": deletedMemberships, "revision": result.Revision, "set_id": result.SetID})
}

func (h *configAdminHandler) handleDeleteProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProjectsWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	projectID := normalizeAccountID(r.PathValue("project"))
	environmentID := normalizeAccountID(r.PathValue("environment"))
	if projectID == "" || environmentID == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "project and environment ids are required")
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminProjects, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	deletedMemberships := 0
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:environment-delete:"+projectID+":"+environmentID, h.now(), func(cfg *Config) error {
		materializeSyntheticAccounts(cfg)
		for _, caller := range cfg.Callers {
			if normalizeAccountID(caller.Project) == projectID && normalizeAccountID(caller.Environment) == environmentID {
				return configAdminError(http.StatusConflict, "environment-in-use", "environment is referenced by an active caller")
			}
		}
		projectIndex := -1
		environmentIndex := -1
		for i := range cfg.Projects {
			if normalizeAccountID(cfg.Projects[i].ID) != projectID {
				continue
			}
			projectIndex = i
			for j := range cfg.Projects[i].Environments {
				if normalizeAccountID(cfg.Projects[i].Environments[j].ID) == environmentID {
					environmentIndex = j
					break
				}
			}
			break
		}
		if projectIndex < 0 || environmentIndex < 0 {
			return configAdminError(http.StatusNotFound, "environment-not-found", "project environment is not in the active configuration")
		}
		if len(cfg.Projects[projectIndex].Environments) == 1 {
			return configAdminError(http.StatusConflict, "last-project-environment", "delete the project instead of removing its last environment")
		}
		cfg.Projects[projectIndex].Environments = append(cfg.Projects[projectIndex].Environments[:environmentIndex], cfg.Projects[projectIndex].Environments[environmentIndex+1:]...)
		kept := cfg.ProjectMemberships[:0]
		for _, membership := range cfg.ProjectMemberships {
			if normalizeAccountID(membership.Project) == projectID && normalizeAccountID(membership.Environment) == environmentID {
				deletedMemberships++
				continue
			}
			kept = append(kept, membership)
		}
		cfg.ProjectMemberships = kept
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted_environment": map[string]string{"project": projectID, "environment": environmentID}, "deleted_memberships": deletedMemberships, "revision": result.Revision, "set_id": result.SetID})
}

func materializeSyntheticAccounts(cfg *Config) {
	if len(cfg.Users)+len(cfg.Projects)+len(cfg.ProjectMemberships) != 0 {
		return
	}
	for _, caller := range cfg.Callers {
		owner, err := callerOwnerUser(caller)
		if err != nil || owner == "" || caller.Project == "" {
			return
		}
	}
	users, projects, memberships := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, caller := range cfg.Callers {
		owner, err := callerOwnerUser(caller)
		if err != nil || owner == "" || caller.Project == "" {
			continue
		}
		project := normalizeAccountID(caller.Project)
		owner = normalizeAccountID(owner)
		if !users[owner] {
			cfg.Users = append(cfg.Users, UserConfig{ID: owner, Type: "human", Status: "active"})
			users[owner] = true
		}
		environment := normalizeAccountID(caller.Environment)
		if !projects[project] {
			projectConfig := ProjectConfig{ID: project, Status: "active"}
			if environment != "" {
				projectConfig.Environments = []ProjectEnvironmentConfig{{ID: environment, Status: "active"}}
			}
			cfg.Projects = append(cfg.Projects, projectConfig)
			projects[project] = true
		} else if environment != "" {
			for i := range cfg.Projects {
				if normalizeAccountID(cfg.Projects[i].ID) == project && !projectHasEnvironment(cfg.Projects[i], environment) {
					cfg.Projects[i].Environments = append(cfg.Projects[i].Environments, ProjectEnvironmentConfig{ID: environment, Status: "active"})
				}
			}
		}
		key := owner + "\x00" + project + "\x00" + environment
		if !memberships[key] {
			cfg.ProjectMemberships = append(cfg.ProjectMemberships, ProjectMembershipConfig{UserID: owner, Project: project, Environment: normalizeAccountID(caller.Environment), Role: "member", Status: "active"})
			memberships[key] = true
		}
	}
}

func (h *configAdminHandler) handlePutMembership(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionMembershipsWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	projectID, environment := normalizeAccountID(r.PathValue("project")), normalizeAccountID(r.PathValue("environment"))
	var body adminMembershipWriteRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	if normalizeAccountID(body.UserID) == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "user_id is required")
		return
	}
	role := normalizeRoleDefault(body.Role)
	if !knownProjectMembershipRole(role) {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "role is not supported")
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminMemberships, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	userID := normalizeAccountID(body.UserID)
	membership := ProjectMembershipConfig{UserID: userID, Project: projectID, Environment: environment, Role: role, Status: normalizeStatusDefault(body.Status), Source: "admin-api", JoinedAt: h.now().UTC().Format(time.RFC3339Nano)}
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:membership:"+userID+":"+projectID+":"+environment, h.now(), func(cfg *Config) error {
		materializeSyntheticAccounts(cfg)
		projectFound := false
		for _, project := range cfg.Projects {
			if normalizeAccountID(project.ID) == projectID && projectHasEnvironment(project, environment) {
				projectFound = true
				break
			}
		}
		if !projectFound {
			return configAdminError(http.StatusNotFound, "membership-domain-not-found", "membership grant targets an unconfigured project environment")
		}
		userFound := false
		for _, user := range cfg.Users {
			if normalizeAccountID(user.ID) == userID {
				userFound = true
				break
			}
		}
		if !userFound {
			return configAdminError(http.StatusNotFound, "user-not-found", "user is not in the active configuration")
		}
		for i := range cfg.ProjectMemberships {
			if membershipKey(normalizeAccountID(cfg.ProjectMemberships[i].UserID), normalizeAccountID(cfg.ProjectMemberships[i].Project), normalizeAccountID(cfg.ProjectMemberships[i].Environment)) == membershipKey(userID, projectID, environment) {
				membership.JoinedAt = cfg.ProjectMemberships[i].JoinedAt
				cfg.ProjectMemberships[i] = membership
				return nil
			}
		}
		cfg.ProjectMemberships = append(cfg.ProjectMemberships, membership)
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"membership": membership, "revision": result.Revision, "set_id": result.SetID})
}

func knownProjectMembershipRole(role string) bool {
	switch role {
	case "admin", "auditor", "developer", "member", "operator", "owner", "viewer":
		return true
	default:
		return false
	}
}

func (h *configAdminHandler) handleDeleteMembership(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionMembershipsWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	projectID := normalizeAccountID(r.PathValue("project"))
	environmentID := normalizeAccountID(r.PathValue("environment"))
	userID := normalizeAccountID(r.PathValue("user_id"))
	if projectID == "" || environmentID == "" || userID == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "project, environment, and user ids are required")
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminMemberships, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:membership-delete:"+userID+":"+projectID+":"+environmentID, h.now(), func(cfg *Config) error {
		projectFound := false
		for _, project := range cfg.Projects {
			if normalizeAccountID(project.ID) == projectID && projectHasEnvironment(project, environmentID) {
				projectFound = true
				break
			}
		}
		if !projectFound {
			return configAdminError(http.StatusNotFound, "membership-domain-not-found", "membership grant targets an unconfigured project environment")
		}
		for i := range cfg.ProjectMemberships {
			membership := cfg.ProjectMemberships[i]
			if normalizeAccountID(membership.UserID) == userID && normalizeAccountID(membership.Project) == projectID && normalizeAccountID(membership.Environment) == environmentID {
				cfg.ProjectMemberships = append(cfg.ProjectMemberships[:i], cfg.ProjectMemberships[i+1:]...)
				return nil
			}
		}
		return configAdminError(http.StatusNotFound, "membership-not-found", "membership is not in the active configuration")
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted_membership": map[string]string{"user_id": userID, "project": projectID, "environment": environmentID}, "revision": result.Revision, "set_id": result.SetID})
}

func (h *configAdminHandler) handleListMemberships(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionMembershipsRead) {
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	projectID, environment := normalizeAccountID(r.PathValue("project")), normalizeAccountID(r.PathValue("environment"))
	if projectID == "" || environment == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "project and environment are required")
		return
	}
	var projectFound bool
	for _, project := range cfg.Projects {
		if normalizeAccountID(project.ID) == projectID && projectHasEnvironment(project, environment) {
			projectFound = true
			break
		}
	}
	if !projectFound {
		writeConfigAdminError(w, http.StatusNotFound, "membership-domain-not-found", "project environment is not configured")
		return
	}
	memberships := make([]ProjectMembershipConfig, 0)
	for _, membership := range cfg.ProjectMemberships {
		if normalizeAccountID(membership.Project) == projectID && normalizeAccountID(membership.Environment) == environment {
			memberships = append(memberships, membership)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"memberships": memberships, "set_id": setID, "revision": revision})
}

func (h *configAdminHandler) handlePutRetention(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionRetentionWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body adminRetentionWriteRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminRetention, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	shortened := false
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:retention", h.now(), func(cfg *Config) error {
		shortened = retentionPolicyShortened(cfg.Server.Retention, body.Retention)
		if shortened && (h.deps.Authorize == nil || !h.deps.Authorize(subject, adminPermissionRetentionForce)) {
			return configAdminError(http.StatusForbidden, "retention-destructive-permission-required", "a retention change that can delete more data requires admin:retention force permission")
		}
		cfg.Server.Retention = body.Retention
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"retention": body.Retention, "revision": result.Revision, "set_id": result.SetID, "shortened": shortened})
}

func retentionPolicyShortened(before, after RetentionConfig) bool {
	if after.Enabled && !before.Enabled {
		return true
	}
	if after.Enabled && retentionDryRun(before) && !retentionDryRun(after) {
		return true
	}
	oldClasses := make(map[string]RetentionClassConfig, len(before.Classes))
	for _, class := range before.Classes {
		oldClasses[normalizeRetentionDataClass(class.DataClass)] = class
	}
	for _, class := range after.Classes {
		if !after.Enabled || !retentionClassEnabled(class) {
			continue
		}
		old, found := oldClasses[normalizeRetentionDataClass(class.DataClass)]
		if !found || !retentionClassEnabled(old) || class.RetentionDays < old.RetentionDays || old.RequireFinalizedRollup && !class.RequireFinalizedRollup {
			return true
		}
	}
	if after.Enabled {
		newClasses := make(map[string]RetentionClassConfig, len(after.Classes))
		for _, class := range after.Classes {
			newClasses[normalizeRetentionDataClass(class.DataClass)] = class
		}
		for _, old := range before.Classes {
			if retentionClassEnabled(old) {
				updated, found := newClasses[normalizeRetentionDataClass(old.DataClass)]
				if !found || !retentionClassEnabled(updated) {
					return true
				}
			}
		}
	}
	return false
}

func retentionDryRun(cfg RetentionConfig) bool {
	return cfg.DryRun == nil || *cfg.DryRun
}

func (h *configAdminHandler) handleUpdateCallerQuota(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionQuotasWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	callerID := strings.TrimSpace(r.PathValue("id"))
	if callerID == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "caller id is required")
		return
	}
	var body adminCallerQuotaRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminQuotas, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:quota:"+callerID, h.now(), func(cfg *Config) error {
		for i := range cfg.Callers {
			if cfg.Callers[i].ID == callerID {
				cfg.Callers[i].Quota = body.Quota
				return nil
			}
		}
		return configAdminError(http.StatusNotFound, "caller-not-found", "caller is not in the active configuration")
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"caller_id": callerID, "quota": body.Quota, "revision": result.Revision, "set_id": result.SetID})
}

func (h *configAdminHandler) handleGetCallerQuota(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionQuotasRead) {
		return
	}
	callerID := strings.TrimSpace(r.PathValue("id"))
	if callerID == "" {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "caller id is required")
		return
	}
	cfg, setID, revision, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	for _, caller := range cfg.Callers {
		if caller.ID == callerID {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"caller_id": callerID, "quota": caller.Quota, "revision": revision, "set_id": setID})
			return
		}
	}
	writeConfigAdminError(w, http.StatusNotFound, "caller-not-found", "caller is not in the active configuration")
}

func (h *configAdminHandler) handlePutProvider(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProvidersWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	name := strings.TrimSpace(r.PathValue("provider"))
	if name == "" || strings.ContainsAny(name, "/\r\n") {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "provider name is invalid")
		return
	}
	var body adminProviderWriteRequest
	if !decodeAdminProviderBody(w, r, &body) {
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminProviders, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	provider := ProviderConfig{BaseURL: body.BaseURL, Dialect: body.Dialect, UpstreamKind: body.UpstreamKind, APIKeyEnv: body.APIKeyEnv, KeyID: body.KeyID, AuthScheme: body.AuthScheme, Headers: body.Headers, Models: body.Models}
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:provider:"+name, h.now(), func(cfg *Config) error {
		if cfg.Provider == nil {
			cfg.Provider = map[string]ProviderConfig{}
		}
		if existing, found := cfg.Provider[name]; found {
			provider.TrafficShape = existing.TrafficShape
			provider.SpendBreaker = existing.SpendBreaker
			provider.PropagateTrace = existing.PropagateTrace
		}
		cfg.Provider[name] = provider
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": name, "revision": result.Revision, "set_id": result.SetID, "provider": safeAdminProvider(provider)})
}

func (h *configAdminHandler) handleValidateProvider(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProvidersWrite) {
		return
	}
	name := strings.TrimSpace(r.PathValue("provider"))
	if name == "" || strings.ContainsAny(name, "/\r\n") {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "provider name is invalid")
		return
	}
	var body adminProviderWriteRequest
	if !decodeAdminProviderBody(w, r, &body) {
		return
	}
	active, _, _, err := h.activeConfigForRead()
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "config-unavailable", "the active configuration is unavailable")
		return
	}
	encoded, err := yaml.Marshal(active)
	if err != nil {
		writeConfigAdminError(w, http.StatusInternalServerError, "config-read-failed", "the active configuration could not be validated")
		return
	}
	var cfg Config
	if err := yaml.Unmarshal(encoded, &cfg); err != nil {
		writeConfigAdminError(w, http.StatusInternalServerError, "config-read-failed", "the active configuration could not be validated")
		return
	}
	if cfg.Provider == nil {
		cfg.Provider = map[string]ProviderConfig{}
	}
	cfg.Provider[name] = ProviderConfig{BaseURL: body.BaseURL, Dialect: body.Dialect, UpstreamKind: body.UpstreamKind, APIKeyEnv: body.APIKeyEnv, KeyID: body.KeyID, AuthScheme: body.AuthScheme, Headers: body.Headers, Models: body.Models}
	if err := cfg.validate(false); err != nil {
		writeConfigAdminError(w, http.StatusUnprocessableEntity, "config-invalid", "the proposed provider or model capabilities are invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "provider": safeAdminProvider(cfg.Provider[name])})
}

func (h *configAdminHandler) handleDisableProvider(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok || !h.authorize(w, r, subject, adminPermissionProvidersWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	name := strings.TrimSpace(r.PathValue("provider"))
	if name == "" || strings.ContainsAny(name, "/\r\n") {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "provider name is invalid")
		return
	}
	expected, valid := parseIfMatch(r)
	if !valid {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "If-Match header is invalid")
		return
	}
	audit, err := adminAuditMetaForWrite(r, subject, authzObjectAdminProviders, "write")
	if err != nil {
		writeConfigAdminError(w, http.StatusServiceUnavailable, "audit-unavailable", "the audit store is unavailable")
		return
	}
	result, err := writeAdminConfigRevision(h.deps.DB, h.deps.RuntimeScope, subject.subject, *expected, audit, "admin-api:provider-disable:"+name, h.now(), func(cfg *Config) error {
		if _, found := cfg.Provider[name]; !found {
			return configAdminError(http.StatusNotFound, "provider-not-found", "provider is not in the active configuration")
		}
		for _, group := range cfg.Models {
			for _, target := range group.Targets {
				if target.Provider == name {
					return configAdminError(http.StatusConflict, "provider-in-use", "provider is referenced by a model group")
				}
			}
		}
		delete(cfg.Provider, name)
		return nil
	})
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.Reload != nil {
		if err := h.deps.Reload(); err != nil {
			writeConfigAdminError(w, http.StatusServiceUnavailable, "config-reload-failed", "the configuration was saved but this replica could not reload it")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": name, "status": "disabled", "revision": result.Revision, "set_id": result.SetID})
}

func decodeAdminProviderBody(w http.ResponseWriter, r *http.Request, value any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdminConfigBodyBytes+1))
	if err != nil || len(body) > maxAdminConfigBodyBytes || len(bytes.TrimSpace(body)) == 0 {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "request body is unreadable, empty, or too large")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeConfigAdminError(w, http.StatusBadRequest, "config-invalid-request", "request body is not a valid provider document")
		return false
	}
	var generic any
	if json.Unmarshal(body, &generic) == nil && jsonContainsSecretShape(generic) {
		writeConfigAdminError(w, http.StatusBadRequest, "config-secret-refused", "provider credentials must use api_key_env references")
		return false
	}
	return true
}

type adminConfigWriteResult struct {
	Revision int64
	SetID    string
}

func writeAdminConfigRevision(db *gorm.DB, runtimeScope, actor string, expected int64, audit AdminAuditMeta, name string, now time.Time, mutate func(*Config) error) (adminConfigWriteResult, error) {
	if db == nil || mutate == nil {
		return adminConfigWriteResult{}, configAdminError(http.StatusConflict, "config_source_mode_file", "configuration writes require database mode")
	}
	var result adminConfigWriteResult
	err := withSQLiteBusyRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockConfigRuntimeTx(tx, runtimeScope); err != nil {
				return err
			}
			if err := ensureConfigAdminWriteSchema(tx); err != nil {
				return err
			}
			state, _, err := loadConfigAdminRuntime(tx, runtimeScope)
			if err != nil {
				return err
			}
			if state.Revision != expected {
				return &ConfigAdminError{Status: http.StatusConflict, Type: "config-revision-conflict", Message: "If-Match revision is stale", Revision: state.Revision}
			}
			raw, err := ExportCanonicalYAML(tx, state.ActiveSetID)
			if err != nil {
				return err
			}
			var cfg Config
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return configAdminError(http.StatusBadRequest, "config-invalid", "the active configuration cannot be decoded")
			}
			if err := mutate(&cfg); err != nil {
				return err
			}
			if err := cfg.validate(false); err != nil {
				return configAdminError(http.StatusUnprocessableEntity, "config-invalid", "the proposed resource is invalid")
			}
			patched, err := yaml.Marshal(&cfg)
			if err != nil {
				return fmt.Errorf("encode updated configuration: %w", err)
			}
			setID, err := importCanonicalDocumentTx(tx, runtimeScope, name, patched, &cfg, actor, now)
			if err != nil {
				return err
			}
			revision, err := activateConfigSetGuardedTx(tx, runtimeScope, setID, expected, now)
			if err != nil {
				return err
			}
			if _, err := insertConfigChangeEventTx(tx, runtimeScope, setID, "resource_update", actor, revision, configChangeEventMeta{Audit: audit, BeforeHash: adminAuditHash(raw), AfterHash: adminAuditHash(patched), PrevSetID: state.ActiveSetID}, now); err != nil {
				return err
			}
			result = adminConfigWriteResult{Revision: revision, SetID: setID}
			return nil
		})
	})
	if err != nil {
		return adminConfigWriteResult{}, err
	}
	return result, nil
}

func safeAdminProvider(provider ProviderConfig) map[string]any {
	return map[string]any{"base_url": provider.BaseURL, "dialect": provider.Dialect, "upstream_kind": provider.UpstreamKind, "key_id": provider.KeyID, "auth_scheme": provider.AuthScheme, "traffic_shape": provider.TrafficShape, "spend_breaker": provider.SpendBreaker, "models": provider.Models}
}
