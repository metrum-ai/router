// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

// DB-mode configuration control helpers for smartrouterctl.
//
// Expected router APIs (not yet on this branch; integrator wires them in):
//   - router.ImportCanonicalYAML(db *gorm.DB, scope string, yaml []byte, actor string) (ConfigSetImportResult, error)
//   - router.ExportConfigYAML(db *gorm.DB, scope string, setID string) ([]byte, error)
//   - router.ActivateConfigSet(db *gorm.DB, scope string, setID string, actor string) (ConfigSetActivationResult, error)
//   - router.RollbackConfigSet(db *gorm.DB, scope string, actor string) (ConfigSetRollbackResult, error)
//   - router.IssueCallerDB(db *gorm.DB, scope string, opts CallerIssueOptions, actor string) (CallerIssueResult, error)
//   - router.RevokeCallerDB(db *gorm.DB, scope string, callerID string, actor string) (CallerRevokeResult, error)
//   - router.RotateCallerDB(db *gorm.DB, scope string, callerID string, actor string) (CallerRotateResult, error)
//   - router.ListCallersDB(db *gorm.DB, scope string) ([]CallerListEntry, error)
//
// The router package currently exposes ConfigControlPlaneMigrationRunner,
// which returns an unexported *migrationRunner with no exported DB accessor.
// Until the router document APIs above exist, every helper below takes its
// database handle and operation as injected function dependencies so this
// package compiles and tests pass without waiting for router changes. The
// integrator replaces the injected stubs with thin closures over the real
// router functions.
//
// Security contract: no helper in this file prints raw caller tokens to
// stdout. Token material is returned inside structured results so the CLI
// layer (cmd/*) decides how to render it exactly once.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/metrum-ai/router/internal/router"
	"gorm.io/gorm"
)

// ConfigDBOptions selects the database and runtime scope for a control-plane
// operation. Driver mirrors router.UsageDBConfig.Driver ("sqlite" or
// "postgres"); Path is the SQLite file; DSN is the postgres connection string.
// RuntimeScope names the config scope (e.g. "prod", "staging"). Actor is the
// human or service identity recorded on mutations for audit.
type ConfigDBOptions struct {
	Driver       string
	Path         string
	DSN          string
	RuntimeScope string
	Actor        string
}

