// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	detect "github.com/metrum-ai/router/internal/security/detect"
	"github.com/metrum-ai/router/pkg/detector"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type automaticContainmentState struct {
	Action                 string
	ThrottlePercent        int
	UnlimitedThrottleLimit int
	ExpiresAt              time.Time
}

func (c *callerRuntime) effectiveRateLimit(base int) int {
	if c == nil {
		return base
	}
	state := c.autoContainment.Load()
	if state == nil || state.Action != "throttle" || !time.Now().UTC().Before(state.ExpiresAt) || state.ThrottlePercent <= 0 || state.ThrottlePercent > 100 {
		return base
	}
	if base <= 0 {
		base = state.UnlimitedThrottleLimit
		if base <= 0 {
			return 0
		}
	}
	limit := base * state.ThrottlePercent / 100
	if limit < 1 {
		return 1
	}
	return limit
}

func firstDetectorEvidenceRequestID(signal *detector.Signal) string {
	if signal == nil || len(signal.EvidenceRequestIDs) == 0 {
		return ""
	}
	return signal.EvidenceRequestIDs[0]
}

func detectorSignalEvidence(signal *detector.Signal) string {
	if signal == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("severity=%s", signal.Severity), fmt.Sprintf("score=%.4f", signal.Score)}
	if signal.SegmentKind != "" {
		parts = append(parts, "segment_kind="+signal.SegmentKind)
	}
	if signal.SegmentIndex != nil {
		parts = append(parts, fmt.Sprintf("segment_index=%d", *signal.SegmentIndex))
	}
	return strings.Join(parts, ";")
}

// applyAutomaticContainmentSuspension uses the same canonical config mutation
// and containment ownership marker as the #883 admin/containment API. This
// keeps both resume paths available and makes the detector the recorded actor.
func (s *Service) applyAutomaticContainmentSuspension(callerID, detectorID string, ttlMS int) error {
	if s == nil || s.configControlPlane == nil || s.cfg == nil {
		return errors.New("automatic suspension requires database config control plane")
	}
	body := fmt.Sprintf(`{"caller_id":%q,"action":"suspend","reason":"detector_action","ttl":%q}`,
		callerID, (time.Duration(ttlMS) * time.Millisecond).String())
	r := httptest.NewRequest(http.MethodPost, "/admin/callers/containment", strings.NewReader(body))
	ctx := WithPrincipal(r.Context(), Principal{Subject: detectorID, Kind: "detector"})
	ctx = WithAdminAuditMeta(ctx, AdminAuditMeta{
		Subject: detectorID, Kind: "detector", Domain: AdminGlobalDomain,
		Object: authzObjectAdminContainment, Action: authzActionContain, OperationID: "automatic_containment",
	})
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h := newCallerAdminHandler(CallerAdminDeps{
		DB: s.configControlPlane, RuntimeScope: s.cfg.Server.ConfigSource.RuntimeScope,
		Authorize: func(r *http.Request, object, action string) bool {
			principal, ok := PrincipalFrom(r.Context())
			return ok && principal.Kind == "detector" && principal.Subject == detectorID && object == authzObjectAdminContainment && action == authzActionContain
		},
		Now: func() time.Time { return time.Now().UTC() },
		AfterMutate: func(*http.Request, string) error {
			if s.quota == nil || s.cfg.Server.ConfigSource.Mode != "database" {
				return nil
			}
			return s.reloadActiveConfigFromDB()
		},
	})
	h.handleContainment(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("containment API returned status %d", w.Code)
	}
	return nil
}

