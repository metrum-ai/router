// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// The /admin/v1 mount layer (ACP-011).
//
// RegisterAdminV1 is what the admin listener mounts (Service.RegisterAdminRoutes
// → RegisterAdminV1 as one line). For every registered operation it wraps the
// operation's existing handler in one deterministic chain:
//
//	request ID / contract headers (no-store, deprecation)
//	→ Authenticator (#814)                     401 on failure
//	→ authentication-failure throttle (#814 hook)  429 while cooling down
//	→ per-identity rate limit                  429 with Retry-After
//	→ domain resolution (explicit request target)
//	→ Authorizer (#812): one decision per operation, plus always-checks
//	→ query/pagination validation              400
//	→ If-Match precondition (428/412) for revision operations
//	→ Idempotency-Key (replay / 409 / in-doubt)
//	→ the existing handler, with captured response
//	→ idempotency completion
//
// The handler itself is never forked: the same http.HandlerFunc that serves the
// legacy route runs here, with Authenticate/Authorize closures that read the
// principal and the decision from the request context instead of re-authenticating
// or consulting a permission map. That keeps #812's Authorizer the single decision
// point on both surfaces.
//
// The mount registers wrapped handlers with Handle only; it never installs global
// middleware, so the listener's hardening stays outermost and the audit middleware
// keeps its position in that chain.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// adminRouter is the part of #809's AdminMux the mount registers on. It is deliberately
// just Handle: the mount registers wrapped handlers and never installs global
// middleware, so the listener's hardening stays outermost and the audit middleware
// keeps its position in that chain. *AdminMux satisfies it, and so does
// http.ServeMux, which is what the conformance test mounts against.
type adminRouter interface {
	Handle(pattern string, handler http.Handler)
}

// AdminV1Deps wires the contract layer into the running service.
type AdminV1Deps struct {
	// Registry is the operation set to mount; nil uses the built-in registry.
	Registry *AdminV1Registry
	// Authenticate is #814's chain.
	Authenticate Authenticator
	// Authorize is #812's sole decision point. It is a function field so this
	// mount does not declare a competing Authorizer interface: the service passes
	// its authorizer's Authorize method value.
	Authorize func(ctx context.Context, principal Principal, domain, object, action string) error
	// Handlers maps an operation's HandlerKey to the existing handler.
	Handlers map[string]http.HandlerFunc
	// ResolveDomain maps a principal to its credential domain (the domain the
	// credential is configured for). It is used for plane-global operations and
	// for the reports operations whose per-domain decisions stay in the handler.
	ResolveDomain func(Principal) string
	// ResolveCallerDomain resolves the authorization domain of a caller-directory
	// resource from the stored caller row, or from the requested
	// project/environment for an issue. It never reads a header.
	ResolveCallerDomain func(*http.Request) (string, error)
	// ResolveCapturedDomains resolves the domains of the captured row addressed by
	// the request, so content-capture deletion keeps today's per-domain decision.
	ResolveCapturedDomains func(*http.Request) ([]string, error)
	// CurrentETag returns the strong validator of a revision-scoped resource.
	CurrentETag func(*http.Request) (string, int64, error)
	// CursorCodec signs pagination cursors.
	CursorCodec *AdminCursorCodec
	// Pagination holds the configured limits.
	Pagination AdminPaginationConfig
	// Idempotency is the Idempotency-Key store (nil disables replay protection
	// for store-mode operations, which then answer 503 rather than run twice).
	Idempotency   AdminIdempotencyKeyStore
	IdempotencyOn bool
	Limiter       AdminRateLimiter
	Throttle      AdminAuthThrottle
	Clock         func() time.Time
}

// AdminAuditEvent is one audit observation the contract layer emits (#816 fills
// the sink). It carries no credential material.
type AdminAuditEvent struct {
	RequestID    string
	Principal    Principal
	Domain       string
	Object       string
	Action       string
	OperationID  string
	Method       string
	Path         string
	Status       int
	Outcome      string
	ReasonCode   string
	Idempotent   bool
	Replayed     bool
	Duration     time.Duration
	RequestBytes int64
}

// AdminObservation is one metrics observation (#816 fills the sink).
type AdminObservation struct {
	OperationID   string
	Object        string
	Action        string
	Method        string
	Status        int
	Duration      time.Duration
	RateLimited   bool
	AuthFailed    bool
	PrincipalKind string
}

func (d AdminV1Deps) now() time.Time {
	if d.Clock != nil {
		return d.Clock().UTC()
	}
	return time.Now().UTC()
}

