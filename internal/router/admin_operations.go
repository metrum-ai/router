// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

const adminOperationsTable = "router_admin_operations"
const adminOperationsMigrationID = 2026111606

const (
	adminOperationPending              = "pending"
	adminOperationRunning              = "running"
	adminOperationSucceeded            = "succeeded"
	adminOperationFailed               = "failed"
	adminOperationTTL                  = 7 * 24 * time.Hour
	adminOperationTimeout              = 2 * time.Minute
	adminOperationStaleTimeout         = 5 * time.Minute
	adminOperationLimit                = 500
	maxAdminOperationBodyBytes         = 32 << 20
	maxAdminOperationResultBytes       = 31 << 20
	maxAdminOperationStoredResultBytes = 64 << 20
	maxAdminBackupDatabaseBytes        = 15 << 20
)

type controlPlaneBackupCell struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type controlPlaneBackupTable struct {
	Name string                              `json:"name"`
	Rows []map[string]controlPlaneBackupCell `json:"rows"`
}

type controlPlaneBackupDatabase struct {
	Tables []controlPlaneBackupTable `json:"tables"`
}

type controlPlaneBackupBundle struct {
	Schema    string                     `json:"schema"`
	CreatedAt string                     `json:"created_at"`
	Config    controlPlaneBackupDatabase `json:"config"`
	Usage     controlPlaneBackupDatabase `json:"usage"`
	SHA256    string                     `json:"sha256"`
}

var controlPlaneConfigBackupTables = []string{
	"router_config_sets", "router_config_documents", "router_config_server",
	"router_config_providers", "router_config_provider_headers", "router_config_provider_models",
	"router_config_provider_model_capabilities", "router_config_provider_model_tool_support",
	"router_config_provider_model_modalities", "router_config_provider_model_request_inbound_dialects",
	"router_config_provider_model_request_unsupported_features", "router_config_provider_model_bridges",
	"router_config_model_groups", "router_config_model_group_targets", "router_config_callers",
	"router_config_caller_allowed_groups", "router_config_runtime", "router_config_change_events",
	"router_config_change_event_targets", "router_config_shadow_runs", "router_revoked_caller_tokens",
	adminClientTable, adminClientCredentialTable,
	adminEmergencyActivationTable,
	adminKeyMetadataTable, adminKeyGenerationTable, adminKeyOperationTable, adminCredentialEventsTable,
	adminCredentialAuditCounterTable,
	adminAuditChainAnchorsTable,
}

var controlPlaneUsageBackupTables = []string{
	"authz_policy_sets", "authz_policy_rules", "authz_role_links", "authz_policy_runtime", "authz_policy_audit_events",
	usageAdminAuditTable,
	adminCredentialAuditCounterTable,
	adminAuditChainAnchorsTable,
}

func createControlPlaneBackup(ctx context.Context, configDB, usageDB *gorm.DB, now time.Time) (controlPlaneBackupBundle, error) {
	if configDB == nil || usageDB == nil {
		return controlPlaneBackupBundle{}, errors.New("both control-plane stores are required")
	}
	bundle := controlPlaneBackupBundle{Schema: "metrum.ai/router-control-plane-backup/v1", CreatedAt: now.UTC().Format(time.RFC3339Nano)}
	var err error
	if bundle.Config, err = snapshotControlPlaneDatabase(ctx, configDB, controlPlaneConfigBackupTables); err != nil {
		return controlPlaneBackupBundle{}, err
	}
	if bundle.Usage, err = snapshotControlPlaneDatabase(ctx, usageDB, controlPlaneUsageBackupTables); err != nil {
		return controlPlaneBackupBundle{}, err
	}
	digest, err := controlPlaneBackupDigest(bundle)
	if err != nil {
		return controlPlaneBackupBundle{}, err
	}
	bundle.SHA256 = digest
	return bundle, nil
}

