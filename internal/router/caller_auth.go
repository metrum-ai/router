// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Caller bearer-token lookup for the Metrum AI Router request path.
//
// Every request authenticates, so the steady-state path has to be cheap:
//
//  1. token_hash callers are indexed by their public token_id. A token
//     "<token_id>_<secret>" selects at most one candidate with a few map
//     lookups. Nothing secret is hashed to find it.
//  2. That candidate holds the keyed digest (HMAC-SHA3-512 under a
//     per-process random key) of the last token that passed the PBKDF2
//     check. A matching digest, compared with hmac.Equal, authenticates
//     without re-running the KDF.
//  3. On a digest miss the PBKDF2-HMAC-SHA3-512 verifier runs once, inside a
//     small semaphore so a flood of wrong secrets for a known token_id can
//     only use a bounded share of the CPU.
//  4. Deprecated token_sha256 callers are checked last, by the original
//     constant-time scan, only when such callers are configured.
//
// The verified-digest cache is bounded by construction: one slot per
// token_hash caller, and only verified tokens are stored.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha3"
	"crypto/subtle"
	"hash"
	"io"
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

const callerAuthDigestBytes = 64

type callerAuthDigest [callerAuthDigestBytes]byte

type callerAuthEntry struct {
	caller    *callerRuntime
	tokenID   string
	tokenHash string
	verifier  callerTokenVerifier
	verified  atomic.Pointer[callerAuthDigest]
}

// callerAuthIndex is immutable once built, apart from each entry's verified
// digest slot. Config reloads build a new index and swap it in.
type callerAuthIndex struct {
	byTokenID      map[string]*callerAuthEntry
	maxTokenIDLen  int
	legacyBySHA256 map[string]*callerRuntime
}

// callerAuthState holds the per-process secrets and limits shared by every
// index generation.
type callerAuthState struct {
	digestKey []byte
	kdfSlots  chan struct{}
	index     atomic.Pointer[callerAuthIndex]
	// macs pools keyed HMAC-SHA3-512 instances; Reset restores the keyed
	// state, so steady-state requests do not allocate hash state.
	macs sync.Pool
}

func newCallerAuthState() *callerAuthState {
	key := make([]byte, callerAuthDigestBytes)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand does not fail on supported platforms; refuse to run
		// with a predictable key if it ever does.
		panic("caller auth: read random digest key: " + err.Error())
	}
	slots := runtime.GOMAXPROCS(0) / 2
	if slots < 1 {
		slots = 1
	}
	st := &callerAuthState{digestKey: key, kdfSlots: make(chan struct{}, slots)}
	st.index.Store(&callerAuthIndex{byTokenID: map[string]*callerAuthEntry{}, legacyBySHA256: map[string]*callerRuntime{}})
	return st
}

// rebuild indexes callers and logs one deprecation warning per caller that
// still uses token_sha256. Verified digests carry over for callers whose
// token_id and token_hash did not change.
func (st *callerAuthState) rebuild(callers []*callerRuntime) {
	previous := st.index.Load()
	next := &callerAuthIndex{
		byTokenID:      make(map[string]*callerAuthEntry, len(callers)),
		legacyBySHA256: map[string]*callerRuntime{},
	}
	for _, rt := range callers {
		if rt == nil {
			continue
		}
		if tokenHash := strings.TrimSpace(rt.cfg.TokenHash); tokenHash != "" {
			verifier, err := parseCallerTokenHash(tokenHash)
			if err != nil {
				// Config validation rejects malformed hashes; skip defensively.
				log.Printf("caller %s token_hash is invalid; caller cannot authenticate", rt.cfg.ID)
				continue
			}
			tokenID := strings.TrimSpace(rt.cfg.TokenID)
			entry := &callerAuthEntry{caller: rt, tokenID: tokenID, tokenHash: tokenHash, verifier: verifier}
			if previous != nil {
				if old := previous.byTokenID[tokenID]; old != nil && old.tokenHash == tokenHash {
					entry.verified.Store(old.verified.Load())
				}
			}
			next.byTokenID[tokenID] = entry
			if len(tokenID) > next.maxTokenIDLen {
				next.maxTokenIDLen = len(tokenID)
			}
			continue
		}
		if legacy := strings.TrimSpace(rt.cfg.TokenSHA256); legacy != "" {
			next.legacyBySHA256[strings.ToLower(legacy)] = rt
			log.Printf("caller %s uses deprecated token_sha256; rotate it to token_hash (%s) (see #281)", rt.cfg.ID, CallerTokenHashScheme)
		}
	}
	st.index.Store(next)
}

