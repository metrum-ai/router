// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"testing"

	"github.com/metrum-ai/router/internal/router"
)

// routerctlOnlineRoutes mirrors every client.Do call in configCommand and
// groupsCommand. Keep this inventory beside the CLI parity test so new online
// actions cannot silently point outside the versioned contract.
var routerctlOnlineRoutes = [][2]string{
	{http.MethodGet, "/admin/config/diff"},
	{http.MethodPost, "/admin/config/sets"},
	{http.MethodGet, "/admin/config/sets/{set_id}"},
	{http.MethodPost, "/admin/config/sets/{set_id}/activate"},
	{http.MethodPost, "/admin/config/rollback"},
	{http.MethodGet, "/admin/config/groups"},
	{http.MethodGet, "/admin/config/groups/{group}"},
	{http.MethodGet, "/admin/config/groups/{group}/history"},
	{http.MethodPut, "/admin/config/groups/{group}/targets"},
	{http.MethodPost, "/admin/config/groups/{group}/targets"},
	{http.MethodDelete, "/admin/config/groups/{group}/targets/{target_id...}"},
	{http.MethodPost, "/admin/config/groups/{group}/rollback"},
	{http.MethodPost, "/admin/config/groups/{group}/shadow"},
	{http.MethodGet, "/admin/config/groups/{group}/shadow"},
	{http.MethodPost, "/admin/config/groups/{group}/shadow/promote"},
	{http.MethodDelete, "/admin/config/groups/{group}/shadow"},
	{http.MethodGet, "/admin/reports/api/cfo"},
	{http.MethodGet, "/admin/reports/cfo/export.csv"},
	{http.MethodGet, "/admin/reports/cfo/export.md"},
}

func TestRouterctlOnlineRouteParity(t *testing.T) {
	operations := router.AdminV1Operations()
	manifest := make(map[[2]string]bool, len(operations))
	for _, operation := range operations {
		if operation.LegacyPath != "" {
			manifest[[2]string{operation.Method, operation.LegacyPath}] = true
		}
	}
	for _, route := range routerctlOnlineRoutes {
		if !manifest[route] {
			t.Errorf("online routerctl route %s %s has no /admin/v1 operation", route[0], route[1])
		}
	}
}
