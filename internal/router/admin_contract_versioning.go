// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Versioning and deprecation policy for the admin control plane (ACP-N05).
//
// A deprecated admin operation keeps working while advertising its removal:
//
//	Deprecation: @<unix-seconds>                       (RFC 9745 structured field)
//	Sunset: <HTTP-date>                                (RFC 8594)
//	Link: <doc URL>; rel="deprecation"; type="text/html"
//
// `Deprecation` and `Sunset` are additive headers: they never change a status
// code or a body, so a deprecated operation stays byte-compatible with its
// undeprecated behaviour.
//
// Removal policy, enforced by a test over the registry: the sunset date must be
// at least one minor release AND 90 days after the deprecation date. The legacy
// `/admin/*` surface on the serving port uses the same helper through #809, so
// one policy governs the whole plane.

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// AdminDeprecationMinimumNotice is the minimum notice before removal: 90 days,
// per ACP-N05 and the epic decision.
const AdminDeprecationMinimumNotice = 90 * 24 * time.Hour

// AdminDeprecation describes one deprecation window.
type AdminDeprecation struct {
	// Since is the deprecation date (the value of the Deprecation header).
	Since time.Time
	// Sunset is the removal date (the value of the Sunset header).
	Sunset time.Time
	// Link is the documentation URL for the replacement; optional.
	Link string
	// Note is a short human-readable replacement hint kept in the guide and the
	// OpenAPI description, never in the header.
	Note string
}

// LoadAdminDeprecationHeader emits the RFC 9745 Deprecation header value.
func (d AdminDeprecation) DeprecationHeader() string {
	if d.Since.IsZero() {
		return ""
	}
	return "@" + strconv.FormatInt(d.Since.UTC().Unix(), 10)
}

// SunsetHeader emits the RFC 8594 Sunset header value.
func (d AdminDeprecation) SunsetHeader() string {
	if d.Sunset.IsZero() {
		return ""
	}
	return d.Sunset.UTC().Format(http.TimeFormat)
}

// Validate reports a policy violation: a sunset closer than the minimum notice,
// or a sunset before the deprecation date.
func (d AdminDeprecation) Validate() error {
	if d.Since.IsZero() {
		return fmt.Errorf("deprecation has no since date")
	}
	if d.Sunset.IsZero() {
		return fmt.Errorf("deprecation has no sunset date")
	}
	if d.Sunset.Before(d.Since) {
		return fmt.Errorf("sunset %s is before deprecation %s", d.Sunset.Format(time.RFC3339), d.Since.Format(time.RFC3339))
	}
	if d.Sunset.Sub(d.Since) < AdminDeprecationMinimumNotice {
		return fmt.Errorf("sunset %s is less than %s after deprecation %s", d.Sunset.Format(time.RFC3339), AdminDeprecationMinimumNotice, d.Since.Format(time.RFC3339))
	}
	return nil
}

// ApplyAdminDeprecationHeaders stamps the deprecation headers on w.
func ApplyAdminDeprecationHeaders(w http.ResponseWriter, d AdminDeprecation) {
	if w == nil || d.Since.IsZero() {
		return
	}
	header := w.Header()
	if value := d.DeprecationHeader(); value != "" {
		header.Set("Deprecation", value)
	}
	if value := d.SunsetHeader(); value != "" {
		header.Set("Sunset", value)
	}
	if d.Link != "" {
		header.Set("Link", "<"+d.Link+`>; rel="deprecation"; type="text/html"`)
	}
}

// adminDeprecationHeaders is the nil-safe form used by the contract layer.
func adminDeprecationHeaders(w http.ResponseWriter, deprecation *AdminDeprecation) {
	if deprecation == nil {
		return
	}
	ApplyAdminDeprecationHeaders(w, *deprecation)
}
