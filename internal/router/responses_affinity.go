// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Owner-bound stored Responses affinity (#533). This file implements the
// first reviewed slice: creating affinity metadata for a native Responses
// request that stores its response upstream, and pinning a
// previous_response_id follow-up to the recorded target.
//
// The feature is off by default. Only model groups named in
// server.responses_affinity.enabled_groups use any of this code, and with no
// group enabled the request path is byte-identical to a build without it.
//
// The router never stores response content or bearer tokens. It stores a keyed
// fingerprint of the owning token, a keyed lookup hash of the client-visible
// response id, and the upstream id under authenticated encryption. The
// retention purge and legal-hold integration for this table arrive in a later
// slice; until then rows carry an expiry timestamp and no purge job deletes
// them.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	responsesAffinityDefaultRetentionDays = 30
	responsesAffinityMaxRetentionDays     = 365
	responsesAffinityKeyInfoPrefix        = "metrum-router/responses-affinity/v1/"
)

// ResponsesAffinityConfig configures owner-bound stored Responses affinity.
// It is off by default: only the model groups listed in EnabledGroups use it.
type ResponsesAffinityConfig struct {
	// EnabledGroups names the model groups that opt in. Every listed group must
	// exist and have at least one native Responses target.
	EnabledGroups []string `yaml:"enabled_groups" json:"enabledGroups,omitempty"`
	// KeyEnv names the environment variable holding the 32-byte key material
	// (hex or base64). The value is never stored in configuration or logs.
	KeyEnv string `yaml:"key_env" json:"keyEnv,omitempty"`
	// KeyID is a bounded, loggable identifier written next to every stored
	// value so key rotation is explicit.
	KeyID string `yaml:"key_id" json:"keyId,omitempty"`
	// PreviousKeyEnv and PreviousKeyID describe the preceding epoch. They let a
	// rolling restart still read rows written before a rotation. Both are
	// required together.
	PreviousKeyEnv string `yaml:"previous_key_env" json:"previousKeyEnv,omitempty"`
	PreviousKeyID  string `yaml:"previous_key_id" json:"previousKeyId,omitempty"`
	// RetentionDays bounds how long local affinity metadata is kept. The
	// default is 30 days; expiration removes local affinity only and never
	// implies that the upstream provider deleted its copy.
	RetentionDays int `yaml:"retention_days" json:"retentionDays,omitempty"`
}

func (c ResponsesAffinityConfig) enabled() bool { return len(c.EnabledGroups) > 0 }

func (c ResponsesAffinityConfig) groupEnabled(name string) bool {
	for _, group := range c.EnabledGroups {
		if strings.TrimSpace(group) == name {
			return true
		}
	}
	return false
}

var (
	errResponsesAffinityGroups    = errors.New("server responses_affinity enabled_groups must name existing model groups without duplicates")
	errResponsesAffinityNative    = errors.New("server responses_affinity enabled group requires at least one native Responses target")
	errResponsesAffinityUsageDB   = errors.New("server responses_affinity requires usage_db enabled")
	errResponsesAffinityKey       = errors.New("server responses_affinity requires key_env naming a non-empty secret and a bounded key_id")
	errResponsesAffinityPrevKey   = errors.New("server responses_affinity previous_key_env and previous_key_id must be set together and are only valid with an enabled group")
	errResponsesAffinityRetention = errors.New("server responses_affinity retention_days must be between 1 and 365")
)