func (d AdminV1Deps) domainFor(principal Principal) string {
	if d.ResolveDomain != nil {
		if domain := strings.TrimSpace(d.ResolveDomain(principal)); domain != "" {
			return domain
		}
	}
	return AdminGlobalDomain
}

// RegisterAdminV1 mounts every registered operation on r, which is #809's AdminMux
// (or a plain http.ServeMux in tests).
func RegisterAdminV1(r adminRouter, deps AdminV1Deps) error {
	if r == nil {
		return errors.New("admin router is nil")
	}
	if deps.Authenticate == nil || deps.Authorize == nil {
		return errors.New("admin v1 mount requires an authenticator and an authorizer")
	}
	registry := deps.Registry
	if registry == nil {
		var err error
		registry, err = AdminV1DefaultRegistry()
		if err != nil {
			return err
		}
	}
	mount := &adminV1Mount{deps: deps, registry: registry, codec: deps.CursorCodec}
	for _, op := range registry.Operations() {
		handler, ok := deps.Handlers[op.HandlerKey]
		if !ok || handler == nil {
			return fmt.Errorf("operation %s has no handler for key %q", op.ID, op.HandlerKey)
		}
		if err := adminHandleGuarded(r, op.Pattern(), mount.wrap(op, handler)); err != nil {
			return err
		}
	}
	return nil
}

// adminHandleGuarded registers one pattern, converting a mux pattern conflict (a
// double mount, or a listener route colliding with /admin/v1) into an error instead
// of a startup panic.
func adminHandleGuarded(r adminRouter, pattern string, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("register %s: %v", pattern, recovered)
		}
	}()
	r.Handle(pattern, handler)
	return nil
}

// AdminV1MountedPatterns returns the mux patterns the mount registers, for the
// route conformance test.
func AdminV1MountedPatterns(operations []Operation) []string {
	patterns := make([]string, 0, len(operations))
	for _, op := range operations {
		patterns = append(patterns, op.Pattern())
	}
	return patterns
}

type adminV1Mount struct {
	deps     AdminV1Deps
	registry *AdminV1Registry
	codec    *AdminCursorCodec
}

