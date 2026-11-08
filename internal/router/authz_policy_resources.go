// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPolicySetImmutable = &AdminAuthorizationError{status: 409, code: "policy-set-immutable"}
var errPolicyResourceNotFound = &AdminAuthorizationError{status: 404, code: "not-found"}

type policyResourceView struct {
	Revision    int64
	Set         authzPolicySetRecord
	Rules       []authzPolicyRuleRecord
	Memberships []authzRoleLinkRecord
}
type policyResourceCommand struct {
	Kind, SetID, Name string
	Version           int
	ExpectedRevision  int64
	RecordID          uint
	Fields            []string
	Policy            []string
}
type policyResourceResult struct {
	SetID                 string
	RecordID              uint
	Revision              int64
	BeforeHash, AfterHash string
}

func loadPolicyResource(tx *gorm.DB, id string) (policyResourceView, error) {
	var view policyResourceView
	if err := tx.First(&view.Set, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return view, errPolicyResourceNotFound
		}
		return view, ErrAuthorizationUnavailable
	}
	if err := tx.Where("policy_set_id = ?", id).Order("sequence, id").Find(&view.Rules).Error; err != nil {
		return view, ErrAuthorizationUnavailable
	}
	for _, rule := range view.Rules {
		if defaultString(rule.PType, "p") != "p" || (strings.TrimSpace(rule.Effect) != "" && strings.TrimSpace(rule.Effect) != "allow") {
			return view, ErrInvalidPolicy
		}
	}
	if err := tx.Where("policy_set_id = ?", id).Order("sequence, id").Find(&view.Memberships).Error; err != nil {
		return view, ErrAuthorizationUnavailable
	}
	return view, nil
}
func (s *usageStore) getPolicyResource(id string) (policyResourceView, error) {
	return s.getPolicyResourceContext(context.Background(), id)
}
func (s *usageStore) getPolicyResourceContext(ctx context.Context, id string) (policyResourceView, error) {
	if s == nil || s.db == nil || ctx == nil || ctx.Err() != nil {
		return policyResourceView{}, ErrAuthorizationUnavailable
	}
	var view policyResourceView
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runtime policyMutationRevision
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&runtime, 1).Error; err != nil {
			return ErrAuthorizationUnavailable
		}
		var e error
		view, e = loadPolicyResource(tx, id)
		view.Revision = runtime.Revision
		return e
	})
	return view, normalizePolicyMutationError(err)
}
func (v policyResourceView) lines() []string {
	lines := make([]string, 0, len(v.Rules)+len(v.Memberships))
	encode := func(fields []string) {
		line, _ := formatCasbinPolicyLine(fields...)
		lines = append(lines, line)
	}
	for _, r := range v.Rules {
		f := []string{"p", r.SubjectOrRole, r.Domain, r.Object, r.Action}
		encode(f)
	}
	for _, g := range v.Memberships {
		encode([]string{"g", g.Subject, g.Role, g.Domain})
	}
	return lines
}
func (v policyResourceView) hash() string {
	sum := sha256.Sum256([]byte(strings.Join(v.lines(), "\n")))
	return hex.EncodeToString(sum[:])
}
func policyViewFromLines(set authzPolicySetRecord, lines []string) (policyResourceView, error) {
	v := policyResourceView{Set: set}
	if len(lines) == 0 || len(lines) > maxPolicyGuardCandidateRows {
		return v, ErrInvalidPolicy
	}
	for i, line := range lines {
		f, err := parseCasbinPolicyLine(line)
		if err != nil {
			return v, ErrInvalidPolicy
		}
		if err := validateCasbinPolicyMutationFields(f, "policy candidate"); err != nil {
			return v, ErrInvalidPolicy
		}
		switch f[0] {
		case "p":
			r := authzPolicyRuleRecord{PolicySetID: set.ID, Sequence: i, PType: "p", SubjectOrRole: f[1], Domain: f[2], Object: f[3], Action: f[4]}
			if len(f) == 6 {
				r.Effect = f[5]
			}
			v.Rules = append(v.Rules, r)
		case "g":
			v.Memberships = append(v.Memberships, authzRoleLinkRecord{PolicySetID: set.ID, Sequence: i, Subject: f[1], Role: f[2], Domain: f[3]})
		default:
			return v, ErrInvalidPolicy
		}
	}
	if len(v.Memberships) > maxPolicyGuardMemberships {
		return v, ErrInvalidPolicy
	}
	return v, nil
}

