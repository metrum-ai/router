// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Admin-plane metrics (metrum-ai/router#816, ACP-N01).
//
// The series describe the admin plane only and are rendered only on an admin-plane
// scrape. The serving-port /metrics output is unchanged, so a v4.7 scrape target
// sees byte-identical content.
//
// Label values are registry- or catalogue-bounded: operation IDs and
// object/action pairs come from the typed route registry, status codes from the
// operation's documented set, and denial reasons from the problem catalogue. No
// request path, subject, credential or address is ever a label.
//
// The revision gauges are the freshness signal #813 supplies: this process is the
// replica, so there is no replica label.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type adminMetricKey struct {
	Operation string
	Object    string
	Action    string
	Status    int
}

type adminDenialKey struct {
	Object string
	Action string
	Reason string
}

type adminAuthKey struct {
	Kind    string
	Outcome string
}

type adminMetricValues struct {
	Requests int64
}

// adminDurationValues aggregates latency per operation, independent of status, so
// the histogram never emits a duplicate series.
type adminDurationValues struct {
	SumMS  float64
	Count  int64
	Bucket [adminDurationBucketCount]int64
}

// adminDurationBucketsMS are the fixed latency buckets (milliseconds).
var adminDurationBucketsMS = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

const adminDurationBucketCount = 10

type adminMetricsStore struct {
	mu           sync.Mutex
	requests     map[adminMetricKey]*adminMetricValues
	durations    map[string]*adminDurationValues
	denials      map[adminDenialKey]int64
	rateLimited  map[string]int64
	authOutcomes map[adminAuthKey]int64
	replays      map[string]int64
	auditFailurs map[string]int64
}

func newAdminMetricsStore() *adminMetricsStore {
	return &adminMetricsStore{
		requests:     map[adminMetricKey]*adminMetricValues{},
		durations:    map[string]*adminDurationValues{},
		denials:      map[adminDenialKey]int64{},
		rateLimited:  map[string]int64{},
		authOutcomes: map[adminAuthKey]int64{},
		replays:      map[string]int64{},
		auditFailurs: map[string]int64{},
	}
}

// Observe records one admin request.
func (m *adminMetricsStore) Observe(observation AdminObservation) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := adminMetricKey{
		Operation: sanitizeMetricLabel(observation.OperationID, "unknown"),
		Object:    sanitizeMetricLabel(observation.Object, "unknown"),
		Action:    sanitizeMetricLabel(observation.Action, "unknown"),
		Status:    observation.Status,
	}
	values := m.requests[key]
	if values == nil {
		values = &adminMetricValues{}
		m.requests[key] = values
	}
	values.Requests++
	ms := float64(observation.Duration.Milliseconds())
	durations := m.durations[key.Operation]
	if durations == nil {
		durations = &adminDurationValues{}
		m.durations[key.Operation] = durations
	}
	durations.SumMS += ms
	durations.Count++
	for i, bound := range adminDurationBucketsMS {
		if ms <= bound {
			durations.Bucket[i]++
		}
	}
	if observation.RateLimited {
		m.rateLimited[key.Operation]++
	}
	switch observation.Status {
	case 401, 403:
		m.denials[adminDenialKey{Object: key.Object, Action: key.Action, Reason: adminDenialReason(observation.Status)}]++
	}
}

// RecordAuthentication records one credential outcome (ACP-066).
func (m *adminMetricsStore) RecordAuthentication(kind, outcome string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authOutcomes[adminAuthKey{Kind: sanitizeMetricLabel(kind, "unknown"), Outcome: sanitizeMetricLabel(outcome, "unknown")}]++
}

// RecordIdempotentReplay counts a replayed request.
func (m *adminMetricsStore) RecordIdempotentReplay(operation string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replays[sanitizeMetricLabel(operation, "unknown")]++
}

// RecordAuditFailure counts a failed audit write.
func (m *adminMetricsStore) RecordAuditFailure(kind string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditFailurs[sanitizeMetricLabel(kind, "unknown")]++
}

func adminDurationCounts(values map[string]*adminDurationValues) map[string]int64 {
	out := make(map[string]int64, len(values))
	for key := range values {
		out[key] = 0
	}
	return out
}

func sortedAdminCountKeys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func adminDenialReason(status int) string {
	switch status {
	case 401:
		return "unauthenticated"
	case 403:
		return "forbidden"
	}
	return "other"
}