func (m *adminV1Mount) wrap(op Operation, handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := m.deps.now()
		requestIDValue := requestID()
		ctx := WithAdminRequestID(r.Context(), requestIDValue)
		r = r.WithContext(ctx)

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set(adminRequestIDHeader, requestIDValue)
		adminDeprecationHeaders(w, op.Deprecated)

		observation := AdminObservation{OperationID: op.ID, Object: op.Object, Action: op.Action, Method: op.Method}
		audit := AdminAuditEvent{
			RequestID: requestIDValue, OperationID: op.ID, Object: op.Object, Action: op.Action,
			Method: op.Method, Path: op.Path,
		}

		finish := func(status int, outcome, reason string) {
			observation.Status = status
			observation.Duration = m.deps.now().Sub(started)
			audit.Status = status
			audit.Outcome = outcome
			audit.ReasonCode = reason
			audit.Duration = observation.Duration
		}

		// Admission control for credential guessing: the claim and the source
		// address are checked BEFORE the credential is validated, so a flood of
		// failed attempts is actually slowed down. Only a validated success clears
		// the state.
		if m.deps.Throttle != nil {
			if retryAfter, ok := m.deps.Throttle.Allow(adminClaimedCredential(r), adminSourceAddress(r), started); !ok {
				problem := AdminProblemFor(r, http.StatusTooManyRequests, "authentication-throttled")
				problem.RetryAfterSeconds = adminRetryAfterSeconds(retryAfter)
				WriteAdminProblem(w, r, problem)
				finish(problem.Status, "denied", problem.Code)
				return
			}
		}

		// Authentication (#814 chain). The claimed credential ID is only used to
		// give the throttle a key; it is never trusted for a decision.
		principal, err := m.deps.Authenticate.Authenticate(ctx, r)
		if err != nil {
			status, code := adminAuthProblem(err)
			observation.AuthFailed = true
			if m.deps.Throttle != nil {
				m.deps.Throttle.RecordFailure(adminClaimedCredential(r), adminSourceAddress(r), started)
			}
			writeAdminProblemCode(w, r, status, code, "")
			finish(status, "denied", code)
			return
		}
		observation.PrincipalKind = principal.Kind
		if m.deps.Throttle != nil {
			m.deps.Throttle.RecordSuccess(adminClaimedCredential(r), adminSourceAddress(r))
		}
		ctx = WithPrincipal(ctx, principal)
		r = r.WithContext(ctx)
		audit.Principal = principal
		audit.Domain = m.deps.domainFor(principal)

		// Per-identity rate limit.
		if m.deps.Limiter != nil {
			identity := AdminIdentityKey(principal.Kind, principal.Subject)
			if retryAfter, ok := m.deps.Limiter.Allow(identity, op.RateClass, started); !ok {
				problem := AdminProblemFor(r, http.StatusTooManyRequests, "rate-limited")
				problem.RetryAfterSeconds = adminRetryAfterSeconds(retryAfter)
				WriteAdminProblem(w, r, problem)
				observation.RateLimited = true
				finish(problem.Status, "denied", problem.Code)
				return
			}
		}

		// Domain resolution: an explicit request target, never a header.
		domains, problem := m.resolveDomains(op, r, principal)
		if problem != nil {
			WriteAdminProblem(w, r, *problem)
			finish(problem.Status, "denied", problem.Code)
			return
		}
		if len(domains) > 0 {
			audit.Domain = domains[0]
		}

		// Authorization: one primary decision per operation plus its always-checks.
		// Reports and the caller-directory listing keep the handler's own per-domain
		// decisions authoritative, so no contract-layer gate runs here.
		if op.Domain != AdminDomainHandler && (op.Domain != AdminDomainCallerTarget || op.Method != "GET") {
			for _, domain := range domains {
				if decisionErr := m.deps.Authorize(ctx, principal, domain, op.Object, op.Action); decisionErr != nil {
					status, code := adminAuthorizationProblem(decisionErr)
					writeAdminProblemCode(w, r, status, code, "")
					finish(status, "denied", code)
					return
				}
			}
			checkDomain := AdminGlobalDomain
			if len(domains) > 0 {
				checkDomain = domains[0]
			}
			// Conditional checks (today: force) are evaluated from the request body the
			// handler will read, so a plain write never needs the force grant. The body is
			// peeked, not consumed.
			bodyFlags := map[string]any{}
			for _, check := range op.ExtraChecks {
				if check.When == AdminCheckBodyForce {
					if err := peekAdminJSONBody(r, &bodyFlags); err != nil {
						writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-request", "")
						finish(http.StatusBadRequest, "denied", "invalid-request")
						return
					}
					break
				}
			}
			for _, check := range op.ChecksFor(bodyFlags) {
				if decisionErr := m.deps.Authorize(ctx, principal, checkDomain, check.Object, check.Action); decisionErr != nil {
					status, code := adminAuthorizationProblem(decisionErr)
					writeAdminProblemCode(w, r, status, code, "")
					finish(status, "denied", code)
					return
				}
			}
			// Record exactly the decisions taken, each with the domain it was taken in, so a
			// handler gate can only reuse a decision that really happened. A check whose
			// condition did not apply (force without a forced body) is not recorded, and its
			// handler gate therefore reaches the Authorizer.
			taken := adminV1Tally{}
			for _, domain := range domains {
				taken.add(op.Object, op.Action, domain)
			}
			for _, check := range op.ChecksFor(bodyFlags) {
				taken.add(check.Object, check.Action, checkDomain)
			}
			ctx = WithAdminV1Decision(ctx, adminV1Decision{Taken: taken.entries})
			r = r.WithContext(ctx)
		}

		// Pagination and query validation.
		page := adminPageRequest{Limit: m.deps.Pagination.normalized().DefaultLimit, Direction: "asc", Filters: map[string]string{}}
		if !op.OpaqueQuery {
			parsed, pageProblem := adminParsePage(r, op, m.deps.Pagination, m.codec)
			if pageProblem != nil {
				WriteAdminProblem(w, r, *pageProblem)
				finish(pageProblem.Status, "denied", pageProblem.Code)
				return
			}
			page = parsed
		}

		// Idempotency-Key replay takes precedence over the precondition: a retry of
		// an already committed write must return its recorded response, not 412.
		var idempotencyScope adminIdempotencyScope
		if op.Idempotency != AdminIdempotencyNone && !m.deps.IdempotencyOn &&
			strings.TrimSpace(r.Header.Get("Idempotency-Key")) != "" {
			// A client asked for replay protection the process cannot provide: refuse
			// rather than silently running the mutation unguarded.
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "the idempotency store is unavailable")
			finish(http.StatusServiceUnavailable, "denied", "unavailable")
			return
		}
		idempotencyActive := op.Idempotency == AdminIdempotencyStore && m.deps.IdempotencyOn
		if idempotencyActive {
			key, validKey := adminIdempotencyKeyFrom(r)
			if !validKey {
				writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-idempotency-key", "")
				finish(http.StatusBadRequest, "denied", "invalid-idempotency-key")
				return
			}
			if key != "" {
				if m.deps.Idempotency == nil {
					// Fail closed: never run a replayable mutation without the store.
					writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "the idempotency store is unavailable")
					finish(http.StatusServiceUnavailable, "denied", "unavailable")
					return
				}
				idempotencyScope = adminIdempotencyScope{
					PrincipalKind: principal.Kind, PrincipalSubject: principal.Subject,
					Domain: audit.Domain, Operation: op.ID, Key: key,
					RequestHash: adminRequestHash(r),
				}
				record, outcome, beginErr := m.deps.Idempotency.Begin(idempotencyScope, started)
				if beginErr != nil {
					writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "the idempotency store is unavailable")
					finish(http.StatusServiceUnavailable, "denied", "unavailable")
					return
				}
				switch outcome {
				case adminIdempotencyReplay:
					audit.Replayed = true
					replayAdminRecordedResponse(w, record)
					finish(record.ResponseStatus, "allowed", "replayed")
					return
				case adminIdempotencyConflict:
					writeAdminProblemCode(w, r, http.StatusConflict, "idempotency-key-conflict", "Idempotency-Key was already used with a different request")
					finish(http.StatusConflict, "denied", "idempotency-key-conflict")
					return
				case adminIdempotencyInDoubt:
					problem := AdminProblemFor(r, http.StatusConflict, "idempotent-request-in-doubt")
					problem.RetryAfterSeconds = 1
					WriteAdminProblem(w, r, problem)
					finish(http.StatusConflict, "denied", problem.Code)
					return
				}
				audit.Idempotent = true
			}
		}

		releaseClaim := func() {
			if idempotencyActive && idempotencyScope.Key != "" {
				_ = m.deps.Idempotency.Abort(idempotencyScope)
			}
		}

		// Precondition (ACP-015). A handler-mode operation owns its replay lookup and
		// revision guard (the change-event unique index, inside the write
		// transaction), so on a keyed request the contract layer requires the header
		// but leaves the staleness verdict to the handler: a committed retry must
		// replay its recorded result instead of answering 412 here. A keyed request
		// that is NOT a replay still reaches the handler's guarded update, which
		// receives the translated revision below.
		keyedHandlerReplay := op.Idempotency == AdminIdempotencyHandler &&
			strings.TrimSpace(r.Header.Get("Idempotency-Key")) != ""
		var translationRevision int64
		if op.RequiredPrecondition() {
			currentETag, revision, etagErr := m.currentETag(r)
			if etagErr != nil {
				releaseClaim()
				writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "the configuration revision is unavailable")
				finish(http.StatusServiceUnavailable, "denied", "unavailable")
				return
			}
			translationRevision = revision
			outcome := checkAdminIfMatch(r, currentETag)
			if keyedHandlerReplay && outcome.Present && !outcome.Invalid {
				// Presence is required; the handler decides replay vs conflict.
				outcome.Matched = true
			}
			switch {
			case outcome.Invalid:
				releaseClaim()
				writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-if-match", "If-Match must be a strong entity tag")
				finish(http.StatusBadRequest, "denied", "invalid-if-match")
				return
			case !outcome.Present:
				releaseClaim()
				problem := AdminProblemFor(r, http.StatusPreconditionRequired, "precondition-required")
				problem.Detail = "this operation requires If-Match with the resource's current ETag"
				problem.CurrentETag = currentETag
				problem.CurrentRevision = &revision
				w.Header().Set("ETag", currentETag)
				WriteAdminProblem(w, r, problem)
				finish(problem.Status, "denied", problem.Code)
				return
			case !outcome.Matched:
				releaseClaim()
				problem := AdminProblemFor(r, http.StatusPreconditionFailed, "precondition-failed")
				problem.Detail = "If-Match does not match the current revision"
				problem.CurrentETag = currentETag
				problem.CurrentRevision = &revision
				w.Header().Set("ETag", currentETag)
				WriteAdminProblem(w, r, problem)
				finish(problem.Status, "denied", problem.Code)
				return
			}
		}

		// Conditional GET, only for the operations that declare a revision
		// validator. Reports, metrics and status have no configuration-revision
		// validator, so a 304 for them could hide a body that changed independently.
		if op.Method == "GET" && adminOperationPublishesRevisionETag(op) {
			if currentETag, _, etagErr := m.currentETag(r); etagErr == nil && currentETag != "" && adminIfNoneMatchHit(r, currentETag) {
				adminWriteNotModified(w, currentETag)
				finish(http.StatusNotModified, "allowed", "")
				return
			}
		}

		// Mark the request as an authenticated admin-plane request without claiming any
		// decision: reports and the caller listing take their own decisions, so recording
		// theirs here would let a handler gate skip the Authorizer.
		if _, ok := AdminV1DecisionFrom(ctx); !ok {
			ctx = WithAdminV1Decision(ctx, adminV1Decision{})
			r = r.WithContext(ctx)
		}

		// The handler keeps today's bare-revision precondition parser, so translate
		// the strong validator the contract layer accepted into that form. The
		// decision above already validated the value.
		adminTranslatePreconditionForHandler(r, translationRevision)

		// The existing handler, with its response captured for idempotency.
		capture := newAdminCaptureWriter(w, w, 0)
		// Publish the parsed page so a collection handler can apply its limit, keyset
		// filter and next-page link without knowing the cursor codec.
		if pageCtx := adminPageContext(capture, r, op, page, m.codec); pageCtx != nil {
			ctx = WithAdminPage(ctx, pageCtx)
			r = r.WithContext(ctx)
		}
		handler(capture, r)
		responseStatus := capture.Status()
		if !capture.Written() {
			responseStatus = http.StatusOK
		}
		if responseStatus >= 400 {
			adminRewriteHandlerProblem(capture, r, responseStatus)
			// A handler-side refusal (for example a conditional force check) is a
			// denial too: record it, and refuse silently if that is impossible.
			if responseStatus == http.StatusForbidden || responseStatus == http.StatusUnauthorized {
			}
		}
		if idempotencyActive && idempotencyScope.Key != "" {
			switch {
			case responseStatus >= 500:
				_ = m.deps.Idempotency.Abort(idempotencyScope)
			case op.SensitiveResponse, capture.Overflowed():
				// A credential-bearing or oversized body is never persisted. The claim
				// stays, so a retry answers "in doubt" (one-time secret unavailable)
				// instead of re-executing or replaying a partial result.
				record := adminIdempotencyRecord{ResponseStatus: responseStatus, Sensitive: true}
				_ = m.deps.Idempotency.Complete(idempotencyScope, record, m.deps.now())
				capture.Header().Set("Idempotent-Recorded", "false")
			default:
				record := adminIdempotencyRecord{
					ResponseStatus:      responseStatus,
					ResponseContentType: capture.Header().Get("Content-Type"),
					ResponseETag:        capture.Header().Get("ETag"),
					ResponseBody:        capture.BodyString(),
				}
				if completeErr := m.deps.Idempotency.Complete(idempotencyScope, record, m.deps.now()); completeErr != nil {
					// The mutation already committed; the claim is left in doubt so a
					// retry cannot execute it twice.
					capture.Header().Set("Idempotent-Recorded", "false")
				} else {
					capture.Header().Set("Idempotent-Recorded", "true")
				}
			}
		}
		_ = page
		capture.Flush()
		finish(responseStatus, "allowed", "")
	})
}