// validateResponsesAffinityConfig rejects an enabled configuration that cannot
// enforce ownership or reach a native Responses target. Validation runs before
// serving, so a partially configured deployment never starts.
func validateResponsesAffinityConfig(cfg ResponsesAffinityConfig, models map[string]ModelGroup, providers map[string]ProviderConfig, usageDB UsageDBConfig) error {
	if !cfg.enabled() {
		if strings.TrimSpace(cfg.KeyEnv) != "" || strings.TrimSpace(cfg.KeyID) != "" ||
			strings.TrimSpace(cfg.PreviousKeyEnv) != "" || strings.TrimSpace(cfg.PreviousKeyID) != "" ||
			cfg.RetentionDays != 0 {
			return errors.New("server responses_affinity key, previous key and retention_days require enabled_groups")
		}
		return nil
	}
	if usageDB.Enable != nil && !*usageDB.Enable {
		return errResponsesAffinityUsageDB
	}
	if strings.TrimSpace(cfg.KeyEnv) == "" || !validEnvName(strings.TrimSpace(cfg.KeyEnv)) {
		return errResponsesAffinityKey
	}
	if keyID := strings.TrimSpace(cfg.KeyID); keyID == "" || !affinityKeyIDPattern.MatchString(keyID) {
		return errResponsesAffinityKey
	}
	prevEnv := strings.TrimSpace(cfg.PreviousKeyEnv)
	prevID := strings.TrimSpace(cfg.PreviousKeyID)
	if (prevEnv == "") != (prevID == "") {
		return errResponsesAffinityPrevKey
	}
	if prevEnv != "" {
		if !validEnvName(prevEnv) || !affinityKeyIDPattern.MatchString(prevID) || prevID == strings.TrimSpace(cfg.KeyID) {
			return errResponsesAffinityPrevKey
		}
	}
	if cfg.RetentionDays < 0 || cfg.RetentionDays > responsesAffinityMaxRetentionDays {
		return errResponsesAffinityRetention
	}
	seen := map[string]bool{}
	groups := append([]string(nil), cfg.EnabledGroups...)
	sort.Strings(groups)
	for _, raw := range groups {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			return errResponsesAffinityGroups
		}
		seen[name] = true
		group, ok := models[name]
		if !ok {
			return errResponsesAffinityGroups
		}
		native := false
		for _, target := range group.Targets {
			provider, ok := providers[target.Provider]
			if ok && targetDialect(provider, target) == "openai-responses" {
				native = true
				break
			}
		}
		if !native {
			return errResponsesAffinityNative
		}
	}
	return nil
}

var affinityKeyIDPattern = reasoningEnvelopeKeyIDPattern

// responsesAffinityKeySet is one immutable key epoch. Purpose keys are derived
// from the operator secret with HKDF so the same secret can serve the lookup
// hash, the owner fingerprint and upstream-id encryption without key reuse.
type responsesAffinityKeySet struct {
	id       string
	lookup   []byte
	owner    []byte
	upstream []byte
}

func newResponsesAffinityKeySet(keyID, secret string) (responsesAffinityKeySet, error) {
	raw := strings.TrimSpace(secret)
	if raw == "" {
		return responsesAffinityKeySet{}, errResponsesAffinityKey
	}
	material, err := hex.DecodeString(raw)
	if err != nil || len(material) != 32 {
		material, err = base64.StdEncoding.DecodeString(raw)
	}
	if err != nil || len(material) != 32 {
		return responsesAffinityKeySet{}, errors.New("responses_affinity key material must be 32-byte hex or base64")
	}
	derive := func(purpose string) ([]byte, error) {
		key, err := hkdf.Key(sha256.New, material, nil, responsesAffinityKeyInfoPrefix+purpose, 32)
		if err != nil {
			return nil, err
		}
		return key, nil
	}
	lookup, err := derive("lookup")
	if err != nil {
		return responsesAffinityKeySet{}, err
	}
	owner, err := derive("owner")
	if err != nil {
		return responsesAffinityKeySet{}, err
	}
	upstream, err := derive("upstream-id")
	if err != nil {
		return responsesAffinityKeySet{}, err
	}
	return responsesAffinityKeySet{id: keyID, lookup: lookup, owner: owner, upstream: upstream}, nil
}