// Validate checks that the options are internally consistent. It does not
// open the database.
func (o ConfigDBOptions) Validate() error {
	driver := strings.ToLower(strings.TrimSpace(o.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite":
		if strings.TrimSpace(o.Path) == "" {
			return fmt.Errorf("config db path is required for sqlite driver")
		}
	case "postgres":
		if strings.TrimSpace(o.DSN) == "" {
			return fmt.Errorf("config db dsn is required for postgres driver")
		}
	default:
		return fmt.Errorf("unsupported config db driver %q (want sqlite or postgres)", o.Driver)
	}
	if strings.TrimSpace(o.RuntimeScope) == "" {
		return fmt.Errorf("runtime scope is required")
	}
	if strings.TrimSpace(o.Actor) == "" {
		return fmt.Errorf("actor is required")
	}
	return nil
}

// UsageDBConfig converts the options to the router DB config shape used by
// ConfigControlPlaneMigrationRunner and other router DB entry points.
func (o ConfigDBOptions) UsageDBConfig() router.UsageDBConfig {
	return router.UsageDBConfig{
		Driver: o.Driver,
		Path:   o.Path,
		DSN:    o.DSN,
	}
}

// ConfigDBOpener opens the config control-plane database and returns the
// handle plus a closer. The integrator wires this to a small adapter around
// router.ConfigControlPlaneMigrationRunner (or a direct open helper once the
// router exports one). Tests inject an in-memory SQLite handle.
type ConfigDBOpener func(opts ConfigDBOptions) (*gorm.DB, func() error, error)

// ---------------------------------------------------------------------------
// Config set import / export / activate / rollback
// ---------------------------------------------------------------------------

// ConfigSetImportResult is a secret-free summary of a YAML import.
type ConfigSetImportResult struct {
	SetID            string `json:"set_id"`
	RuntimeScope     string `json:"runtime_scope"`
	Name             string `json:"name"`
	ValidationStatus string `json:"validation_status"`
	CreatedBy        string `json:"created_by"`
}

// ConfigSetActivationResult is a secret-free summary of an activation.
type ConfigSetActivationResult struct {
	SetID        string `json:"set_id"`
	RuntimeScope string `json:"runtime_scope"`
	Status       string `json:"status"`
	ActivatedBy  string `json:"activated_by"`
}

// ConfigSetRollbackResult is a secret-free summary of a rollback.
type ConfigSetRollbackResult struct {
	PreviousSetID string `json:"previous_set_id"`
	ActiveSetID   string `json:"active_set_id"`
	RuntimeScope  string `json:"runtime_scope"`
	RolledBackBy  string `json:"rolled_back_by"`
}

// ImportConfigYAMLFunc is the injected dependency that performs the actual
// import. The integrator wires this to router.ImportCanonicalYAML.
type ImportConfigYAMLFunc func(db *gorm.DB, scope string, yaml []byte, actor string) (*ConfigSetImportResult, error)

// ExportConfigYAMLFunc is the injected dependency that performs the actual
// export. The integrator wires this to router.ExportConfigYAML.
type ExportConfigYAMLFunc func(db *gorm.DB, scope string, setID string) ([]byte, error)

// ActivateConfigSetFunc is the injected dependency that performs the actual
// activation. The integrator wires this to router.ActivateConfigSet.
type ActivateConfigSetFunc func(db *gorm.DB, scope string, setID string, actor string) (*ConfigSetActivationResult, error)

// RollbackConfigSetFunc is the injected dependency that performs the actual
// rollback. The integrator wires this to router.RollbackConfigSet.
type RollbackConfigSetFunc func(db *gorm.DB, scope string, actor string) (*ConfigSetRollbackResult, error)

// ImportConfigYAML imports canonical YAML into the config control-plane DB as
// a new validated (but not yet active) config set.
func ImportConfigYAML(opts ConfigDBOptions, yamlBytes []byte, openDB ConfigDBOpener, importFn ImportConfigYAMLFunc) (*ConfigSetImportResult, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if importFn == nil {
		return nil, errors.New("import function is required")
	}
	if len(yamlBytes) == 0 {
		return nil, errors.New("yaml bytes are required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	result, err := importFn(db, opts.RuntimeScope, yamlBytes, opts.Actor)
	if err != nil {
		return nil, fmt.Errorf("import config yaml: %w", err)
	}
	return result, nil
}

// ExportConfigYAML exports a config set from the DB back to canonical YAML.
func ExportConfigYAML(opts ConfigDBOptions, setID string, openDB ConfigDBOpener, exportFn ExportConfigYAMLFunc) ([]byte, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if exportFn == nil {
		return nil, errors.New("export function is required")
	}
	if strings.TrimSpace(setID) == "" {
		return nil, errors.New("set id is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	body, err := exportFn(db, opts.RuntimeScope, setID)
	if err != nil {
		return nil, fmt.Errorf("export config yaml: %w", err)
	}
	return body, nil
}

// ActivateConfigSet marks a validated config set as the active one for its
// runtime scope.
func ActivateConfigSet(opts ConfigDBOptions, setID string, openDB ConfigDBOpener, activateFn ActivateConfigSetFunc) (*ConfigSetActivationResult, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if activateFn == nil {
		return nil, errors.New("activate function is required")
	}
	if strings.TrimSpace(setID) == "" {
		return nil, errors.New("set id is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	result, err := activateFn(db, opts.RuntimeScope, setID, opts.Actor)
	if err != nil {
		return nil, fmt.Errorf("activate config set: %w", err)
	}
	return result, nil
}

// RollbackConfigSet deactivates the current active set and re-activates the
// previous validated set for the runtime scope.
func RollbackConfigSet(opts ConfigDBOptions, openDB ConfigDBOpener, rollbackFn RollbackConfigSetFunc) (*ConfigSetRollbackResult, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if rollbackFn == nil {
		return nil, errors.New("rollback function is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	result, err := rollbackFn(db, opts.RuntimeScope, opts.Actor)
	if err != nil {
		return nil, fmt.Errorf("rollback config set: %w", err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// DB-mode caller operations
// ---------------------------------------------------------------------------

// CallerIssueOptions carries the non-secret inputs for issuing a new caller.
type CallerIssueOptions struct {
	OwnerUser    string
	Project      string
	Environment  string
	Allow        []string
	MetricsAdmin bool
	ContentAdmin bool
	RPM          int
	TPM          int
	Concurrent   int
}

// CallerIssueResult is the structured result of issuing a caller. The raw
// token is included exactly once here so the CLI layer can print it to the
// user; library code must never log or print it.
type CallerIssueResult struct {
	CallerID  string `json:"caller_id"`
	TokenID   string `json:"token_id"`
	Token     string `json:"-"` // never serialize; CLI prints once interactively
	OwnerUser string `json:"owner_user"`
	Project   string `json:"project"`
}

// CallerRevokeResult is a secret-free summary of a caller revocation.
type CallerRevokeResult struct {
	CallerID  string `json:"caller_id"`
	Status    string `json:"status"`
	RevokedBy string `json:"revoked_by"`
}

// CallerRotateResult is the structured result of rotating a caller token.
// The new raw token is included exactly once here so the CLI layer can print
// it; library code must never log or print it.
type CallerRotateResult struct {
	CallerID  string `json:"caller_id"`
	TokenID   string `json:"token_id"`
	Token     string `json:"-"` // never serialize; CLI prints once interactively
	RotatedBy string `json:"rotated_by"`
}

// CallerListEntry is a secret-free row for listing callers.
type CallerListEntry struct {
	CallerID    string `json:"caller_id"`
	OwnerUser   string `json:"owner_user"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Status      string `json:"status"`
	TokenID     string `json:"token_id"`
}

// CallersIssueDBFunc is the injected dependency that issues a caller in the
// DB. The integrator wires this to router.IssueCallerDB.
type CallersIssueDBFunc func(db *gorm.DB, scope string, opts CallerIssueOptions, actor string) (*CallerIssueResult, error)

// CallersRevokeDBFunc is the injected dependency that revokes a caller in the
// DB. The integrator wires this to router.RevokeCallerDB.
type CallersRevokeDBFunc func(db *gorm.DB, scope string, callerID string, actor string) (*CallerRevokeResult, error)

// CallersRotateDBFunc is the injected dependency that rotates a caller token
// in the DB. The integrator wires this to router.RotateCallerDB.
type CallersRotateDBFunc func(db *gorm.DB, scope string, callerID string, actor string) (*CallerRotateResult, error)

// CallersListDBFunc is the injected dependency that lists callers from the
// DB. The integrator wires this to router.ListCallersDB.
type CallersListDBFunc func(db *gorm.DB, scope string) ([]CallerListEntry, error)

// CallersIssueDB issues a new caller in the config control-plane DB.
func CallersIssueDB(opts ConfigDBOptions, issueOpts CallerIssueOptions, openDB ConfigDBOpener, issueFn CallersIssueDBFunc) (*CallerIssueResult, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if issueFn == nil {
		return nil, errors.New("issue function is required")
	}
	if strings.TrimSpace(issueOpts.OwnerUser) == "" {
		return nil, errors.New("owner user is required")
	}
	if strings.TrimSpace(issueOpts.Project) == "" {
		return nil, errors.New("project is required")
	}
	if strings.TrimSpace(issueOpts.Environment) == "" {
		return nil, errors.New("environment is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	result, err := issueFn(db, opts.RuntimeScope, issueOpts, opts.Actor)
	if err != nil {
		return nil, fmt.Errorf("issue caller: %w", err)
	}
	return result, nil
}

// CallersRevokeDB revokes a caller in the config control-plane DB.
func CallersRevokeDB(opts ConfigDBOptions, callerID string, openDB ConfigDBOpener, revokeFn CallersRevokeDBFunc) (*CallerRevokeResult, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if revokeFn == nil {
		return nil, errors.New("revoke function is required")
	}
	if strings.TrimSpace(callerID) == "" {
		return nil, errors.New("caller id is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	result, err := revokeFn(db, opts.RuntimeScope, callerID, opts.Actor)
	if err != nil {
		return nil, fmt.Errorf("revoke caller: %w", err)
	}
	return result, nil
}

// CallersRotateDB rotates a caller token in the config control-plane DB.
func CallersRotateDB(opts ConfigDBOptions, callerID string, openDB ConfigDBOpener, rotateFn CallersRotateDBFunc) (*CallerRotateResult, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if rotateFn == nil {
		return nil, errors.New("rotate function is required")
	}
	if strings.TrimSpace(callerID) == "" {
		return nil, errors.New("caller id is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	result, err := rotateFn(db, opts.RuntimeScope, callerID, opts.Actor)
	if err != nil {
		return nil, fmt.Errorf("rotate caller: %w", err)
	}
	return result, nil
}

// CallersListDB lists callers from the config control-plane DB.
func CallersListDB(opts ConfigDBOptions, openDB ConfigDBOpener, listFn CallersListDBFunc) ([]CallerListEntry, error) {
	if openDB == nil {
		return nil, errors.New("config db opener is required")
	}
	if listFn == nil {
		return nil, errors.New("list function is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	db, closer, err := openDB(opts)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	defer func() { _ = closer() }()
	entries, err := listFn(db, opts.RuntimeScope)
	if err != nil {
		return nil, fmt.Errorf("list callers: %w", err)
	}
	return entries, nil
}
