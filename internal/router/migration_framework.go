// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// This file contains the durable migration primitives shared by persistent
// router scopes.  It deliberately stores only scalar, operator-safe metadata;
// migration definitions themselves remain checked into the binary.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const usageMigrationScope = "usage"

const usageLegacyBaselineMigrationID = 2026071901
const usageReasoningTelemetryMigrationID = 2026072301
const usageHistoricalValidationMigrationID = 2026080501
const usageContentCaptureEncryptionMigrationID = 2026081901
const usageTargetRegionDiagnosticsMigrationID = 2026090901
const usageCachedInputPricingMigrationID = 2026091301

var usageMigrationCompatibility = MigrationCompatibility{MinSchema: 0, MaxSchema: 5, MinData: 0, MaxData: 1}

// usageMigrationDefinitions is the sole owner of usage application schema.
// The first migration creates fresh-install tables/indexes through its reviewed
// Go step and also adopts a matching pre-ledger installation. Serving startup
// never calls this handler; router-migrate owns it.
var usageMigrationDefinitions = []MigrationDefinition{{
	ID:               usageLegacyBaselineMigrationID,
	Scope:            usageMigrationScope,
	Name:             "create or adopt explicit usage schema baseline",
	Release:          "2026.8",
	Checksum:         "eb12b411373019789680d17866c4c47fd39e99b825c3df5b2484052f6a48bf04",
	SchemaVersion:    1,
	DataVersion:      0,
	Transactional:    true,
	MaintenanceMode:  "online",
	RollbackClass:    "package-only",
	HandlerKey:       "usage.explicit-baseline.apply.v1@applyUsageExplicitBaseline",
	PostconditionKey: "usage.legacy-baseline.schema.v1@verifyUsageLegacyBaseline",
	ExecutionMode:    "transactional",
	LockClass:        "online",
	TimeoutClass:     "bounded",
	Apply:            applyUsageExplicitBaseline,
	Verify:           verifyUsageLegacyBaseline,
}, {
	ID:              usageReasoningTelemetryMigrationID,
	Scope:           usageMigrationScope,
	Name:            "add nullable reasoning-token usage telemetry",
	Release:         "2026.7",
	Checksum:        "e23d1dd0f41292af050f2ee9a6c2bc55a5430d22397c90dd33c145bd350264b8",
	SchemaVersion:   2,
	DataVersion:     0,
	Transactional:   true,
	MaintenanceMode: "online",
	// Older binaries reject this new ledger ID under validate/deployment-job.
	// Downgrade therefore requires restoring the pre-migration database snapshot.
	RollbackClass:    "restore-required",
	HandlerKey:       "usage.reasoning-telemetry.apply.v1@applyUsageReasoningTelemetryMigration",
	PostconditionKey: "usage.reasoning-telemetry.schema.v1@verifyUsageReasoningTelemetryMigration",
	Dependencies:     []int{usageLegacyBaselineMigrationID},
	ExecutionMode:    "transactional",
	LockClass:        "online",
	TimeoutClass:     "bounded",
	Apply:            applyUsageReasoningTelemetryMigration,
	Verify:           verifyUsageReasoningTelemetryMigration,
}, {
	ID:              usageHistoricalValidationMigrationID,
	Scope:           usageMigrationScope,
	Name:            "checkpoint historical usage row validation",
	Release:         "2026.8",
	Checksum:        "6e2f5f71ed4f0e2d63c6eb5845b4c02226cfbafc22a2bc27c04c03e6c6426f15",
	SchemaVersion:   2,
	DataVersion:     1,
	Transactional:   true,
	MaintenanceMode: "online",
	// A previous binary does not know this ledger ID. After it is applied,
	// validate/deployment-job therefore fails closed on package downgrade.
	// Restore the approved pre-migration database snapshot before using an
	// earlier package; no reverse migration is available.
	RollbackClass: "restore-required",
	// This is the exact immutable digest emitted by the merged Stage 3 binary
	// before its rollback classification was corrected. Keep that already
	// applied ledger compatible with this metadata-only correction; no other
	// digest, handler, checksum, or stored data is accepted or changed.
	LegacyManifestDigests: []string{"c81ba6d251fe503a7fd4fc0dffc3d7ce3e80e5043eaaaae74b5570809d7d8627"},
	HandlerKey:            "usage.historical-validation.apply.v1@applyUsageHistoricalValidationMigration",
	PostconditionKey:      "usage.historical-validation.schema.v1@verifyUsageReasoningTelemetryMigration",
	Dependencies:          []int{usageReasoningTelemetryMigrationID},
	ExecutionMode:         "transactional",
	LockClass:             "online",
	TimeoutClass:          "bounded",
	DataJobKey:            "historical-usage-validation-v1",
	Apply:                 applyUsageHistoricalValidationMigration,
	Verify:                verifyUsageReasoningTelemetryMigration,
}, {
	ID:               usageContentCaptureEncryptionMigrationID,
	Scope:            usageMigrationScope,
	Name:             "add content-capture encryption metadata",
	Release:          "2026.8",
	Checksum:         "66c60d75135cc458cbae0ab4866f8758a06b830880f5ea3b3d1f9c9fe0c9984a",
	SchemaVersion:    3,
	DataVersion:      0,
	Transactional:    true,
	MaintenanceMode:  "online",
	RollbackClass:    "restore-required",
	HandlerKey:       "usage.content-capture-encryption.apply.v1@applyUsageContentCaptureEncryptionMigration",
	PostconditionKey: "usage.content-capture-encryption.schema.v1@verifyUsageContentCaptureEncryptionMigration",
	Dependencies:     []int{usageHistoricalValidationMigrationID},
	ExecutionMode:    "transactional",
	LockClass:        "online",
	TimeoutClass:     "bounded",
	Apply:            applyUsageContentCaptureEncryptionMigration,
	Verify:           verifyUsageContentCaptureEncryptionMigration,
}, {
	ID:               usageTargetRegionDiagnosticsMigrationID,
	Scope:            usageMigrationScope,
	Name:             "add selected target region diagnostics",
	Release:          "2026.9",
	Checksum:         "ef1cd1b6458af1c64b9bded4deb79e72530c1cafda222012396e7f0eb512970d",
	SchemaVersion:    4,
	DataVersion:      0,
	Transactional:    true,
	MaintenanceMode:  "online",
	RollbackClass:    "restore-required",
	HandlerKey:       "usage.target-region-diagnostics.apply.v1@applyUsageTargetRegionDiagnosticsMigration",
	PostconditionKey: "usage.target-region-diagnostics.schema.v1@verifyUsageTargetRegionDiagnosticsMigration",
	Dependencies:     []int{usageContentCaptureEncryptionMigrationID},
	ExecutionMode:    "transactional",
	LockClass:        "online",
	TimeoutClass:     "bounded",
	Apply:            applyUsageTargetRegionDiagnosticsMigration,
	Verify:           verifyUsageTargetRegionDiagnosticsMigration,
}, {
	ID:               usageCachedInputPricingMigrationID,
	Scope:            usageMigrationScope,
	Name:             "add nullable cached-input price and token usage fields",
	Release:          "2026.9",
	Checksum:         "b04c1a1139537bd27a6016e312e10b8a2c47e39b742fd9f5286ee5a1c21a9ee4",
	SchemaVersion:    5,
	DataVersion:      0,
	Transactional:    true,
	MaintenanceMode:  "online",
	RollbackClass:    "restore-required",
	HandlerKey:       "usage.cached-input-pricing.apply.v1@applyUsageCachedInputPricingMigration",
	PostconditionKey: "usage.cached-input-pricing.schema.v1@verifyUsageCachedInputPricingMigration",
	Dependencies:     []int{usageTargetRegionDiagnosticsMigrationID},
	ExecutionMode:    "transactional",
	LockClass:        "online",
	TimeoutClass:     "bounded",
	Apply:            applyUsageCachedInputPricingMigration,
	Verify:           verifyUsageCachedInputPricingMigration,
}}

