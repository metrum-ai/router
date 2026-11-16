// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "net/http"

// The /admin/v1 operation table (ACP-011).
//
// Every admin operation that exists today on the serving port has exactly one
// entry here. The entries carry the guard the handler already enforces, so
// re-mounting cannot weaken or duplicate a decision:
//
// Domain resolution per source is documented in docs/ADMIN_API_V1.md:
//
//	AdminDomainGlobal        plane-global domain (the shared AdminGlobalDomain value)
//	AdminDomainCallerTarget  the stored caller row's "<project>/<environment>"; a
//	                         list is filtered per row by each row's stored domain and
//	                         never by a client query parameter; issueCaller takes the
//	                         requested project/environment from the body and is
//	                         authorized against that domain
//	AdminDomainCapturedRow   the captured row's originating caller domain, failing
//	                         closed when it cannot be resolved
//	AdminDomainHandler       the reports handler's own per-domain decision
//	AdminDomainCredential    the authenticated credential's domain with contract-layer authorization
//
//	config-set import        admin:config / activate
//	shadow promotion         admin:shadow / write AND admin:config / write
//	force override           admin:config / force (ExtraChecks)
//	training                 content:training / export|label (global wildcard)
//	content-capture delete   content:capture / delete (captured row's domain)
//	content-capture purge    content:capture / purge (global wildcard)
//	reports                  admin:reports|admin:security_reports (handler domain)
//	admin keys (#815)        admin:keys   policy sets (#813) admin:policy
//
// Browser-only routes are deliberately absent: /admin/auth/{login,callback,
// logout,me,check} and the reports UI (/, /static/, /usage) are not part of the
// control plane and stay on the serving port.

// adminOpenObject documents a handler body that is a map[string]any. The
// generated schema is an open object whose description names the documented
// members, which is the honest contract for the 13 handlers that do not return
// a typed struct yet.
type adminOpenObject struct {
	Members []string
}

// adminBinaryBody documents a binary response (the training archive).
type adminBinaryBody struct{ MediaType string }

// adminTextBody documents a text response (CSV, Markdown, Prometheus).
type adminTextBody struct{ MediaType string }

const (
	adminReportsQueryNote = "Reports endpoints keep their own validated parameter set (time window, filters, sort, limit, cursor) because their pagination and filter semantics are already implemented in the reports handler."

	adminConfigCollectionSortNote = "Stable tie-break on the resource identifier guarantees each row appears exactly once in a full cursor walk."
)

