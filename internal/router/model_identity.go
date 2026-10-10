// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Caller-facing model identity (issue #254), Metrum AI Router.
//
// IRResponse.Model and Target.Model stay the upstream provider model so usage
// rows, attempt rows, logs, metrics and cache keys keep recording what served
// the request. The public name is applied only where a protocol `model` field
// is written to the caller.

// publicModel returns the router model group to report to the caller when
// server.responses.model_identity is requested_group, or "" when caller-facing
// bodies should keep the upstream model unchanged.
func (rc *requestContext) publicModel() string {
	if rc == nil || rc.modelIdentity != ModelIdentityRequestedGroup {
		return ""
	}
	return rc.rec.RequestedModel
}

// callerModel returns the caller-facing name for a response produced by the
// given upstream model.
func (rc *requestContext) callerModel(upstream string) string {
	if public := rc.publicModel(); public != "" {
		return public
	}
	return upstream
}

// withPublicModel returns raw with its top-level model set to public. raw is
// shallow-copied because it can be shared with the response cache and logs.
func withPublicModel(raw map[string]any, public string) map[string]any {
	if public == "" || raw == nil {
		return raw
	}
	out := cloneMap(raw)
	out["model"] = public
	return out
}