func init() {
	for i := range usageMigrationDefinitions {
		usageMigrationDefinitions[i] = FinalizeMigrationDefinition(usageMigrationDefinitions[i])
	}
}

// MigrationCompatibility declares the inclusive schema/data versions a binary
// can safely operate with. Versions are monotonically increasing integers.
type MigrationCompatibility struct {
	MinSchema int
	MaxSchema int
	MinData   int
	MaxData   int
}

// MigrationDefinition is immutable once released. Checksum is calculated from
// the reviewed metadata and must be supplied by the migration author.
type MigrationDefinition struct {
	ID              int
	Scope           string
	Name            string
	Release         string
	Checksum        string
	SchemaVersion   int
	DataVersion     int
	Transactional   bool
	RollbackClass   string // package-only, config-only, restore-required, prohibited
	MaintenanceMode string // online, maintenance
	// HandlerKey and PostconditionKey are checked-in stable identities. They
	// make a manifest change observable even when Go function pointers change.
	HandlerKey       string
	PostconditionKey string
	Dependencies     []int
	ExecutionMode    string // transactional, non-transactional
	LockClass        string // online, maintenance
	TimeoutClass     string // bounded, maintenance
	// DataJobKey identifies the separately resumable conversion required before
	// DataVersion may be reported as complete. It is empty for schema-only
	// migrations. A data job is deliberately not executed by ApplyPending.
	DataJobKey     string
	ManifestDigest string // canonical digest of the immutable metadata
	// LegacyManifestDigests is an explicit, source-reviewed allowlist for an
	// already-applied historical manifest whose metadata-only correction is
	// compatible with the current handler, checksum, and stored data.
	LegacyManifestDigests []string
	Apply                 func(*gorm.DB) error
	Verify                func(*gorm.DB) error
}

// HandlerKey and PostconditionKey deliberately name the checked-in executable
// functions after an @ separator. This keeps the operator-facing portion of a
// key stable while making a source-level handler swap fail manifest validation.
// Function names are used instead of a program-counter address because PC
// values are build-specific and cannot be durable manifest identities.
func migrationHandlerKeyMatches(key string, fn func(*gorm.DB) error) bool {
	if fn == nil {
		return false
	}
	separator := strings.LastIndex(key, "@")
	if separator <= 0 || separator == len(key)-1 {
		return false
	}
	function := runtime.FuncForPC(reflect.ValueOf(fn).Pointer())
	if function == nil {
		return false
	}
	name := function.Name()
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return key[separator+1:] == name
}

// CanonicalMigrationDigest binds all reviewed, non-executable migration
// metadata. It intentionally omits function pointers and instead includes the
// checked-in HandlerKey/PostconditionKey identities.
func CanonicalMigrationDigest(d MigrationDefinition) string {
	deps := append([]int(nil), d.Dependencies...)
	legacyDigests := append([]string(nil), d.LegacyManifestDigests...)
	sort.Ints(deps)
	sort.Strings(legacyDigests)
	parts := []string{fmt.Sprint(d.ID), d.Scope, d.Name, d.Release, fmt.Sprint(d.SchemaVersion), fmt.Sprint(d.DataVersion), fmt.Sprint(d.Transactional), d.RollbackClass, d.MaintenanceMode, d.HandlerKey, d.PostconditionKey, d.ExecutionMode, d.LockClass, d.TimeoutClass, d.DataJobKey}
	for _, dep := range deps {
		parts = append(parts, fmt.Sprint(dep))
	}
	parts = append(parts, legacyDigests...)
	return MigrationChecksum(parts...)
}

// FinalizeMigrationDefinition is used only by checked-in manifest literals.
// It prevents a caller from supplying an arbitrary digest.
func FinalizeMigrationDefinition(d MigrationDefinition) MigrationDefinition {
	d.ManifestDigest = CanonicalMigrationDigest(d)
	return d
}

// MigrationLedgerEntry is intentionally safe to return to operators. It never
// includes SQL, DSNs, request data, credentials, or configuration values.
type MigrationLedgerEntry struct {
	Scope       string
	MigrationID int
	Checksum    string
	State       string
	StartedAt   time.Time
	CompletedAt time.Time
	Runner      string
	DurationMS  int64
	ErrorCode   string
	ErrorText   string
}

type migrationLedgerRecord struct {
	Scope          string `gorm:"primaryKey;column:scope;type:text"`
	MigrationID    int    `gorm:"primaryKey;column:migration_id"`
	Checksum       string `gorm:"column:checksum;type:text;not null"`
	ManifestDigest string `gorm:"column:manifest_digest;type:text;not null;default:''"`
	State          string `gorm:"column:state;type:text;not null"`
	StartedAt      string `gorm:"column:started_at;type:text;not null"`
	CompletedAt    string `gorm:"column:completed_at;type:text;not null;default:''"`
	Runner         string `gorm:"column:runner;type:text;not null;default:''"`
	DurationMS     int64  `gorm:"column:duration_ms;not null;default:0"`
	ErrorCode      string `gorm:"column:error_code;type:text;not null;default:''"`
	ErrorText      string `gorm:"column:error_text;type:text;not null;default:''"`
}

