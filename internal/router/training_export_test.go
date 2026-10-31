// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrainingExportCaptureConsentPrivacyAndFrozenSplits(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	encryption := testContentCaptureEncryption(t)
	s := &Service{usage: store, contentCaptureKeys: envContentCaptureKeyResolver{}}
	now := time.Now().UTC()
	f := trainingFilters{From: now.Add(-4 * time.Hour), ValidationFrom: now.Add(-2 * time.Hour), TestFrom: now.Add(-time.Hour), To: now.Add(time.Hour), Project: "acme-support", SuccessOnly: true, MinQuality: .9}
	for _, id := range []string{"captured", "uncaptured", "disabled"} {
		u := usageRecord{RequestID: id, TS: formatUsageTime(now.Add(-3 * time.Hour)), CallerProject: "acme-support", TokenID: "rtr_public", ResolvedGroup: "support-agent", TargetProvider: "teacher", TargetModel: "licensed-teacher"}
		if err := store.db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		if id != "captured" {
			continue
		}
		payloads := map[string]any{
			contentCaptureScopeRequest:  map[string]any{"chat_messages": []any{map[string]any{"role": "user", "content": "lookup person@example.com +1 212 555 0199 Bearer provider-token-1234567890"}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "call1", "content": "open"}}, "tools": []any{}},
			contentCaptureScopeResponse: map[string]any{"text": "done", "stop_reason": "stop"},
			"training_outcome_v1":       trainingOutcome{Version: "router-outcome-v1", TaskHash: strings.Repeat("a", 64), TaskSuccess: true, SchemaValid: true, Quality: 1, Backend: "acme-support-v1"},
		}
		for scope, payload := range payloads {
			b, _ := json.Marshal(payload)
			cipher, nonce, err := encryptContentCaptureValue(s.contentCaptureKeys, encryption.LocalKeyID, string(b))
			if err != nil {
				t.Fatal(err)
			}
			c := contentCaptureRecord{RequestID: id, Scope: scope, TS: u.TS, Encrypted: true, ContentText: cipher, EncryptionNonce: nonce, EncryptionLocalKeyID: encryption.LocalKeyID, RetentionUntil: formatUsageTime(now.Add(time.Hour))}
			if err := store.db.Create(&c).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	archive, err := s.buildTrainingArchive(f, 1)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		files[file.Name] = b
	}
	train := string(files["train.jsonl"])
	if strings.Count(train, "\n") != 1 || !strings.Contains(train, "call1") || strings.Contains(train, "person@example.com") || strings.Contains(train, "212 555") || strings.Contains(train, "uncaptured") || strings.Contains(train, "disabled") || strings.Contains(train, "provider-token-1234567890") {
		t.Fatalf("invalid export: %s", train)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(files["train.jsonl"]), &row); err != nil {
		t.Fatal(err)
	}
	if row["provenance"].(map[string]any)["teacher_model"] != "licensed-teacher" {
		t.Fatal("teacher provenance must come from resolved usage, not pre-route request capture")
	}
	if row["provenance"].(map[string]any)["task_hash"] != strings.Repeat("a", 64) {
		t.Fatal("privacy filtering corrupted provenance hash")
	}
	if row["provenance"].(map[string]any)["captured_at"] != formatUsageTime(now.Add(-3*time.Hour)) {
		t.Fatal("privacy filtering corrupted captured timestamp")
	}
	if len(files["val.jsonl"]) != 0 || len(files["test.jsonl"]) != 0 {
		t.Fatal("wrong time split")
	}
	var manifest map[string]any
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	info := manifest["files"].(map[string]any)["train.jsonl"].(map[string]any)
	if info["sha256"] != trainingHash(files["train.jsonl"]) {
		t.Fatal("invalid manifest hash")
	}
	// Every identity selector is conjunctive and cannot broaden consent.
	for field, value := range map[string]string{"token": "other", "project": "other", "client": "other", "group": "other"} {
		filtered := f
		switch field {
		case "token":
			filtered.TokenID = value
		case "project":
			filtered.Project = value
		case "client":
			filtered.Client = value
		case "group":
			filtered.Group = value
		}
		data, err := s.buildTrainingArchive(filtered, 3)
		if err != nil {
			t.Fatal(err)
		}
		z, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		for _, file := range z.File {
			if file.Name == "train.jsonl" && file.UncompressedSize64 != 0 {
				t.Fatalf("%s filter ignored", field)
			}
		}
	}
	// A distinct continuation of the same task may not leak across frozen splits.
	second := usageRecord{RequestID: "second", TS: formatUsageTime(now.Add(-30 * time.Minute)), CallerProject: "acme-support", ResolvedGroup: "support-agent"}
	if err := store.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	var original []contentCaptureRecord
	if err := store.db.Where("request_id = ?", "captured").Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range original {
		c.ID = 0
		c.RequestID = "second"
		if c.Scope == contentCaptureScopeResponse {
			cipher, nonce, err := encryptContentCaptureValue(s.contentCaptureKeys, encryption.LocalKeyID, `{"text":"different final answer","stop_reason":"stop"}`)
			if err != nil {
				t.Fatal(err)
			}
			c.ContentText = cipher
			c.EncryptionNonce = nonce
		}
		if err := store.db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.buildTrainingArchive(f, 4); err == nil {
		t.Fatal("same task crossed frozen split")
	}
	if err := store.db.Where("request_id = ?", "second").Delete(&contentCaptureRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	// Outcome selection is training-only: never remove labeled teacher failures
	// from the frozen heldout population and improve evaluation by selection.
	var labelRecord contentCaptureRecord
	if err := store.db.Where("request_id = ? AND scope = ?", "captured", "training_outcome_v1").First(&labelRecord).Error; err != nil {
		t.Fatal(err)
	}
	failed, _ := json.Marshal(trainingOutcome{Version: "router-outcome-v1", TaskHash: strings.Repeat("a", 64), TaskSuccess: false, SchemaValid: false, Quality: 0, Backend: "acme-support-v1"})
	cipher, nonce, err := encryptContentCaptureValue(s.contentCaptureKeys, encryption.LocalKeyID, string(failed))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&labelRecord).Updates(map[string]any{"content_text": cipher, "encryption_nonce": nonce}).Error; err != nil {
		t.Fatal(err)
	}
	var emptyResponse contentCaptureRecord
	if err := store.db.Where("request_id = ? AND scope = ?", "captured", contentCaptureScopeResponse).First(&emptyResponse).Error; err != nil {
		t.Fatal(err)
	}
	emptyCipher, emptyNonce, err := encryptContentCaptureValue(s.contentCaptureKeys, encryption.LocalKeyID, `{"text":"","stop_reason":"length"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&emptyResponse).Updates(map[string]any{"content_text": emptyCipher, "encryption_nonce": emptyNonce}).Error; err != nil {
		t.Fatal(err)
	}
	heldout := f
	heldout.ValidationFrom = now.Add(-210 * time.Minute)
	heldout.TestFrom = now.Add(-195 * time.Minute)
	data, err := s.buildTrainingArchive(heldout, 5)
	if err != nil {
		t.Fatal(err)
	}
	z, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	foundFailure := false
	for _, file := range z.File {
		if file.Name == "test.jsonl" {
			r, _ := file.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			foundFailure = strings.Contains(string(b), `"task_success":false`)
		}
	}
	if !foundFailure {
		t.Fatal("success-only filter removed heldout teacher failure")
	}
	// Upstream HTTP failures still have an opted-in request but no response.
	// Preserve that heldout fixture with explicit missing-response provenance.
	if err := store.db.Where("request_id = ? AND scope = ?", "captured", contentCaptureScopeResponse).Delete(&contentCaptureRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	missing, err := s.buildTrainingArchive(heldout, 6)
	if err != nil {
		t.Fatal(err)
	}
	mz, _ := zip.NewReader(bytes.NewReader(missing), int64(len(missing)))
	missingKept := false
	for _, file := range mz.File {
		if file.Name == "test.jsonl" {
			r, _ := file.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			missingKept = strings.Contains(string(b), `"response_captured":false`)
		}
	}
	if !missingKept {
		t.Fatal("heldout upstream failure was dropped")
	}
	// Deleting captures must remove exportability, even though usage remains.
	if err := store.db.Where("request_id = ?", "captured").Delete(&contentCaptureRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	archive, err = s.buildTrainingArchive(f, 2)
	if err != nil {
		t.Fatal(err)
	}
	reader, _ = zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	for _, file := range reader.File {
		if file.Name == "train.jsonl" && file.UncompressedSize64 != 0 {
			t.Fatal("exported uncaptured usage")
		}
	}
}
