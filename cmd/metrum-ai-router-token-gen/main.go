// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metrum-ai/router/internal/buildinfo"
	"github.com/metrum-ai/router/internal/router"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.Text())
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "generate" {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	ownerUser := fs.String("owner-user", "", "caller owner user id")
	username := fs.String("username", "", "alias for --owner-user")
	user := fs.String("user", "", "deprecated alias for --owner-user")
	project := fs.String("project", "", "caller project name")
	environment := fs.String("env", "dev", "caller environment")
	keySlug := fs.String("key", "", "visible key slug; defaults to kYYYYMMDD")
	allowRaw := fs.String("allow", "", "comma-separated allowed internal model groups; required")
	format := fs.String("format", "yaml", "output format: yaml, json, or env")
	hashFormat := fs.String("hash-format", router.TokenHashFormatPBKDF2SHA3, "stored token verifier: "+router.TokenHashFormatPBKDF2SHA3+" (token_hash) or "+router.TokenHashFormatLegacySHA256+" (deprecated token_sha256)")
	hashIterations := fs.Int("hash-iterations", router.CallerTokenHashDefaultIterations, "PBKDF2-SHA3-512 iterations for token_hash")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	effectiveOwner := firstNonEmpty(*ownerUser, *username, *user)
	generated, err := router.GenerateCallerToken(router.TokenGenerateOptions{
		OwnerUser:      effectiveOwner,
		Project:        *project,
		Environment:    *environment,
		KeySlug:        *keySlug,
		Allow:          splitCSV(*allowRaw),
		HashFormat:     *hashFormat,
		HashIterations: *hashIterations,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "yaml", "":
		printYAML(generated)
	case "json":
		printJSON(generated)
	case "env":
		printEnv(generated)
	default:
		fmt.Fprintf(os.Stderr, "unsupported format %q\n", *format)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: router-token-gen generate --owner-user USER_ID --project PROJECT --allow GROUP[,GROUP...] [--env ENV] [--key KEY] [--format yaml|json|env] [--hash-format pbkdf2-sha3-512|legacy-sha256] [--hash-iterations N]")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func printYAML(g router.GeneratedToken) {
	out := struct {
		Token       string                `yaml:"token"`
		TokenID     string                `yaml:"token_id"`
		TokenHash   string                `yaml:"token_hash,omitempty"`
		TokenSHA256 string                `yaml:"token_sha256,omitempty"`
		Caller      []router.CallerConfig `yaml:"callers"`
	}{
		Token:       g.Token,
		TokenID:     g.TokenID,
		TokenHash:   g.TokenHash,
		TokenSHA256: g.TokenSHA256,
		Caller:      []router.CallerConfig{g.Caller},
	}
	raw, err := yaml.Marshal(out)
	if err != nil {
		panic(err)
	}
	fmt.Print(string(raw))
}

func printJSON(g router.GeneratedToken) {
	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(raw))
}

func printEnv(g router.GeneratedToken) {
	fmt.Printf("ROUTER_TOKEN=%q\n", g.Token)
	fmt.Printf("ROUTER_TOKEN_ID=%q\n", g.TokenID)
	if g.TokenHash != "" {
		fmt.Printf("ROUTER_TOKEN_HASH=%q\n", g.TokenHash)
	}
	if g.TokenSHA256 != "" {
		fmt.Printf("ROUTER_TOKEN_SHA256=%q\n", g.TokenSHA256)
	}
}