// migrationAttemptRecord, migrationDataJobRecord, and
// migrationDataJobCheckpointRecord are the normalized scalar Stage-1 contract
// for future resumable jobs. Stage 1 records no jobs and changes no serving
// behavior; later stages own execution/checkpoint semantics.
type migrationAttemptRecord struct {
	Scope               string `gorm:"primaryKey"`
	MigrationID         int    `gorm:"primaryKey"`
	Attempt             int    `gorm:"primaryKey"`
	Action              string
	State               string
	OwnerGeneration     int64
	BackupEvidenceRef   string
	RecoveryEvidenceRef string
	StartedAt           string
	CompletedAt         string
	SafeErrorClass      string
}

func (migrationAttemptRecord) TableName() string { return "schema_migration_attempts" }

type migrationDataJobRecord struct {
	JobID             string `gorm:"primaryKey"`
	Scope             string
	MigrationID       int
	DataVersion       int
	State             string
	ExecutionMode     string
	ValidationMode    string
	ThrottlePerMinute int
	RowsScanned       int64
	RowsUpdated       int64
	RowsSkipped       int64
	RowsFailed        int64
	StartedAt         string
	CompletedAt       string
	CancelRequestedAt string
	SafeErrorClass    string
}

func (migrationDataJobRecord) TableName() string { return "schema_data_jobs" }

type migrationDataJobCheckpointRecord struct {
	JobID             string `gorm:"primaryKey"`
	CheckpointOrdinal int    `gorm:"primaryKey"`
	Shard             string
	RangeStart        int64
	RangeEnd          int64
	Cursor            string
	RowsScanned       int64
	RowsUpdated       int64
	RowsSkipped       int64
	RowsFailed        int64
	State             string
	StartedAt         string
	CompletedAt       string
}

func (migrationDataJobCheckpointRecord) TableName() string { return "schema_data_job_checkpoints" }

func (migrationLedgerRecord) TableName() string { return "schema_migration_ledger" }

type migrationLockRecord struct {
	Scope      string `gorm:"primaryKey;column:scope;type:text"`
	Runner     string `gorm:"column:runner;type:text;not null"`
	Generation int64  `gorm:"column:generation;not null;default:0"`
	LockedAt   string `gorm:"column:locked_at;type:text;not null"`
	ExpiresAt  string `gorm:"column:expires_at;type:text;not null"`
}

func (migrationLockRecord) TableName() string { return "schema_migration_locks" }

// MigrationStatus summarizes one persistent scope without exposing database
// internals. Incompatible includes checksum tampering and future versions.
type MigrationStatus struct {
	Scope string
	// StatusUnavailable reports that the safe migration ledger snapshot could
	// not be read. It is deliberately a bounded boolean rather than a database
	// error so global metrics can remain available without exposing internals.
	StatusUnavailable bool
	SchemaVersion     int
	DataVersion       int
	Compatibility     MigrationCompatibility
	Compatible        bool
	State             string // current, pending, in-progress, failed, incompatible
	Pending           []MigrationDefinition
	Entries           []MigrationLedgerEntry
	Jobs              []MigrationDataJobStatus
}

type migrationRunner struct {
	db              *gorm.DB
	scope           string
	compatibility   MigrationCompatibility
	definitions     []MigrationDefinition
	dataJobs        []DataJobDefinition
	ownerGeneration int64
	advisorySession *sql.Conn
}

const (
	migrationPostgresMinimumVersion = 120000
	migrationLockTimeout            = 5 * time.Second
	migrationStatementTimeout       = 30 * time.Second
)

// NewMigrationRunner validates a checked-in manifest before it can touch a DB.
func NewMigrationRunner(db *gorm.DB, scope string, compatibility MigrationCompatibility, definitions []MigrationDefinition) (*migrationRunner, error) {
	return NewMigrationRunnerWithDataJobs(db, scope, compatibility, definitions, nil)
}

// NewMigrationRunnerWithDataJobs binds resumable data work to the same strict
// immutable manifest as schema work. It remains a non-serving API.
func NewMigrationRunnerWithDataJobs(db *gorm.DB, scope string, compatibility MigrationCompatibility, definitions []MigrationDefinition, dataJobs []DataJobDefinition) (*migrationRunner, error) {
	if db == nil || strings.TrimSpace(scope) == "" {
		return nil, errors.New("migration runner requires database and scope")
	}
	if compatibility.MaxSchema < compatibility.MinSchema || compatibility.MaxData < compatibility.MinData {
		return nil, errors.New("invalid migration compatibility range")
	}
	defs := append([]MigrationDefinition(nil), definitions...)
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	declaredIDs := make(map[int]struct{}, len(defs))
	for _, d := range defs {
		// Build the complete ID set before examining dependencies. A smaller
		// numeric dependency is not necessarily a declared migration.
		if d.ID <= 0 {
			return nil, errors.New("invalid immutable migration manifest")
		}
		if _, duplicate := declaredIDs[d.ID]; duplicate {
			return nil, errors.New("invalid immutable migration manifest")
		}
		declaredIDs[d.ID] = struct{}{}
	}
	previous := 0
	for _, d := range defs {
		if d.Scope != scope || d.ID <= previous || d.ID <= 0 || !validMigrationChecksum(d.Checksum) ||
			d.HandlerKey == "" || d.PostconditionKey == "" || d.ExecutionMode == "" || d.LockClass == "" || d.TimeoutClass == "" || d.ManifestDigest != CanonicalMigrationDigest(d) ||
			!migrationHandlerKeyMatches(d.HandlerKey, d.Apply) || !migrationHandlerKeyMatches(d.PostconditionKey, d.Verify) {
			return nil, errors.New("invalid immutable migration manifest")
		}
		seenLegacyDigests := make(map[string]struct{}, len(d.LegacyManifestDigests))
		for _, digest := range d.LegacyManifestDigests {
			if !validMigrationChecksum(digest) || digest == d.ManifestDigest {
				return nil, errors.New("invalid immutable migration legacy digest")
			}
			if _, duplicate := seenLegacyDigests[digest]; duplicate {
				return nil, errors.New("invalid immutable migration legacy digest")
			}
			seenLegacyDigests[digest] = struct{}{}
		}
		// Dependencies are an ordered part of the immutable manifest contract:
		// a definition can depend only on a definition already declared in this
		// scope. This prevents a manifest from being valid while describing an
		// unexecutable cycle, forward reference, or duplicate prerequisite.
		seenDependencies := make(map[int]struct{}, len(d.Dependencies))
		for _, dependency := range d.Dependencies {
			if dependency <= 0 || dependency >= d.ID {
				return nil, errors.New("invalid immutable migration manifest dependency")
			}
			if _, declared := declaredIDs[dependency]; !declared {
				return nil, errors.New("invalid immutable migration manifest dependency")
			}
			if _, duplicate := seenDependencies[dependency]; duplicate {
				return nil, errors.New("invalid immutable migration manifest dependency")
			}
			seenDependencies[dependency] = struct{}{}
			if dependency > previous {
				return nil, errors.New("invalid immutable migration manifest dependency")
			}
		}
		previous = d.ID
	}
	jobs, err := validateDataJobDefinitions(scope, defs, dataJobs)
	if err != nil {
		return nil, err
	}
	return &migrationRunner{db: db, scope: scope, compatibility: compatibility, definitions: defs, dataJobs: jobs}, nil
}

