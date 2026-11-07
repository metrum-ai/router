// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	authzPolicyStatusDraft   = "draft"
	authzPolicyStatusActive  = "active"
	authzPolicyStatusRetired = "retired"

	authzPolicyAuditCreate           = "create"
	authzPolicyAuditActivate         = "activate"
	authzPolicyAuditRollback         = "rollback"
	authzPolicyAuditValidationFailed = "validation_failed"
)

type authzPolicySetRecord struct {
	ID          string `gorm:"column:id;primaryKey;type:text"`
	Name        string `gorm:"column:name;type:text;not null;index:idx_authz_policy_set_name_version,priority:1"`
	Version     int    `gorm:"column:version;not null;index:idx_authz_policy_set_name_version,priority:2"`
	Status      string `gorm:"column:status;type:text;not null;index:idx_authz_policy_set_status"`
	CreatedBy   string `gorm:"column:created_by;type:text;not null"`
	CreatedAt   string `gorm:"column:created_at;type:text;not null;index:idx_authz_policy_set_created"`
	ActivatedAt string `gorm:"column:activated_at;type:text;not null;default:'';index:idx_authz_policy_set_activated"`
	RetiredAt   string `gorm:"column:retired_at;type:text;not null;default:'';index:idx_authz_policy_set_retired"`
}

func (authzPolicySetRecord) TableName() string {
	return "authz_policy_sets"
}

type authzPolicyRuleRecord struct {
	ID            uint   `gorm:"column:id;primaryKey;autoIncrement"`
	PolicySetID   string `gorm:"column:policy_set_id;type:text;not null;index:idx_authz_policy_rule_set_seq,priority:1"`
	Sequence      int    `gorm:"column:sequence;not null;index:idx_authz_policy_rule_set_seq,priority:2"`
	PType         string `gorm:"column:ptype;type:text;not null"`
	SubjectOrRole string `gorm:"column:subject_or_role;type:text;not null"`
	Domain        string `gorm:"column:domain;type:text;not null"`
	Object        string `gorm:"column:object;type:text;not null"`
	Action        string `gorm:"column:action;type:text;not null"`
	Effect        string `gorm:"column:effect;type:text;not null;default:''"`
}

func (authzPolicyRuleRecord) TableName() string {
	return "authz_policy_rules"
}

type authzRoleLinkRecord struct {
	ID          uint   `gorm:"column:id;primaryKey;autoIncrement"`
	PolicySetID string `gorm:"column:policy_set_id;type:text;not null;index:idx_authz_role_link_set_seq,priority:1"`
	Sequence    int    `gorm:"column:sequence;not null;index:idx_authz_role_link_set_seq,priority:2"`
	Subject     string `gorm:"column:subject;type:text;not null"`
	Role        string `gorm:"column:role;type:text;not null"`
	Domain      string `gorm:"column:domain;type:text;not null"`
}

func (authzRoleLinkRecord) TableName() string {
	return "authz_role_links"
}

type authzPolicyAuditEventRecord struct {
	ID           uint   `gorm:"column:id;primaryKey;autoIncrement"`
	PolicySetID  string `gorm:"column:policy_set_id;type:text;not null;index:idx_authz_policy_audit_set"`
	ActorSubject string `gorm:"column:actor_subject;type:text;not null"`
	Action       string `gorm:"column:action;type:text;not null;index:idx_authz_policy_audit_action"`
	TS           string `gorm:"column:ts;type:text;not null;index:idx_authz_policy_audit_ts"`
	RequestID    string `gorm:"column:request_id;type:text;not null"`
	SafeSummary  string `gorm:"column:safe_summary;type:text;not null"`
}

func (authzPolicyAuditEventRecord) TableName() string {
	return "authz_policy_audit_events"
}

type authzPolicySetInput struct {
	ID        string
	Name      string
	Version   int
	CreatedBy string
	Policy    []string
	CreatedAt time.Time
}