func (m *adminV1Mount) currentETag(r *http.Request) (string, int64, error) {
	if m.deps.CurrentETag == nil {
		return "", 0, errors.New("no etag source")
	}
	return m.deps.CurrentETag(r)
}

// resolveDomains resolves the authorization domain set for the operation.
func (m *adminV1Mount) resolveDomains(op Operation, r *http.Request, principal Principal) ([]string, *AdminProblem) {
	switch op.Domain {
	case AdminDomainGlobal, AdminDomainGlobalWildcard:
		// Both declaration values target the plane-global domain, the shared
		// AdminGlobalDomain the authorization mount exports.
		return []string{AdminGlobalDomain}, nil
	case AdminDomainHandler:
		// The handler decides for itself; the credential domain is what today's
		// legacy handlers use.
		return []string{m.deps.domainFor(principal)}, nil
	case AdminDomainCallerTarget:
		if op.Method == "GET" {
			// A collection (the caller directory listing) has no single target domain: the
			// handler authorizes each row in that row's stored domain. Taking one
			// contract-layer decision here would either deny a legitimate listing or
			// authorize the whole directory from a query parameter.
			return nil, nil
		}
		if m.deps.ResolveCallerDomain == nil {
			problem := AdminProblemFor(r, http.StatusServiceUnavailable, "unavailable")
			problem.Detail = "the caller directory is unavailable"
			return nil, &problem
		}
		domain, err := m.deps.ResolveCallerDomain(r)
		if err != nil {
			problem := AdminProblemFor(r, http.StatusServiceUnavailable, "unavailable")
			problem.Detail = "the caller directory is unavailable"
			return nil, &problem
		}
		if strings.TrimSpace(domain) == "" {
			problem := AdminProblemFor(r, http.StatusNotFound, "not-found")
			problem.Detail = "the caller is not in the active configuration"
			return nil, &problem
		}
		return []string{domain}, nil
	case AdminDomainCapturedRow:
		if m.deps.ResolveCapturedDomains == nil {
			problem := AdminProblemFor(r, http.StatusServiceUnavailable, "unavailable")
			problem.Detail = "the content capture store is unavailable"
			return nil, &problem
		}
		domains, err := m.deps.ResolveCapturedDomains(r)
		if err != nil {
			problem := AdminProblemFor(r, http.StatusInternalServerError, "internal-error")
			return nil, &problem
		}
		if len(domains) == 0 {
			// Today's fallback when a captured row carries no caller domain: the
			// plane-global content:capture grant is required.
			return []string{AdminGlobalDomain}, nil
		}
		out := make([]string, 0, len(domains))
		for _, domain := range domains {
			if strings.TrimSpace(domain) == "" || domain == contentCaptureUnknownDomain {
				problem := AdminProblemFor(r, http.StatusForbidden, "admin-forbidden")
				problem.Detail = "the captured request has no resolvable caller domain"
				return nil, &problem
			}
			out = append(out, domain)
		}
		return out, nil
	default:
		problem := AdminProblemFor(r, http.StatusInternalServerError, "internal-error")
		return nil, &problem
	}
}

