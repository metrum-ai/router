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
	"strings"

	"github.com/metrum-ai/router/internal/router"
)

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
			if t.RequestShapeSupport.ValidationStatus != "limited" || t.InputPricePerMillionUSD <= 0 || t.OutputPricePerMillionUSD <= 0 || t.PricingSource == "" {
				return errors.New("targets must have limited validation, positive token prices and a pricing source")
			}
			p, ok := c.Provider[t.Provider]
			if !ok {
				return errors.New("target provider missing")
			}
			base := strings.TrimRight(p.BaseURL, "/")
			if (group == "gpt" && (base != "https://api.openai.com/v1" || (t.Dialect != "openai-chat" && t.Dialect != "openai-responses"))) ||
				(group == "claude" && (base != "https://api.anthropic.com" || t.Dialect != "anthropic")) {
				return errors.New("targets must use the native OpenAI and Anthropic API endpoints")
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
