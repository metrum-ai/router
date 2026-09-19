// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

func TestLoadEnvJSONSetsMissingValuesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.json")
	if err := os.WriteFile(path, []byte(`{"ROUTER_TEST_ENV":"from_file","ROUTER_KEEP_ENV":"from_file"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_TEST_ENV", "")
	t.Setenv("ROUTER_KEEP_ENV", "existing")
	os.Unsetenv("ROUTER_TEST_ENV")
	if err := loadEnvJSON(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ROUTER_TEST_ENV"); got != "from_file" {
		t.Fatalf("ROUTER_TEST_ENV=%q", got)
	}
	if got := os.Getenv("ROUTER_KEEP_ENV"); got != "existing" {
		t.Fatalf("ROUTER_KEEP_ENV overwritten: %q", got)
	}
}

func TestUsageDBMigrationPolicyValidation(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Server.UsageDB.MigrationPolicy = "not-a-policy"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "usage_db migration_policy") {
		t.Fatalf("invalid usage migration policy error = %v", err)
	}
	cfg.Server.UsageDB.MigrationPolicy = ""
	cfg.setDefaults()
	if cfg.Server.UsageDB.MigrationPolicy != usageDBMigrationPolicyDeploymentJob {
		t.Fatalf("migration policy default = %q", cfg.Server.UsageDB.MigrationPolicy)
	}
}

func TestAdminBasicAuthRealmDefaultsToCanonicalProductName(t *testing.T) {
	cfg := &Config{}
	cfg.setDefaults()
	if got := cfg.Server.AdminAuth.Basic.Realm; got != "Metrum AI Router Admin" {
		t.Fatalf("admin Basic Auth realm = %q", got)
	}
}

func TestUsageDBDefaultsToSQLiteBesideStatePath(t *testing.T) {
	cfg := &Config{StatePath: "/var/lib/smart-llmrouter/router-state.json"}
	cfg.setDefaults()
	if cfg.Server.UsageDB.Driver != "sqlite" {
		t.Fatalf("usage DB driver = %q, want sqlite", cfg.Server.UsageDB.Driver)
	}
	if cfg.Server.UsageDB.Path != "/var/lib/smart-llmrouter/usage.sqlite" {
		t.Fatalf("usage DB path = %q", cfg.Server.UsageDB.Path)
	}
	if cfg.Server.UsageDB.MigrationPolicy != usageDBMigrationPolicyDeploymentJob {
		t.Fatalf("usage DB migration policy = %q", cfg.Server.UsageDB.MigrationPolicy)
	}

	postgres := &Config{Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"}, UsageDB: UsageDBConfig{
		Driver: "postgres",
		DSN:    "postgres://test-only",
	}}}
	postgres.setDefaults()
	if postgres.Server.UsageDB.Path != "" {
		t.Fatalf("Postgres usage DB path = %q, want empty", postgres.Server.UsageDB.Path)
	}
}

func TestEnvExampleContainsOnlySafePlaceholders(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "env.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("env.example.json must remain valid JSON: %v", err)
	}

	liveSecretRe := regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{16,}|sk-or-v1-[A-Za-z0-9_-]{16,}|xai-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9_]{16,}|[A-Za-z0-9_-]{32,})`)
	for name, value := range values {
		if strings.HasPrefix(name, "STRIPE_") || strings.HasPrefix(name, "RESTIC_") || strings.HasPrefix(name, "BACKUP_") || strings.HasPrefix(name, "COMMERCE_") {
			t.Fatalf("env.example.json must remain instance-only; move %s to ops.env.example.json (or keep out of instance env)", name)
		}
		if strings.HasSuffix(name, "_API_KEY") && value != "" {
			t.Fatalf("env.example.json %s must be an empty placeholder", name)
		}
		if liveSecretRe.MatchString(value) {
			t.Fatalf("env.example.json %s contains a live-looking secret value", name)
		}
	}

	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	ignoreText := "\n" + string(gitignore) + "\n"
	if !strings.Contains(ignoreText, "\nenv.json\n") {
		t.Fatal(".gitignore must keep real env.json out of source control")
	}
	if !strings.Contains(ignoreText, "\nops.env.json\n") {
		t.Fatal(".gitignore must keep real ops.env.json out of source control")
	}
}

func TestProviderModelRefsResolveAndOverride(t *testing.T) {
	cfg := minimalConfig(t)
	provider := cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{
		"small": {
			Model:                              "mock-small",
			Weight:                             99,
			Tier:                               "cheap",
			RPM:                                10,
			InputPricePerMillionUSD:            0.25,
			OutputPricePerMillionUSD:           1.25,
			ImageInputPricePerMillionTokensUSD: 3.5,
			ImageInputPricePerImageUSD:         0.002,
			PricingSource:                      "https://example.test/pricing",
			PricingUpdatedAt:                   "2026-06-17",
			ToolSupport: ToolSupport{
				OpenAIResponses: []string{"function"},
			},
			ForceStoreFalse:  true,
			OutputTokenField: "max_completion_tokens",
		},
		"large": {Model: "mock-large", Weight: 99, Tier: "heavy"},
	}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", ModelRef: "small"},
		{Provider: "mock", ModelRef: "small", Weight: 7, OutputTokenField: "max_tokens"},
		{Provider: "mock", ModelRef: "large", Weight: 9},
		{Provider: "mock", Model: "direct-model", Weight: 1},
	}}
	cfg.Models["fast"] = ModelGroup{Strategy: "weighted", Targets: []Target{
		{Provider: "mock", ModelRef: "small", Weight: 3},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	targets := cfg.Models["default"].Targets
	if targets[0].Model != "mock-small" || targets[0].Weight != 0 || targets[0].Tier != "cheap" || targets[0].RPM != 10 {
		t.Fatalf("small ref not resolved: %#v", targets[0])
	}
	if targets[0].InputPricePerMillionUSD != 0.25 || targets[0].OutputPricePerMillionUSD != 1.25 ||
		targets[0].ImageInputPricePerMillionTokensUSD != 3.5 || targets[0].ImageInputPricePerImageUSD != 0.002 ||
		targets[0].PricingSource != "https://example.test/pricing" || targets[0].PricingUpdatedAt != "2026-06-17" ||
		len(targets[0].ToolSupport.OpenAIResponses) != 1 || targets[0].ToolSupport.OpenAIResponses[0] != "function" {
		t.Fatalf("small ref metadata not resolved: %#v", targets[0])
	}
	if !targets[0].ForceStoreFalse || targets[0].OutputTokenField != "max_completion_tokens" {
		t.Fatalf("small ref encoding metadata not resolved: %#v", targets[0])
	}
	if targets[1].Model != "mock-small" || targets[1].Weight != 7 || !targets[1].ForceStoreFalse || targets[1].OutputTokenField != "max_tokens" {
		t.Fatalf("small ref override not resolved: %#v", targets[1])
	}
	if targets[2].Model != "mock-large" || targets[2].Weight != 9 || targets[2].Tier != "heavy" {
		t.Fatalf("large ref override not resolved: %#v", targets[2])
	}
	if targets[3].Model != "direct-model" {
		t.Fatalf("direct model target changed: %#v", targets[3])
	}
	if got := cfg.Models["fast"].Targets[0].Weight; got != 3 {
		t.Fatalf("group-local target weight = %d, want 3", got)
	}
}

func TestChatToResponsesBridgeStatefulSessionConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		session BridgeStatefulSessionsConfig
		want    string
	}{
		{
			name: "memory remains valid",
			session: BridgeStatefulSessionsConfig{
				Enabled:       true,
				Backend:       "memory",
				SessionHeader: "X-Router-Session",
				TTLSeconds:    600,
				MaxEntries:    100,
			},
		},
		{
			name: "redis valid",
			session: BridgeStatefulSessionsConfig{
				Enabled:       true,
				Backend:       "redis",
				SessionHeader: "X-Router-Session",
				TTLSeconds:    600,
				Redis: BridgeStatefulSessionsRedisConfig{
					Address:          "redis.example.test:6379",
					Namespace:        "prod-router",
					PasswordEnv:      "ROUTER_BRIDGE_REDIS_PASSWORD",
					ConnectTimeoutMS: 250,
					ReadTimeoutMS:    250,
					WriteTimeoutMS:   250,
					TLS: BridgeStatefulSessionsRedisTLSConfig{
						Enabled:    true,
						ServerName: "redis.example.test",
					},
				},
			},
		},
		{
			name: "unsupported backend",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "postgres",
			},
			want: "backend must be memory or redis",
		},
		{
			name: "bad header",
			session: BridgeStatefulSessionsConfig{
				Enabled:       true,
				SessionHeader: "Bad Header",
			},
			want: "session_header",
		},
		{
			name: "redis missing address",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "redis",
				Redis:   BridgeStatefulSessionsRedisConfig{Namespace: "prod-router"},
			},
			want: "address is required",
		},
		{
			name: "redis rejects inline password",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "redis",
				Redis: BridgeStatefulSessionsRedisConfig{
					Address:  "redis.example.test:6379",
					Password: "raw-secret",
				},
			},
			want: "password_env",
		},
		{
			name: "redis rejects url address credentials",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "redis",
				Redis: BridgeStatefulSessionsRedisConfig{
					Address: "redis://:raw-secret@redis.example.test:6379",
				},
			},
			want: "without URL scheme or credentials",
		},
		{
			name: "redis rejects userinfo address",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "redis",
				Redis: BridgeStatefulSessionsRedisConfig{
					Address: "user:raw-secret@redis.example.test:6379",
				},
			},
			want: "without URL scheme or credentials",
		},
		{
			name: "redis rejects invalid namespace",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "redis",
				Redis: BridgeStatefulSessionsRedisConfig{
					Address:   "redis.example.test:6379",
					Namespace: "prod/router",
				},
			},
			want: "namespace",
		},
		{
			name: "redis tls server name requires tls",
			session: BridgeStatefulSessionsConfig{
				Enabled: true,
				Backend: "redis",
				Redis: BridgeStatefulSessionsRedisConfig{
					Address: "redis.example.test:6379",
					TLS:     BridgeStatefulSessionsRedisTLSConfig{ServerName: "redis.example.test"},
				},
			},
			want: "tls.server_name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.Provider["responses"] = ProviderConfig{BaseURL: "https://responses.example.test/v1", Dialect: "openai-responses"}
			cfg.Models["stateful"] = ModelGroup{Strategy: "static", Targets: []Target{{
				Provider: "responses",
				Model:    "responses-model",
				Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{
					Enabled:          true,
					StatefulSessions: tt.session,
				}},
			}}}
			cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "stateful")
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() err=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() err=%v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "raw-secret") {
				t.Fatalf("Validate() exposed inline password: %v", err)
			}
		})
	}
}

