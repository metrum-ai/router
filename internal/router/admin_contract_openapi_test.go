// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateAdminOpenAPI = flag.Bool("update-admin-openapi", false, "regenerate docs/specs/admin-v1.openapi.json")

func adminOpenAPIForTest(t *testing.T) []byte {
	t.Helper()
	operations := AdminV1Operations()
	document, err := GenerateAdminOpenAPI(operations)
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	return document
}

// TestAdminOpenAPIUpToDate is the ACP-012 drift check: the committed document
// must be byte-identical to the document generated from the registry. CI runs
// this, so a route change without a spec update fails the build.
func TestAdminOpenAPIUpToDate(t *testing.T) {
	generated := adminOpenAPIForTest(t)
	path := filepath.Join(repoRootForTest(t), AdminOpenAPIPath)
	if *updateAdminOpenAPI {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, generated, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/router -run TestAdminOpenAPIUpToDate -update-admin-openapi)", AdminOpenAPIPath, err)
	}
	if !bytes.Equal(bytes.TrimRight(committed, "\n"), bytes.TrimRight(generated, "\n")) {
		t.Fatalf("generated OpenAPI document differs from %s; regenerate with: go test ./internal/router -run TestAdminOpenAPIUpToDate -update-admin-openapi", AdminOpenAPIPath)
	}
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repository root not found from %s", dir)
		}
		dir = parent
	}
}

