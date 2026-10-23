// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

const (
	contentCaptureScopeRequest       = "request"
	contentCaptureScopeResponse      = "response"
	contentCaptureScopeUpstreamError = "upstream_error"
	contentCaptureUnknownDomain      = "__unknown_content_capture_domain__"
)

type contentCaptureDecision struct {
	cfg   ContentCaptureConfig
	group string
}

type contentCaptureRecord struct {
	ID                   uint   `gorm:"column:id;primaryKey;autoIncrement"`
	RequestID            string `gorm:"column:request_id;type:text;not null;index:idx_request_content_request"`
	TS                   string `gorm:"column:ts;type:text;not null;index:idx_request_content_ts"`
	Scope                string `gorm:"column:scope;type:text;not null;index:idx_request_content_scope"`
	Sequence             int    `gorm:"column:sequence;not null"`
	CallerID             string `gorm:"column:caller_id;type:text;not null"`
	TokenID              string `gorm:"column:token_id;type:text;not null"`
	ResolvedGroup        string `gorm:"column:resolved_group;type:text;not null;index:idx_request_content_group"`
	TargetProvider       string `gorm:"column:target_provider;type:text;not null"`
	TargetModel          string `gorm:"column:target_model;type:text;not null"`
	InboundDialect       string `gorm:"column:inbound_dialect;type:text;not null"`
	TargetDialect        string `gorm:"column:target_dialect;type:text;not null"`
	ContentType          string `gorm:"column:content_type;type:text;not null"`
	ContentText          string `gorm:"column:content_text;type:text;not null"`
	EncryptionNonce      string `gorm:"column:encryption_nonce;type:text;not null;default:''"`
	EncryptionLocalKeyID string `gorm:"column:encryption_kms_key_id;type:text;not null;default:''"` // column name retained for schema compatibility; stores configured local_key_id
	Encrypted            bool   `gorm:"column:encrypted;not null;default:false"`
	ContentBytes         int    `gorm:"column:content_bytes;not null"`
	Truncated            bool   `gorm:"column:truncated;not null"`
	Redacted             bool   `gorm:"column:redacted;not null"`
	RedactionCount       int    `gorm:"column:redaction_count;not null"`
	ImagesCaptured       bool   `gorm:"column:images_captured;not null"`
	ImageCount           int    `gorm:"column:image_count;not null"`
	SourceStatus         int    `gorm:"column:source_status;not null"`
	RetentionUntil       string `gorm:"column:retention_until;type:text;not null;index:idx_request_content_retention"`
	DeletionRequested    bool   `gorm:"column:deletion_requested;not null;default:false"`
}

func (contentCaptureRecord) TableName() string {
	return "request_content_captures"
}

type contentCaptureHeaderRecord struct {
	ID                   uint   `gorm:"column:id;primaryKey;autoIncrement"`
	CaptureID            uint   `gorm:"column:capture_id;not null;index:idx_request_content_header_capture"`
	RequestID            string `gorm:"column:request_id;type:text;not null;index:idx_request_content_header_request"`
	Scope                string `gorm:"column:scope;type:text;not null"`
	Name                 string `gorm:"column:name;type:text;not null"`
	Value                string `gorm:"column:value;type:text;not null"`
	EncryptionNonce      string `gorm:"column:encryption_nonce;type:text;not null;default:''"`
	EncryptionLocalKeyID string `gorm:"column:encryption_kms_key_id;type:text;not null;default:''"` // column name retained for schema compatibility; stores configured local_key_id
	Encrypted            bool   `gorm:"column:encrypted;not null;default:false"`
	Redacted             bool   `gorm:"column:redacted;not null"`
}

func (contentCaptureHeaderRecord) TableName() string {
	return "request_content_headers"
}

type contentCaptureKeyResolver interface {
	ResolveContentCaptureKey(localKeyID string) ([]byte, error)
}

type envContentCaptureKeyResolver struct{}

func contentCaptureLocalKeyMaterial() string {
	if raw := strings.TrimSpace(os.Getenv("CONTENT_CAPTURE_LOCAL_KEY")); raw != "" {
		return raw
	}
	// Deprecated alias kept for low-risk rollout compatibility.
	return strings.TrimSpace(os.Getenv("CONTENT_CAPTURE_KMS_KEY"))
}