// adminV1Decision records the decisions the contract layer already took for this
// request, so a handler's own guard does not ask the Authorizer a second time for
// the same object/action. A genuine secondary check (force) carries a different
// object/action and still reaches the Authorizer.
type adminV1Decision struct {
	// Taken holds exactly the decisions this request's contract layer took, each with the
	// domain it was taken in.
	Taken []adminV1Taken
}

// adminV1Taken is one decision: object, action and the domain it was taken in.
type adminV1Taken struct {
	Object string
	Action string
	Domain string
}

// adminV1Tally accumulates taken decisions without duplicates.
type adminV1Tally struct{ entries []adminV1Taken }

// add records one taken decision.
func (t *adminV1Tally) add(object, action, domain string) {
	for _, existing := range t.entries {
		if existing.Object == object && existing.Action == action && existing.Domain == domain {
			return
		}
	}
	t.entries = append(t.entries, adminV1Taken{Object: object, Action: action, Domain: domain})
}

type adminV1DecisionContextKey struct{}

// WithAdminV1Decision records the primary decision for the request.
func WithAdminV1Decision(ctx context.Context, decision adminV1Decision) context.Context {
	return context.WithValue(ctx, adminV1DecisionContextKey{}, decision)
}

// AdminV1DecisionFrom returns the operation's primary decision, if taken.
func AdminV1DecisionFrom(ctx context.Context) (adminV1Decision, bool) {
	decision, ok := ctx.Value(adminV1DecisionContextKey{}).(adminV1Decision)
	return decision, ok
}

