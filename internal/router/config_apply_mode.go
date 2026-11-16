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
	out := make([]ConfigApplyMode, 0, len(configApplyModeInventory))
	for path, mode := range configApplyModeInventory {
		reason := "Applied when the active canonical configuration reloads."
		if mode == "restart_required" {
			reason = restartReason(path)
		}
		out = append(out, ConfigApplyMode{Path: path, Mode: mode, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func modeForPath(path string) (string, string) {
	mode, ok := configApplyModeInventory[path]
	if !ok {
		return "unknown", "No apply-mode classification is defined for this config path."
	}
	if mode == "restart_required" {
		return mode, restartReason(path)
	}
	return mode, "Applied when the active canonical configuration reloads."
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