func validMigrationChecksum(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

// MigrationChecksum makes definitions reviewable without serializing function
// bodies. Include this value as a literal in the checked-in manifest.
func MigrationChecksum(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *migrationRunner) ensureLedger() error {
	// Explicit DDL keeps the framework bootstrap independent of AutoMigrate.
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS schema_migration_ledger (scope TEXT NOT NULL, migration_id BIGINT NOT NULL, checksum TEXT NOT NULL, manifest_digest TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', runner TEXT NOT NULL DEFAULT '', duration_ms BIGINT NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_text TEXT NOT NULL DEFAULT '', PRIMARY KEY (scope, migration_id))`,
		`CREATE TABLE IF NOT EXISTS schema_migration_locks (scope TEXT NOT NULL PRIMARY KEY, runner TEXT NOT NULL, generation BIGINT NOT NULL DEFAULT 0, locked_at TEXT NOT NULL, expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS schema_migration_lock_generations (scope TEXT NOT NULL PRIMARY KEY, generation BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS schema_migration_attempts (scope TEXT NOT NULL, migration_id BIGINT NOT NULL, attempt BIGINT NOT NULL, action TEXT NOT NULL, state TEXT NOT NULL, owner_generation BIGINT NOT NULL DEFAULT 0, backup_evidence_ref TEXT NOT NULL DEFAULT '', recovery_evidence_ref TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', safe_error_class TEXT NOT NULL DEFAULT '', PRIMARY KEY (scope, migration_id, attempt), CONSTRAINT schema_migration_attempts_ledger_fk FOREIGN KEY (scope, migration_id) REFERENCES schema_migration_ledger(scope, migration_id) ON DELETE RESTRICT)`,
		`CREATE TABLE IF NOT EXISTS schema_data_jobs (job_id TEXT NOT NULL PRIMARY KEY, scope TEXT NOT NULL, migration_id BIGINT NOT NULL, data_version BIGINT NOT NULL, state TEXT NOT NULL, execution_mode TEXT NOT NULL, validation_mode TEXT NOT NULL, throttle_per_minute BIGINT NOT NULL DEFAULT 0, rows_scanned BIGINT NOT NULL DEFAULT 0, rows_updated BIGINT NOT NULL DEFAULT 0, rows_skipped BIGINT NOT NULL DEFAULT 0, rows_failed BIGINT NOT NULL DEFAULT 0, started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', cancel_requested_at TEXT NOT NULL DEFAULT '', safe_error_class TEXT NOT NULL DEFAULT '', CONSTRAINT schema_data_jobs_ledger_fk FOREIGN KEY (scope, migration_id) REFERENCES schema_migration_ledger(scope, migration_id) ON DELETE RESTRICT)`,
		`CREATE TABLE IF NOT EXISTS schema_data_job_checkpoints (job_id TEXT NOT NULL, checkpoint_ordinal BIGINT NOT NULL, shard TEXT NOT NULL DEFAULT '', range_start BIGINT NOT NULL DEFAULT 0, range_end BIGINT NOT NULL DEFAULT 0, cursor TEXT NOT NULL DEFAULT '', rows_scanned BIGINT NOT NULL DEFAULT 0, rows_updated BIGINT NOT NULL DEFAULT 0, rows_skipped BIGINT NOT NULL DEFAULT 0, rows_failed BIGINT NOT NULL DEFAULT 0, state TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', PRIMARY KEY (job_id, checkpoint_ordinal), CONSTRAINT schema_data_job_checkpoints_job_fk FOREIGN KEY (job_id) REFERENCES schema_data_jobs(job_id) ON DELETE RESTRICT)`,
	} {
		if err := r.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("migration ledger bootstrap: %w", err)
		}
	}
	if !r.db.Migrator().HasColumn(&migrationDataJobRecord{}, "cancel_requested_at") {
		if err := r.db.Migrator().AddColumn(&migrationDataJobRecord{}, "CancelRequestedAt"); err != nil {
			return fmt.Errorf("migration data-job cancellation column upgrade: %w", err)
		}
	}
	if !r.db.Migrator().HasColumn(&migrationLockRecord{}, "generation") {
		if err := r.db.Migrator().AddColumn(&migrationLockRecord{}, "Generation"); err != nil {
			return fmt.Errorf("migration lock generation upgrade: %w", err)
		}
	}
	// The first Stage-1 ledger release did not record the canonical manifest
	// digest. Existing ledgers must be upgraded before any Status query maps a
	// record into migrationLedgerRecord: SELECTing a missing column otherwise
	// turns a read-only status operation into a database error. AddColumn uses
	// the configured GORM dialect, so this remains valid for both SQLite and
	// PostgreSQL rather than relying on a SQLite-only ALTER TABLE variant.
	if !r.db.Migrator().HasColumn(&migrationLedgerRecord{}, "manifest_digest") {
		if err := r.db.Migrator().AddColumn(&migrationLedgerRecord{}, "ManifestDigest"); err != nil {
			return fmt.Errorf("migration ledger manifest digest upgrade: %w", err)
		}
	}
	if !r.db.Migrator().HasColumn(&migrationLedgerRecord{}, "manifest_digest") {
		return errors.New("migration ledger manifest digest upgrade did not create required column")
	}
	// Bind every adopted applied row to the canonical definition that exactly
	// matches its scope, ID, and immutable checksum.  A legacy row without a
	// digest is not trusted merely because its ID is known: a changed checksum
	// must remain visible as an incompatible state instead of being silently
	// adopted into the current manifest.
	known := make(map[int]MigrationDefinition, len(r.definitions))
	for _, d := range r.definitions {
		known[d.ID] = d
	}
	var legacyApplied []migrationLedgerRecord
	if err := r.db.Where("scope = ? AND state = ? AND manifest_digest = ?", r.scope, "applied", "").Find(&legacyApplied).Error; err != nil {
		return fmt.Errorf("migration ledger manifest digest read: %w", err)
	}
	for _, rec := range legacyApplied {
		d, ok := known[rec.MigrationID]
		if !ok || d.Checksum != rec.Checksum {
			continue // Status rejects the unbound row as incompatible.
		}
		if err := r.db.Model(&migrationLedgerRecord{}).
			Where("scope = ? AND migration_id = ? AND checksum = ? AND state = ? AND manifest_digest = ?", r.scope, rec.MigrationID, rec.Checksum, "applied", "").
			Update("manifest_digest", d.ManifestDigest).Error; err != nil {
			return fmt.Errorf("migration ledger manifest digest adoption: %w", err)
		}
	}
	return nil
}

