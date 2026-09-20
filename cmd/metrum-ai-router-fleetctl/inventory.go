// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"github.com/metrum-ai/router/internal/fleet"
	"strings"
)

func fleetTenants(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-router-fleetctl tenants <list|get|sync> [flags]")
	}
	switch args[0] {
	case "list":
		fleetTenantsList(args[1:])
	case "get":
		fleetTenantsGet(args[1:])
	case "sync":
		fleetTenantsSync(args[1:])
	default:
		die("unsupported tenants command %q", args[0])
	}
}

func fleetTenantsList(args []string) {
	fs := flag.NewFlagSet("tenants list", flag.ExitOnError)
	registry := fs.String("registry", defaultRegistryPath(), "private local Fleet lifecycle SQLite path")
	output := fs.String("output", "json", "safe output format (json)")
	fs.Parse(args)
	requireJSONOutput(*output)
	store, err := fleet.OpenTenantDeploymentStoreReadOnly(*registry)
	if err != nil {
		die("open deployment registry: %v", err)
	}
	defer store.Close()
	tenants, err := store.ListFleetTenants(context.Background())
	if err != nil {
		die("list fleet tenants: %v", err)
	}
	writeJSON(map[string]any{
		"schema":  "metrum.ai/smartrouter-fleet-tenant-list/v1",
		"tenants": tenants,
	})
}

func fleetTenantsGet(args []string) {
	fs := flag.NewFlagSet("tenants get", flag.ExitOnError)
	customerID := fs.String("customer-id", "", "exact customer_id")
	registry := fs.String("registry", defaultRegistryPath(), "private local Fleet lifecycle SQLite path")
	output := fs.String("output", "json", "safe output format (json)")
	fs.Parse(args)
	requireJSONOutput(*output)
	if strings.TrimSpace(*customerID) == "" {
		die("customer-id is required")
	}
	store, err := fleet.OpenTenantDeploymentStoreReadOnly(*registry)
	if err != nil {
		die("open deployment registry: %v", err)
	}
	defer store.Close()
	tenant, err := store.GetFleetTenant(context.Background(), *customerID)
	if err != nil {
		die("get fleet tenant: %v", err)
	}
	writeJSON(tenant)
}

func fleetTenantsSync(args []string) {
	fs := flag.NewFlagSet("tenants sync", flag.ExitOnError)
	registry := fs.String("registry", defaultRegistryPath(), "private local Fleet lifecycle SQLite path")
	output := fs.String("output", "json", "safe output format (json)")
	fs.Parse(args)
	requireJSONOutput(*output)
	store, err := fleet.OpenTenantDeploymentStore(*registry)
	if err != nil {
		die("open deployment registry: %v", err)
	}
	defer store.Close()
	count, err := store.SyncFleetInventoryFromJobs(context.Background())
	if err != nil {
		die("sync fleet inventory: %v", err)
	}
	tenants, err := store.ListFleetTenants(context.Background())
	if err != nil {
		die("list fleet tenants: %v", err)
	}
	writeJSON(map[string]any{
		"schema":      "metrum.ai/smartrouter-fleet-tenant-sync/v1",
		"jobs_synced": count,
		"tenants":     tenants,
	})
}
