// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// metrum-ai-routerctl is the customer-local Router operations CLI. It may write
// local config.yaml and SQLite usage backups on file-owned installs. It has no
// cloud, Kubernetes API, or remote activation authority.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
	"github.com/metrum-ai/router/internal/router"
	"github.com/metrum-ai/router/internal/smartrouterctl"
	"gorm.io/gorm"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.Text())
		return
	}
	if len(os.Args) < 2 {
		die("usage: metrum-ai-routerctl <version|config|callers|providers|models|status|usage|blueprint> [flags]")
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(buildinfo.Text())
	case "config":
		configCommand(os.Args[2:])
	case "callers":
		callersCommand(os.Args[2:])
	case "providers":
		providersCommand(os.Args[2:])
	case "models":
		modelsCommand(os.Args[2:])
	case "status":
		statusCommand(os.Args[2:])
	case "usage":
		usageCommand(os.Args[2:])
	case "blueprint":
		blueprintCommand(os.Args[2:])
	default:
		die("unsupported command %q", os.Args[1])
	}
}

func configCommand(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-routerctl config <validate|diff|import|export|activate|rollback> [flags]")
	}
	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("config validate", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		fs.Parse(args[1:])
		cfg := loadConfig(*path)
		writeJSON(configSummary(cfg))
	case "diff":
		fs := flag.NewFlagSet("config diff", flag.ExitOnError)
		from := fs.String("from", "", "existing router configuration path")
		to := fs.String("to", "", "candidate router configuration path")
		fs.Parse(args[1:])
		before, after := loadConfig(*from), loadConfig(*to)
		writeJSON(configDiff(before, after))
	case "import":
		fs := flag.NewFlagSet("config import", flag.ExitOnError)
		yamlPath := fs.String("yaml", "", "canonical YAML path to import")
		name := fs.String("name", "", "config set name")
		opts := addConfigDBFlags(fs)
		fs.Parse(args[1:])
		if strings.TrimSpace(*yamlPath) == "" || strings.TrimSpace(*name) == "" {
			die("config import requires --yaml and --name")
		}
		raw, err := os.ReadFile(*yamlPath)
		if err != nil {
			die("%v", err)
		}
		db, closer, err := openConfigDB(*opts)
		if err != nil {
			die("%v", err)
		}
		defer closer()
		setID, err := router.ImportCanonicalYAML(db, opts.RuntimeScope, *name, raw, opts.Actor)
		if err != nil {
			die("%v", err)
		}
		writeJSON(map[string]any{"set_id": setID, "runtime_scope": opts.RuntimeScope, "name": *name})
	case "export":
		fs := flag.NewFlagSet("config export", flag.ExitOnError)
		setID := fs.String("set-id", "", "config set id (empty = active)")
		outPath := fs.String("out", "", "output path (default: stdout)")
		opts := addConfigDBFlags(fs)
		fs.Parse(args[1:])
		db, closer, err := openConfigDB(*opts)
		if err != nil {
			die("%v", err)
		}
		defer closer()
		var raw []byte
		if strings.TrimSpace(*setID) == "" {
			raw, err = router.LoadActiveCanonicalYAML(db, opts.RuntimeScope)
		} else {
			raw, err = router.ExportCanonicalYAML(db, *setID)
		}
		if err != nil {
			die("%v", err)
		}
		if strings.TrimSpace(*outPath) == "" {
			_, _ = os.Stdout.Write(raw)
			return
		}
		if err := os.WriteFile(*outPath, raw, 0o600); err != nil {
			die("%v", err)
		}
	case "activate":
		fs := flag.NewFlagSet("config activate", flag.ExitOnError)
		setID := fs.String("set-id", "", "config set id to activate")
		opts := addConfigDBFlags(fs)
		fs.Parse(args[1:])
		if strings.TrimSpace(*setID) == "" {
			die("config activate requires --set-id")
		}
		db, closer, err := openConfigDB(*opts)
		if err != nil {
			die("%v", err)
		}
		defer closer()
		if err := router.ActivateConfigSet(db, opts.RuntimeScope, *setID, opts.Actor); err != nil {
			die("%v", err)
		}
		writeJSON(map[string]any{"set_id": *setID, "runtime_scope": opts.RuntimeScope, "status": "active"})
	case "rollback":
		fs := flag.NewFlagSet("config rollback", flag.ExitOnError)
		setID := fs.String("set-id", "", "prior config set id to reactivate")
		opts := addConfigDBFlags(fs)
		fs.Parse(args[1:])
		if strings.TrimSpace(*setID) == "" {
			die("config rollback requires --set-id")
		}
		db, closer, err := openConfigDB(*opts)
		if err != nil {
			die("%v", err)
		}
		defer closer()
		if err := router.RollbackConfigSet(db, opts.RuntimeScope, *setID, opts.Actor); err != nil {
			die("%v", err)
		}
		writeJSON(map[string]any{"set_id": *setID, "runtime_scope": opts.RuntimeScope, "status": "active", "action": "rollback"})
	default:
		die("unsupported config command %q", args[0])
	}
}

