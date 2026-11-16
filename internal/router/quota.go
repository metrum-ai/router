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
	"sync/atomic"
	"time"
)

// errQuotaStatePersist is returned when durable quota state cannot be written.
// Callers must fail closed and must not treat spend or exhaustion as recorded.
var errQuotaStatePersist = errors.New("quota-state-error")

type quotaStore struct {
	mu             sync.Mutex
	path           string
	keyID          string
	key            []byte
	callers        map[string]*callerRuntime
	scopedReserved map[string]int64
	state          persistentState
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
	callers, err := prepareQuotaCallers(cfg)
	if err != nil {
		return err
	}
	q.installPreparedCallers(callers)
	return nil
}

// Preparation is detached: rejecting later authn/authz preparation must not
// change the live caller directory or persistent accounting state.
func prepareQuotaCallers(cfg *Config) (map[string]*callerRuntime, error) {
	if cfg == nil {
		return nil, errors.New("rebuild callers: config is required")
	}
	dir, err := cfg.validateAccounts()
	if err != nil {
		return nil, fmt.Errorf("rebuild callers: %w", err)
	}
	newCallers := make(map[string]*callerRuntime, len(cfg.Callers))
	quotaScopes := hierarchyQuotaScopes(cfg.Server.Hierarchy)
	for _, callerCfg := range cfg.Callers {
		allow := map[string]bool{}
		for _, group := range callerCfg.Allow {
			allow[group] = true
		}
		rt := &callerRuntime{cfg: callerCfg, quotaScope: quotaScopes[callerCfg.ID], allow: allow, keyStatus: normalizeStatusDefault(callerCfg.Status)}
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
		if membership, ok := accountMembership(dir, rt.ownerUser, rt.project, rt.cfg.Environment); ok {
			rt.membershipRole = normalizeRoleDefault(membership.Role)
			rt.membershipStatus = normalizeStatusDefault(membership.Status)
		} else {
			rt.membershipRole = "member"
			rt.membershipStatus = accountStatusActive
		}
		newCallers[callerCfg.ID] = rt
	}
	return newCallers, nil
}

// Installation is infallible. Capture current accounting under the live lock
// at commit, not during preparation: serving requests may complete meanwhile.
func (q *quotaStore) installPreparedCallers(newCallers map[string]*callerRuntime) {
	now := time.Now().UTC()
	q.mu.Lock()
	defer q.mu.Unlock()
	ensureHierarchyQuotaStates(q.state.Callers, newCallers, q.callers, now)
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
		if newCallers[id] == nil && !hierarchyQuotaScopeReferenced(id, newCallers) {
			delete(q.state.Callers, id)
		}
	}
	q.callers = newCallers
}

type callerRuntime struct {
	cfg                    CallerConfig
	quotaScope             string
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
	autoContainment        atomic.Pointer[automaticContainmentState]
}

type tokenEvent struct {
	At     time.Time `json:"at"`
	Tokens int       `json:"tokens"`
}

type persistentState struct {
	Callers              map[string]*callerState       `json:"callers"`
	MCPToolLimits        map[string]*mcpToolQuotaState `json:"mcp_tool_limits,omitempty"`
	PersistenceUnhealthy bool                          `json:"persistence_unhealthy,omitempty"`
}

