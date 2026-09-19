// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// metrum-ai-router-fleetctl is the sole #555 fleet lifecycle operator.
// It uses typed AWS and Kubernetes clients and intentionally has no
// command-shell fallback.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/metrum-ai/router/internal/buildinfo"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.Text())
		return
	}
	if len(os.Args) < 2 {
		die("usage: metrum-ai-router-fleetctl <plan|deploy|status|delete|tenants|databases|smoke|customer> [flags]; plan/deploy/delete require --intent PATH")
	}
	switch os.Args[1] {
	case "status":
		deploymentStatus(os.Args[2:])
	case "plan":
		deploymentPlan(os.Args[2:])
	case "deploy":
		deploymentDeploy(os.Args[2:])
	case "delete":
		deploymentDelete(os.Args[2:])
	case "tenants":
		fleetTenants(os.Args[2:])
	case "databases":
		fleetDatabases(os.Args[2:])
	case "smoke":
		fleetSmoke(os.Args[2:])
	case "customer":
		fleetCustomer(os.Args[2:])
	default:
		die("unsupported command %q", os.Args[1])
	}
}

func writeJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		die("encode safe output: %v", err)
	}
	fmt.Println(string(b))
}
func die(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(2) }
