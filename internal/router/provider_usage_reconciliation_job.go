// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	providerUsageReconciliationMaxRun = 15 * time.Minute
	providerUsageDefaultScheduleHour  = 6
	providerUsageDefaultCatchUpDays   = 7
)

func providerUsageScheduleHour(cfg ProviderUsageReconciliationConfig) int {
	if cfg.ScheduleHourUTC == nil {
		return providerUsageDefaultScheduleHour
	}
	return *cfg.ScheduleHourUTC
}

func nextProviderUsageRun(now time.Time, hour int) time.Time {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func (s *Service) startProviderUsageReconciliation() {
	if s == nil || s.cfg == nil || s.usage == nil {
		return
	}
	runtime := s.providerUsageRuntimeConfig()
	if runtime == nil || !runtime.Server.ProviderUsageReconciliation.Enabled {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.providerUsageStop = cancel
	go func() {
		for {
			runtime := s.providerUsageRuntimeConfig()
			if runtime == nil || !runtime.Server.ProviderUsageReconciliation.Enabled {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Minute):
					continue
				}
			}
			next := nextProviderUsageRun(time.Now(), providerUsageScheduleHour(runtime.Server.ProviderUsageReconciliation))
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			runtime = s.providerUsageRuntimeConfig()
			if runtime == nil || !runtime.Server.ProviderUsageReconciliation.Enabled {
				continue
			}
			client := newProviderUsageClient(runtime.Server.ProviderUsageReconciliation, runtime.Provider, nil)
			s.runProviderUsageReconciliationScheduled(ctx, client, time.Now().UTC())
		}
	}()
}

func (s *Service) runProviderUsageReconciliationScheduled(ctx context.Context, source ProviderUsageSource, now time.Time) {
	if err := s.runProviderUsageReconciliation(ctx, source, now); err != nil {
		log.Printf("provider usage reconciliation run failed")
	}
}

func (s *Service) runProviderUsageReconciliation(parent context.Context, source ProviderUsageSource, now time.Time) error {
	runtime := s.providerUsageRuntimeConfig()
	if runtime == nil || s.usage == nil || source == nil || !runtime.Server.ProviderUsageReconciliation.Enabled {
		return nil
	}
	cfg := runtime.Server.ProviderUsageReconciliation
	dayEnd := now.UTC().Truncate(24 * time.Hour)
	ctx, cancel := context.WithTimeout(parent, providerUsageReconciliationMaxRun)
	defer cancel()
	firstErr := error(nil)
	providers := cfg.Providers
	providerTimeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if providerTimeout <= 0 {
		providerTimeout = 10 * time.Second
	}
	maximumShare := providerUsageReconciliationMaxRun / time.Duration(max(1, len(providers)))
	if providerTimeout > maximumShare {
		providerTimeout = maximumShare
	}
	catchUp := cfg.MaxCatchUpDays
	if catchUp == 0 {
		catchUp = providerUsageDefaultCatchUpDays
	}
	catchUpFloor := dayEnd.Add(-time.Duration(catchUp) * 24 * time.Hour)
	for _, entry := range providers {
		start := dayEnd.Add(-24 * time.Hour)
		var checkpoint securityAccessEventRecord
		if err := s.usage.db.WithContext(ctx).Where("event_type = ? AND object = ?", "provider_usage_reconciled", entry.Provider).Order("operation_id DESC").First(&checkpoint).Error; err == nil {
			if last, parseErr := time.Parse("2006-01-02", checkpoint.OperationID); parseErr == nil {
				start = last.Add(24 * time.Hour)
			}
		}
		if checkpoint.OperationID != "" && start.Before(catchUpFloor) {
			start = catchUpFloor
		}
		if pending, ok := s.providerUsagePendingWindow(ctx, entry.Provider, checkpoint.OperationID); ok && pending.Before(start) {
			start = pending
		}
		if start.Before(catchUpFloor) {
			start = catchUpFloor
		}
		for dayStart, days := start, 0; dayStart.Before(dayEnd) && days < catchUp; dayStart, days = dayStart.Add(24*time.Hour), days+1 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dayTo := dayStart.Add(24 * time.Hour)
			var err error
			var timedOut bool
			for attempt := 0; attempt < 3; attempt++ {
				perProvider, stop := context.WithTimeout(ctx, providerTimeout)
				reconciler := providerUsageReconciler{usage: s.usage, source: source, threshold: cfg.ThresholdUSD, onGap: s.recordProviderUsageGap}
				_, err = reconciler.reconcile(perProvider, entry.Provider, dayStart, dayTo)
				timedOut = perProvider.Err() != nil
				stop()
				if err == nil || errors.Is(err, errProviderUsagePartialData) || errors.Is(err, errProviderUsageAuthFailed) {
					break
				}
				if attempt < 2 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(time.Duration(1<<attempt) * 100 * time.Millisecond):
					}
				}
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				status, reason := 502, "provider_usage_source_failed"
				if timedOut {
					status, reason = 504, "provider_usage_timeout"
				}
				if errors.Is(err, errProviderUsageAuthFailed) {
					status, reason = providerUsageAuthStatus(err), "provider_usage_auth_error"
				}
				if errors.Is(err, errProviderUsagePartialData) {
					status, reason = 0, "provider_usage_data_partial"
				}
				_ = s.recordProviderUsageFailure(entry.Provider, dayStart, dayTo, status, reason)
				break
			}
			_ = s.recordProviderUsageCheckpoint(entry.Provider, dayStart)
		}
	}
	return firstErr
}

