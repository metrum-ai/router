// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// The typed route registry of the /admin/v1 contract (ACP-011/012).
//
// One Operation describes one admin resource operation completely: its
// method/path, the legacy handler it re-mounts, the Casbin object/action that
// governs it, its concurrency and idempotency semantics, its pagination and
// filter set, its documented request/response schemas and its deprecation
// window. The registry is the single source of truth for:
//
//   - the HTTP mount (#811 mount layer),
//   - the generated OpenAPI 3.1 document and its drift check,
//   - the route conformance test (registered routes == registry),
//   - the authorization conformance test (one decision per operation).
//
// Adding an admin route means adding an Operation; a route that is not in the
// registry cannot be mounted on the admin listener, and a registry entry that
// is not mounted fails the conformance test.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// AdminConcurrencyMode declares the precondition an operation requires.
// AdminETagSource selects the strong validator source of an operation.
type AdminETagSource string

const (
	// AdminETagConfig is the default: the configuration revision validator.
	AdminETagConfig AdminETagSource = ""
	// AdminETagPolicy validates against the policy revision.
	AdminETagPolicy AdminETagSource = "policy"
)

type AdminConcurrencyMode string

const (
	// AdminConcurrencyNone: the operation has no resource representation to
	// precondition on (a command addressed by an immutable ID).
	AdminConcurrencyNone AdminConcurrencyMode = "none"
	// AdminConcurrencyRevision: If-Match is required and validated against the
	// runtime scope's configuration revision.
	AdminConcurrencyRevision AdminConcurrencyMode = "revision"
	// AdminConcurrencyRepresentation: If-Match is required and validated
	// against the resource's own representation ETag.
	AdminConcurrencyRepresentation AdminConcurrencyMode = "representation"
)

// AdminIdempotencyMode declares where replay protection lives.
type AdminIdempotencyMode string

const (
	AdminIdempotencyNone AdminIdempotencyMode = "none"
	// AdminIdempotencyHandler: the handler records idempotency_key and
	// request_hash in the configuration change event inside the write
	// transaction (the existing config control-plane path).
	AdminIdempotencyHandler AdminIdempotencyMode = "handler"
	// AdminIdempotencyStore: the contract layer records the response in the
	// idempotency store.
	AdminIdempotencyStore AdminIdempotencyMode = "store"
)

// AdminDomainSource declares how the authorization domain is resolved. The
// domain is never derived from a request header.
type AdminDomainSource string

const (
	// AdminDomainGlobal authorizes in the plane-global domain ("default").
	AdminDomainGlobal AdminDomainSource = "global"
	// AdminDomainGlobalWildcard authorizes in the whole-plane wildcard domain
	// ("*"), which is how today's authorizeGlobalAdmin decisions are taken.
	AdminDomainGlobalWildcard AdminDomainSource = "global-wildcard"
	// AdminDomainCallerTarget authorizes in the stored caller's
	// "<project>/<environment>" domain.
	AdminDomainCallerTarget AdminDomainSource = "caller-target"
	// AdminDomainCapturedRow authorizes in the domain of the captured row's
	// originating caller, including today's fallback when the row carries none.
	AdminDomainCapturedRow AdminDomainSource = "captured-row"
	// AdminDomainHandler leaves the domain decision to the handler's existing
	// per-domain checks (reports). The contract layer performs no domain-wide
	// gate, so a domain-scoped reports administrator keeps today's access.
	AdminDomainHandler AdminDomainSource = "handler"
)

// AdminCheck is one additional authorization decision an operation requires.
type AdminCheck struct {
	Object string
	Action string
	// When restricts the check to a request condition. Empty means the check is
	// unconditional. Today's handlers ask for force only when the request sets
	// force, so a write without it must not require the grant.
	When AdminCheckWhen
}

// AdminCheckWhen is the request condition that triggers a check.
type AdminCheckWhen string

