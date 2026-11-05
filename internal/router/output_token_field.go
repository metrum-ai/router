// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"strings"
)

// outputCapFields are the two OpenAI Chat output-cap fields. Providers differ on
// which one they accept: `max_tokens` is the legacy field that OpenAI-compatible
// servers have always taken, while OpenAI's newer models require
// `max_completion_tokens` and reject `max_tokens` outright (#778).
var outputCapFields = [...]struct{ legacy, modern string }{{"max_tokens", "max_completion_tokens"}}

// outputTokenFieldRetryBody reports whether a provider 400 unambiguously asks for
// the other output-cap field, and returns a copy of the outbound body with the
// cap moved to that field.
//
// It only reacts to the OpenAI-shaped error envelope whose `param` names the cap
// field that was sent and whose message names the other field, which is the
// provider's own instruction ("Unsupported parameter: 'max_tokens' is not
// supported with this model. Use 'max_completion_tokens' instead."). Anything
// less specific is left alone, so an unrelated 400 is never retried and the
// router never guesses.
func outputTokenFieldRetryBody(sent, providerBody []byte) ([]byte, string, bool) {
	if len(sent) == 0 || len(providerBody) == 0 {
		return nil, "", false
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(providerBody, &envelope); err != nil {
		return nil, "", false
	}
	param := strings.TrimSpace(envelope.Error.Param)
	message := strings.ToLower(envelope.Error.Message)
	if param == "" || message == "" {
		return nil, "", false
	}

	for _, pair := range outputCapFields {
		// The provider rejected the legacy field and named the modern one.
		if param == pair.legacy && strings.Contains(message, pair.modern) {
			if next, ok := moveOutputCapField(sent, pair.legacy, pair.modern); ok {
				return next, pair.modern, true
			}
		}
		// The provider rejected the modern field and named the legacy one. The
		// message must not merely repeat max_completion_tokens' own name.
		if param == pair.modern && mentionsLegacyCapField(message, pair.legacy) {
			if next, ok := moveOutputCapField(sent, pair.modern, pair.legacy); ok {
				return next, pair.legacy, true
			}
		}
	}
	return nil, "", false
}

// mentionsLegacyCapField reports whether the message names max_tokens as the
// field to use, without that match coming from max_completion_tokens.
func mentionsLegacyCapField(message, legacy string) bool {
	return strings.Contains(strings.ReplaceAll(message, "max_completion_tokens", ""), legacy)
}

// moveOutputCapField returns body with the cap value moved from one field to the
// other. It refuses when the source field is absent, when the destination is
// already present (the caller's request already carries both, so the provider's
// complaint is not the one this retry heals), or when the body is not an object.
func moveOutputCapField(body []byte, from, to string) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false
	}
	value, ok := fields[from]
	if !ok {
		return nil, false
	}
	if _, present := fields[to]; present {
		return nil, false
	}
	delete(fields, from)
	fields[to] = value
	next, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	// Keep the JSON object semantics; map marshaling already yields an object.
	if !bytes.HasPrefix(bytes.TrimSpace(next), []byte("{")) {
		return nil, false
	}
	return next, true
}