var adminV1OperationSpecs = []Operation{
	{
		ID: "getAdminOperation", Method: "GET", Path: "/admin/v1/operations/{operation_id}",
		Summary: "Read the state and structured result of an asynchronous admin operation",
		Object:  authzObjectAdminOperations, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOperationResult{}, Errors: []int{404, 503}, RateClass: AdminRateClassRead, HandlerKey: "admin.operations.get",
	},
	{
		ID: "startControlPlaneBackup", Method: "POST", Path: "/admin/v1/control-plane/backups",
		Summary:     "Start an asynchronous control-plane backup",
		Description: "Returns 202 Accepted with a Location header for the operation resource. Download the completed versioned bundle from the separately authorized backup resource.",
		Object:      authzObjectAdminBackup, Action: "write", Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"operation", "operation_url"}}, Errors: []int{400, 503}, SuccessStatus: http.StatusAccepted,
		SuccessHeaders: []AdminResponseHeader{{Name: "Location", Required: true, Description: "URL of the asynchronous operation resource.", Schema: map[string]any{"type": "string", "format": "uri-reference"}}, {Name: "Retry-After", Description: "Suggested delay in seconds before polling the operation.", Schema: map[string]any{"type": "integer", "minimum": 1}}},
		RateClass:      AdminRateClassWrite, HandlerKey: "admin.backup.start",
	},
	{
		ID: "getControlPlaneBackup", Method: "GET", Path: "/admin/v1/control-plane/backups/{operation_id}",
		Summary: "Download a completed control-plane backup bundle",
		Object:  authzObjectAdminBackup, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"bundle"}}, Errors: []int{404, 409, 503},
		RateClass: AdminRateClassRead, HandlerKey: "admin.backup.download",
	},
	{
		ID: "startControlPlaneRestore", Method: "POST", Path: "/admin/v1/control-plane/restores",
		Summary: "Start an asynchronous restore from a control-plane backup bundle",
		Object:  authzObjectAdminRestore, Action: "write", Domain: AdminDomainGlobal,
		Request: controlPlaneBackupBundle{}, Response: adminOpenObject{Members: []string{"operation", "operation_url"}},
		Errors: []int{400, 409, 503}, SuccessStatus: http.StatusAccepted,
		SuccessHeaders: []AdminResponseHeader{{Name: "Location", Required: true, Description: "URL of the asynchronous operation resource.", Schema: map[string]any{"type": "string", "format": "uri-reference"}}, {Name: "Retry-After", Description: "Suggested delay in seconds before polling the operation.", Schema: map[string]any{"type": "integer", "minimum": 1}}},
		RateClass:      AdminRateClassWrite, HandlerKey: "admin.backup.restore",
	},
	{
		ID: "listProviders", Method: "GET", Path: "/admin/v1/providers", LegacyPath: "/admin/providers",
		Summary: "List configured providers without credentials",
		Object:  authzObjectAdminProviders, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"providers", "set_id", "revision"}},
		Errors:   []int{409, 503}, RateClass: AdminRateClassRead, HandlerKey: "providers.list",
	},
	{
		ID: "listModelCatalog", Method: "GET", Path: "/admin/v1/models", LegacyPath: "/admin/models",
		Summary: "List provider models and validated capability metadata",
		Object:  authzObjectAdminCatalog, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"models", "set_id", "revision"}},
		Errors:   []int{409, 503}, RateClass: AdminRateClassRead, HandlerKey: "models.list",
	},
	{
		ID: "getProvider", Method: "GET", Path: "/admin/v1/providers/{provider}", LegacyPath: "/admin/providers/{provider}",
		Summary: "Read provider configuration without credentials",
		Object:  authzObjectAdminProviders, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"id", "provider", "set_id", "revision"}},
		Errors:   []int{404, 503}, RateClass: AdminRateClassRead, HandlerKey: "providers.get",
	},
	{
		ID: "putProvider", Method: "PUT", Path: "/admin/v1/providers/{provider}", LegacyPath: "/admin/providers/{provider}",
		Summary:     "Create or update provider configuration",
		Description: "Credential values are write-only; use api_key_env references and never expect credential fields in the response.",
		Object:      authzObjectAdminProviders, Action: "write", Domain: AdminDomainGlobal,
		Request: adminProviderWriteRequest{}, Response: adminOpenObject{Members: []string{"id", "revision", "set_id", "provider"}},
		Errors: []int{400, 404, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "providers.put",
	},
	{
		ID: "disableProvider", Method: "DELETE", Path: "/admin/v1/providers/{provider}", LegacyPath: "/admin/providers/{provider}",
		Summary: "Disable an unused provider",
		Object:  authzObjectAdminProviders, Action: "write", Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"id", "status", "revision", "set_id"}},
		Errors:   []int{404, 409, 412, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "providers.disable",
	},
	{
		ID: "validateProvider", Method: "POST", Path: "/admin/v1/providers/{provider}/validate", LegacyPath: "/admin/providers/{provider}/validate",
		Summary: "Validate provider and model capability metadata without writing",
		Object:  authzObjectAdminProviders, Action: "write", Domain: AdminDomainGlobal,
		Request: adminProviderWriteRequest{}, Response: adminOpenObject{Members: []string{"valid", "provider"}},
		Errors: []int{400, 404, 422, 503}, NoSideEffect: true,
		RateClass: AdminRateClassWrite, HandlerKey: "providers.validate",
	},
	{
		ID: "getCallerQuota", Method: "GET", Path: "/admin/v1/callers/{id}/quota", LegacyPath: "/admin/callers/{id}/quota",
		Summary: "Read a caller quota",
		Object:  authzObjectAdminQuotas, Action: authzActionRead, Domain: AdminDomainCallerTarget,
		Response: adminOpenObject{Members: []string{"caller_id", "quota", "revision", "set_id"}},
		Errors:   []int{400, 404, 503}, RateClass: AdminRateClassRead, HandlerKey: "callers.quota.read",
	},
	{
		ID: "updateCallerQuota", Method: "POST", Path: "/admin/v1/callers/{id}/quota", LegacyPath: "/admin/callers/{id}/quota",
		Summary: "Update a caller quota",
		Object:  authzObjectAdminQuotas, Action: "write", Domain: AdminDomainCallerTarget,
		Request: adminCallerQuotaRequest{}, Response: adminOpenObject{Members: []string{"caller_id", "quota", "revision", "set_id"}},
		Errors: []int{400, 404, 409, 412, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "callers.quota.update",
	},
	{
		ID: "putRetentionPolicy", Method: "PUT", Path: "/admin/v1/retention", LegacyPath: "/admin/retention",
		Summary: "Update retention policy",
		Object:  authzObjectAdminRetention, Action: "write", Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminRetention, Action: "force", When: AdminCheckBodyForce}},
		Request:     adminRetentionWriteRequest{}, Response: adminOpenObject{Members: []string{"retention", "revision", "set_id", "shortened"}},
		Errors: []int{400, 403, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "retention.put",
	},
	{
		ID: "listProjects", Method: "GET", Path: "/admin/v1/projects", LegacyPath: "/admin/projects",
		Summary: "List projects and environments",
		Object:  authzObjectAdminProjects, Action: authzActionRead, Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminBudgets, Action: authzActionRead}},
		Response:    adminOpenObject{Members: []string{"projects", "set_id", "revision"}},
		Errors:      []int{503}, RateClass: AdminRateClassRead, HandlerKey: "projects.list",
	},
	{
		ID: "getProject", Method: "GET", Path: "/admin/v1/projects/{project}", LegacyPath: "/admin/projects/{project}",
		Summary: "Read a project and its environments",
		Object:  authzObjectAdminProjects, Action: authzActionRead, Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminBudgets, Action: authzActionRead}},
		Response:    adminOpenObject{Members: []string{"project", "set_id", "revision"}},
		Errors:      []int{404, 503}, RateClass: AdminRateClassRead, HandlerKey: "projects.get",
	},
	{
		ID: "putProject", Method: "PUT", Path: "/admin/v1/projects/{project}", LegacyPath: "/admin/projects/{project}",
		Summary: "Create or update a project and its environments",
		Object:  authzObjectAdminProjects, Action: "write", Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminBudgets, Action: "write", When: AdminCheckBodyBudget}},
		Request:     adminProjectWriteRequest{}, Response: adminOpenObject{Members: []string{"project", "revision", "set_id"}},
		Errors: []int{400, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "projects.put",
	},
	{
		ID: "deleteProject", Method: "DELETE", Path: "/admin/v1/projects/{project}", LegacyPath: "/admin/projects/{project}",
		Summary: "Delete an unused project and its memberships",
		Object:  authzObjectAdminProjects, Action: "write", Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"deleted_project", "deleted_memberships", "revision", "set_id"}},
		Errors:   []int{400, 404, 409, 412, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "projects.delete",
	},
	{
		ID: "deleteProjectEnvironment", Method: "DELETE", Path: "/admin/v1/projects/{project}/environments/{environment}", LegacyPath: "/admin/projects/{project}/environments/{environment}",
		Summary: "Delete an unused project environment and its memberships",
		Object:  authzObjectAdminProjects, Action: "write", Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"deleted_environment", "deleted_memberships", "revision", "set_id"}},
		Errors:   []int{400, 404, 409, 412, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "projects.environments.delete",
	},
	{
		ID: "listProjectEnvironmentMemberships", Method: "GET", Path: "/admin/v1/projects/{project}/environments/{environment}/memberships", LegacyPath: "/admin/projects/{project}/environments/{environment}/memberships",
		Summary: "List memberships in a project environment",
		Object:  authzObjectAdminMemberships, Action: authzActionRead, Domain: AdminDomainProjectEnvironment,
		Response: adminOpenObject{Members: []string{"memberships", "set_id", "revision"}},
		Errors:   []int{404, 503}, RateClass: AdminRateClassRead, HandlerKey: "memberships.list",
	},
	{
		ID: "putProjectEnvironmentMembership", Method: "POST", Path: "/admin/v1/projects/{project}/environments/{environment}/memberships", LegacyPath: "/admin/projects/{project}/environments/{environment}/memberships",
		Summary: "Create or update a membership in a project environment",
		Object:  authzObjectAdminMemberships, Action: "write", Domain: AdminDomainProjectEnvironment,
		Request: adminMembershipWriteRequest{}, Response: adminOpenObject{Members: []string{"membership", "revision", "set_id"}},
		Errors: []int{400, 403, 404, 409, 412, 422, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "memberships.put",
	},
	{
		ID: "deleteProjectEnvironmentMembership", Method: "DELETE", Path: "/admin/v1/projects/{project}/environments/{environment}/memberships/{user_id}", LegacyPath: "/admin/projects/{project}/environments/{environment}/memberships/{user_id}",
		Summary: "Delete a membership from a project environment",
		Object:  authzObjectAdminMemberships, Action: "write", Domain: AdminDomainProjectEnvironment,
		Response: adminOpenObject{Members: []string{"deleted_membership", "revision", "set_id"}},
		Errors:   []int{400, 404, 412, 428, 503}, Concurrency: AdminConcurrencyRevision,
		RateClass: AdminRateClassWrite, HandlerKey: "memberships.delete",
	},
	// ---------------------------------------------------------------------
	// Configuration (admin:config, admin:shadow)
	// ---------------------------------------------------------------------
	{
		ID: "listConfigGroups", Method: "GET", Path: "/admin/v1/config/groups", LegacyPath: "/admin/config/groups",
		Summary: "List model groups in the active configuration",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"mode", "runtime_scope", "set_id", "revision", "groups"}},
		Errors:   []int{409}, Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyNone,
		RateClass: AdminRateClassRead, HandlerKey: "config.groups.list",
	},
	{
		ID: "getConfigGroup", Method: "GET", Path: "/admin/v1/config/groups/{group}", LegacyPath: "/admin/config/groups/{group}",
		Summary: "Read one model group",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"group", "strategy", "targets", "contract", "virtual_model", "set_id", "revision"}},
		Errors:    []int{404, 409},
		RateClass: AdminRateClassRead, HandlerKey: "config.group.get",
	},
	{
		ID: "replaceConfigGroupTargets", Method: "PUT", Path: "/admin/v1/config/groups/{group}/targets", LegacyPath: "/admin/config/groups/{group}/targets",
		Summary: "Replace a model group's target list",
		Object:  authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminConfig, Action: "force", When: AdminCheckBodyForce}},
		Request:     configAdminTargetsRequest{},
		Response:    adminOpenObject{Members: []string{"group", "set_id", "previous_set_id", "revision", "dry_run", "forced", "replayed", "strategy", "diff", "targets", "propagation"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.targets.replace",
	},
	{
		ID: "addConfigGroupTarget", Method: "POST", Path: "/admin/v1/config/groups/{group}/targets", LegacyPath: "/admin/config/groups/{group}/targets",
		Summary: "Add one target to a model group",
		Object:  authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminConfig, Action: "force", When: AdminCheckBodyForce}},
		Request:     configAdminAddTargetRequest{},
		Response:    adminOpenObject{Members: []string{"group", "set_id", "previous_set_id", "revision", "dry_run", "forced", "replayed", "strategy", "diff", "targets", "propagation"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.targets.add",
	},
	{
		ID: "bulkConfigGroupTargets", Method: "POST", Path: "/admin/v1/config/groups/{group}/targets:bulk",
		Summary: "Apply up to 100 target additions and removals in one configuration revision",
		Object:  authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		Request:     configAdminBulkTargetsRequest{},
		Response:    adminOpenObject{Members: []string{"group", "set_id", "previous_set_id", "revision", "replayed", "strategy", "diff", "targets", "propagation"}},
		Errors:      []int{400, 404, 409, 412, 422, 428, 503},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.targets.bulk",
	},
	{
		ID: "removeConfigGroupTarget", Method: "DELETE", Path: "/admin/v1/config/groups/{group}/targets/{target_id...}", LegacyPath: "/admin/config/groups/{group}/targets/{target_id...}",
		Summary: "Remove one target from a model group",
		Object:  authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminConfig, Action: "force", When: AdminCheckBodyForce}},
		Response:    adminOpenObject{Members: []string{"group", "set_id", "previous_set_id", "revision", "dry_run", "forced", "replayed", "diff", "targets", "propagation"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.targets.remove",
	},
	{
		ID: "validateConfigGroupTargets", Method: "POST", Path: "/admin/v1/config/groups/{group}/targets:validate", LegacyPath: "/admin/config/groups/{group}/targets:validate",
		Summary: "Validate a target list without writing",
		Description: "A dry run: no configuration set is created, no revision is bumped, no change event is written. " +
			"Keeps today's config:write guard because the shared write pipeline enforces it.",
		Object: authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		Request:     configAdminTargetsRequest{},
		Response:    adminOpenObject{Members: []string{"group", "dry_run", "strategy", "diff", "targets"}},
		Errors:      []int{404, 422},
		Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyNone, NoSideEffect: true,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.targets.validate",
	},
	{
		ID: "rollbackConfigGroup", Method: "POST", Path: "/admin/v1/config/groups/{group}/rollback", LegacyPath: "/admin/config/groups/{group}/rollback",
		Summary: "Roll a model group back to a previous set",
		Object:  authzObjectAdminConfig, Action: "activate", Domain: AdminDomainGlobal,
		Request:     configAdminRollbackRequest{},
		Response:    adminOpenObject{Members: []string{"group", "set_id", "previous_set_id", "revision", "restored_from_set_id", "diff", "propagation"}},
		Errors:      []int{404, 409, 412, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.rollback",
	},
	{
		ID: "listConfigGroupHistory", Method: "GET", Path: "/admin/v1/config/groups/{group}/history", LegacyPath: "/admin/config/groups/{group}/history",
		Summary: "List the change history of a model group",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"group", "mode", "events"}},
		RateClass: AdminRateClassRead, HandlerKey: "config.group.history",
	},
	{
		ID: "startConfigGroupShadow", Method: "POST", Path: "/admin/v1/config/groups/{group}/shadow", LegacyPath: "/admin/config/groups/{group}/shadow",
		Summary: "Start a shadow run for a candidate target",
		Object:  authzObjectAdminShadow, Action: "write", Domain: AdminDomainGlobal,
		Request:       ShadowStartRequest{},
		Response:      configAdminShadowStartResponse{},
		SuccessStatus: 201,
		Errors:        []int{404, 409, 412, 422, 428},
		Concurrency:   AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.shadow.start",
	},
	{
		ID: "getConfigGroupShadow", Method: "GET", Path: "/admin/v1/config/groups/{group}/shadow", LegacyPath: "/admin/config/groups/{group}/shadow",
		Summary: "Read the active shadow run of a model group",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"shadow"}},
		RateClass: AdminRateClassRead, HandlerKey: "config.group.shadow.status",
	},
	{
		ID: "promoteConfigGroupShadow", Method: "POST", Path: "/admin/v1/config/groups/{group}/shadow/promote", LegacyPath: "/admin/config/groups/{group}/shadow/promote",
		Summary: "Promote a shadow run's candidate into the group",
		Description: "Requires both admin:shadow/write and admin:config/write, matching today's two-decision handler; " +
			"a forced promotion additionally requires admin:config/force.",
		Object: authzObjectAdminShadow, Action: "write", Domain: AdminDomainGlobal,
		ExtraChecks: []AdminCheck{{Object: authzObjectAdminConfig, Action: "write"}, {Object: authzObjectAdminConfig, Action: "force", When: AdminCheckBodyForce}},
		Request:     configAdminReasonForceRequest{},
		Response:    adminOpenObject{Members: []string{"group", "set_id", "previous_set_id", "revision", "replayed", "diff", "shadow_run_id"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.shadow.promote",
	},
	{
		ID: "stopConfigGroupShadow", Method: "DELETE", Path: "/admin/v1/config/groups/{group}/shadow", LegacyPath: "/admin/config/groups/{group}/shadow",
		Summary: "Stop the active shadow run of a model group",
		Object:  authzObjectAdminShadow, Action: "write", Domain: AdminDomainGlobal,
		Request:     configAdminRollbackRequest{},
		Response:    configAdminShadowStartResponse{},
		Errors:      []int{404, 409, 412, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.shadow.stop",
	},
	{
		ID: "getConfigStatus", Method: "GET", Path: "/admin/v1/config/status", LegacyPath: "/admin/config/status",
		Summary: "Read control-plane and replica revision status",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"mode", "runtime_scope", "driver", "revision", "active_set_id", "replica"}},
		RateClass: AdminRateClassRead, HandlerKey: "config.status",
	},
	{
		ID: "importConfigSet", Method: "POST", Path: "/admin/v1/config/sets", LegacyPath: "/admin/config/sets",
		Summary: "Import a canonical configuration document as a draft set",
		Object:  authzObjectAdminConfig, Action: "activate", Domain: AdminDomainGlobal,
		Request:       configAdminImportRequest{},
		SuccessStatus: 201,
		Response:      adminOpenObject{Members: []string{"set_id", "runtime_scope", "name", "status"}},
		Errors:        []int{409, 422},
		Concurrency:   AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.sets.import",
	},
	{
		ID: "listConfigSets", Method: "GET", Path: "/admin/v1/config/sets", LegacyPath: "/admin/config/sets",
		Summary: "List configuration sets",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response: adminOpenObject{Members: []string{"mode", "runtime_scope", "sets"}},
		Errors:   []int{409},
		Pagination: &AdminPaginationSpec{
			Sorts: []string{"created_at", "name", "status", "id"}, DefaultSort: "created_at",
		},
		RateClass: AdminRateClassRead, HandlerKey: "config.sets.list",
	},
	{
		ID: "getConfigSet", Method: "GET", Path: "/admin/v1/config/sets/{set_id}", LegacyPath: "/admin/config/sets/{set_id}",
		Summary: "Export one configuration set",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"set_id", "runtime_scope", "name", "status", "validation_status", "document"}},
		Errors:    []int{404},
		RateClass: AdminRateClassRead, HandlerKey: "config.sets.get",
	},
	{
		ID: "activateConfigSet", Method: "POST", Path: "/admin/v1/config/sets/{set_id}/activate", LegacyPath: "/admin/config/sets/{set_id}/activate",
		Summary: "Activate a validated configuration set", Description: "A configuration-set import is an admin:config/activate decision, as today.",
		Object: authzObjectAdminConfig, Action: "activate", Domain: AdminDomainGlobal,
		Request:     configAdminRollbackRequest{},
		Response:    adminOpenObject{Members: []string{"set_id", "previous_set_id", "revision", "replayed", "propagation"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.sets.activate",
	},
	{
		ID: "rollbackConfigSet", Method: "POST", Path: "/admin/v1/config/rollback", LegacyPath: "/admin/config/rollback",
		Summary: "Roll the runtime scope back to a previous set",
		Object:  authzObjectAdminConfig, Action: "activate", Domain: AdminDomainGlobal,
		Request:     configAdminRollbackRequest{},
		Response:    adminOpenObject{Members: []string{"set_id", "previous_set_id", "revision", "replayed", "propagation"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyHandler,
		RateClass: AdminRateClassWrite, HandlerKey: "config.sets.rollback",
	},
	{
		ID: "diffConfig", Method: "GET", Path: "/admin/v1/config/diff", LegacyPath: "/admin/config/diff",
		Summary: "Diff two configuration sets",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"from_set_id", "to_set_id", "groups", "providers"}},
		Errors:    []int{404, 409},
		Query:     []adminQueryParameter{{Name: "from", Required: true, Description: "base configuration set ID"}, {Name: "to", Required: true, Description: "target configuration set ID"}},
		RateClass: AdminRateClassRead, HandlerKey: "config.diff",
	},

	// ---------------------------------------------------------------------
	// Caller directory (admin:callers)
	// ---------------------------------------------------------------------
	{
		ID: "listCallers", Method: "GET", Path: "/admin/v1/callers", LegacyPath: "/admin/callers",
		Summary: "List registered callers of the active configuration",
		Object:  authzObjectAdminCallers, Action: authzActionList, Domain: AdminDomainCallerTarget,
		Response: adminOpenObject{Members: []string{"generated_utc", "config_set_id", "callers"}},
		Errors:   []int{409},
		Pagination: &AdminPaginationSpec{
			Sorts: []string{"caller_id", "project", "environment", "status"}, DefaultSort: "caller_id",
		},
		RateClass: AdminRateClassRead, HandlerKey: "callers.list",
	},
	{
		ID: "issueCaller", Method: "POST", Path: "/admin/v1/callers/issue", LegacyPath: "/admin/callers/issue",
		Summary:     "Issue a caller credential",
		Description: "The raw caller token is returned exactly once in the response and is never stored or logged.",
		Object:      authzObjectAdminCallers, Action: authzActionIssue, Domain: AdminDomainCallerTarget,
		Request:       callerAdminIssueRequest{},
		SuccessStatus: 201,
		Response:      callerAdminIssueResponse{},
		Errors:        []int{409, 422},
		Concurrency:   AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore, SensitiveResponse: true,
		RateClass: AdminRateClassWrite, HandlerKey: "callers.issue",
	},
	{
		ID: "rotateCaller", Method: "POST", Path: "/admin/v1/callers/{id}/rotate", LegacyPath: "/admin/callers/{id}/rotate",
		Summary: "Rotate a caller credential",
		Object:  authzObjectAdminCallers, Action: authzActionRotate, Domain: AdminDomainCallerTarget,
		Response:    adminOpenObject{Members: []string{"caller_id", "status", "token_id", "token"}},
		Errors:      []int{404, 409, 412, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore, SensitiveResponse: true,
		RateClass: AdminRateClassWrite, HandlerKey: "callers.rotate",
	},
	{
		ID: "revokeCaller", Method: "POST", Path: "/admin/v1/callers/{id}/revoke", LegacyPath: "/admin/callers/{id}/revoke",
		Summary: "Revoke a caller credential",
		Object:  authzObjectAdminCallers, Action: authzActionRevoke, Domain: AdminDomainCallerTarget,
		Response:    adminOpenObject{Members: []string{"caller_id", "status", "token_id"}},
		Errors:      []int{404, 409, 412, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "callers.revoke",
	},
	{
		ID: "containCaller", Method: "POST", Path: "/admin/v1/containment",
		Summary:     "Suspend or resume a caller key",
		Description: "The containment permission can suspend and resume caller keys. It cannot issue, rotate, or grant access.",
		Object:      authzObjectAdminContainment, Action: authzActionContain, Domain: AdminDomainGlobal,
		Request:     callerContainmentRequest{},
		Response:    adminOpenObject{Members: []string{"caller_id", "status", "expires_at"}},
		Errors:      []int{400, 404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "callers.containment",
	},
	{
		ID: "containCallerWebhook", Method: "POST", Path: "/admin/v1/containment/webhook",
		Summary: "Suspend a caller key from a signed SOAR or EDR webhook",
		Object:  authzObjectAdminContainment, Action: authzActionContain, Domain: AdminDomainGlobal,
		Request: callerContainmentRequest{}, Response: adminOpenObject{Members: []string{"caller_id", "status", "expires_at"}},
		Errors:      []int{400, 403, 404, 409, 422, 503},
		Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyNone,
		RateClass: AdminRateClassWrite, HandlerKey: "containment.webhook",
	},

	// ---------------------------------------------------------------------
	// Training exports (content:training, global domain)
	// ---------------------------------------------------------------------
	{
		ID: "exportTrainingData", Method: "POST", Path: "/admin/v1/training/export", LegacyPath: "/admin/training/export",
		Summary: "Export a consented training archive",
		Description: "Keeps today's content:training/export decision in the whole-plane domain and the durable " +
			"security-event precondition: the export fails closed unless its audit event commits first.",
		Object: trainingObject, Action: authzActionExport, Domain: AdminDomainGlobal,
		Request:     trainingFilters{},
		Response:    adminBinaryBody{MediaType: "application/zip"},
		Errors:      []int{409, 422, 503},
		Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "training.export",
	},
	{
		ID: "labelTrainingOutcome", Method: "POST", Path: "/admin/v1/training/outcomes/{request_id}", LegacyPath: "/admin/training/outcomes/{request_id}",
		Summary: "Label a captured request outcome",
		Object:  trainingObject, Action: "label", Domain: AdminDomainGlobal,
		Request:     trainingOutcome{},
		Response:    adminOpenObject{Members: []string{"request_id", "label"}},
		Errors:      []int{404, 409, 422, 503},
		Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "training.outcomes.label",
	},

	// ---------------------------------------------------------------------
	// Admin-plane metrics (metrics/read)
	// ---------------------------------------------------------------------
	{
		ID: "getAdminMetrics", Method: "GET", Path: "/admin/v1/metrics", LegacyPath: "/metrics",
		Summary: "Scrape admin-plane and serving metrics",
		Description: "The versioned spelling of the admin listener's /metrics. Both use the same renderer and the " +
			"same metrics/read decision in the whole-plane domain; no caller token is involved.",
		Object: authzObjectMetrics, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminTextBody{MediaType: "text/plain; version=0.0.4; charset=utf-8"},
		RateClass: AdminRateClassRead, HandlerKey: "metrics.scrape",
	},

	// ---------------------------------------------------------------------
	// Content-capture maintenance (content:capture)
	// ---------------------------------------------------------------------
	{
		ID: "deleteContentCapture", Method: "DELETE", Path: "/admin/v1/content-captures/{request_id}", LegacyPath: "/v1/content-captures/{request_id}",
		Summary: "Delete the captures of one request",
		Description: "Keeps today's domain rule: the decision is taken in the domain of the captured row's originating " +
			"caller, and a row with no resolvable domain fails closed.",
		Object: authzObjectContentCapture, Action: authzActionDelete, Domain: AdminDomainCapturedRow,
		Response:    adminOpenObject{Members: []string{"request_id", "deleted"}},
		Errors:      []int{404, 409, 503},
		Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "content.captures.delete",
	},
	{
		ID: "purgeExpiredContentCaptures", Method: "POST", Path: "/admin/v1/content-captures/purge-expired", LegacyPath: "/v1/content-captures/purge-expired",
		Summary:     "Purge expired content captures",
		Description: "Keeps today's whole-plane content:capture/purge decision and the retention-driven purge body.",
		Object:      authzObjectContentCapture, Action: authzActionPurge, Domain: AdminDomainGlobal,
		Response:    adminOpenObject{Members: []string{"purged", "cutoff"}},
		Errors:      []int{503},
		Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "content.captures.purge",
	},

	// Tool approval resolution. The opaque approval ID is not a bearer token;
	// this route requires the explicit admin:tool_approvals/resolve grant.
	{
		ID: "resolveToolApproval", Method: "POST", Path: "/admin/v1/tool-approvals/{approval_id}",
		Summary: "Release or deny a pending tool call",
		Object:  authzObjectToolApprovals, Action: authzActionResolve, Domain: AdminDomainGlobal,
		Request: adminOpenObject{Members: []string{"decision"}}, Response: adminOpenObject{Members: []string{"approval_id", "decision"}},
		Errors: []int{400, 404}, Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyNone,
		RateClass: AdminRateClassWrite, HandlerKey: "tool.approvals.resolve",
	},

	// ---------------------------------------------------------------------
	// Reports (admin:reports, admin:security_reports; handler-scoped domains)
	// ---------------------------------------------------------------------
	{
		ID: "getAdminReportVersion", Method: "GET", Path: "/admin/v1/reports/version", LegacyPath: "/admin/reports/api/version",
		Summary: "Report the router build version",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"version", "commit", "build_date", "go_version", "goos", "goarch"}},
		Adapter:  "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.version",
	},
	{
		ID: "getAdminSecurityPostureReport", Method: "GET", Path: "/admin/v1/reports/security/posture/latest", LegacyPath: "/admin/reports/api/security/posture/latest",
		Summary: "Get the latest security posture self-test summary",
		Object:  authzObjectSecurityReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"schema", "generated_at", "outcome", "findings"}}, Errors: []int{404, 503},
		Adapter: "reports", RateClass: AdminRateClassRead, HandlerKey: "reports.security_posture.latest.read",
	},
	{
		ID: "saveAdminSecurityPostureReport", Method: "POST", Path: "/admin/v1/reports/security/posture/latest", LegacyPath: "/admin/reports/api/security/posture/latest",
		Summary: "Save the latest security posture self-test summary",
		Object:  authzObjectSecurityReports, Action: "write", Domain: AdminDomainHandler,
		Request:  adminOpenObject{Members: []string{"schema", "generated_at", "outcome", "findings"}},
		Response: adminOpenObject{Members: []string{"saved"}}, Errors: []int{400, 413, 503},
		Adapter: "reports", RateClass: AdminRateClassWrite, HandlerKey: "reports.security_posture.latest.write",
	},
	{
		ID: "getAdminQuotaStatus", Method: "GET", Path: "/admin/v1/reports/quota-status", LegacyPath: "/admin/reports/api/quota-status",
		Summary: "Report caller quota status",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "callers"}},
		Errors:   []int{503}, Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.quota_status",
	},
	{
		ID: "getAdminReportSummary", Method: "GET", Path: "/admin/v1/reports/summary", LegacyPath: "/admin/reports/api/summary",
		Summary: "Report usage summary",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "totals", "pagination"}},
		Errors:   []int{400, 503}, Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.summary",
	},
	{
		ID: "getAdminReportOverview", Method: "GET", Path: "/admin/v1/reports/overview", LegacyPath: "/admin/reports/api/overview",
		Summary: "Report usage and savings overview",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "totals", "sections"}},
		Errors:   []int{400, 503}, Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.overview",
	},
	{
		ID: "getAdminReportSavings", Method: "GET", Path: "/admin/v1/reports/savings", LegacyPath: "/admin/reports/api/savings",
		Summary: "Report routing savings against a baseline",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "baseline", "sections"}},
		Errors:   []int{400, 503}, Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.savings",
	},
	{
		ID: "getCFOReport", Method: "GET", Path: "/admin/v1/reports/cfo", LegacyPath: "/admin/reports/api/cfo",
		Summary: "Report CFO cost and savings view",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "teams", "totals"}},
		Errors:   []int{400, 503}, Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.cfo",
	},
	{
		ID: "exportCFOReportCSV", Method: "GET", Path: "/admin/v1/reports/cfo/export.csv", LegacyPath: "/admin/reports/cfo/export.csv",
		Summary: "Export the CFO report as CSV",
		Object:  authzObjectAdminReports, Action: authzActionExport, Domain: AdminDomainHandler,
		Response: adminTextBody{MediaType: "text/csv"}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.cfo.csv",
	},
	{
		ID: "exportCFOReportMarkdown", Method: "GET", Path: "/admin/v1/reports/cfo/export.md", LegacyPath: "/admin/reports/cfo/export.md",
		Summary: "Export the CFO report as Markdown",
		Object:  authzObjectAdminReports, Action: authzActionExport, Domain: AdminDomainHandler,
		Response: adminTextBody{MediaType: "text/markdown"}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.cfo.md",
	},
	{
		ID: "exportAdminReportMarkdown", Method: "GET", Path: "/admin/v1/reports/export.md", LegacyPath: "/admin/reports/export.md",
		Summary: "Export the usage report as Markdown",
		Object:  authzObjectAdminReports, Action: authzActionExport, Domain: AdminDomainHandler,
		Response: adminTextBody{MediaType: "text/markdown"}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.export.md",
	},
	{
		ID: "getAdminRetentionStatus", Method: "GET", Path: "/admin/v1/reports/retention-status", LegacyPath: "/admin/reports/api/retention-status",
		Summary: "Report retention job status",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "tables"}}, Errors: []int{503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.retention_status",
	},
	{
		ID: "getAdminProviderCatalogStatus", Method: "GET", Path: "/admin/v1/reports/provider-catalog-status", LegacyPath: "/admin/reports/api/provider-catalog-status",
		Summary: "Report provider catalog status",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"providers", "catalog_models", "active_targets", "validated_targets"}},
		Adapter:  "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.provider_catalog_status",
	},
	{
		ID: "getAdminMigrationStatus", Method: "GET", Path: "/admin/v1/reports/migrations", LegacyPath: "/admin/reports/api/migrations",
		Summary: "Report database migration status",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"scope", "state", "entries", "jobs"}}, Errors: []int{503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.migrations",
	},
	{
		ID: "listAdminReportRequests", Method: "GET", Path: "/admin/v1/reports/requests", LegacyPath: "/admin/reports/api/requests",
		Summary: "List request rows with cursor pagination",
		Object:  authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "rows", "pagination"}}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.requests",
	},
	{
		ID: "getAdminReportRequest", Method: "GET", Path: "/admin/v1/reports/requests/{request_id}", LegacyPath: "/admin/reports/api/request/{request_id}",
		Summary: "Read one request row",
		Object:  authzObjectAdminReports, Action: authzActionDrilldown, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"request", "attempts", "usage"}}, Errors: []int{400, 404, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.request",
	},
	{
		ID: "getAdminReportRequestEvidence", Method: "GET", Path: "/admin/v1/reports/request-evidence", LegacyPath: "/admin/reports/api/request-evidence",
		Summary: "Read the evidence for one request",
		Object:  authzObjectAdminReports, Action: authzActionDrilldown, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"request", "captures", "usage"}}, Errors: []int{400, 404, 503},
		Query:   []adminQueryParameter{{Name: "request_id", Required: true, Description: "request identifier"}},
		Adapter: "reports", RateClass: AdminRateClassRead, HandlerKey: "reports.request_evidence",
	},
	{
		ID: "listAdminSecurityEvents", Method: "GET", Path: "/admin/v1/reports/security/events", LegacyPath: "/admin/reports/api/security/events",
		Summary: "List security access events with cursor pagination",
		Object:  authzObjectSecurityReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "events", "pagination"}}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.security.events",
	},
	{
		ID: "getAdminSecurityOverview", Method: "GET", Path: "/admin/v1/reports/security/overview", LegacyPath: "/admin/reports/api/security/overview",
		Summary: "Report security access overview",
		Object:  authzObjectSecurityReports, Action: authzActionRead, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"generated_utc", "window", "totals", "sections"}}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.security.overview",
	},
	{
		ID: "exportAdminSecurityCSV", Method: "GET", Path: "/admin/v1/reports/security/export.csv", LegacyPath: "/admin/reports/security/export.csv",
		Summary: "Export security access events as CSV",
		Object:  authzObjectSecurityReports, Action: authzActionExport, Domain: AdminDomainHandler,
		Response: adminTextBody{MediaType: "text/csv"}, Errors: []int{400, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.security.csv",
	},
	{
		ID: "exportAdminSecurityIncidentJSON", Method: "GET", Path: "/admin/v1/reports/security/export.json", LegacyPath: "/admin/reports/security/export.json",
		Summary: "Export a security incident bundle as JSON",
		Object:  authzObjectSecurityReports, Action: authzActionExport, Domain: AdminDomainHandler,
		Response: adminOpenObject{Members: []string{"incident", "evidence"}}, Errors: []int{400, 404, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.security.json",
	},
	{
		ID: "exportAdminSecurityIncidentMarkdown", Method: "GET", Path: "/admin/v1/reports/security/export.md", LegacyPath: "/admin/reports/security/export.md",
		Summary: "Export a security incident bundle as Markdown",
		Object:  authzObjectSecurityReports, Action: authzActionExport, Domain: AdminDomainHandler,
		Response: adminTextBody{MediaType: "text/markdown"}, Errors: []int{400, 404, 503},
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.security.markdown",
	},
}

// adminV1ScalarReportSpecs are the twelve savings/usage breakdown reports. They
// share one handler and differ only by report name and dimension.
var adminV1ScalarReportSpecs = []struct {
	Slug       string
	Summary    string
	PrimaryKey string
}{
	{"savings-by-user", "Savings by caller user", "savingsByUser"},
	{"savings-by-key", "Savings by token", "savingsByKey"},
	{"savings-by-group", "Savings by model group", "savingsByGroup"},
	{"savings-by-project", "Savings by project and environment", "savingsByProject"},
	{"savings-by-provider-model", "Savings by provider model and dialect", "savingsByProviderModel"},
	{"model-groups-by-user", "Model groups used by caller user", "modelGroupsByUser"},
	{"usage-by-key", "Usage by token", "usageByKey"},
	{"usage-by-caller", "Usage by caller", "usageByCaller"},
	{"requested-models", "Requested models by model group", "requestedModels"},
	{"provider-model-mix", "Provider model mix by dialect", "providerModelMix"},
	{"latency-throughput", "Latency and throughput by provider model", "latencyThroughput"},
	{"errors-fallbacks", "Errors and fallbacks by provider model", "errorsFallbacks"},
}

func init() {
	for _, spec := range adminV1ScalarReportSpecs {
		adminV1OperationSpecs = append(adminV1OperationSpecs, Operation{
			ID: spec.PrimaryKey, Method: "GET",
			Path:       "/admin/v1/reports/" + spec.Slug,
			LegacyPath: "/admin/reports/api/" + spec.Slug,
			Summary:    spec.Summary,
			Object:     authzObjectAdminReports, Action: authzActionRead, Domain: AdminDomainHandler,
			Response: adminOpenObject{Members: []string{"generated_utc", "window", "rows", "pagination"}},
			Errors:   []int{400, 503},
			Adapter:  "reports", OpaqueQuery: true, RateClass: AdminRateClassRead,
			HandlerKey: "reports.scalar." + spec.Slug,
		})
	}
}
