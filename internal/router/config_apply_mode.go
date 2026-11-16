// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"reflect"
	"sort"
	"strings"
)

// ConfigApplyMode describes whether changing a canonical config path takes
// effect through the active-config reload path or requires process restart.
type ConfigApplyMode struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

// ConfigApplyModes inventories every exported YAML-backed Config leaf. Wildcard
// segments represent map keys and repeated list entries.
func ConfigApplyModes() []ConfigApplyMode {
	var paths []string
	collectConfigPaths(reflect.TypeOf(Config{}), "", &paths)
	out := make([]ConfigApplyMode, 0, len(paths))
	for _, path := range paths {
		mode, reason := modeForPath(path)
		if mode == "restart_required" {
			mode, reason = "restart_required", restartReason(path)
		}
		out = append(out, ConfigApplyMode{Path: path, Mode: mode, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func modeForPath(path string) (string, string) {
	var paths []string
	collectConfigPaths(reflect.TypeOf(Config{}), "", &paths)
	known := false
	for _, candidate := range paths {
		if candidate == path {
			known = true
			break
		}
	}
	if !known {
		return "unknown", "No apply-mode classification is defined for this config path."
	}
	if requiresRestart(path) {
		return "restart_required", restartReason(path)
	}
	return "hot_reload", "Applied when the active canonical configuration reloads."
}

func collectConfigPaths(t reflect.Type, prefix string, paths *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		collectConfigPaths(t.Elem(), joinConfigPath(prefix, "*"), paths)
		return
	}
	if t.Kind() == reflect.Map {
		t = t.Elem()
		prefix = joinConfigPath(prefix, "*")
	}
	if t.Kind() != reflect.Struct {
		if prefix != "" {
			*paths = append(*paths, prefix)
		}
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		parts := strings.Split(f.Tag.Get("yaml"), ",")
		name := parts[0]
		inline := false
		for _, option := range parts[1:] {
			if option == "inline" {
				inline = true
				break
			}
		}
		if name == "" && inline {
			collectConfigPaths(f.Type, prefix, paths)
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		collectConfigPaths(f.Type, joinConfigPath(prefix, name), paths)
	}
}

func joinConfigPath(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

func requiresRestart(path string) bool {
	for _, prefix := range []string{"admin.", "server.listen", "server.tls", "server.admin.", "server.admin_auth", "server.usage_db", "server.config_source", "server.client_ip", "server.logging", "server.identifiers", "server.content_capture", "providers.*.api_key_env", "server.admin_api", "state_path", "server.read_timeout_ms", "server.write_timeout_ms", "server.idle_timeout_ms", "server.stream_idle_timeout_ms"} {
		if path == strings.TrimSuffix(prefix, ".") || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func restartReason(path string) string {
	switch {
	case strings.HasPrefix(path, "admin."), strings.HasPrefix(path, "server.listen"), strings.HasPrefix(path, "server.tls"), strings.HasPrefix(path, "server.admin."):
		return "Listener binding and TLS trust are initialized before the serving process starts."
	case strings.HasPrefix(path, "server.usage_db"), strings.HasPrefix(path, "server.config_source"):
		return "Database connections and config ownership are initialized at process startup."
	case strings.HasPrefix(path, "server.admin_auth"), strings.HasPrefix(path, "server.admin_api"), strings.HasPrefix(path, "server.client_ip"):
		return "Authentication, trust, and network boundary settings are initialized at process startup."
	default:
		return "This local runtime dependency is initialized at process startup."
	}
}
