// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const trainingObject = "content:training"

type trainingOutcome struct {
	Version       string           `json:"version"`
	TaskHash      string           `json:"task_hash"`
	TaskSuccess   bool             `json:"task_success"`
	SchemaValid   bool             `json:"schema_valid"`
	Quality       float64          `json:"quality"`
	Backend       string           `json:"backend"`
	Seed          int64            `json:"seed"`
	ExpectedCalls []map[string]any `json:"expected_calls,omitempty"`
	ExpectedFinal string           `json:"expected_final,omitempty"`
}

type trainingFilters struct {
	TokenID              string            `json:"token_id"`
	Project              string            `json:"project"`
	Client               string            `json:"client"`
	Group                string            `json:"group"`
	From                 time.Time         `json:"from"`
	To                   time.Time         `json:"to"`
	ValidationFrom       time.Time         `json:"validation_from"`
	TestFrom             time.Time         `json:"test_from"`
	SuccessOnly          bool              `json:"success_only"`
	MinQuality           float64           `json:"min_quality"`
	TeacherTermsDecision map[string]string `json:"teacher_terms_decision"`
	TaskSource           map[string]string `json:"task_source"`
}

func (s *Service) trainingAdmin(w http.ResponseWriter, r *http.Request, action string) (uint, bool) {
	w.Header().Set("Cache-Control", "no-store")
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeJSON(w, 415, map[string]string{"error": "application-json-required"})
		return 0, false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			writeJSON(w, 403, map[string]string{"error": "cross-origin-forbidden"})
			return 0, false
		}
	}
	subject, ok := s.authenticateAdminSubject(w, r)
	if !ok {
		s.recordAdminSecurityAccess(r, adminAuthSubject{}, 401, "unauthorized", trainingObject, action)
		return 0, false
	}
	if !s.authorizeGlobalAdmin(subject, trainingObject, action) {
		s.recordAdminSecurityAccess(r, subject, 403, "training-forbidden", trainingObject, action)
		writeJSON(w, 403, map[string]string{"error": "training-forbidden"})
		return 0, false
	}
	if !s.securityReportsEnabled() {
		writeJSON(w, 503, map[string]string{"error": "security-audit-required"})
		return 0, false
	}
	// Unlike best-effort request telemetry, privileged export must fail closed
	// unless the security event is durably committed before any bytes leave.
	event := securityAccessEventRecord{TS: formatUsageTime(time.Now().UTC()), EventType: "training_" + action, Surface: "training", HTTPMethod: r.Method, PathTemplate: "/admin/training/" + action, StatusCode: 200, Outcome: "authorized_attempt", AdminSubject: subject.subject, AdminDomain: subject.domain, AuthSource: subject.source, ModelGroup: trainingObject, RequestedModel: action}
	if err := s.usage.db.Create(&event).Error; err != nil {
		writeJSON(w, 503, map[string]string{"error": "security-audit-failed"})
		return 0, false
	}
	return event.ID, true
}

func decodeTrainingBody(r *http.Request, value any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing input")
	}
	return nil
}

func (s *Service) handleTrainingOutcome(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trainingAdmin(w, r, "label"); !ok {
		return
	}
	if s.usage == nil {
		writeJSON(w, 503, map[string]string{"error": "content-store-disabled"})
		return
	}
	var label trainingOutcome
	if decodeTrainingBody(r, &label) != nil || label.Version != "router-outcome-v1" || len(label.TaskHash) != 64 || label.Quality < 0 || label.Quality > 1 || label.Backend != "acme-support-v1" || len(label.ExpectedFinal) > 1 || (label.ExpectedFinal != "" && !strings.Contains("ABCDEFGHIJ", label.ExpectedFinal)) {
		writeJSON(w, 400, map[string]string{"error": "invalid-outcome"})
		return
	}
	if _, err := hex.DecodeString(label.TaskHash); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid-task-hash"})
		return
	}
	var capture contentCaptureRecord
	// Labels cannot manufacture capture consent. Only an extant, non-expired,
	// complete encrypted request can receive a label. No raw transcript upload.
	err := s.usage.db.Where("request_id = ? AND scope = ? AND encrypted = ? AND truncated = ? AND deletion_requested = ? AND retention_until > ?", r.PathValue("request_id"), contentCaptureScopeRequest, true, false, false, formatUsageTime(time.Now().UTC())).First(&capture).Error
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "captured-request-not-found"})
		return
	}
	raw, _ := json.Marshal(label)
	text, nonce, err := encryptContentCaptureValue(s.contentCaptureKeys, capture.EncryptionLocalKeyID, string(raw))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "label-encryption-failed"})
		return
	}
	capture.ID = 0
	capture.Scope = "training_outcome_v1"
	capture.ContentText = text
	capture.EncryptionNonce = nonce
	capture.ContentBytes = len(raw)
	// Repeated feedback is rejected. Concurrent multi-replica duplicates are
	// also rejected by the exporter as ambiguous, never arbitrarily selected.
	var n int64
	s.usage.db.Model(&contentCaptureRecord{}).Where("request_id = ? AND scope = ?", capture.RequestID, capture.Scope).Count(&n)
	if n != 0 {
		writeJSON(w, 409, map[string]string{"error": "outcome-already-recorded"})
		return
	}
	if err := s.usage.db.Create(&capture).Error; err != nil {
		writeJSON(w, 500, map[string]string{"error": "label-write-failed"})
		return
	}
	writeJSON(w, 201, map[string]string{"status": "recorded"})
}

func trainingHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *Service) trainingPlain(c contentCaptureRecord) ([]byte, error) {
	if !c.Encrypted || c.Truncated || c.DeletionRequested {
		return nil, errors.New("ineligible capture")
	}
	key, err := s.contentCaptureKeys.ResolveContentCaptureKey(c.EncryptionLocalKeyID)
	if err != nil {
		return nil, err
	}
	plain, err := decryptContentCaptureValue(key, c.EncryptionLocalKeyID, c.ContentText, c.EncryptionNonce)
	return []byte(plain), err
}

func (s *Service) handleTrainingExport(w http.ResponseWriter, r *http.Request) {
	auditID, ok := s.trainingAdmin(w, r, "export")
	if !ok {
		return
	}
	if s.usage == nil {
		writeJSON(w, 503, map[string]string{"error": "content-store-disabled"})
		return
	}
	var f trainingFilters
	if decodeTrainingBody(r, &f) != nil || f.From.IsZero() || !f.From.Before(f.ValidationFrom) || !f.ValidationFrom.Before(f.TestFrom) || !f.TestFrom.Before(f.To) || f.MinQuality < 0 || f.MinQuality > 1 || f.TeacherTermsDecision["decision"] == "" || f.TeacherTermsDecision["training_outputs_allowed"] != "true" || f.TaskSource["license"] == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid-export-filters"})
		return
	}
	data, err := s.buildTrainingArchive(f, auditID)
	if err != nil {
		writeJSON(w, 422, map[string]string{"error": "export-failed"})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="router-capture.zip"`)
	w.Write(data)
}

func (s *Service) buildTrainingArchive(f trainingFilters, auditID uint) ([]byte, error) {
	var usages []usageRecord
	q := s.usage.db.Where("ts >= ? AND ts < ?", formatUsageTime(f.From), formatUsageTime(f.To))
	for column, value := range map[string]string{"token_id": f.TokenID, "caller_project": f.Project, "client": f.Client, "resolved_group": f.Group} {
		if value != "" {
			q = q.Where(column+" = ?", value)
		}
	}
	if err := q.Order("ts ASC, request_id ASC").Limit(10001).Find(&usages).Error; err != nil {
		return nil, err
	}
	if len(usages) > 10000 {
		return nil, errors.New("export too large; narrow range")
	}
	files := map[string]*bytes.Buffer{"train.jsonl": {}, "val.jsonl": {}, "test.jsonl": {}}
	counts := map[string]int{}
	seen := map[string]bool{}
	toolCounts := map[string]int{"identical_declarations_removed": 0, "rows_with_identical_duplicates": 0, "conflicting_rows_rejected": 0, "invalid_rows_rejected": 0}
	taskSplit := map[string]string{}
	for _, u := range usages {
		ts, err := time.Parse(time.RFC3339Nano, u.TS)
		if err != nil {
			return nil, err
		}
		split := "train.jsonl"
		if !ts.Before(f.TestFrom) {
			split = "test.jsonl"
		} else if !ts.Before(f.ValidationFrom) {
			split = "val.jsonl"
		}
		var captures []contentCaptureRecord
		if err := s.usage.db.Where("request_id = ? AND retention_until > ?", u.RequestID, formatUsageTime(time.Now().UTC())).Find(&captures).Error; err != nil {
			return nil, err
		}
		byScope := map[string]contentCaptureRecord{}
		for _, c := range captures {
			if _, exists := byScope[c.Scope]; exists {
				return nil, errors.New("ambiguous capture")
			}
			byScope[c.Scope] = c
		}
		req, ok := byScope[contentCaptureScopeRequest]
		if !ok {
			continue
		}
		resp, responseCaptured := byScope[contentCaptureScopeResponse]
		if !responseCaptured && split != "test.jsonl" {
			continue
		}
		labelCapture, ok := byScope["training_outcome_v1"]
		if !ok {
			continue
		}
		requestBytes, err := s.trainingPlain(req)
		if err != nil {
			continue
		}
		responseBytes := []byte(`{"text":"","stop_reason":"missing_response"}`)
		if responseCaptured {
			responseBytes, err = s.trainingPlain(resp)
			if err != nil {
				continue
			}
		}
		labelBytes, err := s.trainingPlain(labelCapture)
		if err != nil {
			continue
		}
		var label trainingOutcome
		if json.Unmarshal(labelBytes, &label) != nil || label.Version != "router-outcome-v1" || (split != "test.jsonl" && (label.Quality < f.MinQuality || (f.SuccessOnly && (!label.TaskSuccess || !label.SchemaValid)))) {
			continue
		}
		var request map[string]any
		var response map[string]any
		if json.Unmarshal(requestBytes, &request) != nil || json.Unmarshal(responseBytes, &response) != nil {
			continue
		}
		messages, ok := request["chat_messages"].([]any)
		if !ok || len(messages) == 0 || (split != "test.jsonl" && !trainingChatValid(messages)) {
			continue
		}
		// Require a completed assistant turn. The final request already includes
		// every intermediate assistant call and deterministic tool result.
		text, ok := response["text"].(string)
		if split != "test.jsonl" && (response["stop_reason"] == "tool_calls" || !ok || text == "" || (response["stop_reason"] != "stop" && response["stop_reason"] != "end_turn")) {
			continue
		}
		last := map[string]any{"role": "assistant", "content": text}
		if split == "test.jsonl" && response["stop_reason"] == "tool_calls" {
			if raw, ok := response["raw"].(map[string]any); ok {
				if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]any); ok {
						if canonical := captureChatMessages([]any{choice["message"]}); len(canonical) == 1 {
							last = canonical[0].(map[string]any)
						}
					}
				}
			}
		}
		messages = append(messages, last)
		tools, removed, toolErr := trainingTools(request["tools"])
		if toolErr != nil {
			if errors.Is(toolErr, errConflictingTrainingTools) {
				toolCounts["conflicting_rows_rejected"]++
			} else {
				toolCounts["invalid_rows_rejected"]++
			}
			continue
		}
		canonical, _ := json.Marshal(map[string]any{"messages": messages, "tools": tools})
		// Export-only filtering never restores PII. Filter parsed string leaves so
		// replacements cannot corrupt JSON syntax or function-argument escaping.
		var transcript any
		json.Unmarshal(canonical, &transcript)
		transcript = trainingPrivacy(transcript)
		// Redaction can also collapse names; recheck before hashing/emission.
		cleanTools, extraRemoved, toolErr := trainingTools(transcript.(map[string]any)["tools"])
		if toolErr != nil {
			if errors.Is(toolErr, errConflictingTrainingTools) {
				toolCounts["conflicting_rows_rejected"]++
			} else {
				toolCounts["invalid_rows_rejected"]++
			}
			continue
		}
		transcript.(map[string]any)["tools"] = cleanTools
		removed += extraRemoved
		toolCounts["identical_declarations_removed"] += removed
		if removed > 0 {
			toolCounts["rows_with_identical_duplicates"]++
		}
		canonical, _ = json.Marshal(transcript)
		hash := trainingHash(canonical)
		if seen[hash] {
			continue
		}
		seen[hash] = true
		if previous, ok := taskSplit[label.TaskHash]; ok && previous != split {
			return nil, errors.New("task crosses frozen split boundary")
		}
		taskSplit[label.TaskHash] = split
		clean := transcript.(map[string]any)
		if split != "test.jsonl" && !trainingChatValid(clean["messages"].([]any)) {
			continue
		}
		// PII filters must never rewrite provenance hashes or timestamps. Only
		// content fields (including the public checker fixture) are filtered.
		if label.ExpectedCalls == nil {
			label.ExpectedCalls = []map[string]any{}
		}
		fixtureBytes, _ := json.Marshal(label.ExpectedCalls)
		var fixture any
		_ = json.Unmarshal(fixtureBytes, &fixture)
		fixture = trainingPrivacy(fixture)
		fixtureBytes, _ = json.Marshal(fixture)
		_ = json.Unmarshal(fixtureBytes, &label.ExpectedCalls)
		initial := []any{}
		for _, message := range clean["messages"].([]any) {
			role := message.(map[string]any)["role"]
			if role == "assistant" || role == "tool" {
				break
			}
			initial = append(initial, message)
		}
		row := map[string]any{"messages": clean["messages"], "tools": clean["tools"], "provenance": map[string]any{"trace_hash": trainingHash([]byte(u.RequestID)), "task_hash": label.TaskHash, "trace_sha256": trainingHash([]byte(u.RequestID)), "task_sha256": label.TaskHash, "content_hash": hash, "captured_at": req.TS, "response_captured": responseCaptured, "teacher_transcript_incomplete": response["stop_reason"] == "tool_calls" || !responseCaptured, "teacher_provider": u.TargetProvider, "teacher_model": u.TargetModel}, "outcome": map[string]any{"version": label.Version, "task_hash": label.TaskHash, "task_success": label.TaskSuccess, "schema_valid": label.SchemaValid, "tool_schema_valid": label.SchemaValid, "quality": label.Quality}, "replay": map[string]any{"version": "router-replay-v1", "backend": label.Backend, "checker": "expected-tool-calls-v1", "seed": label.Seed, "initial_messages": initial, "tools": clean["tools"], "expected_calls": fixture, "expected_final": label.ExpectedFinal}}
		encoded, _ := json.Marshal(row)
		files[split].Write(encoded)
		files[split].WriteByte('\n')
		if files["train.jsonl"].Len()+files["val.jsonl"].Len()+files["test.jsonl"].Len() > 64<<20 {
			return nil, errors.New("export exceeds 64 MiB; narrow range")
		}
		counts[split]++
	}
	manifest := map[string]any{"format": "metrum-router-ft-export-v1", "chat_format": "router-chat-tools-v1", "audit_event_id": auditID, "teacher_terms_decision": f.TeacherTermsDecision, "task_source": f.TaskSource, "source": map[string]string{"kind": "router_capture"}, "filters": f, "privacy": map[string]any{"redaction_preserved": true, "pii_filter": "export-email-phone-v1", "headers_exported": false}, "files": map[string]any{}}
	manifest["tool_declarations"] = map[string]any{"policy": "first-identical-declaration-wins; reject ambiguous/invalid rows before hashing", "count_scope": "eligible candidate rows before content dedupe", "counts": toolCounts}
	manifest["outcome_filter_scope"] = "train,val only; heldout retains labeled teacher failures"
	manifest["split_boundaries"] = map[string]any{"from": f.From, "validation_from": f.ValidationFrom, "test_from": f.TestFrom, "to": f.To}
	manifest["audit_event_ref"] = map[string]any{"table": "security_access_events", "id": auditID}
	manifest["teacher_terms"] = map[string]any{"training_outputs_allowed": f.TeacherTermsDecision["training_outputs_allowed"] == "true", "decision": f.TeacherTermsDecision, "attestation": "authorized admin assertion, not independent legal verification"}
	privacy := manifest["privacy"].(map[string]any)
	privacy["capture_opt_in"] = true
	privacy["redaction_applied"] = true
	privacy["pii_filter_applied"] = true
	manifest["splits"] = map[string]any{}
	fileInfo := manifest["files"].(map[string]any)
	names := []string{"train.jsonl", "val.jsonl", "test.jsonl"}
	sort.Strings(names)
	for _, name := range names {
		fileInfo[name] = map[string]any{"sha256": trainingHash(files[name].Bytes()), "count": counts[name]}
		manifest["splits"].(map[string]any)[name[:len(name)-len(".jsonl")]] = fileInfo[name]
	}
	fileHashes, _ := json.Marshal(fileInfo)
	manifest["dataset_hash"] = trainingHash(fileHashes)
	manifest["export_id"] = trainingHash(append(fileHashes, []byte(formatUsageTime(f.To))...))
	m, _ := json.MarshalIndent(manifest, "", "  ")
	files["manifest.json"] = bytes.NewBuffer(m)
	names = append(names, "manifest.json")
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for _, name := range names {
		entry, err := z.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = entry.Write(files[name].Bytes()); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return archive.Bytes(), nil
}
