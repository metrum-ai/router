// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Store-level group target writes for the Admin API (metrum-ai/router#395).
//
// Every group write is copy-on-write: the active canonical document is
// loaded, patched for the one group, validated, imported as a new
// configuration set, and activated in a single transaction. The active set's
// rows are never mutated in place, so export/diff see the change and config
// rollback can revert it. Optimistic concurrency is enforced by the guarded
// router_config_runtime update (exactly one concurrent writer wins) plus the
// optional If-Match revision precondition, and Idempotency-Key replays return
// the recorded result without creating a second set.
//
// Secrets boundary: group writes reference catalog providers and models only.
// Credential-shaped fields are rejected by the API layer before this code
// runs, and this file stores no request content — only ordered target
// identities, reason text, and the request hash.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// maxAdminGroupTargets bounds a single group target list so a malformed or
// hostile body cannot create unbounded projection work.
const maxAdminGroupTargets = 100

// maxAdminReasonBytes caps operator-supplied audit reason text.
const maxAdminReasonBytes = 500

// Admin API change-event actions (issue #395 §7).
const (
	configActionGroupTargetsReplace = "group_targets_replace"
	configActionGroupTargetAdd      = "group_target_add"
	configActionGroupTargetRemove   = "group_target_remove"
	configActionGroupRollback       = "group_rollback"
	configActionSetImport           = "set_import"
	configActionSetActivate         = "set_activate"
	configActionSetRollback         = "set_rollback"
	configActionShadowStart         = "shadow_start"
	configActionShadowStop          = "shadow_stop"
	configActionShadowPromote       = "shadow_promote"
)

// ConfigAdminError is the typed error the HTTP layer maps to status codes.
// Type is a stable machine-readable code; Message is safe operator-facing
// text that never contains credentials or request bodies.
type ConfigAdminError struct {
	Status   int
	Type     string
	Message  string
	Revision int64
	Reports  []GroupTargetReport
}

func (e *ConfigAdminError) Error() string { return e.Type }

func configAdminError(status int, errType, message string) *ConfigAdminError {
	return &ConfigAdminError{Status: status, Type: errType, Message: message}
}

// GroupTargetValidationSpec is the snake_case API form of target validation
// metadata (the internal TargetValidation JSON tags are camelCase and must
// not leak into this API).
type GroupTargetValidationSpec struct {
	Status       string  `json:"status"`
	Workload     string  `json:"workload,omitempty"`
	ValidatedAt  string  `json:"validated_at,omitempty"`
	QualityScore float64 `json:"quality_score,omitempty"`
	PassRate     float64 `json:"pass_rate,omitempty"`
	Harness      string  `json:"harness,omitempty"`
	Notes        string  `json:"notes,omitempty"`
}

func (s *GroupTargetValidationSpec) toTargetValidation() *TargetValidation {
	if s == nil {
		return nil
	}
	return &TargetValidation{
		Status: s.Status, Workload: s.Workload, ValidatedAt: s.ValidatedAt,
		QualityScore: s.QualityScore, PassRate: s.PassRate, Harness: s.Harness, Notes: s.Notes,
	}
}

// GroupTargetSpec is one desired target in a group write request. Targets
// resolve against the active provider catalog: ModelRef selects a catalog
// entry; explicit Model must match a catalog entry's served model. No
// credential fields exist here by design.
type GroupTargetSpec struct {
	Provider   string                     `json:"provider"`
	Model      string                     `json:"model,omitempty"`
	ModelRef   string                     `json:"model_ref,omitempty"`
	Weight     int                        `json:"weight,omitempty"`
	Region     string                     `json:"region,omitempty"`
	Tags       []string                   `json:"tags,omitempty"`
	Validation *GroupTargetValidationSpec `json:"validation,omitempty"`
}

func (s GroupTargetSpec) identity() string {
	ref := s.ModelRef
	if ref == "" {
		ref = s.Model
	}
	return s.Provider + "/" + ref
}

// GroupTargetCheck is one named validation outcome for a target.
type GroupTargetCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// GroupTargetReport is the per-target eligibility report returned by dry
// runs and by 422 ineligible responses.
type GroupTargetReport struct {
	Provider string             `json:"provider"`
	Model    string             `json:"model,omitempty"`
	ModelRef string             `json:"model_ref,omitempty"`
	Eligible bool               `json:"eligible"`
	Checks   []GroupTargetCheck `json:"checks"`
}

// GroupTargetsDiff summarizes a group change.
type GroupTargetsDiff struct {
	Added          []string `json:"added"`
	Removed        []string `json:"removed"`
	Reordered      []string `json:"reordered"`
	StrategyFrom   string   `json:"strategy_from,omitempty"`
	StrategyTo     string   `json:"strategy_to,omitempty"`
	StrategyChange bool     `json:"strategy_change"`
}

// GroupTargetsWriteRequest is the normalized swap/add/remove input.
type GroupTargetsWriteRequest struct {
	Group            string
	Targets          []GroupTargetSpec
	Strategy         string
	Reason           string
	DryRun           bool
	Force            bool
	IdempotencyKey   string
	ExpectedRevision *int64
	Action           string // one of the group_* change-event actions
}

// GroupTargetsWriteResult is the write (or dry-run) outcome.
type GroupTargetsWriteResult struct {
	Group           string              `json:"group"`
	SetID           string              `json:"set_id,omitempty"`
	PreviousSetID   string              `json:"previous_set_id,omitempty"`
	Revision        int64               `json:"revision"`
	DryRun          bool                `json:"dry_run"`
	Forced          bool                `json:"forced"`
	Replayed        bool                `json:"replayed"`
	Diff            GroupTargetsDiff    `json:"diff"`
	Targets         []GroupTargetReport `json:"targets"`
	Strategy        string              `json:"strategy"`
	RestoredFromSet string              `json:"restored_from_set_id,omitempty"`
}

