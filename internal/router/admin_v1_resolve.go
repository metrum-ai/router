// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Domain resolution and body peeking for the /admin/v1 mount (ACP-011/015).
//
// The authorization domain is an explicit request target. For a caller-directory
// resource it comes from the stored caller row (or, for an issue, from the
// requested project/environment); for content-capture maintenance it comes from
// the captured row. It is never taken from a header.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"gorm.io/gorm"
)

var (
	errAdminV1NoControlPlane = errors.New("config control-plane database is required")
	errAdminV1NoUsageStore   = errors.New("usage store is required")
)

// AdminV1CallerResourceDomain resolves the domain of a caller-directory resource:
// the stored caller row for rotate/revoke, and the requested project/environment
// for an issue. It returns "" when the operation addresses the whole directory.
func AdminV1CallerResourceDomain(db *gorm.DB, runtimeScope string, r *http.Request) (string, error) {
	if db == nil || r == nil {
		return "", errAdminV1NoControlPlane
	}
	scope := strings.TrimSpace(runtimeScope)
	var sets []configSetRow
	if err := db.Where("runtime_scope = ? AND status = ?", scope, "active").Limit(1).Find(&sets).Error; err != nil {
		return "", err
	}
	if len(sets) == 0 {
		return "", errors.New("no active configuration set")
	}
	setID := sets[0].ID
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issue"):
		body := struct {
			Project     string `json:"project"`
			Environment string `json:"environment"`
		}{}
		if err := peekAdminJSONBody(r, &body); err != nil {
			return "", err
		}
		return authzDomainForProjectEnvironment(body.Project, body.Environment), nil
	case r.Method == http.MethodPost:
		callerID := strings.TrimSpace(r.PathValue("id"))
		if callerID == "" {
			return "", errors.New("caller id is required")
		}
		var rows []callerRow
		if err := db.Where("config_set_id = ? AND caller_id = ?", setID, callerID).Limit(1).Find(&rows).Error; err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "", nil
		}
		return authzDomainForProjectEnvironment(rows[0].Project, rows[0].Environment), nil
	}
	return "", nil
}

// AdminV1CapturedRowDomains wraps a captured-row domain lookup for the mount.
func AdminV1CapturedRowDomains(lookup func(requestID string) ([]string, error), r *http.Request) ([]string, error) {
	if lookup == nil || r == nil {
		return nil, errAdminV1NoUsageStore
	}
	return lookup(strings.TrimSpace(r.PathValue("request_id")))
}

// peekAdminJSONBody decodes a bounded copy of the request body and restores it, so
// the handler downstream still sees the original bytes.
func peekAdminJSONBody(r *http.Request, out any) error {
	if r == nil || r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAdminConfigBodyBytes+1))
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxAdminConfigBodyBytes {
		return errors.New("request body is too large")
	}
	return json.Unmarshal(raw, out)
}