func (envContentCaptureKeyResolver) ResolveContentCaptureKey(localKeyID string) ([]byte, error) {
	if strings.TrimSpace(localKeyID) == "" {
		return nil, errors.New("content capture local key id is required")
	}
	raw := contentCaptureLocalKeyMaterial()
	if raw == "" {
		return nil, errors.New("CONTENT_CAPTURE_LOCAL_KEY is required when content capture is enabled")
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		key, err = base64.StdEncoding.DecodeString(raw)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("CONTENT_CAPTURE_LOCAL_KEY must be 32-byte hex or base64")
	}
	return key, nil
}

func configuredContentCaptureLocalKeyID(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.Server.ContentCapture.Enabled {
		return cfg.Server.ContentCapture.Encryption.LocalKeyID
	}
	for _, caller := range cfg.Callers {
		if caller.ContentCapture.Enabled {
			return caller.ContentCapture.Encryption.LocalKeyID
		}
	}
	for _, group := range cfg.Models {
		if group.ContentCapture.Enabled {
			return group.ContentCapture.Encryption.LocalKeyID
		}
	}
	return ""
}

func encryptContentCaptureValue(resolver contentCaptureKeyResolver, localKeyID, plaintext string) (string, string, error) {
	if resolver == nil {
		return "", "", errors.New("content capture key resolver is required")
	}
	key, err := resolver.ResolveContentCaptureKey(localKeyID)
	if err != nil {
		return "", "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), []byte(localKeyID))
	return base64.StdEncoding.EncodeToString(ciphertext), base64.StdEncoding.EncodeToString(nonce), nil
}

func decryptContentCaptureValue(key []byte, localKeyID, ciphertext, nonceText string) (string, error) {
	block, err := aes.NewCipher(key)
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
	ciphertextBytes, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, []byte(localKeyID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

type contentCaptureAuditRecord struct {
	ID            uint   `gorm:"column:id;primaryKey;autoIncrement"`
	TS            string `gorm:"column:ts;type:text;not null;index:idx_request_content_audit_ts"`
	Action        string `gorm:"column:action;type:text;not null;index:idx_request_content_audit_action"`
	RequestID     string `gorm:"column:request_id;type:text;not null;index:idx_request_content_audit_request"`
	ActorCallerID string `gorm:"column:actor_caller_id;type:text;not null"`
	ActorTokenID  string `gorm:"column:actor_token_id;type:text;not null"`
	Scope         string `gorm:"column:scope;type:text;not null"`
	RowsAffected  int64  `gorm:"column:rows_affected;not null"`
	Reason        string `gorm:"column:reason;type:text;not null"`
}

func (contentCaptureAuditRecord) TableName() string {
	return "request_content_audit_events"
}

func (s *Service) contentCaptureFor(caller *callerRuntime, groupName string, group ModelGroup) contentCaptureDecision {
	if s == nil || s.cfg == nil {
		return contentCaptureDecision{}
	}
	cfg := s.cfg.Server.ContentCapture
	if caller != nil && caller.cfg.ContentCapture.Enabled {
		cfg = mergeContentCaptureConfig(cfg, caller.cfg.ContentCapture)
	}
	if group.ContentCapture.Enabled {
		cfg = mergeContentCaptureConfig(cfg, group.ContentCapture)
	}
	return contentCaptureDecision{cfg: cfg, group: groupName}
}

func mergeContentCaptureConfig(base, override ContentCaptureConfig) ContentCaptureConfig {
	base.Enabled = override.Enabled
	if override.RetentionDays != 0 {
		base.RetentionDays = override.RetentionDays
	}
	base.CaptureRequest = override.CaptureRequest
	base.CaptureResponse = override.CaptureResponse
	base.CaptureToolCalls = override.CaptureToolCalls
	base.CaptureImages = override.CaptureImages
	base.CaptureUpstreamErrors = override.CaptureUpstreamErrors
	if override.CaptureHeadersAllowlist != nil {
		base.CaptureHeadersAllowlist = append([]string(nil), override.CaptureHeadersAllowlist...)
	}
	if override.RedactBeforeStorage != nil {
		base.RedactBeforeStorage = override.RedactBeforeStorage
	}
	if override.RedactionPatterns != nil {
		base.RedactionPatterns = append([]ContentCaptureRedactionRule(nil), override.RedactionPatterns...)
	}
	if override.MaxCaptureBytes != 0 {
		base.MaxCaptureBytes = override.MaxCaptureBytes
	}
	base.Encryption = override.Encryption
	return base
}

func (s *Service) captureRequestContent(rc *requestContext, req *IRRequest, header http.Header, decision contentCaptureDecision) {
	if rc == nil || req == nil || !decision.cfg.Enabled || !decision.cfg.CaptureRequest {
		return
	}
	payload := contentCaptureRequestPayload(req, decision.cfg)
	s.captureContent(rc, decision, contentCaptureScopeRequest, 0, "application/json", payload, requestImageCount(req), 0, header)
}

func (s *Service) captureResponseContent(rc *requestContext, resp *IRResponse, decision contentCaptureDecision) {
	if rc == nil || resp == nil || !decision.cfg.Enabled || !decision.cfg.CaptureResponse {
		return
	}
	payload := map[string]any{
		"id":          resp.ID,
		"model":       resp.Model,
		"text":        resp.Text,
		"stop_reason": resp.StopReason,
		"warnings":    resp.Warnings,
	}
	if decision.cfg.CaptureToolCalls && resp.Raw != nil {
		payload["raw"] = resp.Raw
	}
	s.captureContent(rc, decision, contentCaptureScopeResponse, 0, "application/json", payload, 0, 0, nil)
}

func (s *Service) captureUpstreamErrorContent(rc *requestContext, req *IRRequest, dec decision, attempts int, err upstreamError, captureDecision contentCaptureDecision, status int) {
	if rc == nil || !captureDecision.cfg.Enabled || !captureDecision.cfg.CaptureUpstreamErrors {
		return
	}
	targets := append([]Target{dec.Target}, dec.Fallbacks...)
	attempted := make([]string, 0, min(attempts, len(targets)))
	for i := 0; i < attempts && i < len(targets); i++ {
		attempted = append(attempted, targets[i].Provider+"/"+targets[i].Model)
	}
	payload := map[string]any{
		"model":         "",
		"error_class":   err.Class,
		"message":       sanitizeDiagnosticText(err.Message, true, s.diagnosticMaxErrorBytes()),
		"retryable":     err.Retryable,
		"attempts":      attempts,
		"attempted":     strings.Join(attempted, ","),
		"source_status": err.StatusCode,
	}
	if req != nil {
		payload["model"] = req.Model
	}
	s.captureContent(rc, captureDecision, contentCaptureScopeUpstreamError, attempts, "application/json", payload, 0, status, nil)
}

func (s *Service) captureContent(rc *requestContext, decision contentCaptureDecision, scope string, seq int, contentType string, payload any, imageCount, sourceStatus int, header http.Header) {
	if s == nil || s.usage == nil || rc == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw, _ = json.Marshal(map[string]string{"error": "content capture marshal failed"})
	}
	text, redactions := redactContentCaptureText(string(raw), decision.cfg)
	text, truncated := truncateContentCaptureText(text, decision.cfg.MaxCaptureBytes)
	plainBytes := len(text)
	ciphertext, nonce, err := encryptContentCaptureValue(s.contentCaptureKeys, decision.cfg.Encryption.LocalKeyID, text)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	retentionUntil := now.Add(time.Duration(decision.cfg.RetentionDays) * 24 * time.Hour)
	rec := &contentCaptureRecord{
		RequestID:            rc.id,
		TS:                   formatUsageTime(now),
		Scope:                scope,
		Sequence:             seq,
		CallerID:             rc.rec.CallerID,
		TokenID:              publicTokenID(rc.rec.TokenID),
		ResolvedGroup:        defaultString(rc.rec.ResolvedGroup, decision.group),
		TargetProvider:       rc.rec.TargetProvider,
		TargetModel:          rc.rec.TargetModel,
		InboundDialect:       rc.rec.InboundDialect,
		TargetDialect:        rc.rec.TargetDialect,
		ContentType:          contentType,
		ContentText:          ciphertext,
		EncryptionNonce:      nonce,
		EncryptionLocalKeyID: decision.cfg.Encryption.LocalKeyID,
		Encrypted:            true,
		ContentBytes:         plainBytes,
		Truncated:            truncated,
		Redacted:             redactions > 0,
		RedactionCount:       redactions,
		ImagesCaptured:       decision.cfg.CaptureImages,
		ImageCount:           imageCount,
		SourceStatus:         sourceStatus,
		RetentionUntil:       formatUsageTime(retentionUntil),
	}
	job := usageContentCaptureJob{record: rec, headers: s.contentCaptureHeaders(rc.id, scope, header, decision.cfg)}
	if s.usage.writer != nil {
		s.usage.writer.record(job)
		return
	}
	_ = s.usage.writeUsageJobs(context.Background(), []any{job})
}

func (s *Service) contentCaptureHeaders(requestID, scope string, header http.Header, cfg ContentCaptureConfig) []*contentCaptureHeaderRecord {
	var records []*contentCaptureHeaderRecord
	for _, name := range cfg.CaptureHeadersAllowlist {
		canonical := http.CanonicalHeaderKey(name)
		for _, value := range header.Values(canonical) {
			redacted, count := redactContentCaptureText(value, cfg)
			ciphertext, nonce, err := encryptContentCaptureValue(s.contentCaptureKeys, cfg.Encryption.LocalKeyID, redacted)
			if err != nil {
				continue
			}
			records = append(records, &contentCaptureHeaderRecord{
				RequestID:            requestID,
				Scope:                scope,
				Name:                 canonical,
				Value:                ciphertext,
				EncryptionNonce:      nonce,
				EncryptionLocalKeyID: cfg.Encryption.LocalKeyID,
				Encrypted:            true,
				Redacted:             count > 0,
			})
		}
	}
	return records
}

func contentCaptureRequestPayload(req *IRRequest, cfg ContentCaptureConfig) map[string]any {
	raw, _ := json.Marshal(req)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	delete(payload, "raw")
	if !cfg.CaptureToolCalls {
		delete(payload, "tools")
		redactToolMessages(payload)
	}
	if !cfg.CaptureImages {
		redactImages(payload)
	}
	return payload
}

func redactToolMessages(v any) {
	switch x := v.(type) {
	case map[string]any:
		if role, _ := x["role"].(string); strings.EqualFold(role, "tool") {
			x["content"] = "[TOOL_CONTENT_REDACTED]"
			delete(x, "parts")
		}
		for _, child := range x {
			redactToolMessages(child)
		}
	case []any:
		for _, child := range x {
			redactToolMessages(child)
		}
	}
}

func redactImages(v any) {
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); strings.EqualFold(typ, "image") {
			for _, key := range []string{"image_url", "data", "file_id"} {
				if _, ok := x[key]; ok {
					x[key] = "[IMAGE_REDACTED]"
				}
			}
		}
		for _, child := range x {
			redactImages(child)
		}
	case []any:
		for _, child := range x {
			redactImages(child)
		}
	}
}

