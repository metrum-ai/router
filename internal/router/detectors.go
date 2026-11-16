// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	detect "github.com/metrum-ai/router/internal/security/detect"
	"github.com/metrum-ai/router/pkg/detector"
)

type detectorRuntime struct {
	config         DetectorConfig
	implementation detector.Detector
	segments       map[string]bool
}

func (s *Service) initDetectors() error {
	if len(s.cfg.Server.Detectors) > 0 {
		s.detectorMetricsEnabled.Store(true)
	}
	dispatcher, runtimes, err := buildDetectorRuntimes(s.cfg.Server.Detectors, s.handleDetectorResult, &s.detectorDropped)
	if err != nil {
		return err
	}
	s.detectorDispatcher = dispatcher
	s.detectors = runtimes
	return nil
}

func buildDetectorRuntimes(configs []DetectorConfig, sink func(detect.Result), drops *atomic.Uint64) (*detect.Dispatcher, []detectorRuntime, error) {
	if len(configs) == 0 {
		return nil, nil, nil
	}
	dispatcher := detect.NewDispatcherWithDropCounter(4, 256, sink, drops)
	runtimes := make([]detectorRuntime, 0, len(configs))
	for _, cfg := range configs {
		var implementation detector.Detector
		switch cfg.Builtin {
		case "honeytoken":
			implementation = detect.HoneytokenDetector{}
		case "spend_breaker":
			implementation = detect.SpendBreakerDetector{}
		default:
			external, err := newExternalDetector(cfg.URL, cfg.AllowHosts, cfg.AllowHTTP, time.Duration(cfg.TimeoutMS)*time.Millisecond)
			if err != nil {
				dispatcher.Close()
				return nil, nil, fmt.Errorf("initialize detector %s: %w", cfg.ID, err)
			}
			implementation = external
		}
		segments := make(map[string]bool, len(cfg.IncludeSegments))
		for _, kind := range cfg.IncludeSegments {
			segments[kind] = true
		}
		runtimes = append(runtimes, detectorRuntime{config: cfg, implementation: implementation, segments: segments})
	}
	return dispatcher, runtimes, nil
}

// replaceDetectorConfig installs the new runtime, then cancels the previous
// worker pool without waiting for detector I/O to finish.
func (s *Service) replaceDetectorConfig(configs []DetectorConfig) error {
	if len(configs) > 0 {
		s.detectorMetricsEnabled.Store(true)
	}
	dispatcher, runtimes, err := buildDetectorRuntimes(configs, s.handleDetectorResult, &s.detectorDropped)
	if err != nil {
		return err
	}
	s.detectorMu.Lock()
	previous := s.detectorDispatcher
	s.detectorDispatcher = dispatcher
	s.detectors = runtimes
	s.detectorMu.Unlock()
	if previous != nil {
		previous.Close()
	}
	return nil
}

func (s *Service) detectorSegmentsEnabled() bool {
	s.detectorMu.RLock()
	defer s.detectorMu.RUnlock()
	return detectorSegmentsConfigured(s.detectors)
}

func (s *Service) detectorDropMetrics() (bool, uint64) {
	s.detectorMu.RLock()
	dispatcher := s.detectorDispatcher
	enabled := s.detectorMetricsEnabled.Load()
	drops := s.detectorDropped.Load()
	if dispatcher != nil {
		enabled = true
		if active := dispatcher.Dropped(); active > drops {
			drops = active
		}
	}
	s.detectorMu.RUnlock()
	return enabled, drops
}

func (s *Service) dispatchCompletedRequestDetectors(rc *requestContext) {
	if rc == nil || rc.caller == nil {
		return
	}
	var segments []detector.Segment
	if s.detectorSegmentsEnabled() {
		segments = rc.detectorSegments
	}
	event := detector.Event{
		SchemaVersion: detector.SchemaVersion,
		RequestID:     rc.id,
		CallerID:      rc.caller.cfg.ID,
		Kind:          "request",
		ObservedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		SourceIP:      rc.clientIP.Address,
		KeyID:         opaqueDetectorKeyID(rc.rec.TokenID),
		TinyRequest:   rc.detectorTinyRequest,
	}
	s.submitDetectorEvent(event, segments, false)
	usageEvent := event
	usageEvent.Kind = "usage"
	usageEvent.TinyRequest = false
	usageEvent.Tokens = int64(totalTokens(rc.rec.Usage))
	usageEvent.Segments = nil
	s.submitDetectorEvent(usageEvent, nil, false)
}