func (s *Service) applyAutomaticContainmentResult(result detect.Result) {
	if s == nil || !result.Enforce || result.Err != nil || result.CallerID == "" {
		return
	}
	cfgSource := s.cfg
	if s.configSnapshot != nil {
		if snapshot := s.configSnapshot.Load(); snapshot != nil {
			cfgSource = snapshot
		}
	}
	if cfgSource == nil {
		return
	}
	cfg := cfgSource.Server.AutoContainment
	if cfg.Mode == "" || cfg.Mode == "off" {
		return
	}
	exempt := false
	for _, caller := range cfg.ExemptCallers {
		if caller == result.CallerID {
			exempt = true
			break
		}
	}
	if exempt {
		return
	}
	var matched *AutoContainmentRuleConfig
	var matchedSignal *detector.Signal
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		if rule.DetectorID == result.DetectorID {
			for j := range result.Signals {
				signal := &result.Signals[j]
				if signal.Severity == rule.Severity {
					matched = rule
					matchedSignal = signal
					break
				}
			}
		}
		if matched != nil {
			break
		}
	}
	if matched == nil {
		return
	}
	if cfg.Mode == "shadow" {
		if s.usage == nil || s.usage.db == nil {
			return
		}
		now := time.Now().UTC()
		row := automaticContainmentActionRecord{
			CallerID: result.CallerID, DetectorID: result.DetectorID, Action: matched.Action,
			ThrottlePercent: matched.ThrottlePercent,
			CreatedAt:       formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Duration(matched.TTLMS) * time.Millisecond)),
			RequestID: firstDetectorEvidenceRequestID(matchedSignal), Evidence: detectorSignalEvidence(matchedSignal),
		}
		created, err := s.usage.recordAutomaticContainmentShadow(row, cfg.MaxActionsPerMinute, now)
		if err != nil {
			log.Printf("automatic containment shadow record failed: storage-error")
			return
		}
		if !created {
			return
		}
		s.recordSecurityAccessEvent(securityAccessEvent{
			TS: now, EventType: "security_containment_shadow", Outcome: matched.Action,
			ReasonCode: "detector_action_proposed:" + result.DetectorID + ":" + matched.Action,
			CallerID:   result.CallerID,
		})
		return
	}
	if cfg.Mode != "enforce" || s.usage == nil || s.usage.db == nil {
		return
	}
	if matched.Action == "suspend" && s.configControlPlane == nil {
		log.Printf("automatic containment suspend refused: database config control plane required")
		return
	}
	now := time.Now().UTC()
	row := automaticContainmentActionRecord{
		CallerID: result.CallerID, DetectorID: result.DetectorID, Action: matched.Action,
		ThrottlePercent: matched.ThrottlePercent,
		CreatedAt:       formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Duration(matched.TTLMS) * time.Millisecond)),
		RequestID: firstDetectorEvidenceRequestID(matchedSignal), Evidence: detectorSignalEvidence(matchedSignal),
	}
	created, err := s.usage.applyAutomaticContainment(row, cfg.MaxActionsPerMinute, now)
	if err != nil {
		log.Printf("automatic containment action was not persisted: storage-error")
		return
	}
	if created {
		if matched.Action == "suspend" {
			if err := s.applyAutomaticContainmentSuspension(result.CallerID, result.DetectorID, matched.TTLMS); err != nil {
				log.Printf("automatic containment suspend failed: control-plane-error")
				return
			}
		} else {
			s.syncAutomaticContainment()
		}
		s.recordSecurityAccessEvent(securityAccessEvent{
			TS: now, EventType: "security_containment_action", Outcome: matched.Action,
			ReasonCode: fmt.Sprintf("detector_action:%s:%s", result.DetectorID, matched.Action), CallerID: result.CallerID,
		})
	}
}

func (s *Service) syncAutomaticContainment() {
	if s == nil || s.usage == nil || s.usage.db == nil || s.quota == nil {
		return
	}
	rows, err := s.usage.activeAutomaticContainments(time.Now().UTC())
	if err != nil {
		log.Printf("automatic containment state refresh failed: storage-error")
		return
	}
	active := make(map[string]*automaticContainmentState, len(rows))
	for _, row := range rows {
		expires, err := time.Parse(time.RFC3339Nano, row.ExpiresAt)
		if err != nil || !time.Now().UTC().Before(expires) {
			continue
		}
		// Suspensions are authoritative in the #883 canonical config plane so
		// containment and admin resume paths can clear them. Only throttles live
		// in this usage-store projection.
		if row.Action == "throttle" {
			cfg := s.cfg
			if s.configSnapshot != nil && s.configSnapshot.Load() != nil {
				cfg = s.configSnapshot.Load()
			}
			unlimitedLimit := 0
			if cfg != nil {
				unlimitedLimit = cfg.Server.AutoContainment.UnlimitedThrottleLimit
			}
			active[row.CallerID] = &automaticContainmentState{Action: row.Action, ThrottlePercent: row.ThrottlePercent, UnlimitedThrottleLimit: unlimitedLimit, ExpiresAt: expires}
		}
	}
	s.quota.mu.Lock()
	for callerID, caller := range s.quota.callers {
		caller.autoContainment.Store(active[callerID])
	}
	s.quota.mu.Unlock()
}

