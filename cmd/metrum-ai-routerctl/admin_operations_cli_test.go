// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestControlPlaneRouterctlBackupAndRestore(t *testing.T) {
	bundle := map[string]any{"schema": "metrum.ai/router-control-plane-backup/v1", "created_at": "2026-11-16T00:00:00Z", "config": map[string]any{"tables": []any{}}, "usage": map[string]any{"tables": []any{}}, "sha256": ""}
	canonical, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	bundle["sha256"] = hex.EncodeToString(digest[:])
	backup, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "control-plane.json")
	var restored bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /admin/v1/control-plane/backups":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-1"},"operation_url":"/admin/v1/operations/op-1"}`))
		case "POST /admin/v1/control-plane/restores":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["sha256"] != bundle["sha256"] {
				t.Errorf("restore payload was not the submitted bundle: err=%v", err)
			}
			restored = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-2"},"operation_url":"/admin/v1/operations/op-2"}`))
		case "GET /admin/v1/operations/op-1", "GET /admin/v1/operations/op-2":
			_, _ = w.Write([]byte(`{"id":"op","kind":"control-plane-backup","status":"succeeded"}`))
		case "GET /admin/v1/control-plane/backups/op-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(append(append([]byte(`{"bundle":`), backup...), '}'))
		default:
			t.Errorf("unexpected routerctl request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_cli_test_key_material")

	controlPlaneCommand([]string{"backup", "--out", backupPath, "--router-url", server.URL})
	written, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(backup) {
		t.Fatal("routerctl changed the backup bundle")
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file permissions=%#o, want 0600", info.Mode().Perm())
	}
	controlPlaneCommand([]string{"restore", "--in", backupPath, "--router-url", server.URL})
	if !restored {
		t.Fatal("routerctl did not submit the backup bundle to the restore API")
	}
}