func (r *migrationRunner) Status() (MigrationStatus, error) {
	if err := r.ensureLedger(); err != nil {
		return MigrationStatus{}, err
	}
	status := MigrationStatus{Scope: r.scope, Compatibility: r.compatibility, Compatible: true, State: "current"}
	var records []migrationLedgerRecord
	if err := r.db.Where("scope = ?", r.scope).Order("migration_id ASC").Find(&records).Error; err != nil {
		return status, err
	}
	known := make(map[int]MigrationDefinition, len(r.definitions))
	for _, d := range r.definitions {
		known[d.ID] = d
	}
	applied := map[int]bool{}
	for _, rec := range records {
		status.Entries = append(status.Entries, ledgerEntry(rec))
		d, ok := known[rec.MigrationID]
		if !ok || d.Checksum != rec.Checksum || !migrationManifestDigestMatches(d, rec.ManifestDigest) {
			status.Compatible = false
			status.State = "incompatible"
			continue
		}
		switch rec.State {
		case "applied":
			applied[rec.MigrationID] = true
			if d.SchemaVersion > status.SchemaVersion {
				status.SchemaVersion = d.SchemaVersion
			}
			if d.DataVersion > status.DataVersion && d.DataJobKey == "" {
				status.DataVersion = d.DataVersion
			}
		case "running":
			status.State = "in-progress"
		case "failed":
			status.State = "failed"
		default:
			status.Compatible = false
			status.State = "incompatible"
		}
	}
	if err := r.populateDataJobStatus(&status, applied); err != nil {
		return status, err
	}
	// An applied child cannot stand without every declared prerequisite having
	// an applied, compatible ledger entry.  This independently verifies the
	// historical ledger order; ApplyPending's preflight alone cannot detect a
	// damaged or manually edited ledger.
	for migrationID := range applied {
		for _, dependency := range known[migrationID].Dependencies {
			if !applied[dependency] {
				status.Compatible = false
				status.State = "incompatible"
			}
		}
	}
	if status.SchemaVersion < r.compatibility.MinSchema || status.SchemaVersion > r.compatibility.MaxSchema || status.DataVersion < r.compatibility.MinData || status.DataVersion > r.compatibility.MaxData {
		status.Compatible = false
		status.State = "incompatible"
	}
	for _, d := range r.definitions {
		if !applied[d.ID] {
			status.Pending = append(status.Pending, d)
		}
	}
	if status.State == "current" && len(status.Pending) > 0 {
		status.State = "pending"
	}
	return status, nil
}

func migrationManifestDigestMatches(d MigrationDefinition, digest string) bool {
	if d.ManifestDigest == digest {
		return true
	}
	for _, legacyDigest := range d.LegacyManifestDigests {
		if legacyDigest == digest {
			return true
		}
	}
	return false
}

// Verify rechecks the immutable ledger and postconditions of applied
// definitions. Pending definitions are reported but never executed.
func (r *migrationRunner) Verify() (MigrationStatus, error) {
	status, err := r.Status()
	if err != nil || !status.Compatible {
		return status, err
	}
	applied := make(map[int]bool, len(status.Entries))
	for _, entry := range status.Entries {
		if entry.State == "applied" {
			applied[entry.MigrationID] = true
		}
	}
	for _, d := range r.definitions {
		if applied[d.ID] && d.Verify != nil {
			if err := d.Verify(r.db); err != nil {
				return status, fmt.Errorf("migration %d verification: %w", d.ID, err)
			}
		}
	}
	return status, nil
}

func ledgerEntry(rec migrationLedgerRecord) MigrationLedgerEntry {
	started, _ := time.Parse(time.RFC3339Nano, rec.StartedAt)
	completed, _ := time.Parse(time.RFC3339Nano, rec.CompletedAt)
	return MigrationLedgerEntry{rec.Scope, rec.MigrationID, rec.Checksum, rec.State, started, completed, rec.Runner, rec.DurationMS, rec.ErrorCode, rec.ErrorText}
}

// ApplyPending executes only transactional, online definitions. It is safe for
// a serving-process startup policy because it stops before any definition that
// requires a maintenance window or a non-transactional operation.
func (r *migrationRunner) ApplyPending(runner string) error {
	return r.applyPending(runner, "online", "")
}

// ApplyMaintenancePending is an explicit non-serving maintenance operation.
// It executes only transactional definitions marked maintenance. Callers must
// first apply the online prefix and schedule the required maintenance window;
// non-transactional definitions (for example, a PostgreSQL concurrent index
// build) still require a dedicated non-transactional runner.
func (r *migrationRunner) ApplyMaintenancePending(runner string) error {
	return r.applyPending(runner, "maintenance", "")
}

// ApplyMaintenancePendingWithEvidence records the bounded backup reference
// alongside the applied attempt. The CLI uses this method for every
// maintenance action; the compatibility wrapper remains for existing callers.
func (r *migrationRunner) ApplyMaintenancePendingWithEvidence(runner, backupEvidenceRef string) error {
	if safeMigrationText(backupEvidenceRef) == "" {
		return errors.New("maintenance migration requires backup evidence reference")
	}
	return r.applyPending(runner, "maintenance", backupEvidenceRef)
}

