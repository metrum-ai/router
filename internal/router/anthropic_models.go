// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"strconv"
	"strings"
)

func isAnthropicModelRequest(r *http.Request) bool {
	return r.Header.Get("Anthropic-Version") != "" || (r.Header.Get("X-API-Key") != "" && r.Header.Get("Authorization") == "")
}

func anthropicModel(m map[string]any) map[string]any {
	return map[string]any{"type": "model", "id": m["id"], "display_name": m["display_name"], "created_at": "1970-01-01T00:00:00Z"}
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

func (s *Service) writeAnthropicModelList(w http.ResponseWriter, r *http.Request, rc *requestContext) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			s.writeError(w, rc, http.StatusBadRequest, "invalid-limit")
			return
		}
		limit = n
	}
	before, after := strings.TrimSpace(r.URL.Query().Get("before_id")), strings.TrimSpace(r.URL.Query().Get("after_id"))
	if before != "" && after != "" {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-pagination")
		return
	}
	models := s.callerModelMetadata(rc.caller)
	start, end := 0, len(models)
	cursor := before
	if after != "" {
		cursor = after
	}
	if cursor != "" {
		index := -1
		for i, m := range models {
			if m["id"] == cursor {
				index = i
				break
			}
		}
		if index < 0 {
			s.writeError(w, rc, http.StatusBadRequest, "invalid-cursor")
			return
		}
		if after != "" {
			start = index + 1
		} else {
			end = index
		}
	}
	hasMore := false
	if end-start > limit {
		hasMore = true
		if before != "" {
			start = end - limit
		} else {
			end = start + limit
		}
	}
	data := make([]map[string]any, 0, end-start)
	for _, m := range models[start:end] {
		data = append(data, anthropicModel(m))
	}
	var first, last any
	if len(data) > 0 {
		first, last = data[0]["id"], data[len(data)-1]["id"]
	}
	defer s.finish(rc, http.StatusOK, nil)
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": hasMore, "first_id": first, "last_id": last})
}