func TestModelGroupContractValidation(t *testing.T) {
	score := 0.9
	passRate := 0.95
	for _, tt := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "valid contract",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{
					Strategy: "weighted",
					Contract: &ModelGroupContract{
						SupportedAPIShapes: []string{"openai_chat"},
						IntendedWorkloads:  []string{"support-chat"},
						RequiredCaps: ContractRequiredCapabilities{
							InputModalities:  []string{"text"},
							OutputModalities: []string{"text"},
							MinContextTokens: 1024,
						},
						QualityFloor: ContractQualityFloor{
							RequireTags:             []string{"validated"},
							MinEvalQualityScore:     &score,
							MinEvalPassRate:         &passRate,
							AllowedValidationStatus: []string{"passed"},
						},
					},
					Targets: []Target{{
						Provider:      "mock",
						Model:         "mock-model",
						ContextTokens: 4096,
						Tags:          []string{"validated"},
						Validation:    &TargetValidation{Status: "passed", Workload: "support-chat", ValidatedAt: "2026-06-20", QualityScore: 0.92, PassRate: 0.97, Harness: "unit"},
					}},
				}
			},
		},
		{
			name: "invalid api shape",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Contract: &ModelGroupContract{SupportedAPIShapes: []string{"bad_shape"}}, Targets: []Target{{Provider: "mock", Model: "mock-model"}}}
			},
			want: "unsupported API shape",
		},
		{
			name: "api shape unavailable",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Contract: &ModelGroupContract{SupportedAPIShapes: []string{"anthropic_messages"}}, Targets: []Target{{Provider: "mock", Model: "mock-model", ToolOnly: true}}}
			},
			want: "is not served by any target",
		},
		{
			name: "translated plain api shape",
			edit: func(cfg *Config) {
				cfg.Provider["mock"] = ProviderConfig{BaseURL: "http://mock", APIKey: "test-key", Dialect: "openai-responses"}
				cfg.Models["default"] = ModelGroup{
					Strategy: "static",
					Contract: &ModelGroupContract{SupportedAPIShapes: []string{"openai_chat"}},
					Targets:  []Target{{Provider: "mock", Model: "responses-model"}},
				}
			},
		},
		{
			name: "invalid modality",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Contract: &ModelGroupContract{RequiredCaps: ContractRequiredCapabilities{InputModalities: []string{"smell"}}}, Targets: []Target{{Provider: "mock", Model: "mock-model"}}}
			},
			want: "unsupported modality",
		},
		{
			name: "invalid quality score",
			edit: func(cfg *Config) {
				bad := 1.1
				cfg.Models["default"] = ModelGroup{Strategy: "static", Contract: &ModelGroupContract{QualityFloor: ContractQualityFloor{MinEvalQualityScore: &bad}}, Targets: []Target{{Provider: "mock", Model: "mock-model"}}}
			},
			want: "min_eval_quality_score must be between 0 and 1",
		},
		{
			name: "require tag unavailable",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Contract: &ModelGroupContract{QualityFloor: ContractQualityFloor{RequireTags: []string{"validated"}}}, Targets: []Target{{Provider: "mock", Model: "mock-model"}}}
			},
			want: "cannot be satisfied",
		},
		{
			name: "tools unavailable",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Contract: &ModelGroupContract{RequiredCaps: ContractRequiredCapabilities{Tools: true}}, Targets: []Target{{Provider: "mock", Model: "mock-model"}}}
			},
			want: "cannot be satisfied",
		},
		{
			name: "invalid validation date",
			edit: func(cfg *Config) {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model", Validation: &TargetValidation{Status: "passed", Workload: "support", ValidatedAt: "06/20/2026"}}}}
			},
			want: "validation.validated_at must be YYYY-MM-DD",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			tt.edit(cfg)
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() error=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestContentCaptureConfigValidation(t *testing.T) {
	falseValue := false
	encryption := ContentCaptureEncryptionConfig{Enabled: true, LocalKeyID: "local-test"}
	for _, tt := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "enabled without scope",
			edit: func(cfg *Config) {
				cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 30}
			},
			want: "no capture scope",
		},
		{
			name: "redaction disabled",
			edit: func(cfg *Config) {
				cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true, RedactBeforeStorage: &falseValue, Encryption: encryption}
			},
			want: "redact_before_storage must remain true",
		},
		{
			name: "forbidden header",
			edit: func(cfg *Config) {
				cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true, CaptureHeadersAllowlist: []string{"Authorization"}, Encryption: encryption}
			},
			want: "forbidden header",
		},
		{
			name: "invalid custom regex",
			edit: func(cfg *Config) {
				cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true, RedactionPatterns: []ContentCaptureRedactionRule{{Name: "bad", Expression: "["}}, Encryption: encryption}
			},
			want: "redaction pattern bad is invalid",
		},
		{
			name: "capture without encryption",
			edit: func(cfg *Config) {
				cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true}
			},
			want: "encryption.enabled must be true",
		},
		{
			name: "encryption without local key id",
			edit: func(cfg *Config) {
				cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true, Encryption: ContentCaptureEncryptionConfig{Enabled: true}}
			},
			want: "encryption.local_key_id is required",
		},
		{
			name: "caller capture requires usage db",
			edit: func(cfg *Config) {
				enabled := false
				cfg.Server.UsageDB.Enable = &enabled
				cfg.Callers[0].ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true, Encryption: encryption}
			},
			want: "content_capture requires usage_db enabled",
		},
		{
			name: "model group capture requires usage db",
			edit: func(cfg *Config) {
				enabled := false
				cfg.Server.UsageDB.Enable = &enabled
				group := cfg.Models["default"]
				group.ContentCapture = ContentCaptureConfig{Enabled: true, CaptureRequest: true, Encryption: encryption}
				cfg.Models["default"] = group
			},
			want: "content_capture requires usage_db enabled",
		},
		{
			name: "caller override validates",
			edit: func(cfg *Config) {
				cfg.Callers[0].ContentCapture = ContentCaptureConfig{Enabled: true}
			},
			want: "no capture scope",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.setDefaults()
			tt.edit(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestContentCaptureEncryptionConfigValidates(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.setDefaults()
	cfg.Server.ContentCapture = ContentCaptureConfig{
		Enabled:        true,
		CaptureRequest: true,
		Encryption:     ContentCaptureEncryptionConfig{Enabled: true, LocalKeyID: "test-key"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error=%v", err)
	}
}

func TestRetentionConfigValidation(t *testing.T) {
	falseValue := false
	disabledUsageDB := false
	for _, tt := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "unknown data class",
			edit: func(cfg *Config) {
				cfg.Server.Retention = RetentionConfig{
					Enabled: true,
					Classes: []RetentionClassConfig{{DataClass: "raw_prompts", RetentionDays: 30}},
				}
				defaultRetentionConfig(&cfg.Server.Retention)
			},
			want: "unknown data_class",
		},
		{
			name: "non positive retention days",
			edit: func(cfg *Config) {
				cfg.Server.Retention = RetentionConfig{
					Enabled: true,
					Classes: []RetentionClassConfig{{DataClass: retentionDataClassContentCapture, RetentionDays: -1}},
				}
				defaultRetentionConfig(&cfg.Server.Retention)
			},
			want: "retention_days must be positive",
		},
		{
			name: "non positive batch",
			edit: func(cfg *Config) {
				cfg.Server.Retention = RetentionConfig{
					Enabled:          true,
					DefaultBatchSize: -5,
					Classes:          []RetentionClassConfig{{DataClass: retentionDataClassContentCapture, RetentionDays: 30, BatchSize: -5}},
				}
				defaultRetentionConfig(&cfg.Server.Retention)
			},
			want: "default_batch_size must be positive",
		},
		{
			name: "usage db disabled",
			edit: func(cfg *Config) {
				cfg.Server.UsageDB.Enable = &disabledUsageDB
				cfg.Server.Retention.Enabled = true
			},
			want: "retention requires usage_db enabled",
		},
		{
			name: "usage detail requires finalized rollup",
			edit: func(cfg *Config) {
				cfg.Server.Retention = RetentionConfig{
					Enabled: true,
					Classes: []RetentionClassConfig{{
						DataClass:              retentionDataClassUsageDetail,
						RetentionDays:          365,
						RequireFinalizedRollup: false,
					}},
				}
				cfg.Server.Retention.DryRun = boolPtr(true)
				cfg.Server.Retention.DefaultBatchSize = 500
				for i := range cfg.Server.Retention.Classes {
					cfg.Server.Retention.Classes[i].BatchSize = 500
				}
			},
			want: "usage_detail requires finalized rollup",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.setDefaults()
			tt.edit(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error=%v, want %q", err, tt.want)
			}
		})
	}
	cfg := minimalConfig(t)
	cfg.setDefaults()
	cfg.Server.Retention = RetentionConfig{
		Enabled: true,
		DryRun:  &falseValue,
		Classes: []RetentionClassConfig{{
			DataClass:     retentionDataClassUsageDiagnostics,
			RetentionDays: 30,
			BatchSize:     25,
		}},
		DefaultBatchSize: 25,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() dry_run=false error=%v", err)
	}
}

func TestExplicitAccountsValidateCallerOwnership(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Users = []UserConfig{{ID: "Alice", Name: "Alice Example"}}
	cfg.Projects = []ProjectConfig{{ID: "Example Project", Name: "Example Project"}}
	cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "Alice", Project: "Example Project", Role: "admin"}}
	cfg.Callers[0].OwnerUser = "Alice"
	cfg.Callers[0].Project = "Example Project"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() explicit account directory: %v", err)
	}
}

func TestExplicitAccountsRejectInactiveMembership(t *testing.T) {
	for _, status := range []string{"disabled", "suspended", "removed", "archived"} {
		t.Run(status, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.Users = []UserConfig{{ID: "alice"}}
			cfg.Projects = []ProjectConfig{{ID: "metrum-insights"}}
			cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "alice", Project: "metrum-insights", Status: status}}
			cfg.Callers[0].OwnerUser = "alice"
			cfg.Callers[0].Project = "metrum-insights"
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "non-active membership") {
				t.Fatalf("Validate() error=%v, want inactive membership rejection", err)
			}
		})
	}
}

func TestCallerKeyLifecycleStatusesValidate(t *testing.T) {
	for _, status := range []string{"active", "disabled", "suspended", "removed", "archived", "expired", "rotated"} {
		t.Run(status, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.Callers[0].Status = status
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() status %q: %v", status, err)
			}
		})
	}
}

func TestExplicitAccountsRejectConflictingLegacyUserAlias(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Callers[0].OwnerUser = "alice"
	cfg.Callers[0].User = "bob"
	cfg.Callers[0].Project = "metrum-insights"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "conflicting owner_user") {
		t.Fatalf("Validate() error=%v, want owner_user/user conflict", err)
	}
}

func TestExplicitAccountsRejectAccountlessCaller(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Users = []UserConfig{{ID: "alice"}}
	cfg.Projects = []ProjectConfig{{ID: "metrum-insights"}}
	cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "alice", Project: "metrum-insights"}}
	cfg.Callers[0].OwnerUser = ""
	cfg.Callers[0].User = ""
	cfg.Callers[0].Project = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "missing owner_user and project") {
		t.Fatalf("Validate() error=%v, want accountless caller rejection", err)
	}
}

func TestExplicitAccountsRejectDuplicateNormalizedIDs(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Users = []UserConfig{{ID: "Alice Smith"}, {ID: "alice/smith"}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate user id") {
		t.Fatalf("Validate() error=%v, want duplicate user id rejection", err)
	}
}

func TestMissingProviderModelRefFailsValidation(t *testing.T) {
	cfg := minimalConfig(t)
	provider := cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{
		"known": {Model: "mock-known"},
	}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "missing"}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown model_ref missing") {
		t.Fatalf("expected missing model_ref error, got %v", err)
	}
}

func TestProviderModelPricingAndToolSupportValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		model  ProviderModel
		target Target
		want   string
	}{
		{
			name:  "negative provider input price",
			model: ProviderModel{Model: "mock-known", InputPricePerMillionUSD: -0.01},
			want:  "input_price_per_million_usd",
		},
		{
			name:  "duplicate provider tool support",
			model: ProviderModel{Model: "mock-known", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "function"}}},
			want:  "duplicate",
		},
		{
			name:   "negative target output price",
			model:  ProviderModel{Model: "mock-known"},
			target: Target{OutputPricePerMillionUSD: -0.01},
			want:   "output_price_per_million_usd",
		},
		{
			name:  "negative provider image token price",
			model: ProviderModel{Model: "mock-known", ImageInputPricePerMillionTokensUSD: -0.01},
			want:  "image_input_price_per_million_tokens_usd",
		},
		{
			name:  "invalid provider output token field",
			model: ProviderModel{Model: "mock-known", OutputTokenField: "tokens"},
			want:  "output_token_field",
		},
		{
			name:   "negative target image unit price",
			model:  ProviderModel{Model: "mock-known"},
			target: Target{ImageInputPricePerImageUSD: -0.01},
			want:   "image_input_price_per_image_usd",
		},
		{
			name:   "invalid target output token field",
			model:  ProviderModel{Model: "mock-known"},
			target: Target{OutputTokenField: "completion_tokens"},
			want:   "output_token_field",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			provider := cfg.Provider["mock"]
			provider.Models = map[string]ProviderModel{"known": tt.model}
			cfg.Provider["mock"] = provider
			target := Target{Provider: "mock", ModelRef: "known"}
			if tt.target.OutputPricePerMillionUSD != 0 {
				target.OutputPricePerMillionUSD = tt.target.OutputPricePerMillionUSD
			}
			if tt.target.ImageInputPricePerMillionTokensUSD != 0 {
				target.ImageInputPricePerMillionTokensUSD = tt.target.ImageInputPricePerMillionTokensUSD
			}
			if tt.target.ImageInputPricePerImageUSD != 0 {
				target.ImageInputPricePerImageUSD = tt.target.ImageInputPricePerImageUSD
			}
			if tt.target.OutputTokenField != "" {
				target.OutputTokenField = tt.target.OutputTokenField
			}
			cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateRejectsDuplicateCallerIdentifiers(t *testing.T) {
	const (
		hashOne = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		hashTwo = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	)
	for _, tt := range []struct {
		name          string
		callers       []CallerConfig
		want          string
		mustNotExpose string
	}{
		{
			name: "duplicate token hash different caller ids",
			callers: []CallerConfig{
				{ID: "alice", TokenSHA256: hashOne, Allow: []string{"default"}},
				{ID: "bob", TokenSHA256: hashOne, Allow: []string{"default"}},
			},
			want:          "duplicate caller token_sha256 for callers alice and bob",
			mustNotExpose: hashOne,
		},
		{
			name: "duplicate caller id different hashes",
			callers: []CallerConfig{
				{ID: "alice", TokenSHA256: hashOne, Allow: []string{"default"}},
				{ID: "alice", TokenSHA256: hashTwo, Allow: []string{"default"}},
			},
			want: "duplicate caller id",
		},
		{
			name: "duplicate public token id",
			callers: []CallerConfig{
				{ID: "alice", TokenSHA256: hashOne, TokenID: "rtr_metrum_alice_test_dev_k1", Allow: []string{"default"}},
				{ID: "bob", TokenSHA256: hashTwo, TokenID: "rtr_metrum_alice_test_dev_k1", Allow: []string{"default"}},
			},
			want: "duplicate caller token_id for callers alice and bob",
		},
		{
			name: "case insensitive duplicate hash",
			callers: []CallerConfig{
				{ID: "alice", TokenSHA256: hashOne, Allow: []string{"default"}},
				{ID: "bob", TokenSHA256: strings.ToUpper(hashOne), Allow: []string{"default"}},
			},
			want:          "duplicate caller token_sha256 for callers alice and bob",
			mustNotExpose: hashOne,
		},
		{
			name: "duplicate involving metrics admin caller",
			callers: []CallerConfig{
				{ID: "metrics-admin", TokenSHA256: hashOne, TokenID: "rtr_metrum_metrics_admin_test_k1", MetricsAdmin: true},
				{ID: "alice", TokenSHA256: hashOne, TokenID: "rtr_metrum_alice_test_dev_k1", Allow: []string{"default"}},
			},
			want:          "duplicate caller token_sha256 for callers metrics-admin and alice",
			mustNotExpose: hashOne,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.Callers = tt.callers
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() err=%v, want %q", err, tt.want)
			}
			if tt.mustNotExpose != "" && strings.Contains(err.Error(), tt.mustNotExpose) {
				t.Fatalf("Validate() exposed token hash in error: %v", err)
			}
		})
	}
}

func TestPIIFilterValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		filter PIIFilterConfig
		want   string
	}{
		{
			name:   "disabled but configured",
			filter: PIIFilterConfig{Rules: []PIIFilterRule{{Name: "email", Expression: `@`, PlaceholderPrefix: "EMAIL"}}},
			want:   "enabled is false",
		},
		{
			name:   "missing rules",
			filter: PIIFilterConfig{Enabled: true},
			want:   "requires at least one rule",
		},
		{
			name: "invalid mode",
			filter: PIIFilterConfig{
				Enabled: true,
				Mode:    "restore_everywhere",
				Rules:   []PIIFilterRule{{Name: "email", Expression: `@`, PlaceholderPrefix: "EMAIL"}},
			},
			want: "mode must be",
		},
		{
			name: "invalid regex",
			filter: PIIFilterConfig{
				Enabled: true,
				Rules:   []PIIFilterRule{{Name: "email", Expression: `[`, PlaceholderPrefix: "EMAIL"}},
			},
			want: "invalid expression",
		},
		{
			name: "duplicate rule",
			filter: PIIFilterConfig{
				Enabled: true,
				Rules: []PIIFilterRule{
					{Name: "email", Expression: `@`, PlaceholderPrefix: "EMAIL"},
					{Name: "email", Expression: `phone`, PlaceholderPrefix: "PHONE"},
				},
			},
			want: "duplicate rule",
		},
		{
			name: "missing placeholder prefix",
			filter: PIIFilterConfig{
				Enabled: true,
				Rules:   []PIIFilterRule{{Name: "email", Expression: `@`, PlaceholderPrefix: "!!!"}},
			},
			want: "missing placeholder_prefix",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.Models["default"] = ModelGroup{
				Strategy:  "static",
				PIIFilter: tt.filter,
				Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
			}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() err=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestAdminBasicAuthValidation(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	for _, tt := range []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{
			name: "disabled default",
		},
		{
			name: "disabled but users configured",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", Domain: "local/dev"}}
			},
			want: "users configured but basic auth is disabled",
		},
		{
			name: "enabled without users",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Basic.Enabled = true
			},
			want: "requires at least one user",
		},
		{
			name: "duplicate usernames",
			configure: func(cfg *Config) {
				t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{
					{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", Domain: "local/dev"},
					{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", Domain: "local/dev"},
				}
			},
			want: "duplicate username",
		},
		{
			name: "missing env reference",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", Domain: "local/dev"}}
			},
			want: "requires password_hash_env",
		},
		{
			name: "invalid env hash",
			configure: func(cfg *Config) {
				t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_INVALID", "not-a-hash")
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_INVALID", Domain: "local/dev"}}
			},
			want: "password hash must be bcrypt",
		},
		{
			name: "env hash",
			configure: func(cfg *Config) {
				t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", Domain: "local/dev"}}
			},
		},
		{
			name: "missing env hash",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_MISSING", Domain: "local/dev"}}
			},
			want: "password_hash_env SMART_ROUTER_ADMIN_PASSWORD_HASH_MISSING is not set",
		},
		{
			name: "missing domain",
			configure: func(cfg *Config) {
				t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST"}}
			},
			want: "domain is required",
		},
		{
			name: "invalid trusted proxy cidr",
			configure: func(cfg *Config) {
				t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.TrustedProxyCIDRs = []string{"not-a-cidr"}
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", Domain: "local/dev"}}
			},
			want: "invalid CIDR",
		},
		{
			name: "valid configured subject and permission",
			configure: func(cfg *Config) {
				t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
				cfg.Server.AdminAuth.Basic.Enabled = true
				cfg.Server.AdminAuth.Basic.TrustedProxyCIDRs = []string{"127.0.0.1/32"}
				cfg.Server.AdminAuth.Basic.Users = []AdminBasicAuthUser{{
					Username:        "admin",
					PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
					Subject:         "basic:admin",
					Domain:          "local/dev",
					Permissions:     []string{"admin:auth:read"},
				}}
			},
		},
		{
			name: "oidc missing env",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.OIDC.Enabled = true
				cfg.Server.AdminAuth.OIDC.IssuerURL = "https://accounts.example.com"
				cfg.Server.AdminAuth.OIDC.ClientIDEnv = "TEST_OIDC_CLIENT_ID_MISSING"
				cfg.Server.AdminAuth.OIDC.ClientSecretEnv = "TEST_OIDC_CLIENT_SECRET"
				cfg.Server.AdminAuth.OIDC.RedirectURL = "https://router.example.com/admin/auth/callback"
				cfg.Server.AdminAuth.OIDC.Domain = "example/prod"
				t.Setenv("TEST_OIDC_CLIENT_SECRET", "secret")
			},
			want: "client_id_env TEST_OIDC_CLIENT_ID_MISSING is not set",
		},
		{
			name: "oidc invalid redirect",
			configure: func(cfg *Config) {
				t.Setenv("TEST_OIDC_CLIENT_ID", "client-id")
				t.Setenv("TEST_OIDC_CLIENT_SECRET", "secret")
				cfg.Server.AdminAuth.OIDC.Enabled = true
				cfg.Server.AdminAuth.OIDC.IssuerURL = "https://accounts.example.com"
				cfg.Server.AdminAuth.OIDC.ClientIDEnv = "TEST_OIDC_CLIENT_ID"
				cfg.Server.AdminAuth.OIDC.ClientSecretEnv = "TEST_OIDC_CLIENT_SECRET"
				cfg.Server.AdminAuth.OIDC.RedirectURL = "http://router.example.com/admin/auth/callback"
				cfg.Server.AdminAuth.OIDC.Domain = "example/prod"
			},
			want: "redirect_url must use https",
		},
		{
			name: "oidc invalid allowed domain",
			configure: func(cfg *Config) {
				t.Setenv("TEST_OIDC_CLIENT_ID", "client-id")
				t.Setenv("TEST_OIDC_CLIENT_SECRET", "secret")
				cfg.Server.AdminAuth.OIDC.Enabled = true
				cfg.Server.AdminAuth.OIDC.IssuerURL = "https://accounts.example.com"
				cfg.Server.AdminAuth.OIDC.ClientIDEnv = "TEST_OIDC_CLIENT_ID"
				cfg.Server.AdminAuth.OIDC.ClientSecretEnv = "TEST_OIDC_CLIENT_SECRET"
				cfg.Server.AdminAuth.OIDC.RedirectURL = "https://router.example.com/admin/auth/callback"
				cfg.Server.AdminAuth.OIDC.AllowedDomains = []string{"@example.com"}
				cfg.Server.AdminAuth.OIDC.Domain = "example/prod"
			},
			want: "allowed domain",
		},
		{
			name: "oidc valid localhost development",
			configure: func(cfg *Config) {
				t.Setenv("TEST_OIDC_CLIENT_ID", "client-id")
				t.Setenv("TEST_OIDC_CLIENT_SECRET", "secret")
				cfg.Server.AdminAuth.OIDC.Enabled = true
				cfg.Server.AdminAuth.OIDC.IssuerURL = "http://localhost:8081"
				cfg.Server.AdminAuth.OIDC.ClientIDEnv = "TEST_OIDC_CLIENT_ID"
				cfg.Server.AdminAuth.OIDC.ClientSecretEnv = "TEST_OIDC_CLIENT_SECRET"
				cfg.Server.AdminAuth.OIDC.RedirectURL = "http://localhost:8080/admin/auth/callback"
				cfg.Server.AdminAuth.OIDC.AllowedDomains = []string{"example.com"}
				cfg.Server.AdminAuth.OIDC.Domain = "example/prod"
				cfg.Server.AdminAuth.Sessions = AdminSessionConfig{CookieName: "test_admin_session", TTL: time.Hour, SecureCookies: testBoolPtr(false), SameSite: "lax"}
			},
		},
		{
			name: "session same_site none requires secure cookies",
			configure: func(cfg *Config) {
				t.Setenv("TEST_OIDC_CLIENT_ID", "client-id")
				t.Setenv("TEST_OIDC_CLIENT_SECRET", "secret")
				cfg.Server.AdminAuth.OIDC.Enabled = true
				cfg.Server.AdminAuth.OIDC.IssuerURL = "https://accounts.example.com"
				cfg.Server.AdminAuth.OIDC.ClientIDEnv = "TEST_OIDC_CLIENT_ID"
				cfg.Server.AdminAuth.OIDC.ClientSecretEnv = "TEST_OIDC_CLIENT_SECRET"
				cfg.Server.AdminAuth.OIDC.RedirectURL = "https://router.example.com/admin/auth/callback"
				cfg.Server.AdminAuth.OIDC.Domain = "example/prod"
				cfg.Server.AdminAuth.Sessions = AdminSessionConfig{CookieName: "test_admin_session", TTL: time.Hour, SecureCookies: testBoolPtr(false), SameSite: "none"}
			},
			want: "same_site none requires secure_cookies",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			if tt.configure != nil {
				tt.configure(cfg)
			}
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() err=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() err=%v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), hash) {
				t.Fatalf("Validate() exposed password hash: %v", err)
			}
		})
	}
}

