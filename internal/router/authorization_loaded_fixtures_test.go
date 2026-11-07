// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV47AuthorizationLoadedReferenceConfigs(t *testing.T) {
	// All resolver values are local fixtures. Never expand a credential from
	// the ambient agent environment into a test file or diagnostic.
	t.Setenv("OPENAI_API_KEY", "fixture-provider-key")
	t.Setenv("LOCAL_VLLM_API_KEY", "fixture-local-key")
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	hash := mustBcryptHash(t, "fixture-only-password")
	t.Setenv("LOCAL_PI_ADMIN_PASSWORD_HASH", hash)
	t.Setenv("REFERENCE_ADMIN_PASSWORD_HASH", hash)
	for _, name := range []string{"config.example.yaml", "config.minimal.example.yaml", "deploy/reference/amd-mi350p-datacenter/config.yaml", "deploy/reference/amd-strix-halo-edge/config.yaml", "deploy/reference/nvidia-datacenter/config.yaml", "deploy/reference/nvidia-gb10-edge/config.yaml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../..", name))
			if err != nil {
				t.Fatal(err)
			}
			for _, env := range envReferenceNames(raw) {
				if env != "ROUTER_ID_TRANSFORM_KEY" {
					t.Setenv(env, "fixture-provider-key")
				}
			}
			text := string(raw)
			if name == "config.example.yaml" {
				text = replaceExampleCallerTokenHashes(t, text)
				text = strings.ReplaceAll(text, "script: scripts/router.ts", "script: ../../scripts/router.ts")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			callers := map[string]*callerRuntime{}
			for _, caller := range cfg.Callers {
				callers[caller.ID] = &callerRuntime{cfg: caller}
			}
			old, err := v47FixtureAuthorizer(cfg.Server.AdminAuth.Authorization, callers)
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, callers, nil, cfg.Server.AdminAuth)
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{authorizer: compiled}
			subjects := []adminAuthSubject{{subject: "unknown", domain: "foreign/prod", source: "oauth"}}
			for _, user := range cfg.Server.AdminAuth.Basic.Users {
				subject := user.Subject
				if subject == "" {
					subject = "basic:" + user.Username
				}
				permissions := map[string]bool{}
				for _, permission := range user.Permissions {
					permissions[permission] = true
				}
				subjects = append(subjects, adminAuthSubject{subject: subject, domain: user.Domain, source: "basic", credentialID: user.Username, permissions: permissions})
			}
			for _, caller := range cfg.Callers {
				subjects = append(subjects, adminAuthSubject{subject: "caller:" + caller.ID, domain: authzDomainForCaller(callers[caller.ID]), source: "caller_token"})
			}
			for _, subject := range subjects {
				for permission := range knownAdminKeyPermissions {
					object, action, _ := adminPermissionObjectAction(permission)
					want := subject.permissions[permission] || old.enforce(authzSubjectForAdmin(subject), object, action)
					if got := svc.authorizeAdminPermission(subject, permission); got != want {
						t.Errorf("%s %s: got %v legacy %v", subject.subject, permission, got, want)
					}
				}
				for _, domain := range []string{subject.domain, "foreign/prod", "*"} {
					for _, object := range []string{"metrics", "content:capture", "content:training", "admin:reports", "admin:security_reports"} {
						for _, action := range []string{"read", "export", "delete", "purge", "label", "xreadx"} {
							identity := authzSubjectForAdmin(subject)
							identity.domain = domain
							if got, want := compiled.enforce(identity, object, action), old.enforce(identity, object, action); got != want {
								t.Errorf("%s %s %s/%s: got %v legacy %v", subject.subject, domain, object, action, got, want)
							}
						}
					}
				}
			}
		})
	}
}
