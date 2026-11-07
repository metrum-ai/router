// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretRefResolveEnv(t *testing.T) {
	t.Setenv("TLS_TEST_SECRET", "resolved-value")
	value, err := SecretRef{Env: "TLS_TEST_SECRET"}.resolve("serving.tls.dns.credentials.api_token")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if value != "resolved-value" {
		t.Fatalf("value = %q", value)
	}
}

func TestSecretRefResolveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	value, err := SecretRef{File: path}.resolve("field")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if value != "file-secret" {
		t.Fatalf("value = %q (trailing newline must be trimmed)", value)
	}
}

func TestSecretRefErrorsNameTheFieldNotTheValue(t *testing.T) {
	_, err := SecretRef{Env: "TLS_TEST_UNSET_SECRET"}.resolve("serving.tls.dns.credentials.token")
	if err == nil || !strings.Contains(err.Error(), "TLS_TEST_UNSET_SECRET") {
		t.Fatalf("error = %v", err)
	}

	t.Setenv("TLS_TEST_EMPTY_SECRET", "  ")
	if _, err := (SecretRef{Env: "TLS_TEST_EMPTY_SECRET"}).resolve("field"); err == nil {
		t.Fatal("an empty environment variable must be an error")
	}

	if _, err := (SecretRef{File: filepath.Join(t.TempDir(), "missing")}).resolve("field"); err == nil {
		t.Fatal("a missing secret file must be an error")
	}
}

func TestSecretRefValidate(t *testing.T) {
	if err := (SecretRef{Env: "A", File: "b"}).validate("x.api_token"); err == nil {
		t.Fatal("both env and file must be rejected")
	}
	if err := (SecretRef{Env: "not-a-name"}).validate("x.api_token"); err == nil {
		t.Fatal("an invalid environment name must be rejected")
	}
	if err := (SecretRef{Env: "GOOD_NAME"}).validate("x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (SecretRef{}).validate("x"); err != nil {
		t.Fatalf("an unset reference is valid: %v", err)
	}
}

func TestSecretSetRedactsResolvedValues(t *testing.T) {
	t.Setenv("TLS_TEST_TOKEN", "tok-123456")
	secrets := &secretSet{}
	if _, err := secrets.resolve(SecretRef{Env: "TLS_TEST_TOKEN"}, "serving.tls.dns.credentials.api_token"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	text := secrets.redact("provider rejected tok-123456 for zone example.com")
	if strings.Contains(text, "tok-123456") {
		t.Fatalf("credential leaked: %q", text)
	}
	if !strings.Contains(text, "<redacted:serving.tls.dns.credentials.api_token>") {
		t.Fatalf("redaction label missing: %q", text)
	}
}
