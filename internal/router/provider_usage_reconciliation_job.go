// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"log"
	"time"
)

const providerUsageReconciliationMaxRun = 15 * time.Minute

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
			now := time.Now().UTC()
			next := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			runtime := s.providerUsageRuntimeConfig()
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
	dayEnd := now.UTC().Truncate(24 * time.Hour)
	dayStart := dayEnd.Add(-24 * time.Hour)
	ctx, cancel := context.WithTimeout(parent, providerUsageReconciliationMaxRun)
	defer cancel()
	var firstErr error
	providers := runtime.Server.ProviderUsageReconciliation.Providers
	providerTimeout := time.Duration(runtime.Server.ProviderUsageReconciliation.TimeoutMS) * time.Millisecond
	if providerTimeout <= 0 {
		providerTimeout = 10 * time.Second
	}
	maximumShare := providerUsageReconciliationMaxRun / time.Duration(max(1, len(providers)))
	if providerTimeout > maximumShare {
		providerTimeout = maximumShare
	}
	for _, entry := range providers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		perProvider, stop := context.WithTimeout(ctx, providerTimeout)
		reconciler := providerUsageReconciler{
			usage: s.usage, source: source,
			threshold: runtime.Server.ProviderUsageReconciliation.ThresholdUSD,
			onGap:     s.recordProviderUsageGap,
		}
		_, err := reconciler.reconcile(perProvider, entry.Provider, dayStart, dayEnd)
		timedOut := perProvider.Err() != nil
		stop()
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if errors.Is(err, errProviderUsageSourceFailed) {
			statusCode, reasonCode := 502, "provider_usage_source_failed"
			if timedOut {
				statusCode, reasonCode = 504, "provider_usage_timeout"
			}
			_ = s.recordProviderUsageFailure(entry.Provider, dayStart, dayEnd, statusCode, reasonCode)
		}
	}
	return firstErr
}

func (s *Service) recordProviderUsageFailure(provider string, from, to time.Time, statusCode int, reasonCode string) error {
	if s == nil || s.usage == nil || s.usage.db == nil {
		return errors.New("provider usage failure event store is unavailable")
	}
	if provider == "" || !to.After(from) || (statusCode != 502 && statusCode != 504) || (reasonCode != "provider_usage_source_failed" && reasonCode != "provider_usage_timeout") {
		return errors.New("provider usage failure event is invalid")
	}
	s.usage.EmitSecurityAccessEvent(securityAccessEvent{
		TS:             time.Now().UTC(),
		EventType:      "provider_usage_failure",
		Surface:        "provider_usage_reconciliation",
		HTTPMethod:     "scheduled",
		PathTemplate:   "daily",
		StatusCode:     statusCode,
		Outcome:        "failed",
		ReasonCode:     reasonCode,
		ModelGroup:     provider,
		RequestedModel: formatUsageTime(from) + "/" + formatUsageTime(to),
	})
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