func (u *usageStore) applyAutomaticContainment(row automaticContainmentActionRecord, cap int, now time.Time) (bool, error) {
	if u == nil || u.db == nil || cap < 1 {
		return false, errors.New("automatic containment storage unavailable")
	}
	created := false
	err := u.db.Transaction(func(tx *gorm.DB) error {
		var guard automaticContainmentGuardRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&guard, 1).Error; err != nil {
			return err
		}
		var existing automaticContainmentActionRecord
		existingQuery := tx.Where("caller_id = ?", row.CallerID).Find(&existing)
		if existingQuery.Error != nil {
			return existingQuery.Error
		}
		if existingQuery.RowsAffected != 0 {
			if existing.ExpiresAt > formatUsageTime(now) && !(existing.Action == "throttle" && row.Action == "suspend") {
				return nil
			}
		}
		if err := tx.Where("created_at < ?", formatUsageTime(now.Add(-time.Minute))).Delete(&automaticContainmentEventRecord{}).Error; err != nil {
			return err
		}
		var recent int64
		if err := tx.Model(&automaticContainmentEventRecord{}).Where("created_at >= ?", formatUsageTime(now.Add(-time.Minute))).Count(&recent).Error; err != nil {
			return err
		}
		if recent >= int64(cap) {
			return nil
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := tx.Create(&automaticContainmentEventRecord{CallerID: row.CallerID, DetectorID: row.DetectorID, Action: row.Action, CreatedAt: row.CreatedAt}).Error; err != nil {
			return err
		}
		if err := insertAdminAuditUsageTx(tx, AdminAuditRecord{
			At: now, PrincipalSubject: row.DetectorID, PrincipalKind: "detector",
			Object: "caller_key", Action: row.Action, OperationID: "detector_containment",
			RequestID: row.RequestID, Outcome: "committed", ReasonCode: "detector_action:" + row.DetectorID + ":" + row.Action,
			Detail: row.Evidence + ";expires_at=" + row.ExpiresAt,
		}); err != nil {
			return err
		}
		if err := tx.Model(&automaticContainmentGuardRecord{}).Where("id = 1").UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
			return err
		}
		if tx.Dialector.Name() == "postgres" {
			queueAutomaticContainmentNotification(tx)
		}
		created = true
		return nil
	})
	return created, err
}