func TestAdminAuthorizationSourceValidation(t *testing.T) {
	disabled := false
	for _, tt := range []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{
			name: "db source allows policy-free config",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
			},
		},
		{
			name: "db source requires usage db enabled",
			configure: func(cfg *Config) {
				cfg.Server.UsageDB.Enable = &disabled
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
			},
			want: "source db requires usage_db enabled",
		},
		{
			name: "db source rejects inline policy mixing",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db", Policy: []string{"p, role, local/test, metrics, read"}}
			},
			want: "source db cannot combine inline policy or policy_file",
		},
		{
			name: "unknown source rejected",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "elsewhere"}
			},
			want: "source must be static or db",
		},
		{
			name: "static policy rejects unsafe fields",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, bearer secret, local/test, metrics, read"}}
			},
			want: "contains unsafe field",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			tt.configure(cfg)
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() err=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() err=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestAdminReportsConfigValidation(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	for _, tt := range []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{
			name: "enabled requires browser auth",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read"}}
				cfg.Server.AdminReports.Enabled = true
			},
			want: "requires server.admin_auth.basic or server.admin_auth.oidc enabled",
		},
		{
			name: "enabled requires authorization",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", Domain: "local/test"}}}
				cfg.Server.AdminReports.Enabled = true
			},
			want: "requires server.admin_auth.authorization enabled",
		},
		{
			name: "invalid policy",
			configure: func(cfg *Config) {
				cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, only-two-fields"}}
			},
			want: "p lines require",
		},
		{
			name: "bad range",
			configure: func(cfg *Config) {
				cfg.Server.AdminReports.DefaultSince = "32d"
				cfg.Server.AdminReports.MaxRange = "31d"
			},
			want: "default_since cannot exceed max_range",
		},
		{
			name: "bad rows",
			configure: func(cfg *Config) {
				cfg.Server.AdminReports.MaxRows = 10001
			},
			want: "max_rows",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.setDefaults()
			tt.configure(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() err=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestPIIFilterDocumentedYAMLShapeValidates(t *testing.T) {
	raw := []byte(`
server:
  logging:
    path: requests.jsonl
providers:
  mock:
    base_url: http://127.0.0.1:1/v1
    dialect: openai-chat
    api_key: secret
models:
  sensitive-workloads:
    strategy: weighted
    pii_filter:
      enabled: true
      mode: redact_only
      restore_response: false
      max_replacements_per_request: 200
      apply_to:
        system: true
        messages: true
        responses_input: true
        tool_results: true
        image_urls: false
      rules:
        - name: email
          expression: '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
          placeholder_prefix: EMAIL
        - name: us_phone
          expression: '\b(?:\+1[-. ]?)?\(?[2-9]\d{2}\)?[-. ]?[2-9]\d{2}[-. ]?\d{4}\b'
          placeholder_prefix: PHONE
        - name: us_ssn
          expression: '\b\d{3}-\d{2}-\d{4}\b'
          placeholder_prefix: US_SSN
    targets:
      - { provider: mock, model: mock-model, weight: 1 }
callers:
  - id: alice
    token_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    allow: [sensitive-workloads]
`)
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Server.Identifiers.Mode = "passthrough"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("documented pii_filter YAML shape did not validate: %v", err)
	}
	filter := cfg.Models["sensitive-workloads"].PIIFilter
	if !filter.Enabled || filter.Mode != "redact_only" || len(filter.Rules) != 3 {
		t.Fatalf("unexpected pii_filter decode: %#v", filter)
	}
	if filter.ApplyTo.System == nil || !*filter.ApplyTo.System || filter.ApplyTo.ImageURLs {
		t.Fatalf("unexpected apply_to decode: %#v", filter.ApplyTo)
	}
}

func TestWeightedOrderTreatsOmittedTargetWeightAsOne(t *testing.T) {
	targets := weightedOrder([]Target{{Provider: "mock", Model: "only"}})
	if len(targets) != 1 || targets[0].Provider != "mock" || targets[0].Model != "only" {
		t.Fatalf("weighted order with omitted weight = %#v", targets)
	}
}

func TestExampleConfigDefaultIncludesLatestCodingTargets(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	standardSum := sha256.Sum256([]byte("rtr_example_standard_test"))
	codingSum := sha256.Sum256([]byte("rtr_example_coding_test"))
	metricsAdminSum := sha256.Sum256([]byte("rtr_example_metrics_admin_test"))
	contentAdminSum := sha256.Sum256([]byte("rtr_example_content_admin_test"))
	text := strings.ReplaceAll(string(raw), "REPLACE_WITH_SHA256_HEX_OF_STANDARD_ROUTER_TOKEN", hex.EncodeToString(standardSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CODING_ROUTER_TOKEN", hex.EncodeToString(codingSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_METRICS_ADMIN_ROUTER_TOKEN", hex.EncodeToString(metricsAdminSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CONTENT_ADMIN_ROUTER_TOKEN", hex.EncodeToString(contentAdminSum[:]))
	text = strings.ReplaceAll(text, "script: scripts/router.ts", "script: ../../scripts/router.ts")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	assertDefaultGroupTargets(t, cfg.Models["default"])
	assertAnthropicCompatibleProvider(t, cfg.Provider["minimax_anthropic"], "m3", "MiniMax-M3")
	assertAnthropicCompatibleProvider(t, cfg.Provider["kimi_anthropic"], "kimi-k2.7-code", "kimi-k2.7-code")
	assertAnthropicCompatibleProvider(t, cfg.Provider["baseten_anthropic"], "gpt-oss-120b", "openai/gpt-oss-120b")
	if cfg.Provider["baseten"].Models["gpt-oss-120b"].Model != "openai/gpt-oss-120b" {
		t.Fatalf("example config missing Baseten GPT OSS 120B catalog entry")
	}
	assertOpenAIChatProvider(t, cfg.Provider["crusoe"], "llama-3-3-70b-instruct", "meta-llama/Llama-3.3-70B-Instruct")
	assertResponsesCompatibleProvider(t, cfg.Provider["fireworks_responses"], "kimi-k2p7-code", "accounts/fireworks/models/kimi-k2p7-code")
	assertOpenAIChatProvider(t, cfg.Provider["minimax"], "m3", "MiniMax-M3")
	assertResponsesCompatibleProvider(t, cfg.Provider["minimax_responses"], "m3", "MiniMax-M3")
	if got := cfg.Provider["minimax"].Models["m3"]; len(got.ToolSupport.OpenAIResponses) != 0 {
		t.Fatalf("example config MiniMax Chat skin should not claim Responses support: %#v", got.ToolSupport)
	}
	if got := cfg.Provider["minimax_responses"].Models["m3"]; !stringSliceContains(got.ToolSupport.OpenAIResponses, "function") || !got.ForceStoreFalse || !got.Reasoning.Supported || !stringSliceContains(got.RequestShapeSupport.UnsupportedRequestFeatures, "forced_tool_choice") {
		t.Fatalf("example config MiniMax Responses catalog entry=%#v", got)
	}
	if got := cfg.Provider["xai"].Models["grok-4-5"]; !stringSliceContains(got.RequestShapeSupport.UnsupportedRequestFeatures, "stream_options") {
		t.Fatalf("example config xAI Grok 4.5 should gate opencode stream_options shape: %#v", got.RequestShapeSupport)
	}
	if got := cfg.Provider["kimi"].Models["kimi-k2.7-code"]; !stringSliceContains(got.RequestShapeSupport.UnsupportedRequestFeatures, "stream_options") {
		t.Fatalf("example config Kimi K2.7 Code should gate opencode stream_options shape: %#v", got.RequestShapeSupport)
	}
	if got := cfg.Provider["fireworks_responses"].Models["kimi-k2p7-code"]; got.InputPricePerMillionUSD != 0.95 || got.OutputPricePerMillionUSD != 4.00 ||
		!stringSliceContains(got.ToolSupport.OpenAIResponses, "function") || !got.ForceStoreFalse {
		t.Fatalf("example config Fireworks Responses Kimi catalog entry=%#v", got)
	}
	if got := cfg.Provider["fireworks"].Models["deepseek-v4-flash"]; !stringSliceContains(got.RequestShapeSupport.UnsupportedRequestFeatures, "stream_options") {
		t.Fatalf("example config Fireworks DeepSeek should gate opencode stream_options shape: %#v", got.RequestShapeSupport)
	}
	if got := cfg.Provider["crusoe"].Models["gpt-oss-120b"]; got.Model != "openai/gpt-oss-120b" || got.InputPricePerMillionUSD != 0.05 || got.OutputPricePerMillionUSD != 0.2 {
		t.Fatalf("example config Crusoe GPT OSS catalog entry=%#v", got)
	}
	if got := cfg.Provider["crusoe"].Models["gemma-4-31b-it"]; got.Model != "google/gemma-4-31b-it" || got.InputPricePerMillionUSD != 0.14 || got.OutputPricePerMillionUSD != 0.4 ||
		!stringSliceContains(got.ToolSupport.OpenAIChat, "tools") ||
		!stringSliceContains(got.ToolSupport.OpenAIChat, "tool_choice") ||
		!stringSliceContains(got.ToolSupport.OpenAIChat, "structured_outputs") {
		t.Fatalf("example config Crusoe Gemma catalog entry=%#v", got)
	}
	assertAnthropicCompatibleProvider(t, cfg.Provider["openrouter_anthropic"], "gemma-4-26b-a4b-it-nitro", "google/gemma-4-26b-a4b-it:nitro")
	for _, name := range []string{"small", "medium", "high"} {
		group, ok := cfg.Models[name]
		if !ok {
			t.Fatalf("example config missing %s model group", name)
		}
		if len(group.Targets) == 0 {
			t.Fatalf("example config %s model group has no targets", name)
		}
	}
	for name, group := range cfg.Models {
		if strings.HasPrefix(name, "image-analysis-smoke-") {
			if group.Strategy != "static" || len(group.Targets) != 1 {
				t.Fatalf("example config %s must be a single-target static VLM smoke group: %#v", name, group)
			}
			target := group.Targets[0]
			if !stringSliceContains(target.InputModalities, "image") || !stringSliceContains(target.RequestShapeSupport.RequiredInputModalities, "image") || !stringSliceContains(target.RequestShapeSupport.SupportedInboundDialects, "openai-responses") || target.RequestShapeSupport.MinRequestedOutputTokens != 512 {
				t.Fatalf("example config %s must require image-bearing Responses requests with a 512-token floor: %#v", name, target)
			}
			continue
		}
		if name == "agent-tools-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "minimax_responses" || group.Targets[0].Model != "MiniMax-M3" || targetDialect(cfg.Provider[group.Targets[0].Provider], group.Targets[0]) != "openai-responses" {
				t.Fatalf("example config agent-tools-smoke=%#v, want static MiniMax Responses target", group)
			}
			continue
		}
		if name == "minimax-responses-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "minimax_responses" || group.Targets[0].Model != "MiniMax-M3" || group.Targets[0].ToolOnly {
				t.Fatalf("example config minimax-responses-smoke=%#v, want static text-capable MiniMax Responses target", group)
			}
			if !group.Targets[0].ForceStoreFalse || !stringSliceContains(group.Targets[0].ToolSupport.OpenAIResponses, "function") || !group.Targets[0].Reasoning.Supported {
				t.Fatalf("example config minimax-responses-smoke missing force_store_false/function/reasoning metadata: %#v", group.Targets[0])
			}
			continue
		}
		if name == "minimax-responses-tool-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "minimax_responses" || group.Targets[0].Model != "MiniMax-M3" || !group.Targets[0].ToolOnly {
				t.Fatalf("example config minimax-responses-tool-smoke=%#v, want static tool-only MiniMax Responses target", group)
			}
			if !group.Targets[0].ForceStoreFalse || !stringSliceContains(group.Targets[0].ToolSupport.OpenAIResponses, "function") {
				t.Fatalf("example config minimax-responses-tool-smoke missing force_store_false/function metadata: %#v", group.Targets[0])
			}
			continue
		}
		if name == "claude-tools-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "minimax_anthropic" || group.Targets[0].Model != "MiniMax-M3" {
				t.Fatalf("example config claude-tools-smoke=%#v, want static minimax Anthropic-compatible MiniMax-M3", group)
			}
			continue
		}
		if name == "agent-tools-smoke-openrouter" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "openrouter_responses" || group.Targets[0].Model != "anthropic/claude-sonnet-4.6" {
				t.Fatalf("example config agent-tools-smoke-openrouter=%#v, want static OpenRouter Claude Sonnet responses", group)
			}
			continue
		}
		if name == "fireworks-responses-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "fireworks_responses" || group.Targets[0].Model != "accounts/fireworks/models/kimi-k2p7-code" {
				t.Fatalf("example config fireworks-responses-smoke=%#v, want static Fireworks Kimi Responses target", group)
			}
			if group.Targets[0].ToolOnly || !group.Targets[0].ForceStoreFalse || !stringSliceContains(group.Targets[0].ToolSupport.OpenAIResponses, "function") {
				t.Fatalf("example config fireworks-responses-smoke missing text-capable force_store_false function metadata: %#v", group.Targets[0])
			}
			continue
		}
		if name == "fireworks-responses-tool-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "fireworks_responses" || group.Targets[0].Model != "accounts/fireworks/models/kimi-k2p7-code" {
				t.Fatalf("example config fireworks-responses-tool-smoke=%#v, want static Fireworks Kimi Responses target", group)
			}
			if !group.Targets[0].ToolOnly || !group.Targets[0].ForceStoreFalse || !stringSliceContains(group.Targets[0].ToolSupport.OpenAIResponses, "function") {
				t.Fatalf("example config fireworks-responses-tool-smoke missing tool-only force_store_false function metadata: %#v", group.Targets[0])
			}
			continue
		}
		if name == "claude-tools-smoke-openrouter" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "openrouter_anthropic" || group.Targets[0].Model != "anthropic/claude-sonnet-4.6" {
				t.Fatalf("example config claude-tools-smoke-openrouter=%#v, want static OpenRouter Claude Sonnet Anthropic-compatible target", group)
			}
			continue
		}
		if name == "claude-tools-smoke-openrouter-gemma" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "openrouter_anthropic" || group.Targets[0].Model != "google/gemma-4-26b-a4b-it:nitro" {
				t.Fatalf("example config claude-tools-smoke-openrouter-gemma=%#v, want static OpenRouter Gemma Anthropic-compatible target", group)
			}
			continue
		}
		if name == "baseten-nemotron-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "baseten" || group.Targets[0].Model != "nvidia/Nemotron-120B-A12B" {
				t.Fatalf("example config baseten-nemotron-smoke=%#v, want static Baseten Nemotron target", group)
			}
			continue
		}
		if name == "baseten-glm52-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "baseten" || group.Targets[0].Model != "zai-org/GLM-5.2" {
				t.Fatalf("example config baseten-glm52-smoke=%#v, want static Baseten GLM 5.2 target", group)
			}
			continue
		}
		if name == "baseten-gpt-oss-120b-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "baseten" || group.Targets[0].Model != "openai/gpt-oss-120b" {
				t.Fatalf("example config baseten-gpt-oss-120b-smoke=%#v, want static Baseten GPT OSS 120B target", group)
			}
			continue
		}
		if name == "fireworks-gpt-oss-20b-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "fireworks" || group.Targets[0].Model != "accounts/fireworks/models/gpt-oss-20b" {
				t.Fatalf("example config fireworks-gpt-oss-20b-smoke=%#v, want static Fireworks GPT OSS 20B target", group)
			}
			if !stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "tools") ||
				!stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "tool_choice") ||
				!stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "structured_outputs") {
				t.Fatalf("example config fireworks-gpt-oss-20b-smoke missing validated OpenAI Chat capability metadata: %#v", group.Targets[0].ToolSupport)
			}
			if !group.Targets[0].Reasoning.Supported || group.Targets[0].Reasoning.Control != "effort_enum" {
				t.Fatalf("example config fireworks-gpt-oss-20b-smoke missing validated OpenAI Chat reasoning metadata: %#v", group.Targets[0].Reasoning)
			}
			continue
		}
		if name == "large-openai-chat-tools-smoke" {
			if group.Strategy != "failover" || len(group.Targets) != 2 ||
				group.Targets[0].Provider != "fireworks" ||
				group.Targets[0].Model != "accounts/fireworks/models/gpt-oss-20b" ||
				group.Targets[0].RequestShapeSupport.MaxRequestBytes != 196608 ||
				group.Targets[1].Provider != "minimax" ||
				group.Targets[1].Model != "MiniMax-M3" {
				t.Fatalf("example config large-openai-chat-tools-smoke=%#v, want failover with capped Fireworks target and MiniMax fallback", group)
			}
			continue
		}
		if name == "reasoning-smoke" {
			if group.Strategy != "failover" || len(group.Targets) != 3 {
				t.Fatalf("example config reasoning-smoke=%#v, want failover with Chat, Responses, and Anthropic targets", group)
			}
			chat := group.Targets[0]
			if chat.Provider != "fireworks" || chat.Model != "accounts/fireworks/models/gpt-oss-20b" || targetDialect(cfg.Provider[chat.Provider], chat) != "openai-chat" || !chat.Reasoning.Supported || chat.Reasoning.Control != "effort_enum" {
				t.Fatalf("example config reasoning-smoke Chat target=%#v", chat)
			}
			responses := group.Targets[1]
			if responses.Provider != "minimax_responses" || responses.Model != "MiniMax-M3" || targetDialect(cfg.Provider[responses.Provider], responses) != "openai-responses" || !responses.Reasoning.Supported || responses.Reasoning.Control != "effort_enum" {
				t.Fatalf("example config reasoning-smoke Responses target=%#v", responses)
			}
			anthropic := group.Targets[2]
			if anthropic.Provider != "kimi_anthropic" || anthropic.Model != "kimi-k2.7-code" || targetDialect(cfg.Provider[anthropic.Provider], anthropic) != "anthropic" || anthropic.ToolOnly || len(anthropic.DefaultThinking) == 0 {
				t.Fatalf("example config reasoning-smoke Anthropic target=%#v", anthropic)
			}
			continue
		}
		if name == "baseten-gpt-oss-120b-claude-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "baseten_anthropic" || group.Targets[0].Model != "openai/gpt-oss-120b" {
				t.Fatalf("example config baseten-gpt-oss-120b-claude-smoke=%#v, want static Baseten Anthropic GPT OSS 120B target", group)
			}
			if group.Targets[0].ToolOnly {
				t.Fatalf("example config baseten-gpt-oss-120b-claude-smoke=%#v, want text-and-tool smoke target, not tool_only", group)
			}
			continue
		}
		if name == "crusoe-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "crusoe" || group.Targets[0].Model != "meta-llama/Llama-3.3-70B-Instruct" {
				t.Fatalf("example config crusoe-smoke=%#v, want static Crusoe Llama 3.3 70B Instruct target", group)
			}
			if !stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "tools") ||
				!stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "tool_choice") ||
				!stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "structured_outputs") {
				t.Fatalf("example config crusoe-smoke missing validated OpenAI Chat capability metadata: %#v", group.Targets[0].ToolSupport)
			}
			continue
		}
		if name == "crusoe-gemma-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "crusoe" || group.Targets[0].Model != "google/gemma-4-31b-it" {
				t.Fatalf("example config crusoe-gemma-smoke=%#v, want static Crusoe Gemma 4 31B-it target", group)
			}
			if !stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "tools") ||
				!stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "tool_choice") ||
				!stringSliceContains(group.Targets[0].ToolSupport.OpenAIChat, "structured_outputs") {
				t.Fatalf("example config crusoe-gemma-smoke missing validated OpenAI Chat capability metadata: %#v", group.Targets[0].ToolSupport)
			}
			continue
		}
		if name == "crusoe-nemotron-omni-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "crusoe" || group.Targets[0].Model != "nvidia/Nemotron-3-Nano-Omni-Reasoning-30B-A3B" {
				t.Fatalf("example config crusoe-nemotron-omni-smoke=%#v, want static Crusoe Nemotron 3 Nano Omni target", group)
			}
			if !stringSliceContains(group.Targets[0].InputModalities, "image") {
				t.Fatalf("example config crusoe-nemotron-omni-smoke missing image modality: %#v", group.Targets[0].InputModalities)
			}
			if len(group.Targets[0].ToolSupport.OpenAIChat) != 0 {
				t.Fatalf("example config crusoe-nemotron-omni-smoke has unvalidated tool metadata: %#v", group.Targets[0].ToolSupport)
			}
			continue
		}
		if name == "openai-gpt54-vision-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "openai" || group.Targets[0].Model != "gpt-5.4" {
				t.Fatalf("example config openai-gpt54-vision-smoke=%#v, want static OpenAI GPT-5.4 target", group)
			}
			if !stringSliceContains(group.Targets[0].InputModalities, "image") {
				t.Fatalf("example config openai-gpt54-vision-smoke missing image modality: %#v", group.Targets[0].InputModalities)
			}
			if !stringSliceContains(group.Targets[0].ToolSupport.OpenAIResponses, "function") || !stringSliceContains(group.Targets[0].RequestShapeSupport.RequiredInputModalities, "image") {
				t.Fatalf("example config openai-gpt54-vision-smoke missing validated image/function eligibility metadata: %#v", group.Targets[0])
			}
			continue
		}
		if name == "warp-agent-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "baseten" || group.Targets[0].Model != "nvidia/Nemotron-120B-A12B" {
				t.Fatalf("example config warp-agent-smoke=%#v, want static Baseten Nemotron OpenAI Chat tool target", group)
			}
			continue
		}
		if name == "reasoning-bridge-smoke" {
			if group.Strategy != "failover" || len(group.Targets) != 3 {
				t.Fatalf("example config reasoning-bridge-smoke=%#v, want deterministic multi-skin failover smoke group", group)
			}
			if group.Targets[0].Provider != "minimax_responses" || !group.Targets[0].Bridges.ChatToResponses.Enabled {
				t.Fatalf("reasoning-bridge-smoke first target=%#v, want Chat-to-Responses bridge target first", group.Targets[0])
			}
			continue
		}
		if name == "responses-to-chat-bridge-smoke" {
			if group.Strategy != "static" || len(group.Targets) != 1 || group.Targets[0].Provider != "minimax" || !group.Targets[0].ResponsesToChat.Enabled {
				t.Fatalf("example config responses-to-chat-bridge-smoke=%#v, want isolated Responses-to-Chat bridge target", group)
			}
			continue
		}
		if name == "vision" {
			if group.Strategy != "weighted" || len(group.Targets) < 2 {
				t.Fatalf("example config vision=%#v, want weighted multi-target vision group", group)
			}
			seen := map[string]bool{}
			for _, target := range group.Targets {
				seen[target.Provider+":"+target.Model] = true
				if !stringSliceContains(target.InputModalities, "image") {
					t.Fatalf("example config vision target lacks image modality: %#v", target)
				}
			}
			if !seen["xai:grok-4.3"] || !seen["openai:gpt-5.4"] || !seen["openai:gpt-5.4-nano"] {
				t.Fatalf("example config vision targets=%#v, want xAI Grok 4.3, OpenAI GPT-5.4, and OpenAI GPT-5.4 Nano", group.Targets)
			}
			continue
		}
		if name == "external-policy-demo" {
			if group.Strategy != "external" ||
				group.ExternalPolicy.URL != "http://127.0.0.1:18090/route" ||
				!stringSliceContains(group.ExternalPolicy.AllowHosts, "127.0.0.1") ||
				group.ExternalPolicy.IncludeRequest ||
				len(group.Targets) != 2 {
				t.Fatalf("example config external-policy-demo=%#v, want prompt-size external policy demo", group)
			}
			continue
		}
		if name == "adaptive-agent" {
			if group.Strategy != "dynamic_score" || len(group.Targets) != 3 || len(group.RoutingPolicy.DynamicScore.ScoreTerms) != 2 {
				t.Fatalf("example config adaptive-agent=%#v, want dynamic_score reference group with three targets and score terms", group)
			}
			if group.RoutingPolicy.DynamicScore.MinObservations != 20 {
				t.Fatalf("adaptive-agent min_observations=%d, want 20", group.RoutingPolicy.DynamicScore.MinObservations)
			}
			continue
		}
		if group.Strategy != "weighted" {
			t.Fatalf("example config group %s strategy=%q want weighted", name, group.Strategy)
		}
		if name == "temp-coder" {
			assertTempCoderGroup(t, group)
			continue
		}
		if name == "big-coder" {
			assertReducedBigCoderGroup(t, cfg, group)
			assertBigCoderImageTargetPolicy(t, cfg, group)
			continue
		}
		assertActiveGroupPolicy(t, name, group)
	}
	wantAllows := map[string][]string{
		"standard-dev":      {"default", "fast", "small", "vision", "external-policy-demo"},
		"coding-dev":        {"default", "fast", "big-coder", "small", "medium", "high", "vision", "agent-tools-smoke", "claude-tools-smoke", "agent-tools-smoke-openrouter", "claude-tools-smoke-openrouter", "claude-tools-smoke-openrouter-gemma", "baseten-nemotron-smoke", "warp-agent-smoke", "baseten-glm52-smoke", "baseten-gpt-oss-120b-smoke", "fireworks-gpt-oss-20b-smoke", "large-openai-chat-tools-smoke", "fireworks-responses-smoke", "fireworks-responses-tool-smoke", "minimax-responses-smoke", "minimax-responses-tool-smoke", "baseten-gpt-oss-120b-claude-smoke", "crusoe-smoke", "crusoe-gemma-smoke", "crusoe-nemotron-omni-smoke", "openai-gpt54-vision-smoke", "reasoning-smoke", "reasoning-bridge-smoke", "responses-to-chat-bridge-smoke", "temp-coder"},
		"metrics-admin-dev": {},
		"content-admin-dev": {},
	}
	for _, caller := range cfg.Callers {
		want, ok := wantAllows[caller.ID]
		if !ok {
			continue
		}
		if strings.Join(caller.Allow, ",") != strings.Join(want, ",") {
			t.Fatalf("caller %s allow=%v want %v", caller.ID, caller.Allow, want)
		}
		if caller.ID == "metrics-admin-dev" && !caller.MetricsAdmin {
			t.Fatalf("caller %s metrics_admin=false, want true", caller.ID)
		}
		if caller.ID != "metrics-admin-dev" && caller.MetricsAdmin {
			t.Fatalf("caller %s metrics_admin=true, want false", caller.ID)
		}
		if caller.ID == "content-admin-dev" && !caller.ContentAdmin {
			t.Fatalf("caller %s content_admin=false, want true", caller.ID)
		}
		if caller.ID != "content-admin-dev" && caller.ContentAdmin {
			t.Fatalf("caller %s content_admin=true, want false", caller.ID)
		}
		delete(wantAllows, caller.ID)
	}
	if len(wantAllows) != 0 {
		t.Fatalf("example config missing caller profiles: %#v", wantAllows)
	}
}