const (
	// AdminCheckAlways is the zero value: the check always applies.
	AdminCheckAlways AdminCheckWhen = ""
	// AdminCheckBodyForce applies the check only when the request body sets
	// force: true.
	AdminCheckBodyForce AdminCheckWhen = "body.force"
)

// adminBodyFlag reports whether a decoded request body sets a boolean flag.
func adminBodyFlag(body map[string]any, name string) bool {
	if body == nil {
		return false
	}
	value, ok := body[name]
	if !ok {
		return false
	}
	flag, ok := value.(bool)
	return ok && flag
}

// ChecksFor returns the additional checks that apply to one request body. The
// mount uses it after decoding nothing itself: the flag is read from the same
// documented body field the handler uses.
func (op Operation) ChecksFor(body map[string]any) []AdminCheck {
	if len(op.ExtraChecks) == 0 {
		return nil
	}
	out := make([]AdminCheck, 0, len(op.ExtraChecks))
	for _, check := range op.ExtraChecks {
		switch check.When {
		case AdminCheckAlways:
			out = append(out, check)
		case AdminCheckBodyForce:
			if adminBodyFlag(body, "force") {
				out = append(out, check)
			}
		}
	}
	return out
}

// AdminPaginationSpec declares the collection contract of a list operation.
type AdminPaginationSpec struct {
	Sorts       []string
	DefaultSort string
	// CursorEndpoint overrides the cursor payload's endpoint ID; empty uses the
	// operation ID.
	CursorEndpoint string
}

// Operation is one /admin/v1 operation.
type Operation struct {
	ID          string
	Method      string
	Path        string
	LegacyPath  string
	Summary     string
	Description string
	// Object/Action are the Casbin decision the contract layer takes before the
	// handler runs.
	Object string
	Action string
	// ExtraChecks are additional decisions the operation requires (shadow
	// promotion needs both admin:shadow/write and admin:config/write; a forced
	// write needs admin:config/force).
	ExtraChecks []AdminCheck
	Domain      AdminDomainSource
	// Request/Response are typed schema prototypes (nil means no body).
	Request  any
	Response any
	// Errors lists route-specific status codes; the generator always adds the
	// contract-wide 400/401/403/429/500/503.
	Errors      []int
	Concurrency AdminConcurrencyMode
	Idempotency AdminIdempotencyMode
	Pagination  *AdminPaginationSpec
	Query       []adminQueryParameter
	RateClass   AdminRateClass
	// SuccessStatus is the documented success code; 0 means 200.
	SuccessStatus int
	// OpaqueQuery marks an operation whose query parameters are validated by the
	// handler itself (the reports surface already returns typed 400s). The
	// contract layer then skips its unknown-parameter and limit validation.
	OpaqueQuery bool
	// SensitiveResponse marks an operation whose success body carries a credential
	// (a caller token). Such a response is NEVER recorded for replay: a token is
	// shown exactly once, so a retry answers "in doubt" instead of returning it
	// again. This is the only way a credential could otherwise reach the
	// Idempotency-Key store.
	SensitiveResponse bool
	// NoSideEffect marks read-shaped POSTs (validation/dry-run) that must not
	// require a precondition.
	NoSideEffect bool

	// ETagSource names which strong validator gates this operation's If-Match:
	// the configuration revision (default) or the POLICY revision. A policy
	// write must never be gated on the config tag.
	ETagSource AdminETagSource
	Deprecated *AdminDeprecation
	// HandlerKey names the handler the mount layer binds to this operation.
	HandlerKey string
	// Adapter is optional: it rewrites the request for handlers that dispatch on
	// the legacy path (the reports catch-all).
	Adapter string
}

// normalized fills the zero values that mean "not applicable".
func (op Operation) normalized() Operation {
	if op.Concurrency == "" {
		op.Concurrency = AdminConcurrencyNone
	}
	if op.Idempotency == "" {
		op.Idempotency = AdminIdempotencyNone
	}
	return op
}