// ApplyNonTransactionalMaintenancePending is the only route for a checked-in
// non-transactional maintenance definition (for example a PostgreSQL
// concurrent index build). It never runs from serving startup and leaves a
// durable running/failed/applied ledger trail around the independently
// committed operation.
func (r *migrationRunner) ApplyNonTransactionalMaintenancePending(runner string) error {
	return r.applyPending(runner, "non-transactional-maintenance", "")
}

// ApplyNonTransactionalMaintenancePendingWithEvidence is the audited variant
// used by router-migrate for non-transactional maintenance work.
func (r *migrationRunner) ApplyNonTransactionalMaintenancePendingWithEvidence(runner, backupEvidenceRef string) error {
	if safeMigrationText(backupEvidenceRef) == "" {
		return errors.New("non-transactional migration requires backup evidence reference")
	}
	return r.applyPending(runner, "non-transactional-maintenance", backupEvidenceRef)
}

func (r *migrationRunner) applyPending(runner, maintenanceMode, backupEvidenceRef string) error {
	status, err := r.Status()
	if err != nil {
		return err
	}
	if !status.Compatible {
		return errors.New("migration state is incompatible")
	}
	return r.withMigrationOwnership(runner, func(owned *migrationRunner) error {
		applied := make(map[int]bool, len(status.Entries))
		for _, entry := range status.Entries {
			if entry.State == "applied" {
				applied[entry.MigrationID] = true
			}
		}
		for _, d := range status.Pending {
			isNonTransactional := !d.Transactional || d.ExecutionMode == "non-transactional"
			allowed := d.MaintenanceMode == maintenanceMode
			if maintenanceMode == "non-transactional-maintenance" {
				allowed = d.MaintenanceMode == "maintenance" && isNonTransactional
			} else {
				allowed = allowed && !isNonTransactional
			}
			if !allowed {
				if maintenanceMode == "online" {
					return fmt.Errorf("migration %d requires explicit maintenance runner", d.ID)
				}
				return fmt.Errorf("migration %d requires its declared runner mode", d.ID)
			}
			if d.Apply == nil {
				return fmt.Errorf("migration %d has no apply step", d.ID)
			}
			for _, dependency := range d.Dependencies {
				if !applied[dependency] {
					return fmt.Errorf("migration %d requires applied dependency %d", d.ID, dependency)
				}
			}
			var applyErr error
			if isNonTransactional {
				applyErr = owned.applyOneNonTransactional(d, runner, backupEvidenceRef)
			} else {
				applyErr = owned.applyOne(d, runner, backupEvidenceRef)
			}
			if applyErr != nil {
				return applyErr
			}
			applied[d.ID] = true
		}
		return nil
	})
}

// withMigrationOwnership pins PostgreSQL advisory ownership to one physical
// connection. SQLite uses its single connection plus an exclusive-lock and
// integrity preflight. The durable lock row is still retained as safe audit
// evidence and as a fence for interrupted runners.
func (r *migrationRunner) withMigrationOwnership(runner string, fn func(*migrationRunner) error) error {
	if err := r.preflightMigrationOperation(); err != nil {
		return err
	}
	return r.db.Connection(func(conn *gorm.DB) (operationErr error) {
		owned := *r
		// Connection passes a clone-zero handle. Re-open a clean GORM session
		// over its pinned ConnPool so handled record-not-found reads do not
		// contaminate later checkpoint operations with a sticky DB error.
		owned.db = conn.Session(&gorm.Session{NewDB: true})
		if err := applyMigrationOwnershipTimeouts(owned.db); err != nil {
			return err
		}
		defer func() {
			if err := resetMigrationOwnershipTimeouts(owned.db); err != nil && operationErr == nil {
				operationErr = err
			}
		}()
		if owned.db.Dialector.Name() == "sqlite" {
			// Prove an exclusive maintenance lock can be acquired without
			// leaving connection-local locking mode behind for later work.
			if err := owned.db.Exec("BEGIN EXCLUSIVE").Error; err != nil {
				return errors.New("migration preflight sqlite exclusive lock failed")
			}
			if err := owned.db.Exec("ROLLBACK").Error; err != nil {
				return errors.New("migration preflight sqlite exclusive lock cleanup failed")
			}
		}
		if err := owned.acquireLock(runner); err != nil {
			return err
		}
		defer func() {
			if err := owned.releaseLock(runner); err != nil && operationErr == nil {
				operationErr = err
			}
		}()
		return fn(&owned)
	})
}

func (r *migrationRunner) preflightMigrationOperation() error {
	switch r.db.Dialector.Name() {
	case "postgres", "postgresql":
		var versionText string
		if err := r.db.Raw("SHOW server_version_num").Scan(&versionText).Error; err != nil {
			return errors.New("migration preflight could not verify postgres version")
		}
		version, err := strconv.Atoi(strings.TrimSpace(versionText))
		if err != nil || version < migrationPostgresMinimumVersion {
			return errors.New("migration preflight requires supported postgres version")
		}
	case "sqlite":
		var integrity string
		if err := r.db.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil || integrity != "ok" {
			return errors.New("migration preflight sqlite integrity check failed")
		}
		// The runner opens SQLite with one connection for its operation. An
		// exclusive transaction below is the maintenance-window proof; it is
		// deliberately not represented as multi-replica availability.
		if err := r.db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
			return errors.New("migration preflight sqlite busy timeout failed")
		}
	default:
		return errors.New("migration preflight unsupported database driver")
	}
	return nil
}

// applyMigrationOwnershipTimeouts configures the pinned PostgreSQL session
// before advisory ownership or fenced generation/lease DML. SET LOCAL would
// expire before acquisition's separate transactions, so ownership uses
// session settings that are reset before the connection returns to the pool.
func applyMigrationOwnershipTimeouts(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "postgresql" {
		return nil
	}
	if err := setMigrationTimeouts(db, false); err != nil {
		return errors.New("migration ownership timeout setup failed")
	}
	return nil
}

func resetMigrationOwnershipTimeouts(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "postgresql" {
		return nil
	}
	if err := db.Exec("RESET lock_timeout").Error; err != nil {
		return errors.New("migration ownership timeout cleanup failed")
	}
	if err := db.Exec("RESET statement_timeout").Error; err != nil {
		return errors.New("migration ownership timeout cleanup failed")
	}
	return nil
}

