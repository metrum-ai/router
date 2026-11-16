// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package releaseverify

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyAcceptsSignedLinuxReleaseBinary(t *testing.T) {
	binary := []byte("router executable bytes")
	archive := makeTarGzip(t, "metrum-ai-router-v1.2.3-linux-amd64/bin/metrum-ai-router", binary)
	server := assetServer(t, map[string][]byte{
		"metrum-ai-router-v1.2.3-linux-amd64.tar.gz":               archive,
		"metrum-ai-router-v1.2.3-linux-amd64.tar.gz.sigstore.json": []byte("bundle"),
	})
	local := writeBinary(t, binary)
	var commands [][]string
	result, err := Verify(context.Background(), Options{
		Release: "v1.2.3", Binary: local, GOOS: "linux", GOARCH: "amd64", BaseURL: server.URL,
		RunCommand: func(_ context.Context, name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(binary)
	if !result.Verified || result.BinarySHA256 != hex.EncodeToString(want[:]) || result.Platform != "linux/amd64" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(commands) != 2 || commands[0][0] != "cosign" || commands[1][0] != "gh" {
		t.Fatalf("signature and provenance verification commands = %#v", commands)
	}
	if !strings.Contains(strings.Join(commands[0], " "), "refs/tags/v1.2.3") {
		t.Fatalf("cosign identity did not bind the tag: %#v", commands[0])
	}
	wantAttestation := []string{
		"gh", "attestation", "verify", "metrum-ai-router-v1.2.3-linux-amd64.tar.gz",
		"--repo", "metrum-ai/router",
		"--signer-workflow", "github.com/metrum-ai/router/.github/workflows/release.yml",
		"--source-ref", "refs/tags/v1.2.3",
	}
	if len(commands[1]) != len(wantAttestation) || strings.Join(commands[1][:3], " ") != "gh attestation verify" || strings.Join(commands[1][4:], " ") != strings.Join(wantAttestation[4:], " ") {
		t.Fatalf("GitHub provenance command did not pin signer workflow and tag: %#v", commands[1])
	}
}

func TestAttestationPolicyRejectsDifferentWorkflowOrTag(t *testing.T) {
	args := attestationVerificationArgs("artifact.tar.gz", "metrum-ai/router", "v1.2.3")
	contains := func(flag, value string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag && args[i+1] == value {
				return true
			}
		}
		return false
	}
	if !contains("--signer-workflow", "github.com/metrum-ai/router/.github/workflows/release.yml") || !contains("--source-ref", "refs/tags/v1.2.3") {
		t.Fatalf("expected workflow/tag policy missing from %#v", args)
	}
	if contains("--signer-workflow", "github.com/metrum-ai/router/.github/workflows/other.yml") || contains("--source-ref", "refs/tags/v1.2.4") {
		t.Fatal("verification policy accepts a different workflow or tag")
	}
}

func TestVerifyRejectsModifiedLocalBinary(t *testing.T) {
	archive := makeTarGzip(t, "metrum-ai-router-v1.2.3-linux-amd64/bin/metrum-ai-router", []byte("released"))
	server := assetServer(t, map[string][]byte{
		"metrum-ai-router-v1.2.3-linux-amd64.tar.gz":               archive,
		"metrum-ai-router-v1.2.3-linux-amd64.tar.gz.sigstore.json": []byte("bundle"),
	})
	_, err := Verify(context.Background(), Options{
		Release: "v1.2.3", Binary: writeBinary(t, []byte("modified")), GOOS: "linux", GOARCH: "amd64", BaseURL: server.URL,
		RunCommand: func(context.Context, string, ...string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("modified executable error = %v", err)
	}
}

func TestVerifyFailsClosedForMissingAssetsAndInvalidSignatures(t *testing.T) {
	t.Run("missing bundle", func(t *testing.T) {
		archive := makeTarGzip(t, "metrum-ai-router-v1.2.3-linux-amd64/bin/metrum-ai-router", []byte("released"))
		server := assetServer(t, map[string][]byte{"metrum-ai-router-v1.2.3-linux-amd64.tar.gz": archive})
		_, err := Verify(context.Background(), Options{
			Release: "v1.2.3", Binary: writeBinary(t, []byte("released")), GOOS: "linux", GOARCH: "amd64", BaseURL: server.URL,
			RunCommand: func(context.Context, string, ...string) error { return nil },
		})
		if err == nil || !strings.Contains(err.Error(), "signature bundle") {
			t.Fatalf("missing bundle error = %v", err)
		}
	})
	t.Run("bad signature", func(t *testing.T) {
		archive := makeTarGzip(t, "metrum-ai-router-v1.2.3-linux-amd64/bin/metrum-ai-router", []byte("released"))
		server := assetServer(t, map[string][]byte{
			"metrum-ai-router-v1.2.3-linux-amd64.tar.gz":               archive,
			"metrum-ai-router-v1.2.3-linux-amd64.tar.gz.sigstore.json": []byte("bundle"),
		})
		_, err := Verify(context.Background(), Options{
			Release: "v1.2.3", Binary: writeBinary(t, []byte("released")), GOOS: "linux", GOARCH: "amd64", BaseURL: server.URL,
			RunCommand: func(_ context.Context, name string, _ ...string) error {
				if name == "cosign" {
					return fmt.Errorf("signature rejected")
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "verify Cosign signature") {
			t.Fatalf("bad signature error = %v", err)
		}
	})
}

func TestVerifyTimeoutAndUnsupportedInputsFail(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		archive := makeTarGzip(t, "metrum-ai-router-v1.2.3-linux-amd64/bin/metrum-ai-router", []byte("released"))
		server := assetServer(t, map[string][]byte{
			"metrum-ai-router-v1.2.3-linux-amd64.tar.gz":               archive,
			"metrum-ai-router-v1.2.3-linux-amd64.tar.gz.sigstore.json": []byte("bundle"),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err := Verify(ctx, Options{
			Release: "v1.2.3", Binary: writeBinary(t, []byte("released")), GOOS: "linux", GOARCH: "amd64", BaseURL: server.URL,
			RunCommand: func(ctx context.Context, _ string, _ ...string) error {
				<-ctx.Done()
				return ctx.Err()
			},
		})
		if err == nil {
			t.Fatal("verification succeeded after caller timeout")
		}
	})
	t.Run("invalid tag", func(t *testing.T) {
		_, err := Verify(context.Background(), Options{Release: "../../main"})
		if err == nil {
			t.Fatal("accepted unsafe release tag")
		}
	})
	t.Run("unsupported platform", func(t *testing.T) {
		_, err := Verify(context.Background(), Options{Release: "v1.2.3", GOOS: "darwin", GOARCH: "arm64"})
		if err == nil || !strings.Contains(err.Error(), "unsupported release platform") {
			t.Fatalf("unsupported platform error = %v", err)
		}
	})
}

func TestVerifyAcceptsWindowsZipBinary(t *testing.T) {
	binary := []byte("windows router executable")
	archive := makeZip(t, "metrum-ai-router-v1.2.3-windows-arm64/bin/metrum-ai-router.exe", binary)
	server := assetServer(t, map[string][]byte{
		"metrum-ai-router-v1.2.3-windows-arm64.zip":               archive,
		"metrum-ai-router-v1.2.3-windows-arm64.zip.sigstore.json": []byte("bundle"),
	})
	result, err := Verify(context.Background(), Options{
		Release: "v1.2.3", Binary: writeBinary(t, binary), GOOS: "windows", GOARCH: "arm64", BaseURL: server.URL,
		RunCommand: func(context.Context, string, ...string) error { return nil },
	})
	if err != nil || !result.Verified || result.Platform != "windows/arm64" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func assetServer(t *testing.T, assets map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		content, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	}))
}

func writeBinary(t *testing.T, content []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "router")
	if err := os.WriteFile(name, content, 0o700); err != nil {
		t.Fatal(err)
	}
	return name
}

func makeTarGzip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	file, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