// TestAdminOpenAPISchemaCoverage asserts the document describes every
// registered operation completely: identifiers, authorization metadata,
// parameters for the declared preconditions and the full error model.
func TestAdminOpenAPISchemaCoverage(t *testing.T) {
	var document struct {
		OpenAPI    string                    `json:"openapi"`
		Info       map[string]any            `json:"info"`
		Paths      map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas         map[string]any `json:"schemas"`
			Responses       map[string]any `json:"responses"`
			SecuritySchemes map[string]any `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(adminOpenAPIForTest(t), &document); err != nil {
		t.Fatalf("document is not JSON: %v", err)
	}
	if !strings.HasPrefix(document.OpenAPI, "3.1.") {
		t.Fatalf("openapi version = %q, want 3.1.x", document.OpenAPI)
	}
	for _, scheme := range []string{"adminBearerAuth", "adminMutualTLS"} {
		if _, ok := document.Components.SecuritySchemes[scheme]; !ok {
			t.Fatalf("security scheme %q missing", scheme)
		}
	}
	if _, ok := document.Components.Schemas["AdminProblem"]; !ok {
		t.Fatal("AdminProblem schema missing from components")
	}
	if _, ok := document.Components.Responses["Problem"]; !ok {
		t.Fatal("Problem response missing from components")
	}

	operations := AdminV1Operations()
	count := 0
	for _, op := range operations {
		pathItem, ok := document.Paths[op.Path]
		if !ok {
			t.Fatalf("operation %s path %s missing from document", op.ID, op.Path)
		}
		item, ok := pathItem[strings.ToLower(op.Method)].(map[string]any)
		if !ok {
			t.Fatalf("operation %s method %s missing from document", op.ID, op.Method)
		}
		count++
		if got := item["operationId"]; got != op.ID {
			t.Fatalf("operation %s operationId = %v", op.ID, got)
		}
		if item["summary"] == "" || item["summary"] == nil {
			t.Fatalf("operation %s has no summary", op.ID)
		}
		if got := item["x-admin-object"]; got != op.Object {
			t.Fatalf("operation %s x-admin-object = %v, want %s", op.ID, got, op.Object)
		}
		if got := item["x-admin-action"]; got != op.Action {
			t.Fatalf("operation %s x-admin-action = %v, want %s", op.ID, got, op.Action)
		}
		responses, _ := item["responses"].(map[string]any)
		for _, status := range []string{"400", "401", "403", "429", "500", "503"} {
			if _, ok := responses[status]; !ok {
				t.Fatalf("operation %s is missing documented status %s", op.ID, status)
			}
		}
		if op.RequiredPrecondition() {
			for _, status := range []string{"412", "428"} {
				if _, ok := responses[status]; !ok {
					t.Fatalf("operation %s requires If-Match but does not document %s", op.ID, status)
				}
			}
		}
		parameters, _ := item["parameters"].([]any)
		paramNames := map[string]bool{}
		for _, raw := range parameters {
			if parameter, ok := raw.(map[string]any); ok {
				paramNames[parameter["name"].(string)] = true
			}
		}
		if op.RequiredPrecondition() && !paramNames["If-Match"] {
			t.Fatalf("operation %s does not document the If-Match parameter", op.ID)
		}
		if op.Idempotency != AdminIdempotencyNone && !paramNames["Idempotency-Key"] {
			t.Fatalf("operation %s does not document the Idempotency-Key parameter", op.ID)
		}
		for _, name := range adminPathParameterNames(op.Path) {
			if !paramNames[name] {
				t.Fatalf("operation %s does not document path parameter %s", op.ID, name)
			}
		}
	}
	if count != len(operations) {
		t.Fatalf("documented %d operations, registry has %d", count, len(operations))
	}
}

// TestAdminV1SpecIntegrity validates the registry itself and the cross-cutting
// invariants the contract relies on.
func TestAdminV1SpecIntegrity(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatalf("registry is invalid: %v", err)
	}
	operations := registry.Operations()
	if len(operations) == 0 {
		t.Fatal("registry is empty")
	}
	handlerKeys := map[string]string{}
	legacyPaths := map[string]string{}
	for _, op := range operations {
		if previous, ok := handlerKeys[op.HandlerKey]; ok {
			t.Fatalf("operations %s and %s share handler key %s", previous, op.ID, op.HandlerKey)
		}
		handlerKeys[op.HandlerKey] = op.ID
		if op.LegacyPath != "" {
			legacyKey := op.Method + " " + op.LegacyPath
			if previous, ok := legacyPaths[legacyKey]; ok {
				t.Fatalf("operations %s and %s re-mount the same legacy route %s", previous, op.ID, legacyKey)
			}
			legacyPaths[legacyKey] = op.ID
		}
		if op.Idempotency == AdminIdempotencyStore && !op.RequiredPrecondition() && op.NoSideEffect {
			t.Fatalf("operation %s is idempotent without being a mutation", op.ID)
		}
	}
}

// TestAdminV1ObjectAndDomainFixes pins the domain/object decisions the
// supervisor approved: training keeps content:training in the whole-plane
// domain, content-capture delete resolves the captured row's domain, and
// reports leave the domain decision to the handler so a domain-scoped reports
// administrator keeps today's access.
func TestAdminV1ObjectAndDomainFixes(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id     string
		object string
		action string
		domain AdminDomainSource
	}{
		{"exportTrainingData", "content:training", "export", AdminDomainGlobalWildcard},
		{"labelTrainingOutcome", "content:training", "label", AdminDomainGlobalWildcard},
		{"deleteContentCapture", "content:capture", "delete", AdminDomainCapturedRow},
		{"purgeExpiredContentCaptures", "content:capture", "purge", AdminDomainGlobalWildcard},
		{"insertConfigSetPlaceholder", "", "", ""},
	}
	cases = cases[:len(cases)-1]
	for _, testCase := range cases {
		op, ok := registry.Lookup(testCase.id)
		if !ok {
			t.Fatalf("operation %s is not registered", testCase.id)
		}
		if op.Object != testCase.object || op.Action != testCase.action {
			t.Fatalf("operation %s guard = %s/%s, want %s/%s", testCase.id, op.Object, op.Action, testCase.object, testCase.action)
		}
		if op.Domain != testCase.domain {
			t.Fatalf("operation %s domain = %s, want %s", testCase.id, op.Domain, testCase.domain)
		}
	}
	for _, op := range registry.Operations() {
		if op.Adapter != "reports" {
			continue
		}
		if op.Domain != AdminDomainHandler {
			t.Fatalf("reports operation %s must delegate its domain decision to the handler, got %s", op.ID, op.Domain)
		}
	}
}

// TestAdminV1GuardsMatchToday asserts the guards the handlers enforce today are
// the guards the registry declares.
func TestAdminV1GuardsMatchToday(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	expectations := []struct {
		id     string
		object string
		action string
		checks []AdminCheck
	}{
		{"importConfigSet", authzObjectAdminConfig, "activate", nil},
		{"promoteConfigGroupShadow", authzObjectAdminShadow, "write", []AdminCheck{
			{Object: authzObjectAdminConfig, Action: "write"},
			{Object: authzObjectAdminConfig, Action: "force"},
		}},
		{"replaceConfigGroupTargets", authzObjectAdminConfig, "write", []AdminCheck{{Object: authzObjectAdminConfig, Action: "force"}}},
		{"validateConfigGroupTargets", authzObjectAdminConfig, "write", nil},
		{"startConfigGroupShadow", authzObjectAdminShadow, "write", nil},
		{"getConfigGroupShadow", authzObjectAdminConfig, authzActionRead, nil},
		{"stopConfigGroupShadow", authzObjectAdminShadow, "write", nil},
		{"getAdminMetrics", authzObjectMetrics, authzActionRead, nil},
		{"listAdminSecurityEvents", authzObjectSecurityReports, authzActionRead, nil},
		{"exportAdminSecurityCSV", authzObjectSecurityReports, authzActionExport, nil},
		{"getAdminReportRequest", authzObjectAdminReports, authzActionDrilldown, nil},
	}
	for _, expectation := range expectations {
		op, ok := registry.Lookup(expectation.id)
		if !ok {
			t.Fatalf("operation %s is not registered", expectation.id)
		}
		if op.Object != expectation.object || op.Action != expectation.action {
			t.Fatalf("operation %s guard = %s/%s, want %s/%s", expectation.id, op.Object, op.Action, expectation.object, expectation.action)
		}
		if len(op.ExtraChecks) != len(expectation.checks) {
			t.Fatalf("operation %s has %d extra checks, want %d", expectation.id, len(op.ExtraChecks), len(expectation.checks))
		}
		for i, check := range expectation.checks {
			if op.ExtraChecks[i] != check {
				t.Fatalf("operation %s extra check %d = %+v, want %+v", expectation.id, i, op.ExtraChecks[i], check)
			}
		}
	}
}

// TestAdminV1PaginationDeclarations asserts every paginated operation declares
// a stable tie-break sort key and that no non-GET paginates.
func TestAdminV1PaginationDeclarations(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	paginated := 0
	for _, op := range registry.Operations() {
		if op.Pagination == nil {
			continue
		}
		paginated++
		if op.Method != "GET" {
			t.Fatalf("operation %s paginates but is not a GET", op.ID)
		}
		hasTieBreak := false
		for _, sort := range op.Pagination.Sorts {
			if sort == "id" || sort == "caller_id" || sort == "request_id" || sort == "created_at" {
				hasTieBreak = true
			}
		}
		if !hasTieBreak {
			t.Fatalf("operation %s has no unique tie-break sort key: %v", op.ID, op.Pagination.Sorts)
		}
	}
	if paginated == 0 {
		t.Fatal("no operation declares pagination")
	}
}

// TestAdminProblemCatalogue asserts every registry entry renders a consistent
// problem document and that no catalogue code can break the type URN.
func TestAdminProblemCatalogue(t *testing.T) {
	codes := AdminProblemCodes()
	if len(codes) < 30 {
		t.Fatalf("problem catalogue has %d codes, want the full contract set", len(codes))
	}
	for code, spec := range codes {
		if !validAdminProblemCode(code) {
			t.Fatalf("code %q is not a valid problem code", code)
		}
		if spec.Status < 400 || spec.Status > 599 || spec.Title == "" {
			t.Fatalf("code %q has an invalid spec: %+v", code, spec)
		}
		problem := AdminProblemFor(nil, spec.Status, code)
		if problem.Type != adminProblemTypePrefix+code || problem.Code != code || problem.Status != spec.Status {
			t.Fatalf("code %q renders an inconsistent document: %+v", code, problem)
		}
	}
	// The lanes' approved code spellings must all be present.
	for _, code := range []string{
		"authentication_required", "invalid_credential", "unsupported_authentication",
		"sender_constraint_failed", "emergency_inactive",
		"admin-forbidden", "privilege-escalation", "last-policy-admin", "invalid-policy",
		"authorization-unavailable", "policy-stale",
		"admin-not-found", "admin-method-not-allowed", "admin-cors-rejected", "admin-payload-too-large",
		"admin-tls-required", "admin-proxy-untrusted", "admin-metrics-forbidden",
	} {
		if _, ok := codes[code]; !ok {
			t.Fatalf("approved problem code %q is not registered", code)
		}
	}
}
