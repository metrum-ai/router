// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
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
		fs.Parse(args[1:])
		if strings.TrimSpace(*label) == "" || strings.TrimSpace(*tokenOut) == "" {
			die("honeytoken issue requires --label and --token-out")
		}
		groups := splitCSV(*canaryGroups)
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
		if err := writeNewPrivateFile(*tokenOut, []byte(generated.Token+"\n")); err != nil {
			die("write honeytoken: %v", err)
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
		out := map[string]any{
			"schema":            "metrum.ai/router-honeytoken/v1",
			"caller_id":         generated.Caller.ID,
			"label":             *label,
			"canary_groups":     groups,
			"token_file":        filepath.Base(*tokenOut),
			"activation":        "configuration-controller-required",
			"token_hash_format": tokenHashFormat(generated),
		}
		if backup != "" {
			out["config_backup"] = filepath.Base(backup)
			out["activation"] = "local-config-written-restart-required"
		}
		writeJSON(out)
	case "report":
		fs := flag.NewFlagSet("honeytoken report", flag.ExitOnError)
		configPath := fs.String("config", "", "router configuration path")
		fs.Parse(args[1:])
		if strings.TrimSpace(*configPath) == "" {
			die("honeytoken report requires --config")
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