func redactContentCaptureText(text string, cfg ContentCaptureConfig) (string, int) {
	count := 0
	redacted := redactDiagnosticSecrets(text)
	if redacted != text {
		count++
		text = redacted
	}
	for _, rule := range cfg.RedactionPatterns {
		re, err := regexp.Compile(rule.Expression)
		if err != nil {
			continue
		}
		matches := re.FindAllStringIndex(text, -1)
		if len(matches) == 0 {
			continue
		}
		count += len(matches)
		text = re.ReplaceAllString(text, "[REDACTED_"+sanitizeCaptureRuleName(rule.Name)+"]")
	}
	return text, count
}

func sanitizeCaptureRuleName(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "CUSTOM"
	}
	return out
}

func truncateContentCaptureText(text string, maxBytes int) (string, bool) {
	if maxBytes > 0 && len(text) > maxBytes {
		return text[:maxBytes], true
	}
	return text, false
}

func (s *usageStore) PurgeExpiredContentCaptures(now time.Time, actorCallerID, actorTokenID, reason string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	cutoff := formatUsageTime(now.UTC())
	var rows []contentCaptureRecord
	if err := s.db.Where("retention_until <= ?", cutoff).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]uint, 0, len(rows))
	requestRows := map[string]int64{}
	for _, row := range rows {
		ids = append(ids, row.ID)
		requestRows[row.RequestID]++
	}
	if err := s.db.Where("capture_id IN ?", ids).Delete(&contentCaptureHeaderRecord{}).Error; err != nil {
		return 0, err
	}
	result := s.db.Where("id IN ?", ids).Delete(&contentCaptureRecord{})
	if result.Error != nil {
		return 0, result.Error
	}
	for requestID, rowCount := range requestRows {
		s.emitContentCaptureAudit("retention_purge", requestID, actorCallerID, actorTokenID, "", rowCount, reason)
	}
	return result.RowsAffected, nil
}

