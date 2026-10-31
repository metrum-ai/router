// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "net/http"

func isAnthropicModelRequest(r *http.Request) bool {
	return r.Header.Get("Anthropic-Version") != "" || (r.Header.Get("X-API-Key") != "" && r.Header.Get("Authorization") == "")
}

func (s *Service) writeAnthropicModelNotFound(w http.ResponseWriter, rc *requestContext, reason string) {
	code := reason
	rc.trace("request_error", code, Target{}, 0, http.StatusNotFound, code, false, 0)
	rc.rec.Status = http.StatusNotFound
	rc.rec.Error = &code
	rc.rec.ErrorClass = code
	rc.rec.ErrorMessage = code
	s.finish(rc, http.StatusNotFound, &code)
	w.Header().Set("Request-Id", rc.id)
	writeJSON(w, http.StatusNotFound, map[string]any{"type": "error", "request_id": rc.id, "error": map[string]any{"type": "not_found_error", "message": "Model not found."}})
}
