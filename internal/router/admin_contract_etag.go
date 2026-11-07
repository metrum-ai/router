// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Strong ETags and preconditions for the /admin/v1 contract (ACP-015).
//
// Two validator shapes exist, both strong:
//
//	"rev-<config_revision>"          a resource whose state is the configuration
//	                                 revision of its runtime scope
//	"set-<hex sha256[:16]>"          a configuration set, keyed by the hash of its
//	                                 canonical document so exporting an old set is
//	                                 not invalidated by unrelated revisions
//
// Legacy /admin/* routes keep today's semantics (optional If-Match, bare
// revision, 409 on a stale precondition). Only the /admin/v1 surface makes the
// precondition mandatory (428) and answers 412 when it is stale.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

const (
	adminETagRevisionPrefix = "rev-"
	adminETagSetPrefix      = "set-"
)

// AdminETagForRevision is the strong ETag of a revision-scoped resource.
func AdminETagForRevision(revision int64) string {
	return `"` + adminETagRevisionPrefix + strconv.FormatInt(revision, 10) + `"`
}

// AdminETagForSet is the strong ETag of one configuration set representation.
func AdminETagForSet(canonicalYAML []byte) string {
	sum := sha256.Sum256(canonicalYAML)
	return `"` + adminETagSetPrefix + hex.EncodeToString(sum[:8]) + `"`
}

// AdminETagForBody is the strong ETag of a complete response representation.
// It is used for collection responses that were returned in full; a paginated
// response gets a Link cursor instead, never both.
func AdminETagForBody(body []byte) string {
	sum := sha256.Sum256(body)
	return `"sha256-` + hex.EncodeToString(sum[:16]) + `"`
}

type adminETagParseResult struct {
	// values holds every entity tag from the header, verbatim.
	values []string
	// any is set when the header carried the "*" wildcard.
	any bool
	// weak is set when any listed tag was weak; If-Match must not match a
	// weak validator (RFC 9110 §13.1.1).
	weak bool
}

func parseAdminETagHeader(raw string) (adminETagParseResult, bool) {
	var parsed adminETagParseResult
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsed, false
	}
	if raw == "*" {
		parsed.any = true
		return parsed, true
	}
	for _, part := range strings.Split(raw, ",") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			return adminETagParseResult{}, false
		}
		if len(tag) >= 2 && (tag[0] == 'W' || tag[0] == 'w') && tag[1] == '/' {
			parsed.weak = true
			tag = strings.TrimSpace(tag[2:])
		}
		if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
			// Tolerate a bare revision ("42") on the v1 surface: it is
			// unambiguous and keeps scripts written against the legacy
			// revision precondition working.
			if _, err := strconv.ParseInt(tag, 10, 64); err != nil {
				return adminETagParseResult{}, false
			}
			tag = `"` + adminETagRevisionPrefix + tag + `"`
		}
		parsed.values = append(parsed.values, tag)
	}
	return parsed, true
}

// adminIfMatchOutcome is what the contract layer does with If-Match.
type adminIfMatchOutcome struct {
	// Present reports whether the request carried If-Match.
	Present bool
	// Matched reports whether the current validator was matched.
	Matched bool
	// Invalid reports a malformed or weak precondition (400).
	Invalid bool
}

// checkAdminIfMatch evaluates If-Match against the resource's current strong
// ETag. current == "" means the resource has no representation (404 is the
// caller's business, not the precondition's).
func checkAdminIfMatch(r *http.Request, current string) adminIfMatchOutcome {
	if r == nil {
		return adminIfMatchOutcome{}
	}
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return adminIfMatchOutcome{}
	}
	parsed, ok := parseAdminETagHeader(raw)
	if !ok || parsed.weak {
		return adminIfMatchOutcome{Present: true, Invalid: true}
	}
	if parsed.any {
		return adminIfMatchOutcome{Present: true, Matched: current != ""}
	}
	for _, tag := range parsed.values {
		if tag == current {
			return adminIfMatchOutcome{Present: true, Matched: true}
		}
	}
	return adminIfMatchOutcome{Present: true}
}

// adminIfNoneMatchHit reports whether a conditional GET should answer 304.
func adminIfNoneMatchHit(r *http.Request, current string) bool {
	if r == nil || current == "" {
		return false
	}
	raw := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if raw == "" {
		return false
	}
	parsed, ok := parseAdminETagHeader(raw)
	if !ok {
		return false
	}
	if parsed.any {
		return true
	}
	for _, tag := range parsed.values {
		if tag == current {
			return true
		}
	}
	return false
}

// adminWriteNotModified answers a conditional GET that matched.
func adminWriteNotModified(w http.ResponseWriter, etag string) {
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotModified)
}
