// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// toolReliabilityStore keeps bounded, windowed tool-call counters per
// group|provider|model. Counters are aggregates, not per-request records, so a
// hot target costs the same as a cold one. State is process-local, exactly like
// dynamic-score observations, and is rebuilt from the window after a restart.
type toolReliabilityStore struct {
	mu      sync.Mutex
	records map[string]toolReliabilityCounters
}

type toolReliabilityCounters struct {
	WindowStart time.Time
	Calls       int
	Valid       int
	Invalid     int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Classes     map[string]int
	Retries     int
	Escalations int
}

type toolReliabilityScore struct {
	Score   float64
	Calls   int
	Valid   int
	Invalid int
}

func newToolReliabilityStore() *toolReliabilityStore {
	return &toolReliabilityStore{records: map[string]toolReliabilityCounters{}}
}

func toolReliabilityKey(groupName, provider, model string) string {
	return strings.Join([]string{"tool", groupName, provider, model}, "|")
}

// reliabilityWindow returns the configured window, applied at read time so a
// config change cannot resurrect stale counters.
func toolReliabilityWindow(cfg ToolRoutingConfig) time.Duration {
	return time.Duration(cfg.windowSeconds()) * time.Second
}

// observe records one response's validation outcome when the window is current.
// A window that has expired restarts the counters rather than mixing periods.
func (s *toolReliabilityStore) observe(key string, validation toolCallValidation, window time.Duration, now time.Time) {
	if s == nil || key == "" {
		return
	}
	if window <= 0 {
		window = defaultToolRoutingWindowSeconds * time.Second
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		record = toolReliabilityCounters{WindowStart: now, FirstSeenAt: now}
	}
	if now.Sub(record.WindowStart) >= window {
		record = toolReliabilityCounters{WindowStart: now, FirstSeenAt: now}
	}
	record.LastSeenAt = now
	record.Calls += validation.Calls
	record.Valid += validation.Valid
	record.Invalid += validation.Invalid
	if len(validation.failures) > 0 {
		if record.Classes == nil {
			record.Classes = map[string]int{}
		}
		for class, count := range validation.failures {
			record.Classes[class] += count
		}
	}
	s.records[key] = record
}

// score returns the Wilson lower bound of valid/(valid+invalid) for the window.
// The lower bound, not the raw ratio, so 2/2 cannot outrank 180/200.
func (s *toolReliabilityStore) score(key string, window time.Duration, now time.Time) toolReliabilityScore {
	if s == nil || key == "" {
		return toolReliabilityScore{}
	}
	if window <= 0 {
		window = defaultToolRoutingWindowSeconds * time.Second
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return toolReliabilityScore{}
	}
	if now.Sub(record.WindowStart) >= window {
		delete(s.records, key)
		return toolReliabilityScore{}
	}
	total := record.Valid + record.Invalid
	return toolReliabilityScore{
		Score:   wilsonLowerBound(record.Valid, total),
		Calls:   record.Calls,
		Valid:   record.Valid,
		Invalid: record.Invalid,
	}
}

// snapshot returns a copy for reports and tests.
func (s *toolReliabilityStore) snapshot() map[string]toolReliabilityCounters {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]toolReliabilityCounters, len(s.records))
	for key, record := range s.records {
		copied := record
		if record.Classes != nil {
			copied.Classes = make(map[string]int, len(record.Classes))
			for class, count := range record.Classes {
				copied.Classes[class] = count
			}
		}
		out[key] = copied
	}
	return out
}

// wilsonLowerBound is the lower bound of the Wilson score interval for a
// proportion at 80% confidence (z = 0.8416). A zero sample is 0, not 0.5.
func wilsonLowerBound(successes, total int) float64 {
	if total <= 0 {
		return 0
	}
	const z = 0.8416
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	centre := p + z*z/(2*n)
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n))
	lower := (centre - margin) / denominator
	if lower < 0 {
		return 0
	}
	if lower > 1 {
		return 1
	}
	return lower
}

// orderToolAwareTargets orders eligible targets by tool reliability for a
// tool-bearing request, then by configured weight, then by configured order.
// A target without enough samples keeps its configured position: the score is
// only trusted after min_samples outcomes in the window.
func (s *Service) orderToolAwareTargets(groupName string, cfg ToolRoutingConfig, targets []Target, hasTools bool) []Target {
	if s == nil || s.toolReliability == nil || !hasTools {
		return append([]Target(nil), targets...)
	}
	minSamples := cfg.minSamples()
	window := toolReliabilityWindow(cfg)
	now := time.Now().UTC()
	type ranked struct {
		target Target
		score  toolReliabilityScore
		ready  bool
		index  int
	}
	rankedTargets := make([]ranked, 0, len(targets))
	for index, target := range targets {
		score := s.toolReliability.score(toolReliabilityKey(groupName, target.Provider, target.Model), window, now)
		rankedTargets = append(
			rankedTargets,
			ranked{target: target, score: score, ready: score.Valid+score.Invalid >= minSamples, index: index},
		)
	}
	ready := 0
	for _, candidate := range rankedTargets {
		if candidate.ready {
			ready++
		}
	}
	if ready < 2 {
		// Cold start, or only one target with evidence: nothing to reorder.
		return append([]Target(nil), targets...)
	}
	sort.SliceStable(rankedTargets, func(i, j int) bool {
		left, right := rankedTargets[i], rankedTargets[j]
		if left.ready != right.ready {
			return left.ready
		}
		if left.ready && right.ready && left.score.Score != right.score.Score {
			return left.score.Score > right.score.Score
		}
		if left.target.Weight != right.target.Weight {
			return left.target.Weight > right.target.Weight
		}
		return left.index < right.index
	})
	out := make([]Target, 0, len(rankedTargets))
	for _, candidate := range rankedTargets {
		out = append(out, candidate.target)
	}
	return out
}