// adminV1DecisionCoversPrimary reports whether (object, action) was decided in any
// domain for this request. It is only for a handler guard that repeats the operation's own
// primary decision, where the mount resolved the domain; every per-resource or secondary
// gate uses the domain-strict check.
func adminV1DecisionCoversPrimary(ctx context.Context, object, action string) bool {
	decision, ok := AdminV1DecisionFrom(ctx)
	if !ok {
		return false
	}
	for _, taken := range decision.Taken {
		if taken.Object == object && taken.Action == action {
			return true
		}
	}
	return false
}

// adminV1DecisionCovers reports whether (object, action) in the given domain was actually
// decided for this request. A handler gate that is not covered must call the Authorizer.
func adminV1DecisionCovers(ctx context.Context, domain, object, action string) bool {
	decision, ok := AdminV1DecisionFrom(ctx)
	if !ok {
		return false
	}
	for _, taken := range decision.Taken {
		if taken.Object == object && taken.Action == action && (domain == "" || taken.Domain == domain) {
			return true
		}
	}
	return false
}

// adminTranslatePreconditionForHandler rewrites a strong v1 validator
// ("rev-42" or "*") into the bare revision today's handlers parse, so the same
// handler body serves both surfaces.
func adminTranslatePreconditionForHandler(r *http.Request, currentRevision int64) {
	if r == nil {
		return
	}
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return
	}
	value := strings.Trim(raw, `"`)
	if revision, ok := strings.CutPrefix(value, adminETagRevisionPrefix); ok {
		r.Header.Set("If-Match", revision)
		return
	}
	if raw == "*" {
		// The contract layer accepted the wildcard; the handler needs a concrete
		// revision, and the wildcard already matched the current one.
		r.Header.Set("If-Match", strconv.FormatInt(currentRevision, 10))
	}
}

