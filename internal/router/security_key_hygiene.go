// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

var errCallerTokenAlreadyRevoked = errors.New("caller token is already revoked")

func (s *Service) handleLeakedKeySelfRevoke(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.begin(w, r, "security")
	if !ok {
		return
	}
	if r.Context().Err() != nil || s.configControlPlane == nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	verifier := storedCallerTokenVerifier(rc.caller.cfg)
	if strings.TrimSpace(verifier) == "" {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	controlDB := s.configControlPlane.WithContext(r.Context())
	now := time.Now().UTC()
	err := controlDB.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&revokedCallerTokenRow{}).Where("token_hash = ?", verifier).Count(&existing).Error; err != nil {
			return err
		}
		if existing != 0 {
			return errCallerTokenAlreadyRevoked
		}
		var set configSetRow
		if err := tx.Where("runtime_scope = ? AND status = ? AND validation_status = ?", strings.TrimSpace(s.cfg.Server.ConfigSource.RuntimeScope), "active", "valid").First(&set).Error; err != nil {
			return err
		}
		insert := tx.Exec("INSERT INTO "+revokedCallerTokensTable+" (token_hash, revoked_at, reason_code) VALUES (?, ?, ?) ON CONFLICT (token_hash) DO NOTHING", verifier, now.Format(time.RFC3339Nano), "self_revocation")
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			return errCallerTokenAlreadyRevoked
		}
		meta, err := adminAuditMetaForWrite(r, adminAuthSubject{subject: rc.caller.cfg.ID, source: "caller_token"}, "security:caller_token", "revoke")
		if err != nil {
			return err
		}
		if _, err := bumpConfigRevision(tx, strings.TrimSpace(s.cfg.Server.ConfigSource.RuntimeScope), set.ID, "caller_token_self_revoke", rc.caller.cfg.ID, meta, "revoke", publicTokenID(rc.caller.cfg.TokenID)); err != nil {
			return err
		}
		return insertAdminCredentialEventTx(tx, AdminCredentialEvent{At: now, Kind: "caller_token", CredentialID: publicTokenID(rc.caller.cfg.TokenID), Subject: rc.caller.cfg.ID, Outcome: "success", ReasonCode: "self_revocation", RequestID: rc.id, Lifecycle: "revoke"})
	})
	if err != nil {
		if errors.Is(err, errCallerTokenAlreadyRevoked) {
			s.writeError(w, rc, http.StatusUnauthorized, "unauthorized")
			return
		}
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	if err := s.reloadActiveConfigFromDB(); err != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	rc.rec.TokenID = publicTokenID(rc.caller.cfg.TokenID)
	rc.rec.Status = http.StatusOK
	s.finish(rc, http.StatusOK, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "token_id": rc.rec.TokenID})
}