func addConfigDBFlags(fs *flag.FlagSet) *smartrouterctl.ConfigDBOptions {
	opts := &smartrouterctl.ConfigDBOptions{}
	fs.StringVar(&opts.Driver, "driver", "sqlite", "config control-plane driver")
	fs.StringVar(&opts.Path, "db", "", "sqlite config control-plane path")
	fs.StringVar(&opts.DSN, "dsn", "", "postgres config control-plane dsn")
	fs.StringVar(&opts.RuntimeScope, "scope", "prod", "runtime scope")
	fs.StringVar(&opts.Actor, "actor", "operator", "audit actor")
	return opts
}

func openConfigDB(opts smartrouterctl.ConfigDBOptions) (*gorm.DB, func() error, error) {
	if err := opts.Validate(); err != nil {
		return nil, nil, err
	}
	return router.OpenConfigControlPlaneDB(opts.UsageDBConfig())
}

func callersCommand(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-routerctl callers <list|generate|revoke|rotate|hash|verify> [flags]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("callers list", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		fs.Parse(args[1:])
		cfg := loadConfigRaw(*path)
		rows := make([]map[string]any, 0, len(cfg.Callers))
		for _, caller := range cfg.Callers {
			rows = append(rows, map[string]any{
				"id": caller.ID, "status": caller.Status, "allow": caller.Allow,
				"owner_user": caller.OwnerUser, "project": caller.Project, "environment": caller.Environment,
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["id"].(string) < rows[j]["id"].(string) })
		writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-caller-list/v1", "data": rows})
	case "generate":
		fs := flag.NewFlagSet("callers generate", flag.ExitOnError)
		owner := fs.String("owner-user", "", "caller owner user id")
		project := fs.String("project", "", "caller project name")
		environment := fs.String("env", "dev", "caller environment")
		key := fs.String("key", "", "visible key slug")
		allow := fs.String("allow", "", "comma-separated allowed model groups")
		tokenOut := fs.String("token-out", "", "new mode-0600 token file; must not already exist")
		configPath := fs.String("config", "", "optional config to merge hashed caller into")
		writeCfg := fs.Bool("write", false, "merge hashed caller into --config")
		hashFormat, hashIterations := addTokenHashFlags(fs)
		fs.Parse(args[1:])
		if strings.TrimSpace(*tokenOut) == "" {
			die("token-out is required")
		}
		generated, err := router.GenerateCallerToken(router.TokenGenerateOptions{
			OwnerUser: *owner, Project: *project, Environment: *environment, KeySlug: *key, Allow: splitCSV(*allow),
			HashFormat: *hashFormat, HashIterations: *hashIterations,
		})
		if err != nil {
			die("generate caller token: %v", err)
		}
		if err := writeNewPrivateFile(*tokenOut, []byte(generated.Token+"\n")); err != nil {
			die("write token: %v", err)
		}
		activation := "configuration-controller-required"
		backup := ""
		if *writeCfg {
			if strings.TrimSpace(*configPath) == "" {
				die("--write requires --config")
			}
			cfg := loadConfigRaw(*configPath)
			smartrouterctl.EnsureAccountDirectory(cfg, generated.Caller.OwnerUser, generated.Caller.Project)
			if err := smartrouterctl.MergeCaller(cfg, generated.Caller); err != nil {
				die("merge caller: %v", err)
			}
			backup, err = smartrouterctl.WriteConfigAtomic(*configPath, cfg)
			if err != nil {
				die("write config: %v", err)
			}
			activation = "local-config-written-restart-required"
		}
		out := map[string]any{
			"schema": "metrum.ai/smartrouter-caller-grant/v1", "caller_id": generated.Caller.ID,
			"token_id": generated.TokenID, "allowed_model_groups": generated.Caller.Allow,
			"token_file": filepath.Base(*tokenOut), "activation": activation,
			"token_hash_format": tokenHashFormat(generated),
		}
		if backup != "" {
			out["config_backup"] = filepath.Base(backup)
		}
		writeJSON(out)
	case "revoke":
		fs := flag.NewFlagSet("callers revoke", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		id := fs.String("id", "", "caller id")
		fs.Parse(args[1:])
		cfg := loadConfigRaw(*path)
		if err := smartrouterctl.RevokeCaller(cfg, *id); err != nil {
			die("%v", err)
		}
		backup, err := smartrouterctl.WriteConfigAtomic(*path, cfg)
		if err != nil {
			die("write config: %v", err)
		}
		writeJSON(map[string]any{
			"schema": "metrum.ai/smartrouter-caller-revoke/v1", "caller_id": *id, "status": "disabled",
			"config_backup": filepath.Base(backup), "activation": "local-config-written-restart-required",
		})
	case "rotate":
		fs := flag.NewFlagSet("callers rotate", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		id := fs.String("id", "", "caller id")
		tokenOut := fs.String("token-out", "", "new mode-0600 token file")
		hashFormat, hashIterations := addTokenHashFlags(fs)
		fs.Parse(args[1:])
		if strings.TrimSpace(*tokenOut) == "" {
			die("token-out is required")
		}
		cfg := loadConfigRaw(*path)
		var existing *router.CallerConfig
		for i := range cfg.Callers {
			if cfg.Callers[i].ID == *id {
				existing = &cfg.Callers[i]
				break
			}
		}
		if existing == nil {
			die("caller %q not found", *id)
		}
		generated, err := router.GenerateCallerToken(router.TokenGenerateOptions{
			OwnerUser: existing.OwnerUser, Project: existing.Project, Environment: existing.Environment,
			KeySlug: "rot" + time.Now().UTC().Format("150405"), Allow: append([]string(nil), existing.Allow...),
			HashFormat: *hashFormat, HashIterations: *hashIterations,
		})
		if err != nil {
			die("generate caller token: %v", err)
		}
		if err := writeNewPrivateFile(*tokenOut, []byte(generated.Token+"\n")); err != nil {
			die("write token: %v", err)
		}
		if generated.TokenHash != "" {
			err = smartrouterctl.RotateCallerTokenHash(cfg, *id, generated.TokenHash, generated.TokenID)
		} else {
			err = smartrouterctl.RotateCallerHash(cfg, *id, generated.TokenSHA256, generated.TokenID)
		}
		if err != nil {
			die("%v", err)
		}
		backup, err := smartrouterctl.WriteConfigAtomic(*path, cfg)
		if err != nil {
			die("write config: %v", err)
		}
		writeJSON(map[string]any{
			"schema": "metrum.ai/smartrouter-caller-rotate/v1", "caller_id": *id, "token_id": generated.TokenID,
			"token_file": filepath.Base(*tokenOut), "config_backup": filepath.Base(backup),
			"activation": "local-config-written-restart-required", "token_hash_format": tokenHashFormat(generated),
		})
	case "hash":
		// Converts an existing token (for example one whose config entry still
		// uses deprecated token_sha256) to token_hash without rotating it. The
		// router finds token_hash callers by the "<token_id>_" prefix, so only
		// tokens with that shape can be converted; anything else would lock
		// the caller out.
		fs := flag.NewFlagSet("callers hash", flag.ExitOnError)
		tokenFile := fs.String("token-file", "", "file containing the raw caller token")
		iterations := fs.Int("hash-iterations", router.CallerTokenHashDefaultIterations, "PBKDF2-SHA3-512 iterations")
		fs.Parse(args[1:])
		token := readTokenFile(*tokenFile)
		tokenID, ok := router.CallerTokenIDFromToken(token)
		if !ok {
			fmt.Fprintln(os.Stderr, "token does not have the <token_id>_<secret> shape (rtr_metrum_<user>_<project>_<env>_<key>_<secret>) that token_hash lookup requires; rotate it with `metrum-ai-routerctl callers rotate`, or keep token_sha256 until rotation (#281)")
			os.Exit(1)
		}
		tokenHash, err := router.HashCallerToken(token, nil, *iterations)
		if err != nil {
			die("hash caller token: %v", err)
		}
		writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-caller-token-hash/v1", "token_hash": tokenHash, "token_id": tokenID,
			"note": "set both token_hash and token_id on the caller and remove token_sha256"})
	case "verify":
		fs := flag.NewFlagSet("callers verify", flag.ExitOnError)
		tokenFile := fs.String("token-file", "", "file containing the raw caller token")
		tokenHash := fs.String("token-hash", "", "token_hash value to check; alternative to --config/--id")
		path := fs.String("config", "", "router configuration path")
		id := fs.String("id", "", "caller id in --config")
		fs.Parse(args[1:])
		token := readTokenFile(*tokenFile)
		encoded := strings.TrimSpace(*tokenHash)
		if encoded == "" {
			if strings.TrimSpace(*path) == "" || strings.TrimSpace(*id) == "" {
				die("verify requires --token-hash or --config with --id")
			}
			cfg := loadConfigRaw(*path)
			tokenID := ""
			for _, caller := range cfg.Callers {
				if caller.ID == *id {
					encoded = strings.TrimSpace(caller.TokenHash)
					tokenID = strings.TrimSpace(caller.TokenID)
				}
			}
			if encoded == "" {
				die("caller %q has no token_hash", *id)
			}
			// Match the router: token_hash callers are found only by the
			// "<token_id>_" prefix, so a token without it never authenticates.
			reason := ""
			if tokenID == "" {
				reason = "caller has no token_id; token_hash callers require one"
			} else if !strings.HasPrefix(token, tokenID+"_") {
				reason = "token does not start with " + tokenID + "_"
			}
			if reason != "" {
				writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-caller-token-verify/v1", "match": false, "reason": reason})
				os.Exit(1)
			}
		}
		ok, err := router.VerifyCallerTokenHash(encoded, token)
		if err != nil {
			die("invalid token_hash: %v", err)
		}
		writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-caller-token-verify/v1", "match": ok})
		if !ok {
			os.Exit(1)
		}
	default:
		die("unsupported callers command %q", args[0])
	}
}

func providersCommand(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-routerctl providers <list|upsert|remove> [flags]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("providers list", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		fs.Parse(args[1:])
		cfg := loadConfigRaw(*path)
		rows := make([]map[string]any, 0, len(cfg.Provider))
		for name, provider := range cfg.Provider {
			rows = append(rows, map[string]any{
				"name": name, "base_url": redactURLCredentials(provider.BaseURL), "dialect": provider.Dialect,
				"api_key_env": redactEnvReference(provider.APIKeyEnv), "model_count": len(provider.Models),
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["name"].(string) < rows[j]["name"].(string) })
		writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-provider-list/v1", "data": rows})
	case "upsert":
		fs := flag.NewFlagSet("providers upsert", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		name := fs.String("name", "", "provider name")
		baseURL := fs.String("base-url", "", "upstream base URL")
		dialect := fs.String("dialect", "openai-chat", "upstream dialect")
		apiKeyEnv := fs.String("api-key-env", "", "environment variable name for api_key")
		modelRef := fs.String("model-ref", "", "provider model map key")
		modelID := fs.String("model", "", "served model id")
		fs.Parse(args[1:])
		cfg := loadConfigRaw(*path)
		provider := router.ProviderConfig{
			BaseURL: *baseURL, Dialect: *dialect, APIKeyEnv: *apiKeyEnv, AuthScheme: "bearer",
			Models: map[string]router.ProviderModel{},
		}
		if strings.TrimSpace(*apiKeyEnv) != "" {
			provider.APIKey = "${" + strings.TrimSpace(*apiKeyEnv) + "}"
		}
		if existing, ok := cfg.Provider[*name]; ok {
			provider.Models = existing.Models
			if provider.Models == nil {
				provider.Models = map[string]router.ProviderModel{}
			}
			if strings.TrimSpace(*baseURL) == "" {
				provider.BaseURL = existing.BaseURL
			}
			if strings.TrimSpace(*apiKeyEnv) == "" {
				provider.APIKeyEnv = existing.APIKeyEnv
				provider.APIKey = existing.APIKey
			}
		}
		if strings.TrimSpace(*modelRef) != "" {
			served := strings.TrimSpace(*modelID)
			if served == "" {
				served = *modelRef
			}
			provider.Models[*modelRef] = router.ProviderModel{Model: served}
		}
		if err := smartrouterctl.UpsertProvider(cfg, *name, provider); err != nil {
			die("%v", err)
		}
		backup, err := smartrouterctl.WriteConfigAtomic(*path, cfg)
		if err != nil {
			die("write config: %v", err)
		}
		writeJSON(map[string]any{
			"schema": "metrum.ai/smartrouter-provider-upsert/v1", "name": *name,
			"base_url": redactURLCredentials(provider.BaseURL), "config_backup": filepath.Base(backup),
			"activation": "local-config-written-restart-required",
		})
	case "remove":
		fs := flag.NewFlagSet("providers remove", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		name := fs.String("name", "", "provider name")
		fs.Parse(args[1:])
		cfg := loadConfigRaw(*path)
		if err := smartrouterctl.RemoveProvider(cfg, *name); err != nil {
			die("%v", err)
		}
		backup, err := smartrouterctl.WriteConfigAtomic(*path, cfg)
		if err != nil {
			die("write config: %v", err)
		}
		writeJSON(map[string]any{
			"schema": "metrum.ai/smartrouter-provider-remove/v1", "name": *name,
			"config_backup": filepath.Base(backup), "activation": "local-config-written-restart-required",
		})
	default:
		die("unsupported providers command %q", args[0])
	}
}

func modelsCommand(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-routerctl models <list|upsert-group|set-targets> [flags]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("models list", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		fs.Parse(args[1:])
		cfg := loadConfigRaw(*path)
		groups := make([]map[string]any, 0, len(cfg.Models))
		for name, group := range cfg.Models {
			groups = append(groups, map[string]any{"name": name, "strategy": group.Strategy, "target_count": len(group.Targets)})
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i]["name"].(string) < groups[j]["name"].(string) })
		writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-model-list/v1", "data": groups})
	case "upsert-group", "set-targets":
		fs := flag.NewFlagSet("models upsert-group", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		name := fs.String("name", "", "model group name")
		strategy := fs.String("strategy", "static", "routing strategy")
		targetsRaw := fs.String("targets", "", "comma-separated provider:model_ref[:weight]")
		fs.Parse(args[1:])
		targets, err := parseTargets(*targetsRaw)
		if err != nil {
			die("%v", err)
		}
		cfg := loadConfigRaw(*path)
		if err := smartrouterctl.UpsertModelGroup(cfg, *name, *strategy, targets); err != nil {
			die("%v", err)
		}
		backup, err := smartrouterctl.WriteConfigAtomic(*path, cfg)
		if err != nil {
			die("write config: %v", err)
		}
		writeJSON(map[string]any{
			"schema": "metrum.ai/smartrouter-model-group-upsert/v1", "name": *name, "strategy": *strategy,
			"target_count": len(targets), "config_backup": filepath.Base(backup),
			"activation": "local-config-written-restart-required",
		})
	default:
		die("unsupported models command %q", args[0])
	}
}

func statusCommand(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("config", "", "router configuration path")
	fs.Parse(args)
	cfg := loadConfig(*path)
	writeJSON(map[string]any{"schema": "metrum.ai/smartrouter-customer-status/v1", "config": configSummary(cfg), "authority": "local-file-owned"})
}

func usageCommand(args []string) {
	if len(args) == 0 {
		die("usage: metrum-ai-routerctl usage <summary|backup|restore> [flags]")
	}
	switch args[0] {
	case "summary":
		fs := flag.NewFlagSet("usage summary", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		since := fs.Duration("since", 24*time.Hour, "summary window")
		fs.Parse(args[1:])
		cfg := loadConfig(*path)
		if cfg.Server.UsageDB.Enable == nil || !*cfg.Server.UsageDB.Enable {
			die("usage database is disabled")
		}
		to := time.Now().UTC()
		markdown, err := router.GenerateUsageMarkdown(router.UsageReportOptions{
			Driver: cfg.Server.UsageDB.Driver, DBPath: cfg.Server.UsageDB.Path, DSN: cfg.Server.UsageDB.DSN,
			MigrationPolicy: cfg.Server.UsageDB.MigrationPolicy, From: to.Add(-*since), To: to,
		})
		if err != nil {
			die("generate usage summary: %v", err)
		}
		fmt.Print(markdown)
	case "backup":
		fs := flag.NewFlagSet("usage backup", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		out := fs.String("out", "", "destination sqlite path")
		confirm := fs.Bool("confirm-offline", false, "confirm router is stopped and SQLite is exclusive")
		fs.Parse(args[1:])
		if strings.TrimSpace(*out) == "" {
			die("--out is required")
		}
		cfg := loadConfigRaw(*path)
		result, err := smartrouterctl.BackupSQLiteUsage(cfg, *out, *confirm)
		if err != nil {
			die("%v", err)
		}
		writeJSON(result)
	case "restore":
		fs := flag.NewFlagSet("usage restore", flag.ExitOnError)
		path := fs.String("config", "", "router configuration path")
		from := fs.String("from", "", "backup sqlite path")
		confirm := fs.Bool("confirm-offline", false, "confirm router is stopped and SQLite is exclusive")
		fs.Parse(args[1:])
		if strings.TrimSpace(*from) == "" {
			die("--from is required")
		}
		cfg := loadConfigRaw(*path)
		result, err := smartrouterctl.RestoreSQLiteUsage(cfg, *from, *confirm)
		if err != nil {
			die("%v", err)
		}
		writeJSON(result)
	default:
		die("unsupported usage command %q", args[0])
	}
}

func blueprintCommand(args []string) {
	if len(args) == 0 || args[0] != "render" {
		die("usage: metrum-ai-routerctl blueprint render --intent PATH --out DIR")
	}
	fs := flag.NewFlagSet("blueprint render", flag.ExitOnError)
	intentPath := fs.String("intent", "", "stack intent YAML path")
	outDir := fs.String("out", "", "output directory")
	fs.Parse(args[1:])
	intent, err := smartrouterctl.LoadIntent(*intentPath)
	if err != nil {
		die("load intent: %v", err)
	}
	result, err := smartrouterctl.RenderBlueprint(intent, *outDir)
	if err != nil {
		die("render blueprint: %v", err)
	}
	writeJSON(result)
}

func parseTargets(raw string) ([]router.Target, error) {
	parts := splitCSV(raw)
	if len(parts) == 0 {
		return nil, fmt.Errorf("--targets is required (provider:model_ref[:weight])")
	}
	out := make([]router.Target, 0, len(parts))
	for _, part := range parts {
		fields := strings.Split(part, ":")
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid target %q", part)
		}
		weight := 100
		if len(fields) >= 3 {
			if _, err := fmt.Sscanf(fields[2], "%d", &weight); err != nil || weight <= 0 {
				return nil, fmt.Errorf("invalid weight in target %q", part)
			}
		}
		out = append(out, router.Target{Provider: fields[0], ModelRef: fields[1], Weight: weight})
	}
	return out, nil
}

func loadConfig(path string) *router.Config {
	if strings.TrimSpace(path) == "" {
		die("config is required")
	}
	cfg, err := router.LoadConfig(path)
	if err != nil {
		die("load config: %v", err)
	}
	return cfg
}

func loadConfigRaw(path string) *router.Config {
	cfg, err := smartrouterctl.LoadConfigRaw(path)
	if err != nil {
		die("load config: %v", err)
	}
	return cfg
}

func configSummary(cfg *router.Config) map[string]any {
	groups, callers := sortedGroups(cfg), sortedCallerIDs(cfg)
	return map[string]any{"schema": "metrum.ai/smartrouter-config-summary/v1", "valid": true, "model_groups": groups, "caller_ids": callers, "provider_count": len(cfg.Provider)}
}

func configDiff(before, after *router.Config) map[string]any {
	return map[string]any{"schema": "metrum.ai/smartrouter-config-diff/v1", "model_groups": setDiff(sortedGroups(before), sortedGroups(after)), "caller_ids": setDiff(sortedCallerIDs(before), sortedCallerIDs(after)), "provider_count": map[string]int{"before": len(before.Provider), "after": len(after.Provider)}}
}

func sortedGroups(cfg *router.Config) []string {
	out := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func sortedCallerIDs(cfg *router.Config) []string {
	out := make([]string, 0, len(cfg.Callers))
	for _, caller := range cfg.Callers {
		out = append(out, caller.ID)
	}
	sort.Strings(out)
	return out
}
func setDiff(before, after []string) map[string][]string {
	return map[string][]string{"added": difference(after, before), "removed": difference(before, after)}
}
func difference(left, right []string) []string {
	seen := map[string]struct{}{}
	for _, value := range right {
		seen[value] = struct{}{}
	}
	out := make([]string, 0)
	for _, value := range left {
		if _, ok := seen[value]; !ok {
			out = append(out, value)
		}
	}
	return out
}

func splitCSV(raw string) []string {
	values := strings.Split(raw, ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
func writeNewPrivateFile(path string, contents []byte) error {
	if strings.TrimSpace(path) == "" || filepath.Base(path) == "." {
		return errors.New("token output path is invalid")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(contents); err != nil {
		return err
	}
	return f.Sync()
}

// redactEnvReference returns name when it is a well-formed environment
// variable name (the only thing an *_env field may hold). Anything else may
// be a credential pasted into the wrong field, so it is never echoed.
func redactEnvReference(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if !validEnvReferenceName(trimmed) {
		return "[redacted: not a valid environment variable name]"
	}
	return trimmed
}

func validEnvReferenceName(name string) bool {
	for i, r := range name {
		if r == '_' || ('A' <= r && r <= 'Z') || (i > 0 && '0' <= r && r <= '9') {
			continue
		}
		return false
	}
	return name != ""
}

// redactURLCredentials strips userinfo (user:password@) from a base URL
// before it is printed. Unparseable values are withheld entirely.
func redactURLCredentials(raw string) string {
	if strings.TrimSpace(raw) == "" || !strings.Contains(raw, "@") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[redacted: unparseable base_url]"
	}
	if parsed.User != nil {
		parsed.User = url.User("redacted")
	}
	return parsed.String()
}

func addTokenHashFlags(fs *flag.FlagSet) (*string, *int) {
	format := fs.String("hash-format", router.TokenHashFormatPBKDF2SHA3, "stored token verifier: "+router.TokenHashFormatPBKDF2SHA3+" (token_hash) or "+router.TokenHashFormatLegacySHA256+" (deprecated token_sha256)")
	iterations := fs.Int("hash-iterations", router.CallerTokenHashDefaultIterations, "PBKDF2-SHA3-512 iterations for token_hash")
	return format, iterations
}
func tokenHashFormat(generated router.GeneratedToken) string {
	if generated.TokenHash != "" {
		return router.TokenHashFormatPBKDF2SHA3
	}
	return router.TokenHashFormatLegacySHA256
}
func readTokenFile(path string) string {
	if strings.TrimSpace(path) == "" {
		die("--token-file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		die("read token file: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		die("token file is empty")
	}
	return token
}
func writeJSON(value any) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		die("encode safe output: %v", err)
	}
	fmt.Println(string(body))
}
func die(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(2) }