// Prometheus renders the admin series. It is only ever appended to an
// admin-plane scrape.
func (m *adminMetricsStore) Prometheus(freshness AdminFreshnessSource) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	requestKeys := make([]adminMetricKey, 0, len(m.requests))
	for key := range m.requests {
		requestKeys = append(requestKeys, key)
	}
	sort.Slice(requestKeys, func(i, j int) bool {
		if requestKeys[i].Operation != requestKeys[j].Operation {
			return requestKeys[i].Operation < requestKeys[j].Operation
		}
		if requestKeys[i].Object != requestKeys[j].Object {
			return requestKeys[i].Object < requestKeys[j].Object
		}
		if requestKeys[i].Action != requestKeys[j].Action {
			return requestKeys[i].Action < requestKeys[j].Action
		}
		return requestKeys[i].Status < requestKeys[j].Status
	})
	denialKeys := make([]adminDenialKey, 0, len(m.denials))
	for key := range m.denials {
		denialKeys = append(denialKeys, key)
	}
	sort.Slice(denialKeys, func(i, j int) bool {
		if denialKeys[i].Object != denialKeys[j].Object {
			return denialKeys[i].Object < denialKeys[j].Object
		}
		if denialKeys[i].Action != denialKeys[j].Action {
			return denialKeys[i].Action < denialKeys[j].Action
		}
		return denialKeys[i].Reason < denialKeys[j].Reason
	})
	authKeys := make([]adminAuthKey, 0, len(m.authOutcomes))
	for key := range m.authOutcomes {
		authKeys = append(authKeys, key)
	}
	sort.Slice(authKeys, func(i, j int) bool {
		if authKeys[i].Kind != authKeys[j].Kind {
			return authKeys[i].Kind < authKeys[j].Kind
		}
		return authKeys[i].Outcome < authKeys[j].Outcome
	})
	rateOperationKeys := sortedAdminCountKeys(m.rateLimited)
	durationOperations := sortedAdminCountKeys(adminDurationCounts(m.durations))
	durationsSnapshot := make(map[string]adminDurationValues, len(m.durations))
	for operation, values := range m.durations {
		durationsSnapshot[operation] = *values
	}
	replayKeys := sortedAdminCountKeys(m.replays)
	auditKeys := sortedAdminCountKeys(m.auditFailurs)
	type snapshot struct {
		key    adminMetricKey
		values adminMetricValues
	}
	var snapshots []snapshot
	for _, key := range requestKeys {
		values := *m.requests[key]
		snapshots = append(snapshots, snapshot{key: key, values: values})
	}
	denials := make(map[adminDenialKey]int64, len(m.denials))
	for key, value := range m.denials {
		denials[key] = value
	}
	auth := make(map[adminAuthKey]int64, len(m.authOutcomes))
	for key, value := range m.authOutcomes {
		auth[key] = value
	}
	rate := make(map[string]int64, len(m.rateLimited))
	for key, value := range m.rateLimited {
		rate[key] = value
	}
	replay := make(map[string]int64, len(m.replays))
	for key, value := range m.replays {
		replay[key] = value
	}
	audit := make(map[string]int64, len(m.auditFailurs))
	for key, value := range m.auditFailurs {
		audit[key] = value
	}
	m.mu.Unlock()

	var out strings.Builder
	out.WriteString("# HELP router_admin_requests_total Admin-plane requests by operation, authorization object/action and status.\n")
	out.WriteString("# TYPE router_admin_requests_total counter\n")
	for _, snap := range snapshots {
		fmt.Fprintf(&out, "router_admin_requests_total{operation=%q,object=%q,action=%q,status=%q} %d\n",
			snap.key.Operation, snap.key.Object, snap.key.Action, fmt.Sprintf("%d", snap.key.Status), snap.values.Requests)
	}
	out.WriteString("# HELP router_admin_request_duration_seconds Admin-plane request latency.\n")
	out.WriteString("# TYPE router_admin_request_duration_seconds histogram\n")
	for _, operation := range durationOperations {
		values := durationsSnapshot[operation]
		for i, bound := range adminDurationBucketsMS {
			fmt.Fprintf(&out, "router_admin_request_duration_seconds_bucket{operation=%q,le=%q} %d\n",
				operation, formatMetricFloat(bound/1000), values.Bucket[i])
		}
		fmt.Fprintf(&out, "router_admin_request_duration_seconds_bucket{operation=%q,le=\"+Inf\"} %d\n", operation, values.Count)
		fmt.Fprintf(&out, "router_admin_request_duration_seconds_sum{operation=%q} %s\n",
			operation, formatMetricFloat(values.SumMS/1000))
		fmt.Fprintf(&out, "router_admin_request_duration_seconds_count{operation=%q} %d\n", operation, values.Count)
	}
	out.WriteString("# HELP router_admin_authz_denials_total Admin-plane authentication and authorization denials.\n")
	out.WriteString("# TYPE router_admin_authz_denials_total counter\n")
	for _, key := range denialKeys {
		fmt.Fprintf(&out, "router_admin_authz_denials_total{object=%q,action=%q,reason=%q} %d\n",
			key.Object, key.Action, key.Reason, denials[key])
	}
	out.WriteString("# HELP router_admin_rate_limited_total Admin-plane requests refused by the per-identity rate limit.\n")
	out.WriteString("# TYPE router_admin_rate_limited_total counter\n")
	for _, operation := range rateOperationKeys {
		fmt.Fprintf(&out, "router_admin_rate_limited_total{operation=%q} %d\n", operation, rate[operation])
	}
	out.WriteString("# HELP router_admin_authentications_total Admin credential outcomes by kind.\n")
	out.WriteString("# TYPE router_admin_authentications_total counter\n")
	for _, key := range authKeys {
		fmt.Fprintf(&out, "router_admin_authentications_total{kind=%q,outcome=%q} %d\n", key.Kind, key.Outcome, auth[key])
	}
	out.WriteString("# HELP router_admin_idempotent_replays_total Admin-plane Idempotency-Key replays.\n")
	out.WriteString("# TYPE router_admin_idempotent_replays_total counter\n")
	for _, operation := range replayKeys {
		fmt.Fprintf(&out, "router_admin_idempotent_replays_total{operation=%q} %d\n", operation, replay[operation])
	}
	out.WriteString("# HELP router_admin_audit_write_failures_total Failed admin audit writes.\n")
	out.WriteString("# TYPE router_admin_audit_write_failures_total counter\n")
	for _, kind := range auditKeys {
		fmt.Fprintf(&out, "router_admin_audit_write_failures_total{kind=%q} %d\n", kind, audit[kind])
	}
	out.WriteString("# HELP router_admin_revision Admin-plane desired and applied revisions.\n")
	out.WriteString("# TYPE router_admin_revision gauge\n")
	if freshness != nil {
		if desired, applied, _, ok := freshness.ConfigFreshness(); ok {
			fmt.Fprintf(&out, "router_admin_revision{scope=\"config\",kind=\"desired\"} %d\n", desired)
			fmt.Fprintf(&out, "router_admin_revision{scope=\"config\",kind=\"applied\"} %d\n", applied)
		}
		if desired, applied, _, ok := freshness.PolicyFreshness(); ok {
			fmt.Fprintf(&out, "router_admin_revision{scope=\"policy\",kind=\"desired\"} %d\n", desired)
			fmt.Fprintf(&out, "router_admin_revision{scope=\"policy\",kind=\"applied\"} %d\n", applied)
		}
	}
	return out.String()
}

func formatMetricFloat(value float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.6f", value), "0"), ".")
}

// adminObserveHookFor wires the mount's observation hook to the store.
func (s *Service) adminObserveHookFor() func(AdminObservation) {
	if s == nil {
		return nil
	}
	return func(observation AdminObservation) {
		s.adminMetrics().Observe(observation)
	}
}

// adminMetrics returns the process-wide admin metrics store, creating it on first
// use so a Service literal in tests still works.
func (s *Service) adminMetrics() *adminMetricsStore {
	if s == nil {
		return nil
	}
	s.adminMetricsOnce.Do(func() {
		if s.adminMetricsStore == nil {
			s.adminMetricsStore = newAdminMetricsStore()
		}
	})
	return s.adminMetricsStore
}

// adminAuditRetentionCutoff returns the cutoff before which admin audit rows may be purged.
func (s *Service) adminAuditRetentionCutoff(now time.Time) time.Time {
	days := s.adminAPISettings().auditRetentionDays
	if days <= 0 {
		days = 90
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}