// GroupWritePolicy carries the deployment's admin write policy.
type GroupWritePolicy struct {
	// RequireValidationStatus, when non-empty (for example "passed"), refuses
	// targets whose validation.status does not match unless the write sets
	// force (which requires the config:force permission).
	RequireValidationStatus string
}

// configChangeEventMeta extends the change-event row with the phase-8 audit
// metadata. All fields are safe scalars.
type configChangeEventMeta struct {
	GroupName      string
	PrevSetID      string
	Reason         string
	Forced         bool
	IdempotencyKey string
	RequestHash    string
	Detail         string
	BeforeTargets  []GroupTargetSpec
	AfterTargets   []GroupTargetSpec
}

// insertConfigChangeEventTx appends one change event with phase-8 metadata
// and its before/after target rows.
func insertConfigChangeEventTx(tx *gorm.DB, runtimeScope, configSetID, action, actor string, revision int64, meta configChangeEventMeta, now time.Time) (string, error) {
	eventID := newConfigChangeEventID()
	forced := 0
	if meta.Forced {
		forced = 1
	}
	reason := strings.TrimSpace(meta.Reason)
	if len(reason) > maxAdminReasonBytes {
		reason = reason[:maxAdminReasonBytes]
	}
	if err := tx.Exec(
		`INSERT INTO router_config_change_events (event_id, runtime_scope, config_set_id, action, actor, at, config_revision, group_name, prev_config_set_id, reason, forced, idempotency_key, request_hash, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, runtimeScope, configSetID, action, actor, now.UTC().Format(time.RFC3339Nano), revision,
		strings.TrimSpace(meta.GroupName), strings.TrimSpace(meta.PrevSetID), reason, forced,
		strings.TrimSpace(meta.IdempotencyKey), strings.TrimSpace(meta.RequestHash), strings.TrimSpace(meta.Detail),
	).Error; err != nil {
		return "", fmt.Errorf("append change event: %w", err)
	}
	insertTargets := func(phase string, targets []GroupTargetSpec) error {
		for i, target := range targets {
			if err := tx.Exec(
				`INSERT INTO router_config_change_event_targets (event_id, phase, sequence, provider_name, model_ref, model) VALUES (?, ?, ?, ?, ?, ?)`,
				eventID, phase, i, target.Provider, target.ModelRef, target.Model,
			).Error; err != nil {
				return fmt.Errorf("append change-event targets: %w", err)
			}
		}
		return nil
	}
	if err := insertTargets("before", meta.BeforeTargets); err != nil {
		return "", err
	}
	if err := insertTargets("after", meta.AfterTargets); err != nil {
		return "", err
	}
	return eventID, nil
}

// configAdminRuntimeRow mirrors router_config_runtime for guarded updates.
type configAdminRuntimeState struct {
	Revision    int64
	ActiveSetID string
}

// loadConfigAdminRuntime reads the runtime pointer and the single validated
// active set for the scope. The runtime row may be absent before the first
// activation; the active set must exist.
func loadConfigAdminRuntime(tx *gorm.DB, runtimeScope string) (configAdminRuntimeState, configSetRow, error) {
	var state configAdminRuntimeState
	revision, err := currentRevisionForScope(tx, runtimeScope)
	if err != nil {
		return state, configSetRow{}, err
	}
	state.Revision = revision
	var sets []configSetRow
	if err := tx.Where("runtime_scope = ? AND status = ? AND validation_status = ?", runtimeScope, "active", "valid").Limit(2).Find(&sets).Error; err != nil {
		return state, configSetRow{}, fmt.Errorf("load active config set: %w", err)
	}
	if len(sets) == 0 {
		return state, configSetRow{}, configAdminError(409, "config-no-active-set", "no validated active configuration set for the runtime scope")
	}
	if len(sets) != 1 {
		return state, configSetRow{}, fmt.Errorf("multiple validated active config sets for runtime scope %q", runtimeScope)
	}
	state.ActiveSetID = sets[0].ID
	return state, sets[0], nil
}

// activateConfigSetGuardedTx supersede/activates newSetID and bumps the
// runtime revision, guarded on the expected current revision so exactly one
// concurrent writer wins. expectedRevision nil means "whatever the
// transaction observed at guardRevision".
func activateConfigSetGuardedTx(tx *gorm.DB, runtimeScope, newSetID string, guardRevision int64, now time.Time) (int64, error) {
	var target configSetRow
	if err := tx.Where("id = ? AND runtime_scope = ?", newSetID, runtimeScope).First(&target).Error; err != nil {
		return 0, fmt.Errorf("load target config set: %w", err)
	}
	if target.ValidationStatus != "valid" {
		return 0, configAdminError(409, "config-set-not-valid", "only validated configuration sets can be activated")
	}
	if err := tx.Exec(
		`UPDATE router_config_sets SET status = 'superseded' WHERE runtime_scope = ? AND status = 'active' AND id != ?`,
		runtimeScope, newSetID,
	).Error; err != nil {
		return 0, fmt.Errorf("supersede previous active set: %w", err)
	}
	if err := tx.Exec(
		`UPDATE router_config_sets SET status = 'active', activated_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339Nano), newSetID,
	).Error; err != nil {
		return 0, fmt.Errorf("activate config set: %w", err)
	}
	res := tx.Exec(
		`UPDATE router_config_runtime SET config_revision = config_revision + 1, active_set_id = ? WHERE runtime_scope = ? AND config_revision = ?`,
		newSetID, runtimeScope, guardRevision,
	)
	if res.Error != nil {
		return 0, fmt.Errorf("update runtime revision: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// Either the runtime row is missing (first activation) or the
		// revision moved under us.
		var count int64
		if err := tx.Table("router_config_runtime").Where("runtime_scope = ?", runtimeScope).Count(&count).Error; err != nil {
			return 0, err
		}
		if count == 0 {
			if err := tx.Exec(
				`INSERT INTO router_config_runtime (runtime_scope, config_revision, active_set_id) VALUES (?, 1, ?)`,
				runtimeScope, newSetID,
			).Error; err != nil {
				return 0, fmt.Errorf("insert runtime revision: %w", err)
			}
			queueConfigRevisionNotification(tx, runtimeScope)
			return 1, nil
		}
		current, err := currentRevisionForScope(tx, runtimeScope)
		if err != nil {
			return 0, err
		}
		return 0, &ConfigAdminError{Status: 409, Type: "config-revision-conflict", Message: "the active configuration changed concurrently; retry with the current revision", Revision: current}
	}
	queueConfigRevisionNotification(tx, runtimeScope)
	return guardRevision + 1, nil
}