// Pattern is the Go 1.22 mux pattern for the operation.
func (op Operation) Pattern() string { return strings.ToUpper(op.Method) + " " + op.Path }

// RequiredPrecondition reports whether If-Match is mandatory.
func (op Operation) RequiredPrecondition() bool {
	return op.Concurrency != AdminConcurrencyNone
}

var adminOperationActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var adminOperationObjectPattern = regexp.MustCompile(`^[a-z][a-z0-9:_-]{0,127}$`)

var adminOperationPathPattern = regexp.MustCompile(`^/admin/v1(/[a-z0-9][a-z0-9._:-]*|/\{[a-z_]+\}|/\{[a-z_]+\.\.\.\})*$`)

// validate rejects a registry entry that could mount an ungoverned route, an
// unanchored action, or a schema-less operation.
func (op Operation) validate() error {
	op = op.normalized()
	if !regexp.MustCompile(`^[a-z][A-Za-z0-9]{2,63}$`).MatchString(op.ID) {
		return fmt.Errorf("operation ID %q must be lowerCamelCase", op.ID)
	}
	switch op.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("operation %s has unsupported method %q", op.ID, op.Method)
	}
	if !adminOperationPathPattern.MatchString(op.Path) {
		return fmt.Errorf("operation %s path %q is not a valid /admin/v1 path", op.ID, op.Path)
	}
	if !adminOperationObjectPattern.MatchString(op.Object) {
		return fmt.Errorf("operation %s has invalid Casbin object %q", op.ID, op.Object)
	}
	if !adminOperationActionPattern.MatchString(op.Action) {
		return fmt.Errorf("operation %s has invalid Casbin action %q (must be one anchored literal action)", op.ID, op.Action)
	}
	for _, check := range op.ExtraChecks {
		switch check.When {
		case AdminCheckAlways, AdminCheckBodyForce:
		default:
			return fmt.Errorf("operation %s has invalid check condition %q", op.ID, check.When)
		}
	}
	for _, check := range op.ExtraChecks {
		if !adminOperationObjectPattern.MatchString(check.Object) || !adminOperationActionPattern.MatchString(check.Action) {
			return fmt.Errorf("operation %s has invalid extra check %s/%s", op.ID, check.Object, check.Action)
		}
	}
	switch op.Domain {
	case AdminDomainGlobal, AdminDomainCallerTarget, AdminDomainCapturedRow, AdminDomainHandler:
	default:
		return fmt.Errorf("operation %s has invalid domain source %q", op.ID, op.Domain)
	}
	switch op.Concurrency {
	case AdminConcurrencyNone, AdminConcurrencyRevision, AdminConcurrencyRepresentation:
	default:
		return fmt.Errorf("operation %s has invalid concurrency mode %q", op.ID, op.Concurrency)
	}
	switch op.Idempotency {
	case AdminIdempotencyNone, AdminIdempotencyHandler, AdminIdempotencyStore:
	default:
		return fmt.Errorf("operation %s has invalid idempotency mode %q", op.ID, op.Idempotency)
	}
	if op.Method == "GET" && op.Idempotency != AdminIdempotencyNone {
		return fmt.Errorf("operation %s is a GET and cannot declare idempotency", op.ID)
	}
	if op.NoSideEffect && op.Idempotency != AdminIdempotencyNone {
		return fmt.Errorf("operation %s has no side effect and cannot declare idempotency", op.ID)
	}
	if op.NoSideEffect && op.Concurrency != AdminConcurrencyNone {
		return fmt.Errorf("operation %s has no side effect and must not require a precondition", op.ID)
	}
	if op.Idempotency == AdminIdempotencyStore && op.Concurrency == AdminConcurrencyNone && op.Method == "GET" {
		return fmt.Errorf("operation %s cannot be both idempotent and a GET", op.ID)
	}
	if op.Pagination != nil {
		if op.Method != "GET" {
			return fmt.Errorf("operation %s paginates but is not a GET", op.ID)
		}
		if len(op.Pagination.Sorts) == 0 {
			return fmt.Errorf("operation %s paginates without sort keys", op.ID)
		}
		if !adminValueInEnum(op.Pagination.DefaultSort, op.Pagination.Sorts) {
			return fmt.Errorf("operation %s default sort %q is not one of its sorts", op.ID, op.Pagination.DefaultSort)
		}
	}
	if op.RateClass != AdminRateClassRead && op.RateClass != AdminRateClassWrite {
		return fmt.Errorf("operation %s has invalid rate class %q", op.ID, op.RateClass)
	}
	if op.Deprecated != nil {
		if err := op.Deprecated.Validate(); err != nil {
			return fmt.Errorf("operation %s deprecation is invalid: %w", op.ID, err)
		}
	}
	if strings.TrimSpace(op.HandlerKey) == "" {
		return fmt.Errorf("operation %s has no handler key", op.ID)
	}
	if op.OpaqueQuery && op.Adapter == "" {
		return fmt.Errorf("operation %s declares an opaque query but has no adapter", op.ID)
	}
	if op.SuccessStatus != 0 && (op.SuccessStatus < 200 || op.SuccessStatus > 299) {
		return fmt.Errorf("operation %s has invalid success status %d", op.ID, op.SuccessStatus)
	}
	return nil
}