func (s *Service) dispatchSecurityDetectorEvent(event detector.Event) {
	if s == nil {
		return
	}
	s.submitDetectorEvent(event, nil, false)
}

func detectorSegmentsConfigured(detectors []detectorRuntime) bool {
	for _, runtime := range detectors {
		if runtime.config.Builtin == "" && runtime.config.Mode != "off" && len(runtime.segments) > 0 {
			return true
		}
	}
	return false
}

func (s *Service) dispatchBuiltinDetectorEvent(kind, callerID, requestID, keyID string) {
	if s == nil {
		return
	}
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: kind, CallerID: callerID, RequestID: requestID, KeyID: opaqueDetectorKeyID(keyID), ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.submitDetectorEvent(event, nil, true)
}

func (s *Service) submitDetectorEvent(event detector.Event, segments []detector.Segment, builtinsOnly bool) {
	s.detectorMu.RLock()
	defer s.detectorMu.RUnlock()
	if s.detectorDispatcher == nil {
		return
	}
	for _, runtime := range s.detectors {
		if runtime.config.Mode == "off" || (runtime.config.Builtin != "") != builtinsOnly {
			continue
		}
		perDetectorEvent := event
		perDetectorEvent.Segments = nil
		if len(runtime.segments) != 0 {
			for _, segment := range segments {
				if runtime.segments[segment.Kind] {
					perDetectorEvent.Segments = append(perDetectorEvent.Segments, segment)
				}
			}
		}
		policy := detect.Policy{Mode: runtime.config.Mode, Timeout: time.Duration(runtime.config.TimeoutMS) * time.Millisecond, OnError: runtime.config.OnError, SampleRate: runtime.config.SampleRate}
		s.detectorDispatcher.Submit(runtime.config.ID, event.CallerID, runtime.implementation, perDetectorEvent, policy)
	}
}

func (s *Service) handleDetectorResult(result detect.Result) {
	consumer := s.currentDetectorResultConsumer()
	if result.Err != nil {
		outcome := "fallback"
		if result.FailClosed {
			outcome = "fail_closed"
		}
		s.recordSecurityAccessEvent(securityAccessEvent{
			TS: time.Now().UTC(), EventType: "security_detector_failure", Outcome: outcome,
			ReasonCode: "detector_failed", CallerID: result.CallerID, RequestedModel: result.DetectorID,
		})
		if consumer != nil {
			consumer(result)
		}
		return
	}
	for _, signal := range result.Signals {
		requestID := ""
		if len(signal.EvidenceRequestIDs) != 0 {
			requestID = signal.EvidenceRequestIDs[0]
		}
		reason := fmt.Sprintf("detector_signal:%s:%.4f:%s", signal.Severity, signal.Score, signal.SegmentKind)
		if signal.SegmentIndex != nil {
			reason += fmt.Sprintf(":%d", *signal.SegmentIndex)
		}
		s.recordSecurityAccessEvent(securityAccessEvent{
			TS: time.Now().UTC(), RequestID: requestID, EventType: "security_detector_signal", Outcome: result.Mode,
			ReasonCode: reason, CallerID: result.CallerID, RequestedModel: signal.DetectorID, TokenID: signal.Key,
		})
	}
	if consumer != nil {
		consumer(result)
	}
}

// SetDetectorResultConsumer is the handoff point for asynchronous enforce
// consumers such as automatic containment. Enforce results are marked by both
// Result.Enforce and Result.FailClosed; callers must never infer them from text.
func (s *Service) SetDetectorResultConsumer(consumer func(detect.Result)) {
	if s != nil {
		s.detectorMu.Lock()
		s.detectorResultConsumer = consumer
		s.detectorMu.Unlock()
	}
}

func (s *Service) currentDetectorResultConsumer() func(detect.Result) {
	s.detectorMu.RLock()
	consumer := s.detectorResultConsumer
	s.detectorMu.RUnlock()
	return consumer
}

func opaqueDetectorKeyID(tokenID string) string {
	if strings.TrimSpace(tokenID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tokenID))
	return hex.EncodeToString(sum[:16])
}