func TestExampleConfigOpenAINanoResponsesReasoningMetadata(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	standardSum := sha256.Sum256([]byte("rtr_example_standard_test"))
	codingSum := sha256.Sum256([]byte("rtr_example_coding_test"))
	metricsAdminSum := sha256.Sum256([]byte("rtr_example_metrics_admin_test"))
	contentAdminSum := sha256.Sum256([]byte("rtr_example_content_admin_test"))
	text := strings.ReplaceAll(string(raw), "REPLACE_WITH_SHA256_HEX_OF_STANDARD_ROUTER_TOKEN", hex.EncodeToString(standardSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CODING_ROUTER_TOKEN", hex.EncodeToString(codingSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_METRICS_ADMIN_ROUTER_TOKEN", hex.EncodeToString(metricsAdminSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CONTENT_ADMIN_ROUTER_TOKEN", hex.EncodeToString(contentAdminSum[:]))
	text = strings.ReplaceAll(text, "script: scripts/router.ts", "script: ../../scripts/router.ts")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	model := cfg.Provider["openai"].Models["gpt-5.4-nano"]
	if !model.Reasoning.Supported || model.Reasoning.Mode != reasoningModeOptIn || model.Reasoning.Control != reasoningControlEffortEnum {
		t.Fatalf("openai gpt-5.4-nano reasoning metadata=%#v, want opt-in effort enum support", model.Reasoning)
	}
	if model.Reasoning.DefaultOn || model.Reasoning.SupportsSummaries || model.Reasoning.StreamBlock != "none" {
		t.Fatalf("openai gpt-5.4-nano reasoning details=%#v, want opt-in no summaries stream_block none", model.Reasoning)
	}
	if model.RequestShapeSupport.MinRequestedOutputTokens != 16 {
		t.Fatalf("openai gpt-5.4-nano min output cap=%d, want 16", model.RequestShapeSupport.MinRequestedOutputTokens)
	}
	found := false
	for _, target := range cfg.Models["big-coder"].Targets {
		if target.Provider == "openai" && target.Model == "gpt-5.4-nano" {
			found = true
			if !target.Reasoning.Supported || target.Reasoning.Control != reasoningControlEffortEnum {
				t.Fatalf("big-coder openai target reasoning=%#v, want resolved catalog metadata", target.Reasoning)
			}
			if target.RequestShapeSupport.MinRequestedOutputTokens != 16 {
				t.Fatalf("big-coder openai target min output cap=%d, want resolved catalog minimum 16", target.RequestShapeSupport.MinRequestedOutputTokens)
			}
		}
	}
	if !found {
		t.Fatalf("big-coder missing openai gpt-5.4-nano target")
	}
	found = false
	for _, target := range cfg.Models["big-coder"].Targets {
		if target.Provider == "openai" && target.Model == "gpt-5.4" {
			found = true
			if !stringSliceContains(target.InputModalities, "image") || !stringSliceContains(target.ToolSupport.OpenAIResponses, "function") || !stringSliceContains(target.RequestShapeSupport.RequiredInputModalities, "image") {
				t.Fatalf("big-coder gpt-5.4 image target metadata=%#v", target)
			}
		}
	}
	if !found {
		t.Fatalf("big-coder missing image-only OpenAI gpt-5.4 target")
	}
}

func TestExampleConfigPreservesAnthropicTextEligibilityInBroadGroups(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	standardSum := sha256.Sum256([]byte("rtr_example_standard_test"))
	codingSum := sha256.Sum256([]byte("rtr_example_coding_test"))
	metricsAdminSum := sha256.Sum256([]byte("rtr_example_metrics_admin_test"))
	contentAdminSum := sha256.Sum256([]byte("rtr_example_content_admin_test"))
	text := strings.ReplaceAll(string(raw), "REPLACE_WITH_SHA256_HEX_OF_STANDARD_ROUTER_TOKEN", hex.EncodeToString(standardSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CODING_ROUTER_TOKEN", hex.EncodeToString(codingSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_METRICS_ADMIN_ROUTER_TOKEN", hex.EncodeToString(metricsAdminSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CONTENT_ADMIN_ROUTER_TOKEN", hex.EncodeToString(contentAdminSum[:]))
	text = strings.ReplaceAll(text, "script: scripts/router.ts", "script: ../../scripts/router.ts")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, groupName := range []string{"default", "fast", "small", "medium", "high", "big-coder", "temp-coder"} {
		group := cfg.Models[groupName]
		found := false
		for _, target := range group.Targets {
			if target.ToolOnly {
				continue
			}
			provider := cfg.Provider[target.Provider]
			outDialect := targetDialect(provider, target)
			if normalizeDialect(outDialect) == "anthropic" {
				continue
			}
			if anthropicInboundDialectFilterReason(target, "anthropic", outDialect) == "" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s has no non-tool target eligible for Anthropic Messages text", groupName)
		}
	}
}

func TestExampleConfigLargeOpenAIChatToolsSmokeHasShapeGate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	standardSum := sha256.Sum256([]byte("rtr_example_standard_test"))
	codingSum := sha256.Sum256([]byte("rtr_example_coding_test"))
	metricsAdminSum := sha256.Sum256([]byte("rtr_example_metrics_admin_test"))
	contentAdminSum := sha256.Sum256([]byte("rtr_example_content_admin_test"))
	text := strings.ReplaceAll(string(raw), "REPLACE_WITH_SHA256_HEX_OF_STANDARD_ROUTER_TOKEN", hex.EncodeToString(standardSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CODING_ROUTER_TOKEN", hex.EncodeToString(codingSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_METRICS_ADMIN_ROUTER_TOKEN", hex.EncodeToString(metricsAdminSum[:]))
	text = strings.ReplaceAll(text, "REPLACE_WITH_SHA256_HEX_OF_CONTENT_ADMIN_ROUTER_TOKEN", hex.EncodeToString(contentAdminSum[:]))
	text = strings.ReplaceAll(text, "script: scripts/router.ts", "script: ../../scripts/router.ts")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	group := cfg.Models["large-openai-chat-tools-smoke"]
	if group.Strategy != "failover" || len(group.Targets) != 2 {
		t.Fatalf("large-openai-chat-tools-smoke=%#v, want failover with limited target plus fallback", group)
	}
	foundSmokeTarget := false
	for _, target := range group.Targets {
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/gpt-oss-20b" {
			foundSmokeTarget = true
			if target.RequestShapeSupport.MaxRequestBytes != 196608 {
				t.Fatalf("large-openai-chat-tools-smoke Fireworks GPT OSS max_request_bytes=%d, want 196608", target.RequestShapeSupport.MaxRequestBytes)
			}
			notes := target.RequestShapeSupport.ValidationNotes
			if target.RequestShapeSupport.ValidationStatus != "passed" ||
				!strings.Contains(notes, "production-derived large") ||
				!strings.Contains(notes, "Broad production coding groups are not changed") ||
				!strings.Contains(notes, "reusable scoped evaluation caller") {
				t.Fatalf("large-openai-chat-tools-smoke validation notes=%#v", target.RequestShapeSupport)
			}
			if group.Targets[1].Provider != "minimax" || group.Targets[1].Model != "MiniMax-M3" {
				t.Fatalf("large-openai-chat-tools-smoke fallback target=%#v, want MiniMax M3", group.Targets[1])
			}
		}
	}
	if !foundSmokeTarget {
		t.Fatalf("large-openai-chat-tools-smoke missing Fireworks GPT OSS target")
	}
	for _, target := range cfg.Models["big-coder"].Targets {
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/gpt-oss-20b" && target.RequestShapeSupport.MaxRequestBytes != 0 {
			t.Fatalf("big-coder Fireworks GPT OSS target should not carry the smoke-group request-shape cap: %#v", target.RequestShapeSupport)
		}
	}
	highGroup := cfg.Models["high"]
	wantCappedHighTargets := map[string]bool{
		"baseten:openai/gpt-oss-120b":                false,
		"baseten:nvidia/Nemotron-120B-A12B":          false,
		"baseten:zai-org/GLM-5.2":                    false,
		"openrouter:google/gemma-4-26b-a4b-it:nitro": false,
		"crusoe:zai/GLM-5.2":                         false,
		"kimi:kimi-k2.7-code":                        false,
		"openai:gpt-5.4-nano":                        false,
	}
	foundUncappedMiniMax := false
	for _, target := range highGroup.Targets {
		if target.ToolOnly {
			continue
		}
		key := target.Provider + ":" + target.Model
		if key == "minimax:MiniMax-M3" {
			foundUncappedMiniMax = true
			if target.RequestShapeSupport.MaxRequestBytes != 0 {
				t.Fatalf("high MiniMax M3 target should remain uncapped for gt-1mb OpenAI Chat tools: %#v", target.RequestShapeSupport)
			}
			continue
		}
		if _, ok := wantCappedHighTargets[key]; ok {
			wantCappedHighTargets[key] = true
			if target.RequestShapeSupport.MaxRequestBytes != 1048576 {
				t.Fatalf("high target %s max_request_bytes=%d, want 1048576", key, target.RequestShapeSupport.MaxRequestBytes)
			}
		}
	}
	if !foundUncappedMiniMax {
		t.Fatalf("high group missing uncapped MiniMax M3 ordinary target")
	}
	for key, found := range wantCappedHighTargets {
		if !found {
			t.Fatalf("high group missing capped ordinary target %s", key)
		}
	}
}

func assertDefaultGroupTargets(t *testing.T, defaultGroup ModelGroup) {
	t.Helper()
	want := map[string]string{
		"baseten:nvidia/Nemotron-120B-A12B":          "nvidia/Nemotron-120B-A12B",
		"baseten:openai/gpt-oss-120b":                "openai/gpt-oss-120b",
		"baseten:zai-org/GLM-5.2":                    "zai-org/GLM-5.2",
		"minimax:MiniMax-M3":                         "MiniMax-M3",
		"kimi:kimi-k2.7-code":                        "kimi-k2.7-code",
		"openrouter:google/gemma-4-26b-a4b-it:nitro": "google/gemma-4-26b-a4b-it:nitro",
		"openai:gpt-5.4-nano":                        "gpt-5.4-nano",
	}
	for name, model := range want {
		found := false
		for _, target := range defaultGroup.Targets {
			if target.Provider+":"+target.Model == name && target.Model == model {
				found = true
				if !target.ToolOnly && target.Weight <= 0 {
					t.Fatalf("%s target resolved with non-positive weight: %#v", name, target)
				}
				break
			}
		}
		if !found {
			t.Fatalf("default group missing resolved target %s; targets=%#v", name, defaultGroup.Targets)
		}
	}
}

func assertAnthropicCompatibleProvider(t *testing.T, provider ProviderConfig, ref, model string) {
	t.Helper()
	if provider.Dialect != "anthropic" || normalizeAuthScheme(provider.AuthScheme) != "bearer" || provider.Models[ref].Model != model {
		t.Fatalf("provider not configured for Anthropic-compatible bearer skin: %#v", provider)
	}
}

func assertTempCoderGroup(t *testing.T, group ModelGroup) {
	t.Helper()
	wantNormal := map[string]int{
		"openai:gpt-5.4-nano": 70,
		"minimax:MiniMax-M3":  15,
		"kimi:kimi-k2.7-code": 15,
	}
	wantToolOnly := map[string]int{
		"minimax_anthropic:MiniMax-M3":  50,
		"kimi_anthropic:kimi-k2.7-code": 50,
	}
	normalTotal := 0
	toolTotal := 0
	gotNormal := map[string]int{}
	gotToolOnly := map[string]int{}
	for _, target := range group.Targets {
		if target.Provider == "openrouter" || target.Provider == "openrouter_responses" || target.Provider == "openrouter_anthropic" {
			t.Fatalf("temp-coder target uses OpenRouter, want only direct providers: %#v", target)
		}
		key := target.Provider + ":" + target.Model
		if target.ToolOnly {
			gotToolOnly[key] = target.Weight
			toolTotal += target.Weight
		} else {
			gotNormal[key] = target.Weight
			normalTotal += target.Weight
		}
		if target.Provider == "kimi_anthropic" {
			if target.DefaultThinking["type"] != "enabled" || target.DefaultThinking["budget_tokens"] != 1024 {
				t.Fatalf("temp-coder kimi_anthropic target default_thinking=%#v, want enabled budget 1024", target.DefaultThinking)
			}
		}
	}
	if normalTotal != 100 || len(gotNormal) != len(wantNormal) {
		t.Fatalf("temp-coder normal weights=%#v total=%d, want %#v total=100", gotNormal, normalTotal, wantNormal)
	}
	for key, weight := range wantNormal {
		if gotNormal[key] != weight {
			t.Fatalf("temp-coder normal target %s weight=%d, want %d; all weights=%#v", key, gotNormal[key], weight, gotNormal)
		}
	}
	if toolTotal != 100 || len(gotToolOnly) != len(wantToolOnly) {
		t.Fatalf("temp-coder tool-only weights=%#v total=%d, want %#v total=100", gotToolOnly, toolTotal, wantToolOnly)
	}
	for key, weight := range wantToolOnly {
		if gotToolOnly[key] != weight {
			t.Fatalf("temp-coder tool-only target %s weight=%d, want %d; all weights=%#v", key, gotToolOnly[key], weight, gotToolOnly)
		}
	}
}

func assertReducedBigCoderGroup(t *testing.T, cfg *Config, group ModelGroup) {
	t.Helper()
	wantNormal := map[string]int{
		"fireworks:accounts/fireworks/models/gpt-oss-20b":       25,
		"minimax_responses:MiniMax-M3":                          25,
		"xai:grok-4.5":                                          15,
		"fireworks:accounts/fireworks/models/deepseek-v4-flash": 15,
		"minimax:MiniMax-M3":                                    5,
		"kimi:kimi-k2.7-code":                                   5,
		"crusoe:zai/GLM-5.2":                                    5,
		"openai:gpt-5.4-nano":                                   5,
	}
	wantToolOnly := map[string]int{
		"fireworks_responses:accounts/fireworks/models/kimi-k2p7-code": 33,
		"minimax_anthropic:MiniMax-M3":                                 34,
		"kimi_anthropic:kimi-k2.7-code":                                33,
	}
	normalTotal := 0
	gotNormal := map[string]int{}
	gotToolOnly := map[string]int{}
	gotImageOnly := map[string]int{}
	chatToolCapable := map[string]bool{}
	chatReasoningCapable := map[string]bool{}
	responsesReasoningCapable := map[string]bool{}
	responsesEligible := map[string]bool{}
	for _, target := range group.Targets {
		key := target.Provider + ":" + target.Model
		if stringSliceContains(target.RequestShapeSupport.RequiredInputModalities, "image") {
			gotImageOnly[key] = target.Weight
		} else if target.ToolOnly {
			gotToolOnly[key] = target.Weight
		} else {
			gotNormal[key] = target.Weight
			normalTotal += target.Weight
			provider := cfg.Provider[target.Provider]
			model := provider.Models[target.ModelRef]
			if target.Dialect == "openai-chat" || (target.Dialect == "" && provider.Dialect == "openai-chat") {
				if supportsAnyCapability(model.ToolSupport.OpenAIChat, "tools", "function", "functions", "function_tools", "tool_choice", "forced_tool_choice") {
					chatToolCapable[key] = true
				}
				if model.Reasoning.Supported {
					chatReasoningCapable[key] = true
				}
			}
			if targetDialect(provider, target) == "openai-responses" {
				responsesEligible[key] = true
				if model.Reasoning.Supported {
					responsesReasoningCapable[key] = true
				}
			}
		}
		if target.Provider == "kimi_anthropic" {
			if !target.ToolOnly {
				t.Fatalf("big-coder kimi_anthropic target tool_only=false, want true while hosted image requires default thinking for Kimi")
			}
			if target.DefaultThinking["type"] != "enabled" || target.DefaultThinking["budget_tokens"] != 1024 {
				t.Fatalf("big-coder kimi_anthropic target default_thinking=%#v, want enabled budget 1024", target.DefaultThinking)
			}
		}
	}
	if normalTotal != 100 || len(gotNormal) != len(wantNormal) {
		t.Fatalf("big-coder normal weights=%#v total=%d, want %#v total=100", gotNormal, normalTotal, wantNormal)
	}
	wantImageOnly := map[string]int{
		"openai:gpt-5.4": 1,
		"openrouter_responses:anthropic/claude-sonnet-4.6": 1,
	}
	if len(gotImageOnly) != len(wantImageOnly) {
		t.Fatalf("big-coder image-only targets=%#v, want %#v", gotImageOnly, wantImageOnly)
	}
	for key, weight := range wantImageOnly {
		if gotImageOnly[key] != weight {
			t.Fatalf("big-coder image-only target %s weight=%d, want %d; all image-only weights=%#v", key, gotImageOnly[key], weight, gotImageOnly)
		}
	}
	for key, weight := range wantNormal {
		if gotNormal[key] != weight {
			t.Fatalf("big-coder normal target %s weight=%d, want %d; all weights=%#v", key, gotNormal[key], weight, gotNormal)
		}
	}
	if len(chatToolCapable) < 3 {
		t.Fatalf("big-coder OpenAI Chat tool-capable normal targets=%#v, want at least three independent targets", chatToolCapable)
	}
	if !chatToolCapable["fireworks:accounts/fireworks/models/deepseek-v4-flash"] || !chatToolCapable["minimax:MiniMax-M3"] || !chatToolCapable["kimi:kimi-k2.7-code"] {
		t.Fatalf("big-coder Chat tool targets=%#v, want Fireworks DeepSeek, MiniMax M3, and Kimi K2.7 Code", chatToolCapable)
	}
	if len(responsesEligible) < 2 || !responsesEligible["openai:gpt-5.4-nano"] || !responsesEligible["minimax_responses:MiniMax-M3"] {
		t.Fatalf("big-coder Responses-eligible normal targets=%#v, want OpenAI fallback plus MiniMax Responses", responsesEligible)
	}
	if !chatReasoningCapable["fireworks:accounts/fireworks/models/gpt-oss-20b"] {
		t.Fatalf("big-coder Chat reasoning targets=%#v, want Fireworks GPT OSS 20B", chatReasoningCapable)
	}
	if !responsesReasoningCapable["minimax_responses:MiniMax-M3"] || !responsesReasoningCapable["openai:gpt-5.4-nano"] {
		t.Fatalf("big-coder Responses reasoning targets=%#v, want MiniMax Responses M3 and OpenAI gpt-5.4-nano (#1056 Codex Responses+reasoning coverage)", responsesReasoningCapable)
	}
	if len(gotToolOnly) != len(wantToolOnly) {
		t.Fatalf("big-coder tool-only weights=%#v, want %#v", gotToolOnly, wantToolOnly)
	}
	for key, weight := range wantToolOnly {
		if gotToolOnly[key] != weight {
			t.Fatalf("big-coder tool-only target %s weight=%d, want %d; all weights=%#v", key, gotToolOnly[key], weight, gotToolOnly)
		}
	}
}

func assertBigCoderImageTargetPolicy(t *testing.T, cfg *Config, group ModelGroup) {
	t.Helper()
	type imageTargetPolicy struct {
		weight                   int
		toolOnly                 bool
		supportedInboundDialects string
	}
	want := map[string]imageTargetPolicy{
		"openai:gpt-5.4": {
			weight:                   1,
			supportedInboundDialects: "openai-responses",
		},
		"openrouter_responses:anthropic/claude-sonnet-4.6": {
			weight:   1,
			toolOnly: true,
		},
	}
	imageTargets := make([]Target, 0, len(want))
	for _, target := range group.Targets {
		if stringSliceContains(target.RequestShapeSupport.RequiredInputModalities, "image") {
			imageTargets = append(imageTargets, target)
		}
	}
	if len(imageTargets) != len(want) {
		t.Fatalf("big-coder image-gated target entries=%#v, want exactly %d entries", imageTargets, len(want))
	}
	got := make(map[string]Target, len(imageTargets))
	for _, target := range imageTargets {
		key := target.Provider + ":" + target.Model
		if _, exists := got[key]; exists {
			t.Fatalf("big-coder duplicate image-gated target %s; entries=%#v", key, imageTargets)
		}
		got[key] = target
	}

	textReq := &IRRequest{Model: "big-coder", Messages: []IRMessage{{Role: "user", Content: "hello"}}}
	imageReq := &IRRequest{Model: "big-coder", InputParts: []IRContentPart{{Type: "image", ImageURL: "https://example.test/receipt.png"}}}
	svc := &Service{cfg: cfg}
	for key, policy := range want {
		target, ok := got[key]
		if !ok {
			t.Fatalf("big-coder missing image-gated target %s; got %#v", key, got)
		}
		if target.Weight != policy.weight || target.ToolOnly != policy.toolOnly ||
			!stringSliceContains(target.InputModalities, "image") ||
			strings.Join(target.RequestShapeSupport.RequiredInputModalities, ",") != "image" ||
			strings.Join(target.RequestShapeSupport.SupportedInboundDialects, ",") != policy.supportedInboundDialects ||
			targetDialect(cfg.Provider[target.Provider], target) != "openai-responses" {
			t.Fatalf("big-coder image-gated target %s policy=%#v, want weight=%d tool_only=%t required_input_modalities=image supported_inbound_dialects=%q openai-responses output", key, target, policy.weight, policy.toolOnly, policy.supportedInboundDialects)
		}

		outDialect := targetDialect(cfg.Provider[target.Provider], target)
		textFit := svc.targetRequestShapeFit(target, textReq, "openai-responses", outDialect, requestTokenEstimateFromIR(textReq, "openai-responses", 96))
		if textFit.FilterReason != "request-shape-required-input-modality" {
			t.Fatalf("big-coder image-gated target %s allowed ordinary text: %#v", key, textFit)
		}
		imageFit := svc.targetRequestShapeFit(target, imageReq, "openai-responses", outDialect, requestTokenEstimateFromIR(imageReq, "openai-responses", 96))
		if imageFit.FilterReason != "" || imageFit.EligibilityDecision != "eligible" {
			t.Fatalf("big-coder image-gated target %s rejected image request: %#v", key, imageFit)
		}
	}

	selectedKeys := func(targets []Target) map[string]bool {
		keys := make(map[string]bool, len(targets))
		for _, target := range targets {
			keys[target.Provider+":"+target.Model] = true
		}
		return keys
	}
	withoutTools := selectedKeys(svc.targetsForRequest(nil, group.Targets, imageReq, "openai-responses"))
	if !withoutTools["openai:gpt-5.4"] || withoutTools["openrouter_responses:anthropic/claude-sonnet-4.6"] {
		t.Fatalf("big-coder image-without-tools selection=%#v, want OpenAI image target and no tool-only Claude target", withoutTools)
	}
	imageWithFunction := &IRRequest{
		Model:      "big-coder",
		InputParts: []IRContentPart{{Type: "image", ImageURL: "https://example.test/receipt.png"}},
		Tools:      []map[string]any{{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}},
	}
	withTools := selectedKeys(svc.targetsForRequest(nil, group.Targets, imageWithFunction, "openai-responses"))
	for _, key := range []string{"openai:gpt-5.4", "openrouter_responses:anthropic/claude-sonnet-4.6"} {
		if !withTools[key] {
			t.Fatalf("big-coder image-plus-function selection=%#v, missing %s", withTools, key)
		}
	}
}

func assertOpenAIChatProvider(t *testing.T, provider ProviderConfig, ref, model string) {
	t.Helper()
	if provider.Dialect != "openai-chat" || normalizeAuthScheme(provider.AuthScheme) != "bearer" || provider.Models[ref].Model != model {
		t.Fatalf("provider not configured for OpenAI Chat-compatible bearer skin: %#v", provider)
	}
}

func assertResponsesCompatibleProvider(t *testing.T, provider ProviderConfig, ref, model string) {
	t.Helper()
	if provider.Dialect != "openai-responses" || provider.Models[ref].Model != model {
		t.Fatalf("provider not configured for OpenAI Responses-compatible skin: %#v", provider)
	}
}

func assertActiveGroupPolicy(t *testing.T, name string, group ModelGroup) {
	t.Helper()
	totalWeight := 0
	m3Weight := 0
	kimiWeight := 0
	gemmaWeight := 0
	openAIWeight := 0
	basetenNemotronWeight := 0
	basetenGLMWeight := 0
	basetenGPTOSSWeight := 0
	crusoeGemmaWeight := 0
	crusoeGLMWeight := 0
	crusoeNemotronWeight := 0
	fireworksGPTOSS20BWeight := 0
	fireworksGLM52Weight := 0
	fireworksKimiWeight := 0
	fireworksDeepSeekWeight := 0
	fireworksQwenWeight := 0
	normalTargets := 0
	codexToolTarget := false
	codexOpenRouterMiniMaxToolTarget := false
	codexFireworksToolTarget := false
	claudeBasetenToolTarget := false
	claudeMiniMaxToolTarget := false
	claudeKimiToolTarget := false
	claudeOpenRouterMiniMaxToolTarget := false
	claudeGemmaToolTarget := false
	for _, target := range group.Targets {
		if violatesCurrentRoutingPolicy(target) {
			t.Fatalf("example config group %s has routing-policy violation %#v", name, target)
		}
		if target.ToolOnly {
			if target.Provider == "minimax_responses" && target.Model == "MiniMax-M3" {
				codexToolTarget = true
			}
			if target.Provider == "openrouter_responses" && target.Model == "minimax/minimax-m3" {
				codexOpenRouterMiniMaxToolTarget = true
			}
			if target.Provider == "fireworks_responses" && target.Model == "accounts/fireworks/models/kimi-k2p7-code" && target.ForceStoreFalse {
				codexFireworksToolTarget = true
			}
			if target.Provider == "minimax_anthropic" && target.Model == "MiniMax-M3" {
				claudeMiniMaxToolTarget = true
			}
			if target.Provider == "baseten_anthropic" && target.Model == "openai/gpt-oss-120b" {
				claudeBasetenToolTarget = true
			}
			if target.Provider == "kimi_anthropic" && target.Model == "kimi-k2.7-code" && target.DefaultThinking["type"] == "enabled" {
				claudeKimiToolTarget = true
			}
			if target.Provider == "openrouter_anthropic" && target.Model == "minimax/minimax-m3" {
				claudeOpenRouterMiniMaxToolTarget = true
			}
			if target.Provider == "openrouter_anthropic" && target.Model == "google/gemma-4-26b-a4b-it:nitro" {
				claudeGemmaToolTarget = true
			}
			continue
		}
		normalTargets++
		totalWeight += target.Weight
		if target.Provider == "minimax" && target.Model == "MiniMax-M3" {
			m3Weight += target.Weight
		}
		if target.Provider == "kimi" && target.Model == "kimi-k2.7-code" {
			kimiWeight += target.Weight
		}
		if target.Provider == "openrouter" && target.Model == "google/gemma-4-26b-a4b-it:nitro" {
			gemmaWeight += target.Weight
		}
		if target.Provider == "openai" && target.Model == "gpt-5.4-nano" {
			openAIWeight += target.Weight
		}
		if target.Provider == "baseten" && target.Model == "nvidia/Nemotron-120B-A12B" {
			basetenNemotronWeight += target.Weight
		}
		if target.Provider == "baseten" && target.Model == "zai-org/GLM-5.2" {
			basetenGLMWeight += target.Weight
		}
		if target.Provider == "baseten" && target.Model == "openai/gpt-oss-120b" {
			basetenGPTOSSWeight += target.Weight
		}
		if target.Provider == "crusoe" && target.Model == "google/gemma-4-31b-it" {
			crusoeGemmaWeight += target.Weight
		}
		if target.Provider == "crusoe" && target.Model == "zai/GLM-5.2" {
			crusoeGLMWeight += target.Weight
		}
		if target.Provider == "crusoe" && target.Model == "nvidia/Nemotron-3-Nano-Omni-Reasoning-30B-A3B" {
			crusoeNemotronWeight += target.Weight
		}
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/gpt-oss-20b" {
			fireworksGPTOSS20BWeight += target.Weight
		}
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/glm-5p2" {
			fireworksGLM52Weight += target.Weight
		}
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/kimi-k2p7-code" {
			fireworksKimiWeight += target.Weight
		}
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/deepseek-v4-flash" {
			fireworksDeepSeekWeight += target.Weight
		}
		if target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/qwen3p6-plus" {
			fireworksQwenWeight += target.Weight
		}
	}
	if !codexToolTarget || !codexOpenRouterMiniMaxToolTarget || (name == "big-coder" && !codexFireworksToolTarget) || !claudeMiniMaxToolTarget || !claudeBasetenToolTarget || !claudeKimiToolTarget || !claudeOpenRouterMiniMaxToolTarget || !claudeGemmaToolTarget {
		t.Fatalf("example config group %s missing tool-only targets codex=%v codex_openrouter_minimax=%v codex_fireworks=%v minimax=%v baseten=%v kimi=%v claude_openrouter_minimax=%v claude_gemma=%v", name, codexToolTarget, codexOpenRouterMiniMaxToolTarget, codexFireworksToolTarget, claudeMiniMaxToolTarget, claudeBasetenToolTarget, claudeKimiToolTarget, claudeOpenRouterMiniMaxToolTarget, claudeGemmaToolTarget)
	}
	if name != "big-coder" {
		anthropicText := false
		for _, target := range group.Targets {
			if !target.ToolOnly && target.Provider == "minimax_anthropic" && target.Model == "MiniMax-M3" {
				anthropicText = true
				break
			}
		}
		if !anthropicText {
			t.Fatalf("example config group %s has no plain-text Anthropic MiniMax M3 target; Anthropic Messages without tools would return no-eligible-target", name)
		}
	}
	want := map[string]struct {
		gptOSS, m3, gemma, kimi, openAI, basetenNemotron, basetenGLM, crusoeGemma, crusoeGLM, crusoeNemotron, fireworksGPTOSS20B, fireworksGLM52, fireworksKimi, fireworksDeepSeek, fireworksQwen, xaiGrok45, targets int
	}{
		"default":   {51, 24, 2, 6, 1, 3, 5, 0, 5, 0, 0, 0, 0, 0, 0, 0, 9},
		"fast":      {56, 23, 2, 5, 1, 3, 5, 0, 2, 0, 0, 0, 0, 0, 0, 0, 9},
		"small":     {58, 25, 2, 4, 1, 3, 2, 0, 2, 0, 0, 0, 0, 0, 0, 0, 9},
		"medium":    {51, 22, 2, 8, 1, 3, 5, 0, 5, 0, 0, 0, 0, 0, 0, 0, 9},
		"high":      {45, 23, 2, 10, 1, 3, 6, 0, 7, 0, 0, 0, 0, 0, 0, 0, 9},
		"big-coder": {0, 20, 0, 15, 10, 0, 0, 0, 15, 0, 0, 0, 0, 25, 0, 15, 6},
	}
	expect, ok := want[name]
	if !ok {
		t.Fatalf("example config group %s has no expected weight policy", name)
	}
	xaiGrok45Weight := 0
	for _, target := range group.Targets {
		if !target.ToolOnly && target.Provider == "xai" && target.Model == "grok-4.5" {
			xaiGrok45Weight += target.Weight
		}
	}
	if totalWeight != 100 || normalTargets != expect.targets || basetenGPTOSSWeight != expect.gptOSS || m3Weight != expect.m3 || gemmaWeight != expect.gemma || kimiWeight != expect.kimi || openAIWeight != expect.openAI || basetenNemotronWeight != expect.basetenNemotron || basetenGLMWeight != expect.basetenGLM || crusoeGemmaWeight != expect.crusoeGemma || crusoeGLMWeight != expect.crusoeGLM || crusoeNemotronWeight != expect.crusoeNemotron || fireworksGPTOSS20BWeight != expect.fireworksGPTOSS20B || fireworksGLM52Weight != expect.fireworksGLM52 || fireworksKimiWeight != expect.fireworksKimi || fireworksDeepSeekWeight != expect.fireworksDeepSeek || fireworksQwenWeight != expect.fireworksQwen || xaiGrok45Weight != expect.xaiGrok45 {
		t.Fatalf("example config group %s weights gpt_oss=%d m3=%d gemma=%d kimi=%d openai=%d baseten_nemotron=%d baseten_glm=%d crusoe_gemma=%d crusoe_glm=%d crusoe_nemotron=%d fireworks_gpt_oss_20b=%d fireworks_glm52=%d fireworks_kimi=%d fireworks_deepseek=%d fireworks_qwen=%d xai_grok_45=%d total=%d normal_targets=%d, want %#v", name, basetenGPTOSSWeight, m3Weight, gemmaWeight, kimiWeight, openAIWeight, basetenNemotronWeight, basetenGLMWeight, crusoeGemmaWeight, crusoeGLMWeight, crusoeNemotronWeight, fireworksGPTOSS20BWeight, fireworksGLM52Weight, fireworksKimiWeight, fireworksDeepSeekWeight, fireworksQwenWeight, xaiGrok45Weight, totalWeight, normalTargets, expect)
	}
}