func snapshotControlPlaneDatabase(ctx context.Context, db *gorm.DB, names []string) (controlPlaneBackupDatabase, error) {
	out := controlPlaneBackupDatabase{Tables: make([]controlPlaneBackupTable, 0, len(names))}
	var encodedBytes int
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return controlPlaneBackupDatabase{}, err
		}
		table := controlPlaneBackupTable{Name: name, Rows: []map[string]controlPlaneBackupCell{}}
		if !db.Migrator().HasTable(name) {
			continue
		}
		rows, err := db.Table(name).Rows()
		if err != nil {
			return controlPlaneBackupDatabase{}, err
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return controlPlaneBackupDatabase{}, err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				_ = rows.Close()
				return controlPlaneBackupDatabase{}, err
			}
			row := make(map[string]controlPlaneBackupCell, len(columns))
			for i, column := range columns {
				cell, err := encodeControlPlaneCell(values[i])
				if err != nil {
					_ = rows.Close()
					return controlPlaneBackupDatabase{}, fmt.Errorf("encode %s.%s: %w", name, column, err)
				}
				row[column] = cell
			}
			encodedRow, err := json.Marshal(row)
			if err != nil {
				_ = rows.Close()
				return controlPlaneBackupDatabase{}, err
			}
			encodedBytes += len(encodedRow)
			if encodedBytes > maxAdminBackupDatabaseBytes {
				_ = rows.Close()
				return controlPlaneBackupDatabase{}, errors.New("control-plane backup exceeds the bounded database snapshot size")
			}
			table.Rows = append(table.Rows, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return controlPlaneBackupDatabase{}, err
		}
		if err := rows.Close(); err != nil {
			return controlPlaneBackupDatabase{}, err
		}
		out.Tables = append(out.Tables, table)
	}
	return out, nil
}

func encodeControlPlaneCell(value any) (controlPlaneBackupCell, error) {
	cell := func(kind string, value any) (controlPlaneBackupCell, error) {
		encoded, err := json.Marshal(value)
		return controlPlaneBackupCell{Type: kind, Value: encoded}, err
	}
	switch v := value.(type) {
	case nil:
		return cell("null", nil)
	case int64:
		return cell("int64", v)
	case int:
		return cell("int64", int64(v))
	case float64:
		return cell("float64", v)
	case bool:
		return cell("bool", v)
	case string:
		return cell("string", v)
	case []byte:
		return cell("bytes", base64.StdEncoding.EncodeToString(v))
	case time.Time:
		return cell("time", v.UTC().Format(time.RFC3339Nano))
	default:
		return controlPlaneBackupCell{}, fmt.Errorf("unsupported stored type %T", value)
	}
}

func decodeControlPlaneCell(cell controlPlaneBackupCell) (any, error) {
	switch cell.Type {
	case "null":
		return nil, nil
	case "int64":
		var value int64
		err := json.Unmarshal(cell.Value, &value)
		return value, err
	case "float64":
		var value float64
		err := json.Unmarshal(cell.Value, &value)
		return value, err
	case "bool":
		var value bool
		err := json.Unmarshal(cell.Value, &value)
		return value, err
	case "string":
		var value string
		err := json.Unmarshal(cell.Value, &value)
		return value, err
	case "bytes":
		var encoded string
		if err := json.Unmarshal(cell.Value, &encoded); err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(encoded)
	case "time":
		var value string
		err := json.Unmarshal(cell.Value, &value)
		if err != nil {
			return nil, err
		}
		return time.Parse(time.RFC3339Nano, value)
	default:
		return nil, fmt.Errorf("unsupported backup cell type %q", cell.Type)
	}
}