func (s *usageStore) CreateAuthzPolicySet(input authzPolicySetInput) error {
	if s == nil || s.db == nil {
		return errors.New("usage db is not enabled")
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return errors.New("authz policy set id is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return errors.New("authz policy set name is required")
	}
	if input.Version <= 0 {
		return errors.New("authz policy set version must be positive")
	}
	if len(input.Policy) == 0 {
		return errors.New("authz policy set must contain policy")
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	lines := make([]string, 0, len(input.Policy))
	for _, raw := range input.Policy {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		lines = append(lines, raw)
	}
	if err := validateCasbinPolicyLines(lines); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		set := authzPolicySetRecord{
			ID:        id,
			Name:      name,
			Version:   input.Version,
			Status:    authzPolicyStatusDraft,
			CreatedBy: safeAuthzAuditField(input.CreatedBy),
			CreatedAt: input.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if err := tx.Create(&set).Error; err != nil {
			return err
		}
		if err := insertAuthzPolicyLines(tx, id, lines); err != nil {
			return err
		}
		return insertAuthzPolicyAudit(tx, id, safeAuthzAuditField(input.CreatedBy), authzPolicyAuditCreate, "", "draft policy set created", input.CreatedAt)
	})
}

func (s *usageStore) ActivateAuthzPolicySet(policySetID, actorSubject, requestID, summary string, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("usage db is not enabled")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	policySetID = strings.TrimSpace(policySetID)
	lines, err := loadAuthzPolicyLines(s.db, policySetID)
	if err != nil {
		return err
	}
	if err := validateCasbinPolicyLines(lines); err != nil {
		_ = insertAuthzPolicyAudit(s.db, policySetID, safeAuthzAuditField(actorSubject), authzPolicyAuditValidationFailed, requestID, summary, now)
		return fmt.Errorf("authz policy set %q failed validation: %w", policySetID, err)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		ts := now.UTC().Format(time.RFC3339Nano)
		if err := tx.Model(&authzPolicySetRecord{}).
			Where("status = ?", authzPolicyStatusActive).
			Updates(map[string]any{"status": authzPolicyStatusRetired, "retired_at": ts}).Error; err != nil {
			return err
		}
		updated := tx.Model(&authzPolicySetRecord{}).
			Where("id = ?", policySetID).
			Updates(map[string]any{"status": authzPolicyStatusActive, "activated_at": ts, "retired_at": ""})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return fmt.Errorf("authz policy set %q was not found", policySetID)
		}
		return insertAuthzPolicyAudit(tx, policySetID, safeAuthzAuditField(actorSubject), authzPolicyAuditActivate, requestID, summary, now)
	})
}

func (s *usageStore) RollbackAuthzPolicySet(actorSubject, requestID, summary string, now time.Time) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("usage db is not enabled")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var target authzPolicySetRecord
	if err := s.db.Where("status = ?", authzPolicyStatusRetired).
		Order("retired_at DESC, activated_at DESC, id DESC").
		First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.New("no retired authz policy set is available for rollback")
		}
		return "", err
	}
	lines, err := loadAuthzPolicyLines(s.db, target.ID)
	if err != nil {
		return "", err
	}
	if err := validateCasbinPolicyLines(lines); err != nil {
		_ = insertAuthzPolicyAudit(s.db, target.ID, safeAuthzAuditField(actorSubject), authzPolicyAuditValidationFailed, requestID, summary, now)
		return "", fmt.Errorf("authz rollback policy set %q failed validation: %w", target.ID, err)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		ts := now.UTC().Format(time.RFC3339Nano)
		if err := tx.Model(&authzPolicySetRecord{}).
			Where("status = ?", authzPolicyStatusActive).
			Updates(map[string]any{"status": authzPolicyStatusRetired, "retired_at": ts}).Error; err != nil {
			return err
		}
		updated := tx.Model(&authzPolicySetRecord{}).
			Where("id = ? AND status = ?", target.ID, authzPolicyStatusRetired).
			Updates(map[string]any{"status": authzPolicyStatusActive, "activated_at": ts, "retired_at": ""})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return fmt.Errorf("authz policy set %q is no longer retired", target.ID)
		}
		return insertAuthzPolicyAudit(tx, target.ID, safeAuthzAuditField(actorSubject), authzPolicyAuditRollback, requestID, summary, now)
	})
	return target.ID, err
}

