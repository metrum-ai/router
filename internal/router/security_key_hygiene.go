// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"time"
)

func (s *Service) handleLeakedKeySelfRevoke(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.begin(w, r, "security")
	if !ok {
		return
	}
	if r.Context().Err() != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	if err := s.quota.disableCaller(rc.caller); err != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	tokenID := publicTokenID(rc.caller.cfg.TokenID)
	event := securityAccessEvent{
		TS:                time.Now().UTC(),
		RequestID:         rc.id,
		EventType:         "caller_token_revoked",
		Surface:           "security",
		HTTPMethod:        http.MethodPost,
		PathTemplate:      "/v1/security/leaked-keys",
		StatusCode:        http.StatusOK,
		Outcome:           "success",
		ReasonCode:        "self_revocation",
		AuthSubject:       authzSubjectForCaller(rc.caller).subject,
		AuthSource:        "caller_token",
		CallerID:          rc.caller.cfg.ID,
		CallerUser:        callerUser(rc.caller.cfg),
		CallerProject:     callerProject(rc.caller.cfg),
		CallerEnvironment: callerEnvironment(rc.caller.cfg),
		TokenID:           tokenID,
	}
	if err := s.usage.RecordSecurityAccessEvent(event); err != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-audit-unavailable")
		return
	}
	rc.rec.TokenID = tokenID
	rc.rec.Status = http.StatusOK
	s.finish(rc, http.StatusOK, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "token_id": tokenID})
}