func controlPlaneBackupDigest(bundle controlPlaneBackupBundle) (string, error) {
	bundle.SHA256 = ""
	encoded, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateControlPlaneBackup(bundle controlPlaneBackupBundle) error {
	if bundle.Schema != "metrum.ai/router-control-plane-backup/v1" || strings.TrimSpace(bundle.CreatedAt) == "" || len(bundle.SHA256) != 64 {
		return errors.New("backup bundle header is invalid")
	}
	digest, err := controlPlaneBackupDigest(bundle)
	if err != nil || digest != bundle.SHA256 {
		return errors.New("backup bundle integrity check failed")
	}
	if err := validateBackupTables(bundle.Config, controlPlaneConfigBackupTables); err != nil {
		return err
	}
	return validateBackupTables(bundle.Usage, controlPlaneUsageBackupTables)
}

func validateBackupTables(database controlPlaneBackupDatabase, allowlist []string) error {
	allowed := map[string]bool{}
	for _, name := range allowlist {
		allowed[name] = true
	}
	seen := map[string]bool{}
	for _, table := range database.Tables {
		if !allowed[table.Name] || seen[table.Name] {
			return fmt.Errorf("backup contains an unsupported or duplicate table %q", table.Name)
		}
		seen[table.Name] = true
		for _, row := range table.Rows {
			for _, cell := range row {
				if _, err := decodeControlPlaneCell(cell); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func restoreControlPlaneBackup(ctx context.Context, configDB, usageDB *gorm.DB, bundle controlPlaneBackupBundle) error {
	return restoreControlPlaneBackupWithAudit(ctx, configDB, usageDB, bundle, nil)
}

func restoreControlPlaneBackupWithAudit(ctx context.Context, configDB, usageDB *gorm.DB, bundle controlPlaneBackupBundle, audit *AdminAuditRecord) error {
	if configDB == nil || usageDB == nil {
		return errors.New("both control-plane stores are required")
	}
	if err := validateControlPlaneBackup(bundle); err != nil {
		if auditErr := recordOperationAudit(usageDB, audit, "failed", "invalid_backup"); auditErr != nil {
			return errors.New("restore failed and its audit record could not be written")
		}
		return err
	}
	if err := checkBackupTargetClean(configDB, bundle.Config); err != nil {
		if auditErr := recordOperationAudit(usageDB, audit, "failed", "restore_target_not_clean"); auditErr != nil {
			return errors.New("restore failed and its audit record could not be written")
		}
		return err
	}
	if err := checkBackupTargetClean(usageDB, bundle.Usage); err != nil {
		if auditErr := recordOperationAudit(usageDB, audit, "failed", "restore_target_not_clean"); auditErr != nil {
			return errors.New("restore failed and its audit record could not be written")
		}
		return err
	}
	if err := restoreBackupDatabase(ctx, configDB, bundle.Config, controlPlaneConfigBackupTables, false); err != nil {
		if auditErr := recordOperationAudit(usageDB, audit, "failed", "restore_failed"); auditErr != nil {
			return errors.New("restore failed and its audit record could not be written")
		}
		return err
	}
	if err := restoreBackupDatabaseWithAudit(ctx, usageDB, bundle.Usage, controlPlaneUsageBackupTables, false, audit); err != nil {
		// Restore spans two databases and is not a distributed transaction. If the
		// usage-side transaction fails, remove the config-side rows before returning.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := restoreBackupDatabase(cleanupCtx, configDB, bundle.Config, controlPlaneConfigBackupTables, true); cleanupErr != nil {
			return fmt.Errorf("usage restore failed (%v) and config cleanup failed: %w", err, cleanupErr)
		}
		if auditErr := recordOperationAudit(usageDB, audit, "failed", "restore_failed"); auditErr != nil {
			return errors.New("restore failed and its audit record could not be written")
		}
		return err
	}
	return nil
}

func checkBackupTargetClean(db *gorm.DB, backup controlPlaneBackupDatabase) error {
	for _, table := range backup.Tables {
		// The authenticated restore request records its login before the worker
		// starts. That row is operational evidence, not restored control-plane
		// state, and must not make an otherwise fresh replica dirty.
		if table.Name == adminCredentialEventsTable {
			continue
		}
		if table.Name == adminAuditChainAnchorsTable {
			if !db.Migrator().HasTable(table.Name) {
				return fmt.Errorf("restore target table %q is not migrated", table.Name)
			}
			continue
		}
		if table.Name == "authz_policy_runtime" {
			// A freshly initialized usage store has exactly one empty singleton
			// row. It carries no policy state and is replaced by the backup below.
			var active int64
			if err := db.Table(table.Name).Where("revision != 0 OR config_revision != 0 OR active_set_id != ''").Count(&active).Error; err != nil {
				return err
			}
			if active != 0 {
				return errors.New("restore target must be clean")
			}
			continue
		}
		if !db.Migrator().HasTable(table.Name) {
			return fmt.Errorf("restore target table %q is not migrated", table.Name)
		}
		var count int64
		if err := db.Table(table.Name).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errors.New("restore target must be clean")
		}
	}
	return nil
}

func restoreBackupDatabase(ctx context.Context, db *gorm.DB, backup controlPlaneBackupDatabase, allowlist []string, clear bool) error {
	return restoreBackupDatabaseWithAudit(ctx, db, backup, allowlist, clear, nil)
}

func restoreBackupDatabaseWithAudit(ctx context.Context, db *gorm.DB, backup controlPlaneBackupDatabase, allowlist []string, clear bool, audit *AdminAuditRecord) error {
	allowed := map[string]bool{}
	for _, name := range allowlist {
		allowed[name] = true
	}
	tables := append([]controlPlaneBackupTable(nil), backup.Tables...)
	if clear {
		for left, right := 0, len(tables)-1; left < right; left, right = left+1, right-1 {
			tables[left], tables[right] = tables[right], tables[left]
		}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if clear {
			for _, table := range tables {
				if allowed[table.Name] {
					if err := tx.Exec("DELETE FROM " + table.Name).Error; err != nil {
						return err
					}
				}
			}
			return nil
		}
		if !clear {
			hasAnchors := false
			for _, table := range tables {
				if table.Name == adminAuditChainAnchorsTable {
					hasAnchors = true
				}
				if table.Name == "authz_policy_runtime" {
					if err := tx.Exec("DELETE FROM authz_policy_runtime WHERE id = 1 AND revision = 0 AND config_revision = 0 AND active_set_id = ''").Error; err != nil {
						return err
					}
					break
				}
			}
			if hasAnchors {
				// The restore request's own login event was recorded on the fresh
				// target before this worker began. Its durable operation audit is
				// written in the usage store below; replace target credential rows
				// with the backup chain so restored rows and anchors stay coherent.
				if allowed[adminCredentialEventsTable] {
					if err := tx.Exec("DELETE FROM " + adminCredentialEventsTable).Error; err != nil {
						return err
					}
				}
				if err := tx.Exec("DELETE FROM " + adminAuditChainAnchorsTable).Error; err != nil {
					return err
				}
			}
		}
		for _, table := range tables {
			if !allowed[table.Name] {
				return errors.New("backup contains an unsupported table")
			}
			for _, row := range table.Rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				values := make(map[string]any, len(row))
				for column, cell := range row {
					if !validSQLIdentifier(column) {
						return errors.New("backup contains an invalid column")
					}
					value, err := decodeControlPlaneCell(cell)
					if err != nil {
						return err
					}
					values[column] = value
				}
				if len(values) == 0 {
					return errors.New("backup contains an empty row")
				}
				if err := tx.Table(table.Name).Create(values).Error; err != nil {
					return err
				}
			}
		}
		if audit != nil {
			if err := insertAdminAuditUsageTx(tx, *audit); err != nil {
				return err
			}
		}
		return nil
	})
}

func recordOperationAudit(db *gorm.DB, record *AdminAuditRecord, outcome, reason string) error {
	if record == nil {
		return nil
	}
	copy := *record
	copy.At = time.Now().UTC()
	copy.Outcome = outcome
	copy.ReasonCode = reason
	return db.Transaction(func(tx *gorm.DB) error { return insertAdminAuditUsageTx(tx, copy) })
}

func validSQLIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_') {
			return false
		}
	}
	return true
}