func (s *usageStore) LoadActiveAuthzPolicyLines() ([]string, string, error) {
	if s == nil || s.db == nil {
		return nil, "", errors.New("usage db is not enabled")
	}
	var active []authzPolicySetRecord
	if err := s.db.Where("status = ?", authzPolicyStatusActive).Find(&active).Error; err != nil {
		return nil, "", err
	}
	if len(active) == 0 {
		return nil, "", errors.New("no active authz policy set")
	}
	if len(active) > 1 {
		return nil, "", errors.New("multiple active authz policy sets")
	}
	lines, err := loadAuthzPolicyLines(s.db, active[0].ID)
	if err != nil {
		return nil, "", err
	}
	if err := validateCasbinPolicyLines(lines); err != nil {
		return nil, "", fmt.Errorf("active authz policy set %q is invalid: %w", active[0].ID, err)
	}
	return lines, active[0].ID, nil
}

func insertAuthzPolicyLines(tx *gorm.DB, policySetID string, lines []string) error {
	for i, raw := range lines {
		fields, err := parseCasbinPolicyLine(raw)
		if err != nil {
			return err
		}
		if err := validateCasbinPolicyMutationFields(fields, fmt.Sprintf("policy line %d", i+1)); err != nil {
			return err
		}
		switch fields[0] {
		case "p":
			row := authzPolicyRuleRecord{
				PolicySetID:   policySetID,
				Sequence:      i + 1,
				PType:         fields[0],
				SubjectOrRole: fields[1],
				Domain:        fields[2],
				Object:        fields[3],
				Action:        fields[4],
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		case "g":
			row := authzRoleLinkRecord{
				PolicySetID: policySetID,
				Sequence:    i + 1,
				Subject:     fields[1],
				Role:        fields[2],
				Domain:      fields[3],
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func loadAuthzPolicyLines(db *gorm.DB, policySetID string) ([]string, error) {
	policySetID = strings.TrimSpace(policySetID)
	if policySetID == "" {
		return nil, errors.New("authz policy set id is required")
	}
	var set authzPolicySetRecord
	if err := db.Where("id = ?", policySetID).First(&set).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("authz policy set %q was not found", policySetID)
		}
		return nil, err
	}
	var rules []authzPolicyRuleRecord
	if err := db.Where("policy_set_id = ?", policySetID).Order("sequence ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	var links []authzRoleLinkRecord
	if err := db.Where("policy_set_id = ?", policySetID).Order("sequence ASC").Find(&links).Error; err != nil {
		return nil, err
	}
	type policyLine struct {
		sequence int
		line     string
	}
	combined := make([]policyLine, 0, len(rules)+len(links))
	for _, rule := range rules {
		effect := strings.TrimSpace(rule.Effect)
		if effect != "" && effect != "allow" {
			return nil, fmt.Errorf("authz policy rule %d has unsupported effect %q", rule.ID, rule.Effect)
		}
		line, err := formatCasbinPolicyLine(defaultString(rule.PType, "p"), rule.SubjectOrRole, rule.Domain, rule.Object, rule.Action)
		if err != nil {
			return nil, err
		}
		combined = append(combined, policyLine{sequence: rule.Sequence, line: line})
	}
	for _, link := range links {
		line, err := formatCasbinPolicyLine("g", link.Subject, link.Role, link.Domain)
		if err != nil {
			return nil, err
		}
		combined = append(combined, policyLine{sequence: link.Sequence, line: line})
	}
	sort.SliceStable(combined, func(i, j int) bool {
		return combined[i].sequence < combined[j].sequence
	})
	out := make([]string, 0, len(combined))
	for _, item := range combined {
		out = append(out, item.line)
	}
	return out, nil
}

func insertAuthzPolicyAudit(tx *gorm.DB, policySetID, actorSubject, action, requestID, summary string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := authzPolicyAuditEventRecord{
		PolicySetID:  safeAuthzAuditField(policySetID),
		ActorSubject: safeAuthzAuditField(actorSubject),
		Action:       safeAuthzAuditField(action),
		TS:           now.UTC().Format(time.RFC3339Nano),
		RequestID:    safeAuthzAuditField(requestID),
		SafeSummary:  safeAuthzAuditField(summary),
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func safeAuthzAuditField(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization", "bearer ", "token", "token_hash", "api_key", "provider_key", "password", "secret", "sha256"} {
		if strings.Contains(lower, marker) {
			return "[redacted]"
		}
	}
	if authzPolicyFieldLooksSensitive(value) {
		return "[redacted]"
	}
	return value
}