// The complete representation used for the guard is also the representation
// persisted below. A fragment request cannot choose a different effective set.
func (s *usageStore) mutatePolicyResource(ctx context.Context, p Principal, deps policyMutationDeps, cmd policyResourceCommand, audit func(*gorm.DB, int64, policyResourceResult) error) (policyResourceResult, error) {
	var result policyResourceResult
	if deps.Build == nil || audit == nil {
		return result, ErrAuthorizationUnavailable
	}
	var view policyResourceView
	create := cmd.Kind == "create-set"
	remove := cmd.Kind == "delete-set"
	activate := cmd.Kind == "activate"
	rollback := cmd.Kind == "rollback"
	action := "write"
	if activate {
		action = "activate"
	}
	if rollback {
		action = "rollback"
	}
	m := policyMutation{Operation: action, ExpectedRevision: cmd.ExpectedRevision}
	m.Candidate = func(tx *gorm.DB, cfg *Config) (*Service, error) {
		if strings.TrimSpace(cmd.SetID) == "" {
			return nil, ErrInvalidPolicy
		}
		if create {
			if strings.TrimSpace(cmd.Name) == "" || cmd.Version < 1 {
				return nil, ErrInvalidPolicy
			}
			view.Set = authzPolicySetRecord{ID: cmd.SetID, Name: cmd.Name, Version: cmd.Version, Status: authzPolicyStatusDraft, CreatedBy: safeAuthzAuditField(p.Subject), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			var err error
			view, err = policyViewFromLines(view.Set, cmd.Policy)
			if err != nil {
				return nil, err
			}
		} else {
			var err error
			view, err = loadPolicyResource(tx, cmd.SetID)
			if err != nil {
				return nil, err
			}
			result.BeforeHash = view.hash()
			if rollback {
				view, err = clonePolicyHistory(tx, view, p)
				if err != nil {
					return nil, err
				}
			} else if view.Set.Status != authzPolicyStatusDraft {
				return nil, errPolicySetImmutable
			}
			switch cmd.Kind {
			case "replace-set":
				view, err = policyViewFromLines(view.Set, cmd.Policy)
				if err != nil {
					return nil, err
				}
			case "create-rule", "replace-rule", "delete-rule", "create-membership", "replace-membership", "delete-membership":
				if err := view.edit(cmd); err != nil {
					return nil, err
				}
			case "delete-set", "activate", "rollback":
			default:
				return nil, ErrInvalidPolicy
			}
		}
		result.SetID = view.Set.ID
		if remove {
			return deps.Current(tx, cfg)
		}
		// Enforce authored limits before compiling the candidate's role graph.
		if len(view.Rules)+len(view.Memberships) > maxPolicyGuardCandidateRows || len(view.Memberships) > maxPolicyGuardMemberships {
			return nil, ErrInvalidPolicy
		}
		result.AfterHash = view.hash()
		return deps.Build(tx, cfg, view.lines())
	}
	m.Apply = func(tx *gorm.DB) error {
		if create || rollback {
			if err := tx.Create(&view.Set).Error; err != nil {
				return err
			}
		}
		if !activate {
			if !create && !rollback {
				if err := tx.Where("policy_set_id = ?", cmd.SetID).Delete(&authzPolicyRuleRecord{}).Error; err != nil {
					return err
				}
				if err := tx.Where("policy_set_id = ?", cmd.SetID).Delete(&authzRoleLinkRecord{}).Error; err != nil {
					return err
				}
			}
			if remove {
				return tx.Delete(&authzPolicySetRecord{}, "id = ?", cmd.SetID).Error
			}
			if len(view.Rules) > 0 {
				if err := tx.Create(&view.Rules).Error; err != nil {
					return err
				}
			}
			if len(view.Memberships) > 0 {
				if err := tx.Create(&view.Memberships).Error; err != nil {
					return err
				}
			}
			if cmd.Kind == "create-rule" {
				result.RecordID = view.Rules[len(view.Rules)-1].ID
			}
			if cmd.Kind == "create-membership" {
				result.RecordID = view.Memberships[len(view.Memberships)-1].ID
			}
			if !rollback {
				return nil
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := tx.Model(&authzPolicySetRecord{}).Where("status = ?", authzPolicyStatusActive).Updates(map[string]any{"status": authzPolicyStatusRetired, "retired_at": now}).Error; err != nil {
			return err
		}
		updated := tx.Model(&authzPolicySetRecord{}).Where("id = ?", view.Set.ID).Updates(map[string]any{"status": authzPolicyStatusActive, "activated_at": now, "retired_at": ""})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return ErrAuthorizationUnavailable
		}
		active := tx.Model(&policyMutationRevision{}).Where("id = ?", 1).Update("active_set_id", view.Set.ID)
		if active.Error != nil || active.RowsAffected != 1 {
			return ErrAuthorizationUnavailable
		}
		return nil
	}
	m.Audit = func(tx *gorm.DB, rev int64) error { result.Revision = rev; return audit(tx, rev, result) }
	_, err := s.withPolicyMutation(ctx, p, deps, m)
	if err != nil {
		return policyResourceResult{}, err
	}
	return result, nil
}

func (v *policyResourceView) edit(cmd policyResourceCommand) error {
	rule := strings.HasSuffix(cmd.Kind, "rule")
	add := strings.HasPrefix(cmd.Kind, "create-")
	remove := strings.HasPrefix(cmd.Kind, "delete-")
	if !add && cmd.RecordID == 0 {
		return ErrInvalidPolicy
	}
	if !remove {
		if err := validateCasbinPolicyMutationFields(cmd.Fields, "policy record"); err != nil {
			return ErrInvalidPolicy
		}
		if rule && cmd.Fields[0] != "p" || !rule && cmd.Fields[0] != "g" {
			return ErrInvalidPolicy
		}
	}
	if rule {
		index := -1
		for i, r := range v.Rules {
			if r.ID == cmd.RecordID {
				index = i
				break
			}
		}
		if !add && index < 0 {
			return errPolicyResourceNotFound
		}
		if remove {
			v.Rules = append(v.Rules[:index], v.Rules[index+1:]...)
			return nil
		}
		f := cmd.Fields
		r := authzPolicyRuleRecord{ID: cmd.RecordID, PolicySetID: v.Set.ID, PType: "p", SubjectOrRole: f[1], Domain: f[2], Object: f[3], Action: f[4]}
		if len(f) == 6 {
			r.Effect = f[5]
		}
		if add {
			r.ID = 0
			for _, old := range v.Rules {
				if old.Sequence >= r.Sequence {
					r.Sequence = old.Sequence + 1
				}
			}
			v.Rules = append(v.Rules, r)
		} else {
			r.Sequence = v.Rules[index].Sequence
			v.Rules[index] = r
		}
	} else {
		index := -1
		for i, g := range v.Memberships {
			if g.ID == cmd.RecordID {
				index = i
				break
			}
		}
		if !add && index < 0 {
			return errPolicyResourceNotFound
		}
		if remove {
			v.Memberships = append(v.Memberships[:index], v.Memberships[index+1:]...)
			return nil
		}
		f := cmd.Fields
		g := authzRoleLinkRecord{ID: cmd.RecordID, PolicySetID: v.Set.ID, Subject: f[1], Role: f[2], Domain: f[3]}
		if add {
			g.ID = 0
			for _, old := range v.Memberships {
				if old.Sequence >= g.Sequence {
					g.Sequence = old.Sequence + 1
				}
			}
			v.Memberships = append(v.Memberships, g)
		} else {
			g.Sequence = v.Memberships[index].Sequence
			v.Memberships[index] = g
		}
	}
	return nil
}
