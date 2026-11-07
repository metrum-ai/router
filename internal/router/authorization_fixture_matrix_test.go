// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The oracle below freezes the v4.7 distinction between permission helpers,
// object/action helpers, policy-only handlers and Basic-only auth discovery.
// Provider credentials are never resolved: only the authorization projection
// is decoded, without network access or reading real credential environments.
func TestV47AuthorizationFixtureMatrix(t *testing.T) {
	type row struct{ Fixture, Credential, Domain, Mode, Decisions string }
	permissions := make([]string, 0, len(knownAdminKeyPermissions))
	for permission := range knownAdminKeyPermissions {
		permissions = append(permissions, permission)
	}
	sort.Strings(permissions)
	var rows []row
	count := 0
	err := filepath.WalkDir("../..", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The edge installer template uses @@TOKEN@@ placeholders. Render
		// deterministic non-secret fixture strings before YAML parsing; the
		// authorization projection contains no numeric installer fields.
		if strings.HasSuffix(path, "deploy/edge/config.template.yaml") {
			raw = regexp.MustCompile(`@@[A-Z_]+@@`).ReplaceAll(raw, []byte("fixture"))
		}
		// Resolve identity/domain placeholders deterministically rather than
		// freezing unexpanded ${...} strings or ambient secret values.
		raw = []byte(os.Expand(string(raw), func(name string) string {
			return "fixture-" + strings.ToLower(strings.ReplaceAll(name, "_", "-"))
		}))
		var root map[string]yaml.Node
		if err := yaml.Unmarshal(raw, &root); err != nil {
			t.Errorf("classify %s: %v", path, err)
			return nil
		}
		if _, server := root["server"]; !server {
			// Kubernetes embeds a router configuration under ConfigMap.data.
			if data, ok := root["data"]; ok {
				var embedded map[string]string
				if err := data.Decode(&embedded); err == nil {
					if value, ok := embedded["config.yaml"]; ok {
						raw = []byte(value)
						if err := yaml.Unmarshal(raw, &root); err != nil {
							t.Errorf("embedded %s: %v", path, err)
							return nil
						}
					}
				}
			}
		}
		_, server := root["server"]
		_, callers := root["callers"]
		_, models := root["models"]
		if !server && !callers && !models {
			return nil
		} // workflows, promptfoo, deployment manifests
		var cfg struct {
			Server struct {
				AdminAuth AdminAuthConfig `yaml:"admin_auth"`
			} `yaml:"server"`
			Callers []struct {
				ID           string `yaml:"id"`
				Project      string `yaml:"project"`
				Environment  string `yaml:"environment"`
				MetricsAdmin bool   `yaml:"metrics_admin"`
				ContentAdmin bool   `yaml:"content_admin"`
			} `yaml:"callers"`
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Errorf("load authorization projection %s: %v", path, err)
			return nil
		}
		count++
		auth := cfg.Server.AdminAuth
		runtimes := map[string]*callerRuntime{}
		for _, caller := range cfg.Callers {
			runtimes[caller.ID] = &callerRuntime{cfg: CallerConfig{ID: caller.ID, Project: caller.Project, Environment: caller.Environment, MetricsAdmin: caller.MetricsAdmin, ContentAdmin: caller.ContentAdmin}}
		}
		old, err := v47FixtureAuthorizer(auth.Authorization, runtimes)
		if err != nil {
			t.Errorf("legacy fixture %s: %v", path, err)
			return nil
		}
		compiled, err := newAuthorizer(auth.Authorization, runtimes, nil, auth)
		if err != nil {
			t.Errorf("compiled fixture %s: %v", path, err)
			return nil
		}
		svc := &Service{authorizer: compiled}
		subjects := []adminAuthSubject{{subject: "unknown", domain: "foreign/prod", source: "oauth"}}
		for _, key := range auth.APIKeys {
			if key.Status == "disabled" {
				continue
			}
			subject := key.Subject
			if subject == "" {
				subject = "adminkey:" + key.KeyID
			}
			permissions := map[string]bool{}
			for _, permission := range key.Permissions {
				permissions[permission] = true
			}
			subjects = append(subjects, adminAuthSubject{subject: subject, domain: key.Domain, source: "admin_key", credentialID: key.KeyID, permissions: permissions})
		}
		if auth.Basic.Enabled {
			for _, user := range auth.Basic.Users {
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
		}
		for _, caller := range cfg.Callers {
			subjects = append(subjects, adminAuthSubject{subject: "caller:" + caller.ID, domain: authzDomainForCaller(runtimes[caller.ID]), source: "caller_token"})
		}
		for _, subject := range subjects {
			// Cross-domain policy-only matrix covers all admin objects/actions,
			// including the training object that no legacy key map matched.
			for _, domain := range []string{subject.domain, "*", "foreign/prod"} {
				var bits strings.Builder
				for _, object := range []string{"metrics", "content:capture", "content:training", "admin:training", "admin:config", "admin:shadow", "admin:auth", "admin:callers", "admin:reports", "admin:security_reports", "admin:policy", "admin:keys", "unknown"} {
					for _, action := range []string{"read", "write", "activate", "force", "list", "issue", "rotate", "revoke", "export", "drilldown", "delete", "purge", "label", "xreadx", "unknown"} {
						identity := authzSubjectForAdmin(subject)
						identity.domain = domain
						want := old.enforce(identity, object, action)
						if got := compiled.enforce(identity, object, action); got != want {
							t.Errorf("%s %s domain=%s %s/%s: got %v legacy %v", path, subject.subject, domain, object, action, got, want)
						}
						if want {
							bits.WriteByte('1')
						} else {
							bits.WriteByte('0')
						}
					}
				}
				rows = append(rows, row{filepath.ToSlash(path), subject.source + ":" + subject.credentialID + ":" + subject.subject, domain, "cross-domain-policy", bits.String()})
			}
			for _, mode := range []string{"permission", "object", "public", "global", "basic-only"} {
				var bits strings.Builder
				for _, permission := range permissions {
					object, action, _ := adminPermissionObjectAction(permission)
					public := authzSubjectForAdmin(subject)
					want := old.enforce(public, object, action)
					var got bool
					switch mode {
					case "permission":
						want = want || subject.permissions[permission]
						got = svc.authorizeAdminPermission(subject, permission)
					case "object":
						want = want || subject.permissions[object+":"+action]
						got = svc.authorizeAdminObjectAction(subject, object, action)
					case "public":
						got = svc.authorizeAdmin(subject, object, action)
					case "global":
						public.domain = "*"
						want = old.enforce(public, object, action)
						got = svc.authorizeGlobalAdmin(subject, object, action)
					case "basic-only":
						want = subject.permissions[permission]
						got = svc.authorizeCompatibility(subject, object, action, "direct")
						if subject.source != "basic" {
							continue
						}
					}
					if got != want {
						t.Errorf("%s %s/%s %s %s: got %v legacy %v", path, subject.source, subject.credentialID, mode, permission, got, want)
					}
					if want {
						bits.WriteByte('1')
					} else {
						bits.WriteByte('0')
					}
				}
				rows = append(rows, row{filepath.ToSlash(path), subject.source + ":" + subject.credentialID + ":" + subject.subject, subject.domain, mode, bits.String()})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no router configuration fixtures discovered")
	}
	encoded, err := json.MarshalIndent(struct {
		Permissions []string
		Rows        []row
	}{permissions, rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := append([]byte("# Copyright 2026 Metrum AI\n# SPDX-License-Identifier: Apache-2.0\n"), encoded...)
	golden = append(golden, '\n')
	path := "testdata/authz-v47-matrix.golden"
	if os.Getenv("UPDATE_AUTHZ_V47_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, golden, 0644); err != nil {
			t.Fatal(err)
		}
	}
	frozen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(frozen) != string(golden) {
		t.Fatal("v4.7 authorization matrix differs from frozen golden; report differences, do not silently regenerate")
	}
	t.Logf("loaded %d router configuration fixtures; %d matrix rows", count, len(rows))
}