func (s *Service) providerUsagePendingWindow(ctx context.Context, provider, checkpoint string) (time.Time, bool) {
	var failures []securityAccessEventRecord
	if err := s.usage.db.WithContext(ctx).
		Where("object = ? AND event_type IN ?", provider, []string{"provider_usage_failure", "provider_usage_partial"}).
		Order("operation_id ASC").Limit(1000).Find(&failures).Error; err != nil {
		return time.Time{}, false
	}
	var earliest time.Time
	lastCheckpoint, _ := time.Parse("2006-01-02", checkpoint)
	for _, failure := range failures {
		windowStart, _, ok := strings.Cut(failure.OperationID, "/")
		if !ok {
			continue
		}
		start, err := time.Parse("2006-01-02T15:04:05.000000000Z", windowStart)
		if err != nil || !start.After(lastCheckpoint) {
			continue
		}
		start = start.UTC().Truncate(24 * time.Hour)
		if earliest.IsZero() || start.Before(earliest) {
			earliest = start
		}
	}
	return earliest, !earliest.IsZero()
}

func (s *Service) recordProviderUsageCheckpoint(provider string, day time.Time) error {
	s.recordSecurityAccessEvent(securityAccessEvent{TS: time.Now().UTC(), EventType: "provider_usage_reconciled", Surface: "provider_usage_reconciliation", HTTPMethod: "scheduled", PathTemplate: "daily", Outcome: "success", ReasonCode: "provider_usage_window_reconciled", Object: provider, Action: "reconcile", OperationID: day.UTC().Format("2006-01-02")})
	return nil
}

func (s *Service) recordProviderUsageFailure(provider string, from, to time.Time, statusCode int, reasonCode string) error {
	if s == nil || s.usage == nil || s.usage.db == nil {
		return errors.New("provider usage failure event store is unavailable")
	}
	valid := (statusCode == 502 && reasonCode == "provider_usage_source_failed") || (statusCode == 504 && reasonCode == "provider_usage_timeout") || ((statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) && reasonCode == "provider_usage_auth_error") || (statusCode == 0 && reasonCode == "provider_usage_data_partial")
	if provider == "" || !to.After(from) || !valid {
		return errors.New("provider usage failure event is invalid")
	}
	eventType := "provider_usage_failure"
	if statusCode == 0 {
		eventType = "provider_usage_partial"
	}
	s.recordSecurityAccessEvent(securityAccessEvent{TS: time.Now().UTC(), EventType: eventType, Surface: "provider_usage_reconciliation", HTTPMethod: "scheduled", PathTemplate: "daily", StatusCode: statusCode, Outcome: "failed", ReasonCode: reasonCode, Object: provider, Action: "reconcile", OperationID: formatUsageTime(from) + "/" + formatUsageTime(to)})
	return nil
}

func (s *Service) providerUsageRuntimeConfig() *Config {
	if s == nil {
		return nil
	}
	if s.configSnapshot != nil {
		return s.configSnapshot.Get()
	}
	return s.cfg
}