type adminOperationRow struct {
	ID        string `gorm:"primaryKey;column:id" json:"id"`
	Kind      string `gorm:"column:kind;not null" json:"kind"`
	Status    string `gorm:"column:status;not null;index" json:"status"`
	Result    string `gorm:"column:result;not null;default:''" json:"-"`
	ErrorCode string `gorm:"column:error_code;not null;default:''" json:"error_code,omitempty"`
	CreatedAt string `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt string `gorm:"column:updated_at;not null" json:"updated_at"`
	ExpiresAt string `gorm:"column:expires_at;not null;index" json:"-"`
}

func (adminOperationRow) TableName() string { return adminOperationsTable }

type adminOperationResult struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func adminOperationsMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: adminOperationsMigrationID, Scope: configControlPlaneScope,
		Name: "durable asynchronous admin operations", Release: "2026.11",
		Checksum: MigrationChecksum("router-config.admin-operations.v1"), SchemaVersion: 16,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "router-config.phase16.apply.v1@applyAdminOperationsMigration",
		PostconditionKey: "router-config.phase16.schema.v1@verifyAdminOperationsMigration",
		Dependencies:     []int{callerTokenRevocationMigrationID}, ExecutionMode: "transactional",
		LockClass: "online", TimeoutClass: "bounded",
		Apply: applyAdminOperationsMigration, Verify: verifyAdminOperationsMigration,
	})
}

func applyAdminOperationsMigration(db *gorm.DB) error {
	if err := db.Exec("CREATE TABLE IF NOT EXISTS " + adminOperationsTable + " (id TEXT PRIMARY KEY, kind TEXT NOT NULL, status TEXT NOT NULL, result TEXT NOT NULL DEFAULT '', error_code TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, expires_at TEXT NOT NULL)").Error; err != nil {
		return fmt.Errorf("create admin operations table: %w", err)
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS router_admin_operations_status ON " + adminOperationsTable + " (status)").Error; err != nil {
		return fmt.Errorf("index admin operations state: %w", err)
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS router_admin_operations_expiry ON " + adminOperationsTable + " (expires_at)").Error; err != nil {
		return fmt.Errorf("index admin operations expiry: %w", err)
	}
	return verifyAdminOperationsMigration(db)
}

func verifyAdminOperationsMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(adminOperationsTable) {
		return errors.New("required admin operations table is missing")
	}
	for _, column := range []string{"id", "kind", "status", "result", "error_code", "created_at", "updated_at", "expires_at"} {
		if !db.Migrator().HasColumn(&adminOperationRow{}, column) {
			return fmt.Errorf("required admin operation column %s is missing", column)
		}
	}
	return nil
}

func newAdminOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func pruneAdminOperations(db *gorm.DB, now time.Time) error {
	if db == nil {
		return errors.New("admin operation store is unavailable")
	}
	if err := db.Where("expires_at <= ?", now.UTC().Format(time.RFC3339Nano)).Delete(&adminOperationRow{}).Error; err != nil {
		return err
	}
	var rows []adminOperationRow
	if err := db.Order("created_at DESC, id DESC").Limit(adminOperationLimit + 1).Find(&rows).Error; err != nil {
		return err
	}
	var stored int64
	for i, row := range rows {
		stored += int64(len(row.Result))
		if i >= adminOperationLimit || stored > maxAdminOperationStoredResultBytes {
			if err := db.Delete(&row).Error; err != nil {
				return err
			}
			stored -= int64(len(row.Result))
		}
	}
	return nil
}

// startAdminOperation persists pending before returning, then records either a
// structured success payload or a stable error code. Raw errors never enter the
// operation table or API response.
func startAdminOperation(db *gorm.DB, kind string, now time.Time, work func(context.Context) (any, error)) (adminOperationRow, error) {
	return startAdminOperationWithTimeout(db, kind, now, adminOperationTimeout, work)
}

func startAdminOperationWithTimeout(db *gorm.DB, kind string, now time.Time, timeout time.Duration, work func(context.Context) (any, error)) (adminOperationRow, error) {
	if db == nil || work == nil {
		return adminOperationRow{}, errors.New("admin operation dependencies are unavailable")
	}
	if err := pruneAdminOperations(db, now); err != nil {
		return adminOperationRow{}, err
	}
	id, err := newAdminOperationID()
	if err != nil {
		return adminOperationRow{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	row := adminOperationRow{ID: id, Kind: kind, Status: adminOperationPending, CreatedAt: stamp, UpdatedAt: stamp, ExpiresAt: now.Add(adminOperationTTL).UTC().Format(time.RFC3339Nano)}
	if err := db.Create(&row).Error; err != nil {
		return adminOperationRow{}, err
	}
	go func() {
		_ = db.Model(&adminOperationRow{}).Where("id = ? AND status = ?", id, adminOperationPending).Updates(map[string]any{"status": adminOperationRunning, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}).Error
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		result, workErr := work(ctx)
		updated := map[string]any{"updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
		if workErr != nil {
			updated["status"] = adminOperationFailed
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				updated["error_code"] = "operation_timeout"
			} else {
				updated["error_code"] = "operation_failed"
			}
		} else {
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil || len(encoded) > maxAdminOperationResultBytes {
				updated["status"] = adminOperationFailed
				updated["error_code"] = "operation_failed"
			} else {
				updated["status"] = adminOperationSucceeded
				updated["result"] = string(encoded)
			}
		}
		if workErr == nil {
			_ = db.Model(&adminOperationRow{}).Where("id = ? AND (status = ? OR (status = ? AND error_code = ?))", id, adminOperationRunning, adminOperationFailed, "operation_timeout").Updates(updated).Error
		} else {
			_ = db.Model(&adminOperationRow{}).Where("id = ? AND status = ?", id, adminOperationRunning).Updates(updated).Error
		}
		_ = pruneAdminOperations(db, time.Now().UTC())
	}()
	return row, nil
}

func writeAdminOperation(w http.ResponseWriter, row adminOperationRow) {
	var result json.RawMessage
	if row.Kind != "control-plane-backup" && strings.TrimSpace(row.Result) != "" {
		result = json.RawMessage(row.Result)
	}
	writeJSON(w, http.StatusOK, adminOperationResult{ID: row.ID, Kind: row.Kind, Status: row.Status, Result: result, ErrorCode: row.ErrorCode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func adminBackupResultHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "admin-operation-store-unavailable", "operation store is unavailable")
			return
		}
		var row adminOperationRow
		if err := db.Where("id = ? AND kind = ?", r.PathValue("operation_id"), "control-plane-backup").First(&row).Error; err != nil {
			writeAdminProblemCode(w, r, http.StatusNotFound, "admin-operation-not-found", "backup operation was not found")
			return
		}
		if row.Status != adminOperationSucceeded || strings.TrimSpace(row.Result) == "" {
			writeAdminProblemCode(w, r, http.StatusConflict, "admin-backup-not-ready", "backup operation has no completed bundle")
			return
		}
		writeJSON(w, http.StatusOK, map[string]json.RawMessage{"bundle": json.RawMessage(row.Result)})
	}
}

func adminOperationResourceHandler(db *gorm.DB, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "admin-operation-store-unavailable", "operation store is unavailable")
			return
		}
		var row adminOperationRow
		if err := db.Where("id = ?", r.PathValue("operation_id")).First(&row).Error; err != nil {
			writeAdminProblemCode(w, r, http.StatusNotFound, "admin-operation-not-found", "operation was not found")
			return
		}
		current := time.Now().UTC()
		if now != nil {
			current = now().UTC()
		}
		if expires, err := time.Parse(time.RFC3339Nano, row.ExpiresAt); err == nil && !current.Before(expires) {
			writeAdminProblemCode(w, r, http.StatusNotFound, "admin-operation-not-found", "operation was not found")
			return
		}
		if (row.Status == adminOperationPending || row.Status == adminOperationRunning) && staleAdminOperation(row, current) {
			stamp := current.Format(time.RFC3339Nano)
			_ = db.Model(&adminOperationRow{}).Where("id = ? AND status IN (?, ?)", row.ID, adminOperationPending, adminOperationRunning).Updates(map[string]any{
				"status": adminOperationFailed, "error_code": "operation_timeout", "updated_at": stamp,
			}).Error
			row.Status, row.ErrorCode, row.UpdatedAt = adminOperationFailed, "operation_timeout", stamp
		}
		writeAdminOperation(w, row)
	}
}

func staleAdminOperation(row adminOperationRow, now time.Time) bool {
	updated, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	return err != nil || now.Sub(updated) > adminOperationStaleTimeout
}

func startAdminOperationHandler(db *gorm.DB, now func() time.Time, kind string, work func(context.Context, []byte, AdminAuditMeta) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "admin-operation-store-unavailable", "operation store is unavailable")
			return
		}
		limit := int64(maxAdminOperationBodyBytes)
		if kind != "control-plane-restore" {
			limit = maxAdminConfigBodyBytes
		}
		body, err := ioReadAllBounded(r, limit)
		if err != nil {
			writeAdminProblemCode(w, r, http.StatusBadRequest, "admin-invalid-request", "request body is invalid or too large")
			return
		}
		at := time.Now().UTC()
		if now != nil {
			at = now().UTC()
		}
		meta, _ := AdminAuditMetaFrom(r.Context())
		row, err := startAdminOperation(db, kind, at, func(ctx context.Context) (any, error) { return work(ctx, body, meta) })
		if err != nil {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "admin-operation-unavailable", "operation could not be started")
			return
		}
		w.Header().Set("Location", "/admin/v1/operations/"+row.ID)
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusAccepted, map[string]any{"operation": adminOperationResult{ID: row.ID, Kind: row.Kind, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, "operation_url": "/admin/v1/operations/" + row.ID})
	}
}

func ioReadAllBounded(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("request body too large")
	}
	return raw, nil
}