func violatesCurrentRoutingPolicy(target Target) bool {
	needle := strings.ToLower(target.Provider + "/" + target.Model + "/" + target.ModelRef)
	if target.ToolOnly && stringSliceContains(target.InputModalities, "image") && (target.Provider == "openrouter_responses" || target.Provider == "openrouter_anthropic") {
		switch target.ModelRef {
		case "qwen3-7-plus-nitro", "openrouter-claude-sonnet-4-6", "openrouter-xai-grok-4-3", "openrouter-minimax-m3":
			return false
		}
	}
	if strings.Contains(needle, "moonshotai/kimi") {
		return true
	}
	allowedBasetenNemotron := target.Provider == "baseten" && target.Model == "nvidia/Nemotron-120B-A12B"
	allowedBasetenGLM := target.Provider == "baseten" && target.Model == "zai-org/GLM-5.2"
	allowedBasetenGPTOSS := target.Provider == "baseten" && target.Model == "openai/gpt-oss-120b"
	allowedCrusoeGLM := target.Provider == "crusoe" && target.Model == "zai/GLM-5.2"
	allowedCrusoeNemotron := target.Provider == "crusoe" && target.Model == "nvidia/Nemotron-3-Nano-Omni-Reasoning-30B-A3B"
	allowedFireworksGLM := target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/glm-5p2"
	allowedFireworksQwen := target.Provider == "fireworks" && target.Model == "accounts/fireworks/models/qwen3p6-plus"
	for _, bad := range []string{"qwen", "glm", "hy3", "kat-coder", "nemotron", "mercury", "ling-2.6", "pareto", "m2.7-highspeed"} {
		if strings.Contains(needle, bad) {
			return !allowedBasetenNemotron && !allowedBasetenGLM && !allowedBasetenGPTOSS && !allowedCrusoeGLM && !allowedCrusoeNemotron && !allowedFireworksGLM && !allowedFireworksQwen
		}
	}
	if target.Provider == "openrouter" && strings.Contains(strings.ToLower(target.Model), "deepseek/") {
		return true
	}
	if target.ToolOnly && (target.Provider == "openai" || target.Provider == "anthropic") {
		return true
	}
	if (target.Provider == "openai" || target.Provider == "anthropic") && target.Weight > 1 {
		return true
	}
	return false
}

