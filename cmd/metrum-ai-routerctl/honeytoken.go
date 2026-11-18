// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/metrum-ai/router/internal/router"
	"github.com/metrum-ai/router/internal/smartrouterctl"
)

func honeytokenCommand(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-routerctl honeytoken <issue|report> [flags]")
	}
	switch args[0] {
	case "issue":
		fs := flag.NewFlagSet("honeytoken issue", flag.ExitOnError)
		label := fs.String("label", "", "where the decoy key will be planted")
		owner := fs.String("owner-user", "", "caller owner user id")
		project := fs.String("project", "", "caller project name")
		environment := fs.String("env", "dev", "caller environment")
		key := fs.String("key", "honeytoken", "visible key slug")
		canaryGroups := fs.String("canary-groups", "", "comma-separated canary model groups to allow")
		tokenOut := fs.String("token-out", "", "new mode-0600 token file; must not already exist")
		configPath := fs.String("config", "", "router configuration path")
		writeConfig := fs.Bool("write", false, "merge the honeytoken caller and canary visibility into config")
		routerURL := fs.String("router-url", "", "database-backed router admin API URL (or ROUTER_URL)")
		adminKeyFile := fs.String("admin-key-file", "", "mode-0600 admin key file (or ROUTER_ADMIN_KEY)")
		ifMatch := fs.String("if-match", "", "If-Match runtime revision precondition")
		idempotencyKey := fs.String("idempotency-key", "", "idempotency key (generated if omitted)")
		fs.Parse(args[1:])
		if strings.TrimSpace(*label) == "" || strings.TrimSpace(*tokenOut) == "" {
			die("honeytoken issue requires --label and --token-out")
		}
		groups := splitCSV(*canaryGroups)
		api := &adminAPIFlags{routerURL: routerURL, adminKeyFile: adminKeyFile, ifMatch: ifMatch, idempotencyKey: idempotencyKey}
		if strings.TrimSpace(*routerURL) != "" || strings.TrimSpace(os.Getenv("ROUTER_URL")) != "" {
			if *writeConfig || strings.TrimSpace(*configPath) != "" {
				die("database-backed honeytoken issuance cannot be combined with --config or --write")
			}
			client := mustAdminAPIClient(api)
			body, _, err := client.Do(http.MethodPost, "/admin/v1/callers/issue", map[string]any{
				"owner_user": *owner, "project": *project, "environment": *environment, "key_slug": *key,
				"allow": groups, "honeytoken_label": *label,
			}, api.revisionMutationHeaders())
			if err != nil {
				dieAdmin(err)
			}
			var issued struct {
				Caller  map[string]any `json:"caller"`
				Token   string         `json:"token"`
				TokenID string         `json:"token_id"`
			}
			if err := json.Unmarshal(body, &issued); err != nil || !strings.HasPrefix(issued.Token, "rtr_metrum_") {
				die("caller issue API returned an invalid honeytoken response")
			}
			if err := writeNewPrivateFile(*tokenOut, []byte(issued.Token+"\n")); err != nil {
				die("write honeytoken: %v", err)
			}
			writeJSON(map[string]any{"schema": "metrum.ai/router-honeytoken/v1", "caller_id": issued.Caller["id"], "caller_record": issued.Caller, "label": *label, "token_id": issued.TokenID, "token_file": filepath.Base(*tokenOut), "activation": "database-config-committed"})
			return
		}
		var cfg *router.Config
		if *writeConfig {
			if strings.TrimSpace(*configPath) == "" {
				die("--write requires --config")
			}
			cfg = loadConfigRaw(*configPath)
		}
		generated, err := router.GenerateCallerToken(router.TokenGenerateOptions{
			OwnerUser: *owner, Project: *project, Environment: *environment, KeySlug: *key,
			Allow: groups, HoneytokenLabel: *label,
		})
		if err != nil {
			die("generate honeytoken: %v", err)
		}
		if *writeConfig {
			for _, existing := range cfg.Callers {
				if existing.ID == generated.Caller.ID {
					die("caller %q already exists in %s; use a distinct owner, project, or environment", generated.Caller.ID, *configPath)
				}
			}
			for _, groupName := range groups {
				group, ok := cfg.Models[groupName]
				if !ok || group.Canary == nil {
					die("model group %q is not configured as a canary", groupName)
				}
				group.Canary.Callers = append(group.Canary.Callers, generated.Caller.ID)
				cfg.Models[groupName] = group
			}
		}
		backup := ""
		if *writeConfig {
			if err := smartrouterctl.MergeCaller(cfg, generated.Caller); err != nil {
				die("merge honeytoken caller: %v", err)
			}
			backup, err = smartrouterctl.WriteConfigAtomic(*configPath, cfg)
			if err != nil {
				die("write config: %v", err)
			}
		}
		if err := writeNewPrivateFile(*tokenOut, []byte(generated.Token+"\n")); err != nil {
			die("write honeytoken: %v", err)
		}
		out := map[string]any{
			"schema":            "metrum.ai/router-honeytoken/v1",
			"caller_id":         generated.Caller.ID,
			"label":             *label,
			"canary_groups":     groups,
			"token_file":        filepath.Base(*tokenOut),
			"activation":        "configuration-controller-required",
			"token_hash_format": tokenHashFormat(),
			"caller_record":     generated.Caller,
		}
		if backup != "" {
			out["config_backup"] = filepath.Base(backup)
			out["activation"] = "local-config-written-restart-required"
		}
		writeJSON(out)
	case "report":
		fs := flag.NewFlagSet("honeytoken report", flag.ExitOnError)
		configPath := fs.String("config", "", "router configuration path")
		routerURL := fs.String("router-url", "", "database-backed router admin API URL (or ROUTER_URL)")
		adminKeyFile := fs.String("admin-key-file", "", "mode-0600 admin key file (or ROUTER_ADMIN_KEY)")
		ifMatch := fs.String("if-match", "", "If-Match runtime revision precondition")
		idempotencyKey := fs.String("idempotency-key", "", "idempotency key (generated if omitted)")
		fs.Parse(args[1:])
		api := &adminAPIFlags{routerURL: routerURL, adminKeyFile: adminKeyFile, ifMatch: ifMatch, idempotencyKey: idempotencyKey}
		if strings.TrimSpace(*routerURL) != "" || strings.TrimSpace(os.Getenv("ROUTER_URL")) != "" {
			client := mustAdminAPIClient(api)
			body, _, err := client.Do(http.MethodGet, "/admin/v1/callers", nil, nil)
			if err != nil {
				dieAdmin(err)
			}
			var response struct {
				Callers []struct {
					ID        string `json:"id"`
					Label     string `json:"honeytoken_label"`
					PlantedAt string `json:"honeytoken_planted_at"`
					LastSeen  string `json:"honeytoken_last_seen"`
				} `json:"callers"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				die("decode database honeytoken report: %v", err)
			}
			rows := make([]router.HoneytokenStatus, 0)
			for _, caller := range response.Callers {
				if caller.Label == "" {
					continue
				}
				rows = append(rows, router.HoneytokenStatus{CallerID: caller.ID, Label: caller.Label, PlantedAt: caller.PlantedAt, LastSeen: caller.LastSeen})
			}
			writeJSON(map[string]any{"schema": "metrum.ai/router-honeytoken-report/v1", "data": rows})
			return
		}
		if strings.TrimSpace(*configPath) == "" {
			die("honeytoken report requires --config or --router-url")
		}
		cfg := loadConfigRaw(*configPath)
		rows, err := router.HoneytokenReport(cfg)
		if err != nil {
			die("honeytoken report: %v", err)
		}
		writeJSON(map[string]any{"schema": "metrum.ai/router-honeytoken-report/v1", "data": rows})
	default:
		die("unsupported honeytoken command %q", args[0])
	}
}
