// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Opt-in experiment profiles for endpoint-bound agent studies (#379).
//
// An experiment profile injects an added time-to-first-token (TTFT) delay and
// paces the delivered output for requests that match its caller/group/target
// scope. Profiles are off by default and must be scoped: a profile with no
// caller, group, and target scope is rejected at validation so experiments
// can never silently shape normal traffic.
//
// Pacing only ever delays delivery; it cannot make an upstream faster than
// its real performance. Injected delays are bounded per request by
// max_extra_delay_ms, and cancellation/timeouts still abort waits. The
// delivered-token rate uses an explicit calibration (chars per token) and is
// recorded as an estimate; frame pacing is available as an explicitly
// labeled event rate instead. Every paced request records trace events with
// the profile name, profile hash, and injected delay for provenance.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ExperimentProfileConfig controls one experiment profile.
type ExperimentProfileConfig struct {
	Enabled     *bool  `yaml:"enabled" json:"enabled,omitempty"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Seed makes per-request jitter reproducible: the injected delay is a
	// deterministic function of the profile seed, the profile hash, and the
	// request ID.
	Seed string `yaml:"seed" json:"seed,omitempty"`
	// Scope: at least one list must be non-empty. A request matches when
	// every non-empty list contains it.
	Callers []string `yaml:"callers" json:"callers,omitempty"`
	Groups  []string `yaml:"groups" json:"groups,omitempty"`
	// Targets match the selected upstream as "provider/model".
	Targets []string `yaml:"targets" json:"targets,omitempty"`

	// TTFTDelayMS is the base added delay before the first response byte
	// reaches the client. TTFTJitterMS adds a seeded uniform jitter in
	// [-jitter, +jitter].
	TTFTDelayMS  float64 `yaml:"ttft_delay_ms" json:"ttftDelayMs,omitempty"`
	TTFTJitterMS float64 `yaml:"ttft_jitter_ms" json:"ttftJitterMs,omitempty"`

	// OutputTokensPerSec paces delivered output by an estimated token rate.
	// The estimate uses TokenEstimateCharsPerToken (default 4) and is
	// explicitly labeled an estimate in telemetry; it is never presented as
	// an exact upstream token count.
	OutputTokensPerSec float64 `yaml:"output_tokens_per_sec" json:"outputTokensPerSec,omitempty"`
	// EventsPerSec paces delivered SSE frames (events) directly, as an
	// explicitly labeled event rate. Exactly one of output_tokens_per_sec
	// and events_per_sec may be set.
	EventsPerSec float64 `yaml:"events_per_sec" json:"eventsPerSec,omitempty"`
	// Burst is the number of events (or estimated tokens) released
	// immediately at stream start before the rate applies. Defaults to 1.
	Burst int `yaml:"burst" json:"burst,omitempty"`
	// TokenEstimateCharsPerToken calibrates the delivered-token estimate
	// used by output_tokens_per_sec.
	TokenEstimateCharsPerToken float64 `yaml:"token_estimate_chars_per_token" json:"tokenEstimateCharsPerToken,omitempty"`

	// MaxExtraDelayMS caps the total injected delay for one request.
	MaxExtraDelayMS int `yaml:"max_extra_delay_ms" json:"maxExtraDelayMs,omitempty"`
}

const (
	experimentProfileMaxTTFTDelayMS  = 120000.0
	experimentProfileMaxExtraDelayMS = 600000
)

func experimentProfileEnabled(cfg ExperimentProfileConfig) bool {
	return cfg.Enabled != nil && *cfg.Enabled
}

func validateExperimentProfiles(profiles map[string]ExperimentProfileConfig) error {
	for _, name := range sortedExperimentProfileNames(profiles) {
		cfg := profiles[name]
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("experiment profile name must be non-empty")
		}
		if !experimentProfileEnabled(cfg) {
			continue
		}
		if len(cfg.Callers) == 0 && len(cfg.Groups) == 0 && len(cfg.Targets) == 0 {
			return fmt.Errorf("experiment profile %q must scope to at least one caller, group, or target (global profiles would shape normal traffic)", name)
		}
		if cfg.TTFTDelayMS < 0 || cfg.TTFTJitterMS < 0 {
			return fmt.Errorf("experiment profile %q: ttft delays must be non-negative", name)
		}
		if cfg.TTFTDelayMS+cfg.TTFTJitterMS > experimentProfileMaxTTFTDelayMS {
			return fmt.Errorf("experiment profile %q: ttft_delay_ms + ttft_jitter_ms may not exceed %d", name, int(experimentProfileMaxTTFTDelayMS))
		}
		if cfg.OutputTokensPerSec < 0 || cfg.EventsPerSec < 0 {
			return fmt.Errorf("experiment profile %q: pacing rates must be non-negative", name)
		}
		if cfg.OutputTokensPerSec > 0 && cfg.EventsPerSec > 0 {
			return fmt.Errorf("experiment profile %q: set exactly one of output_tokens_per_sec or events_per_sec", name)
		}
		if cfg.Burst < 0 {
			return fmt.Errorf("experiment profile %q: burst must be non-negative", name)
		}
		if cfg.TokenEstimateCharsPerToken < 0 {
			return fmt.Errorf("experiment profile %q: token_estimate_chars_per_token must be non-negative", name)
		}
		if cfg.MaxExtraDelayMS < 0 || cfg.MaxExtraDelayMS > experimentProfileMaxExtraDelayMS {
			return fmt.Errorf("experiment profile %q: max_extra_delay_ms must be in [0, %d]", name, experimentProfileMaxExtraDelayMS)
		}
		if cfg.TTFTDelayMS == 0 && cfg.TTFTJitterMS == 0 && cfg.OutputTokensPerSec == 0 && cfg.EventsPerSec == 0 {
			return fmt.Errorf("experiment profile %q: an enabled profile must inject a delay or pace output", name)
		}
	}
	return nil
}

func sortedExperimentProfileNames(profiles map[string]ExperimentProfileConfig) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// experimentProfileHash is the versioned profile/config hash recorded in
// telemetry: sha256 of the canonical profile JSON.
func experimentProfileHash(cfg ExperimentProfileConfig) string {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// resolveExperimentProfile returns the first enabled profile (sorted by name,
// for determinism) whose scope matches the request.
func resolveExperimentProfile(profiles map[string]ExperimentProfileConfig, callerID, group, targetKey string) (ExperimentProfileConfig, string, bool) {
	for _, name := range sortedExperimentProfileNames(profiles) {
		cfg := profiles[name]
		if !experimentProfileEnabled(cfg) {
			continue
		}
		if len(cfg.Callers) > 0 && !stringSliceContains(cfg.Callers, callerID) {
			continue
		}
		if len(cfg.Groups) > 0 && !stringSliceContains(cfg.Groups, group) {
			continue
		}
		if len(cfg.Targets) > 0 && !stringSliceContains(cfg.Targets, targetKey) {
			continue
		}
		return cfg, name, true
	}
	return ExperimentProfileConfig{}, "", false
}

// experimentPacer applies one request's injected delays. It never shortens
// waits: pacing only releases output later than it arrived.
type experimentPacer struct {
	profile     ExperimentProfileConfig
	name        string
	hash        string
	ttftMS      float64
	injectedMS  float64
	maxExtraMS  float64
	started     bool
	nextRelease time.Time
	budget      float64
}

func newExperimentPacer(profile ExperimentProfileConfig, name, requestID string) *experimentPacer {
	ttft := profile.TTFTDelayMS
	if profile.TTFTJitterMS > 0 {
		// Seeded per-request jitter: deterministic for (seed, profile, request).
		sum := sha256.Sum256([]byte(profile.Seed + ":" + experimentProfileHash(profile) + ":" + requestID))
		unit := float64(sum[0]) / 255.0
		ttft += (2*unit - 1) * profile.TTFTJitterMS
		if ttft < 0 {
			ttft = 0
		}
	}
	maxExtra := float64(profile.MaxExtraDelayMS)
	if maxExtra <= 0 {
		maxExtra = experimentProfileMaxExtraDelayMS
	}
	burst := profile.Burst
	if burst <= 0 {
		burst = 1
	}
	return &experimentPacer{
		profile:    profile,
		name:       name,
		hash:       experimentProfileHash(profile),
		ttftMS:     ttft,
		maxExtraMS: maxExtra,
		budget:     float64(burst),
	}
}

// sleep waits up to duration, bounded by the remaining injected-delay budget
// and the request context, and returns the actually injected milliseconds.
func (p *experimentPacer) sleep(ctx context.Context, duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	remaining := p.maxExtraMS - p.injectedMS
	if remaining <= 0 {
		return 0
	}
	if cap := time.Duration(remaining * float64(time.Millisecond)); duration > cap {
		duration = cap
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0
	case <-timer.C:
	}
	ms := float64(duration) / float64(time.Millisecond)
	p.injectedMS += ms
	return ms
}

// ttftDelay injects the added TTFT delay once, before the first client byte.
func (p *experimentPacer) ttftDelay(ctx context.Context) float64 {
	if p.started {
		return 0
	}
	p.started = true
	p.nextRelease = time.Now()
	return p.sleep(ctx, time.Duration(p.ttftMS*float64(time.Millisecond)))
}

// paceFrame schedules the release of one delivered frame carrying
// estTokens estimated tokens (or one event under events_per_sec) and sleeps
// until its scheduled release time.
func (p *experimentPacer) paceFrame(ctx context.Context, estTokens float64) float64 {
	rate := p.profile.EventsPerSec
	cost := 1.0
	if p.profile.OutputTokensPerSec > 0 {
		rate = p.profile.OutputTokensPerSec
		cost = estTokens
	}
	if rate <= 0 {
		return 0
	}
	p.budget -= cost
	if p.budget >= 0 {
		return 0
	}
	delay := time.Duration(-p.budget / rate * float64(time.Second))
	p.budget = 0
	// Pace relative to the scheduled release time so bursts average out to
	// the configured rate without ever accelerating the upstream.
	now := time.Now()
	if p.nextRelease.Before(now) {
		p.nextRelease = now
	}
	p.nextRelease = p.nextRelease.Add(delay)
	return p.sleep(ctx, time.Until(p.nextRelease))
}

// estimateFrameTokens converts one SSE frame's payload size to an estimated
// token count using the profile's calibrated chars-per-token constant.
func (p *experimentPacer) estimateFrameTokens(frameLen int) float64 {
	chars := p.profile.TokenEstimateCharsPerToken
	if chars <= 0 {
		chars = 4
	}
	if est := float64(frameLen) / chars; est >= 1 {
		return est
	}
	return 1
}

// experimentPacingWriter delays and paces writes to the client. It passes
// every byte through unchanged and flushes on demand.
type experimentPacingWriter struct {
	http.ResponseWriter
	ctx   context.Context
	pacer *experimentPacer
}

func (w *experimentPacingWriter) Write(p []byte) (int, error) {
	if w.pacer != nil {
		if !w.pacer.started {
			w.pacer.ttftDelay(w.ctx)
		} else {
			w.pacer.paceFrame(w.ctx, w.pacer.estimateFrameTokens(len(p)))
		}
	}
	return w.ResponseWriter.Write(p)
}

// Flush keeps the wrapper transparent to flusher assertions in the stream
// proxies.
func (w *experimentPacingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.ResponseController.
func (w *experimentPacingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// experimentHoldForResponse computes the non-streaming hold: added TTFT plus
// the paced delivery time for the response's reported output tokens, capped
// by the injected-delay budget.
func (p *experimentPacer) holdForResponse(ctx context.Context, outputTokens int) float64 {
	p.ttftDelay(ctx)
	if p.profile.OutputTokensPerSec > 0 && outputTokens > 0 {
		duration := time.Duration(float64(outputTokens) / p.profile.OutputTokensPerSec * float64(time.Second))
		p.sleep(ctx, duration)
	}
	return p.injectedMS
}
