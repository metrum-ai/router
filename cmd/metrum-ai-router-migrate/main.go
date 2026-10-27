// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// router-migrate is intentionally non-serving. It exposes only safe migration
// metadata until persistent scopes have checked-in conversion definitions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/metrum-ai/router/internal/buildinfo"
	"github.com/metrum-ai/router/internal/router"
)

// migrationRunner is the unexported router migration runner surface this
// command uses.
type migrationRunner interface {
	Status() (router.MigrationStatus, error)
	Verify() (router.MigrationStatus, error)
	ApplyPending(runner string) error
	ApplyMaintenancePendingWithEvidence(runner, backupEvidenceRef string) error
	ApplyNonTransactionalMaintenancePendingWithEvidence(runner, backupEvidenceRef string) error
	RequestDataJobCancellation(jobKey string) error
	RetryDataJob(jobKey, recoveryEvidenceRef string) error
	RunDataJob(ctx context.Context, jobKey, runnerID string, checkpoint router.DataJobCheckpoint) (router.MigrationDataJobStatus, error)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.Text())
		return
	}
	action := flag.String("action", "status", "migration action: status, plan, verify, verify-serving, apply, maintenance, non-transactional-maintenance, cancel, retry, or resume")
	scope := flag.String("scope", "usage", "migration scope: usage or router-config (the configuration control plane, #395)")
	driver := flag.String("driver", "sqlite", "usage DB driver: sqlite or postgres")
	dbPath := flag.String("db", "usage.sqlite", "path to usage SQLite database")
	dsnEnv := flag.String("dsn-env", "ROUTER_USAGE_DB_DSN", "environment variable containing the Postgres DSN")
	runnerID := flag.String("runner", "router-migrate", "safe runner identifier for the migration ledger")
	jobKey := flag.String("job", "", "checked-in migration data-job key for cancel, retry, or resume")
	checkpointOrdinal := flag.Int("checkpoint-ordinal", 0, "non-negative scalar checkpoint ordinal for resume")
	recoveryEvidenceRef := flag.String("recovery-evidence-ref", "", "bounded safe recovery evidence reference required for retry")
	backupEvidenceRef := flag.String("backup-evidence-ref", "", "bounded safe backup evidence reference required for maintenance actions")
	jsonOut := flag.Bool("json", false, "emit machine-readable safe migration status")
	flag.Parse()
	dsn := ""
	if *driver == "postgres" {
		dsn = os.Getenv(*dsnEnv)
		if dsn == "" {
			die("Postgres migration scope requires non-empty --dsn-env %q", *dsnEnv)
		}
	}
	dbCfg := router.UsageDBConfig{Driver: *driver, Path: *dbPath, DSN: dsn}
	var r migrationRunner
	var closeDB func() error
	var err error
	configScope := false
	switch strings.ToLower(strings.TrimSpace(*scope)) {
	case "", "usage":
		r, closeDB, err = router.UsageMigrationRunner(dbCfg)
	case "router-config":
		// The configuration control plane is migrated by a non-serving
		// deployment job (docs/CONFIG_CONTROL_PLANE.md), never by router
		// startup. This scope applies the online prefix; the phase 2-5
		// maintenance batch still requires --action maintenance with a
		// backup evidence reference.
		configScope = true
		r, closeDB, err = router.ConfigControlPlaneMigrationRunner(dbCfg)
	default:
		die("unsupported migration scope %q (want usage or router-config)", *scope)
	}
	if err != nil {
		die("open migration scope: %v", err)
	}
	defer closeDB()
	if configScope {
		switch *action {
		case "status", "plan", "verify", "apply", "maintenance":
		default:
			die("scope router-config supports only status, plan, verify, apply, and maintenance")
		}
	}
	var status router.MigrationStatus
	switch *action {
	case "status", "plan":
		status, err = r.Status()
	case "verify":
		status, err = r.Verify()
	case "verify-serving":
		status, err = r.Verify()
		if err == nil {
			err = requireServingCompatible(status)
		}
	case "apply":
		err = r.ApplyPending(*runnerID)
		if err == nil {
			status, err = r.Status()
		}
	case "maintenance":
		if *backupEvidenceRef == "" {
			die("migration maintenance requires --backup-evidence-ref")
		}
		err = r.ApplyMaintenancePendingWithEvidence(*runnerID, *backupEvidenceRef)
		if err == nil {
			status, err = r.Status()
		}
	case "non-transactional-maintenance":
		if *backupEvidenceRef == "" {
			die("migration non-transactional-maintenance requires --backup-evidence-ref")
		}
		err = r.ApplyNonTransactionalMaintenancePendingWithEvidence(*runnerID, *backupEvidenceRef)
		if err == nil {
			status, err = r.Status()
		}
	case "cancel":
		if *jobKey == "" {
			die("migration cancel requires --job")
		}
		err = r.RequestDataJobCancellation(*jobKey)
		if err == nil {
			status, err = r.Status()
		}
	case "retry":
		if *jobKey == "" {
			die("migration retry requires --job")
		}
		err = r.RetryDataJob(*jobKey, *recoveryEvidenceRef)
		if err == nil {
			status, err = r.Status()
		}
	case "resume":
		if *jobKey == "" || *checkpointOrdinal < 0 {
			die("migration resume requires --job and non-negative --checkpoint-ordinal")
		}
		_, err = r.RunDataJob(context.Background(), *jobKey, *runnerID, router.DataJobCheckpoint{Ordinal: *checkpointOrdinal})
		if err == nil {
			status, err = r.Status()
		}
	default:
		die("unsupported --action %q", *action)
	}
	if err != nil {
		die("migration %s: %v", *action, err)
	}
	if *jsonOut {
		b, _ := json.Marshal(status)
		fmt.Println(string(b))
		return
	}
	fmt.Printf("scope=%s schema_version=%d data_version=%d compatible=%t state=%s pending=%d entries=%d\n", status.Scope, status.SchemaVersion, status.DataVersion, status.Compatible, status.State, len(status.Pending), len(status.Entries))
}

func die(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }

func requireServingCompatible(status router.MigrationStatus) error {
	if !router.UsageMigrationServingCompatible(status) {
		return fmt.Errorf("migration verify-serving requires current compatible ledger and validated data jobs (state %s)", status.State)
	}
	return nil
}
