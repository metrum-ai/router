// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// compat-live-guard checks the local live harness preconditions. It does not
// prove that a provider will report all billable usage.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/metrum-ai/router/internal/router"
)

// liveBases is the allowlist of upstream endpoints a metered run may point at,
// per model group. It names the live provider hosts plus the gateway the
// approved release configuration routes the claude group through. Anything
// else (a mock, a loopback proxy, a typo) is refused before any request.
var liveBases = map[string][]string{
	"gpt":    {"https://api.openai.com/v1"},
	"claude": {"https://api.anthropic.com", "https://openrouter.ai/api"},
}

func validate(c *router.Config) error {
	if mode := strings.ToLower(strings.TrimSpace(c.Server.ConfigSource.Mode)); mode != "" && mode != "yaml" {
		return errors.New("live harness requires a YAML config source")
	}
	if len(c.Callers) != 1 {
		return errors.New("live harness requires exactly one caller")
	}
	q := c.Callers[0]
	for _, n := range []int64{q.Quota.Day.Tokens, q.Quota.Month.Tokens, q.Key.LifetimeTokens} {
		if n <= 0 || n > 200000 {
			return errors.New("caller day, month and lifetime token limits must be between 1 and 200000")
		}
	}
	if c.Server.ContentCapture.Enabled || q.ContentCapture.Enabled {
		return errors.New("content capture must be disabled")
	}
	db := c.Server.UsageDB
	if db.Enable == nil || !*db.Enable || !strings.EqualFold(db.Driver, "sqlite") || !filepath.IsAbs(db.Path) || db.DSN != "" {
		return errors.New("usage database must be an enabled SQLite database with an absolute path")
	}
	if !filepath.IsAbs(c.StatePath) {
		return errors.New("state path must be absolute")
	}
	for _, p := range []string{db.Path, db.Path + "-wal", db.Path + "-shm", c.StatePath, c.StatePath + ".bak"} {
		if _, err := os.Lstat(p); err == nil || !os.IsNotExist(err) {
			return errors.New("usage database and quota state must not already exist")
		}
	}
	for _, group := range []string{"gpt", "claude"} {
		g, ok := c.Models[group]
		if !ok || len(g.Targets) == 0 || g.ContentCapture.Enabled {
			return errors.New("gpt and claude groups must exist with content capture disabled")
		}
		for _, t := range g.Targets {
			// `passed` is accepted alongside `limited`: the cross-dialect paths
			// require a validated target, so a run that must exercise them cannot
			// present `limited` targets. Both are explicit operator declarations.
			status := strings.ToLower(strings.TrimSpace(t.RequestShapeSupport.ValidationStatus))
			if (status != "limited" && status != "passed") || t.InputPricePerMillionUSD <= 0 || t.OutputPricePerMillionUSD <= 0 || t.PricingSource == "" {
				return errors.New("targets must declare limited or passed validation, positive token prices and a pricing source")
			}
			p, ok := c.Provider[t.Provider]
			if !ok {
				return errors.New("target provider missing")
			}
			base := strings.TrimRight(p.BaseURL, "/")
			// Only named live hosts are acceptable. An allowlist (not any https,
			// not any non-loopback host) keeps an https mock or a mispointed
			// provider out of a metered run.
			if !slices.Contains(liveBases[group], base) {
				return fmt.Errorf("targets in group %q must use a live endpoint, one of: %s", group, strings.Join(liveBases[group], ", "))
			}
			if (group == "gpt" && t.Dialect != "openai-chat" && t.Dialect != "openai-responses") ||
				(group == "claude" && t.Dialect != "anthropic") {
				return errors.New("targets must use the OpenAI or Anthropic dialect for their group")
			}
			if group == "gpt" && !t.ForceStoreFalse {
				return errors.New("OpenAI targets must force store:false to prevent provider-side stored responses")
			}
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: compat-live-guard CONFIG")
		os.Exit(2)
	}
	cfg, err := router.LoadConfig(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot load live configuration")
		os.Exit(2)
	}
	if err := validate(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"usage_db": cfg.Server.UsageDB.Path})
}
