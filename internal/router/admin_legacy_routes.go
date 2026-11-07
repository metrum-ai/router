// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"strings"
)

// legacyMachineAdminPaths reports whether a serving-port path belongs to the
// pre-v4.8 machine admin surface (ACP-009). Only these paths are controlled by
// admin.legacy_routes_on_serving_port: the browser reports UI and the
// /admin/auth/* session routes are not part of the control plane and keep
// working on the serving port with their current login, even with the flag off.
func legacyMachineAdminPath(path string) bool {
	switch {
	case path == "/admin/callers", strings.HasPrefix(path, "/admin/callers/"):
		return true
	case strings.HasPrefix(path, "/admin/config/"), strings.HasPrefix(path, "/admin/training/"):
		return true
	default:
		return false
	}
}

// registerLegacyMachineAdminRoutes mounts the pre-v4.8 machine admin routes on
// the serving port behind admin.legacy_routes_on_serving_port. The handlers,
// registration patterns and decision path are unchanged from v4.7, so
// authentication and authorization decisions stay identical (ACP-025); only the
// deprecation headers are added, by withLegacyAdminDeprecation.
func (s *Service) registerLegacyMachineAdminRoutes() {
	if s.configControlPlane != nil {
		RegisterCallerDirectoryAdmin(s.mux, CallerAdminDeps{
			DB:           s.configControlPlane,
			RuntimeScope: s.cfg.Server.ConfigSource.RuntimeScope,
			Authorize: func(r *http.Request, object, action string) bool {
				w := &discardAdminAuthWriter{header: make(http.Header)}
				subject, ok := s.authenticateAdminSubject(w, r)
				if !ok {
					return false
				}
				return s.authorizeAdminObjectAction(subject, object, action)
			},
			AfterMutate: func(r *http.Request, callerID string) error {
				_ = r
				_ = callerID
				return s.reloadActiveConfigFromDB()
			},
		})
	}
	RegisterConfigAdminAPI(s.mux, ConfigAdminDeps{
		DB:                      s.configControlPlane,
		RuntimeScope:            s.cfg.Server.ConfigSource.RuntimeScope,
		FileMode:                s.configControlPlane == nil,
		RequireValidationStatus: s.cfg.Server.ConfigSource.RequireValidationStatus,
		InstanceID:              s.configInstanceID,
		Authenticate:            s.authenticateAdminSubject,
		Authorize:               s.authorizeAdminPermission,
		CurrentConfig:           func() *Config { return s.configSnapshot.Load() },
		LoadedRevision:          s.loadedConfigRevision,
		Reload:                  s.reloadActiveConfigFromDB,
		ReloadShadows:           s.loadShadowRunsFromDB,
		UsageDB:                 s.usageDBHandle,
		RecordAccess:            s.recordAdminSecurityAccess,
	})
	s.mux.HandleFunc("POST /admin/training/export", s.handleTrainingExport)
	s.mux.HandleFunc("POST /admin/training/outcomes/{request_id}", s.handleTrainingOutcome)
}

// withLegacyAdminDeprecation advertises the deprecation on every legacy
// machine admin response: RFC 9745 Deprecation, RFC 8594 Sunset and a Link to
// the /admin/v1 replacement. It wraps the serving handler, so the headers land
// on success and error responses alike without changing any handler.
func (s *Service) withLegacyAdminDeprecation(next http.Handler) http.Handler {
	sunset := s.cfg.Admin.legacySunsetTime()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if legacyMachineAdminPath(r.URL.Path) {
			w.Header().Set("Deprecation", "true")
			if !sunset.IsZero() {
				w.Header().Set("Sunset", sunset.Format(http.TimeFormat))
			}
			if link := legacyAdminReplacementLink(r.URL.Path); link != "" {
				w.Header().Add("Link", "<"+link+">; rel=\"deprecation\"")
			}
		}
		next.ServeHTTP(w, r)
	})
}

// legacyAdminReplacementLink maps a legacy path to its /admin/v1 equivalent
// using the versioned registry (#811) as the source of truth, so a Link can
// never drift from the published contract. A path the registry does not list
// gets no Link at all.
func legacyAdminReplacementLink(path string) string {
	segments := splitAdminPath(path)
	// Compare the part after the shared "admin" prefix: the versioned form has
	// an extra "v1" marker that the legacy form does not.
	request := trimAdminPrefix(segments, "admin")
	if request == nil {
		return ""
	}
	for _, op := range AdminV1Operations() {
		legacy := trimAdminPrefix(splitAdminPath(op.LegacyPath), "admin")
		versioned := trimAdminPrefix(splitAdminPath(op.Path), "admin", "v1")
		if legacy == nil || versioned == nil || len(legacy) != len(request) || len(versioned) != len(request) {
			continue
		}
		matched := true
		for i := range legacy {
			if strings.HasPrefix(legacy[i], "{") {
				continue
			}
			if legacy[i] != request[i] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		for i := range versioned {
			if strings.HasPrefix(versioned[i], "{") {
				versioned[i] = request[i]
			}
		}
		return "/admin/v1/" + strings.Join(versioned, "/")
	}
	return ""
}

// trimAdminPrefix removes the leading segments from path and returns the rest,
// or nil when the prefix does not match.
func trimAdminPrefix(path []string, prefix ...string) []string {
	if len(path) < len(prefix) {
		return nil
	}
	for i, want := range prefix {
		if path[i] != want {
			return nil
		}
	}
	return path[len(prefix):]
}

func splitAdminPath(path string) []string {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