// AdminV1Registry is an immutable set of operations.
type AdminV1Registry struct {
	operations []Operation
	byID       map[string]Operation
	byPattern  map[string]Operation
}

// NewAdminV1Registry validates and freezes a registry.
func NewAdminV1Registry(operations []Operation) (*AdminV1Registry, error) {
	registry := &AdminV1Registry{
		operations: make([]Operation, 0, len(operations)),
		byID:       make(map[string]Operation, len(operations)),
		byPattern:  make(map[string]Operation, len(operations)),
	}
	for _, op := range operations {
		if err := registry.Register(op); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register adds one operation, rejecting duplicates by ID or method+path.
func (r *AdminV1Registry) Register(op Operation) error {
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	op = op.normalized()
	if err := op.validate(); err != nil {
		return err
	}
	if _, exists := r.byID[op.ID]; exists {
		return fmt.Errorf("operation ID %q is already registered", op.ID)
	}
	if _, exists := r.byPattern[op.Pattern()]; exists {
		return fmt.Errorf("route %s is already registered", op.Pattern())
	}
	r.operations = append(r.operations, op)
	r.byID[op.ID] = op
	r.byPattern[op.Pattern()] = op
	return nil
}

// Operations returns the registered operations sorted by path then method, which
// is also the order the generated OpenAPI document uses.
func (r *AdminV1Registry) Operations() []Operation {
	if r == nil {
		return nil
	}
	out := make([]Operation, len(r.operations))
	copy(out, r.operations)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Lookup returns the operation registered under an operation ID.
func (r *AdminV1Registry) Lookup(id string) (Operation, bool) {
	if r == nil {
		return Operation{}, false
	}
	op, ok := r.byID[id]
	return op, ok
}

// LookupPattern returns the operation registered for a mux pattern.
func (r *AdminV1Registry) LookupPattern(pattern string) (Operation, bool) {
	if r == nil {
		return Operation{}, false
	}
	op, ok := r.byPattern[pattern]
	return op, ok
}

// AdminV1Operations returns the built-in registry contents. It is the source of
// truth for the OpenAPI document and the conformance test.
func AdminV1Operations() []Operation {
	return append([]Operation(nil), adminV1OperationSpecs...)
}

// AdminV1DefaultRegistry returns the built-in registry.
func AdminV1DefaultRegistry() (*AdminV1Registry, error) {
	return NewAdminV1Registry(adminV1OperationSpecs)
}
