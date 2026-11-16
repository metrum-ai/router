// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

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
	var set configSetRow
	if err := controlDB.Where("runtime_scope = ? AND status = ? AND validation_status = ?", strings.TrimSpace(s.cfg.Server.ConfigSource.RuntimeScope), "active", "valid").First(&set).Error; err != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "revocation-unavailable")
		return
	}
	now := time.Now().UTC()
	err := controlDB.Transaction(func(tx *gorm.DB) error {
		var row callerRow
		if err := tx.Where("config_set_id = ? AND caller_id = ?", set.ID, rc.caller.cfg.ID).First(&row).Error; err != nil {
			return err
		}
		if row.TokenSHA256 != verifier {
			return gorm.ErrRecordNotFound
		}
		if strings.TrimSpace(row.RevokedTokenHash) == verifier {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Exec("UPDATE router_config_callers SET revoked_token_hash = ? WHERE config_set_id = ? AND caller_id = ?", verifier, set.ID, row.CallerID).Error; err != nil {
			return err
		}
		var doc configControlPlaneDocumentRow
		if err := tx.Where("config_set_id = ?", set.ID).First(&doc).Error; err != nil {
			return err
		}
		patched, err := PatchCanonicalCallersYAML([]byte(doc.CanonicalYAML), func(callers []CallerConfig) ([]CallerConfig, error) {
			for i := range callers {
				if callers[i].ID == row.CallerID {
					callers[i].RevokedTokenHash = verifier
					return callers, nil
				}
			}
			return nil, gorm.ErrRecordNotFound
		})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(patched)
		if err := tx.Exec("UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?", string(patched), hex.EncodeToString(digest[:]), now.Format(time.RFC3339Nano), set.ID).Error; err != nil {
			return err
		}
		meta, err := adminAuditMetaForWrite(r, adminAuthSubject{subject: rc.caller.cfg.ID, source: "caller_token"}, "security:caller_token", "revoke")
		if err != nil {
			return err
		}
		if _, err := bumpConfigRevision(tx, strings.TrimSpace(s.cfg.Server.ConfigSource.RuntimeScope), set.ID, "caller_token_self_revoke", row.CallerID, meta, "revoke", publicTokenID(row.TokenID)); err != nil {
			return err
		}
		return insertAdminCredentialEventTx(tx, AdminCredentialEvent{At: now, Kind: "caller_token", CredentialID: publicTokenID(row.TokenID), Subject: row.CallerID, Outcome: "success", ReasonCode: "self_revocation", RequestID: rc.id, Lifecycle: "revoke"})
	})
	if err != nil {
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
