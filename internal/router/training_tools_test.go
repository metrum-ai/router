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

func TestTrainingToolDeclarationArchiveCounts(t *testing.T) {
	for _, tc := range []struct {
		name, tools                       string
		rows, removed, conflicts, invalid int
	}{
		{"identical", `[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}},{"function":{"parameters":{"type":"object"},"name":"lookup"},"type":"function"}]`, 1, 1, 0, 0},
		{"conflicting", `[{"type":"function","function":{"name":"lookup","description":"one"}},{"type":"function","function":{"name":"lookup","description":"two"}}]`, 0, 0, 1, 0},
		{"invalid", `[{}]`, 0, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			encryption := testContentCaptureEncryption(t)
			resolver := envContentCaptureKeyResolver{}
			now := time.Now().UTC()
			ts := formatUsageTime(now.Add(-3 * time.Hour))
			if err := store.db.Create(&usageRecord{RequestID: "trace", TS: ts}).Error; err != nil {
				t.Fatal(err)
			}
			payloads := map[string]string{contentCaptureScopeRequest: `{"chat_messages":[{"role":"user","content":"lookup"}],"tools":` + tc.tools + `}`, contentCaptureScopeResponse: `{"text":"done","stop_reason":"stop"}`, "training_outcome_v1": `{"version":"router-outcome-v1","task_hash":"` + strings.Repeat("a", 64) + `","task_success":true,"schema_valid":true,"quality":1,"backend":"acme-support-v1"}`}
			for scope, payload := range payloads {
				cipher, nonce, err := encryptContentCaptureValue(resolver, encryption.LocalKeyID, payload)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.db.Create(&contentCaptureRecord{RequestID: "trace", Scope: scope, TS: ts, ContentText: cipher, Encrypted: true, EncryptionNonce: nonce, EncryptionLocalKeyID: encryption.LocalKeyID, RetentionUntil: formatUsageTime(now.Add(time.Hour))}).Error; err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{usage: store, contentCaptureKeys: resolver}
			data, err := s.buildTrainingArchive(trainingFilters{From: now.Add(-4 * time.Hour), ValidationFrom: now.Add(-2 * time.Hour), TestFrom: now.Add(-time.Hour), To: now.Add(time.Hour)}, 1)
			if err != nil {
				t.Fatal(err)
			}
			z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range z.File {
				r, _ := file.Open()
				b, _ := io.ReadAll(r)
				r.Close()
				if file.Name == "train.jsonl" {
					if bytes.Count(b, []byte("\n")) != tc.rows {
						t.Fatalf("rows: %s", b)
					}
					if tc.rows > 0 {
						var row map[string]any
						json.Unmarshal(bytes.TrimSpace(b), &row)
						if len(row["tools"].([]any)) != 1 || len(row["replay"].(map[string]any)["tools"].([]any)) != 1 {
							t.Fatal("duplicate declarations leaked")
						}
					}
				}
				if file.Name == "manifest.json" {
					var m map[string]any
					json.Unmarshal(b, &m)
					c := m["tool_declarations"].(map[string]any)["counts"].(map[string]any)
					for key, want := range map[string]int{"identical_declarations_removed": tc.removed, "conflicting_rows_rejected": tc.conflicts, "invalid_rows_rejected": tc.invalid} {
						if c[key] != float64(want) {
							t.Fatalf("%s: %v", key, c)
						}
					}
				}
			}
		})
	}
}

func TestTrainingToolsStableFirstOrder(t *testing.T) {
	var tools any
	json.Unmarshal([]byte(`[{"type":"function","function":{"name":"z"}},{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"z"}}]`), &tools)
	got, n, err := trainingTools(tools)
	if err != nil || n != 1 || len(got) != 2 {
		t.Fatalf("%v %d %v", got, n, err)
	}
	if got[0].(map[string]any)["function"].(map[string]any)["name"] != "z" {
		t.Fatal("first order changed")
	}
}
