// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// errQuotaStatePersist is returned when durable quota state cannot be written.
// Callers must fail closed and must not treat spend or exhaustion as recorded.
var errQuotaStatePersist = errors.New("quota-state-error")

type quotaStore struct {
	mu      sync.Mutex
	path    string
	keyID   string
	key     []byte
	callers map[string]*callerRuntime
	state   persistentState
	// saveFault, when set, forces saveLocked to return without writing. Tests only.
	saveFault error
}

// rebuildCallers replaces the in-memory caller runtime map with runtimes built
// from cfg.Callers. Existing caller state (day/month/lifetime counters) is
// preserved for caller IDs that still exist in the new configuration. New
// callers get fresh state. The caller's allow map and status are rebuilt from
// the new configuration. This is used by the DB-mode reload path to swap in
// updated caller definitions without losing quota history.
func (q *quotaStore) rebuildCallers(cfg *Config) error {
	if cfg == nil {
		return errors.New("rebuild callers: config is required")
	}
	now := time.Now().UTC()
	dir, err := cfg.validateAccounts()
	if err != nil {
		return fmt.Errorf("rebuild callers: %w", err)
	}
	newCallers := make(map[string]*callerRuntime, len(cfg.Callers))
	for _, callerCfg := range cfg.Callers {
		allow := map[string]bool{}
		for _, group := range callerCfg.Allow {
			allow[group] = true
		}
		rt := &callerRuntime{cfg: callerCfg, allow: allow, keyStatus: normalizeStatusDefault(callerCfg.Status)}
		rt.ownerUser, _ = callerOwnerUser(callerCfg)
		rt.project = normalizeAccountID(callerCfg.Project)
		if user, ok := dir.users[rt.ownerUser]; ok {
			rt.ownerUserStatus = normalizeStatusDefault(user.Status)
		} else {
			rt.ownerUserStatus = accountStatusActive
		}
		if project, ok := dir.projects[rt.project]; ok {
			rt.projectStatus = normalizeStatusDefault(project.Status)
		} else {
			rt.projectStatus = accountStatusActive
		}
		if membership, ok := dir.memberships[membershipKey(rt.ownerUser, rt.project)]; ok {
			rt.membershipRole = normalizeRoleDefault(membership.Role)
			rt.membershipStatus = normalizeStatusDefault(membership.Status)
		} else {
			rt.membershipRole = "member"
			rt.membershipStatus = accountStatusActive
		}
		newCallers[callerCfg.ID] = rt
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// Preserve existing caller state for IDs that survive the reload.
	for id, rt := range newCallers {
		if existing, ok := q.callers[id]; ok {
			rt.inFlight = existing.inFlight
			rt.inFlightReservedTokens = existing.inFlightReservedTokens
			rt.reqTimes = existing.reqTimes
			rt.tokenTimes = existing.tokenTimes
		}
		if q.state.Callers[id] == nil {
			q.state.Callers[id] = &callerState{DayStart: dayStart(now), MonthStart: monthStart(now)}
		}
	}
	// Remove state for callers that no longer exist.
	for id := range q.state.Callers {
		if newCallers[id] == nil {
			delete(q.state.Callers, id)
		}
	}
	q.callers = newCallers
	return nil
}

type callerRuntime struct {
	cfg                    CallerConfig
	allow                  map[string]bool
	ownerUser              string
	ownerUserStatus        string
	project                string
	projectStatus          string
	membershipRole         string
	membershipStatus       string
	keyStatus              string
	inFlight               int
	inFlightReservedTokens int64
	reqTimes               []time.Time
	tokenTimes             []tokenEvent
}

type tokenEvent struct {
	At     time.Time `json:"at"`
	Tokens int       `json:"tokens"`
}

type persistentState struct {
	Callers              map[string]*callerState `json:"callers"`
	PersistenceUnhealthy bool                    `json:"persistence_unhealthy,omitempty"`
}

type callerState struct {
	DayStart       time.Time `json:"day_start"`
	MonthStart     time.Time `json:"month_start"`
	DayRequests    int64     `json:"day_requests"`
	DayTokens      int64     `json:"day_tokens"`
	MonthRequests  int64     `json:"month_requests"`
	MonthTokens    int64     `json:"month_tokens"`
	LifetimeTokens int64     `json:"lifetime_tokens"`
	Disabled       bool      `json:"disabled"`
}

type admission struct {
	OK          bool
	Status      int
	Reason      string
	RetryAfter  string
	QuotaState  string
	KeyState    string
	WarningText string
	Reservation *quotaReservation
}

type quotaReservation struct {
	tokens int
	active bool
}

func newQuotaStore(path string, cfg *Config) (*quotaStore, error) {
	key, keyID := quotaStateIntegrityKey(cfg)
	qs := &quotaStore{path: path, keyID: keyID, key: key, callers: map[string]*callerRuntime{}, state: persistentState{Callers: map[string]*callerState{}}}
	if raw, err := readVersionedStateFile(path); err == nil && len(raw) > 0 {
		state, enveloped, err := unmarshalIntegrityState[persistentState](raw, keyID, key)
		if err != nil {
			return nil, err
		}
		if enveloped {
			qs.state = state
		} else {
			if os.Getenv("METRUM_AI_ROUTER_ALLOW_UNSIGNED_STATE_MIGRATION") != "1" &&
				os.Getenv("SMART_LLMROUTER_ALLOW_UNSIGNED_STATE_MIGRATION") != "1" {
				return nil, errStateIntegrity
			}
			if err := json.Unmarshal(raw, &qs.state); err != nil {
				return nil, err
			}
		}
	}
	if qs.state.Callers == nil {
		qs.state.Callers = map[string]*callerState{}
	}
	now := time.Now().UTC()
	dir, err := cfg.validateAccounts()
	if err != nil {
		return nil, err
	}
	for _, callerCfg := range cfg.Callers {
		allow := map[string]bool{}
		for _, group := range callerCfg.Allow {
			allow[group] = true
		}
		rt := &callerRuntime{cfg: callerCfg, allow: allow, keyStatus: normalizeStatusDefault(callerCfg.Status)}
		rt.ownerUser, _ = callerOwnerUser(callerCfg)
		rt.project = normalizeAccountID(callerCfg.Project)
		if user, ok := dir.users[rt.ownerUser]; ok {
			rt.ownerUserStatus = normalizeStatusDefault(user.Status)
		} else {
			rt.ownerUserStatus = accountStatusActive
		}
		if project, ok := dir.projects[rt.project]; ok {
			rt.projectStatus = normalizeStatusDefault(project.Status)
		} else {
			rt.projectStatus = accountStatusActive
		}
		if membership, ok := dir.memberships[membershipKey(rt.ownerUser, rt.project)]; ok {
			rt.membershipRole = normalizeRoleDefault(membership.Role)
			rt.membershipStatus = normalizeStatusDefault(membership.Status)
		} else {
			rt.membershipRole = "member"
			rt.membershipStatus = accountStatusActive
		}
		qs.callers[callerCfg.ID] = rt
		if qs.state.Callers[callerCfg.ID] == nil {
			qs.state.Callers[callerCfg.ID] = &callerState{DayStart: dayStart(now), MonthStart: monthStart(now)}
		}
	}
	if path != "" {
		if err := qs.saveLocked(); err != nil {
			return nil, err
		}
	}
	return qs, nil
}

func (q *quotaStore) Admit(c *callerRuntime, estTokens int) admission {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ad := q.persistenceGuardLocked(); !ad.OK {
		return ad
	}
	now := time.Now().UTC()
	st := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	keyState := q.keyState(c.cfg, st, 0)
	if st.Disabled || keyState == "exhausted" {
		if ad := q.persistExhaustedLocked(st); !ad.OK {
			return ad
		}
		return admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	if c.cfg.Rate.Concurrent > 0 && c.inFlight >= c.cfg.Rate.Concurrent {
		return admission{Status: http.StatusTooManyRequests, Reason: "concurrency-exceeded", RetryAfter: "1", QuotaState: "ok", KeyState: keyState}
	}
	c.reqTimes = pruneTimes(c.reqTimes, now.Add(-time.Minute))
	if c.cfg.Rate.RPM > 0 && len(c.reqTimes) >= c.cfg.Rate.RPM {
		return admission{Status: http.StatusTooManyRequests, Reason: "rpm-exceeded", RetryAfter: "60", QuotaState: "ok", KeyState: keyState}
	}
	c.tokenTimes = pruneTokens(c.tokenTimes, now.Add(-time.Minute))
	if exceedsBudget(c.cfg.Quota.Day.Requests, st.DayRequests+1) || exceedsBudget(c.cfg.Quota.Month.Requests, st.MonthRequests+1) {
		return admission{Status: http.StatusTooManyRequests, Reason: "quota-exhausted", RetryAfter: "3600", QuotaState: "reject", KeyState: keyState}
	}
	res, ad := q.reserveTokensLocked(c, st, estTokens, now)
	if !ad.OK {
		return ad
	}
	c.inFlight++
	c.reqTimes = append(c.reqTimes, now)
	st.DayRequests++
	st.MonthRequests++
	if err := q.saveLocked(); err != nil {
		st.DayRequests--
		st.MonthRequests--
		if len(c.reqTimes) > 0 {
			c.reqTimes = c.reqTimes[:len(c.reqTimes)-1]
		}
		c.inFlight--
		q.releaseReservationLocked(c, res)
		q.markPersistenceUnhealthyLocked()
		return admission{
			Status:     http.StatusServiceUnavailable,
			Reason:     "quota-state-error",
			QuotaState: "error",
			KeyState:   "error",
		}
	}
	reservedEstimate := c.inFlightReservedTokens
	quotaState, warning := q.quotaState(c.cfg, st, reservedEstimate)
	keyState = q.keyState(c.cfg, st, 0)
	if estTokens > 0 {
		keyState = q.keyState(c.cfg, st, reservedEstimate)
	}
	return admission{OK: true, Status: http.StatusOK, QuotaState: quotaState, KeyState: keyState, WarningText: warning, Reservation: res}
}

// AcquireConcurrency reserves one in-flight slot without counting a request
// against RPM/day/month budgets. Used to bound body buffering before the
// full Admit that runs after traffic-shape queue waits.
func (q *quotaStore) AcquireConcurrency(c *callerRuntime) admission {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ad := q.persistenceGuardLocked(); !ad.OK {
		return ad
	}
	now := time.Now().UTC()
	st := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	keyState := q.keyState(c.cfg, st, 0)
	if st.Disabled || keyState == "exhausted" {
		if ad := q.persistExhaustedLocked(st); !ad.OK {
			return ad
		}
		return admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	if c.cfg.Rate.Concurrent > 0 && c.inFlight >= c.cfg.Rate.Concurrent {
		return admission{Status: http.StatusTooManyRequests, Reason: "concurrency-exceeded", RetryAfter: "1", QuotaState: "ok", KeyState: keyState}
	}
	c.inFlight++
	return admission{OK: true, Status: http.StatusOK, QuotaState: "ok", KeyState: keyState}
}

func (q *quotaStore) Release(c *callerRuntime) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if c.inFlight > 0 {
		c.inFlight--
	}
}

func (q *quotaStore) ReleaseReservation(c *callerRuntime, reservation *quotaReservation) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseReservationLocked(c, reservation)
}

func (q *quotaStore) ReserveTokens(c *callerRuntime, estTokens int) admission {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ad := q.persistenceGuardLocked(); !ad.OK {
		return ad
	}
	now := time.Now().UTC()
	st := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	keyState := q.keyState(c.cfg, st, c.inFlightReservedTokens)
	actualKeyState := q.keyState(c.cfg, st, 0)
	if st.Disabled || actualKeyState == "exhausted" {
		if ad := q.persistExhaustedLocked(st); !ad.OK {
			return ad
		}
		return admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	if keyState == "exhausted" {
		return admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	res, ad := q.reserveTokensLocked(c, st, estTokens, now)
	if !ad.OK {
		return ad
	}
	reservedEstimate := c.inFlightReservedTokens
	quotaState, warning := q.quotaState(c.cfg, st, reservedEstimate)
	return admission{OK: true, Status: http.StatusOK, QuotaState: quotaState, KeyState: q.keyState(c.cfg, st, reservedEstimate), WarningText: warning, Reservation: res}
}

func (q *quotaStore) RecordTokens(c *callerRuntime, reservation *quotaReservation, usage Usage) (string, string, error) {
	total := usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseReservationLocked(c, reservation)
	now := time.Now().UTC()
	st := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	st.DayTokens += int64(total)
	st.MonthTokens += int64(total)
	st.LifetimeTokens += int64(total)
	c.tokenTimes = append(c.tokenTimes, tokenEvent{At: now, Tokens: total})
	keyState := q.keyState(c.cfg, st, 0)
	if keyState == "exhausted" {
		st.Disabled = true
	}
	// Consumed upstream work remains an outstanding liability even when the
	// durable write fails. Keep counters, latch persistence health, and refuse
	// further paid admissions until a later save succeeds.
	if err := q.saveLocked(); err != nil {
		q.markPersistenceUnhealthyLocked()
		return "", "", fmt.Errorf("%w: %v", errQuotaStatePersist, err)
	}
	q.state.PersistenceUnhealthy = false
	quotaState, _ := q.quotaState(c.cfg, st, 0)
	return quotaState, keyState, nil
}

// persistExhaustedLocked marks a caller exhausted and persists that flag.
// On save failure it rolls back Disabled, latches persistence health, and
// returns a fail-closed admission.
func (q *quotaStore) persistExhaustedLocked(st *callerState) admission {
	wasDisabled := st.Disabled
	st.Disabled = true
	if err := q.saveLocked(); err != nil {
		st.Disabled = wasDisabled
		q.markPersistenceUnhealthyLocked()
		return admission{
			Status:     http.StatusServiceUnavailable,
			Reason:     "quota-state-error",
			QuotaState: "error",
			KeyState:   "error",
		}
	}
	q.state.PersistenceUnhealthy = false
	return admission{OK: true}
}

func (q *quotaStore) persistenceGuardLocked() admission {
	if !q.state.PersistenceUnhealthy {
		return admission{OK: true}
	}
	// Storage may have recovered. Persist outstanding liability before any new
	// paid admission; fail closed until that write succeeds.
	if err := q.saveLocked(); err != nil {
		return admission{
			Status:     http.StatusServiceUnavailable,
			Reason:     "quota-state-error",
			QuotaState: "error",
			KeyState:   "error",
		}
	}
	q.state.PersistenceUnhealthy = false
	return admission{OK: true}
}

func (q *quotaStore) markPersistenceUnhealthyLocked() {
	q.state.PersistenceUnhealthy = true
}

func (q *quotaStore) PersistenceUnhealthy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.state.PersistenceUnhealthy
}

func (q *quotaStore) releaseReservationLocked(c *callerRuntime, reservation *quotaReservation) {
	if c == nil || reservation == nil || !reservation.active {
		return
	}
	c.inFlightReservedTokens -= int64(reservation.tokens)
	if c.inFlightReservedTokens < 0 {
		c.inFlightReservedTokens = 0
	}
	reservation.active = false
}

func (q *quotaStore) reserveTokensLocked(c *callerRuntime, st *callerState, estTokens int, now time.Time) (*quotaReservation, admission) {
	if estTokens <= 0 {
		return nil, admission{OK: true, Status: http.StatusOK}
	}
	reservedBefore := c.inFlightReservedTokens
	keyState := q.keyState(c.cfg, st, reservedBefore)
	c.tokenTimes = pruneTokens(c.tokenTimes, now.Add(-time.Minute))
	if c.cfg.Rate.TPM > 0 && int64(sumTokenEvents(c.tokenTimes)+estTokens)+reservedBefore > int64(c.cfg.Rate.TPM) {
		return nil, admission{Status: http.StatusTooManyRequests, Reason: "tpm-exceeded", RetryAfter: "60", QuotaState: "ok", KeyState: keyState}
	}
	reservedEstimate := reservedBefore + int64(estTokens)
	if exceedsBudget(c.cfg.Quota.Day.Tokens, st.DayTokens+reservedEstimate) ||
		exceedsBudget(c.cfg.Quota.Month.Tokens, st.MonthTokens+reservedEstimate) {
		return nil, admission{Status: http.StatusTooManyRequests, Reason: "quota-exhausted", RetryAfter: "3600", QuotaState: "reject", KeyState: keyState}
	}
	if c.cfg.Key.LifetimeTokens > 0 && st.LifetimeTokens+reservedEstimate > c.cfg.Key.LifetimeTokens {
		return nil, admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	c.inFlightReservedTokens += int64(estTokens)
	return &quotaReservation{tokens: estTokens, active: true}, admission{OK: true, Status: http.StatusOK}
}

func (q *quotaStore) Usage(c *callerRuntime) map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now().UTC()
	st := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	return map[string]any{
		"caller_id":          c.cfg.ID,
		"caller_user":        callerUser(c.cfg),
		"caller_project":     callerProject(c.cfg),
		"caller_environment": callerEnvironment(c.cfg),
		"day": map[string]any{
			"requests": st.DayRequests,
			"tokens":   st.DayTokens,
		},
		"month": map[string]any{
			"requests": st.MonthRequests,
			"tokens":   st.MonthTokens,
		},
		"key": map[string]any{
			"lifetime_tokens":   st.LifetimeTokens,
			"remaining_tokens":  remaining(c.cfg.Key.LifetimeTokens, st.LifetimeTokens),
			"exhausted":         st.Disabled,
			"configured_budget": c.cfg.Key.LifetimeTokens,
		},
	}
}

func (q *quotaStore) stateFor(id string, now time.Time) *callerState {
	st := q.state.Callers[id]
	if st == nil {
		st = &callerState{DayStart: dayStart(now), MonthStart: monthStart(now)}
		q.state.Callers[id] = st
	}
	return st
}

func (q *quotaStore) resetWindows(st *callerState, now time.Time) {
	ds := dayStart(now)
	ms := monthStart(now)
	if st.DayStart.Before(ds) {
		st.DayStart = ds
		st.DayRequests = 0
		st.DayTokens = 0
	}
	if st.MonthStart.Before(ms) {
		st.MonthStart = ms
		st.MonthRequests = 0
		st.MonthTokens = 0
	}
}

func (q *quotaStore) quotaState(cfg CallerConfig, st *callerState, est int64) (string, string) {
	soft := cfg.Quota.SoftPct
	if soft == 0 {
		soft = 80
	}
	if overSoft(cfg.Quota.Day.Requests, st.DayRequests, soft) || overSoft(cfg.Quota.Day.Tokens, st.DayTokens+est, soft) ||
		overSoft(cfg.Quota.Month.Requests, st.MonthRequests, soft) || overSoft(cfg.Quota.Month.Tokens, st.MonthTokens+est, soft) {
		return "soft", "rolling quota soft cap reached"
	}
	return "ok", ""
}

func (q *quotaStore) keyState(cfg CallerConfig, st *callerState, est int64) string {
	if cfg.Key.LifetimeTokens <= 0 {
		return "active"
	}
	used := st.LifetimeTokens + est
	if used >= cfg.Key.LifetimeTokens || st.Disabled {
		return "exhausted"
	}
	soft := cfg.Key.SoftPct
	if soft == 0 {
		soft = 90
	}
	if overSoft(cfg.Key.LifetimeTokens, used, soft) {
		return "soft"
	}
	return "active"
}

func (q *quotaStore) saveLocked() error {
	if q.path == "" {
		return nil
	}
	if q.saveFault != nil {
		return q.saveFault
	}
	raw, err := marshalIntegrityState(q.state, q.keyID, q.key)
	if err != nil {
		return err
	}
	if err := writeVersionedStateFile(q.path, raw); err != nil {
		return err
	}
	return nil
}

func quotaStateIntegrityKey(cfg *Config) ([]byte, string) {
	parts := []string{"smart-llmrouter quota state v1"}
	if cfg != nil {
		parts = append(parts, cfg.StatePath)
	}
	return stateIntegrityMaterial(parts...)
}

func (q *quotaStore) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.path == "" {
		return nil
	}
	if q.state.Callers == nil {
		return errors.New("quota state not initialized")
	}
	return q.saveLocked()
}

func pruneTimes(in []time.Time, cutoff time.Time) []time.Time {
	out := in[:0]
	for _, t := range in {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

func pruneTokens(in []tokenEvent, cutoff time.Time) []tokenEvent {
	out := in[:0]
	for _, ev := range in {
		if ev.At.After(cutoff) {
			out = append(out, ev)
		}
	}
	return out
}

func sumTokenEvents(in []tokenEvent) int {
	total := 0
	for _, ev := range in {
		total += ev.Tokens
	}
	return total
}

func exceedsBudget(limit, val int64) bool {
	return limit > 0 && val > limit
}

func overSoft(limit, val int64, pct int) bool {
	return limit > 0 && val*100 >= limit*int64(pct)
}

func remaining(limit, used int64) int64 {
	if limit <= 0 {
		return 0
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}
