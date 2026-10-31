// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"log"
	"time"
)

func shouldRecordNativeResponse(s *Service, rc *requestContext, req *IRRequest, target Target) bool {
	if s == nil || s.usage == nil || s.usage.writer == nil || rc == nil || rc.caller == nil || rc.pathTemplate != "/v1/responses" || req == nil || target.ForceStoreFalse {
		return false
	}
	provider, ok := s.cfg.Provider[target.Provider]
	if !ok || targetDialect(provider, target) != "openai-responses" || isBedrock(provider) {
		return false
	}
	if value, exists := req.Raw["store"]; exists {
		enabled, ok := value.(bool)
		return ok && enabled
	}
	return true
}

// rememberNativeResponse captures only routing metadata, never content. The
// queue is nonblocking and all insertion failures are recorded separately.
func (s *Service) rememberNativeResponse(rc *requestContext, req *IRRequest, target Target, upstreamID, status string) {
	if !shouldRecordNativeResponse(s, rc, req, target) || upstreamID == "" {
		return
	}
	if len(upstreamID) > 2048 {
		s.usage.writer.affinityFailures.Add(1)
		log.Printf("response affinity identifier too long")
		return
	}
	key, err := responseAffinityTargetKey(target.Provider, s.cfg.Provider[target.Provider], target)
	retention, retentionErr := responseStoreRetention(s.cfg.Server.ResponsesStore)
	if err != nil || retentionErr != nil {
		s.usage.writer.affinityFailures.Add(1)
		log.Printf("response affinity target identity unavailable")
		return
	}
	visibleID := upstreamID
	if s.identifiers != nil {
		visibleID = s.identifiers.Encode(upstreamID)
	}
	background := requestWantsResponsesBackground(req)
	var snapshot string
	if background {
		var snapshotErr error
		snapshot, snapshotErr = makeBackgroundBillingSnapshot(rc, target)
		if snapshotErr != nil {
			s.usage.writer.affinityFailures.Add(1)
			log.Printf("background billing snapshot unavailable")
			return
		}
	}
	state := "completed"
	if status == "in_progress" || status == "queued" {
		state = "in_progress"
	}
	if status == "cancelled" {
		state = "cancelled"
	}
	s.usage.recordResponseAffinity(responseAffinityRecord{ResponseID: visibleID, RequestID: rc.id, UpstreamResponseID: upstreamID, TargetKey: key, ModelGroup: req.Model, CallerID: rc.caller.cfg.ID, Project: callerProject(rc.caller.cfg), Background: background, BillingSnapshot: snapshot, Status: state, UsageSettled: !background, CreatedAt: rc.start.UTC().UnixMilli(), ExpiresAt: time.Now().UTC().Add(retention).UnixMilli()})
}

// queueBackgroundTerminal schedules idempotent settlement after the affinity
// insert already enqueued by response.created (or the unary response body).
func (s *Service) queueBackgroundTerminal(rc *requestContext, visibleID string, response map[string]any) {
	if s == nil || s.usage == nil || rc == nil || rc.caller == nil || visibleID == "" {
		return
	}
	status, terminal := terminalBackgroundStatus(stringValue(response["status"]))
	usage, reported := reportedBackgroundUsage(response)
	if !terminal {
		return
	}
	if !reported {
		if s.usage.writer != nil {
			s.usage.writer.settlementUnreported.Add(1)
		}
		log.Printf("background terminal response did not report usage")
		return
	}
	s.usage.recordBackgroundSettlement(backgroundSettlementJob{responseID: visibleID, owner: rc.caller.cfg.ID, status: status, usage: usage, observedAt: time.Now().UTC()})
}

// rewriteNativeResponsesObject mirrors the native stream translator: only
// protocol IDs are rewritten; metadata, output text and tool arguments remain
// untouched. It operates on an exclusively owned decoded upstream object.
func rewriteNativeResponsesObject(resp *IRResponse, ids IdentifierTransform) {
	if resp == nil || !resp.RawResponse || resp.Raw == nil || ids == nil {
		return
	}
	rewrite := func(m map[string]any, key string) {
		if id, ok := m[key].(string); ok && id != "" {
			m[key] = ids.Encode(id)
		}
	}
	rewrite(resp.Raw, "id")
	resp.ID = stringValue(resp.Raw["id"])
	if output, ok := resp.Raw["output"].([]any); ok {
		for _, v := range output {
			if item, ok := v.(map[string]any); ok {
				rewrite(item, "id")
				rewrite(item, "call_id")
			}
		}
	}
}