type mcpToolQuotaState struct {
	HourKey      string      `json:"hour_key"`
	DayKey       string      `json:"day_key"`
	HourlyUSD    float64     `json:"hourly_usd"`
	DailyUSD     float64     `json:"daily_usd"`
	RequestTimes []time.Time `json:"request_times,omitempty"`
	UpdatedAt    time.Time   `json:"updated_at"`
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
	qs := &quotaStore{path: path, keyID: keyID, key: key, callers: map[string]*callerRuntime{}, scopedReserved: map[string]int64{}, state: persistentState{Callers: map[string]*callerState{}}}
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
		rt := &callerRuntime{cfg: callerCfg, quotaScope: hierarchyQuotaScopes(cfg.Server.Hierarchy)[callerCfg.ID], allow: allow, keyStatus: normalizeStatusDefault(callerCfg.Status)}
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
		if membership, ok := accountMembership(dir, rt.ownerUser, rt.project, rt.cfg.Environment); ok {
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
	ensureHierarchyQuotaStates(qs.state.Callers, qs.callers, nil, now)
	if path != "" {
		if err := qs.saveLocked(); err != nil {
			return nil, err
		}
	}
	return qs, nil
}

func (q *quotaStore) Admit(c *callerRuntime, estTokens int) admission {
	return q.admit(c, estTokens, 0)
}

func (q *quotaStore) AdmitMCP(c *callerRuntime, rpm int) admission {
	return q.admit(c, 0, rpm)
}

func (q *quotaStore) admit(c *callerRuntime, estTokens, rpmOverride int) admission {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ad := q.persistenceGuardLocked(); !ad.OK {
		return ad
	}
	now := time.Now().UTC()
	st := q.stateFor(c.quotaStateID(), now)
	q.resetWindows(st, now)
	keySt := q.stateFor(c.cfg.ID, now)
	q.resetWindows(keySt, now)
	keyState := q.keyState(c.cfg, keySt, 0)
	if keySt.Disabled || keyState == "exhausted" {
		if ad := q.persistExhaustedLocked(keySt); !ad.OK {
			return ad
		}
		return admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	if limit := c.effectiveRateLimit(c.cfg.Rate.Concurrent); limit > 0 && c.inFlight >= limit {
		return admission{Status: http.StatusTooManyRequests, Reason: "concurrency-exceeded", RetryAfter: "1", QuotaState: "ok", KeyState: keyState}
	}
	c.reqTimes = pruneTimes(c.reqTimes, now.Add(-time.Minute))
	effectiveRPM := c.effectiveRateLimit(c.cfg.Rate.RPM)
	if rpmOverride > 0 && (effectiveRPM == 0 || rpmOverride < effectiveRPM) {
		effectiveRPM = rpmOverride
	}
	if effectiveRPM > 0 && len(c.reqTimes) >= effectiveRPM {
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
	if c.quotaScope != "" {
		reservedEstimate = q.scopedReserved[c.quotaScope]
	}
	quotaState, warning := q.quotaState(c.cfg, st, reservedEstimate)
	keyState = q.keyState(c.cfg, keySt, c.inFlightReservedTokens)
	if estTokens > 0 {
		keyState = q.keyState(c.cfg, keySt, c.inFlightReservedTokens)
	}
	return admission{OK: true, Status: http.StatusOK, QuotaState: quotaState, KeyState: keyState, WarningText: warning, Reservation: res}
}

func (q *quotaStore) AdmitMCPTool(caller, tool string, limits []MCPToolLimitConfig) admission {
	if q == nil || caller == "" || tool == "" || len(limits) == 0 {
		return admission{OK: true, Status: http.StatusOK}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if ad := q.persistenceGuardLocked(); !ad.OK {
		return ad
	}
	now := time.Now().UTC()
	hourKey := now.Format("2006-01-02T15")
	dayKey := now.Format("2006-01-02")
	if q.state.MCPToolLimits == nil {
		q.state.MCPToolLimits = map[string]*mcpToolQuotaState{}
	}
	states := make([]*mcpToolQuotaState, 0, len(limits))
	for _, limit := range limits {
		keyTool := limit.Tool
		key := caller + "\x00" + keyTool
		state := q.state.MCPToolLimits[key]
		if state == nil {
			state = &mcpToolQuotaState{}
			q.state.MCPToolLimits[key] = state
		}
		if state.HourKey != hourKey {
			state.HourKey, state.HourlyUSD = hourKey, 0
		}
		if state.DayKey != dayKey {
			state.DayKey, state.DailyUSD = dayKey, 0
		}
		state.RequestTimes = pruneTimes(state.RequestTimes, now.Add(-time.Minute))
		if limit.RPM > 0 && len(state.RequestTimes) >= limit.RPM {
			return admission{Status: http.StatusTooManyRequests, Reason: "mcp-tool-rpm-exceeded", RetryAfter: "60"}
		}
		cost := limit.CostPerCallUSD
		if limit.HourlyUSD > 0 && roundUSD(state.HourlyUSD+cost) > limit.HourlyUSD || limit.DailyUSD > 0 && roundUSD(state.DailyUSD+cost) > limit.DailyUSD {
			return admission{Status: http.StatusTooManyRequests, Reason: "mcp-tool-spend-exceeded", RetryAfter: "60"}
		}
		states = append(states, state)
	}
	for i, state := range states {
		limit := limits[i]
		state.RequestTimes = append(state.RequestTimes, now)
		state.HourlyUSD = roundUSD(state.HourlyUSD + limit.CostPerCallUSD)
		state.DailyUSD = roundUSD(state.DailyUSD + limit.CostPerCallUSD)
		state.UpdatedAt = now
	}
	if err := q.saveLocked(); err != nil {
		for i, state := range states {
			state.RequestTimes = state.RequestTimes[:len(state.RequestTimes)-1]
			state.HourlyUSD = roundUSD(state.HourlyUSD - limits[i].CostPerCallUSD)
			state.DailyUSD = roundUSD(state.DailyUSD - limits[i].CostPerCallUSD)
		}
		q.markPersistenceUnhealthyLocked()
		return admission{Status: http.StatusServiceUnavailable, Reason: "quota-state-error", RetryAfter: "1"}
	}
	if len(q.state.MCPToolLimits) > 10000 {
		cutoff := now.Add(-30 * 24 * time.Hour)
		for key, state := range q.state.MCPToolLimits {
			if state.UpdatedAt.Before(cutoff) {
				delete(q.state.MCPToolLimits, key)
			}
		}
	}
	return admission{OK: true, Status: http.StatusOK}
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
	st := q.stateFor(c.quotaStateID(), now)
	q.resetWindows(st, now)
	keySt := q.stateFor(c.cfg.ID, now)
	q.resetWindows(keySt, now)
	keyState := q.keyState(c.cfg, keySt, c.inFlightReservedTokens)
	if keySt.Disabled || keyState == "exhausted" {
		if ad := q.persistExhaustedLocked(keySt); !ad.OK {
			return ad
		}
		return admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	if limit := c.effectiveRateLimit(c.cfg.Rate.Concurrent); limit > 0 && c.inFlight >= limit {
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
	st := q.stateFor(c.quotaStateID(), now)
	q.resetWindows(st, now)
	keySt := q.stateFor(c.cfg.ID, now)
	q.resetWindows(keySt, now)
	keyState := q.keyState(c.cfg, keySt, c.inFlightReservedTokens)
	actualKeyState := q.keyState(c.cfg, keySt, 0)
	if keySt.Disabled || actualKeyState == "exhausted" {
		if ad := q.persistExhaustedLocked(keySt); !ad.OK {
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
	if c.quotaScope != "" {
		reservedEstimate = q.scopedReserved[c.quotaScope]
	}
	quotaState, warning := q.quotaState(c.cfg, st, reservedEstimate)
	return admission{OK: true, Status: http.StatusOK, QuotaState: quotaState, KeyState: q.keyState(c.cfg, keySt, c.inFlightReservedTokens), WarningText: warning, Reservation: res}
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
	st := q.stateFor(c.quotaStateID(), now)
	keySt := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	q.resetWindows(keySt, now)
	st.DayTokens += int64(total)
	st.MonthTokens += int64(total)
	keySt.LifetimeTokens += int64(total)
	c.tokenTimes = append(c.tokenTimes, tokenEvent{At: now, Tokens: total})
	keyState := q.keyState(c.cfg, keySt, 0)
	if keyState == "exhausted" {
		keySt.Disabled = true
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
	if c.quotaScope != "" {
		q.scopedReserved[c.quotaScope] -= int64(reservation.tokens)
		if q.scopedReserved[c.quotaScope] < 0 {
			q.scopedReserved[c.quotaScope] = 0
		}
	}
	reservation.active = false
}

func (q *quotaStore) reserveTokensLocked(c *callerRuntime, st *callerState, estTokens int, now time.Time) (*quotaReservation, admission) {
	if estTokens <= 0 {
		return nil, admission{OK: true, Status: http.StatusOK}
	}
	reservedBefore := c.inFlightReservedTokens
	keySt := q.stateFor(c.cfg.ID, now)
	q.resetWindows(keySt, now)
	keyState := q.keyState(c.cfg, keySt, reservedBefore)
	c.tokenTimes = pruneTokens(c.tokenTimes, now.Add(-time.Minute))
	if limit := c.effectiveRateLimit(c.cfg.Rate.TPM); limit > 0 && int64(sumTokenEvents(c.tokenTimes)+estTokens)+reservedBefore > int64(limit) {
		return nil, admission{Status: http.StatusTooManyRequests, Reason: "tpm-exceeded", RetryAfter: "60", QuotaState: "ok", KeyState: keyState}
	}
	quotaReservedBefore := reservedBefore
	if c.quotaScope != "" {
		quotaReservedBefore = q.scopedReserved[c.quotaScope]
	}
	reservedEstimate := quotaReservedBefore + int64(estTokens)
	if exceedsBudget(c.cfg.Quota.Day.Tokens, st.DayTokens+reservedEstimate) ||
		exceedsBudget(c.cfg.Quota.Month.Tokens, st.MonthTokens+reservedEstimate) {
		return nil, admission{Status: http.StatusTooManyRequests, Reason: "quota-exhausted", RetryAfter: "3600", QuotaState: "reject", KeyState: keyState}
	}
	if c.cfg.Key.LifetimeTokens > 0 && keySt.LifetimeTokens+reservedBefore+int64(estTokens) > c.cfg.Key.LifetimeTokens {
		return nil, admission{Status: http.StatusForbidden, Reason: "key-exhausted", QuotaState: "ok", KeyState: "exhausted"}
	}
	c.inFlightReservedTokens += int64(estTokens)
	if c.quotaScope != "" {
		if q.scopedReserved == nil {
			q.scopedReserved = make(map[string]int64)
		}
		q.scopedReserved[c.quotaScope] += int64(estTokens)
	}
	return &quotaReservation{tokens: estTokens, active: true}, admission{OK: true, Status: http.StatusOK}
}

func (q *quotaStore) Usage(c *callerRuntime) map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now().UTC()
	st := q.stateFor(c.quotaStateID(), now)
	keySt := q.stateFor(c.cfg.ID, now)
	q.resetWindows(st, now)
	q.resetWindows(keySt, now)
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
			"lifetime_tokens":   keySt.LifetimeTokens,
			"remaining_tokens":  remaining(c.cfg.Key.LifetimeTokens, keySt.LifetimeTokens),
			"exhausted":         keySt.Disabled,
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

func (c *callerRuntime) quotaStateID() string {
	if c != nil && c.quotaScope != "" {
		return c.quotaScope
	}
	if c == nil {
		return ""
	}
	return c.cfg.ID
}

func ensureHierarchyQuotaStates(state map[string]*callerState, callers, previous map[string]*callerRuntime, now time.Time) {
	if state == nil {
		return
	}
	for _, caller := range callers {
		if caller.quotaScope == "" || state[caller.quotaScope] != nil {
			continue
		}
		shared := &callerState{DayStart: dayStart(now), MonthStart: monthStart(now)}
		seenPreviousScopes := make(map[string]bool)
		for id, member := range callers {
			if member.quotaScope != caller.quotaScope {
				continue
			}
			stateID := id
			if previousCaller := previous[id]; previousCaller != nil && previousCaller.quotaScope != "" {
				stateID = previousCaller.quotaScope
				if seenPreviousScopes[stateID] {
					continue
				}
				seenPreviousScopes[stateID] = true
			}
			if prior := state[stateID]; prior != nil {
				if prior.DayStart.Equal(shared.DayStart) {
					shared.DayRequests += prior.DayRequests
					shared.DayTokens += prior.DayTokens
				}
				if prior.MonthStart.Equal(shared.MonthStart) {
					shared.MonthRequests += prior.MonthRequests
					shared.MonthTokens += prior.MonthTokens
				}
			}
		}
		state[caller.quotaScope] = shared
	}
}

func hierarchyQuotaScopeReferenced(id string, callers map[string]*callerRuntime) bool {
	for _, caller := range callers {
		if caller.quotaScope == id {
			return true
		}
	}
	return false
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