func newSHA3512() hash.Hash { return sha3.New512() }

func (st *callerAuthState) digest(token string) callerAuthDigest {
	mac, _ := st.macs.Get().(hash.Hash)
	if mac == nil {
		mac = hmac.New(newSHA3512, st.digestKey)
	} else {
		mac.Reset()
	}
	_, _ = io.WriteString(mac, token)
	var out callerAuthDigest
	mac.Sum(out[:0])
	st.macs.Put(mac)
	return out
}

// lookup returns the authenticated caller and the token label for usage
// records, or nil when no caller matches.
func (st *callerAuthState) lookup(token string) (*callerRuntime, string) {
	if token == "" || len(token) > maxCallerTokenBytes {
		return nil, ""
	}
	idx := st.index.Load()
	if entry := idx.candidate(token); entry != nil && st.verify(entry, token) {
		return entry.caller, publicTokenID(entry.caller.cfg.TokenID)
	}
	if len(idx.legacyBySHA256) == 0 {
		return nil, ""
	}
	sumHex := legacyCallerTokenSHA256Hex(token)
	for configured, caller := range idx.legacyBySHA256 {
		if subtle.ConstantTimeCompare([]byte(configured), []byte(sumHex)) == 1 {
			label := "sha256:" + sumHex[:12]
			if caller.cfg.TokenID != "" {
				label = publicTokenID(caller.cfg.TokenID)
			}
			return caller, label
		}
	}
	return nil, ""
}

// candidate finds the token_hash caller whose token_id is followed by "_" at
// the start of token. Config validation guarantees at most one such caller.
func (idx *callerAuthIndex) candidate(token string) *callerAuthEntry {
	if idx == nil || len(idx.byTokenID) == 0 {
		return nil
	}
	limit := len(token) - 1
	if idx.maxTokenIDLen < limit {
		limit = idx.maxTokenIDLen
	}
	for i := 1; i <= limit; i++ {
		if token[i] != '_' {
			continue
		}
		if entry := idx.byTokenID[token[:i]]; entry != nil {
			return entry
		}
	}
	return nil
}

func (st *callerAuthState) verify(entry *callerAuthEntry, token string) bool {
	d := st.digest(token)
	if cached := entry.verified.Load(); cached != nil && hmac.Equal(cached[:], d[:]) {
		return true
	}
	st.kdfSlots <- struct{}{}
	ok := entry.verifier.matches(token)
	<-st.kdfSlots
	if ok {
		entry.verified.Store(&d)
	}
	return ok
}

// callerRuntimesInConfigOrder lists runtimes in config order so startup
// deprecation warnings are deterministic.
func callerRuntimesInConfigOrder(cfg *Config, byID map[string]*callerRuntime) []*callerRuntime {
	out := make([]*callerRuntime, 0, len(byID))
	seen := make(map[string]bool, len(byID))
	if cfg != nil {
		for _, c := range cfg.Callers {
			if rt := byID[c.ID]; rt != nil && !seen[c.ID] {
				out = append(out, rt)
				seen[c.ID] = true
			}
		}
	}
	for id, rt := range byID {
		if !seen[id] {
			out = append(out, rt)
		}
	}
	return out
}

// callerTokenIDPrefixConflict reports two token_hash token_ids where one is
// an underscore-delimited prefix of the other, which would make the token_id
// lookup ambiguous.
func callerTokenIDPrefixConflict(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	return a != "" && strings.HasPrefix(b, a+"_")
}