func TestValidateDynamicScoreRejectsUnsafeBounds(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MaxScoreAdjustmentPercent: 101,
		}},
		Targets: []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_score_adjustment_percent") {
		t.Fatalf("Validate() err=%v, want max_score_adjustment_percent error", err)
	}

	cfg = minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			Signals: DynamicScoreSignals{
				PromptFeatures: DynamicSignalPromptFeatures{Enabled: true, MaxScanBytes: 70000},
			},
		}},
		Targets: []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_scan_bytes") {
		t.Fatalf("Validate() err=%v, want max_scan_bytes error", err)
	}
}

func TestValidateExternalPolicyHTTPRequiresOptInForNonLocalHost(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        "http://routing-policy.internal.example/route",
			AllowHosts: []string{"routing-policy.internal.example"},
		},
		Targets: []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "allow_http") {
		t.Fatalf("Validate() err=%v, want allow_http error", err)
	}

	cfg.Models["default"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        "http://routing-policy.internal.example/route",
			AllowHosts: []string{"routing-policy.internal.example"},
			AllowHTTP:  true,
		},
		Targets: []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with allow_http=true: %v", err)
	}

	cfg.Models["default"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        "http://127.0.0.1:18090/route",
			AllowHosts: []string{"127.0.0.1"},
		},
		Targets: []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() for trusted-local http: %v", err)
	}
}

