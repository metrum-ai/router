// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
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
	if len(s.cfg.Server.Detectors) == 0 {
		return nil
	}
	s.detectorDispatcher = detect.NewDispatcher(4, 256, s.handleDetectorResult)
	for _, cfg := range s.cfg.Server.Detectors {
		var implementation detector.Detector
		switch cfg.Builtin {
		case "honeytoken":
			implementation = detect.HoneytokenDetector{}
		case "spend_breaker":
			implementation = detect.SpendBreakerDetector{}
		default:
			external, err := newExternalDetector(cfg.URL, cfg.AllowHosts, cfg.AllowHTTP, time.Duration(cfg.TimeoutMS)*time.Millisecond)
			if err != nil {
				s.detectorDispatcher.Close()
				return fmt.Errorf("initialize detector %s: %w", cfg.ID, err)
			}
			implementation = external
		}
		segments := make(map[string]bool, len(cfg.IncludeSegments))
		for _, kind := range cfg.IncludeSegments {
			segments[kind] = true
		}
		s.detectors = append(s.detectors, detectorRuntime{config: cfg, implementation: implementation, segments: segments})
	}
	return nil
}

func (s *Service) dispatchCompletedRequestDetectors(rc *requestContext) {
	if rc == nil || rc.caller == nil || s.detectorDispatcher == nil {
		return
	}
	var segments []detector.Segment
	if detectorSegmentsConfigured(s.detectors) {
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
	if s == nil || s.detectorDispatcher == nil {
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
	if s == nil || s.detectorDispatcher == nil {
		return
	}
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: kind, CallerID: callerID, RequestID: requestID, KeyID: opaqueDetectorKeyID(keyID), ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.submitDetectorEvent(event, nil, true)
}

func (s *Service) submitDetectorEvent(event detector.Event, segments []detector.Segment, builtinsOnly bool) {
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
	if result.Err != nil {
		outcome := "fallback"
		if result.FailClosed {
			outcome = "fail_closed"
		}
		s.recordSecurityAccessEvent(securityAccessEvent{
			TS: time.Now().UTC(), EventType: "security_detector_failure", Outcome: outcome,
			ReasonCode: "detector_failed", CallerID: result.CallerID, RequestedModel: result.DetectorID,
		})
		if s.detectorResultConsumer != nil {
			s.detectorResultConsumer(result)
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
			ReasonCode: reason, CallerID: result.CallerID, RequestedModel: result.DetectorID, TokenID: signal.Key,
		})
	}
	if s.detectorResultConsumer != nil {
		s.detectorResultConsumer(result)
	}
}

// SetDetectorResultConsumer is the handoff point for asynchronous enforce
// consumers such as automatic containment. Enforce results are marked by both
// Result.Enforce and Result.FailClosed; callers must never infer them from text.
func (s *Service) SetDetectorResultConsumer(consumer func(detect.Result)) {
	if s != nil {
		s.detectorResultConsumer = consumer
	}
}

func opaqueDetectorKeyID(tokenID string) string {
	if strings.TrimSpace(tokenID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tokenID))
	return hex.EncodeToString(sum[:16])
}
