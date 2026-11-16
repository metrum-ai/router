// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var toolApprovalIDPattern = regexp.MustCompile(`^approval_[a-f0-9]{32}$`)

func (s *Service) handleToolApprovalResolution(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("approval_id"))
	if !toolApprovalIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_approval_id"})
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4097))
	decoder.DisallowUnknownFields()
	var request struct {
		Decision string `json:"decision"`
	}
	if err := decoder.Decode(&request); err != nil || (request.Decision != "release" && request.Decision != "deny") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_decision"})
		return
	}
	if !s.toolApprovals.resolve(id, request.Decision) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "approval_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval_id": id, "decision": request.Decision})
}
