// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// pgschema manages a fresh PostgreSQL schema per e2e run and scans the
// config control-plane tables for credential-shaped leftovers. It reads the
// DSN from the environment only and never prints it.
//
// Usage:
//
//	pgschema create --name SCHEMA   (prints the scoped DSN env assignment name only)
//	pgschema drop   --name SCHEMA
//	pgschema scan   --name SCHEMA --needle-check   (exit 1 if a secret-shaped value is found)
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func die(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }

func dsn() string {
	d := strings.TrimSpace(os.Getenv("ROUTER_TEST_PG_DSN"))
	if d == "" {
		die("ROUTER_TEST_PG_DSN is not set")
	}
	return d
}

func validSchema(name string) bool {
	ok, _ := regexp.MatchString(`^[a-z][a-z0-9_]{0,40}$`, name)
	return ok
}

func main() {
	if len(os.Args) < 2 {
		die("usage: pgschema <create|drop|scan> --name SCHEMA")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	name := fs.String("name", "", "schema name")
	_ = fs.Parse(os.Args[2:])
	if !validSchema(*name) {
		die("invalid schema name %q", *name)
	}
	db, err := sql.Open("pgx", dsn())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()

	switch os.Args[1] {
	case "create":
		if _, err := db.Exec(`DROP SCHEMA IF EXISTS ` + *name + ` CASCADE`); err != nil {
			die("preclean: %v", err)
		}
		if _, err := db.Exec(`CREATE SCHEMA ` + *name); err != nil {
			die("create schema: %v", err)
		}
		fmt.Println("created")
	case "drop":
		if _, err := db.Exec(`DROP SCHEMA IF EXISTS ` + *name + ` CASCADE`); err != nil {
			die("drop schema: %v", err)
		}
		fmt.Println("dropped")
	case "scan":
		needle := strings.TrimSpace(os.Getenv("PGSCAN_NEEDLE"))
		if needle == "" {
			die("PGSCAN_NEEDLE is not set")
		}
		tables := []string{"router_config_documents", "router_config_change_events", "router_config_change_event_targets", "router_config_shadow_runs"}
		found := 0
		for _, table := range tables {
			var count int
			// Row-to-text cast server-side; the needle is bound as a parameter
			// and never interpolated into SQL.
			query := fmt.Sprintf(`SELECT COUNT(*) FROM %s.%s AS rowref WHERE row_to_json(rowref)::text LIKE '%%' || $1 || '%%'`, *name, table)
			if err := db.QueryRow(query, needle).Scan(&count); err != nil {
				if strings.Contains(err.Error(), "does not exist") {
					continue
				}
				die("scan %s: %v", table, err)
			}
			found += count
		}
		if found > 0 {
			fmt.Println("NEEDLE FOUND")
			os.Exit(1)
		}
		fmt.Println("clean")
	default:
		die("unknown action %q", os.Args[1])
	}
}
