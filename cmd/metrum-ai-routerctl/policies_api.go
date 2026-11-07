// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/metrum-ai/router/internal/smartrouterctl"
	"io"
	"os"
)

func policiesCommand(args []string) {
	if err := runPoliciesCommand(args, os.Stdout); err != nil {
		die("%v", err)
	}
}
func runPoliciesCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: policies <list|get|draft|validate|diff|activate|rollback|replace|delete> [flags]")
	}
	operation := args[0]
	switch operation {
	case "list", "get", "draft", "validate", "diff", "activate", "rollback", "replace", "delete":
	default:
		return fmt.Errorf("unsupported policy command")
	}
	fs := flag.NewFlagSet("policies "+operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpoint := fs.String("router-url", "", "dedicated admin API URL (or ROUTER_URL)")
	keyFile := fs.String("admin-key-file", "", "regular mode-0600 admin credential file")
	id := fs.String("set-id", "", "policy set ID")
	file := fs.String("file", "", "JSON request file, or - for stdin")
	match := fs.String("if-match", "", "explicit current revision ETag for mutation")
	replay := fs.String("idempotency-key", "", "explicit replay key, generated if omitted")
	against := fs.String("against", "", "base policy set for diff")
	limit := fs.Int("limit", 0, "page limit")
	cursor := fs.String("cursor", "", "opaque server-issued cursor")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("invalid policies flags: %v", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional policies arguments")
	}
	if (operation == "draft" || operation == "replace") && *file == "" {
		return fmt.Errorf("JSON request --file required")
	}
	if *file != "" && operation != "draft" && operation != "replace" {
		return fmt.Errorf("--file applies only to draft or replace")
	}
	var body json.RawMessage
	if *file != "" {
		var reader io.Reader = os.Stdin
		if *file != "-" {
			f, err := os.Open(*file)
			if err != nil {
				return fmt.Errorf("policy request file unavailable")
			}
			defer f.Close()
			reader = f
		}
		raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 || !json.Valid(raw) {
			return fmt.Errorf("invalid or oversized policy JSON request")
		}
		body = raw
	}
	client, err := smartrouterctl.NewPolicyAPIClient(*endpoint, *keyFile)
	if err != nil {
		return err
	}
	result, err := client.CallWithMetadata(operation, *id, body, smartrouterctl.PolicyCallOptions{IfMatch: *match, IdempotencyKey: *replay, Against: *against, PageSize: *limit, Cursor: *cursor})
	if err != nil {
		return err
	}
	// An envelope preserves ETag/Link without following links or silently replaying
	// mutations. Server problem details are never printed by the client.
	if !json.Valid(result.Body) {
		return fmt.Errorf("policy API returned invalid JSON")
	}
	return json.NewEncoder(out).Encode(result)
}