// importCanonicalDocumentTx inserts the configuration-set row and canonical
// document for yamlBytes and projects the relational rows, without any
// change event. Callers record the event that matches their action. The set
// name is suffixed with the set ID because router_config_sets enforces
// UNIQUE (runtime_scope, name).
func importCanonicalDocumentTx(tx *gorm.DB, runtimeScope, namePrefix string, canonicalYAML []byte, cfg *Config, actor string, now time.Time) (string, error) {
	contentHash := sha256.Sum256(canonicalYAML)
	configSetID := newConfigSetID()
	name := strings.TrimSpace(namePrefix) + ":" + configSetID
	if err := tx.Exec(
		`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		configSetID, runtimeScope, name, "draft", "valid", actor, now.UTC().Format(time.RFC3339Nano),
	).Error; err != nil {
		return "", fmt.Errorf("insert config set: %w", err)
	}
	if err := tx.Exec(
		`INSERT INTO router_config_documents (config_set_id, canonical_yaml, content_sha256, updated_at) VALUES (?, ?, ?, ?)`,
		configSetID, string(canonicalYAML), hex.EncodeToString(contentHash[:]), now.UTC().Format(time.RFC3339Nano),
	).Error; err != nil {
		return "", fmt.Errorf("insert canonical document: %w", err)
	}
	if ProjectConfigHook != nil {
		if err := ProjectConfigHook(tx, configSetID, cfg); err != nil {
			return "", fmt.Errorf("project config to relational: %w", err)
		}
	}
	return configSetID, nil
}

// groupTargetSpecsFromGroup projects a resolved group back into spec form for
// diffs and audit rows. Only the spec-owned fields are carried; resolved
// catalog metadata is omitted.
func groupTargetSpecsFromGroup(group ModelGroup) []GroupTargetSpec {
	out := make([]GroupTargetSpec, 0, len(group.Targets))
	for _, target := range group.Targets {
		out = append(out, GroupTargetSpec{
			Provider:   target.Provider,
			Model:      target.Model,
			ModelRef:   target.ModelRef,
			Weight:     target.Weight,
			Region:     target.Region,
			Tags:       append([]string(nil), target.Tags...),
			Validation: groupTargetValidationFromTarget(target.Validation),
		})
	}
	return out
}

func groupTargetValidationFromTarget(v *TargetValidation) *GroupTargetValidationSpec {
	if v == nil {
		return nil
	}
	return &GroupTargetValidationSpec{
		Status: v.Status, Workload: v.Workload, ValidatedAt: v.ValidatedAt,
		QualityScore: v.QualityScore, PassRate: v.PassRate, Harness: v.Harness, Notes: v.Notes,
	}
}

// diffGroupTargetSpecs computes added/removed/reordered identities.
func diffGroupTargetSpecs(before, after []GroupTargetSpec) GroupTargetsDiff {
	index := func(specs []GroupTargetSpec) map[string]int {
		out := map[string]int{}
		for i, spec := range specs {
			out[spec.identity()] = i
		}
		return out
	}
	beforeIdx, afterIdx := index(before), index(after)
	diff := GroupTargetsDiff{Added: []string{}, Removed: []string{}, Reordered: []string{}}
	for _, spec := range after {
		id := spec.identity()
		if _, ok := beforeIdx[id]; !ok {
			diff.Added = append(diff.Added, id)
		}
	}
	for _, spec := range before {
		id := spec.identity()
		to, ok := afterIdx[id]
		if !ok {
			diff.Removed = append(diff.Removed, id)
			continue
		}
		if beforeIdx[id] != to {
			diff.Reordered = append(diff.Reordered, fmt.Sprintf("%s:%d->%d", id, beforeIdx[id], to))
		}
	}
	return diff
}

// groupWriteRequestHash binds an idempotency key to the exact request so a
// reused key with a different body conflicts instead of replaying.
func groupWriteRequestHash(group string, req GroupTargetsWriteRequest) string {
	h := sha256.New()
	_, _ = h.Write([]byte(group))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(req.Action))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(req.Strategy))
	_, _ = h.Write([]byte{0})
	for _, spec := range req.Targets {
		_, _ = h.Write([]byte(spec.identity()))
		_, _ = h.Write([]byte{1})
		_, _ = h.Write([]byte(fmt.Sprintf("%d", spec.Weight)))
		_, _ = h.Write([]byte{2})
	}
	_, _ = h.Write([]byte(fmt.Sprintf("%v", req.Force)))
	return hex.EncodeToString(h.Sum(nil))
}

// validateGroupTargetSpecs resolves every target against the catalog and the
// group contract and returns the per-target report. It fails closed: any
// resolution or eligibility failure is reported, never skipped.
func validateGroupTargetSpecs(cfg *Config, groupName string, group ModelGroup, specs []GroupTargetSpec, policy GroupWritePolicy, now time.Time) ([]GroupTargetReport, []Target) {
	reports := make([]GroupTargetReport, 0, len(specs))
	resolved := make([]Target, 0, len(specs))
	shapes := []string(nil)
	if group.Contract != nil {
		shapes = append(shapes, group.Contract.SupportedAPIShapes...)
	}
	for _, spec := range specs {
		report := GroupTargetReport{Provider: spec.Provider, Model: spec.Model, ModelRef: spec.ModelRef, Checks: []GroupTargetCheck{}}
		add := func(name string, ok bool, detail string) {
			report.Checks = append(report.Checks, GroupTargetCheck{Name: name, OK: ok, Detail: detail})
		}
		provider, ok := cfg.Provider[spec.Provider]
		if !ok {
			add("provider_exists", false, "provider is not in the active catalog")
			report.Eligible = false
			reports = append(reports, report)
			continue
		}
		add("provider_exists", true, "")
		target := Target{
			Provider:   spec.Provider,
			Model:      spec.Model,
			ModelRef:   spec.ModelRef,
			Region:     spec.Region,
			Tags:       append([]string(nil), spec.Tags...),
			Validation: spec.Validation.toTargetValidation(),
		}
		if spec.Weight > 0 {
			target.Weight = spec.Weight
		}
		// Catalog resolution: model_ref must exist; an explicit model must
		// match a catalog entry's served model (no ad hoc models).
		if spec.ModelRef != "" {
			if _, ok := provider.Models[spec.ModelRef]; !ok {
				add("model_in_catalog", false, "model_ref is not in the provider catalog")
				report.Eligible = false
				reports = append(reports, report)
				continue
			}
		} else {
			found := false
			for _, catalogModel := range provider.Models {
				if catalogModel.Model == spec.Model {
					found = true
					break
				}
			}
			if !found {
				add("model_in_catalog", false, "model is not served by any catalog entry for the provider")
				report.Eligible = false
				reports = append(reports, report)
				continue
			}
		}
		add("model_in_catalog", true, "")
		resolvedTarget, err := cfg.resolveTarget(groupName, target)
		if err != nil {
			add("resolve", false, "target does not resolve against the active catalog")
			report.Eligible = false
			reports = append(reports, report)
			continue
		}
		add("resolve", true, "")
		if err := validateTargetRegion(resolvedTarget.Region); err != nil {
			add("region", false, "region metadata is invalid")
			report.Eligible = false
			reports = append(reports, report)
			continue
		}
		add("region", true, "")
		// Contract eligibility for every API shape the group promises.
		eligible := true
		if group.Contract != nil {
			outDialect := targetDialect(cfg.Provider[resolvedTarget.Provider], resolvedTarget)
			for _, shape := range shapes {
				callerDialect := normalizeContractAPIShape(shape)
				if callerDialect == "" {
					continue
				}
				if reason := targetPassesContract(group.Contract, resolvedTarget, outDialect, callerDialect, nil, dynamicStats{}, now); reason != "" {
					add("contract:"+callerDialect, false, reason)
					eligible = false
				} else {
					add("contract:"+callerDialect, true, "")
				}
			}
		}
		// require_validation_status policy.
		if status := strings.ToLower(strings.TrimSpace(policy.RequireValidationStatus)); status != "" {
			actual := ""
			if resolvedTarget.Validation != nil {
				actual = strings.ToLower(strings.TrimSpace(resolvedTarget.Validation.Status))
			}
			if actual != status {
				add("validation_status", false, "target validation.status is not "+status)
				eligible = false
			} else {
				add("validation_status", true, "")
			}
		}
		report.Eligible = eligible
		reports = append(reports, report)
		resolved = append(resolved, resolvedTarget)
	}
	return reports, resolved
}

// groupWriteValidationFailed returns the 422 error for an ineligible write,
// or nil when every target is eligible (or the failure is bypassed by force
// where the policy allows it).
func groupWriteEligibilityError(reports []GroupTargetReport, force bool, policy GroupWritePolicy) *ConfigAdminError {
	if force {
		// force bypasses only the validation-status policy; catalog
		// resolution and contract failures remain fatal.
		policyOnly := true
		for _, report := range reports {
			for _, check := range report.Checks {
				if check.OK {
					continue
				}
				if check.Name == "validation_status" {
					continue
				}
				policyOnly = false
			}
		}
		if policyOnly {
			return nil
		}
	}
	for _, report := range reports {
		if !report.Eligible {
			return &ConfigAdminError{Status: 422, Type: "target-ineligible", Message: "one or more targets fail catalog or contract eligibility", Reports: reports}
		}
	}
	return nil
}

// WriteGroupTargets applies one copy-on-write group change. The whole
// operation — revision precondition, idempotency check, validation, import,
// activation, and the audit event — commits or rolls back as one
// transaction.
func WriteGroupTargets(db *gorm.DB, runtimeScope string, req GroupTargetsWriteRequest, actor string, policy GroupWritePolicy, now time.Time) (*GroupTargetsWriteResult, error) {
	if db == nil {
		return nil, configAdminError(409, "config_source_mode_file", "the router serves configuration from a YAML file; enable server.config_source.mode: database and import the current YAML to use Admin API writes")
	}
	runtimeScope = strings.TrimSpace(runtimeScope)
	if runtimeScope == "" {
		return nil, errors.New("config runtime scope is required")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, errors.New("config actor is required")
	}
	group := strings.TrimSpace(req.Group)
	if group == "" || strings.ContainsAny(group, "/\r\n") {
		return nil, configAdminError(400, "config-invalid-request", "group name is invalid")
	}
	if len(req.Targets) > maxAdminGroupTargets {
		return nil, configAdminError(400, "config-target-list-too-large", fmt.Sprintf("target list exceeds the maximum of %d", maxAdminGroupTargets))
	}
	action := req.Action
	if action == "" {
		action = configActionGroupTargetsReplace
	}
	requestHash := groupWriteRequestHash(group, req)

	var result *GroupTargetsWriteResult
	// SQLite WAL write-write races fail with SQLITE_BUSY even under a busy
	// timeout; retry the whole transaction, which re-reads all state and
	// keeps If-Match and idempotency semantics. PostgreSQL relies on the
	// guarded runtime update and sees no busy errors.
	err := withSQLiteBusyRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockConfigRuntimeTx(tx, runtimeScope); err != nil {
				return err
			}
			if err := ensureConfigAdminWriteSchema(tx); err != nil {
				return err
			}
			state, _, err := loadConfigAdminRuntime(tx, runtimeScope)
			if err != nil {
				return err
			}
			// Idempotent replay comes before the If-Match precondition: a retry
			// legitimately resends the revision it originally matched, which is
			// now stale. The same key with the same request returns the recorded
			// result; with a different request it conflicts.
			if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
				replay, conflict, err := lookupIdempotentGroupWrite(tx, runtimeScope, key, requestHash, group)
				if err != nil {
					return err
				}
				if conflict {
					return configAdminError(409, "idempotency-key-conflict", "Idempotency-Key was already used with a different request")
				}
				if replay != nil {
					result = replay
					return nil
				}
			}
			// If-Match optimistic concurrency precondition.
			if req.ExpectedRevision != nil && *req.ExpectedRevision != state.Revision {
				return &ConfigAdminError{Status: 409, Type: "config-revision-conflict", Message: "If-Match revision is stale", Revision: state.Revision}
			}

			raw, err := ExportCanonicalYAML(tx, state.ActiveSetID)
			if err != nil {
				return err
			}
			patched, before, after, strategyFrom, strategyTo, reports, perr := prepareGroupWrite(raw, group, req, policy, now)
			if perr != nil {
				return perr
			}
			diff := diffGroupTargetSpecs(before, after)
			diff.StrategyFrom = strategyFrom
			diff.StrategyTo = strategyTo
			diff.StrategyChange = strategyTo != "" && strategyFrom != strategyTo

			if req.DryRun {
				result = &GroupTargetsWriteResult{
					Group: group, Revision: state.Revision, DryRun: true, Forced: req.Force,
					Diff: diff, Targets: reports, Strategy: firstNonEmpty(strategyTo, strategyFrom),
				}
				return nil
			}

			// Import the patched document as a new set and activate it, guarded
			// on the revision observed above so a concurrent writer cannot
			// interleave.
			var candidate Config
			if err := yaml.Unmarshal(patched, &candidate); err != nil {
				return fmt.Errorf("parse patched canonical YAML: %w", err)
			}
			candidate.setDefaults()
			newSetID, err := importCanonicalDocumentTx(tx, runtimeScope, "admin-api:group:"+group, patched, &candidate, actor, now)
			if err != nil {
				return err
			}
			revision, err := activateConfigSetGuardedTx(tx, runtimeScope, newSetID, state.Revision, now)
			if err != nil {
				return err
			}
			if _, err := insertConfigChangeEventTx(tx, runtimeScope, newSetID, action, actor, revision, configChangeEventMeta{
				GroupName:      group,
				PrevSetID:      state.ActiveSetID,
				Reason:         req.Reason,
				Forced:         req.Force,
				IdempotencyKey: req.IdempotencyKey,
				RequestHash:    requestHash,
				BeforeTargets:  before,
				AfterTargets:   after,
			}, now); err != nil {
				return err
			}
			result = &GroupTargetsWriteResult{
				Group: group, SetID: newSetID, PreviousSetID: state.ActiveSetID, Revision: revision,
				Forced: req.Force, Diff: diff, Targets: reports, Strategy: firstNonEmpty(strategyTo, strategyFrom),
			}
			return nil
		})
	})
	if err != nil {
		// A concurrent writer using the same Idempotency-Key wins the
		// partial unique index; map the race to the documented conflict.
		if strings.Contains(err.Error(), "SQLSTATE 23505") && strings.TrimSpace(req.IdempotencyKey) != "" {
			return nil, configAdminError(409, "idempotency-key-conflict", "Idempotency-Key is being used by a concurrent write; retry the request")
		}
		return nil, err
	}
	return result, nil
}

// withSQLiteBusyRetry retries a write transaction that loses the SQLite WAL
// write-write race. ConfigAdminError values (typed 4xx outcomes) are never
// retried.
func withSQLiteBusyRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = fn()
		if err == nil || !isSQLiteBusyError(err) {
			return err
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return err
}

func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	var adminErr *ConfigAdminError
	if errors.As(err, &adminErr) {
		return false
	}
	text := err.Error()
	if strings.Contains(text, "SQLITE_BUSY") || strings.Contains(text, "database is locked") {
		return true
	}
	// PostgreSQL deadlock/serialization errors are safe to retry: the
	// transaction re-reads all state and the guarded revision update keeps
	// exactly-one-winner semantics.
	return strings.Contains(text, "SQLSTATE 40P01") || strings.Contains(text, "SQLSTATE 40001") || strings.Contains(text, "SQLSTATE 55P03")
}

// lockConfigRuntimeTx serializes writers within one runtime scope. On
// PostgreSQL it takes a transaction-scoped advisory lock keyed by the scope
// name before any DDL or DML, so concurrent group writes cannot deadlock on
// lock ordering; it works before the runtime row exists. On SQLite it is a
// no-op (single-writer WAL semantics plus the busy retry cover it).
func lockConfigRuntimeTx(tx *gorm.DB, runtimeScope string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "router-config:"+runtimeScope).Error; err != nil {
		return fmt.Errorf("lock runtime scope: %w", err)
	}
	return nil
}

// prepareGroupWrite loads the canonical document, applies the requested group
// mutation, validates the patched document end to end, and produces the
// per-target eligibility report. It returns the patched document bytes; no
// writes happen here.
func prepareGroupWrite(raw []byte, group string, req GroupTargetsWriteRequest, policy GroupWritePolicy, now time.Time) (patched []byte, before, after []GroupTargetSpec, strategyFrom, strategyTo string, reports []GroupTargetReport, err error) {
	var current Config
	if err = yaml.Unmarshal(raw, &current); err != nil {
		err = fmt.Errorf("parse active canonical YAML: %w", err)
		return
	}
	current.setDefaults()
	existing, ok := current.Models[group]
	if !ok {
		err = configAdminError(404, "group-not-found", "model group is not in the active configuration")
		return
	}
	// The target-write API currently accepts only a subset of Target fields.
	// Refuse embedding groups rather than dropping their space/dimension
	// invariants; operators can make these changes through canonical import.
	for _, target := range existing.Targets {
		resolved, resolveErr := current.resolveTarget(group, target)
		if resolveErr != nil {
			err = configAdminError(422, "config-invalid", "the existing group cannot be resolved")
			return
		}
		if isEmbeddingTarget(resolved) {
			err = configAdminError(422, "embedding-target-write-unsupported", "update embedding groups through canonical configuration import")
			return
		}
	}
	strategyFrom = existing.Strategy
	before = groupTargetSpecsFromGroup(existing)
	after = append([]GroupTargetSpec(nil), req.Targets...)
	if req.Strategy != "" {
		strategyTo = req.Strategy
	}

	// Empty-group refusal comes before per-target checks so the error is
	// unambiguous.
	if len(after) == 0 {
		err = configAdminError(422, "group-would-be-empty", "the change would leave the group without targets; refusing to empty a group")
		return
	}

	// Build the patched group for validation. Specs become targets; resolved
	// catalog metadata is filled by resolveTarget during report generation
	// and again by Config.Validate below.
	patchedGroup := existing
	patchedGroup.Targets = make([]Target, 0, len(after))
	for _, spec := range after {
		target := Target{Provider: spec.Provider, Model: spec.Model, ModelRef: spec.ModelRef, Region: spec.Region, Tags: append([]string(nil), spec.Tags...), Validation: spec.Validation.toTargetValidation()}
		if spec.Weight > 0 {
			target.Weight = spec.Weight
		}
		patchedGroup.Targets = append(patchedGroup.Targets, target)
	}
	if strategyTo != "" {
		patchedGroup.Strategy = strategyTo
	}
	reports, _ = validateGroupTargetSpecs(&current, group, existing, after, policy, now)
	if eligErr := groupWriteEligibilityError(reports, req.Force, policy); eligErr != nil {
		err = eligErr
		return
	}
	// Effective eligibility for shape coverage: with force, a target whose
	// only failure is the validation-status policy still counts (the write
	// is explicitly forced past that gate); catalog and contract failures
	// never count.
	eligibleForCoverage := func(i int) bool {
		if reports[i].Eligible {
			return true
		}
		if !req.Force {
			return false
		}
		for _, check := range reports[i].Checks {
			if !check.OK && check.Name != "validation_status" {
				return false
			}
		}
		return true
	}
	// Contract shape coverage: every API shape the contract supports must
	// keep at least one eligible target.
	if existing.Contract != nil {
		for _, shape := range existing.Contract.SupportedAPIShapes {
			callerDialect := normalizeContractAPIShape(shape)
			if callerDialect == "" {
				continue
			}
			covered := false
			for i, spec := range after {
				if !eligibleForCoverage(i) {
					continue
				}
				resolvedTarget, resolveErr := current.resolveTarget(group, Target{Provider: spec.Provider, Model: spec.Model, ModelRef: spec.ModelRef, Region: spec.Region, Tags: spec.Tags, Validation: spec.Validation.toTargetValidation()})
				if resolveErr != nil {
					continue
				}
				outDialect := targetDialect(current.Provider[resolvedTarget.Provider], resolvedTarget)
				if targetPassesContract(existing.Contract, resolvedTarget, outDialect, callerDialect, nil, dynamicStats{}, now) == "" {
					covered = true
					break
				}
			}
			if !covered {
				err = configAdminError(422, "group-shape-uncovered", "the change would leave contract API shape "+callerDialect+" without an eligible target")
				return
			}
		}
	}

	// Whole-document validation on the patched form, mirroring the import
	// path. The document stays unexpanded; environment references are
	// preserved verbatim.
	patched, err = PatchCanonicalModelGroupYAML(raw, group, func(_ ModelGroup) (ModelGroup, error) {
		return patchedGroup, nil
	})
	if err != nil {
		return
	}
	var candidate Config
	if uerr := yaml.Unmarshal(patched, &candidate); uerr != nil {
		err = fmt.Errorf("parse patched canonical YAML: %w", uerr)
		return
	}
	candidate.setDefaults()
	if verr := candidate.ValidateForImport(); verr != nil {
		err = configAdminError(422, "config-invalid", "patched configuration fails validation: "+truncateSafeDetail(verr.Error()))
		return
	}
	return
}

// PatchCanonicalModelGroupYAML replaces one model group in a canonical
// document, preserving every other key byte-for-byte at the YAML level (the
// document is re-marshaled as a mapping, matching PatchCanonicalCallersYAML).
func PatchCanonicalModelGroupYAML(raw []byte, group string, mutate func(ModelGroup) (ModelGroup, error)) ([]byte, error) {
	if mutate == nil {
		return nil, errors.New("mutate callback is required")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse canonical config YAML: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	models := map[string]ModelGroup{}
	if existing, ok := doc["models"]; ok && existing != nil {
		encoded, err := yaml.Marshal(existing)
		if err != nil {
			return nil, fmt.Errorf("re-encode models map: %w", err)
		}
		if err := yaml.Unmarshal(encoded, &models); err != nil {
			return nil, fmt.Errorf("decode models map: %w", err)
		}
	}
	groupConfig, ok := models[group]
	if !ok {
		return nil, configAdminError(404, "group-not-found", "model group is not in the active configuration")
	}
	patchedGroup, err := mutate(groupConfig)
	if err != nil {
		return nil, err
	}
	models[group] = patchedGroup
	doc["models"] = models
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical config YAML: %w", err)
	}
	return out, nil
}

// lookupIdempotentGroupWrite finds a recorded write for an idempotency key.
// conflict is true when the key exists with a different request hash.
func lookupIdempotentGroupWrite(tx *gorm.DB, runtimeScope, key, requestHash, group string) (replay *GroupTargetsWriteResult, conflict bool, err error) {
	var rows []struct {
		ConfigSetID    string `gorm:"column:config_set_id"`
		PrevConfigSet  string `gorm:"column:prev_config_set_id"`
		ConfigRevision int64  `gorm:"column:config_revision"`
		RequestHash    string `gorm:"column:request_hash"`
	}
	if err = tx.Table("router_config_change_events").
		Select("config_set_id", "prev_config_set_id", "config_revision", "request_hash").
		Where("runtime_scope = ? AND idempotency_key = ?", runtimeScope, key).
		Limit(1).Find(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	if rows[0].RequestHash != requestHash {
		return nil, true, nil
	}
	return &GroupTargetsWriteResult{
		Group:         group,
		SetID:         rows[0].ConfigSetID,
		PreviousSetID: rows[0].PrevConfigSet,
		Revision:      rows[0].ConfigRevision,
		Replayed:      true,
		Targets:       []GroupTargetReport{},
		Diff:          GroupTargetsDiff{Added: []string{}, Removed: []string{}, Reordered: []string{}},
	}, false, nil
}

// RollbackGroup restores one group to its state in a prior configuration set
// (default: the set active before the last change to this group), as a new
// copy-on-write set. One call, fully audited.
func RollbackGroup(db *gorm.DB, runtimeScope, group, fromSetID, reason, actor string, now time.Time) (*GroupTargetsWriteResult, error) {
	if db == nil {
		return nil, configAdminError(409, "config_source_mode_file", "the router serves configuration from a YAML file; enable server.config_source.mode: database and import the current YAML to use Admin API writes")
	}
	group = strings.TrimSpace(group)
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, errors.New("config actor is required")
	}
	var result *GroupTargetsWriteResult
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := lockConfigRuntimeTx(tx, runtimeScope); err != nil {
			return err
		}
		if err := ensureConfigAdminWriteSchema(tx); err != nil {
			return err
		}
		state, _, err := loadConfigAdminRuntime(tx, runtimeScope)
		if err != nil {
			return err
		}
		sourceSet := strings.TrimSpace(fromSetID)
		if sourceSet == "" {
			// Default: the set active before the most recent change to this
			// group.
			var events []struct {
				PrevConfigSet string `gorm:"column:prev_config_set_id"`
			}
			if err := tx.Table("router_config_change_events").
				Select("prev_config_set_id").
				Where("runtime_scope = ? AND group_name = ? AND prev_config_set_id != ''", runtimeScope, group).
				Order("at DESC").Limit(1).Find(&events).Error; err != nil {
				return fmt.Errorf("load group history: %w", err)
			}
			if len(events) == 0 {
				return configAdminError(409, "group-rollback-unavailable", "no prior configuration set is recorded for this group")
			}
			sourceSet = events[0].PrevConfigSet
		}
		sourceRaw, err := ExportCanonicalYAML(tx, sourceSet)
		if err != nil {
			return configAdminError(404, "config-set-not-found", "the requested prior configuration set does not exist")
		}
		var sourceConfig Config
		if err := yaml.Unmarshal(sourceRaw, &sourceConfig); err != nil {
			return fmt.Errorf("parse prior canonical YAML: %w", err)
		}
		sourceConfig.setDefaults()
		restored, ok := sourceConfig.Models[group]
		if !ok {
			return configAdminError(422, "group-not-in-prior-set", "the prior set has no such group")
		}
		activeRaw, err := ExportCanonicalYAML(tx, state.ActiveSetID)
		if err != nil {
			return err
		}
		var current Config
		if err := yaml.Unmarshal(activeRaw, &current); err != nil {
			return fmt.Errorf("parse active canonical YAML: %w", err)
		}
		current.setDefaults()
		existing, ok := current.Models[group]
		if !ok {
			return configAdminError(404, "group-not-found", "model group is not in the active configuration")
		}
		before := groupTargetSpecsFromGroup(existing)
		patched, err := PatchCanonicalModelGroupYAML(activeRaw, group, func(_ ModelGroup) (ModelGroup, error) {
			return restored, nil
		})
		if err != nil {
			return err
		}
		var candidate Config
		if err := yaml.Unmarshal(patched, &candidate); err != nil {
			return fmt.Errorf("parse patched canonical YAML: %w", err)
		}
		candidate.setDefaults()
		if err := candidate.Validate(); err != nil {
			return configAdminError(422, "config-invalid", "restored configuration fails validation: "+truncateSafeDetail(err.Error()))
		}
		newSetID, err := importCanonicalDocumentTx(tx, runtimeScope, "admin-api:group-rollback:"+group, patched, &candidate, actor, now)
		if err != nil {
			return err
		}
		revision, err := activateConfigSetGuardedTx(tx, runtimeScope, newSetID, state.Revision, now)
		if err != nil {
			return err
		}
		after := groupTargetSpecsFromGroup(restored)
		if _, err := insertConfigChangeEventTx(tx, runtimeScope, newSetID, configActionGroupRollback, actor, revision, configChangeEventMeta{
			GroupName:     group,
			PrevSetID:     state.ActiveSetID,
			Reason:        reason,
			Detail:        "restored_from_set=" + sourceSet,
			BeforeTargets: before,
			AfterTargets:  after,
		}, now); err != nil {
			return err
		}
		result = &GroupTargetsWriteResult{
			Group: group, SetID: newSetID, PreviousSetID: state.ActiveSetID, Revision: revision,
			Diff: diffGroupTargetSpecs(before, after), Targets: []GroupTargetReport{},
			Strategy: restored.Strategy, RestoredFromSet: sourceSet,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GroupHistoryEvent is one audit record for the group history endpoint.
type GroupHistoryEvent struct {
	EventID        string            `json:"event_id"`
	Action         string            `json:"action"`
	Actor          string            `json:"actor"`
	At             string            `json:"at"`
	ConfigSetID    string            `json:"config_set_id"`
	PreviousSetID  string            `json:"previous_set_id,omitempty"`
	Revision       int64             `json:"revision"`
	Reason         string            `json:"reason,omitempty"`
	Forced         bool              `json:"forced"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Detail         string            `json:"detail,omitempty"`
	BeforeTargets  []GroupTargetSpec `json:"before_targets,omitempty"`
	AfterTargets   []GroupTargetSpec `json:"after_targets,omitempty"`
}

// ListGroupHistory returns the change events recorded for one group, newest
// first. Events predate phase 8 (or come from the generic set endpoints) when
// they carry no group name and are not listed here.
func ListGroupHistory(db *gorm.DB, runtimeScope, group string, limit int) ([]GroupHistoryEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []struct {
		EventID        string `gorm:"column:event_id"`
		Action         string `gorm:"column:action"`
		Actor          string `gorm:"column:actor"`
		At             string `gorm:"column:at"`
		ConfigSetID    string `gorm:"column:config_set_id"`
		PrevSetID      string `gorm:"column:prev_config_set_id"`
		Revision       int64  `gorm:"column:config_revision"`
		Reason         string `gorm:"column:reason"`
		Forced         int    `gorm:"column:forced"`
		IdempotencyKey string `gorm:"column:idempotency_key"`
		Detail         string `gorm:"column:detail"`
	}
	if err := db.Table("router_config_change_events").
		Select("event_id", "action", "actor", "at", "config_set_id", "prev_config_set_id", "config_revision", "reason", "forced", "idempotency_key", "detail").
		Where("runtime_scope = ? AND group_name = ?", runtimeScope, group).
		Order("at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load group history: %w", err)
	}
	out := make([]GroupHistoryEvent, 0, len(rows))
	for _, row := range rows {
		event := GroupHistoryEvent{
			EventID: row.EventID, Action: row.Action, Actor: row.Actor, At: row.At,
			ConfigSetID: row.ConfigSetID, PreviousSetID: row.PrevSetID, Revision: row.Revision,
			Reason: row.Reason, Forced: row.Forced != 0, IdempotencyKey: row.IdempotencyKey, Detail: row.Detail,
		}
		var targets []struct {
			Phase    string `gorm:"column:phase"`
			Provider string `gorm:"column:provider_name"`
			ModelRef string `gorm:"column:model_ref"`
			Model    string `gorm:"column:model"`
		}
		if err := db.Table("router_config_change_event_targets").
			Select("phase", "provider_name", "model_ref", "model").
			Where("event_id = ?", row.EventID).Order("phase ASC, sequence ASC").Find(&targets).Error; err != nil {
			return nil, fmt.Errorf("load group history targets: %w", err)
		}
		for _, target := range targets {
			spec := GroupTargetSpec{Provider: target.Provider, ModelRef: target.ModelRef, Model: target.Model}
			if target.Phase == "before" {
				event.BeforeTargets = append(event.BeforeTargets, spec)
			} else {
				event.AfterTargets = append(event.AfterTargets, spec)
			}
		}
		out = append(out, event)
	}
	return out, nil
}

// truncateSafeDetail bounds validation detail in API errors. The text comes
// from Config.Validate, which names config keys but never secret values.
func truncateSafeDetail(detail string) string {
	const maxDetail = 300
	if len(detail) > maxDetail {
		return detail[:maxDetail] + "..."
	}
	return detail
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// sortedGroupNames is a small helper for deterministic list responses.
func sortedGroupNames(groups map[string]ModelGroup) []string {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