// acquireLock is the durable single-runner guard. PostgreSQL additionally
// takes a session advisory lock; the caller must use withMigrationOwnership so
// acquire, execution, and release remain on the same physical connection.
func (r *migrationRunner) acquireLock(runner string) error {
	if r.db.Dialector.Name() == "postgres" || r.db.Dialector.Name() == "postgresql" {
		var acquired bool
		conn, err := r.postgresAdvisorySession()
		if err != nil || conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock(hashtext($1))", r.scope).Scan(&acquired) != nil || !acquired {
			return errors.New("migration runner is already active for this scope")
		}
		r.advisorySession = conn
	}
	now := time.Now().UTC()
	if err := r.db.Session(&gorm.Session{NewDB: true}).Where("scope = ? AND expires_at < ?", r.scope, now.Format(time.RFC3339Nano)).Delete(&migrationLockRecord{}).Error; err != nil {
		r.releaseAdvisoryLock()
		return errors.New("migration lock cleanup failed")
	}
	var generation int64
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO schema_migration_lock_generations (scope, generation) VALUES (?, 1) ON CONFLICT(scope) DO UPDATE SET generation = schema_migration_lock_generations.generation + 1`, r.scope).Error; err != nil {
			return err
		}
		return tx.Raw("SELECT generation FROM schema_migration_lock_generations WHERE scope = ?", r.scope).Scan(&generation).Error
	}); err != nil || generation <= 0 {
		r.releaseAdvisoryLock()
		if err != nil && safeMigrationErrorCode(err) == "migration-timeout" {
			return errors.New("migration ownership acquisition timed out")
		}
		return errors.New("migration lock generation failed")
	}
	lock := migrationLockRecord{Scope: r.scope, Runner: safeMigrationText(runner), Generation: generation, LockedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339Nano)}
	if err := r.db.Session(&gorm.Session{NewDB: true}).Create(&lock).Error; err != nil {
		r.releaseAdvisoryLock()
		return errors.New("migration runner is already active for this scope")
	}
	r.ownerGeneration = generation
	return nil
}

func (r *migrationRunner) releaseLock(runner string) error {
	var releaseErr error
	if err := r.db.Session(&gorm.Session{NewDB: true}).Where("scope = ? AND runner = ? AND generation = ?", r.scope, safeMigrationText(runner), r.ownerGeneration).Delete(&migrationLockRecord{}).Error; err != nil {
		releaseErr = errors.New("migration lock release failed")
	}
	if err := r.releaseAdvisoryLock(); err != nil && releaseErr == nil {
		releaseErr = errors.New("migration advisory lock release failed")
	}
	return releaseErr
}

func (r *migrationRunner) releaseAdvisoryLock() error {
	if r.db.Dialector.Name() == "postgres" || r.db.Dialector.Name() == "postgresql" {
		conn, err := r.postgresAdvisorySession()
		if err != nil {
			return err
		}
		var released bool
		if err := conn.QueryRowContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", r.scope).Scan(&released); err != nil {
			return err
		}
		if !released {
			return errors.New("migration advisory lock was not held by release session")
		}
	}
	return nil
}

// postgresAdvisorySession exposes the physical connection installed by
// withMigrationOwnership. Session advisory locks cannot safely fall back to a
// pooled GORM handle: a different backend could release nothing while the
// original backend retains the lock.
func (r *migrationRunner) postgresAdvisorySession() (*sql.Conn, error) {
	if r.advisorySession != nil {
		return r.advisorySession, nil
	}
	conn, ok := r.db.Statement.ConnPool.(*sql.Conn)
	if !ok || conn == nil {
		return nil, errors.New("migration advisory ownership requires pinned postgres connection")
	}
	return conn, nil
}

func (r *migrationRunner) applyOne(d MigrationDefinition, runner, backupEvidenceRef string) error {
	now := time.Now().UTC()
	started := now.Format(time.RFC3339Nano)
	err := r.db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := applyMigrationTimeouts(tx); err != nil {
			return err
		}
		rec := migrationLedgerRecord{Scope: r.scope, MigrationID: d.ID, Checksum: d.Checksum, ManifestDigest: d.ManifestDigest, State: "running", StartedAt: started, Runner: safeMigrationText(runner)}
		if err := tx.Create(&rec).Error; err != nil {
			return errors.New("migration already running or applied")
		}
		if err := d.Apply(tx); err != nil {
			return err
		}
		if d.Verify != nil {
			if err := d.Verify(tx); err != nil {
				return err
			}
		}
		// The applied ledger state and its normalized attempt are one
		// transaction. In particular, a maintenance backup reference must not
		// become an after-the-fact receipt that a crash can omit.
		if err := r.recordAttemptTx(tx, d, runner, "apply", "applied", backupEvidenceRef, "", "", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return errors.New("migration applied audit record failed")
		}
		return tx.Model(&migrationLedgerRecord{}).Where("scope = ? AND migration_id = ?", r.scope, d.ID).Updates(map[string]any{"state": "applied", "completed_at": time.Now().UTC().Format(time.RFC3339Nano), "duration_ms": time.Since(now).Milliseconds(), "error_code": "", "error_text": ""}).Error
	})
	if err != nil {
		return r.recordFailure(d, runner, "apply", backupEvidenceRef, err)
	}
	return nil
}

func applyMigrationTimeouts(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "postgresql" {
		return nil
	}
	if err := setMigrationTimeouts(db, true); err != nil {
		return errors.New("migration lock timeout setup failed")
	}
	return nil
}

func setMigrationTimeouts(db *gorm.DB, local bool) error {
	scope := "false"
	if local {
		scope = "true"
	}
	if err := db.Exec("SELECT set_config('lock_timeout', ?, "+scope+")", migrationLockTimeout.String()).Error; err != nil {
		return err
	}
	return db.Exec("SELECT set_config('statement_timeout', ?, "+scope+")", migrationStatementTimeout.String()).Error
}

// applyOneNonTransactional models operations that PostgreSQL cannot execute
// inside a transaction. The durable running row is committed before work
// begins; success is recorded only after the postcondition verifies. A crash
// therefore remains visibly running for an explicit operator recovery rather
// than being mistaken for an atomic rollback.
func (r *migrationRunner) applyOneNonTransactional(d MigrationDefinition, runner, backupEvidenceRef string) error {
	now := time.Now().UTC()
	if err := r.db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := applyMigrationTimeouts(tx); err != nil {
			return err
		}
		if err := tx.Create(&migrationLedgerRecord{Scope: r.scope, MigrationID: d.ID, Checksum: d.Checksum, ManifestDigest: d.ManifestDigest, State: "running", StartedAt: now.Format(time.RFC3339Nano), Runner: safeMigrationText(runner)}).Error; err != nil {
			return err
		}
		// A crash after independently committed work leaves durable running
		// state plus its bounded recovery evidence, never an invented applied
		// receipt.
		return r.recordAttemptTx(tx, d, runner, "apply", "running", backupEvidenceRef, "", "", now.Format(time.RFC3339Nano))
	}); err != nil {
		return r.recordFailure(d, runner, "non-transactional-start", backupEvidenceRef, err)
	}
	if err := d.Apply(r.db); err != nil {
		return r.recordFailure(d, runner, "non-transactional-apply", backupEvidenceRef, err)
	}
	if d.Verify != nil {
		if err := d.Verify(r.db); err != nil {
			return r.recordFailure(d, runner, "non-transactional-verify", backupEvidenceRef, err)
		}
	}
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := r.recordAttemptTx(tx, d, runner, "apply", "applied", backupEvidenceRef, "", "", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return tx.Model(&migrationLedgerRecord{}).Where("scope = ? AND migration_id = ? AND state = ?", r.scope, d.ID, "running").Updates(map[string]any{"state": "applied", "completed_at": time.Now().UTC().Format(time.RFC3339Nano), "duration_ms": time.Since(now).Milliseconds()}).Error
	}); err != nil {
		return r.recordFailure(d, runner, "non-transactional-completion", backupEvidenceRef, err)
	}
	return nil
}

// recordFailure deliberately runs after an atomic migration transaction has
// rolled back. It gives every scope a fenced, scalar recovery record without
// retaining the original database error or any SQL/input content.
func (r *migrationRunner) recordFailure(d MigrationDefinition, runner, action, backupEvidenceRef string, cause error) error {
	code := safeMigrationErrorCode(cause)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var rec migrationLedgerRecord
		err := tx.Where("scope = ? AND migration_id = ?", r.scope, d.ID).First(&rec).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rec = migrationLedgerRecord{Scope: r.scope, MigrationID: d.ID, Checksum: d.Checksum, ManifestDigest: d.ManifestDigest, State: "failed", StartedAt: now, CompletedAt: now, Runner: safeMigrationText(runner), ErrorCode: code, ErrorText: code}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if rec.State == "applied" || rec.Checksum != d.Checksum || !migrationManifestDigestMatches(d, rec.ManifestDigest) {
			return errors.New("migration failure record is fenced")
		} else if err := tx.Model(&migrationLedgerRecord{}).Where("scope = ? AND migration_id = ? AND state = ?", r.scope, d.ID, rec.State).Updates(map[string]any{"state": "failed", "completed_at": now, "error_code": code, "error_text": code}).Error; err != nil {
			return err
		}
		return r.recordAttemptTx(tx, d, runner, action, "failed", backupEvidenceRef, "", code, now)
	}); err != nil {
		return fmt.Errorf("migration failed and durable recovery record could not be written: %w", err)
	}
	return fmt.Errorf("migration %d failed (%s)", d.ID, code)
}

func (r *migrationRunner) recordAttempt(d MigrationDefinition, runner, action, state, backupEvidenceRef, recoveryEvidenceRef, errorClass string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if r.db.Dialector.Name() == "sqlite" {
		// The exclusive SQLite connection already serializes this one scalar
		// insert. Starting a second transaction after an exclusive operation
		// can ask the driver pool for another writer and self-deadlock.
		return r.recordAttemptTx(r.db, d, runner, action, state, backupEvidenceRef, recoveryEvidenceRef, errorClass, now)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		return r.recordAttemptTx(tx, d, runner, action, state, backupEvidenceRef, recoveryEvidenceRef, errorClass, now)
	})
}

func (r *migrationRunner) recordAttemptTx(tx *gorm.DB, d MigrationDefinition, runner, action, state, backupEvidenceRef, recoveryEvidenceRef, errorClass, now string) error {
	clean := tx.Session(&gorm.Session{NewDB: true})
	var next int64
	if err := clean.Raw("SELECT COALESCE(MAX(attempt), 0) + 1 FROM schema_migration_attempts WHERE scope = ? AND migration_id = ?", r.scope, d.ID).Scan(&next).Error; err != nil {
		return err
	}
	return clean.Create(&migrationAttemptRecord{Scope: r.scope, MigrationID: d.ID, Attempt: int(next), Action: safeMigrationText(action), State: safeMigrationText(state), OwnerGeneration: r.ownerGeneration, BackupEvidenceRef: safeMigrationText(backupEvidenceRef), RecoveryEvidenceRef: safeMigrationText(recoveryEvidenceRef), StartedAt: now, CompletedAt: now, SafeErrorClass: safeMigrationText(errorClass)}).Error
}

func safeMigrationErrorCode(err error) string {
	if err == nil {
		return "migration-failed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout") || strings.Contains(message, "canceling statement"):
		return "migration-timeout"
	case strings.Contains(message, "locked") || strings.Contains(message, "busy"):
		return "migration-lock-conflict"
	case strings.Contains(message, "verification"):
		return "migration-verification-failed"
	default:
		return "migration-apply-failed"
	}
}

func safeMigrationText(v string) string {
	v = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return -1
	}, v)
	if len(v) > 120 {
		return v[:120]
	}
	return v
}

// UsageMigrationRunner opens the configured persistent usage scope without
// invoking the legacy usage-store initializer, so plan/status are non-serving
// and do not silently alter application tables.
func UsageMigrationRunner(cfg UsageDBConfig) (*migrationRunner, func() error, error) {
	db, err := openUsageDB(cfg)
	if err != nil {
		return nil, nil, err
	}
	r, err := newUsageMigrationRunner(db)
	if err != nil {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		return nil, nil, err
	}
	closeFn := func() error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return r, closeFn, nil
}

func newUsageMigrationRunner(db *gorm.DB) (*migrationRunner, error) {
	return NewMigrationRunnerWithDataJobs(db, usageMigrationScope, usageMigrationCompatibility, usageMigrationDefinitions, usageDataJobDefinitions)
}
