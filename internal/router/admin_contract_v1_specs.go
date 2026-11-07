// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

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
	// ---------------------------------------------------------------------
	// Configuration (admin:config, admin:shadow)
	// ---------------------------------------------------------------------
	{
		ID: "listConfigGroups", Method: "GET", Path: "/admin/v1/config/groups", LegacyPath: "/admin/config/groups",
		Summary: "List model groups in the active configuration",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		ETag:     AdminETagRevision,
		Response: adminOpenObject{Members: []string{"mode", "runtime_scope", "set_id", "revision", "groups"}},
		Errors:   []int{409}, Concurrency: AdminConcurrencyNone, Idempotency: AdminIdempotencyNone,
		RateClass: AdminRateClassRead, HandlerKey: "config.groups.list",
	},
	{
		ID: "getConfigGroup", Method: "GET", Path: "/admin/v1/config/groups/{group}", LegacyPath: "/admin/config/groups/{group}",
		Summary: "Read one model group",
		Object:  authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		Response:  adminOpenObject{Members: []string{"group", "strategy", "targets", "contract", "set_id", "revision"}},
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
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
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
		Concurrency:   AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
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
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "config.group.shadow.promote",
	},
	{
		ID: "stopConfigGroupShadow", Method: "DELETE", Path: "/admin/v1/config/groups/{group}/shadow", LegacyPath: "/admin/config/groups/{group}/shadow",
		Summary: "Stop the active shadow run of a model group",
		Object:  authzObjectAdminShadow, Action: "write", Domain: AdminDomainGlobal,
		Request:     configAdminRollbackRequest{},
		Response:    configAdminShadowStartResponse{},
		Errors:      []int{404, 409, 412, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
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
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
		RateClass: AdminRateClassWrite, HandlerKey: "config.sets.activate",
	},
	{
		ID: "rollbackConfigSet", Method: "POST", Path: "/admin/v1/config/rollback", LegacyPath: "/admin/config/rollback",
		Summary: "Roll the runtime scope back to a previous set",
		Object:  authzObjectAdminConfig, Action: "activate", Domain: AdminDomainGlobal,
		Request:     configAdminRollbackRequest{},
		Response:    adminOpenObject{Members: []string{"set_id", "previous_set_id", "revision", "replayed", "propagation"}},
		Errors:      []int{404, 409, 412, 422, 428},
		Concurrency: AdminConcurrencyRevision, Idempotency: AdminIdempotencyStore,
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
		Object:  authzObjectAdminCallers, Action: authzActionList, Domain: AdminDomainGlobal,
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
		Adapter: "reports", OpaqueQuery: true, RateClass: AdminRateClassRead, HandlerKey: "reports.request_evidence",
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