func (s *usageStore) DeleteContentCapturesByRequestID(requestID, actorCallerID, actorTokenID, reason string) (int64, error) {
	if s == nil || s.db == nil || strings.TrimSpace(requestID) == "" {
		return 0, nil
	}
	// Maintenance deletion must not race captures already accepted by the
	// asynchronous serving writer and resurrect them after acknowledging delete.
	if s.writer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), s.writer.drainTimeout)
		defer cancel()
		if err := s.writer.flush(ctx); err != nil {
			return 0, err
		}
	}
	var rows []contentCaptureRecord
	if err := s.db.Where("request_id = ?", requestID).Find(&rows).Error; err != nil {
		return 0, err
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) > 0 {
		if err := s.db.Where("capture_id IN ?", ids).Delete(&contentCaptureHeaderRecord{}).Error; err != nil {
			return 0, err
		}
	}
	result := s.db.Where("request_id = ?", requestID).Delete(&contentCaptureRecord{})
	if result.Error != nil {
		return 0, result.Error
	}
	s.emitContentCaptureAudit("request_delete", requestID, actorCallerID, actorTokenID, "", result.RowsAffected, reason)
	return result.RowsAffected, nil
}

func (s *usageStore) ContentCaptureTargetDomainsByRequestID(requestID string) ([]string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(requestID) == "" {
		return nil, nil
	}
	// Authorization must see capture-time domains for already queued captures,
	// never fall back to a caller's current domain because telemetry is pending.
	if s.writer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), s.writer.drainTimeout)
		defer cancel()
		if err := s.writer.flush(ctx); err != nil {
			return nil, err
		}
	}
	var captureCount int64
	if err := s.db.Model(&contentCaptureRecord{}).Where("request_id = ?", requestID).Count(&captureCount).Error; err != nil {
		return nil, err
	}
	if captureCount == 0 {
		return nil, nil
	}
	var rows []usageRecord
	if err := s.db.Model(&usageRecord{}).
		Where("request_id = ?", requestID).
		Select("caller_project", "caller_environment").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []string{contentCaptureUnknownDomain}, nil
	}
	domains := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		domain := authzDomainForProjectEnvironment(row.CallerProject, row.CallerEnvironment)
		if domain != "" && !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
		}
	}
	if len(domains) == 0 {
		return []string{contentCaptureUnknownDomain}, nil
	}
	return domains, nil
}

func (s *usageStore) emitContentCaptureAudit(action, requestID, actorCallerID, actorTokenID, scope string, rows int64, reason string) {
	if s == nil || s.db == nil {
		return
	}
	rec := &contentCaptureAuditRecord{
		TS:            formatUsageTime(time.Now().UTC()),
		Action:        action,
		RequestID:     requestID,
		ActorCallerID: actorCallerID,
		ActorTokenID:  publicTokenID(actorTokenID),
		Scope:         scope,
		RowsAffected:  rows,
		Reason:        reason,
	}
	_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(rec).Error
}

func (s *usageStore) RecordContentCaptureRead(requestID, actorCallerID, actorTokenID, scope, reason string, rows int64) {
	if s == nil || s.db == nil {
		return
	}
	s.emitContentCaptureAudit("read", requestID, actorCallerID, actorTokenID, scope, rows, reason)
}

func (cfg ContentCaptureConfig) String() string {
	return fmt.Sprintf("enabled=%t request=%t response=%t upstream_errors=%t retention_days=%d", cfg.Enabled, cfg.CaptureRequest, cfg.CaptureResponse, cfg.CaptureUpstreamErrors, cfg.RetentionDays)
}