// adminOperationPublishesRevisionETag reports whether a GET operation's body is
// validated by the configuration revision, so a conditional request may answer 304.
// Reports, metrics and status change independently of the revision.
func adminOperationPublishesRevisionETag(op Operation) bool {
	if op.Method != "GET" || op.Domain == AdminDomainHandler || op.Pagination != nil {
		return false
	}
	switch op.ID {
	case "listConfigGroups", "getConfigGroup", "getConfigStatus", "listConfigGroupHistory", "getConfigGroupShadow":
		return true
	}
	return false
}

// adminClaimedKind names the credential class a request presents, for the
// failure record only.
func adminClaimedKind(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if strings.HasPrefix(bearerTokenFromRequest(r), adminAPIKeyPrefix+"_") {
		return "adminkey"
	}
	if bearerTokenFromRequest(r) != "" {
		return "bearer"
	}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		return "mtls"
	}
	return "none"
}

// adminClaimedCredential returns the credential identifier a request claims, used
// only as a throttle key. It is never used for a decision.
func adminClaimedCredential(r *http.Request) string {
	if r == nil {
		return ""
	}
	bearer := bearerTokenFromRequest(r)
	if bearer == "" {
		return ""
	}
	if strings.HasPrefix(bearer, adminAPIKeyPrefix+"_") {
		rest := strings.TrimPrefix(bearer, adminAPIKeyPrefix+"_")
		if idx := strings.Index(rest, "_"); idx > 0 {
			return "adminkey:" + rest[:idx]
		}
		return "adminkey"
	}
	return "bearer"
}

func adminSourceAddress(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host
}

// adminIdempotencyKeyFrom validates the Idempotency-Key header.
func adminIdempotencyKeyFrom(r *http.Request) (string, bool) {
	if r == nil {
		return "", true
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", true
	}
	if len(key) > maxAdminIdempotencyKeyBytes || strings.ContainsAny(key, "\r\n\t") {
		return "", false
	}
	return key, true
}

// adminRequestHash digests the request so a replay with a different body is a
// conflict rather than a wrong replay.
func adminRequestHash(r *http.Request) string {
	if r == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdminConfigBodyBytes+1))
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return adminRequestHashFor(r.Method, r.URL.RequestURI(), body)
}

func adminRequestHashFor(method, uri string, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", method, uri)
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// replayAdminRecordedResponse writes a stored response verbatim.
func replayAdminRecordedResponse(w http.ResponseWriter, record adminIdempotencyRecord) {
	if record.ResponseContentType != "" {
		w.Header().Set("Content-Type", record.ResponseContentType)
	}
	if record.ResponseETag != "" {
		w.Header().Set("ETag", record.ResponseETag)
	}
	w.Header().Set("Idempotent-Replay", "true")
	status := record.ResponseStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if record.ResponseBody != "" {
		_, _ = io.WriteString(w, record.ResponseBody)
	}
}

// adminAuthProblem maps #814's typed errors onto problem codes.
func adminAuthProblem(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNoCredential):
		return http.StatusUnauthorized, "authentication_required"
	case errors.Is(err, ErrUnsupportedAuthentication):
		return http.StatusUnauthorized, "unsupported_authentication"
	case errors.Is(err, ErrInvalidCredential):
		return http.StatusUnauthorized, "invalid_credential"
	}
	if coder, ok := err.(interface{ AdminProblemCode() (int, string) }); ok {
		if status, code := coder.AdminProblemCode(); status != 0 && code != "" {
			return status, code
		}
	}
	return http.StatusUnauthorized, "invalid_credential"
}

// adminAuthorizationProblem maps the authorization seam's errors onto problem codes: a
// refusal is 403, an unavailable or stale policy is 503 (never a silent allow).
func adminAuthorizationProblem(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if coder, ok := err.(interface{ AdminProblemCode() (int, string) }); ok {
		if status, code := coder.AdminProblemCode(); status != 0 && code != "" {
			return status, code
		}
	}
	switch {
	case errors.Is(err, ErrAuthorizationUnavailable):
		return http.StatusServiceUnavailable, "authorization-unavailable"
	case errors.Is(err, ErrAdminForbidden):
		return http.StatusForbidden, "admin-forbidden"
	}
	return http.StatusForbidden, "admin-forbidden"
}

// ---------------------------------------------------------------------------
// Response capture
// ---------------------------------------------------------------------------

// adminCaptureWriter buffers a handler response so the contract layer can add
// headers and record the body for idempotency. It never changes the bytes the
// client receives: once the body exceeds the buffer cap it flushes what it has
// and passes the rest straight through.
type adminCaptureWriter struct {
	destination http.ResponseWriter
	header      http.Header
	buffer      bytes.Buffer
	status      int
	written     bool
	flushed     bool
	maxBytes    int
}

