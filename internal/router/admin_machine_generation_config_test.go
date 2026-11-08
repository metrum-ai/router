// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/x509"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeMachineGroupedConfigListenerUnion(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, keyB, _ := certificateBindingCA(t)
	paths := []string{filepath.Join(t.TempDir(), "a.pem"), filepath.Join(t.TempDir(), "b.pem")}
	trustStoreWriteCA(t, paths[0], caA)
	trustStoreWriteCA(t, paths[1], caB)
	for _, mode := range []string{"valid", "duplicate-root", "mixed-flat"} {
		t.Run(mode, func(t *testing.T) {
			second := paths[1]
			if mode == "duplicate-root" {
				second = paths[0]
			}
			flat := ""
			if mode == "mixed-flat" {
				flat = fmt.Sprintf("  issuer: flat\n  ca_files: [%q]\n", paths[0])
			}
			text := fmt.Sprintf("mtls:\n%s  trust_domains:\n    - issuer: a\n      ca_files: [%q]\n    - issuer: b\n      ca_files: [%q]\n", flat, paths[0], second)
			var auth AdminAuthenticationConfig
			decoder := yaml.NewDecoder(strings.NewReader(text))
			decoder.KnownFields(true)
			if err := decoder.Decode(&auth); err != nil {
				t.Fatal("approved grouped schema rejected by actual admin authentication DTO", err)
			}
			cfg := AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:0", Authentication: auth}
			defaultAdminPlaneConfig(&cfg)
			cfg.TLS.ClientAuth = adminClientAuthOptional
			err := validateAdminPlane(&cfg)
			var plane *AdminPlane
			if err == nil {
				plane, err = NewAdminPlane(&cfg, http.NotFoundHandler())
			}
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid cross-domain anchors/mixed form accepted by listener startup")
				}
				return
			}
			if err != nil {
				t.Fatal("valid grouped listener config rejected", err)
			}
			roots := plane.ClientCASource().Snapshot()
			if roots == nil {
				t.Fatal("listener union source missing")
			}
			for _, leaf := range []*x509.Certificate{runtimeMTLSLeaf(t, caA, keyA), runtimeMTLSLeaf(t, caB, keyB)} {
				if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
					t.Fatal("listener handshake union omitted configured domain", err)
				}
			}
		})
	}
}