func TestValidateExternalPolicyIncludeRequestRequiresExternalStrategy(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy:       "weighted",
		ExternalPolicy: ExternalPolicyConfig{IncludeRequest: true},
		Targets:        []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "configures external_policy but does not use external strategy") {
		t.Fatalf("Validate() err=%v, want external_policy strategy error", err)
	}
}

func TestTargetRegionYAMLRoundTripAndValidation(t *testing.T) {
	original := Target{Provider: "mock", Model: "mock-model", Region: "eu-west/customer-a"}
	raw, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped Target
	if err := yaml.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.Region != original.Region {
		t.Fatalf("target region round trip=%q, want %q", roundTripped.Region, original.Region)
	}

	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{roundTripped}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid target region rejected: %v", err)
	}
	if got := cfg.Models["default"].Targets[0].Region; got != original.Region {
		t.Fatalf("resolved target region=%q, want %q", got, original.Region)
	}

	for _, invalid := range []string{" leading", "trailing ", strings.Repeat("a", 65), "eu west", "eu\nwest"} {
		t.Run(regexp.QuoteMeta(invalid), func(t *testing.T) {
			candidate := minimalConfig(t)
			candidate.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model", Region: invalid}}}
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "invalid region") {
				t.Fatalf("Validate() region=%q error=%v", invalid, err)
			}
		})
	}
}

func TestTargetRegionPublicDocsHaveTypeAndAvoidLegalGuarantees(t *testing.T) {
	for _, path := range []string{
		"../../docs-site/docs/reference/model-metadata.md",
		"../../docs-site/docs/privacy.md",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(raw))
		if !strings.Contains(text, "doc_type:") || !strings.Contains(text, "region") {
			t.Fatalf("%s must contain doc_type frontmatter and target-region guidance", path)
		}
		for _, claim := range []string{"guaranteed data residency", "never trains", "zero retention guarantee"} {
			if strings.Contains(text, claim) {
				t.Fatalf("%s contains unsupported legal claim %q", path, claim)
			}
		}
	}
}

func TestExternalPolicyModeValidationAndBackwardCompatibleDefault(t *testing.T) {
	for _, mode := range []string{"", "enforce", "shadow", "baseline"} {
		t.Run(defaultString(mode, "omitted"), func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.Models["default"] = ModelGroup{
				Strategy: "external",
				ExternalPolicy: ExternalPolicyConfig{
					URL: "http://127.0.0.1:18090/route", AllowHosts: []string{"127.0.0.1"}, Mode: mode,
				},
				Targets: []Target{{Provider: "mock", Model: "mock-model"}},
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() mode=%q error=%v", mode, err)
			}
		})
	}
	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy:       "external",
		ExternalPolicy: ExternalPolicyConfig{URL: "http://127.0.0.1:18090/route", AllowHosts: []string{"127.0.0.1"}, Mode: "promote"},
		Targets:        []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mode must be enforce, shadow, or baseline") {
		t.Fatalf("Validate() invalid external mode error=%v", err)
	}
}

func TestIntelligentRoutingConfigValidation(t *testing.T) {
	cfg := minimalConfig(t)
	provider := cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{"selector": {Model: "selector-model", Dialect: "openai-chat"}}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{
		Strategy: "intelligent",
		IntelligentRouting: IntelligentRoutingConfig{
			Mode:          "shadow",
			DecisionModel: IntelligentDecisionModel{Provider: "mock", ModelRef: "selector"},
			TimeoutMS:     250, MaxOutputTokens: 128, MaxConcurrent: 4, MaxDecisionCostUSD: 0.01,
			ConfidenceThreshold: 0.7, ContextMode: "scalar_only", OnError: "fallback", SchemaVersion: "v1",
		},
		Targets: []Target{{Provider: "mock", Model: "serving-model"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, mutate := range map[string]func(*IntelligentRoutingConfig){
		"unknown model":     func(policy *IntelligentRoutingConfig) { policy.DecisionModel.ModelRef = "missing" },
		"unsafe context":    func(policy *IntelligentRoutingConfig) { policy.ContextMode = "request_body" },
		"unbounded timeout": func(policy *IntelligentRoutingConfig) { policy.TimeoutMS = 5001 },
		"bad confidence":    func(policy *IntelligentRoutingConfig) { policy.ConfidenceThreshold = 1.1 },
		"missing fallback":  func(policy *IntelligentRoutingConfig) { policy.OnError = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := minimalConfig(t)
			candidateProvider := candidate.Provider["mock"]
			candidateProvider.Models = map[string]ProviderModel{"selector": {Model: "selector-model", Dialect: "openai-chat"}}
			candidate.Provider["mock"] = candidateProvider
			group := cfg.Models["default"]
			mutate(&group.IntelligentRouting)
			candidate.Models["default"] = group
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestIntelligentRoutingConfigRequiresStrategy(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy:           "static",
		IntelligentRouting: IntelligentRoutingConfig{Mode: "shadow"},
		Targets:            []Target{{Provider: "mock", Model: "mock-model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "configures intelligent_routing but does not use intelligent strategy") {
		t.Fatalf("Validate() err=%v, want intelligent strategy error", err)
	}
}

func minimalConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			Cache: CacheConfig{Enabled: true},
			// Test fixtures explicitly opt into the bounded single-node path; the
			// shipped configuration default remains deployment-job validation.
			UsageDB: UsageDBConfig{MigrationPolicy: usageDBMigrationPolicyAutoSafe},
			Logging: LoggingConfig{
				Path: filepath.Join(t.TempDir(), "requests.jsonl"),
			},
		},
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Provider: map[string]ProviderConfig{
			"mock": {BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "secret", APIKeyEnv: "MOCK_API_KEY", KeyID: "mock-key"},
		},
		Models: map[string]ModelGroup{
			"default": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model"}}},
		},
		Callers: []CallerConfig{{
			ID:          "alice",
			TokenSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Allow:       []string{"default"},
		}},
	}
}

func mustBcryptHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}