func newAdminCaptureWriter(destination http.ResponseWriter, _ http.ResponseWriter, maxBytes int) *adminCaptureWriter {
	if maxBytes <= 0 {
		maxBytes = maxAdminConfigBodyBytes
	}
	// The handler sees the capture writer's header map only once it is written
	// through; seed it with the headers the contract layer already set so
	// handler-set headers win.
	header := http.Header{}
	for name, values := range destination.Header() {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	return &adminCaptureWriter{destination: destination, header: header, maxBytes: maxBytes}
}

func (c *adminCaptureWriter) Header() http.Header { return c.header }

func (c *adminCaptureWriter) WriteHeader(status int) {
	if c.written {
		return
	}
	c.status = status
	c.written = true
}

func (c *adminCaptureWriter) Write(p []byte) (int, error) {
	if !c.written {
		c.WriteHeader(http.StatusOK)
	}
	if c.flushed {
		return c.destination.Write(p)
	}
	if c.buffer.Len()+len(p) > c.maxBytes {
		c.Flush()
		return c.destination.Write(p)
	}
	return c.buffer.Write(p)
}

// Status returns the status the handler set, or 0 when it wrote nothing.
func (c *adminCaptureWriter) Status() int { return c.status }

// Written reports whether the handler produced a response.
func (c *adminCaptureWriter) Written() bool { return c.written }

// BodyString returns the buffered body (empty once flushed).
func (c *adminCaptureWriter) BodyString() string { return c.buffer.String() }

// Overflowed reports that the handler produced more than the capture buffer, so the
// buffered body is not the complete response.
func (c *adminCaptureWriter) Overflowed() bool { return c.flushed }

// ReplaceBody swaps the captured response for a rewritten body, used to render a
// handler's legacy error envelope as a problem document on /admin/v1.
func (c *adminCaptureWriter) ReplaceBody(status int, contentType string, body []byte) {
	c.buffer.Reset()
	_, _ = c.buffer.Write(body)
	c.status = status
	c.written = true
	c.header.Set("Content-Type", contentType)
}

// Flush sends the captured response to the client.
func (c *adminCaptureWriter) Flush() {
	if c.flushed {
		return
	}
	c.flushed = true
	destinationHeader := c.destination.Header()
	for name := range destinationHeader {
		delete(destinationHeader, name)
	}
	for name, values := range c.header {
		for _, value := range values {
			destinationHeader.Add(name, value)
		}
	}
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	c.destination.WriteHeader(status)
	if c.buffer.Len() > 0 {
		_, _ = c.destination.Write(c.buffer.Bytes())
		c.buffer.Reset()
	}
}

// adminSetETagHeader publishes the strong validator on a GET response.
func adminSetETagHeader(w http.ResponseWriter, etag string) {
	if w != nil && etag != "" {
		w.Header().Set("ETag", etag)
	}
}

// adminContentLengthOf is a small helper used by the idempotency tests.
func adminContentLengthOf(body string) string { return strconv.Itoa(len(body)) }

// adminRewriteHandlerProblem renders a handler's legacy error envelope as an RFC 9457
// problem document. The v1 surface has one error format; the legacy surface keeps
// its own. A handler-specific code is registered lazily with the status the handler
// chose, so its meaning stays stable for clients.
func adminRewriteHandlerProblem(w *adminCaptureWriter, r *http.Request, status int) {
	body := w.BodyString()
	code, detail := adminLegacyErrorCode(body)
	if code == "" {
		return
	}
	if _, known := AdminProblemCodes()[code]; !known {
		RegisterAdminProblemCode(code, status, http.StatusText(status))
	}
	w.ReplaceBody(status, "application/problem+json", adminProblemBytes(w, r, status, code, detail))
}

// adminLegacyErrorCode extracts the code and message from the admin handlers' error
// envelopes: {"error":{"type","message"}} or {"error":"code"}.
func adminLegacyErrorCode(body string) (string, string) {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "{") {
		return "", ""
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || len(envelope.Error) == 0 {
		return "", ""
	}
	var typed struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(envelope.Error, &typed); err == nil && typed.Type != "" {
		return truncateSafeDetail(typed.Type), typed.Message
	}
	var plain string
	if err := json.Unmarshal(envelope.Error, &plain); err == nil && plain != "" {
		return truncateSafeDetail(plain), ""
	}
	return "", ""
}

// adminProblemBytes encodes a problem document.
func adminProblemBytes(_ http.ResponseWriter, r *http.Request, status int, code, detail string) []byte {
	problem := AdminProblemFor(r, status, code)
	problem.Detail = truncateSafeDetail(redactDiagnosticSecrets(detail))
	var buffer bytes.Buffer
	_ = json.NewEncoder(&buffer).Encode(problem)
	return buffer.Bytes()
}
