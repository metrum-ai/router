// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const mcpToolAuditTable = "mcp_tool_audit_events"

type mcpToolAuditEventRecord struct {
	ID              string `gorm:"column:id;primaryKey;type:text"`
	TS              string `gorm:"column:ts;type:text;not null;index:idx_mcp_tool_audit_ts"`
	CallerID        string `gorm:"column:caller_id;type:text;not null;index:idx_mcp_tool_audit_caller"`
	Tool            string `gorm:"column:tool;type:text;not null;index:idx_mcp_tool_audit_tool"`
	ArgumentsSHA256 string `gorm:"column:arguments_sha256;type:text;not null"`
	ResultStatus    string `gorm:"column:result_status;type:text;not null"`
	LatencyMS       int64  `gorm:"column:latency_ms;not null"`
}

func (mcpToolAuditEventRecord) TableName() string { return mcpToolAuditTable }

func mcpToolArgumentsDigest(key []byte, arguments map[string]any) (string, error) {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("metrum-router:mcp-tool-arguments:v1\x00"))
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) beginMCPToolAudit(caller *callerRuntime, tool string, arguments map[string]any) (string, error) {
	if s == nil || s.usage == nil || s.usage.db == nil || caller == nil {
		return "", errors.New("MCP audit store unavailable")
	}
	digest, err := mcpToolArgumentsDigest(s.mcpAuditKey[:], arguments)
	if err != nil {
		return "", fmt.Errorf("digest MCP arguments: %w", err)
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", fmt.Errorf("generate MCP audit ID: %w", err)
	}
	id := "mcp-" + hex.EncodeToString(idBytes[:])
	now := time.Now().UTC()
	row := mcpToolAuditEventRecord{ID: id, TS: formatUsageTime(now), CallerID: caller.cfg.ID, Tool: tool, ArgumentsSHA256: digest, ResultStatus: "started"}
	if err := s.usage.db.Create(&row).Error; err != nil {
		return "", fmt.Errorf("write MCP audit start: %w", err)
	}
	return id, nil
}

func (s *Service) finishMCPToolAudit(id string, started time.Time, status string) error {
	if s == nil || s.usage == nil || s.usage.db == nil || id == "" {
		return errors.New("MCP audit store unavailable")
	}
	if status != "success" && status != "error" && status != "denied" && status != "limited" && status != "approval_pending" {
		status = "error"
	}
	latency := time.Since(started).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	return s.usage.db.Transaction(func(tx *gorm.DB) error {
		var row mcpToolAuditEventRecord
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return fmt.Errorf("read MCP audit start: %w", err)
		}
		result := tx.Model(&mcpToolAuditEventRecord{}).Where("id = ?", id).Updates(map[string]any{"result_status": status, "latency_ms": latency})
		if result.Error != nil {
			return fmt.Errorf("write MCP audit result: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errors.New("MCP audit event disappeared")
		}
		// Mirror only identifiers, outcome, and the keyed argument digest into the
		// existing hash-chained audit export. Raw arguments and results stay out.
		eventID := "mcp-audit-" + strings.TrimPrefix(row.ID, "mcp-")
		detail, _ := json.Marshal(struct {
			Tool      string `json:"tool"`
			LatencyMS int64  `json:"latency_ms"`
		}{Tool: row.Tool, LatencyMS: latency})
		audit := AdminAuditRecord{
			EventID: eventID, At: time.Now().UTC(), PrincipalSubject: row.CallerID,
			PrincipalKind: "router-caller", Domain: string(AdminDomainCredential),
			Object: "mcp:tool", Action: "call", OperationID: "mcp.tools.call",
			RequestID: row.ID, Outcome: status, ReasonCode: "mcp-tool-invocation",
			RequestHash: row.ArgumentsSHA256, Detail: "mcp:v1:" + string(detail),
		}
		if err := insertAdminAuditUsageTx(tx, audit); err != nil {
			return fmt.Errorf("export MCP audit result: %w", err)
		}
		return nil
	})
}

const mcpToolAuditMigrationID = 2026110705

func MCPToolAuditMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: mcpToolAuditMigrationID, Scope: usageMigrationScope, Name: "MCP tool invocation audit events", Release: "2026.11",
		Checksum: MigrationChecksum("usage.mcp-tool-audit.v1"), SchemaVersion: 16, DataVersion: 0,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey: "usage.mcp-tool-audit.apply.v1@applyMCPToolAuditMigration", PostconditionKey: "usage.mcp-tool-audit.schema.v1@verifyMCPToolAuditMigration",
		Dependencies: []int{policyRuntimeMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		Apply: applyMCPToolAuditMigration, Verify: verifyMCPToolAuditMigration,
	})
}

func applyMCPToolAuditMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("MCP tool audit migration requires database")
	}
	if !db.Migrator().HasTable(&mcpToolAuditEventRecord{}) {
		if err := db.Migrator().CreateTable(&mcpToolAuditEventRecord{}); err != nil {
			return err
		}
	}
	return verifyMCPToolAuditMigration(db)
}

func verifyMCPToolAuditMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("MCP tool audit migration requires database")
	}
	return verifyUsageRelationalModel(db, &mcpToolAuditEventRecord{})
}