func (u *usageStore) recordAutomaticContainmentShadow(row automaticContainmentActionRecord, cap int, now time.Time) (bool, error) {
	if u == nil || u.db == nil || cap < 1 {
		return false, errors.New("automatic containment storage unavailable")
	}
	created := false
	err := u.db.Transaction(func(tx *gorm.DB) error {
		var guard automaticContainmentGuardRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&guard, 1).Error; err != nil {
			return err
		}
		if err := tx.Where("created_at < ?", formatUsageTime(now.Add(-time.Minute))).Delete(&automaticContainmentEventRecord{}).Error; err != nil {
			return err
		}
		var recent int64
		if err := tx.Model(&automaticContainmentEventRecord{}).Where("created_at >= ?", formatUsageTime(now.Add(-time.Minute))).Count(&recent).Error; err != nil {
			return err
		}
		if recent >= int64(cap) {
			return nil
		}
		if err := tx.Create(&automaticContainmentEventRecord{CallerID: row.CallerID, DetectorID: row.DetectorID, Action: row.Action, CreatedAt: row.CreatedAt}).Error; err != nil {
			return err
		}
		if err := insertAdminAuditUsageTx(tx, AdminAuditRecord{
			At: now, PrincipalSubject: row.DetectorID, PrincipalKind: "detector",
			Object: "caller_key", Action: row.Action, OperationID: "detector_containment",
			RequestID: row.RequestID, Outcome: "shadow", ReasonCode: "detector_action_proposed:" + row.DetectorID + ":" + row.Action,
			Detail: row.Evidence + ";proposed_expires_at=" + row.ExpiresAt,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

func (u *usageStore) activeAutomaticContainments(now time.Time) ([]automaticContainmentActionRecord, error) {
	if u == nil || u.db == nil {
		return nil, errors.New("automatic containment storage unavailable")
	}
	var rows []automaticContainmentActionRecord
	err := u.db.Where("expires_at > ?", formatUsageTime(now)).Find(&rows).Error
	return rows, err
}

const automaticContainmentNotifyChannel = "router_auto_containment"

func queueAutomaticContainmentNotification(tx *gorm.DB) {
	if err := tx.Exec("SAVEPOINT router_auto_containment_notify").Error; err != nil {
		log.Printf("automatic containment notification unavailable; polling remains active")
		return
	}
	if err := tx.Exec("SELECT pg_notify(?, ?)", automaticContainmentNotifyChannel, "updated").Error; err != nil {
		_ = tx.Exec("ROLLBACK TO SAVEPOINT router_auto_containment_notify").Error
		log.Printf("automatic containment notification failed; polling remains active")
	}
	if err := tx.Exec("RELEASE SAVEPOINT router_auto_containment_notify").Error; err != nil {
		log.Printf("automatic containment notification cleanup failed; polling remains active")
	}
}

func (s *Service) startAutomaticContainment(ctx context.Context) (func(), error) {
	if s == nil || s.cfg == nil || s.cfg.Server.AutoContainment.Mode == "" || s.cfg.Server.AutoContainment.Mode == "off" {
		return func() {}, nil
	}
	if s.usage == nil || s.usage.db == nil {
		return nil, errors.New("automatic containment requires usage database")
	}
	if !s.usage.db.Migrator().HasTable(&automaticContainmentActionRecord{}) || !s.usage.db.Migrator().HasTable(&automaticContainmentGuardRecord{}) || !s.usage.db.Migrator().HasTable(usageAdminAuditTable) {
		return nil, errors.New("automatic containment schema is not migrated; run router-migrate")
	}
	s.syncAutomaticContainment()
	stopPoll, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		polls := 0
		for {
			select {
			case <-stopPoll.Done():
				return
			case <-ticker.C:
				polls++
				if polls%60 == 0 {
					if err := s.usage.deleteExpiredAutomaticContainments(time.Now().UTC()); err != nil {
						log.Printf("automatic containment expiry cleanup failed: storage-error")
					}
				}
				s.syncAutomaticContainment()
			}
		}
	}()
	stopNotify, notifyErr := startAutomaticContainmentNotifier(stopPoll, s.usage.db, s.syncAutomaticContainment)
	if notifyErr != nil {
		log.Printf("automatic containment notifier unavailable; one-second polling remains enabled")
		stopNotify = func() {}
	}
	return func() { cancel(); stopNotify(); <-done }, nil
}

func (u *usageStore) deleteExpiredAutomaticContainments(now time.Time) error {
	if u == nil || u.db == nil {
		return errors.New("automatic containment storage unavailable")
	}
	return u.db.Where("expires_at <= ?", formatUsageTime(now)).Delete(&automaticContainmentActionRecord{}).Error
}

func startAutomaticContainmentNotifier(ctx context.Context, db *gorm.DB, reload func()) (func(), error) {
	if db == nil || reload == nil {
		return nil, errors.New("automatic containment notifier requires database and callback")
	}
	return startPostgresNotifier(ctx, db, automaticContainmentNotifyChannel, "updated", reload, "automatic containment")
}

func (s *Service) configureAutomaticContainment(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.autoContainmentMu.Lock()
	defer s.autoContainmentMu.Unlock()
	cfg := s.cfg
	if s.configSnapshot != nil && s.configSnapshot.Load() != nil {
		cfg = s.configSnapshot.Load()
	}
	enabled := cfg != nil && cfg.Server.AutoContainment.Mode != "" && cfg.Server.AutoContainment.Mode != "off"
	if !enabled {
		if s.autoContainmentStop != nil {
			s.autoContainmentStop()
			s.autoContainmentStop = nil
		}
		return nil
	}
	if s.autoContainmentStop != nil {
		return nil
	}
	stop, err := s.startAutomaticContainment(ctx)
	if err != nil {
		return err
	}
	s.autoContainmentStop = stop
	return nil
}

func (s *Service) stopAutomaticContainment() {
	if s == nil {
		return
	}
	s.autoContainmentMu.Lock()
	defer s.autoContainmentMu.Unlock()
	if s.autoContainmentStop != nil {
		s.autoContainmentStop()
		s.autoContainmentStop = nil
	}
}