func (k responsesAffinityKeySet) fingerprintOwner(token string) string {
	mac := hmac.New(sha256.New, k.owner)
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func (k responsesAffinityKeySet) hashLookup(clientID string) string {
	mac := hmac.New(sha256.New, k.lookup)
	_, _ = mac.Write([]byte(clientID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (k responsesAffinityKeySet) decryptUpstreamID(ciphertext, nonceText, ownerFingerprint, lookupHash string) (string, error) {
	block, err := aes.NewCipher(k.upstream)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceText)
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, sealed, affinityUpstreamAAD(k.id, ownerFingerprint, lookupHash))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// affinityUpstreamAAD binds the encrypted upstream id to the key epoch, the
// owning token fingerprint and the lookup hash, so a swapped or re-owned row
// cannot be decrypted with unchanged metadata.
func affinityUpstreamAAD(keyID, ownerFingerprint, lookupHash string) []byte {
	return []byte(keyID + "\x00" + ownerFingerprint + "\x00" + lookupHash)
}

func (k responsesAffinityKeySet) encryptUpstreamID(upstreamID, ownerFingerprint, lookupHash string) (ciphertext, nonce string, err error) {
	block, err := aes.NewCipher(k.upstream)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonceBytes := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return "", "", err
	}
	sealed := gcm.Seal(nil, nonceBytes, []byte(upstreamID), affinityUpstreamAAD(k.id, ownerFingerprint, lookupHash))
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(nonceBytes), nil
}

// responsesAffinities is the serving runtime. A nil runtime means the feature
// is off everywhere.
type responsesAffinities struct {
	groups    map[string]bool
	retention time.Duration
	current   responsesAffinityKeySet
	previous  *responsesAffinityKeySet
	store     *usageStore
}

func newResponsesAffinities(cfg ResponsesAffinityConfig, store *usageStore, lookupEnv func(string) string) (*responsesAffinities, error) {
	if !cfg.enabled() {
		return nil, nil
	}
	if lookupEnv == nil {
		lookupEnv = func(name string) string { return "" }
	}
	if store == nil {
		return nil, errResponsesAffinityUsageDB
	}
	keyID := strings.TrimSpace(cfg.KeyID)
	current, err := newResponsesAffinityKeySet(keyID, lookupEnv(strings.TrimSpace(cfg.KeyEnv)))
	if err != nil {
		return nil, err
	}
	runtime := &responsesAffinities{
		groups:    map[string]bool{},
		retention: responsesAffinityDefaultRetentionDays * 24 * time.Hour,
		current:   current,
		store:     store,
	}
	if cfg.RetentionDays > 0 {
		runtime.retention = time.Duration(cfg.RetentionDays) * 24 * time.Hour
	}
	if prevEnv := strings.TrimSpace(cfg.PreviousKeyEnv); prevEnv != "" {
		prev, err := newResponsesAffinityKeySet(strings.TrimSpace(cfg.PreviousKeyID), lookupEnv(prevEnv))
		if err != nil {
			return nil, err
		}
		runtime.previous = &prev
	}
	for _, name := range cfg.EnabledGroups {
		runtime.groups[strings.TrimSpace(name)] = true
	}
	return runtime, nil
}

func (r *responsesAffinities) enabledFor(group string) bool {
	return r != nil && r.groups[group]
}

func (r *responsesAffinities) keySets() []responsesAffinityKeySet {
	if r == nil {
		return nil
	}
	sets := []responsesAffinityKeySet{r.current}
	if r.previous != nil {
		sets = append(sets, *r.previous)
	}
	return sets
}

// responsesAffinityOwner is the ownership domain bound to one stored
// response. Tenant is the owning user account (the router has no separate
// tenant identity), project and environment come from the caller, and the
// exact caller token is bound through its keyed fingerprint.
type responsesAffinityOwner struct {
	// Fingerprints maps each configured key epoch to the keyed fingerprint of
	// the owning caller token, so a key rotation still resolves rows written
	// under the previous epoch.
	Fingerprints map[string]string
	Tenant       string
	Project      string
	Environment  string
}

type affinityClientError struct {
	status  int
	code    string
	message string
}

func (e affinityClientError) Error() string { return e.code }

type responsesAffinityRequest struct {
	runtime   *responsesAffinities
	owner     responsesAffinityOwner
	previous  *responseAffinityRecord
	target    *Target
	storage   bool
	captured  bool
	failed    bool
	storeOnce bool
}

func (s *Service) callerTokenForAffinity(r *http.Request) string {
	if r == nil {
		return ""
	}
	if header := strings.TrimSpace(r.Header.Get("Authorization")); header != "" {
		const prefix = "Bearer "
		if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
			return strings.TrimSpace(header[len(prefix):])
		}
	}
	// authenticate() accepts X-API-Key when the Authorization header is absent
	// or not a bearer token; the affinity fingerprint must use that same token.
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func (s *Service) callerAffinityOwner(rc *requestContext, token string) (responsesAffinityOwner, error) {
	if rc == nil || rc.caller == nil || s.responsesAffinity == nil {
		return responsesAffinityOwner{}, errors.New("responses affinity requires an authenticated caller")
	}
	if strings.TrimSpace(token) == "" {
		return responsesAffinityOwner{}, errors.New("responses affinity requires the caller token")
	}
	tenant := strings.TrimSpace(rc.caller.ownerUser)
	if tenant == "" {
		tenant = strings.TrimSpace(rc.caller.cfg.User)
	}
	if tenant == "" {
		return responsesAffinityOwner{}, errors.New("responses affinity requires an owning account")
	}
	fingerprints := make(map[string]string, len(s.responsesAffinity.keySets()))
	for _, keys := range s.responsesAffinity.keySets() {
		fingerprints[keys.id] = keys.fingerprintOwner(token)
	}
	return responsesAffinityOwner{
		Fingerprints: fingerprints,
		Tenant:       tenant,
		Project:      strings.TrimSpace(rc.caller.project),
		Environment:  strings.TrimSpace(rc.caller.cfg.Environment),
	}, nil
}

// beginResponsesAffinity installs the per-request affinity state and, when the
// request continues a stored response, returns the pinned target. Errors are
// affinityClientError values the handler maps to the documented status.
func (s *Service) beginResponsesAffinity(rc *requestContext, r *http.Request, groupName string, group ModelGroup, req *IRRequest, dialect string) (*Target, error) {
	if s.responsesAffinity == nil || normalizeDialect(dialect) != "openai-responses" {
		return nil, nil
	}
	if rc == nil || req == nil || rc.rec.Endpoint == "responses.compact" {
		return nil, nil
	}
	previousID := strings.TrimSpace(stringValue(req.Raw["previous_response_id"]))
	if !s.responsesAffinity.enabledFor(groupName) {
		// Once affinity is configured anywhere, an owner-bound continuation may
		// not bypass the ownership lookup by naming a group that does not store
		// affinity: forwarding the provider id would let any caller of that
		// account continue another caller's stored response.
		if previousID != "" {
			return nil, affinityClientError{status: http.StatusBadRequest, code: "stored_response_unsupported", message: "This model group does not serve stored responses."}
		}
		return nil, nil
	}
	owner, err := s.callerAffinityOwner(rc, s.callerTokenForAffinity(r))
	if err != nil {
		return nil, affinityClientError{status: 503, code: "response_affinity_unavailable", message: "response affinity is enabled but the caller identity is unavailable"}
	}
	state := &responsesAffinityRequest{runtime: s.responsesAffinity, owner: owner}
	rc.affinity = state
	if previousID == "" {
		return nil, nil
	}
	row, err := s.responsesAffinity.lookup(owner, previousID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, affinityClientError{status: 404, code: "response_not_found", message: "The response was not found."}
		}
		return nil, affinityClientError{status: 503, code: "response_affinity_unavailable", message: "Stored response lookup is unavailable."}
	}
	if row.ModelGroup != groupName {
		return nil, affinityClientError{status: 409, code: "response_target_unavailable", message: "The stored response belongs to a different model group."}
	}
	target, ok := s.affinityTargetForRow(row, group)
	if !ok {
		return nil, affinityClientError{status: 409, code: "response_target_unavailable", message: "The upstream target that holds this stored response is no longer available."}
	}
	state.previous = row
	state.target = target
	return target, nil
}

// affinityTargetForRow resolves the exact configured target that holds a
// stored response. A target matches only when provider, account, model and
// dialect are identical; there is no silent re-route.
func (s *Service) affinityTargetForRow(row *responseAffinityRecord, group ModelGroup) (*Target, bool) {
	if row == nil {
		return nil, false
	}
	for i := range group.Targets {
		target := group.Targets[i]
		provider, ok := s.cfg.Provider[target.Provider]
		if !ok {
			continue
		}
		if target.Provider != row.Provider || target.Model != row.Model {
			continue
		}
		if strings.TrimSpace(provider.KeyID) != row.AccountRef {
			continue
		}
		if targetDialect(provider, target) != row.ProviderDialect {
			continue
		}
		selected := target
		return &selected, true
	}
	return nil, false
}

// pinResponsesAffinityTarget restricts routing to the recorded target. The
// request still passes authentication, authorization, admission, quota and
// capability checks; only normal strategy selection is bypassed.
func pinResponsesAffinityTarget(group *ModelGroup, target *Target) {
	if group == nil || target == nil {
		return
	}
	group.Targets = []Target{*target}
}

// responsesAffinityStorageEnabled reports whether this request must create a
// retrievable affinity record. A no-store request or a force_store_false
// target never creates one, including a no-store follow-up.
func responsesAffinityStorageEnabled(state *responsesAffinityRequest, req *IRRequest, target Target, dialect string) bool {
	if state == nil || state.runtime == nil || req == nil {
		return false
	}
	if normalizeDialect(dialect) != "openai-responses" {
		return false
	}
	if target.ForceStoreFalse || responsesExplicitNoStore(req) {
		return false
	}
	return true
}

// bindResponsesAffinityTarget records the served target and decides whether to
// store affinity for this request.
func (s *Service) bindResponsesAffinityTarget(rc *requestContext, req *IRRequest, target Target, _ string) {
	if rc == nil || rc.affinity == nil {
		return
	}
	rc.affinity.target = &target
	dialect := targetDialect(s.cfg.Provider[target.Provider], target)
	rc.affinity.storage = responsesAffinityStorageEnabled(rc.affinity, req, target, dialect)
	if rc.affinity.storage {
		rc.affinityCapture = func(upstreamID string) error {
			return s.captureResponsesAffinity(rc, upstreamID)
		}
	}
}

func (s *Service) responsesAffinityStorageActive(rc *requestContext) bool {
	return rc != nil && rc.affinity != nil && rc.affinity.storage && !rc.affinity.captured
}

// captureResponsesAffinity persists the affinity record for one upstream
// response. It is idempotent per request and fails closed: the caller returns
// an error instead of exposing an id the router cannot later own or pin.
func (s *Service) decryptAffinityUpstreamID(row *responseAffinityRecord) (string, error) {
	if row == nil {
		return "", errors.New("missing affinity record")
	}
	for _, keys := range s.responsesAffinity.keySets() {
		if keys.id != row.UpstreamIDKeyID {
			continue
		}
		return keys.decryptUpstreamID(row.UpstreamIDCiphertext, row.UpstreamIDNonce, row.OwnerTokenFingerprint, row.ResponseLookupHash)
	}
	return "", errors.New("no configured key matches the stored upstream id")
}

func (s *Service) captureResponsesAffinity(rc *requestContext, upstreamID string) error {
	if rc == nil || rc.affinity == nil || !rc.affinity.storage {
		return nil
	}
	state := rc.affinity
	if state.captured {
		return nil
	}
	if strings.TrimSpace(upstreamID) == "" {
		state.failed = true
		return errors.New("responses affinity requires the upstream response id")
	}
	if state.target == nil {
		state.failed = true
		return errors.New("responses affinity requires the served target")
	}
	// Hash the upstream id, not the caller-visible encoding: the identifier
	// transform is deterministic, so the caller's encoded id decodes back to
	// this value before lookup and key rotation does not change the hash input.
	now := time.Now().UTC()
	row := responseAffinityRecord{
		OwnerTokenFingerprint: state.owner.Fingerprints[state.runtime.current.id],
		OwnerFingerprintKeyID: state.runtime.current.id,
		Tenant:                state.owner.Tenant,
		Project:               state.owner.Project,
		Environment:           state.owner.Environment,
		LookupHashKeyID:       state.runtime.current.id,
		Provider:              state.target.Provider,
		AccountRef:            strings.TrimSpace(s.cfg.Provider[state.target.Provider].KeyID),
		Model:                 state.target.Model,
		ModelGroup:            rc.rec.ResolvedGroup,
		ProviderDialect:       targetDialect(s.cfg.Provider[state.target.Provider], *state.target),
		Endpoint:              "responses",
		State:                 "in_progress",
		CreatedAt:             formatUsageTime(now),
		UpdatedAt:             formatUsageTime(now),
		ExpiresAt:             formatUsageTime(now.Add(state.runtime.retention)),
	}
	if row.ModelGroup == "" {
		row.ModelGroup = rc.rec.RequestedModel
	}
	row.ResponseLookupHash = state.runtime.current.hashLookup(upstreamID)
	var err error
	row.UpstreamIDCiphertext, row.UpstreamIDNonce, err = state.runtime.current.encryptUpstreamID(upstreamID, row.OwnerTokenFingerprint, row.ResponseLookupHash)
	if err != nil {
		state.failed = true
		return err
	}
	row.UpstreamIDKeyID = state.runtime.current.id
	if err := state.runtime.insert(&row); err != nil {
		state.failed = true
		rc.trace("response_affinity_store_failed", "failed to persist stored-response affinity", *state.target, 0, http.StatusBadGateway, "response_affinity_store_failed", false, 0)
		return err
	}
	state.captured = true
	if state.previous == nil {
		rc.trace("response_affinity_stored", "stored-response affinity recorded", *state.target, 0, 0, "", false, 0)
	}
	return nil
}

func (r *responsesAffinities) insert(row *responseAffinityRecord) error {
	if r == nil || r.store == nil || row == nil {
		return errors.New("responses affinity store is unavailable")
	}
	if err := r.store.db.Create(row).Error; err != nil {
		return err
	}
	return nil
}

// lookup resolves the response id inside the caller's ownership domain. The
// caller-visible id has already passed identifier decoding, so rewrite and
// passthrough modes hash the same upstream value. Another caller, tenant,
// project or environment gets the same not-found result as an unknown id.
func (r *responsesAffinities) lookup(owner responsesAffinityOwner, clientID string, now time.Time) (*responseAffinityRecord, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("responses affinity store is unavailable")
	}
	var lastErr error
	for _, keys := range r.keySets() {
		fingerprint := owner.Fingerprints[keys.id]
		if fingerprint == "" {
			continue
		}
		var row responseAffinityRecord
		err := r.store.db.Where(
			"owner_token_fingerprint = ? AND owner_fingerprint_key_id = ? AND tenant = ? AND project = ? AND environment = ? AND response_lookup_hash = ?",
			fingerprint, keys.id, owner.Tenant, owner.Project, owner.Environment, keys.hashLookup(clientID),
		).First(&row).Error
		if err == nil {
			if row.State == "deleted" || row.State == "expired" {
				return nil, gorm.ErrRecordNotFound
			}
			if expires, perr := time.Parse(time.RFC3339Nano, row.ExpiresAt); perr == nil && !expires.After(now) {
				return nil, gorm.ErrRecordNotFound
			}
			return &row, nil
		}
		lastErr = err
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, lastErr
}

// affinityClientErrorResponse writes the documented error shape for this
// feature without exposing internals. Both unknown and foreign ids produce the
// same 404.
func (s *Service) writeResponsesAffinityError(w http.ResponseWriter, rc *requestContext, err error) {
	var clientErr affinityClientError
	if !errors.As(err, &clientErr) {
		clientErr = affinityClientError{status: 503, code: "response_affinity_unavailable", message: "Stored response affinity is unavailable."}
	}
	if rc != nil {
		rc.trace("response_affinity_error", clientErr.code, Target{}, 0, clientErr.status, clientErr.code, false, 0)
		rc.rec.Status = clientErr.status
		code := clientErr.code
		rc.rec.Error = &code
		rc.rec.ErrorClass = code
		rc.rec.ErrorMessage = code
		s.finish(rc, clientErr.status, &code)
	}
	writeAPIJSON(w, clientErr.status, map[string]any{"error": map[string]any{
		"type":    "invalid_request_error",
		"code":    clientErr.code,
		"message": clientErr.message,
	}})
}

// responsesAffinityBlocksShadow reports whether a Responses request must not be
// shadow-replayed. A shadow replay goes to a different account, so a stored
// response continuation or a stored create would either leak the response to
// another account or create state that the router cannot own.
func (s *Service) responsesAffinityBlocksShadow(group string, dialect string, req *IRRequest) bool {
	if s.responsesAffinity == nil || req == nil || normalizeDialect(dialect) != "openai-responses" {
		return false
	}
	if !s.responsesAffinity.enabledFor(group) {
		return strings.TrimSpace(stringValue(req.Raw["previous_response_id"])) != ""
	}
	if strings.TrimSpace(stringValue(req.Raw["previous_response_id"])) != "" {
		return true
	}
	return !responsesExplicitNoStore(req)
}

// rewriteResponsesMapIDs maps provider ids to caller-visible ids on a unary
// Responses payload. It only touches protocol id fields, never content,
// arguments or metadata, and returns a clone so cached payloads are unchanged.
func rewriteResponsesMapIDs(body map[string]any, ids IdentifierTransform) map[string]any {
	if ids == nil || body == nil {
		return body
	}
	out := cloneMap(body)
	encode := func(m map[string]any, key string) {
		if v, ok := m[key].(string); ok && v != "" && !strings.HasPrefix(v, "mr_") {
			m[key] = ids.Encode(v)
		}
	}
	encode(out, "id")
	encode(out, "previous_response_id")
	encode(out, "item_id")
	encode(out, "response_id")
	encode(out, "first_id")
	encode(out, "last_id")
	if response, ok := out["response"].(map[string]any); ok {
		encode(response, "id")
		encode(response, "previous_response_id")
	}
	for _, key := range []string{"output", "data"} {
		items, ok := out[key].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			encode(m, "id")
			encode(m, "call_id")
			encode(m, "item_id")
			encode(m, "response_id")
		}
	}
	return out
}
