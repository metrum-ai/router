// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

type usageStore struct {
	db *gorm.DB
}

// migrationStatus is a safe, read-only view of the checked-in usage migration
// ledger for admin and metrics surfaces. It deliberately returns no database
// connection details or application records.
func (s *usageStore) migrationStatus() (MigrationStatus, error) {
	r, err := newUsageMigrationRunner(s.db)
	if err != nil {
		return MigrationStatus{}, err
	}
	return r.Status()
}

const (
	// usageDBMigrationPolicyValidate verifies a fully applied immutable ledger
	// and never applies application-schema DDL during serving startup.
	usageDBMigrationPolicyValidate = "validate"
	// usageDBMigrationPolicyAutoSafe applies only checked-in transactional online
	// migrations for an explicitly reviewed small/single-node deployment.
	usageDBMigrationPolicyAutoSafe = "auto-safe"
	// usageDBMigrationPolicyDeploymentJob verifies a current ledger while a
	// non-serving deployment job owns all migration application.
	usageDBMigrationPolicyDeploymentJob = "deployment-job"
)

func validUsageDBMigrationPolicy(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case usageDBMigrationPolicyValidate, usageDBMigrationPolicyAutoSafe, usageDBMigrationPolicyDeploymentJob:
		return true
	default:
		return false
	}
}

type UsageReportOptions struct {
	Driver             string
	DBPath             string
	DSN                string
	MigrationPolicy    string
	LogPath            string
	From               time.Time
	To                 time.Time
	CallerID           string
	CallerIP           string
	TokenID            string
	TokenIDPrefix      string
	CallerUser         string
	CallerProject      string
	CallerEnvironment  string
	RequestedModel     string
	ResolvedGroup      string
	TargetProvider     string
	TargetModel        string
	TargetDialect      string
	Status             int
	Cache              string
	Client             string
	TrafficShapedOnly  bool
	TrafficShapeBucket string
	TrafficShapeScope  string
	InboundDialect     string
	StreamOnly         *bool
	ToolChoiceMode     string
	ToolCountBucket    string
	RequestBytesBucket string
	InputTokensBucket  string
	OutputCapBucket    string
	ReasoningPresent   *bool
	MultimodalOnly     bool
	RequestShapeFP     string
	ToolSchemaFP       string
	ErrorClass         string
}

// ReasoningCoverageExport is an aggregate-only view for protected evaluation
// reporting. It intentionally contains no request identifiers, callers,
// prompts, response content, headers, credentials, or endpoint details.
type ReasoningCoverageExport struct {
	ReasoningTokens                 int                    `json:"reasoning_tokens"`
	ReasoningAttemptCount           int                    `json:"reasoning_attempt_count"`
	ReasoningSuccessfulAttemptCount int                    `json:"reasoning_successful_attempt_count"`
	ReasoningReportedAttemptCount   int                    `json:"reasoning_reported_attempt_count"`
	Coverage                        []ReasoningCoverageRow `json:"reasoning_provider_model_dialect_coverage"`
	CoverageComplete                bool                   `json:"reasoning_provider_model_dialect_coverage_complete"`
}

// ReasoningCoverageRow is a provider/model/dialect aggregate for an evaluation
// window. Reasoning tokens are a subset of output tokens, not an additive total.
type ReasoningCoverageRow struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Dialect          string `json:"dialect"`
	Attempts         int    `json:"attempts"`
	ReportedAttempts int    `json:"reported_attempts"`
	ReasoningTokens  int    `json:"reasoning_tokens"`
}

type UsageRollupOptions struct {
	Driver                           string
	DBPath                           string
	DSN                              string
	RollupType                       string
	From                             time.Time
	To                               time.Time
	Finalize                         bool
	BaselineID                       string
	BaselineName                     string
	BaselineVersion                  string
	BaselineInputPricePerMillionUSD  float64
	BaselineOutputPricePerMillionUSD float64
}

type upstreamShapeJoinedEvent struct {
	Event requestUpstreamShapeEventRecord
	Row   usageRow
}

type UsageRollupResult struct {
	RunID              uint
	RollupType         string
	Status             string
	WindowStart        time.Time
	WindowEnd          time.Time
	SourceRequestCount int64
	RollupRows         int64
	DailyRows          int64
	DecisionBucketRows int64
	SourceChecksum     string
}

type RetentionStatusOptions struct {
	Driver      string
	DBPath      string
	DSN         string
	Config      RetentionConfig
	Now         time.Time
	RequestedBy string
}

type LegalHoldCreateOptions struct {
	Driver     string
	DBPath     string
	DSN        string
	HoldID     string
	DataClass  string
	RequestID  string
	Start      time.Time
	End        time.Time
	ReasonCode string
	Subject    string
	Actor      string
	Notes      string
	Now        time.Time
}

type LegalHoldReleaseOptions struct {
	Driver        string
	DBPath        string
	DSN           string
	HoldID        string
	Actor         string
	ReleaseReason string
	Now           time.Time
}

type LegalHoldResult struct {
	HoldID     string
	DataClass  string
	RequestID  string
	Active     bool
	Start      time.Time
	End        time.Time
	CreatedAt  time.Time
	ReleasedAt time.Time
}

type RetentionStatusResult struct {
	JobID           uint
	PolicyVersionID uint
	Status          string
	StartedAt       time.Time
	CompletedAt     time.Time
	TableResults    []RetentionTableStatus
}

type RetentionTableStatus struct {
	DataClass     string
	TableName     string
	Cutoff        time.Time
	RetentionDays int
	BatchSize     int
	CandidateRows int64
	HeldRows      int64
	EligibleRows  int64
	BlockedRows   int64
	DeletedRows   int64
	Status        string
	Message       string
}

type SecurityReportOptions struct {
	From              time.Time
	To                time.Time
	Limit             int
	Outcome           string
	ReasonCode        string
	Surface           string
	IPAddress         string
	CallerID          string
	CallerUser        string
	CallerProject     string
	CallerEnvironment string
	TokenID           string
	AdminSubject      string
	Client            string
}

type securityTrendBucketRecord struct {
	BucketUTC string
	Outcome   string
	Surface   string
	Reason    string
	Events    int64
}

type usageRow struct {
	TS                                 time.Time
	RequestID                          string
	CallerID                           string
	CallerUser                         string
	CallerProject                      string
	CallerEnvironment                  string
	CallerIP                           string
	TokenID                            string
	Client                             string
	InboundDialect                     string
	RequestedModel                     string
	ResolvedGroup                      string
	Strategy                           string
	TargetProvider                     string
	TargetModel                        string
	TargetDialect                      string
	TargetRegion                       string
	Stream                             bool
	Cache                              string
	Status                             int
	Attempts                           int
	FallbackUsed                       bool
	LatencyMS                          int64
	TTFBMS                             *int64
	UpstreamMS                         *int64
	DownstreamMS                       *int64
	UpstreamOutputTPS                  *float64
	UpstreamTotalTPS                   *float64
	DownstreamOutputTPS                *float64
	DownstreamTotalTPS                 *float64
	InputTokens                        int
	OutputTokens                       int
	TotalTokens                        int
	ReasoningTokens                    *int
	CachedInputTokens                  *int
	ReasoningAttemptCount              int
	ReasoningSuccessfulAttemptCount    int
	ReasoningReportedAttemptCount      int
	InputHasImage                      bool
	InputImageCount                    int
	InputImageTokens                   int
	PIIFilterApplied                   bool
	PIIFilterMode                      string
	PIIFilterReplacements              int
	PIIFilterRuleCount                 int
	ContractPresent                    bool
	ContractBucket                     string
	ContractFailureReason              string
	ContractWorkload                   string
	TargetValidationStatus             string
	TargetValidationWorkload           string
	TargetValidationAgeBucket          string
	InputPricePerMillionUSD            float64
	OutputPricePerMillionUSD           float64
	CachedInputPricePerMillionUSD      *float64
	ImageInputPricePerMillionTokensUSD float64
	ImageInputPricePerImageUSD         float64
	InputCostUSD                       float64
	ImageCostUSD                       float64
	OutputCostUSD                      float64
	TotalCostUSD                       float64
	UpstreamReportedInputCostUSD       float64
	UpstreamReportedOutputCostUSD      float64
	UpstreamReportedTotalCostUSD       float64
	PricingSource                      string
	PricingUpdatedAt                   string
	CacheEnabled                       bool
	CacheItems                         int64
	CacheBytes                         int64
	CacheMaxBytes                      int64
	CacheOccupancyPct                  float64
	QuotaState                         string
	KeyState                           string
	TrafficShapeApplied                bool
	TrafficShapeDecision               string
	TrafficShapeScope                  string
	TrafficShapeBucket                 string
	TrafficShapeRetryAfterMS           int64
	TrafficShapeQueueWaitMS            int64
	TrafficShapeEstimatedInputTokens   int
	TrafficShapeReservedOutputTokens   int
	TrafficShapeTotalReservedTokens    int
	RouterVersion                      string
	RouterBuildDate                    string
	RoutingConfigFingerprint           string
	ModelGroupConfigFingerprint        string
	RoutingPolicyFingerprint           string
	PricingCatalogFingerprint          string
	Error                              string
	UpstreamErrorCode                  string
	UpstreamErrorParam                 string
	UpstreamErrorMessageCategory       string
	RequestShapeFingerprint            string
	ToolSchemaFingerprint              string
	RequestShapeBucket                 string
	TranslationShapeBucket             string
	EstimatedInputTokens               int
	EstimatedToolSchemaTokens          int
	EstimatedImageTokens               int
	EstimatedTotalInputTokens          int
	RequestedOutputCapTokens           int
	TotalReservedTokens                int
	MaxTokenBucket                     string
	InputTokenBucket                   string
	AdmissionReason                    string
	EnabledSignals                     []string
	ScoreBuckets                       []string
	ThresholdBuckets                   []string
}

type securityAccessEvent struct {
	ID                  uint
	TS                  time.Time
	RequestID           string
	EventType           string
	Surface             string
	HTTPMethod          string
	PathTemplate        string
	StatusCode          int
	Outcome             string
	ReasonCode          string
	AuthSubject         string
	AuthSource          string
	CallerID            string
	CallerUser          string
	CallerProject       string
	CallerEnvironment   string
	TokenID             string
	AdminSubject        string
	AdminDomain         string
	Client              string
	UserAgentFamily     string
	IPAddress           string
	IPVersion           int
	IPSource            string
	TrustedProxyApplied bool
	RequestIsPrivate    bool
	RequestIsLoopback   bool
	RequestIsReserved   bool
	ModelGroup          string
	RequestedModel      string
	ResolvedGroup       string
	InputTokens         int
	OutputTokens        int
	TotalTokens         int
}

type usageRecord struct {
	RequestID                          string                             `gorm:"column:request_id;primaryKey;type:text"`
	TS                                 string                             `gorm:"column:ts;type:text;not null;index:idx_request_usage_ts"`
	CallerID                           string                             `gorm:"column:caller_id;type:text;not null"`
	CallerUser                         string                             `gorm:"column:caller_user;type:text;not null"`
	CallerProject                      string                             `gorm:"column:caller_project;type:text;not null"`
	CallerEnvironment                  string                             `gorm:"column:caller_environment;type:text;not null"`
	CallerIP                           string                             `gorm:"column:caller_ip;type:text;index:idx_request_usage_caller_ip,priority:1"`
	TokenID                            string                             `gorm:"column:token_id;type:text;not null;index:idx_request_usage_token,priority:1"`
	Client                             string                             `gorm:"column:client;type:text;not null"`
	InboundDialect                     string                             `gorm:"column:inbound_dialect;type:text;not null"`
	RequestedModel                     string                             `gorm:"column:requested_model;type:text;not null"`
	ResolvedGroup                      string                             `gorm:"column:resolved_group;type:text;not null;index:idx_request_usage_group,priority:1"`
	Strategy                           string                             `gorm:"column:strategy;type:text;not null"`
	TargetProvider                     string                             `gorm:"column:target_provider;type:text;not null;index:idx_request_usage_provider_model,priority:1"`
	TargetModel                        string                             `gorm:"column:target_model;type:text;not null;index:idx_request_usage_provider_model,priority:2"`
	TargetDialect                      string                             `gorm:"column:target_dialect;type:text;not null"`
	TargetRegion                       string                             `gorm:"column:target_region;type:text;not null;default:''"`
	Stream                             bool                               `gorm:"column:stream;not null"`
	Cache                              string                             `gorm:"column:cache;type:text;not null"`
	Status                             int                                `gorm:"column:status;not null"`
	Attempts                           int                                `gorm:"column:attempts;not null"`
	FallbackUsed                       bool                               `gorm:"column:fallback_used;not null"`
	LatencyMS                          int64                              `gorm:"column:latency_ms;not null"`
	TTFBMS                             *int64                             `gorm:"column:ttfb_ms"`
	UpstreamMS                         *int64                             `gorm:"column:upstream_duration_ms"`
	DownstreamMS                       *int64                             `gorm:"column:downstream_duration_ms"`
	UpstreamOutputTPS                  *float64                           `gorm:"column:upstream_output_tokens_per_sec"`
	UpstreamTotalTPS                   *float64                           `gorm:"column:upstream_total_tokens_per_sec"`
	DownstreamOutputTPS                *float64                           `gorm:"column:downstream_output_tokens_per_sec"`
	DownstreamTotalTPS                 *float64                           `gorm:"column:downstream_total_tokens_per_sec"`
	InputTokens                        int                                `gorm:"column:input_tokens;not null"`
	OutputTokens                       int                                `gorm:"column:output_tokens;not null"`
	TotalTokens                        int                                `gorm:"column:total_tokens;not null"`
	ReasoningTokens                    *int                               `gorm:"column:reasoning_tokens"`
	CachedInputTokens                  *int                               `gorm:"column:cached_input_tokens"`
	ReasoningAttemptCount              int                                `gorm:"column:reasoning_attempt_count;not null;default:0"`
	ReasoningSuccessfulAttemptCount    int                                `gorm:"column:reasoning_successful_attempt_count;not null;default:0"`
	ReasoningReportedAttemptCount      int                                `gorm:"column:reasoning_reported_attempt_count;not null;default:0"`
	InputHasImage                      bool                               `gorm:"column:input_has_image;not null;default:false;index:idx_request_usage_input_image"`
	InputImageCount                    int                                `gorm:"column:input_image_count;not null;default:0"`
	InputImageTokens                   int                                `gorm:"column:input_image_tokens;not null;default:0"`
	PIIFilterApplied                   bool                               `gorm:"column:pii_filter_applied;not null;default:false;index:idx_request_usage_pii_filter"`
	PIIFilterMode                      string                             `gorm:"column:pii_filter_mode;type:text;not null;default:''"`
	PIIFilterReplacements              int                                `gorm:"column:pii_filter_replacements;not null;default:0"`
	PIIFilterRuleCount                 int                                `gorm:"column:pii_filter_rule_count;not null;default:0"`
	ContractPresent                    bool                               `gorm:"column:contract_present;not null;default:false;index:idx_request_usage_contract"`
	ContractBucket                     string                             `gorm:"column:contract_bucket;type:text;not null;default:'';index:idx_request_usage_contract_bucket"`
	ContractFailureReason              string                             `gorm:"column:contract_failure_reason;type:text;not null;default:'';index:idx_request_usage_contract_failure"`
	ContractWorkload                   string                             `gorm:"column:contract_workload;type:text;not null;default:'';index:idx_request_usage_contract_workload"`
	TargetValidationStatus             string                             `gorm:"column:target_validation_status;type:text;not null;default:'';index:idx_request_usage_validation_status"`
	TargetValidationWorkload           string                             `gorm:"column:target_validation_workload;type:text;not null;default:'';index:idx_request_usage_validation_workload"`
	TargetValidationAgeBucket          string                             `gorm:"column:target_validation_age_bucket;type:text;not null;default:'';index:idx_request_usage_validation_age"`
	InputPricePerMillionUSD            float64                            `gorm:"column:input_price_per_million_usd;not null;default:0"`
	OutputPricePerMillionUSD           float64                            `gorm:"column:output_price_per_million_usd;not null;default:0"`
	CachedInputPricePerMillionUSD      *float64                           `gorm:"column:cached_input_price_per_million_usd"`
	ImageInputPricePerMillionTokensUSD float64                            `gorm:"column:image_input_price_per_million_tokens_usd;not null;default:0"`
	ImageInputPricePerImageUSD         float64                            `gorm:"column:image_input_price_per_image_usd;not null;default:0"`
	InputCostUSD                       float64                            `gorm:"column:input_cost_usd;not null;default:0"`
	ImageCostUSD                       float64                            `gorm:"column:image_cost_usd;not null;default:0"`
	OutputCostUSD                      float64                            `gorm:"column:output_cost_usd;not null;default:0"`
	TotalCostUSD                       float64                            `gorm:"column:total_cost_usd;not null;default:0"`
	UpstreamReportedInputCostUSD       float64                            `gorm:"column:upstream_reported_input_cost_usd;not null;default:0"`
	UpstreamReportedOutputCostUSD      float64                            `gorm:"column:upstream_reported_output_cost_usd;not null;default:0"`
	UpstreamReportedTotalCostUSD       float64                            `gorm:"column:upstream_reported_total_cost_usd;not null;default:0"`
	PricingSource                      string                             `gorm:"column:pricing_source;type:text;not null;default:''"`
	PricingUpdatedAt                   string                             `gorm:"column:pricing_updated_at;type:text;not null;default:''"`
	CacheEnabled                       bool                               `gorm:"column:cache_enabled;not null"`
	CacheItems                         int64                              `gorm:"column:cache_items;not null"`
	CacheBytes                         int64                              `gorm:"column:cache_bytes;not null"`
	CacheMaxBytes                      int64                              `gorm:"column:cache_max_bytes;not null"`
	CacheOccupancyPct                  float64                            `gorm:"column:cache_occupancy_pct;not null"`
	QuotaState                         string                             `gorm:"column:quota_state;type:text;not null"`
	KeyState                           string                             `gorm:"column:key_state;type:text;not null"`
	TrafficShapeApplied                bool                               `gorm:"column:traffic_shape_applied;not null;default:false"`
	TrafficShapeDecision               string                             `gorm:"column:traffic_shape_decision;type:text;not null;default:''"`
	TrafficShapeScope                  string                             `gorm:"column:traffic_shape_scope;type:text;not null;default:''"`
	TrafficShapeBucket                 string                             `gorm:"column:traffic_shape_bucket;type:text;not null;default:''"`
	TrafficShapeRetryAfterMS           int64                              `gorm:"column:traffic_shape_retry_after_ms;not null;default:0"`
	TrafficShapeQueueWaitMS            int64                              `gorm:"column:traffic_shape_queue_wait_ms;not null;default:0"`
	TrafficShapeEstimatedInputTokens   int                                `gorm:"column:traffic_shape_estimated_input_tokens;not null;default:0"`
	TrafficShapeReservedOutputTokens   int                                `gorm:"column:traffic_shape_reserved_output_tokens;not null;default:0"`
	TrafficShapeTotalReservedTokens    int                                `gorm:"column:traffic_shape_total_reserved_tokens;not null;default:0"`
	RouterVersion                      string                             `gorm:"column:router_version;type:text;not null;default:'';index:idx_request_usage_router_version"`
	RouterBuildDate                    string                             `gorm:"column:router_build_date;type:text;not null;default:''"`
	RoutingConfigFingerprint           string                             `gorm:"column:routing_config_fingerprint;type:text;not null;default:'';index:idx_request_usage_routing_fp"`
	ModelGroupConfigFingerprint        string                             `gorm:"column:model_group_config_fingerprint;type:text;not null;default:'';index:idx_request_usage_group_fp"`
	RoutingPolicyFingerprint           string                             `gorm:"column:routing_policy_fingerprint;type:text;not null;default:'';index:idx_request_usage_policy_fp"`
	PricingCatalogFingerprint          string                             `gorm:"column:pricing_catalog_fingerprint;type:text;not null;default:'';index:idx_request_usage_pricing_fp"`
	Error                              string                             `gorm:"column:error;type:text;not null"`
	TokenEstimate                      requestTokenEstimateRecord         `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	DecisionShapeFeatures              []decisionShapeFeatureRecord       `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	DecisionCandidates                 []decisionTargetCandidateRecord    `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	DecisionFilterReasons              []decisionTargetFilterReasonRecord `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	RoutingDecisions                   []routingDecisionRecord            `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	RoutingSignals                     []routingSignalRecord              `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	DynamicScoreTerms                  []dynamicScoreTermRecord           `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	PolicyExecutions                   []policyExecutionRecord            `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	FallbackTransitions                []fallbackTransitionRecord         `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	DecisionCacheReasons               []decisionCacheReasonRecord        `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	TrafficShapeEvents                 []requestTrafficShapeEventRecord   `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	UpstreamErrorDetails               []requestUpstreamErrorDetailRecord `gorm:"foreignKey:RequestID;references:RequestID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (usageRecord) TableName() string {
	return "request_usage"
}

type securityAccessEventRecord struct {
	ID                  uint   `gorm:"column:id;primaryKey;autoIncrement"`
	TS                  string `gorm:"column:ts;type:text;not null;index:idx_security_access_ts"`
	RequestID           string `gorm:"column:request_id;type:text;not null;index:idx_security_access_request"`
	EventType           string `gorm:"column:event_type;type:text;not null;index:idx_security_access_event_type,priority:1"`
	Surface             string `gorm:"column:surface;type:text;not null;index:idx_security_access_surface"`
	HTTPMethod          string `gorm:"column:http_method;type:text;not null"`
	PathTemplate        string `gorm:"column:path_template;type:text;not null"`
	StatusCode          int    `gorm:"column:status_code;not null;index:idx_security_access_status"`
	Outcome             string `gorm:"column:outcome;type:text;not null;index:idx_security_access_outcome,priority:1"`
	ReasonCode          string `gorm:"column:reason_code;type:text;not null;index:idx_security_access_reason,priority:1"`
	AuthSubject         string `gorm:"column:auth_subject;type:text;not null"`
	AuthSource          string `gorm:"column:auth_source;type:text;not null"`
	CallerID            string `gorm:"column:caller_id;type:text;not null;index:idx_security_access_caller,priority:1"`
	CallerUser          string `gorm:"column:caller_user;type:text;not null"`
	CallerProject       string `gorm:"column:caller_project;type:text;not null;index:idx_security_access_project,priority:1"`
	CallerEnvironment   string `gorm:"column:caller_environment;type:text;not null"`
	TokenID             string `gorm:"column:token_id;type:text;not null;index:idx_security_access_token,priority:1"`
	AdminSubject        string `gorm:"column:admin_subject;type:text;not null;index:idx_security_access_admin,priority:1"`
	AdminDomain         string `gorm:"column:admin_domain;type:text;not null"`
	Client              string `gorm:"column:client;type:text;not null"`
	UserAgentFamily     string `gorm:"column:user_agent_family;type:text;not null"`
	IPAddress           string `gorm:"column:ip_address;type:text;not null;index:idx_security_access_ip,priority:1"`
	IPVersion           int    `gorm:"column:ip_version;not null"`
	IPSource            string `gorm:"column:ip_source;type:text;not null"`
	TrustedProxyApplied bool   `gorm:"column:trusted_proxy_applied;not null"`
	RequestIsPrivate    bool   `gorm:"column:request_is_private;not null"`
	RequestIsLoopback   bool   `gorm:"column:request_is_loopback;not null"`
	RequestIsReserved   bool   `gorm:"column:request_is_reserved;not null"`
	ModelGroup          string `gorm:"column:model_group;type:text;not null;index:idx_security_access_group"`
	RequestedModel      string `gorm:"column:requested_model;type:text;not null"`
	ResolvedGroup       string `gorm:"column:resolved_group;type:text;not null"`
	InputTokens         int    `gorm:"column:input_tokens;not null;default:0"`
	OutputTokens        int    `gorm:"column:output_tokens;not null;default:0"`
	TotalTokens         int    `gorm:"column:total_tokens;not null;default:0"`
}

func (securityAccessEventRecord) TableName() string {
	return "security_access_events"
}

type usageRollupRunRecord struct {
	ID                     uint   `gorm:"column:id;primaryKey;autoIncrement"`
	RollupType             string `gorm:"column:rollup_type;type:text;not null;index:idx_usage_rollup_run_window,priority:1;uniqueIndex:idx_usage_rollup_run_unique,priority:1"`
	Status                 string `gorm:"column:status;type:text;not null;index:idx_usage_rollup_run_status;uniqueIndex:idx_usage_rollup_run_unique,priority:2"`
	WindowStart            string `gorm:"column:window_start;type:text;not null;index:idx_usage_rollup_run_window,priority:2;uniqueIndex:idx_usage_rollup_run_unique,priority:3"`
	WindowEnd              string `gorm:"column:window_end;type:text;not null;index:idx_usage_rollup_run_window,priority:3;uniqueIndex:idx_usage_rollup_run_unique,priority:4"`
	SourceTable            string `gorm:"column:source_table;type:text;not null"`
	SourceRequestCount     int64  `gorm:"column:source_request_count;not null;default:0"`
	SourceMinTS            string `gorm:"column:source_min_ts;type:text;not null;default:''"`
	SourceMaxTS            string `gorm:"column:source_max_ts;type:text;not null;default:''"`
	SourceChecksum         string `gorm:"column:source_checksum;type:text;not null;default:'';index:idx_usage_rollup_run_checksum"`
	DailyRowCount          int64  `gorm:"column:daily_row_count;not null;default:0"`
	RollupRowCount         int64  `gorm:"column:rollup_row_count;not null;default:0"`
	DecisionBucketRowCount int64  `gorm:"column:decision_bucket_row_count;not null;default:0"`
	RouterVersion          string `gorm:"column:router_version;type:text;not null;default:''"`
	RouterCommit           string `gorm:"column:router_commit;type:text;not null;default:''"`
	SafeError              string `gorm:"column:safe_error;type:text;not null;default:''"`
	StartedAt              string `gorm:"column:started_at;type:text;not null"`
	CompletedAt            string `gorm:"column:completed_at;type:text;not null"`
	GeneratedAt            string `gorm:"column:generated_at;type:text;not null;default:''"`
	FinalizedAt            string `gorm:"column:finalized_at;type:text;not null;default:''"`
}

func (usageRollupRunRecord) TableName() string {
	return "usage_rollup_runs"
}

type usageRollupDailyRecord struct {
	ID                                uint    `gorm:"column:id;primaryKey;autoIncrement"`
	RunID                             uint    `gorm:"column:run_id;not null;index:idx_usage_rollup_daily_run;uniqueIndex:idx_usage_rollup_daily_unique,priority:1"`
	RollupType                        string  `gorm:"column:rollup_type;type:text;not null;index:idx_usage_rollup_daily_day,priority:1"`
	Status                            string  `gorm:"column:status;type:text;not null;index:idx_usage_rollup_daily_status"`
	DayUTC                            string  `gorm:"column:day_utc;type:text;not null;index:idx_usage_rollup_daily_day,priority:2;uniqueIndex:idx_usage_rollup_daily_unique,priority:2"`
	WindowStart                       string  `gorm:"column:window_start;type:text;not null"`
	WindowEnd                         string  `gorm:"column:window_end;type:text;not null"`
	SourceTable                       string  `gorm:"column:source_table;type:text;not null"`
	CallerID                          string  `gorm:"column:caller_id;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:3"`
	CallerUser                        string  `gorm:"column:caller_user;type:text;not null;default:'';index:idx_usage_rollup_daily_caller,priority:1;uniqueIndex:idx_usage_rollup_daily_unique,priority:4"`
	CallerProject                     string  `gorm:"column:caller_project;type:text;not null;default:'';index:idx_usage_rollup_daily_caller,priority:2;uniqueIndex:idx_usage_rollup_daily_unique,priority:5"`
	CallerEnvironment                 string  `gorm:"column:caller_environment;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:6"`
	TokenID                           string  `gorm:"column:token_id;type:text;not null;default:'';index:idx_usage_rollup_daily_token;uniqueIndex:idx_usage_rollup_daily_unique,priority:7"`
	Client                            string  `gorm:"column:client;type:text;not null;default:'';index:idx_usage_rollup_daily_client;uniqueIndex:idx_usage_rollup_daily_unique,priority:8"`
	InboundDialect                    string  `gorm:"column:inbound_dialect;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:9"`
	RequestedModel                    string  `gorm:"column:requested_model;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:10"`
	ResolvedGroup                     string  `gorm:"column:resolved_group;type:text;not null;default:'';index:idx_usage_rollup_daily_group;uniqueIndex:idx_usage_rollup_daily_unique,priority:11"`
	Strategy                          string  `gorm:"column:strategy;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:12"`
	TargetProvider                    string  `gorm:"column:target_provider;type:text;not null;default:'';index:idx_usage_rollup_daily_provider_model,priority:1;uniqueIndex:idx_usage_rollup_daily_unique,priority:13"`
	TargetModel                       string  `gorm:"column:target_model;type:text;not null;default:'';index:idx_usage_rollup_daily_provider_model,priority:2;uniqueIndex:idx_usage_rollup_daily_unique,priority:14"`
	TargetDialect                     string  `gorm:"column:target_dialect;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:15"`
	StatusClass                       string  `gorm:"column:status_class;type:text;not null;default:'';index:idx_usage_rollup_daily_status_class;uniqueIndex:idx_usage_rollup_daily_unique,priority:16"`
	Stream                            bool    `gorm:"column:stream;not null;default:false;uniqueIndex:idx_usage_rollup_daily_unique,priority:17"`
	Cache                             string  `gorm:"column:cache;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:18"`
	InputHasImage                     bool    `gorm:"column:input_has_image;not null;default:false;uniqueIndex:idx_usage_rollup_daily_unique,priority:19"`
	PIIFilterApplied                  bool    `gorm:"column:pii_filter_applied;not null;default:false;uniqueIndex:idx_usage_rollup_daily_unique,priority:20"`
	ContractBucket                    string  `gorm:"column:contract_bucket;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:21"`
	TargetValidationStatus            string  `gorm:"column:target_validation_status;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:22"`
	SourceRequestCount                int64   `gorm:"column:source_request_count;not null;default:0"`
	SuccessCount                      int64   `gorm:"column:success_count;not null;default:0"`
	ErrorCount                        int64   `gorm:"column:error_count;not null;default:0"`
	StreamCount                       int64   `gorm:"column:stream_count;not null;default:0"`
	CacheHitCount                     int64   `gorm:"column:cache_hit_count;not null;default:0"`
	CacheMissCount                    int64   `gorm:"column:cache_miss_count;not null;default:0"`
	CacheBypassCount                  int64   `gorm:"column:cache_bypass_count;not null;default:0"`
	FallbackCount                     int64   `gorm:"column:fallback_count;not null;default:0"`
	AttemptCount                      int64   `gorm:"column:attempt_count;not null;default:0"`
	InputTokens                       int64   `gorm:"column:input_tokens;not null;default:0"`
	OutputTokens                      int64   `gorm:"column:output_tokens;not null;default:0"`
	TotalTokens                       int64   `gorm:"column:total_tokens;not null;default:0"`
	InputImageCount                   int64   `gorm:"column:input_image_count;not null;default:0"`
	InputImageTokens                  int64   `gorm:"column:input_image_tokens;not null;default:0"`
	InputCostUSD                      float64 `gorm:"column:input_cost_usd;not null;default:0"`
	ImageCostUSD                      float64 `gorm:"column:image_cost_usd;not null;default:0"`
	OutputCostUSD                     float64 `gorm:"column:output_cost_usd;not null;default:0"`
	TotalCostUSD                      float64 `gorm:"column:total_cost_usd;not null;default:0"`
	UpstreamReportedInputCostUSD      float64 `gorm:"column:upstream_reported_input_cost_usd;not null;default:0"`
	UpstreamReportedOutputCostUSD     float64 `gorm:"column:upstream_reported_output_cost_usd;not null;default:0"`
	UpstreamReportedTotalCostUSD      float64 `gorm:"column:upstream_reported_total_cost_usd;not null;default:0"`
	BaselineID                        string  `gorm:"column:baseline_id;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_daily_unique,priority:23"`
	BaselineName                      string  `gorm:"column:baseline_name;type:text;not null;default:''"`
	BaselineVersion                   string  `gorm:"column:baseline_version;type:text;not null;default:''"`
	BaselineInputPricePerMillionUSD   float64 `gorm:"column:baseline_input_price_per_million_usd;not null;default:0"`
	BaselineOutputPricePerMillionUSD  float64 `gorm:"column:baseline_output_price_per_million_usd;not null;default:0"`
	BaselineInputCostUSD              float64 `gorm:"column:baseline_input_cost_usd;not null;default:0"`
	BaselineOutputCostUSD             float64 `gorm:"column:baseline_output_cost_usd;not null;default:0"`
	BaselineTotalCostUSD              float64 `gorm:"column:baseline_total_cost_usd;not null;default:0"`
	InputSavingsUSD                   float64 `gorm:"column:input_savings_usd;not null;default:0"`
	OutputSavingsUSD                  float64 `gorm:"column:output_savings_usd;not null;default:0"`
	TotalSavingsUSD                   float64 `gorm:"column:total_savings_usd;not null;default:0"`
	LatencyMSSum                      int64   `gorm:"column:latency_ms_sum;not null;default:0"`
	LatencyMSCount                    int64   `gorm:"column:latency_ms_count;not null;default:0"`
	LatencyMSMax                      int64   `gorm:"column:latency_ms_max;not null;default:0"`
	TTFBMSSum                         int64   `gorm:"column:ttfb_ms_sum;not null;default:0"`
	TTFBMSCount                       int64   `gorm:"column:ttfb_ms_count;not null;default:0"`
	TTFBMSMax                         int64   `gorm:"column:ttfb_ms_max;not null;default:0"`
	UpstreamMSSum                     int64   `gorm:"column:upstream_duration_ms_sum;not null;default:0"`
	UpstreamMSCount                   int64   `gorm:"column:upstream_duration_ms_count;not null;default:0"`
	UpstreamMSMax                     int64   `gorm:"column:upstream_duration_ms_max;not null;default:0"`
	DownstreamMSSum                   int64   `gorm:"column:downstream_duration_ms_sum;not null;default:0"`
	DownstreamMSCount                 int64   `gorm:"column:downstream_duration_ms_count;not null;default:0"`
	DownstreamMSMax                   int64   `gorm:"column:downstream_duration_ms_max;not null;default:0"`
	UpstreamOutputTokensPerSecSum     float64 `gorm:"column:upstream_output_tokens_per_sec_sum;not null;default:0"`
	UpstreamOutputTokensPerSecCount   int64   `gorm:"column:upstream_output_tokens_per_sec_count;not null;default:0"`
	UpstreamOutputTokensPerSecMin     float64 `gorm:"column:upstream_output_tokens_per_sec_min;not null;default:0"`
	UpstreamOutputTokensPerSecMax     float64 `gorm:"column:upstream_output_tokens_per_sec_max;not null;default:0"`
	UpstreamTotalTokensPerSecSum      float64 `gorm:"column:upstream_total_tokens_per_sec_sum;not null;default:0"`
	UpstreamTotalTokensPerSecCount    int64   `gorm:"column:upstream_total_tokens_per_sec_count;not null;default:0"`
	UpstreamTotalTokensPerSecMin      float64 `gorm:"column:upstream_total_tokens_per_sec_min;not null;default:0"`
	UpstreamTotalTokensPerSecMax      float64 `gorm:"column:upstream_total_tokens_per_sec_max;not null;default:0"`
	DownstreamOutputTokensPerSecSum   float64 `gorm:"column:downstream_output_tokens_per_sec_sum;not null;default:0"`
	DownstreamOutputTokensPerSecCount int64   `gorm:"column:downstream_output_tokens_per_sec_count;not null;default:0"`
	DownstreamOutputTokensPerSecMin   float64 `gorm:"column:downstream_output_tokens_per_sec_min;not null;default:0"`
	DownstreamOutputTokensPerSecMax   float64 `gorm:"column:downstream_output_tokens_per_sec_max;not null;default:0"`
	DownstreamTotalTokensPerSecSum    float64 `gorm:"column:downstream_total_tokens_per_sec_sum;not null;default:0"`
	DownstreamTotalTokensPerSecCount  int64   `gorm:"column:downstream_total_tokens_per_sec_count;not null;default:0"`
	DownstreamTotalTokensPerSecMin    float64 `gorm:"column:downstream_total_tokens_per_sec_min;not null;default:0"`
	DownstreamTotalTokensPerSecMax    float64 `gorm:"column:downstream_total_tokens_per_sec_max;not null;default:0"`
	CacheSnapshotCount                int64   `gorm:"column:cache_snapshot_count;not null;default:0"`
	CacheItemsMax                     int64   `gorm:"column:cache_items_max;not null;default:0"`
	CacheBytesSum                     int64   `gorm:"column:cache_bytes_sum;not null;default:0"`
	CacheBytesMax                     int64   `gorm:"column:cache_bytes_max;not null;default:0"`
	CacheMaxBytesLatest               int64   `gorm:"column:cache_max_bytes_latest;not null;default:0"`
	CacheOccupancyPctSum              float64 `gorm:"column:cache_occupancy_pct_sum;not null;default:0"`
	CacheOccupancyPctMax              float64 `gorm:"column:cache_occupancy_pct_max;not null;default:0"`
}

func (usageRollupDailyRecord) TableName() string {
	return "usage_rollup_daily"
}

type usageRollupAggregateRecord struct {
	ID                                uint    `gorm:"column:id;primaryKey;autoIncrement"`
	RunID                             uint    `gorm:"column:run_id;not null"`
	RollupType                        string  `gorm:"column:rollup_type;type:text;not null"`
	Status                            string  `gorm:"column:status;type:text;not null"`
	BucketUTC                         string  `gorm:"column:bucket_utc;type:text;not null"`
	BucketStart                       string  `gorm:"column:bucket_start;type:text;not null"`
	BucketEnd                         string  `gorm:"column:bucket_end;type:text;not null"`
	WindowStart                       string  `gorm:"column:window_start;type:text;not null"`
	WindowEnd                         string  `gorm:"column:window_end;type:text;not null"`
	SourceTable                       string  `gorm:"column:source_table;type:text;not null"`
	CallerID                          string  `gorm:"column:caller_id;type:text;not null;default:''"`
	CallerUser                        string  `gorm:"column:caller_user;type:text;not null;default:''"`
	CallerProject                     string  `gorm:"column:caller_project;type:text;not null;default:''"`
	CallerEnvironment                 string  `gorm:"column:caller_environment;type:text;not null;default:''"`
	TokenID                           string  `gorm:"column:token_id;type:text;not null;default:''"`
	Client                            string  `gorm:"column:client;type:text;not null;default:''"`
	InboundDialect                    string  `gorm:"column:inbound_dialect;type:text;not null;default:''"`
	RequestedModel                    string  `gorm:"column:requested_model;type:text;not null;default:''"`
	ResolvedGroup                     string  `gorm:"column:resolved_group;type:text;not null;default:''"`
	Strategy                          string  `gorm:"column:strategy;type:text;not null;default:''"`
	TargetProvider                    string  `gorm:"column:target_provider;type:text;not null;default:''"`
	TargetModel                       string  `gorm:"column:target_model;type:text;not null;default:''"`
	TargetDialect                     string  `gorm:"column:target_dialect;type:text;not null;default:''"`
	StatusClass                       string  `gorm:"column:status_class;type:text;not null;default:''"`
	Stream                            bool    `gorm:"column:stream;not null;default:false"`
	Cache                             string  `gorm:"column:cache;type:text;not null;default:''"`
	InputHasImage                     bool    `gorm:"column:input_has_image;not null;default:false"`
	PIIFilterApplied                  bool    `gorm:"column:pii_filter_applied;not null;default:false"`
	ContractBucket                    string  `gorm:"column:contract_bucket;type:text;not null;default:''"`
	TargetValidationStatus            string  `gorm:"column:target_validation_status;type:text;not null;default:''"`
	SourceRequestCount                int64   `gorm:"column:source_request_count;not null;default:0"`
	SuccessCount                      int64   `gorm:"column:success_count;not null;default:0"`
	ErrorCount                        int64   `gorm:"column:error_count;not null;default:0"`
	StreamCount                       int64   `gorm:"column:stream_count;not null;default:0"`
	CacheHitCount                     int64   `gorm:"column:cache_hit_count;not null;default:0"`
	CacheMissCount                    int64   `gorm:"column:cache_miss_count;not null;default:0"`
	CacheBypassCount                  int64   `gorm:"column:cache_bypass_count;not null;default:0"`
	FallbackCount                     int64   `gorm:"column:fallback_count;not null;default:0"`
	AttemptCount                      int64   `gorm:"column:attempt_count;not null;default:0"`
	InputTokens                       int64   `gorm:"column:input_tokens;not null;default:0"`
	OutputTokens                      int64   `gorm:"column:output_tokens;not null;default:0"`
	TotalTokens                       int64   `gorm:"column:total_tokens;not null;default:0"`
	InputImageCount                   int64   `gorm:"column:input_image_count;not null;default:0"`
	InputImageTokens                  int64   `gorm:"column:input_image_tokens;not null;default:0"`
	InputCostUSD                      float64 `gorm:"column:input_cost_usd;not null;default:0"`
	ImageCostUSD                      float64 `gorm:"column:image_cost_usd;not null;default:0"`
	OutputCostUSD                     float64 `gorm:"column:output_cost_usd;not null;default:0"`
	TotalCostUSD                      float64 `gorm:"column:total_cost_usd;not null;default:0"`
	UpstreamReportedInputCostUSD      float64 `gorm:"column:upstream_reported_input_cost_usd;not null;default:0"`
	UpstreamReportedOutputCostUSD     float64 `gorm:"column:upstream_reported_output_cost_usd;not null;default:0"`
	UpstreamReportedTotalCostUSD      float64 `gorm:"column:upstream_reported_total_cost_usd;not null;default:0"`
	BaselineID                        string  `gorm:"column:baseline_id;type:text;not null;default:''"`
	BaselineName                      string  `gorm:"column:baseline_name;type:text;not null;default:''"`
	BaselineVersion                   string  `gorm:"column:baseline_version;type:text;not null;default:''"`
	BaselineInputPricePerMillionUSD   float64 `gorm:"column:baseline_input_price_per_million_usd;not null;default:0"`
	BaselineOutputPricePerMillionUSD  float64 `gorm:"column:baseline_output_price_per_million_usd;not null;default:0"`
	BaselineInputCostUSD              float64 `gorm:"column:baseline_input_cost_usd;not null;default:0"`
	BaselineOutputCostUSD             float64 `gorm:"column:baseline_output_cost_usd;not null;default:0"`
	BaselineTotalCostUSD              float64 `gorm:"column:baseline_total_cost_usd;not null;default:0"`
	InputSavingsUSD                   float64 `gorm:"column:input_savings_usd;not null;default:0"`
	OutputSavingsUSD                  float64 `gorm:"column:output_savings_usd;not null;default:0"`
	TotalSavingsUSD                   float64 `gorm:"column:total_savings_usd;not null;default:0"`
	LatencyMSSum                      int64   `gorm:"column:latency_ms_sum;not null;default:0"`
	LatencyMSCount                    int64   `gorm:"column:latency_ms_count;not null;default:0"`
	LatencyMSMax                      int64   `gorm:"column:latency_ms_max;not null;default:0"`
	TTFBMSSum                         int64   `gorm:"column:ttfb_ms_sum;not null;default:0"`
	TTFBMSCount                       int64   `gorm:"column:ttfb_ms_count;not null;default:0"`
	TTFBMSMax                         int64   `gorm:"column:ttfb_ms_max;not null;default:0"`
	UpstreamMSSum                     int64   `gorm:"column:upstream_duration_ms_sum;not null;default:0"`
	UpstreamMSCount                   int64   `gorm:"column:upstream_duration_ms_count;not null;default:0"`
	UpstreamMSMax                     int64   `gorm:"column:upstream_duration_ms_max;not null;default:0"`
	DownstreamMSSum                   int64   `gorm:"column:downstream_duration_ms_sum;not null;default:0"`
	DownstreamMSCount                 int64   `gorm:"column:downstream_duration_ms_count;not null;default:0"`
	DownstreamMSMax                   int64   `gorm:"column:downstream_duration_ms_max;not null;default:0"`
	UpstreamOutputTokensPerSecSum     float64 `gorm:"column:upstream_output_tokens_per_sec_sum;not null;default:0"`
	UpstreamOutputTokensPerSecCount   int64   `gorm:"column:upstream_output_tokens_per_sec_count;not null;default:0"`
	UpstreamOutputTokensPerSecMin     float64 `gorm:"column:upstream_output_tokens_per_sec_min;not null;default:0"`
	UpstreamOutputTokensPerSecMax     float64 `gorm:"column:upstream_output_tokens_per_sec_max;not null;default:0"`
	UpstreamTotalTokensPerSecSum      float64 `gorm:"column:upstream_total_tokens_per_sec_sum;not null;default:0"`
	UpstreamTotalTokensPerSecCount    int64   `gorm:"column:upstream_total_tokens_per_sec_count;not null;default:0"`
	UpstreamTotalTokensPerSecMin      float64 `gorm:"column:upstream_total_tokens_per_sec_min;not null;default:0"`
	UpstreamTotalTokensPerSecMax      float64 `gorm:"column:upstream_total_tokens_per_sec_max;not null;default:0"`
	DownstreamOutputTokensPerSecSum   float64 `gorm:"column:downstream_output_tokens_per_sec_sum;not null;default:0"`
	DownstreamOutputTokensPerSecCount int64   `gorm:"column:downstream_output_tokens_per_sec_count;not null;default:0"`
	DownstreamOutputTokensPerSecMin   float64 `gorm:"column:downstream_output_tokens_per_sec_min;not null;default:0"`
	DownstreamOutputTokensPerSecMax   float64 `gorm:"column:downstream_output_tokens_per_sec_max;not null;default:0"`
	DownstreamTotalTokensPerSecSum    float64 `gorm:"column:downstream_total_tokens_per_sec_sum;not null;default:0"`
	DownstreamTotalTokensPerSecCount  int64   `gorm:"column:downstream_total_tokens_per_sec_count;not null;default:0"`
	DownstreamTotalTokensPerSecMin    float64 `gorm:"column:downstream_total_tokens_per_sec_min;not null;default:0"`
	DownstreamTotalTokensPerSecMax    float64 `gorm:"column:downstream_total_tokens_per_sec_max;not null;default:0"`
	CacheSnapshotCount                int64   `gorm:"column:cache_snapshot_count;not null;default:0"`
	CacheItemsMax                     int64   `gorm:"column:cache_items_max;not null;default:0"`
	CacheBytesSum                     int64   `gorm:"column:cache_bytes_sum;not null;default:0"`
	CacheBytesMax                     int64   `gorm:"column:cache_bytes_max;not null;default:0"`
	CacheMaxBytesLatest               int64   `gorm:"column:cache_max_bytes_latest;not null;default:0"`
	CacheOccupancyPctSum              float64 `gorm:"column:cache_occupancy_pct_sum;not null;default:0"`
	CacheOccupancyPctMax              float64 `gorm:"column:cache_occupancy_pct_max;not null;default:0"`
}

type usageRollupHourlyRecord usageRollupAggregateRecord

func (usageRollupHourlyRecord) TableName() string {
	return "usage_rollup_hourly"
}

type usageRollupMonthlyBillingRecord usageRollupAggregateRecord

func (usageRollupMonthlyBillingRecord) TableName() string {
	return "usage_rollup_monthly_billing"
}

type usageRollupAuditEventRecord struct {
	ID             uint                 `gorm:"column:id;primaryKey;autoIncrement"`
	RunID          uint                 `gorm:"column:run_id;not null;index:idx_usage_rollup_audit_run"`
	Action         string               `gorm:"column:action;type:text;not null;index:idx_usage_rollup_audit_action"`
	ActorSubject   string               `gorm:"column:actor_subject;type:text;not null;default:''"`
	TS             string               `gorm:"column:ts;type:text;not null;index:idx_usage_rollup_audit_ts"`
	SafeSummary    string               `gorm:"column:safe_summary;type:text;not null;default:''"`
	UsageRollupRun usageRollupRunRecord `gorm:"foreignKey:RunID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (usageRollupAuditEventRecord) TableName() string {
	return "usage_rollup_audit_events"
}

type usageRollupDecisionBucketRecord struct {
	ID                 uint                 `gorm:"column:id;primaryKey;autoIncrement"`
	RunID              uint                 `gorm:"column:run_id;not null;index:idx_usage_rollup_decision_run;uniqueIndex:idx_usage_rollup_decision_unique,priority:1"`
	RollupType         string               `gorm:"column:rollup_type;type:text;not null;index:idx_usage_rollup_decision_day,priority:1"`
	Status             string               `gorm:"column:status;type:text;not null;index:idx_usage_rollup_decision_status"`
	DayUTC             string               `gorm:"column:day_utc;type:text;not null;index:idx_usage_rollup_decision_day,priority:2;uniqueIndex:idx_usage_rollup_decision_unique,priority:2"`
	WindowStart        string               `gorm:"column:window_start;type:text;not null"`
	WindowEnd          string               `gorm:"column:window_end;type:text;not null"`
	ResolvedGroup      string               `gorm:"column:resolved_group;type:text;not null;default:'';index:idx_usage_rollup_decision_group;uniqueIndex:idx_usage_rollup_decision_unique,priority:3"`
	Strategy           string               `gorm:"column:strategy;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_decision_unique,priority:4"`
	BucketKind         string               `gorm:"column:bucket_kind;type:text;not null;index:idx_usage_rollup_decision_kind;uniqueIndex:idx_usage_rollup_decision_unique,priority:5"`
	BucketName         string               `gorm:"column:bucket_name;type:text;not null;index:idx_usage_rollup_decision_bucket;uniqueIndex:idx_usage_rollup_decision_unique,priority:6"`
	SecondaryBucket    string               `gorm:"column:secondary_bucket;type:text;not null;default:'';uniqueIndex:idx_usage_rollup_decision_unique,priority:7"`
	SourceRequestCount int64                `gorm:"column:source_request_count;not null;default:0"`
	ErrorCount         int64                `gorm:"column:error_count;not null;default:0"`
	InputTokens        int64                `gorm:"column:input_tokens;not null;default:0"`
	OutputTokens       int64                `gorm:"column:output_tokens;not null;default:0"`
	TotalTokens        int64                `gorm:"column:total_tokens;not null;default:0"`
	TotalCostUSD       float64              `gorm:"column:total_cost_usd;not null;default:0"`
	UsageRollupRun     usageRollupRunRecord `gorm:"foreignKey:RunID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (usageRollupDecisionBucketRecord) TableName() string {
	return "usage_rollup_decision_buckets"
}

type retentionPolicyVersionRecord struct {
	ID               uint   `gorm:"column:id;primaryKey;autoIncrement"`
	Version          string `gorm:"column:version;type:text;not null;uniqueIndex:idx_retention_policy_version"`
	ConfigHash       string `gorm:"column:config_hash;type:text;not null;index:idx_retention_policy_hash"`
	Source           string `gorm:"column:source;type:text;not null"`
	Active           bool   `gorm:"column:active;not null;index:idx_retention_policy_active"`
	DryRun           bool   `gorm:"column:dry_run;not null"`
	DefaultBatchSize int    `gorm:"column:default_batch_size;not null"`
	CreatedAt        string `gorm:"column:created_at;type:text;not null"`
	ActivatedAt      string `gorm:"column:activated_at;type:text;not null"`
	DeactivatedAt    string `gorm:"column:deactivated_at;type:text;not null;default:''"`
	Notes            string `gorm:"column:notes;type:text;not null;default:''"`
}

func (retentionPolicyVersionRecord) TableName() string {
	return "retention_policy_versions"
}

type retentionPolicyRuleRecord struct {
	ID                     uint                         `gorm:"column:id;primaryKey;autoIncrement"`
	PolicyVersionID        uint                         `gorm:"column:policy_version_id;not null;index:idx_retention_rule_policy;uniqueIndex:idx_retention_rule_unique,priority:1"`
	RuleOrder              int                          `gorm:"column:rule_order;not null"`
	DataClass              string                       `gorm:"column:data_class;type:text;not null;index:idx_retention_rule_class;uniqueIndex:idx_retention_rule_unique,priority:2"`
	Enabled                bool                         `gorm:"column:enabled;not null;index:idx_retention_rule_enabled"`
	RetentionDays          int                          `gorm:"column:retention_days;not null"`
	BatchSize              int                          `gorm:"column:batch_size;not null"`
	RequireFinalizedRollup bool                         `gorm:"column:require_finalized_rollup;not null"`
	CreatedAt              string                       `gorm:"column:created_at;type:text;not null"`
	PolicyVersion          retentionPolicyVersionRecord `gorm:"foreignKey:PolicyVersionID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (retentionPolicyRuleRecord) TableName() string {
	return "retention_policy_rules"
}

type retentionJobRecord struct {
	ID              uint                         `gorm:"column:id;primaryKey;autoIncrement"`
	PolicyVersionID uint                         `gorm:"column:policy_version_id;not null;index:idx_retention_job_policy"`
	Mode            string                       `gorm:"column:mode;type:text;not null;index:idx_retention_job_mode"`
	Status          string                       `gorm:"column:status;type:text;not null;index:idx_retention_job_status"`
	DryRun          bool                         `gorm:"column:dry_run;not null"`
	StartedAt       string                       `gorm:"column:started_at;type:text;not null;index:idx_retention_job_started"`
	CompletedAt     string                       `gorm:"column:completed_at;type:text;not null;default:''"`
	RequestedBy     string                       `gorm:"column:requested_by;type:text;not null;default:''"`
	ErrorMessage    string                       `gorm:"column:error_message;type:text;not null;default:''"`
	PolicyVersion   retentionPolicyVersionRecord `gorm:"foreignKey:PolicyVersionID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
}

func (retentionJobRecord) TableName() string {
	return "retention_jobs"
}

type retentionJobTableResultRecord struct {
	ID            uint               `gorm:"column:id;primaryKey;autoIncrement"`
	JobID         uint               `gorm:"column:job_id;not null;index:idx_retention_result_job;uniqueIndex:idx_retention_result_unique,priority:1"`
	DataClass     string             `gorm:"column:data_class;type:text;not null;index:idx_retention_result_class;uniqueIndex:idx_retention_result_unique,priority:2"`
	StorageTable  string             `gorm:"column:table_name;type:text;not null;uniqueIndex:idx_retention_result_unique,priority:3"`
	CutoffTS      string             `gorm:"column:cutoff_ts;type:text;not null;index:idx_retention_result_cutoff"`
	RetentionDays int                `gorm:"column:retention_days;not null"`
	BatchSize     int                `gorm:"column:batch_size;not null"`
	CandidateRows int64              `gorm:"column:candidate_rows;not null;default:0"`
	HeldRows      int64              `gorm:"column:held_rows;not null;default:0"`
	EligibleRows  int64              `gorm:"column:eligible_rows;not null;default:0"`
	BlockedRows   int64              `gorm:"column:blocked_rows;not null;default:0"`
	DeletedRows   int64              `gorm:"column:deleted_rows;not null;default:0"`
	Status        string             `gorm:"column:status;type:text;not null;index:idx_retention_result_status"`
	Message       string             `gorm:"column:message;type:text;not null;default:''"`
	StartedAt     string             `gorm:"column:started_at;type:text;not null"`
	CompletedAt   string             `gorm:"column:completed_at;type:text;not null"`
	Job           retentionJobRecord `gorm:"foreignKey:JobID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (retentionJobTableResultRecord) TableName() string {
	return "retention_job_table_results"
}

type legalHoldRecord struct {
	ID            uint   `gorm:"column:id;primaryKey;autoIncrement"`
	HoldID        string `gorm:"column:hold_id;type:text;not null;uniqueIndex:idx_legal_hold_id"`
	DataClass     string `gorm:"column:data_class;type:text;not null;index:idx_legal_hold_class_range,priority:1"`
	RequestID     string `gorm:"column:request_id;type:text;not null;default:'';index:idx_legal_hold_request"`
	Active        bool   `gorm:"column:active;not null;index:idx_legal_hold_active"`
	StartTS       string `gorm:"column:start_ts;type:text;not null;default:'';index:idx_legal_hold_class_range,priority:2"`
	EndTS         string `gorm:"column:end_ts;type:text;not null;default:'';index:idx_legal_hold_class_range,priority:3"`
	ReasonCode    string `gorm:"column:reason_code;type:text;not null;default:''"`
	Subject       string `gorm:"column:subject;type:text;not null;default:''"`
	CreatedBy     string `gorm:"column:created_by;type:text;not null;default:''"`
	CreatedAt     string `gorm:"column:created_at;type:text;not null;index:idx_legal_hold_created"`
	ReleasedBy    string `gorm:"column:released_by;type:text;not null;default:''"`
	ReleasedAt    string `gorm:"column:released_at;type:text;not null;default:''"`
	ReleaseReason string `gorm:"column:release_reason;type:text;not null;default:''"`
	Notes         string `gorm:"column:notes;type:text;not null;default:''"`
}

func (legalHoldRecord) TableName() string {
	return "legal_holds"
}

type legalHoldAuditEventRecord struct {
	ID             uint   `gorm:"column:id;primaryKey;autoIncrement"`
	HoldID         string `gorm:"column:hold_id;type:text;not null;index:idx_legal_hold_audit_hold"`
	EventType      string `gorm:"column:event_type;type:text;not null;index:idx_legal_hold_audit_event"`
	DataClass      string `gorm:"column:data_class;type:text;not null"`
	RequestID      string `gorm:"column:request_id;type:text;not null;default:''"`
	StartTS        string `gorm:"column:start_ts;type:text;not null;default:''"`
	EndTS          string `gorm:"column:end_ts;type:text;not null;default:''"`
	PreviousActive bool   `gorm:"column:previous_active;not null"`
	NewActive      bool   `gorm:"column:new_active;not null"`
	Actor          string `gorm:"column:actor;type:text;not null;default:''"`
	ReasonCode     string `gorm:"column:reason_code;type:text;not null;default:''"`
	Message        string `gorm:"column:message;type:text;not null;default:''"`
	TS             string `gorm:"column:ts;type:text;not null;index:idx_legal_hold_audit_ts"`
}

func (legalHoldAuditEventRecord) TableName() string {
	return "legal_hold_audit_events"
}

type requestAttemptRecord struct {
	RequestID        string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_attempt_request"`
	AttemptIndex     int    `gorm:"column:attempt_index;primaryKey;not null"`
	TS               string `gorm:"column:ts;type:text;not null;index:idx_request_attempt_ts"`
	Provider         string `gorm:"column:provider;type:text;not null;index:idx_request_attempt_provider_model,priority:1"`
	Model            string `gorm:"column:model;type:text;not null;index:idx_request_attempt_provider_model,priority:2"`
	Dialect          string `gorm:"column:dialect;type:text;not null"`
	EndpointHost     string `gorm:"column:endpoint_host;type:text;not null"`
	DurationMS       int64  `gorm:"column:duration_ms;not null"`
	StatusCode       int    `gorm:"column:status_code;not null;index:idx_request_attempt_status"`
	ErrorClass       string `gorm:"column:error_class;type:text;not null;index:idx_request_attempt_error"`
	ErrorMessage     string `gorm:"column:error_message;type:text;not null"`
	Retryable        bool   `gorm:"column:retryable;not null"`
	TimedOut         bool   `gorm:"column:timed_out;not null;index:idx_request_attempt_timeout"`
	ClientCanceled   bool   `gorm:"column:client_canceled;not null;index:idx_request_attempt_cancel"`
	Selected         bool   `gorm:"column:selected;not null"`
	FallbackReason   string `gorm:"column:fallback_reason;type:text;not null"`
	RequestBytes     int64  `gorm:"column:request_bytes;not null"`
	ResponseBytes    int64  `gorm:"column:response_bytes;not null"`
	AttemptTimeoutMS int    `gorm:"column:attempt_timeout_ms;not null"`
	RetryAfterMS     int64  `gorm:"column:retry_after_ms;not null;default:0"`
	ReasoningTokens  *int   `gorm:"column:reasoning_tokens"`
}

func (requestAttemptRecord) TableName() string {
	return "request_attempts"
}

type requestTraceEventRecord struct {
	RequestID  string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_trace_request"`
	Seq        int    `gorm:"column:seq;primaryKey;not null"`
	TS         string `gorm:"column:ts;type:text;not null;index:idx_request_trace_ts"`
	Event      string `gorm:"column:event;type:text;not null;index:idx_request_trace_event"`
	Message    string `gorm:"column:message;type:text;not null"`
	Provider   string `gorm:"column:provider;type:text;not null"`
	Model      string `gorm:"column:model;type:text;not null"`
	Dialect    string `gorm:"column:dialect;type:text;not null"`
	DurationMS int64  `gorm:"column:duration_ms;not null"`
	StatusCode int    `gorm:"column:status_code;not null"`
	ErrorClass string `gorm:"column:error_class;type:text;not null;index:idx_request_trace_error"`
	Retryable  bool   `gorm:"column:retryable;not null"`
	Attempt    int    `gorm:"column:attempt;not null"`
}

func (requestTraceEventRecord) TableName() string {
	return "request_trace_events"
}

type requestTrafficShapeEventRecord struct {
	RequestID            string `gorm:"column:request_id;type:text;primaryKey"`
	Seq                  int    `gorm:"column:seq;primaryKey"`
	Scope                string `gorm:"column:scope;type:text;not null"`
	Bucket               string `gorm:"column:bucket;type:text;not null"`
	Decision             string `gorm:"column:decision;type:text;not null"`
	Cost                 int    `gorm:"column:cost;not null;default:0"`
	RetryAfterMS         int64  `gorm:"column:retry_after_ms;not null;default:0"`
	QueueWaitMS          int64  `gorm:"column:queue_wait_ms;not null;default:0"`
	EstimatedInputTokens int    `gorm:"column:estimated_input_tokens;not null;default:0"`
	ReservedOutputTokens int    `gorm:"column:reserved_output_tokens;not null;default:0"`
	TotalReservedTokens  int    `gorm:"column:total_reserved_tokens;not null;default:0"`
}

func (requestTrafficShapeEventRecord) TableName() string {
	return "request_traffic_shape_events"
}

type requestUpstreamShapeEventRecord struct {
	RequestID            string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_upstream_shape_request"`
	Seq                  int    `gorm:"column:seq;primaryKey;not null"`
	TS                   string `gorm:"column:ts;type:text;not null;index:idx_request_upstream_shape_ts"`
	Scope                string `gorm:"column:scope;type:text;not null"`
	Provider             string `gorm:"column:provider;type:text;not null"`
	ModelRef             string `gorm:"column:model_ref;type:text;not null"`
	Model                string `gorm:"column:model;type:text;not null"`
	Dialect              string `gorm:"column:dialect;type:text;not null"`
	Bucket               string `gorm:"column:bucket;type:text;not null"`
	Decision             string `gorm:"column:decision;type:text;not null"`
	RetryAfterMS         int64  `gorm:"column:retry_after_ms;not null"`
	EstimatedInputTokens int    `gorm:"column:estimated_input_tokens;not null"`
	ReservedOutputTokens int    `gorm:"column:reserved_output_tokens;not null"`
	TotalReservedTokens  int    `gorm:"column:total_reserved_tokens;not null"`
	BackoffReason        string `gorm:"column:backoff_reason;type:text;not null"`
	QueueWaitMS          int64  `gorm:"column:queue_wait_ms;not null"`
}

func (requestUpstreamShapeEventRecord) TableName() string {
	return "request_upstream_shape_events"
}

type requestShapeRecord struct {
	RequestID                  string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_shape_request"`
	TS                         string `gorm:"column:ts;type:text;not null;index:idx_request_shape_ts"`
	InboundDialect             string `gorm:"column:inbound_dialect;type:text;not null;index:idx_request_shape_inbound"`
	RequestedModel             string `gorm:"column:requested_model;type:text;not null;index:idx_request_shape_requested_model"`
	ResolvedGroup              string `gorm:"column:resolved_group;type:text;not null;default:'';index:idx_request_shape_group"`
	Client                     string `gorm:"column:client;type:text;not null;default:'';index:idx_request_shape_client"`
	Stream                     bool   `gorm:"column:stream;not null;default:false;index:idx_request_shape_stream"`
	InputItemCount             int    `gorm:"column:input_item_count;not null;default:0"`
	MessageCount               int    `gorm:"column:message_count;not null;default:0"`
	SystemMessageCount         int    `gorm:"column:system_message_count;not null;default:0"`
	DeveloperMessageCount      int    `gorm:"column:developer_message_count;not null;default:0"`
	UserMessageCount           int    `gorm:"column:user_message_count;not null;default:0"`
	AssistantMessageCount      int    `gorm:"column:assistant_message_count;not null;default:0"`
	ToolResultCount            int    `gorm:"column:tool_result_count;not null;default:0"`
	FunctionCallOutputCount    int    `gorm:"column:function_call_output_count;not null;default:0"`
	ToolCount                  int    `gorm:"column:tool_count;not null;default:0;index:idx_request_shape_tool_count"`
	ToolChoiceMode             string `gorm:"column:tool_choice_mode;type:text;not null;default:'';index:idx_request_shape_tool_choice"`
	ParallelToolCallsPresent   bool   `gorm:"column:parallel_tool_calls_present;not null;default:false"`
	ResponseFormatPresent      bool   `gorm:"column:response_format_present;not null;default:false"`
	StructuredOutputPresent    bool   `gorm:"column:structured_output_present;not null;default:false;index:idx_request_shape_structured"`
	ReasoningPresent           bool   `gorm:"column:reasoning_present;not null;default:false;index:idx_request_shape_reasoning"`
	ReasoningEffortBucket      string `gorm:"column:reasoning_effort_bucket;type:text;not null;default:''"`
	ReasoningBudgetBucket      string `gorm:"column:reasoning_budget_bucket;type:text;not null;default:''"`
	IncludePresent             bool   `gorm:"column:include_present;not null;default:false"`
	TruncationPresent          bool   `gorm:"column:truncation_present;not null;default:false"`
	MetadataPresent            bool   `gorm:"column:metadata_present;not null;default:false"`
	StorePresent               bool   `gorm:"column:store_present;not null;default:false"`
	PreviousResponseIDPresent  bool   `gorm:"column:previous_response_id_present;not null;default:false"`
	ImageCount                 int    `gorm:"column:image_count;not null;default:0;index:idx_request_shape_image_count"`
	AudioPresent               bool   `gorm:"column:audio_present;not null;default:false"`
	VideoPresent               bool   `gorm:"column:video_present;not null;default:false"`
	InputTextBytesBucket       string `gorm:"column:input_text_bytes_bucket;type:text;not null;default:''"`
	ToolSchemaBytesBucket      string `gorm:"column:tool_schema_bytes_bucket;type:text;not null;default:'';index:idx_request_shape_tool_schema_bucket"`
	TotalRequestBytesBucket    string `gorm:"column:total_request_bytes_bucket;type:text;not null;default:'';index:idx_request_shape_bytes_bucket"`
	EstimatedInputTokensBucket string `gorm:"column:estimated_input_tokens_bucket;type:text;not null;default:'';index:idx_request_shape_input_token_bucket"`
	RequestedOutputCapField    string `gorm:"column:requested_output_cap_field;type:text;not null;default:''"`
	RequestedOutputCapBucket   string `gorm:"column:requested_output_cap_bucket;type:text;not null;default:'';index:idx_request_shape_output_cap_bucket"`
	ToolSchemaFingerprint      string `gorm:"column:tool_schema_fingerprint;type:text;not null;default:'';index:idx_request_shape_tool_schema_fp"`
	RequestShapeFingerprint    string `gorm:"column:request_shape_fingerprint;type:text;not null;default:'';index:idx_request_shape_fp"`
}

func (requestShapeRecord) TableName() string {
	return "request_shapes"
}

type requestTranslationShapeRecord struct {
	RequestID                    string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_translation_shape_request"`
	AttemptIndex                 int    `gorm:"column:attempt_index;primaryKey;not null"`
	TS                           string `gorm:"column:ts;type:text;not null;index:idx_request_translation_shape_ts"`
	Provider                     string `gorm:"column:provider;type:text;not null;index:idx_request_translation_shape_provider_model,priority:1"`
	Model                        string `gorm:"column:model;type:text;not null;index:idx_request_translation_shape_provider_model,priority:2"`
	Dialect                      string `gorm:"column:dialect;type:text;not null;index:idx_request_translation_shape_dialect"`
	BridgeDirection              string `gorm:"column:bridge_direction;type:text;not null;default:'';index:idx_request_translation_shape_bridge"`
	EndpointPath                 string `gorm:"column:endpoint_path;type:text;not null;default:''"`
	TranslatedStream             bool   `gorm:"column:translated_stream;not null;default:false;index:idx_request_translation_shape_stream"`
	TranslatedToolCount          int    `gorm:"column:translated_tool_count;not null;default:0;index:idx_request_translation_shape_tool_count"`
	TranslatedToolChoiceMode     string `gorm:"column:translated_tool_choice_mode;type:text;not null;default:'';index:idx_request_translation_shape_tool_choice"`
	TranslatedOutputCapField     string `gorm:"column:translated_output_cap_field;type:text;not null;default:''"`
	TranslatedOutputCapBucket    string `gorm:"column:translated_output_cap_bucket;type:text;not null;default:'';index:idx_request_translation_shape_output_cap"`
	TranslatedReasoningControl   string `gorm:"column:translated_reasoning_control;type:text;not null;default:'';index:idx_request_translation_shape_reasoning"`
	TranslatedRequestBytesBucket string `gorm:"column:translated_request_bytes_bucket;type:text;not null;default:'';index:idx_request_translation_shape_bytes"`
	FieldsStrippedCount          int    `gorm:"column:fields_stripped_count;not null;default:0"`
	FieldsRewrittenCount         int    `gorm:"column:fields_rewritten_count;not null;default:0"`
	UnsupportedFieldsPresent     bool   `gorm:"column:unsupported_fields_present;not null;default:false;index:idx_request_translation_shape_unsupported"`
	TranslationWarningCount      int    `gorm:"column:translation_warning_count;not null;default:0"`
	RequestShapeFingerprint      string `gorm:"column:request_shape_fingerprint;type:text;not null;default:'';index:idx_request_translation_shape_request_fp"`
	ToolSchemaFingerprint        string `gorm:"column:tool_schema_fingerprint;type:text;not null;default:'';index:idx_request_translation_shape_tool_fp"`
}

func (requestTranslationShapeRecord) TableName() string {
	return "request_translation_shapes"
}

type requestTokenEstimateRecord struct {
	RequestID                     string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_token_estimate_request"`
	TS                            string `gorm:"column:ts;type:text;not null;index:idx_request_token_estimate_ts"`
	InboundDialect                string `gorm:"column:inbound_dialect;type:text;not null;index:idx_request_token_estimate_inbound"`
	RequestedModel                string `gorm:"column:requested_model;type:text;not null;index:idx_request_token_estimate_requested_model"`
	ResolvedGroup                 string `gorm:"column:resolved_group;type:text;not null;default:'';index:idx_request_token_estimate_group"`
	EstimateMethod                string `gorm:"column:estimate_method;type:text;not null;default:''"`
	EstimateVersion               string `gorm:"column:estimate_version;type:text;not null;default:''"`
	EstimatedInputTokens          int    `gorm:"column:estimated_input_tokens;not null;default:0;index:idx_request_token_estimate_input"`
	EstimatedToolSchemaTokens     int    `gorm:"column:estimated_tool_schema_tokens;not null;default:0"`
	EstimatedImageTokens          int    `gorm:"column:estimated_image_tokens;not null;default:0"`
	EstimatedAudioTokens          int    `gorm:"column:estimated_audio_tokens;not null;default:0"`
	EstimatedTotalInputTokens     int    `gorm:"column:estimated_total_input_tokens;not null;default:0;index:idx_request_token_estimate_total_input"`
	RequestedOutputCapTokens      int    `gorm:"column:requested_output_cap_tokens;not null;default:0;index:idx_request_token_estimate_output_cap"`
	RequestedOutputCapField       string `gorm:"column:requested_output_cap_field;type:text;not null;default:''"`
	RouterDefaultOutputCapApplied bool   `gorm:"column:router_default_output_cap_applied;not null;default:false"`
	TotalReservedTokens           int    `gorm:"column:total_reserved_tokens;not null;default:0;index:idx_request_token_estimate_reserved"`
	RequestBytes                  int    `gorm:"column:request_bytes;not null;default:0;index:idx_request_token_estimate_bytes"`
	TranslatedRequestBytes        int    `gorm:"column:translated_request_bytes;not null;default:0"`
	EstimateWarningCount          int    `gorm:"column:estimate_warning_count;not null;default:0"`
	EstimateConfidenceBucket      string `gorm:"column:estimate_confidence_bucket;type:text;not null;default:'';index:idx_request_token_estimate_confidence"`
}

func (requestTokenEstimateRecord) TableName() string {
	return "request_token_estimates"
}

type requestTranslationFieldEventRecord struct {
	RequestID    string `gorm:"column:request_id;primaryKey;type:text;index:idx_request_translation_field_request"`
	AttemptIndex int    `gorm:"column:attempt_index;primaryKey;not null;index:idx_request_translation_field_attempt"`
	Seq          int    `gorm:"column:seq;primaryKey;not null"`
	FieldName    string `gorm:"column:field_name;type:text;not null;index:idx_request_translation_field_name"`
	Action       string `gorm:"column:action;type:text;not null;index:idx_request_translation_field_action"`
	Reason       string `gorm:"column:reason;type:text;not null;default:''"`
}

func (requestTranslationFieldEventRecord) TableName() string {
	return "request_translation_field_events"
}

type decisionShapeFeatureRecord struct {
	RequestID   string `gorm:"column:request_id;primaryKey;type:text;index:idx_decision_shape_request" json:"requestId"`
	Seq         int    `gorm:"column:seq;primaryKey;not null" json:"seq"`
	FeatureName string `gorm:"column:feature_name;type:text;not null;index:idx_decision_shape_feature" json:"featureName"`
	BoolValue   bool   `gorm:"column:bool_value;not null" json:"boolValue"`
	IntValue    int    `gorm:"column:int_value;not null" json:"intValue"`
	TextValue   string `gorm:"column:text_value;type:text;not null" json:"textValue"`
}

func (decisionShapeFeatureRecord) TableName() string {
	return "request_decision_shape_features"
}

type decisionTargetCandidateRecord struct {
	RequestID                   string `gorm:"column:request_id;primaryKey;type:text;index:idx_decision_candidate_request" json:"requestId"`
	CandidateIndex              int    `gorm:"column:candidate_index;primaryKey;not null" json:"candidateIndex"`
	GroupTargetIndex            int    `gorm:"column:group_target_index;not null" json:"groupTargetIndex"`
	Provider                    string `gorm:"column:provider;type:text;not null;index:idx_decision_candidate_provider_model,priority:1" json:"provider"`
	Model                       string `gorm:"column:model;type:text;not null;index:idx_decision_candidate_provider_model,priority:2" json:"model"`
	ModelRef                    string `gorm:"column:model_ref;type:text;not null" json:"modelRef"`
	Dialect                     string `gorm:"column:dialect;type:text;not null" json:"dialect"`
	Weight                      int    `gorm:"column:weight;not null" json:"weight"`
	ToolOnly                    bool   `gorm:"column:tool_only;not null" json:"toolOnly"`
	ContextTokens               int    `gorm:"column:context_tokens;not null;default:0" json:"contextTokens"`
	MaxEstimatedInputTokens     int    `gorm:"column:max_estimated_input_tokens;not null;default:0" json:"maxEstimatedInputTokens"`
	MaxRequestedOutputTokens    int    `gorm:"column:max_requested_output_tokens;not null;default:0" json:"maxRequestedOutputTokens"`
	MaxRequestBytes             int    `gorm:"column:max_request_bytes;not null;default:0" json:"maxRequestBytes"`
	MaxToolSchemaBytes          int    `gorm:"column:max_tool_schema_bytes;not null;default:0" json:"maxToolSchemaBytes"`
	EstimatedTotalInputTokens   int    `gorm:"column:estimated_total_input_tokens;not null;default:0" json:"estimatedTotalInputTokens"`
	RequestedOutputCapTokens    int    `gorm:"column:requested_output_cap_tokens;not null;default:0" json:"requestedOutputCapTokens"`
	EstimatedTotalWithOutputCap int    `gorm:"column:estimated_total_with_output_cap;not null;default:0" json:"estimatedTotalWithOutputCap"`
	RequestBytes                int    `gorm:"column:request_bytes;not null;default:0" json:"requestBytes"`
	ToolSchemaBytes             int    `gorm:"column:tool_schema_bytes;not null;default:0" json:"toolSchemaBytes"`
	ContextHeadroomTokens       int    `gorm:"column:context_headroom_tokens;not null;default:0" json:"contextHeadroomTokens"`
	ContextFit                  bool   `gorm:"column:context_fit;not null;default:false;index:idx_decision_candidate_context_fit" json:"contextFit"`
	RequestBytesFit             bool   `gorm:"column:request_bytes_fit;not null;default:false" json:"requestBytesFit"`
	ToolSchemaFit               bool   `gorm:"column:tool_schema_fit;not null;default:false" json:"toolSchemaFit"`
	EligibilityDecision         string `gorm:"column:eligibility_decision;type:text;not null;default:'';index:idx_decision_candidate_eligibility_decision" json:"eligibilityDecision"`
	EligibilityReason           string `gorm:"column:eligibility_reason;type:text;not null;default:'';index:idx_decision_candidate_eligibility_reason" json:"eligibilityReason"`
	InputImage                  bool   `gorm:"column:input_image;not null;default:false;index:idx_decision_candidate_input_image" json:"inputImage"`
	OutputImage                 bool   `gorm:"column:output_image;not null;default:false" json:"outputImage"`
	ToolSupport                 bool   `gorm:"column:tool_support;not null;default:false;index:idx_decision_candidate_tool_support" json:"toolSupport"`
	ForcedToolChoice            bool   `gorm:"column:forced_tool_choice;not null;default:false" json:"forcedToolChoice"`
	StructuredOutput            bool   `gorm:"column:structured_output;not null;default:false" json:"structuredOutput"`
	HonorsMaxTokens             bool   `gorm:"column:honors_max_tokens;not null" json:"honorsMaxTokens"`
	ReasoningSupport            bool   `gorm:"column:reasoning_support;not null;default:false;index:idx_decision_candidate_reasoning_support" json:"reasoningSupport"`
	ReasoningMode               string `gorm:"column:reasoning_mode;type:text;not null;default:''" json:"reasoningMode"`
	ReasoningControl            string `gorm:"column:reasoning_control;type:text;not null;default:''" json:"reasoningControl"`
	ReasoningDefault            bool   `gorm:"column:reasoning_default;not null;default:false" json:"reasoningDefault"`
	ReasoningStream             string `gorm:"column:reasoning_stream_block;type:text;not null;default:''" json:"reasoningStreamBlock"`
	ValidationStatus            string `gorm:"column:validation_status;type:text;not null;default:'';index:idx_decision_candidate_validation_status" json:"validationStatus"`
	ValidationAge               string `gorm:"column:validation_age_bucket;type:text;not null;default:''" json:"validationAgeBucket"`
	Eligible                    bool   `gorm:"column:eligible;not null;index:idx_decision_candidate_eligible" json:"eligible"`
	Selected                    bool   `gorm:"column:selected;not null;index:idx_decision_candidate_selected" json:"selected"`
}

func (decisionTargetCandidateRecord) TableName() string {
	return "request_target_candidates"
}

type decisionTargetFilterReasonRecord struct {
	RequestID      string `gorm:"column:request_id;primaryKey;type:text;index:idx_decision_filter_request" json:"requestId"`
	Seq            int    `gorm:"column:seq;primaryKey;not null" json:"seq"`
	CandidateIndex int    `gorm:"column:candidate_index;not null;index:idx_decision_filter_candidate" json:"candidateIndex"`
	Stage          string `gorm:"column:stage;type:text;not null;index:idx_decision_filter_stage" json:"stage"`
	Reason         string `gorm:"column:reason;type:text;not null;index:idx_decision_filter_reason" json:"reason"`
}

func (decisionTargetFilterReasonRecord) TableName() string {
	return "request_target_filter_reasons"
}

type routingDecisionRecord struct {
	RequestID              string `gorm:"column:request_id;primaryKey;type:text;index:idx_routing_decision_request" json:"requestId"`
	Seq                    int    `gorm:"column:seq;primaryKey;not null" json:"seq"`
	Strategy               string `gorm:"column:strategy;type:text;not null;index:idx_routing_decision_strategy" json:"strategy"`
	SelectedCandidateIndex int    `gorm:"column:selected_candidate_index;not null" json:"selectedCandidateIndex"`
	Provider               string `gorm:"column:provider;type:text;not null;index:idx_routing_decision_provider_model,priority:1" json:"provider"`
	Model                  string `gorm:"column:model;type:text;not null;index:idx_routing_decision_provider_model,priority:2" json:"model"`
	Dialect                string `gorm:"column:dialect;type:text;not null" json:"dialect"`
	FallbackCount          int    `gorm:"column:fallback_count;not null" json:"fallbackCount"`
	ClassLabel             string `gorm:"column:class_label;type:text;not null" json:"classLabel"`
}

func (routingDecisionRecord) TableName() string {
	return "request_routing_decisions"
}

type routingSignalRecord struct {
	RequestID      string  `gorm:"column:request_id;primaryKey;type:text;index:idx_routing_signal_request" json:"requestId"`
	Seq            int     `gorm:"column:seq;primaryKey;not null" json:"seq"`
	Strategy       string  `gorm:"column:strategy;type:text;not null;index:idx_routing_signal_strategy" json:"strategy"`
	SignalName     string  `gorm:"column:signal_name;type:text;not null;index:idx_routing_signal_name" json:"signalName"`
	Source         string  `gorm:"column:source;type:text;not null;default:''" json:"source"`
	CandidateIndex int     `gorm:"column:candidate_index;not null;default:-1;index:idx_routing_signal_candidate" json:"candidateIndex"`
	BoolValue      bool    `gorm:"column:bool_value;not null;default:false" json:"boolValue"`
	IntValue       int     `gorm:"column:int_value;not null;default:0" json:"intValue"`
	FloatValue     float64 `gorm:"column:float_value;not null;default:0" json:"floatValue"`
	TextValue      string  `gorm:"column:text_value;type:text;not null;default:''" json:"textValue"`
}

func (routingSignalRecord) TableName() string {
	return "request_routing_signals"
}

type dynamicScoreTermRecord struct {
	RequestID          string  `gorm:"column:request_id;primaryKey;type:text;index:idx_dynamic_score_term_request" json:"requestId"`
	Seq                int     `gorm:"column:seq;primaryKey;not null" json:"seq"`
	CandidateIndex     int     `gorm:"column:candidate_index;not null;index:idx_dynamic_score_term_candidate" json:"candidateIndex"`
	Rank               int     `gorm:"column:rank;not null;index:idx_dynamic_score_term_rank" json:"rank"`
	Provider           string  `gorm:"column:provider;type:text;not null;index:idx_dynamic_score_term_provider_model,priority:1" json:"provider"`
	Model              string  `gorm:"column:model;type:text;not null;index:idx_dynamic_score_term_provider_model,priority:2" json:"model"`
	Dialect            string  `gorm:"column:dialect;type:text;not null" json:"dialect"`
	TermName           string  `gorm:"column:term_name;type:text;not null;index:idx_dynamic_score_term_name" json:"termName"`
	ScoreName          string  `gorm:"column:score_name;type:text;not null;default:'';index:idx_dynamic_score_score_name" json:"scoreName"`
	Weight             float64 `gorm:"column:weight;not null;default:0" json:"weight"`
	Value              float64 `gorm:"column:value;not null;default:0" json:"value"`
	Contribution       float64 `gorm:"column:contribution;not null;default:0" json:"contribution"`
	FinalScore         float64 `gorm:"column:final_score;not null;default:0" json:"finalScore"`
	ValueBucket        string  `gorm:"column:value_bucket;type:text;not null;default:'';index:idx_dynamic_score_value_bucket" json:"valueBucket"`
	ContributionBucket string  `gorm:"column:contribution_bucket;type:text;not null;default:'';index:idx_dynamic_score_contribution_bucket" json:"contributionBucket"`
	FinalScoreBucket   string  `gorm:"column:final_score_bucket;type:text;not null;default:'';index:idx_dynamic_score_final_bucket" json:"finalScoreBucket"`
	ObservationCount   int     `gorm:"column:observation_count;not null;default:0" json:"observationCount"`
	Selected           bool    `gorm:"column:selected;not null;default:false;index:idx_dynamic_score_term_selected" json:"selected"`
}

func (dynamicScoreTermRecord) TableName() string {
	return "request_dynamic_score_terms"
}

type policyExecutionRecord struct {
	RequestID              string `gorm:"column:request_id;primaryKey;type:text;index:idx_policy_execution_request" json:"requestId"`
	Seq                    int    `gorm:"column:seq;primaryKey;not null" json:"seq"`
	Strategy               string `gorm:"column:strategy;type:text;not null;index:idx_policy_execution_strategy" json:"strategy"`
	PolicyKind             string `gorm:"column:policy_kind;type:text;not null;index:idx_policy_execution_kind" json:"policyKind"`
	Outcome                string `gorm:"column:outcome;type:text;not null;index:idx_policy_execution_outcome" json:"outcome"`
	DurationMS             int64  `gorm:"column:duration_ms;not null;default:0" json:"durationMs"`
	EligibleTargetCount    int    `gorm:"column:eligible_target_count;not null;default:0" json:"eligibleTargetCount"`
	AllTargetCount         int    `gorm:"column:all_target_count;not null;default:0" json:"allTargetCount"`
	SelectedCandidateIndex int    `gorm:"column:selected_candidate_index;not null" json:"selectedCandidateIndex"`
	FallbackCount          int    `gorm:"column:fallback_count;not null;default:0" json:"fallbackCount"`
	ClassLabel             string `gorm:"column:class_label;type:text;not null;default:''" json:"classLabel"`
	ErrorClass             string `gorm:"column:error_class;type:text;not null;default:''" json:"errorClass"`
	ErrorMessage           string `gorm:"column:error_message;type:text;not null;default:''" json:"errorMessage"`
	TerminalErrorType      string `gorm:"column:terminal_error_type;type:text;not null;default:'';index:idx_policy_execution_terminal" json:"terminalErrorType"`
}

func (policyExecutionRecord) TableName() string {
	return "request_policy_executions"
}

type fallbackTransitionRecord struct {
	RequestID              string `gorm:"column:request_id;primaryKey;type:text;index:idx_fallback_transition_request" json:"requestId"`
	Seq                    int    `gorm:"column:seq;primaryKey;not null" json:"seq"`
	AttemptIndex           int    `gorm:"column:attempt_index;not null;index:idx_fallback_transition_attempt" json:"attemptIndex"`
	FailedCandidateIndex   int    `gorm:"column:failed_candidate_index;not null;index:idx_fallback_transition_failed_candidate" json:"failedCandidateIndex"`
	FallbackCandidateIndex int    `gorm:"column:fallback_candidate_index;not null;index:idx_fallback_transition_fallback_candidate" json:"fallbackCandidateIndex"`
	FailedProvider         string `gorm:"column:failed_provider;type:text;not null;index:idx_fallback_transition_failed,priority:1" json:"failedProvider"`
	FailedModel            string `gorm:"column:failed_model;type:text;not null;index:idx_fallback_transition_failed,priority:2" json:"failedModel"`
	FailedDialect          string `gorm:"column:failed_dialect;type:text;not null" json:"failedDialect"`
	FallbackProvider       string `gorm:"column:fallback_provider;type:text;not null;index:idx_fallback_transition_fallback,priority:1" json:"fallbackProvider"`
	FallbackModel          string `gorm:"column:fallback_model;type:text;not null;index:idx_fallback_transition_fallback,priority:2" json:"fallbackModel"`
	FallbackDialect        string `gorm:"column:fallback_dialect;type:text;not null" json:"fallbackDialect"`
	FallbackReason         string `gorm:"column:fallback_reason;type:text;not null;index:idx_fallback_transition_reason" json:"fallbackReason"`
	ErrorClass             string `gorm:"column:error_class;type:text;not null;index:idx_fallback_transition_error_class" json:"errorClass"`
	Retryable              bool   `gorm:"column:retryable;not null;default:false;index:idx_fallback_transition_retryable" json:"retryable"`
	FallbackSucceeded      bool   `gorm:"column:fallback_succeeded;not null;default:false;index:idx_fallback_transition_succeeded" json:"fallbackSucceeded"`
}

func (fallbackTransitionRecord) TableName() string {
	return "request_fallback_transitions"
}

type decisionCacheReasonRecord struct {
	RequestID      string `gorm:"column:request_id;primaryKey;type:text;index:idx_decision_cache_request" json:"requestId"`
	Seq            int    `gorm:"column:seq;primaryKey;not null" json:"seq"`
	Status         string `gorm:"column:status;type:text;not null;index:idx_decision_cache_status" json:"status"`
	Reason         string `gorm:"column:reason;type:text;not null;index:idx_decision_cache_reason" json:"reason"`
	CandidateIndex int    `gorm:"column:candidate_index;not null" json:"candidateIndex"`
	Provider       string `gorm:"column:provider;type:text;not null" json:"provider"`
	Model          string `gorm:"column:model;type:text;not null" json:"model"`
	Dialect        string `gorm:"column:dialect;type:text;not null" json:"dialect"`
}

func (decisionCacheReasonRecord) TableName() string {
	return "request_cache_reasons"
}

type requestErrorRecord struct {
	RequestID    string `gorm:"column:request_id;primaryKey;type:text"`
	TS           string `gorm:"column:ts;type:text;not null;index:idx_request_error_ts"`
	Status       int    `gorm:"column:status;not null;index:idx_request_error_status"`
	ErrorType    string `gorm:"column:error_type;type:text;not null;index:idx_request_error_type"`
	ErrorClass   string `gorm:"column:error_class;type:text;not null;index:idx_request_error_class"`
	ErrorMessage string `gorm:"column:error_message;type:text;not null"`
	Retryable    bool   `gorm:"column:retryable;not null"`
	Attempts     int    `gorm:"column:attempts;not null"`
	Provider     string `gorm:"column:provider;type:text;not null"`
	Model        string `gorm:"column:model;type:text;not null"`
	Dialect      string `gorm:"column:dialect;type:text;not null"`
}

func (requestErrorRecord) TableName() string {
	return "request_errors"
}

type requestUpstreamErrorDetailRecord struct {
	RequestID    string `gorm:"column:request_id;primaryKey;type:text;index:idx_upstream_error_detail_request"`
	AttemptIndex int    `gorm:"column:attempt_index;primaryKey;not null;index:idx_upstream_error_detail_attempt"`
	Seq          int    `gorm:"column:seq;primaryKey;not null"`
	TS           string `gorm:"column:ts;type:text;not null;index:idx_upstream_error_detail_ts"`
	StatusCode   int    `gorm:"column:status_code;not null;index:idx_upstream_error_detail_status"`
	ErrorClass   string `gorm:"column:error_class;type:text;not null;index:idx_upstream_error_detail_class"`
	FieldName    string `gorm:"column:field_name;type:text;not null;index:idx_upstream_error_detail_field"`
	FieldValue   string `gorm:"column:field_value;type:text;not null"`
	Source       string `gorm:"column:source;type:text;not null"`
	Truncated    bool   `gorm:"column:truncated;not null;default:false"`
}

func (requestUpstreamErrorDetailRecord) TableName() string {
	return "request_upstream_error_details"
}

type agg struct {
	Calls                    int64
	Errors                   int64
	Streams                  int64
	CacheHits                int64
	CacheMisses              int64
	CacheBypass              int64
	Fallbacks                int64
	Attempts                 int64
	InputTokens              int64
	OutputTokens             int64
	TotalTokens              int64
	InputCostUSD             float64
	ImageCostUSD             float64
	OutputCostUSD            float64
	TotalCostUSD             float64
	LatencyMS                int64
	MaxLatencyMS             int64
	TTFBMS                   int64
	TTFBCount                int64
	MaxTTFBMS                int64
	UpstreamMS               int64
	UpstreamMSCount          int64
	MaxUpstreamMS            int64
	DownstreamMS             int64
	DownstreamMSCount        int64
	MaxDownstreamMS          int64
	UpstreamOutputTPS        float64
	UpstreamOutputTPSCount   int64
	UpstreamTotalTPS         float64
	UpstreamTotalTPSCount    int64
	DownstreamOutputTPS      float64
	DownstreamOutputTPSCount int64
	DownstreamTotalTPS       float64
	DownstreamTotalTPSCount  int64
	CacheItemsLatest         int64
	CacheBytesLatest         int64
	CacheMaxBytesLatest      int64
	CacheOccupancyLatest     float64
	CacheItemsMax            int64
	CacheBytesMax            int64
	CacheOccupancyMax        float64
	CacheBytesSum            int64
	CacheOccupancySum        float64
	CacheSnapshotCount       int64
}

type decisionTelemetrySummary struct {
	ShapeFeatures       int64
	Candidates          int64
	FilterReasons       int64
	Decisions           int64
	RoutingSignals      int64
	ScoreTerms          int64
	PolicyExecutions    int64
	FallbackTransitions int64
	CacheReasons        int64
	ByStrategy          map[string]int64
	ByPolicyOutcome     map[string]int64
	ByPolicyErrorClass  map[string]int64
	ByFallbackReason    map[string]int64
	ByFilterReason      map[string]int64
	ByCacheReason       map[string]int64
	ByEnabledSignal     map[string]int64
	ByScoreBucket       map[string]int64
	ByThresholdBucket   map[string]int64
	ByMaxTokenBucket    map[string]int64
	ByInputTokenBucket  map[string]int64
	ByAdmissionReason   map[string]int64
}

func newUsageStore(cfg UsageDBConfig) (*usageStore, error) {
	if cfg.Enable != nil && !*cfg.Enable {
		return nil, nil
	}
	driver := strings.ToLower(defaultString(cfg.Driver, "sqlite"))
	if driver == "postgres" && cfg.DSN == "" {
		return nil, errors.New("usage_db.dsn is required for postgres")
	}
	if driver == "sqlite" && cfg.Path == "" {
		return nil, nil
	}
	store, err := OpenUsageStore(cfg)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func OpenUsageStore(cfg UsageDBConfig) (*usageStore, error) {
	db, err := openUsageDB(cfg)
	if err != nil {
		return nil, err
	}
	store := &usageStore{db: db}
	if err := store.initializeSchema(cfg.MigrationPolicy); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// initializeSchema isolates the legacy implicit initializer from the explicit
// migration startup policies. A serving process never runs application DDL
// under validate or deployment-job; both fail closed unless the ledger is
// current and its postconditions verify.
func (s *usageStore) initializeSchema(policy string) error {
	policy = strings.ToLower(strings.TrimSpace(defaultString(policy, usageDBMigrationPolicyDeploymentJob)))
	switch policy {
	case usageDBMigrationPolicyValidate, usageDBMigrationPolicyDeploymentJob:
		r, err := newUsageMigrationRunner(s.db)
		if err != nil {
			return err
		}
		status, err := r.Verify()
		if err != nil {
			return fmt.Errorf("usage migration %s: %w", policy, err)
		}
		if !UsageMigrationServingCompatible(status) {
			return fmt.Errorf("usage migration %s requires a current compatible ledger (state %s)", policy, status.State)
		}
		return nil
	case usageDBMigrationPolicyAutoSafe:
		r, err := newUsageMigrationRunner(s.db)
		if err != nil {
			return err
		}
		if err := r.ApplyPending("router-startup"); err != nil {
			return fmt.Errorf("usage migration auto-safe: %w", err)
		}
		if s.db.Dialector.Name() == "sqlite" {
			if err := r.RunAutoSafeZeroRowDataJob(context.Background(), "router-startup", requireEmptyUsageStore); err != nil {
				return fmt.Errorf("usage migration auto-safe: %w", err)
			}
		}
		status, err := r.Verify()
		if err != nil {
			return fmt.Errorf("usage migration auto-safe: %w", err)
		}
		if !UsageMigrationServingCompatible(status) {
			return fmt.Errorf("usage migration auto-safe requires a current compatible ledger (state %s)", status.State)
		}
		return nil
	default:
		return fmt.Errorf("unsupported usage migration policy %q", policy)
	}
}

// Serving is admitted only after the compatible ledger is current and every
// required bound data job has reached its checked-in validated state. A
// synthesized pending job after apply is deliberately just as unready as a
// durable running, paused, cancelled, failed, or incompatible job.
func UsageMigrationServingCompatible(status MigrationStatus) bool {
	if !status.Compatible || status.State != "current" {
		return false
	}
	for _, job := range status.Jobs {
		if job.State != migrationDataJobValidated {
			return false
		}
	}
	return true
}

func requireEmptyUsageStore(db *gorm.DB) error {
	var usageRows int64
	if err := db.Table("request_usage").Count(&usageRows).Error; err != nil {
		return fmt.Errorf("zero-row preflight: %w", err)
	}
	if usageRows != 0 {
		return errors.New("zero-row preflight found existing usage rows")
	}
	return nil
}

func OpenUsageStorePath(path string) (*usageStore, error) {
	// This compatibility helper is intentionally non-serving: callers that use
	// a bare filesystem path are local tools/tests, not a configured router. It
	// creates only the physical legacy fixture schema, without a migration
	// ledger or data-job admission decision. Production startup flows through
	// newUsageStore and always applies the serving compatibility gate.
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		return nil, err
	}
	store := &usageStore{db: db}
	if err := applyUsageExplicitBaseline(db); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := applyUsageReasoningTelemetryMigration(db); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := applyUsageTargetRegionDiagnosticsMigration(db); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := applyUsageCachedInputPricingMigration(db); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func openUsageDB(cfg UsageDBConfig) (*gorm.DB, error) {
	driver := strings.ToLower(defaultString(cfg.Driver, "sqlite"))
	switch driver {
	case "sqlite":
		if cfg.Path == "" {
			return nil, errors.New("usage db path is required")
		}
		if dir := filepath.Dir(cfg.Path); dir != "." {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
		}
		if err := ensureSQLitePrivateMode(cfg.Path); err != nil {
			return nil, err
		}
		// Foreign keys are a required part of the relational usage and control
		// plane contracts.  The SQLite driver applies this pragma to every
		// connection opened through the DSN, rather than only to whichever
		// connection happens to execute a one-time PRAGMA statement.
		db, err := gorm.Open(sqlite.Open(sqliteDSNWithForeignKeys(cfg.Path)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			return nil, err
		}
		if err := chmodSQLiteFiles(cfg.Path); err != nil {
			sqlDB, _ := db.DB()
			if sqlDB != nil {
				_ = sqlDB.Close()
			}
			return nil, err
		}
		return db, nil
	case "postgres", "postgresql":
		if cfg.DSN == "" {
			return nil, errors.New("usage db dsn is required")
		}
		return gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	default:
		return nil, fmt.Errorf("unsupported usage db driver %q", cfg.Driver)
	}
}

func sqliteDSNWithForeignKeys(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=foreign_keys(1)"
}

func ensureSQLitePrivateMode(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func chmodSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(candidate); err == nil {
			if err := os.Chmod(candidate, 0o600); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *usageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func ensureUsageRelationalSchema(db *gorm.DB) error {
	return ensureUsageRelationalSchemaExcept(db, nil)
}

// ensureUsageRelationalSchemaExcept verifies a versioned subset of the usage
// contract. A later migration may add columns to a table owned by the legacy
// baseline; the baseline verifier must still be able to validate the exact
// historical contract while the later migration verifies the complete current
// contract. Exclusions are deliberately limited to named scalar columns: all
// remaining columns, types, defaults, indexes, foreign keys, and checks stay
// subject to the same strict physical-schema verification.
func ensureUsageRelationalSchemaExcept(db *gorm.DB, excludedColumns map[string]map[string]struct{}) error {
	if db == nil {
		return errors.New("usage schema verification requires database")
	}
	models := usageRelationalModels()
	for _, model := range models {
		if err := verifyUsageRelationalModelExcept(db, model, excludedColumns); err != nil {
			return err
		}
	}
	return nil
}

// usageRelationalModels is the canonical relational baseline contract. Keep it
// aligned with migrate: adoption validates this exact model-level schema rather
// than accepting a database merely because its table names happen to exist.
func usageRelationalModels() []any {
	return []any{
		&usageRecord{}, &requestAttemptRecord{}, &requestTraceEventRecord{}, &requestTrafficShapeEventRecord{}, &requestUpstreamShapeEventRecord{},
		&requestShapeRecord{}, &requestTranslationShapeRecord{}, &requestTokenEstimateRecord{}, &requestTranslationFieldEventRecord{},
		&decisionShapeFeatureRecord{}, &decisionTargetCandidateRecord{}, &decisionTargetFilterReasonRecord{}, &routingDecisionRecord{},
		&routingSignalRecord{}, &dynamicScoreTermRecord{}, &policyExecutionRecord{}, &fallbackTransitionRecord{}, &decisionCacheReasonRecord{},
		&requestErrorRecord{}, &requestUpstreamErrorDetailRecord{}, &contentCaptureRecord{}, &contentCaptureHeaderRecord{}, &contentCaptureAuditRecord{},
		&authzPolicySetRecord{}, &authzPolicyRuleRecord{}, &authzRoleLinkRecord{}, &authzPolicyAuditEventRecord{}, &securityAccessEventRecord{},
		&usageRollupRunRecord{}, &usageRollupDailyRecord{}, &usageRollupHourlyRecord{}, &usageRollupMonthlyBillingRecord{}, &usageRollupAuditEventRecord{}, &usageRollupDecisionBucketRecord{},
		&retentionPolicyVersionRecord{}, &retentionPolicyRuleRecord{}, &retentionJobRecord{}, &retentionJobTableResultRecord{}, &legalHoldRecord{}, &legalHoldAuditEventRecord{},
	}
}

// applyUsageExplicitBaseline is the reviewed, manifest-owned fresh-install
// step. CreateTable is deliberately invoked once per checked-in model rather
// than using AutoMigrate: it cannot inspect and mutate an existing table, and
// a later schema change must be represented by a new immutable definition.
// Existing installations are adopted only after the strict baseline verifier
// proves their pre-ledger contract.
func applyUsageExplicitBaseline(db *gorm.DB) error {
	if db == nil {
		return errors.New("usage baseline requires database")
	}
	if db.Dialector.Name() == "sqlite" {
		if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
			return fmt.Errorf("usage baseline sqlite journal mode: %w", err)
		}
	}
	created := make(map[string]bool, len(usageRelationalModels()))
	for _, model := range usageRelationalModels() {
		if db.Migrator().HasTable(model) {
			continue
		}
		if err := db.Migrator().CreateTable(model); err != nil {
			return fmt.Errorf("create explicit usage table: %w", err)
		}
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return fmt.Errorf("parse explicit usage table: %w", err)
		}
		created[stmt.Schema.Table] = true
	}
	// The model structs intentionally describe the latest contract, while this
	// immutable baseline is schema version 1. Remove fields owned by later
	// migrations from tables created by this invocation so each expansion
	// remains owned by its immutable migration definition. Existing
	// installations are adopted unchanged and must already satisfy their
	// recorded ledger state.
	for _, laterColumns := range []map[string]map[string]struct{}{
		usageReasoningTelemetryColumns,
		usageTargetRegionDiagnosticsColumns,
		usageCachedInputPricingColumns,
	} {
		for table, columns := range laterColumns {
			if !created[table] {
				continue
			}
			for column := range columns {
				if err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column).Error; err != nil {
					return fmt.Errorf("freeze usage baseline column %s.%s: %w", table, column, err)
				}
			}
		}
	}
	return verifyUsageLegacyBaseline(db)
}

func verifyUsageRelationalModel(db *gorm.DB, model any) error {
	return verifyUsageRelationalModelExcept(db, model, nil)
}

func verifyUsageRelationalModelExcept(db *gorm.DB, model any, excludedColumns map[string]map[string]struct{}) error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return err
	}
	table := stmt.Schema.Table
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("required usage table %q is missing", table)
	}
	actualColumns, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		return fmt.Errorf("inspect usage table %q columns: %w", table, err)
	}
	byName := make(map[string]gorm.ColumnType, len(actualColumns))
	for _, actual := range actualColumns {
		name := actual.Name()
		byName[name] = actual
		t := strings.ToLower(actual.DatabaseTypeName())
		if strings.Contains(t, "json") || strings.Contains(t, "array") || strings.HasSuffix(t, "[]") {
			return fmt.Errorf("%s.%s uses forbidden non-relational type %q", table, name, actual.DatabaseTypeName())
		}
	}
	for _, field := range stmt.Schema.Fields {
		if field.DBName == "" { // associations have no scalar database column.
			continue
		}
		if _, excluded := excludedColumns[table][field.DBName]; excluded {
			continue
		}
		actual, ok := byName[field.DBName]
		if !ok {
			return fmt.Errorf("required usage column %s.%s is missing", table, field.DBName)
		}
		if !usageColumnTypeCompatible(field, actual.DatabaseTypeName()) {
			return fmt.Errorf("usage column %s.%s has type %q incompatible with %q", table, field.DBName, actual.DatabaseTypeName(), field.DataType)
		}
		// SQLite reports composite primary-key members as nullable unless the
		// CREATE TABLE spelling also contains NOT NULL. Primary-key membership is
		// verified independently below; explicit not-null contract fields must
		// still be physically non-null on both SQLite and PostgreSQL.
		if nullable, known := actual.Nullable(); known && field.NotNull && nullable {
			return fmt.Errorf("usage column %s.%s nullability does not match contract", table, field.DBName)
		}
		if primary, known := actual.PrimaryKey(); known && primary != field.PrimaryKey {
			return fmt.Errorf("usage column %s.%s primary-key membership does not match contract", table, field.DBName)
		}
		// Identity columns obtain their value from the engine, not a SQL DEFAULT
		// expression (notably SQLite AUTOINCREMENT), so verify their PK contract
		// above rather than demanding a default literal.
		if field.HasDefaultValue && !field.AutoIncrement {
			actualDefault, known := actual.DefaultValue()
			if !known || !usageDefaultCompatible(db.Dialector.Name(), field.DefaultValue, actualDefault) {
				return fmt.Errorf("usage column %s.%s default does not match contract", table, field.DBName)
			}
		}
	}
	if err := verifyUsageIndexes(db, model, stmt.Schema); err != nil {
		return err
	}
	if err := verifyUsageForeignKeys(db, stmt.Schema); err != nil {
		return err
	}
	if err := verifyUsageCheckConstraints(db, model, stmt.Schema); err != nil {
		return err
	}
	return nil
}

type usageCheckConstraintRow struct {
	Name       string `gorm:"column:name"`
	Definition string `gorm:"column:definition"`
}

// verifyUsageCheckConstraints checks the expression as well as the name. A
// named constraint can otherwise be rebuilt with weaker semantics and still
// satisfy HasConstraint. Both SQLite's stored DDL and PostgreSQL's catalog
// expose the checked-in, non-caller-controlled table definition safely.
func verifyUsageCheckConstraints(db *gorm.DB, model any, parsed *schema.Schema) error {
	expected := parsed.ParseCheckConstraints()
	if len(expected) == 0 {
		return nil
	}
	actual, err := inspectUsageCheckConstraints(db, parsed.Table)
	if err != nil {
		return err
	}
	byName := make(map[string]string, len(actual))
	for _, constraint := range actual {
		byName[constraint.Name] = constraint.Definition
	}
	for name, contract := range expected {
		if !db.Migrator().HasConstraint(model, name) {
			return fmt.Errorf("required usage check constraint %s on %s is missing", name, parsed.Table)
		}
		definition, ok := byName[name]
		if !ok || normalizeUsageCheckExpression(definition) != normalizeUsageCheckExpression(contract.Constraint) {
			return fmt.Errorf("usage check constraint %s on %s does not match contract", name, parsed.Table)
		}
	}
	return nil
}

func inspectUsageCheckConstraints(db *gorm.DB, table string) ([]usageCheckConstraintRow, error) {
	switch db.Dialector.Name() {
	case "sqlite":
		var ddl string
		if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&ddl).Error; err != nil {
			return nil, fmt.Errorf("inspect usage check constraints for %s: %w", table, err)
		}
		return parseSQLiteUsageCheckConstraints(ddl), nil
	case "postgres", "postgresql":
		const query = `SELECT con.conname AS name, pg_get_constraintdef(con.oid, true) AS definition
			FROM pg_constraint con
			JOIN pg_class table_class ON table_class.oid = con.conrelid
			JOIN pg_namespace namespace ON namespace.oid = table_class.relnamespace
			WHERE con.contype = 'c' AND namespace.nspname = current_schema() AND table_class.relname = ?`
		var rows []usageCheckConstraintRow
		if err := db.Raw(query, table).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("inspect usage check constraints for %s: %w", table, err)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("unsupported usage database driver %q", db.Dialector.Name())
	}
}

func parseSQLiteUsageCheckConstraints(ddl string) []usageCheckConstraintRow {
	const marker = "constraint"
	lower := strings.ToLower(ddl)
	var rows []usageCheckConstraintRow
	for offset := 0; ; {
		at := strings.Index(lower[offset:], marker)
		if at < 0 {
			return rows
		}
		at += offset
		cursor := at + len(marker)
		for cursor < len(ddl) && (ddl[cursor] == ' ' || ddl[cursor] == '\n' || ddl[cursor] == '\t') {
			cursor++
		}
		nameStart := cursor
		if cursor < len(ddl) && (ddl[cursor] == '"' || ddl[cursor] == '`' || ddl[cursor] == '[') {
			quote := ddl[cursor]
			endQuote := quote
			if quote == '[' {
				endQuote = ']'
			}
			cursor++
			nameStart = cursor
			for cursor < len(ddl) && ddl[cursor] != endQuote {
				cursor++
			}
			if cursor >= len(ddl) {
				return rows
			}
			name := ddl[nameStart:cursor]
			cursor++
			for cursor < len(ddl) && (ddl[cursor] == ' ' || ddl[cursor] == '\n' || ddl[cursor] == '\t') {
				cursor++
			}
			if !strings.HasPrefix(strings.ToLower(ddl[cursor:]), "check") {
				offset = cursor
				continue
			}
			cursor += len("check")
			for cursor < len(ddl) && (ddl[cursor] == ' ' || ddl[cursor] == '\n' || ddl[cursor] == '\t') {
				cursor++
			}
			if cursor >= len(ddl) || ddl[cursor] != '(' {
				offset = cursor
				continue
			}
			end := sqliteBalancedExpressionEnd(ddl, cursor)
			if end < 0 {
				return rows
			}
			rows = append(rows, usageCheckConstraintRow{Name: name, Definition: ddl[cursor : end+1]})
			offset = end + 1
			continue
		}
		for cursor < len(ddl) && ddl[cursor] != ' ' && ddl[cursor] != '\n' && ddl[cursor] != '\t' {
			cursor++
		}
		name := ddl[nameStart:cursor]
		for cursor < len(ddl) && (ddl[cursor] == ' ' || ddl[cursor] == '\n' || ddl[cursor] == '\t') {
			cursor++
		}
		if !strings.HasPrefix(strings.ToLower(ddl[cursor:]), "check") {
			offset = cursor
			continue
		}
		cursor += len("check")
		for cursor < len(ddl) && (ddl[cursor] == ' ' || ddl[cursor] == '\n' || ddl[cursor] == '\t') {
			cursor++
		}
		if cursor >= len(ddl) || ddl[cursor] != '(' {
			offset = cursor
			continue
		}
		end := sqliteBalancedExpressionEnd(ddl, cursor)
		if end < 0 {
			return rows
		}
		rows = append(rows, usageCheckConstraintRow{Name: name, Definition: ddl[cursor : end+1]})
		offset = end + 1
	}
}

func sqliteBalancedExpressionEnd(value string, start int) int {
	depth := 0
	for i := start; i < len(value); i++ {
		switch value[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func normalizeUsageCheckExpression(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if strings.HasPrefix(value, "check") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "check"))
	}
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	value = strings.NewReplacer(" ", "", "\n", "", "\t", "", "\r", "", "\"", "", "`", "").Replace(value)
	return value
}

func usageColumnTypeCompatible(field *schema.Field, actual string) bool {
	t := strings.ToLower(actual)
	switch field.DataType {
	case schema.String:
		return strings.Contains(t, "char") || strings.Contains(t, "text") || strings.Contains(t, "clob")
	case schema.Bool:
		return strings.Contains(t, "bool") || strings.Contains(t, "int") || strings.Contains(t, "numeric")
	case schema.Int, schema.Uint:
		return strings.Contains(t, "int") || strings.Contains(t, "serial")
	case schema.Float:
		return strings.Contains(t, "real") || strings.Contains(t, "double") || strings.Contains(t, "float") || strings.Contains(t, "numeric") || strings.Contains(t, "decimal")
	default:
		return true
	}
}

// usageDefaultCompatible accepts only the small set of driver spelling
// differences that preserve a scalar default's value. In particular,
// PostgreSQL's catalog commonly renders a text literal as ”::text (or with
// redundant parentheses), while GORM's contract records default:”.
//
// This is intentionally not a general SQL expression evaluator: functions,
// operators, non-text casts, and malformed expressions remain different so a
// changed default continues to fail the schema contract closed.
func usageDefaultCompatible(driver, expected, actual string) bool {
	expectedValue, expectedOK := normalizeUsageDefault(expected)
	if !expectedOK {
		return false
	}
	if driver == "postgres" || driver == "postgresql" {
		if value, ok := normalizePostgresTextCastDefault(actual); ok {
			return expectedValue == value
		}
	}
	actualValue, actualOK := normalizeUsageDefault(actual)
	if !actualOK {
		return false
	}
	return expectedValue == actualValue
}

func normalizeUsageDefault(value string) (string, bool) {
	value = trimUsageDefaultParens(strings.TrimSpace(value))
	if literal, rest, ok := parseUsageSQLStringLiteral(value); ok && strings.TrimSpace(rest) == "" {
		return "string:" + literal, true
	}
	switch strings.ToLower(value) {
	case "":
		// GORM parses default:'' tags as an empty expected string while SQLite
		// and PostgreSQL metadata retain an empty SQL literal.
		return "string:", true
	case "false":
		return "scalar:0", true
	case "true":
		return "scalar:1", true
	case "0":
		return "scalar:0", true
	default:
		if usageDefaultIntegerLiteral(value) {
			return "number:" + value, true
		}
		return "", false
	}
}

func usageDefaultIntegerLiteral(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func normalizePostgresTextCastDefault(value string) (string, bool) {
	value = trimUsageDefaultParens(strings.TrimSpace(value))
	literal, rest, ok := parseUsageSQLStringLiteral(value)
	if !ok {
		return "", false
	}
	castCount := 0
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, "::") {
			return "", false
		}
		castCount++
		rest = strings.TrimSpace(rest[2:])
		next := strings.Index(rest, "::")
		typeName := rest
		if next >= 0 {
			typeName, rest = rest[:next], rest[next:]
		} else {
			rest = ""
		}
		if !postgresTextDefaultCast(typeName) {
			return "", false
		}
	}
	if castCount == 0 {
		return "", false
	}
	return "string:" + literal, true
}

func postgresTextDefaultCast(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "\"", "").Replace(value)
	switch value {
	case "text", "pg_catalog.text", "varchar", "charactervarying", "character", "char", "bpchar", "pg_catalog.varchar", "pg_catalog.bpchar":
		return true
	default:
		return false
	}
}

func parseUsageSQLStringLiteral(value string) (literal, rest string, ok bool) {
	if len(value) > 1 && (value[0] == 'e' || value[0] == 'E') && value[1] == '\'' {
		value = value[1:]
	}
	if len(value) == 0 || value[0] != '\'' {
		return "", value, false
	}
	var decoded strings.Builder
	for i := 1; i < len(value); i++ {
		if value[i] != '\'' {
			decoded.WriteByte(value[i])
			continue
		}
		if i+1 < len(value) && value[i+1] == '\'' {
			decoded.WriteByte('\'')
			i++
			continue
		}
		return decoded.String(), value[i+1:], true
	}
	return "", value, false
}

func trimUsageDefaultParens(value string) string {
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' && usageDefaultOuterParens(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func usageDefaultOuterParens(value string) bool {
	depth := 0
	inString := false
	for i := 0; i < len(value); i++ {
		if value[i] == '\'' {
			if inString && i+1 < len(value) && value[i+1] == '\'' {
				i++
				continue
			}
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch value[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(value)-1 {
				return false
			}
		}
	}
	return depth == 0 && !inString
}

func verifyUsageIndexes(db *gorm.DB, model any, expected *schema.Schema) error {
	// The PostgreSQL GORM migrator's index query joins index keys through
	// a.attnum = ANY(i.indkey) without an ORDER BY. It therefore cannot safely
	// represent composite-key order. It also aliases indisunique as non_unique,
	// which makes its Unique result unsuitable for this strict contract. Read the
	// catalog directly on PostgreSQL instead of weakening the comparison.
	if db.Dialector.Name() == "postgres" {
		return verifyUsageIndexesPostgres(db, expected)
	}
	indexes, err := db.Migrator().GetIndexes(model)
	if err != nil {
		return err
	}
	actual := make(map[string]gorm.Index, len(indexes))
	for _, index := range indexes {
		actual[index.Name()] = index
	}
	for _, index := range expected.ParseIndexes() {
		got, ok := actual[index.Name]
		if !ok {
			return fmt.Errorf("required usage index %s.%s is missing", expected.Table, index.Name)
		}
		if len(got.Columns()) != len(index.Fields) {
			return fmt.Errorf("usage index %s.%s columns do not match contract", expected.Table, index.Name)
		}
		for i, field := range index.Fields {
			if got.Columns()[i] != field.DBName {
				return fmt.Errorf("usage index %s.%s columns do not match contract", expected.Table, index.Name)
			}
		}
		if unique, known := got.Unique(); known && unique != (index.Class == "UNIQUE") {
			return fmt.Errorf("usage index %s.%s uniqueness does not match contract", expected.Table, index.Name)
		}
	}
	return nil
}

type usagePostgresIndexMetadata struct {
	Unique         bool
	Valid          bool
	Ready          bool
	Live           bool
	KeyCount       int
	AttributeCount int
	NoPredicate    bool
	Keys           []string
}

func verifyUsageIndexesPostgres(db *gorm.DB, expected *schema.Schema) error {
	for _, index := range expected.ParseIndexes() {
		// There are no partial usage indexes today. Do not silently accept one if
		// a future tag adds it without an exact predicate verifier.
		if strings.TrimSpace(index.Where) != "" {
			return fmt.Errorf("usage index %s.%s has unsupported predicate contract", expected.Table, index.Name)
		}
		metadata, err := inspectUsagePostgresIndex(db, expected.Table, index.Name)
		if err != nil {
			return err
		}
		if err := verifyUsagePostgresIndexMetadata(expected.Table, index, metadata); err != nil {
			return err
		}
	}
	return nil
}

func inspectUsagePostgresIndex(db *gorm.DB, table, name string) (usagePostgresIndexMetadata, error) {
	const query = `SELECT index_info.indisunique,
		index_info.indisvalid,
		index_info.indisready,
		index_info.indislive,
		index_info.indnkeyatts,
		index_info.indnatts,
		index_info.indpred IS NULL,
		key_position.ordinality,
		pg_get_indexdef(index_info.indexrelid, key_position.ordinality, false)
	FROM pg_index AS index_info
	JOIN pg_class AS index_class ON index_class.oid = index_info.indexrelid
	JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_class.relnamespace
	JOIN pg_class AS table_class ON table_class.oid = index_info.indrelid
	JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_class.relnamespace
	CROSS JOIN LATERAL generate_series(1, index_info.indnkeyatts) AS key_position(ordinality)
	WHERE table_namespace.nspname = current_schema()
		AND index_namespace.nspname = current_schema()
		AND table_class.relname = ?
		AND index_class.relname = ?
	ORDER BY key_position.ordinality`
	rows, err := db.Raw(query, table, name).Rows()
	if err != nil {
		return usagePostgresIndexMetadata{}, fmt.Errorf("inspect usage index %s.%s: %w", table, name, err)
	}
	defer rows.Close()
	metadata := usagePostgresIndexMetadata{}
	for rows.Next() {
		var position int
		var key string
		if err := rows.Scan(&metadata.Unique, &metadata.Valid, &metadata.Ready, &metadata.Live, &metadata.KeyCount, &metadata.AttributeCount, &metadata.NoPredicate, &position, &key); err != nil {
			return usagePostgresIndexMetadata{}, fmt.Errorf("inspect usage index %s.%s: %w", table, name, err)
		}
		if position != len(metadata.Keys)+1 {
			return usagePostgresIndexMetadata{}, fmt.Errorf("inspect usage index %s.%s: non-contiguous key metadata", table, name)
		}
		metadata.Keys = append(metadata.Keys, key)
	}
	if err := rows.Err(); err != nil {
		return usagePostgresIndexMetadata{}, fmt.Errorf("inspect usage index %s.%s: %w", table, name, err)
	}
	if len(metadata.Keys) == 0 {
		return usagePostgresIndexMetadata{}, fmt.Errorf("required usage index %s.%s is missing", table, name)
	}
	return metadata, nil
}

func verifyUsagePostgresIndexMetadata(table string, index *schema.Index, metadata usagePostgresIndexMetadata) error {
	expectedUnique := index.Class == "UNIQUE"
	if metadata.Unique != expectedUnique {
		return fmt.Errorf("usage index %s.%s uniqueness does not match contract", table, index.Name)
	}
	if !metadata.Valid || !metadata.Ready || !metadata.Live {
		return fmt.Errorf("usage index %s.%s must be valid, ready, and live", table, index.Name)
	}
	if !metadata.NoPredicate {
		return fmt.Errorf("usage index %s.%s must not be partial", table, index.Name)
	}
	if metadata.KeyCount != len(index.Fields) || metadata.AttributeCount != len(index.Fields) || len(metadata.Keys) != len(index.Fields) {
		return fmt.Errorf("usage index %s.%s columns do not match contract", table, index.Name)
	}
	for position, field := range index.Fields {
		if normalizeUsagePostgresIndexKey(metadata.Keys[position]) != normalizeUsagePostgresIndexKey(field.DBName) {
			return fmt.Errorf("usage index %s.%s columns do not match contract", table, index.Name)
		}
	}
	return nil
}

// normalizeUsagePostgresIndexKey accepts only catalog rendering differences
// (identifier quotes, case, and whitespace). Key position, count, expression
// spelling, predicates, uniqueness, included columns, and validity are checked
// separately and remain exact.
func normalizeUsagePostgresIndexKey(value string) string {
	value = strings.ReplaceAll(strings.ToLower(value), `"`, "")
	return strings.Join(strings.Fields(value), "")
}

type usageForeignKeyRow struct {
	ConstraintID     string `gorm:"column:constraint_id"`
	Sequence         int    `gorm:"column:sequence"`
	ReferencedTable  string `gorm:"column:referenced_table"`
	LocalColumn      string `gorm:"column:local_column"`
	ReferencedColumn string `gorm:"column:referenced_column"`
	OnUpdate         string `gorm:"column:on_update"`
	OnDelete         string `gorm:"column:on_delete"`
}

// verifyUsageForeignKeys compares the checked-in GORM relationship contract
// with the physical schema. HasConstraint alone is insufficient: it cannot
// distinguish a constraint that has the wrong columns, target, or referential
// action. Both queries return one scalar row per FK column and work on the two
// supported production database families.
func verifyUsageForeignKeys(db *gorm.DB, parsed *schema.Schema) error {
	model := reflect.New(parsed.ModelType).Interface()
	for _, relationship := range parsed.Relationships.Relations {
		if relationship == nil || relationship.Field == nil {
			continue
		}
		contract := relationship.ParseConstraint()
		if contract == nil || len(contract.ForeignKeys) == 0 || contract.ReferenceSchema == nil {
			continue
		}
		if !db.Migrator().HasConstraint(model, contract.Name) {
			return fmt.Errorf("required usage foreign-key constraint %s on %s is missing", contract.Name, parsed.Table)
		}
		rows, err := inspectUsageForeignKeys(db, contract.Schema.Table)
		if err != nil {
			return err
		}
		byConstraint := make(map[string][]usageForeignKeyRow)
		for _, row := range rows {
			byConstraint[row.ConstraintID] = append(byConstraint[row.ConstraintID], row)
		}
		matched := false
		for constraintID, candidate := range byConstraint {
			// SQLite's pragma does not expose user-assigned constraint names, so
			// HasConstraint above proves its named GORM constraint and this loop
			// proves the physical column/reference/action contract. PostgreSQL
			// exposes names, which additionally binds the inspected rows to it.
			if db.Dialector.Name() != "sqlite" && constraintID != contract.Name {
				continue
			}
			sort.Slice(candidate, func(i, j int) bool { return candidate[i].Sequence < candidate[j].Sequence })
			if len(candidate) != len(contract.ForeignKeys) || candidate[0].ReferencedTable != contract.ReferenceSchema.Table {
				continue
			}
			matches := true
			for i, row := range candidate {
				if row.LocalColumn != contract.ForeignKeys[i].DBName || row.ReferencedColumn != contract.References[i].DBName ||
					!usageForeignKeyActionMatches(contract.OnUpdate, row.OnUpdate) || !usageForeignKeyActionMatches(contract.OnDelete, row.OnDelete) {
					matches = false
					break
				}
			}
			if matches {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("usage foreign-key constraint %s on %s does not match contract", contract.Name, contract.Schema.Table)
		}
	}
	return nil
}

func inspectUsageForeignKeys(db *gorm.DB, table string) ([]usageForeignKeyRow, error) {
	var rows []usageForeignKeyRow
	switch db.Dialector.Name() {
	case "sqlite":
		// table is a parsed checked-in model table name, never caller input.
		query := fmt.Sprintf(`SELECT id AS constraint_id, seq AS sequence, "table" AS referenced_table, "from" AS local_column, "to" AS referenced_column, on_update, on_delete FROM pragma_foreign_key_list('%s')`, table)
		if err := db.Raw(query).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("inspect usage foreign keys for %s: %w", table, err)
		}
	case "postgres", "postgresql":
		const query = `SELECT con.conname AS constraint_id,
			local_key.ordinality AS sequence,
			referenced_table.relname AS referenced_table,
			local_column.attname AS local_column,
			referenced_column.attname AS referenced_column,
			CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END AS on_update,
			CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END AS on_delete
		FROM pg_constraint con
		JOIN pg_class local_table ON local_table.oid = con.conrelid
		JOIN pg_namespace local_namespace ON local_namespace.oid = local_table.relnamespace
		JOIN pg_class referenced_table ON referenced_table.oid = con.confrelid
		JOIN unnest(con.conkey) WITH ORDINALITY AS local_key(attnum, ordinality) ON TRUE
		JOIN unnest(con.confkey) WITH ORDINALITY AS referenced_key(attnum, ordinality) ON referenced_key.ordinality = local_key.ordinality
		JOIN pg_attribute local_column ON local_column.attrelid = con.conrelid AND local_column.attnum = local_key.attnum
		JOIN pg_attribute referenced_column ON referenced_column.attrelid = con.confrelid AND referenced_column.attnum = referenced_key.attnum
		WHERE con.contype = 'f' AND local_namespace.nspname = current_schema() AND local_table.relname = ?
		ORDER BY con.conname, local_key.ordinality`
		if err := db.Raw(query, table).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("inspect usage foreign keys for %s: %w", table, err)
		}
	default:
		return nil, fmt.Errorf("unsupported usage database driver %q", db.Dialector.Name())
	}
	return rows, nil
}

func usageForeignKeyActionMatches(expected, actual string) bool {
	expected = strings.ToUpper(strings.TrimSpace(expected))
	actual = strings.ToUpper(strings.TrimSpace(actual))
	// SQL defaults to NO ACTION when GORM's constraint tag does not request an
	// explicit action. The comparison is deliberately exact for requested
	// actions such as CASCADE and RESTRICT.
	if expected == "" {
		expected = "NO ACTION"
	}
	return expected == actual
}

var usageRelationalTables = []string{
	"request_usage",
	"request_attempts",
	"request_trace_events",
	"request_traffic_shape_events",
	"request_upstream_shape_events",
	"request_shapes",
	"request_translation_shapes",
	"request_token_estimates",
	"request_translation_field_events",
	"request_decision_shape_features",
	"request_target_candidates",
	"request_target_filter_reasons",
	"request_routing_decisions",
	"request_routing_signals",
	"request_dynamic_score_terms",
	"request_policy_executions",
	"request_fallback_transitions",
	"request_cache_reasons",
	"request_errors",
	"request_upstream_error_details",
	"request_content_captures",
	"request_content_headers",
	"request_content_audit_events",
	"authz_policy_sets",
	"authz_policy_rules",
	"authz_role_links",
	"authz_policy_audit_events",
	"security_access_events",
	"usage_rollup_runs",
	"usage_rollup_hourly",
	"usage_rollup_daily",
	"usage_rollup_monthly_billing",
	"usage_rollup_audit_events",
	"usage_rollup_decision_buckets",
	"retention_policy_versions",
	"retention_policy_rules",
	"retention_jobs",
	"retention_job_table_results",
	"legal_holds",
	"legal_hold_audit_events",
}

// verifyUsageLegacyBaseline is the postcondition for the initial ledger
// adoption. It is deliberately stricter than a table-exists probe, while the
// complete explicit DDL manifest is prepared as a later migration slice.
var usageReasoningTelemetryColumns = map[string]map[string]struct{}{
	"request_usage": {
		"reasoning_tokens":                   {},
		"reasoning_attempt_count":            {},
		"reasoning_successful_attempt_count": {},
		"reasoning_reported_attempt_count":   {},
	},
	"request_attempts": {"reasoning_tokens": {}},
}

var usageContentCaptureEncryptionColumns = map[string]map[string]struct{}{
	"request_content_captures": {
		"encryption_nonce":      {},
		"encryption_kms_key_id": {},
		"encrypted":             {},
	},
	"request_content_headers": {
		"encryption_nonce":      {},
		"encryption_kms_key_id": {},
		"encrypted":             {},
	},
}

var usageTargetRegionDiagnosticsColumns = map[string]map[string]struct{}{
	"request_usage": {"target_region": {}},
}

var usageCachedInputPricingColumns = map[string]map[string]struct{}{
	"request_usage": {
		"cached_input_tokens":                {},
		"cached_input_price_per_million_usd": {},
	},
}

func mergeUsageExcludedColumns(groups ...map[string]map[string]struct{}) map[string]map[string]struct{} {
	merged := map[string]map[string]struct{}{}
	for _, group := range groups {
		for table, columns := range group {
			if merged[table] == nil {
				merged[table] = map[string]struct{}{}
			}
			for column := range columns {
				merged[table][column] = struct{}{}
			}
		}
	}
	return merged
}

func verifyUsageLegacyBaseline(db *gorm.DB) error {
	return ensureUsageRelationalSchemaExcept(db, mergeUsageExcludedColumns(
		usageReasoningTelemetryColumns,
		usageContentCaptureEncryptionColumns,
		usageTargetRegionDiagnosticsColumns,
		usageCachedInputPricingColumns,
	))
}

func applyUsageReasoningTelemetryMigration(db *gorm.DB) error {
	for _, column := range []struct {
		model any
		field string
	}{
		{&usageRecord{}, "ReasoningTokens"},
		{&usageRecord{}, "ReasoningAttemptCount"},
		{&usageRecord{}, "ReasoningSuccessfulAttemptCount"},
		{&usageRecord{}, "ReasoningReportedAttemptCount"},
		{&requestAttemptRecord{}, "ReasoningTokens"},
	} {
		if !db.Migrator().HasColumn(column.model, column.field) {
			if err := db.Migrator().AddColumn(column.model, column.field); err != nil {
				return fmt.Errorf("add reasoning telemetry column %s: %w", column.field, err)
			}
		}
	}
	return verifyUsageReasoningTelemetryMigration(db)
}

func verifyUsageReasoningTelemetryMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchemaExcept(db, mergeUsageExcludedColumns(
		usageContentCaptureEncryptionColumns,
		usageTargetRegionDiagnosticsColumns,
		usageCachedInputPricingColumns,
	))
}

func applyUsageContentCaptureEncryptionMigration(db *gorm.DB) error {
	for _, column := range []struct {
		model any
		field string
	}{
		{&contentCaptureRecord{}, "EncryptionNonce"},
		{&contentCaptureRecord{}, "EncryptionLocalKeyID"},
		{&contentCaptureRecord{}, "Encrypted"},
		{&contentCaptureHeaderRecord{}, "EncryptionNonce"},
		{&contentCaptureHeaderRecord{}, "EncryptionLocalKeyID"},
		{&contentCaptureHeaderRecord{}, "Encrypted"},
	} {
		if !db.Migrator().HasColumn(column.model, column.field) {
			if err := db.Migrator().AddColumn(column.model, column.field); err != nil {
				return fmt.Errorf("add content capture encryption column %s: %w", column.field, err)
			}
		}
	}
	return verifyUsageContentCaptureEncryptionMigration(db)
}

func verifyUsageContentCaptureEncryptionMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchemaExcept(db, mergeUsageExcludedColumns(
		usageTargetRegionDiagnosticsColumns,
		usageCachedInputPricingColumns,
	))
}

func applyUsageTargetRegionDiagnosticsMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&usageRecord{}, "TargetRegion") {
		if err := db.Migrator().AddColumn(&usageRecord{}, "TargetRegion"); err != nil {
			return fmt.Errorf("add selected target region diagnostics column: %w", err)
		}
	}
	return verifyUsageTargetRegionDiagnosticsMigration(db)
}

func verifyUsageTargetRegionDiagnosticsMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchemaExcept(db, usageCachedInputPricingColumns)
}

func applyUsageCachedInputPricingMigration(db *gorm.DB) error {
	for _, column := range []struct {
		model any
		field string
	}{
		{&usageRecord{}, "CachedInputTokens"},
		{&usageRecord{}, "CachedInputPricePerMillionUSD"},
	} {
		if !db.Migrator().HasColumn(column.model, column.field) {
			if err := db.Migrator().AddColumn(column.model, column.field); err != nil {
				return fmt.Errorf("add cached-input pricing column %s: %w", column.field, err)
			}
		}
	}
	return verifyUsageCachedInputPricingMigration(db)
}

func verifyUsageCachedInputPricingMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchema(db)
}

func (s *usageStore) Emit(rec logRecord) {
	if s == nil || s.db == nil || rec.RequestID == "" {
		return
	}
	row := rowFromRecord(rec)
	_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(recordFromRow(row)).Error
	for _, attempt := range rec.AttemptsDetail {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(attemptRecordFromLog(rec.RequestID, attempt)).Error
		for _, detail := range attempt.ErrorDetails {
			_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(upstreamErrorDetailRecordFromLog(rec.RequestID, detail)).Error
		}
	}
	for _, event := range rec.TraceEvents {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(traceRecordFromLog(rec.RequestID, event)).Error
	}
	for _, event := range rec.TrafficShapeEvents {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(trafficShapeEventRecordFromLog(rec.RequestID, event)).Error
	}
	for _, event := range rec.UpstreamShapeEvents {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(upstreamShapeEventRecordFromLog(rec.RequestID, event)).Error
	}
	if rec.RequestShape != nil {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(requestShapeRecordFromLog(rec.RequestID, *rec.RequestShape)).Error
	}
	if rec.TokenEstimate != nil {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(requestTokenEstimateRecordFromLog(rec.RequestID, *rec.TokenEstimate)).Error
	}
	for _, shape := range rec.TranslationShapes {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(requestTranslationShapeRecordFromLog(rec.RequestID, shape)).Error
	}
	for _, event := range rec.TranslationFieldEvents {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(requestTranslationFieldEventRecordFromLog(rec.RequestID, event)).Error
	}
	for _, feature := range rec.DecisionShapeFeatures {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(decisionShapeFeatureRecordFromLog(rec.RequestID, feature)).Error
	}
	for _, candidate := range rec.DecisionCandidates {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(decisionCandidateRecordFromLog(rec.RequestID, candidate)).Error
	}
	for _, reason := range rec.DecisionFilterReasons {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(decisionFilterReasonRecordFromLog(rec.RequestID, reason)).Error
	}
	for _, decision := range rec.RoutingDecisions {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(routingDecisionRecordFromLog(rec.RequestID, decision)).Error
	}
	for _, signal := range rec.RoutingSignals {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(routingSignalRecordFromLog(rec.RequestID, signal)).Error
	}
	for _, term := range rec.DynamicScoreTerms {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(dynamicScoreTermRecordFromLog(rec.RequestID, term)).Error
	}
	for _, execution := range rec.PolicyExecutions {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(policyExecutionRecordFromLog(rec.RequestID, execution)).Error
	}
	for _, transition := range rec.FallbackTransitions {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(fallbackTransitionRecordFromLog(rec.RequestID, transition)).Error
	}
	for _, reason := range rec.CacheReasons {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(decisionCacheReasonRecordFromLog(rec.RequestID, reason)).Error
	}
	if rec.Error != nil {
		_ = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(errorRecordFromLog(rec)).Error
	}
}

func attemptRecordFromLog(requestID string, rec attemptLogRecord) *requestAttemptRecord {
	return &requestAttemptRecord{
		RequestID:        requestID,
		AttemptIndex:     rec.Index,
		TS:               rec.TS,
		Provider:         rec.Provider,
		Model:            rec.Model,
		Dialect:          rec.Dialect,
		EndpointHost:     rec.EndpointHost,
		DurationMS:       rec.DurationMS,
		StatusCode:       rec.StatusCode,
		ErrorClass:       rec.ErrorClass,
		ErrorMessage:     sanitizePersistedDiagnosticText(rec.ErrorMessage),
		Retryable:        rec.Retryable,
		TimedOut:         rec.TimedOut,
		ClientCanceled:   rec.ClientCanceled,
		Selected:         rec.Selected,
		FallbackReason:   rec.FallbackReason,
		RequestBytes:     rec.RequestBytes,
		ResponseBytes:    rec.ResponseBytes,
		AttemptTimeoutMS: rec.AttemptTimeoutMS,
		RetryAfterMS:     rec.RetryAfterMS,
		ReasoningTokens:  rec.ReasoningTokens,
	}
}

func upstreamErrorDetailRecordFromLog(requestID string, rec upstreamErrorDetailLogRecord) *requestUpstreamErrorDetailRecord {
	return &requestUpstreamErrorDetailRecord{
		RequestID:    requestID,
		AttemptIndex: rec.AttemptIndex,
		Seq:          rec.Seq,
		TS:           rec.TS,
		StatusCode:   rec.StatusCode,
		ErrorClass:   safeOptionalReasonToken(rec.ErrorClass),
		FieldName:    safeOptionalReasonToken(rec.FieldName),
		FieldValue:   sanitizePersistedUpstreamErrorDetailScalar(rec.FieldValue),
		Source:       sanitizePersistedUpstreamErrorDetailScalar(rec.Source),
		Truncated:    rec.Truncated,
	}
}

func sanitizePersistedUpstreamErrorDetailScalar(value string) string {
	value = normalizeDiagnosticText(value)
	value = redactDiagnosticSecrets(value)
	return truncateDiagnosticText(value, upstreamErrorDetailMaxValueBytes)
}

func traceRecordFromLog(requestID string, rec traceLogRecord) *requestTraceEventRecord {
	return &requestTraceEventRecord{
		RequestID:  requestID,
		Seq:        rec.Seq,
		TS:         rec.TS,
		Event:      rec.Event,
		Message:    sanitizePersistedTraceMessage(rec.Event, rec.Message),
		Provider:   rec.Provider,
		Model:      rec.Model,
		Dialect:    rec.Dialect,
		DurationMS: rec.DurationMS,
		StatusCode: rec.StatusCode,
		ErrorClass: rec.ErrorClass,
		Retryable:  rec.Retryable,
		Attempt:    rec.Attempt,
	}
}

func trafficShapeEventRecordFromLog(requestID string, rec trafficShapeEventLogRecord) *requestTrafficShapeEventRecord {
	return &requestTrafficShapeEventRecord{
		RequestID:            requestID,
		Seq:                  rec.Seq,
		Scope:                safeOptionalReasonToken(rec.Scope),
		Bucket:               safeOptionalReasonToken(rec.Bucket),
		Decision:             safeOptionalReasonToken(rec.Decision),
		Cost:                 rec.Cost,
		RetryAfterMS:         rec.RetryAfterMS,
		QueueWaitMS:          rec.QueueWaitMS,
		EstimatedInputTokens: rec.EstimatedInputTokens,
		ReservedOutputTokens: rec.ReservedOutputTokens,
		TotalReservedTokens:  rec.TotalReservedTokens,
	}
}

func upstreamShapeEventRecordFromLog(requestID string, rec upstreamShapeEventLogRecord) *requestUpstreamShapeEventRecord {
	return &requestUpstreamShapeEventRecord{
		RequestID:            requestID,
		Seq:                  rec.Seq,
		TS:                   rec.TS,
		Scope:                rec.Scope,
		Provider:             rec.Provider,
		ModelRef:             rec.ModelRef,
		Model:                rec.Model,
		Dialect:              rec.Dialect,
		Bucket:               rec.Bucket,
		Decision:             rec.Decision,
		RetryAfterMS:         rec.RetryAfterMS,
		EstimatedInputTokens: rec.EstimatedInputTokens,
		ReservedOutputTokens: rec.ReservedOutputTokens,
		TotalReservedTokens:  rec.TotalReservedTokens,
		BackoffReason:        rec.BackoffReason,
		QueueWaitMS:          rec.QueueWaitMS,
	}
}

func requestShapeRecordFromLog(requestID string, rec requestShapeLogRecord) *requestShapeRecord {
	return &requestShapeRecord{
		RequestID:                  requestID,
		TS:                         rec.TS,
		InboundDialect:             rec.InboundDialect,
		RequestedModel:             rec.RequestedModel,
		ResolvedGroup:              rec.ResolvedGroup,
		Client:                     rec.Client,
		Stream:                     rec.Stream,
		InputItemCount:             rec.InputItemCount,
		MessageCount:               rec.MessageCount,
		SystemMessageCount:         rec.SystemMessageCount,
		DeveloperMessageCount:      rec.DeveloperMessageCount,
		UserMessageCount:           rec.UserMessageCount,
		AssistantMessageCount:      rec.AssistantMessageCount,
		ToolResultCount:            rec.ToolResultCount,
		FunctionCallOutputCount:    rec.FunctionCallOutputCount,
		ToolCount:                  rec.ToolCount,
		ToolChoiceMode:             rec.ToolChoiceMode,
		ParallelToolCallsPresent:   rec.ParallelToolCallsPresent,
		ResponseFormatPresent:      rec.ResponseFormatPresent,
		StructuredOutputPresent:    rec.StructuredOutputPresent,
		ReasoningPresent:           rec.ReasoningPresent,
		ReasoningEffortBucket:      rec.ReasoningEffortBucket,
		ReasoningBudgetBucket:      rec.ReasoningBudgetBucket,
		IncludePresent:             rec.IncludePresent,
		TruncationPresent:          rec.TruncationPresent,
		MetadataPresent:            rec.MetadataPresent,
		StorePresent:               rec.StorePresent,
		PreviousResponseIDPresent:  rec.PreviousResponseIDPresent,
		ImageCount:                 rec.ImageCount,
		AudioPresent:               rec.AudioPresent,
		VideoPresent:               rec.VideoPresent,
		InputTextBytesBucket:       rec.InputTextBytesBucket,
		ToolSchemaBytesBucket:      rec.ToolSchemaBytesBucket,
		TotalRequestBytesBucket:    rec.TotalRequestBytesBucket,
		EstimatedInputTokensBucket: rec.EstimatedInputTokensBucket,
		RequestedOutputCapField:    rec.RequestedOutputCapField,
		RequestedOutputCapBucket:   rec.RequestedOutputCapBucket,
		ToolSchemaFingerprint:      rec.ToolSchemaFingerprint,
		RequestShapeFingerprint:    rec.RequestShapeFingerprint,
	}
}

func requestTranslationShapeRecordFromLog(requestID string, rec translationShapeLogRecord) *requestTranslationShapeRecord {
	return &requestTranslationShapeRecord{
		RequestID:                    requestID,
		AttemptIndex:                 rec.AttemptIndex,
		TS:                           rec.TS,
		Provider:                     rec.Provider,
		Model:                        rec.Model,
		Dialect:                      rec.Dialect,
		BridgeDirection:              safeOptionalReasonToken(rec.BridgeDirection),
		EndpointPath:                 rec.EndpointPath,
		TranslatedStream:             rec.TranslatedStream,
		TranslatedToolCount:          rec.TranslatedToolCount,
		TranslatedToolChoiceMode:     rec.TranslatedToolChoiceMode,
		TranslatedOutputCapField:     rec.TranslatedOutputCapField,
		TranslatedOutputCapBucket:    rec.TranslatedOutputCapBucket,
		TranslatedReasoningControl:   rec.TranslatedReasoningControl,
		TranslatedRequestBytesBucket: rec.TranslatedRequestBytesBucket,
		FieldsStrippedCount:          rec.FieldsStrippedCount,
		FieldsRewrittenCount:         rec.FieldsRewrittenCount,
		UnsupportedFieldsPresent:     rec.UnsupportedFieldsPresent,
		TranslationWarningCount:      rec.TranslationWarningCount,
		RequestShapeFingerprint:      rec.RequestShapeFingerprint,
		ToolSchemaFingerprint:        rec.ToolSchemaFingerprint,
	}
}

func requestTokenEstimateRecordFromLog(requestID string, rec requestTokenEstimateLogRecord) *requestTokenEstimateRecord {
	return &requestTokenEstimateRecord{
		RequestID:                     requestID,
		TS:                            rec.TS,
		InboundDialect:                rec.InboundDialect,
		RequestedModel:                rec.RequestedModel,
		ResolvedGroup:                 rec.ResolvedGroup,
		EstimateMethod:                rec.EstimateMethod,
		EstimateVersion:               rec.EstimateVersion,
		EstimatedInputTokens:          rec.EstimatedInputTokens,
		EstimatedToolSchemaTokens:     rec.EstimatedToolSchemaTokens,
		EstimatedImageTokens:          rec.EstimatedImageTokens,
		EstimatedAudioTokens:          rec.EstimatedAudioTokens,
		EstimatedTotalInputTokens:     rec.EstimatedTotalInputTokens,
		RequestedOutputCapTokens:      rec.RequestedOutputCapTokens,
		RequestedOutputCapField:       safeOptionalReasonToken(rec.RequestedOutputCapField),
		RouterDefaultOutputCapApplied: rec.RouterDefaultOutputCapApplied,
		TotalReservedTokens:           rec.TotalReservedTokens,
		RequestBytes:                  rec.RequestBytes,
		TranslatedRequestBytes:        rec.TranslatedRequestBytes,
		EstimateWarningCount:          rec.EstimateWarningCount,
		EstimateConfidenceBucket:      safeOptionalReasonToken(rec.EstimateConfidenceBucket),
	}
}

func requestTranslationFieldEventRecordFromLog(requestID string, rec translationFieldEventLogRecord) *requestTranslationFieldEventRecord {
	return &requestTranslationFieldEventRecord{
		RequestID:    requestID,
		AttemptIndex: rec.AttemptIndex,
		Seq:          rec.Seq,
		FieldName:    safeTranslationFieldName(rec.FieldName),
		Action:       safeOptionalReasonToken(rec.Action),
		Reason:       safeOptionalReasonToken(rec.Reason),
	}
}

func decisionShapeFeatureRecordFromLog(requestID string, rec decisionShapeFeatureLogRecord) *decisionShapeFeatureRecord {
	return &decisionShapeFeatureRecord{
		RequestID:   requestID,
		Seq:         rec.Seq,
		FeatureName: rec.Name,
		BoolValue:   rec.BoolValue,
		IntValue:    rec.IntValue,
		TextValue:   rec.TextValue,
	}
}

func decisionCandidateRecordFromLog(requestID string, rec decisionCandidateLogRecord) *decisionTargetCandidateRecord {
	return &decisionTargetCandidateRecord{
		RequestID:                   requestID,
		CandidateIndex:              rec.CandidateIndex,
		GroupTargetIndex:            rec.GroupTargetIndex,
		Provider:                    rec.Provider,
		Model:                       rec.Model,
		ModelRef:                    rec.ModelRef,
		Dialect:                     rec.Dialect,
		Weight:                      rec.Weight,
		ToolOnly:                    rec.ToolOnly,
		ContextTokens:               rec.ContextTokens,
		MaxEstimatedInputTokens:     rec.MaxEstimatedInputTokens,
		MaxRequestedOutputTokens:    rec.MaxRequestedOutputTokens,
		MaxRequestBytes:             rec.MaxRequestBytes,
		MaxToolSchemaBytes:          rec.MaxToolSchemaBytes,
		EstimatedTotalInputTokens:   rec.EstimatedTotalInputTokens,
		RequestedOutputCapTokens:    rec.RequestedOutputCapTokens,
		EstimatedTotalWithOutputCap: rec.EstimatedTotalWithOutputCap,
		RequestBytes:                rec.RequestBytes,
		ToolSchemaBytes:             rec.ToolSchemaBytes,
		ContextHeadroomTokens:       rec.ContextHeadroomTokens,
		ContextFit:                  rec.ContextFit,
		RequestBytesFit:             rec.RequestBytesFit,
		ToolSchemaFit:               rec.ToolSchemaFit,
		EligibilityDecision:         safeOptionalReasonToken(rec.EligibilityDecision),
		EligibilityReason:           safeOptionalReasonToken(rec.EligibilityReason),
		InputImage:                  rec.InputImage,
		OutputImage:                 rec.OutputImage,
		ToolSupport:                 rec.ToolSupport,
		ForcedToolChoice:            rec.ForcedToolChoice,
		StructuredOutput:            rec.StructuredOutput,
		HonorsMaxTokens:             rec.HonorsMaxTokens,
		ReasoningSupport:            rec.ReasoningSupport,
		ReasoningMode:               rec.ReasoningMode,
		ReasoningControl:            rec.ReasoningControl,
		ReasoningDefault:            rec.ReasoningDefault,
		ReasoningStream:             rec.ReasoningStream,
		ValidationStatus:            rec.ValidationStatus,
		ValidationAge:               rec.ValidationAge,
		Eligible:                    rec.Eligible,
		Selected:                    rec.Selected,
	}
}

func decisionFilterReasonRecordFromLog(requestID string, rec decisionFilterReasonLogRecord) *decisionTargetFilterReasonRecord {
	return &decisionTargetFilterReasonRecord{
		RequestID:      requestID,
		Seq:            rec.Seq,
		CandidateIndex: rec.CandidateIndex,
		Stage:          rec.Stage,
		Reason:         rec.Reason,
	}
}

func routingDecisionRecordFromLog(requestID string, rec routingDecisionLogRecord) *routingDecisionRecord {
	classLabel := ""
	if rec.ClassLabel != nil {
		classLabel = *rec.ClassLabel
	}
	return &routingDecisionRecord{
		RequestID:              requestID,
		Seq:                    rec.Seq,
		Strategy:               rec.Strategy,
		SelectedCandidateIndex: rec.SelectedCandidateIndex,
		Provider:               rec.Provider,
		Model:                  rec.Model,
		Dialect:                rec.Dialect,
		FallbackCount:          rec.FallbackCount,
		ClassLabel:             classLabel,
	}
}

func routingSignalRecordFromLog(requestID string, rec routingSignalLogRecord) *routingSignalRecord {
	return &routingSignalRecord{
		RequestID:      requestID,
		Seq:            rec.Seq,
		Strategy:       rec.Strategy,
		SignalName:     rec.SignalName,
		Source:         rec.Source,
		CandidateIndex: rec.CandidateIndex,
		BoolValue:      rec.BoolValue,
		IntValue:       rec.IntValue,
		FloatValue:     rec.FloatValue,
		TextValue:      rec.TextValue,
	}
}

func dynamicScoreTermRecordFromLog(requestID string, rec dynamicScoreTermLogRecord) *dynamicScoreTermRecord {
	valueBucket := defaultString(rec.ValueBucket, scoreValueBucket(rec.Value))
	contributionBucket := defaultString(rec.ContributionBucket, scoreValueBucket(rec.Contribution))
	finalScoreBucket := defaultString(rec.FinalScoreBucket, scoreValueBucket(rec.FinalScore))
	return &dynamicScoreTermRecord{
		RequestID:          requestID,
		Seq:                rec.Seq,
		CandidateIndex:     rec.CandidateIndex,
		Rank:               rec.Rank,
		Provider:           rec.Provider,
		Model:              rec.Model,
		Dialect:            rec.Dialect,
		TermName:           rec.TermName,
		ScoreName:          rec.ScoreName,
		Weight:             rec.Weight,
		Value:              rec.Value,
		Contribution:       rec.Contribution,
		FinalScore:         rec.FinalScore,
		ValueBucket:        valueBucket,
		ContributionBucket: contributionBucket,
		FinalScoreBucket:   finalScoreBucket,
		ObservationCount:   rec.ObservationCount,
		Selected:           rec.Selected,
	}
}

func policyExecutionRecordFromLog(requestID string, rec policyExecutionLogRecord) *policyExecutionRecord {
	classLabel := ""
	if rec.ClassLabel != nil {
		classLabel = *rec.ClassLabel
	}
	return &policyExecutionRecord{
		RequestID:              requestID,
		Seq:                    rec.Seq,
		Strategy:               rec.Strategy,
		PolicyKind:             rec.PolicyKind,
		Outcome:                rec.Outcome,
		DurationMS:             rec.DurationMS,
		EligibleTargetCount:    rec.EligibleTargetCount,
		AllTargetCount:         rec.AllTargetCount,
		SelectedCandidateIndex: rec.SelectedCandidateIndex,
		FallbackCount:          rec.FallbackCount,
		ClassLabel:             classLabel,
		ErrorClass:             rec.ErrorClass,
		ErrorMessage:           sanitizePersistedDiagnosticText(rec.ErrorMessage),
		TerminalErrorType:      safeOptionalReasonToken(rec.TerminalErrorType),
	}
}

func fallbackTransitionRecordFromLog(requestID string, rec fallbackTransitionLogRecord) *fallbackTransitionRecord {
	return &fallbackTransitionRecord{
		RequestID:              requestID,
		Seq:                    rec.Seq,
		AttemptIndex:           rec.AttemptIndex,
		FailedCandidateIndex:   rec.FailedCandidateIndex,
		FallbackCandidateIndex: rec.FallbackCandidateIndex,
		FailedProvider:         rec.FailedProvider,
		FailedModel:            rec.FailedModel,
		FailedDialect:          rec.FailedDialect,
		FallbackProvider:       rec.FallbackProvider,
		FallbackModel:          rec.FallbackModel,
		FallbackDialect:        rec.FallbackDialect,
		FallbackReason:         safeOptionalReasonToken(rec.FallbackReason),
		ErrorClass:             safeOptionalReasonToken(rec.ErrorClass),
		Retryable:              rec.Retryable,
		FallbackSucceeded:      rec.FallbackSucceeded,
	}
}

func decisionCacheReasonRecordFromLog(requestID string, rec cacheReasonLogRecord) *decisionCacheReasonRecord {
	return &decisionCacheReasonRecord{
		RequestID:      requestID,
		Seq:            rec.Seq,
		Status:         rec.Status,
		Reason:         rec.Reason,
		CandidateIndex: rec.CandidateIndex,
		Provider:       rec.Provider,
		Model:          rec.Model,
		Dialect:        rec.Dialect,
	}
}

func errorRecordFromLog(rec logRecord) *requestErrorRecord {
	errType := ""
	if rec.Error != nil {
		errType = *rec.Error
	}
	retryable := errorTypeRetryable(errType)
	if rec.ErrorClass != "" {
		for i := len(rec.AttemptsDetail) - 1; i >= 0; i-- {
			if rec.AttemptsDetail[i].ErrorClass == rec.ErrorClass {
				retryable = rec.AttemptsDetail[i].Retryable
				break
			}
		}
	}
	return &requestErrorRecord{
		RequestID:    rec.RequestID,
		TS:           rec.TS,
		Status:       rec.Status,
		ErrorType:    errType,
		ErrorClass:   rec.ErrorClass,
		ErrorMessage: sanitizePersistedDiagnosticText(rec.ErrorMessage),
		Retryable:    retryable,
		Attempts:     rec.Attempts,
		Provider:     rec.TargetProvider,
		Model:        rec.TargetModel,
		Dialect:      rec.TargetDialect,
	}
}

func rowFromRecord(rec logRecord) usageRow {
	ts, err := parseUsageTime(rec.TS)
	if err != nil {
		ts = time.Now().UTC()
	}
	errText := ""
	if rec.Error != nil {
		errText = *rec.Error
	}
	tokenID := rec.TokenID
	if rec.CallerID == "" {
		tokenID = defaultString(rec.TokenID, "unauthorized")
		if tokenID != "missing-token" {
			tokenID = "invalid-token"
		}
	} else {
		tokenID = publicTokenID(tokenID)
	}
	reasoningTokens, reasoningAttempts, reasoningSuccesses, reasoningReported := rec.ReasoningTokens, rec.ReasoningAttemptCount, rec.ReasoningSuccessfulAttemptCount, rec.ReasoningReportedAttemptCount
	if !rec.ReasoningCoverageMeasured {
		reasoningTokens, reasoningAttempts, reasoningSuccesses, reasoningReported = reasoningUsageCoverage(rec.AttemptsDetail)
	}
	return usageRow{
		TS:                                 ts,
		RequestID:                          rec.RequestID,
		CallerID:                           rec.CallerID,
		CallerUser:                         rec.CallerUser,
		CallerProject:                      rec.CallerProject,
		CallerEnvironment:                  rec.CallerEnvironment,
		CallerIP:                           rec.CallerIP,
		TokenID:                            tokenID,
		Client:                             rec.Client,
		InboundDialect:                     rec.InboundDialect,
		RequestedModel:                     rec.RequestedModel,
		ResolvedGroup:                      defaultString(rec.ResolvedGroup, rec.RequestedModel),
		Strategy:                           rec.Strategy,
		TargetProvider:                     rec.TargetProvider,
		TargetModel:                        rec.TargetModel,
		TargetDialect:                      rec.TargetDialect,
		TargetRegion:                       rec.TargetRegion,
		Stream:                             rec.Stream,
		Cache:                              defaultString(rec.Cache, "bypass"),
		Status:                             rec.Status,
		Attempts:                           rec.Attempts,
		FallbackUsed:                       rec.FallbackUsed,
		LatencyMS:                          rec.LatencyMS,
		TTFBMS:                             rec.TTFBMS,
		UpstreamMS:                         rec.UpstreamMS,
		DownstreamMS:                       rec.DownstreamMS,
		UpstreamOutputTPS:                  rec.UpstreamOutputTPS,
		UpstreamTotalTPS:                   rec.UpstreamTotalTPS,
		DownstreamOutputTPS:                rec.DownstreamOutputTPS,
		DownstreamTotalTPS:                 rec.DownstreamTotalTPS,
		InputTokens:                        rec.Usage.InputTokens,
		OutputTokens:                       rec.Usage.OutputTokens,
		TotalTokens:                        rec.Usage.TotalTokens,
		ReasoningTokens:                    reasoningTokens,
		CachedInputTokens:                  rec.Usage.CachedInputTokens,
		ReasoningAttemptCount:              reasoningAttempts,
		ReasoningSuccessfulAttemptCount:    reasoningSuccesses,
		ReasoningReportedAttemptCount:      reasoningReported,
		InputHasImage:                      rec.InputHasImage,
		InputImageCount:                    rec.InputImageCount,
		InputImageTokens:                   rec.InputImageTokens,
		PIIFilterApplied:                   rec.PIIFilterApplied,
		PIIFilterMode:                      rec.PIIFilterMode,
		PIIFilterReplacements:              rec.PIIFilterReplacements,
		PIIFilterRuleCount:                 rec.PIIFilterRuleCount,
		ContractPresent:                    rec.ContractPresent,
		ContractBucket:                     rec.ContractBucket,
		ContractFailureReason:              rec.ContractFailureReason,
		ContractWorkload:                   rec.ContractWorkload,
		TargetValidationStatus:             rec.TargetValidationStatus,
		TargetValidationWorkload:           rec.TargetValidationWorkload,
		TargetValidationAgeBucket:          rec.TargetValidationAgeBucket,
		InputPricePerMillionUSD:            rec.InputPricePerMillionUSD,
		OutputPricePerMillionUSD:           rec.OutputPricePerMillionUSD,
		CachedInputPricePerMillionUSD:      rec.CachedInputPricePerMillionUSD,
		ImageInputPricePerMillionTokensUSD: rec.ImageInputPricePerMillionTokensUSD,
		ImageInputPricePerImageUSD:         rec.ImageInputPricePerImageUSD,
		InputCostUSD:                       rec.InputCostUSD,
		ImageCostUSD:                       rec.ImageCostUSD,
		OutputCostUSD:                      rec.OutputCostUSD,
		TotalCostUSD:                       rec.TotalCostUSD,
		UpstreamReportedInputCostUSD:       rec.UpstreamReportedInputCostUSD,
		UpstreamReportedOutputCostUSD:      rec.UpstreamReportedOutputCostUSD,
		UpstreamReportedTotalCostUSD:       rec.UpstreamReportedTotalCostUSD,
		PricingSource:                      rec.PricingSource,
		PricingUpdatedAt:                   rec.PricingUpdatedAt,
		CacheEnabled:                       rec.CacheEnabled,
		CacheItems:                         rec.CacheItems,
		CacheBytes:                         rec.CacheBytes,
		CacheMaxBytes:                      rec.CacheMaxBytes,
		CacheOccupancyPct:                  rec.CacheOccupancyPct,
		QuotaState:                         rec.QuotaState,
		KeyState:                           rec.KeyState,
		TrafficShapeApplied:                rec.TrafficShapeApplied,
		TrafficShapeDecision:               rec.TrafficShapeDecision,
		TrafficShapeScope:                  rec.TrafficShapeScope,
		TrafficShapeBucket:                 rec.TrafficShapeBucket,
		TrafficShapeRetryAfterMS:           rec.TrafficShapeRetryAfterMS,
		TrafficShapeQueueWaitMS:            rec.TrafficShapeQueueWaitMS,
		TrafficShapeEstimatedInputTokens:   rec.TrafficShapeEstimatedInputTokens,
		TrafficShapeReservedOutputTokens:   rec.TrafficShapeReservedOutputTokens,
		TrafficShapeTotalReservedTokens:    rec.TrafficShapeTotalReservedTokens,
		RouterVersion:                      rec.RouterVersion,
		RouterBuildDate:                    rec.RouterBuildDate,
		RoutingConfigFingerprint:           rec.RoutingConfigFingerprint,
		ModelGroupConfigFingerprint:        rec.ModelGroupConfigFingerprint,
		RoutingPolicyFingerprint:           rec.RoutingPolicyFingerprint,
		PricingCatalogFingerprint:          rec.PricingCatalogFingerprint,
		Error:                              errText,
	}
}

// reasoningUsageCoverage preserves absence. Reasoning token counts are a subset
// of output tokens and are intentionally not used in cost or throughput totals.
func reasoningUsageCoverage(attempts []attemptLogRecord) (total *int, attempted, successful, reported int) {
	var sum int
	for _, attempt := range attempts {
		attempted++
		// HTTP 2xx alone is not a completed upstream attempt: read, size, or
		// decode failures can fall back after a 2xx response. Selected is set
		// only after router processing succeeds and the attempt is terminal.
		if attempt.Selected {
			successful++
		}
		if attempt.ReasoningTokens != nil {
			reported++
			sum += *attempt.ReasoningTokens
		}
	}
	if reported > 0 {
		return &sum, attempted, successful, reported
	}
	return nil, attempted, successful, 0
}

func recordFromRow(row usageRow) *usageRecord {
	return &usageRecord{
		RequestID:                          row.RequestID,
		TS:                                 formatUsageTime(row.TS),
		CallerID:                           row.CallerID,
		CallerUser:                         row.CallerUser,
		CallerProject:                      row.CallerProject,
		CallerEnvironment:                  row.CallerEnvironment,
		CallerIP:                           row.CallerIP,
		TokenID:                            row.TokenID,
		Client:                             row.Client,
		InboundDialect:                     row.InboundDialect,
		RequestedModel:                     row.RequestedModel,
		ResolvedGroup:                      row.ResolvedGroup,
		Strategy:                           row.Strategy,
		TargetProvider:                     row.TargetProvider,
		TargetModel:                        row.TargetModel,
		TargetDialect:                      row.TargetDialect,
		TargetRegion:                       row.TargetRegion,
		Stream:                             row.Stream,
		Cache:                              row.Cache,
		Status:                             row.Status,
		Attempts:                           row.Attempts,
		FallbackUsed:                       row.FallbackUsed,
		LatencyMS:                          row.LatencyMS,
		TTFBMS:                             row.TTFBMS,
		UpstreamMS:                         row.UpstreamMS,
		DownstreamMS:                       row.DownstreamMS,
		UpstreamOutputTPS:                  row.UpstreamOutputTPS,
		UpstreamTotalTPS:                   row.UpstreamTotalTPS,
		DownstreamOutputTPS:                row.DownstreamOutputTPS,
		DownstreamTotalTPS:                 row.DownstreamTotalTPS,
		InputTokens:                        row.InputTokens,
		OutputTokens:                       row.OutputTokens,
		TotalTokens:                        row.TotalTokens,
		ReasoningTokens:                    row.ReasoningTokens,
		CachedInputTokens:                  row.CachedInputTokens,
		ReasoningAttemptCount:              row.ReasoningAttemptCount,
		ReasoningSuccessfulAttemptCount:    row.ReasoningSuccessfulAttemptCount,
		ReasoningReportedAttemptCount:      row.ReasoningReportedAttemptCount,
		InputHasImage:                      row.InputHasImage,
		InputImageCount:                    row.InputImageCount,
		InputImageTokens:                   row.InputImageTokens,
		PIIFilterApplied:                   row.PIIFilterApplied,
		PIIFilterMode:                      row.PIIFilterMode,
		PIIFilterReplacements:              row.PIIFilterReplacements,
		PIIFilterRuleCount:                 row.PIIFilterRuleCount,
		ContractPresent:                    row.ContractPresent,
		ContractBucket:                     row.ContractBucket,
		ContractFailureReason:              row.ContractFailureReason,
		ContractWorkload:                   row.ContractWorkload,
		TargetValidationStatus:             row.TargetValidationStatus,
		TargetValidationWorkload:           row.TargetValidationWorkload,
		TargetValidationAgeBucket:          row.TargetValidationAgeBucket,
		InputPricePerMillionUSD:            row.InputPricePerMillionUSD,
		OutputPricePerMillionUSD:           row.OutputPricePerMillionUSD,
		CachedInputPricePerMillionUSD:      row.CachedInputPricePerMillionUSD,
		ImageInputPricePerMillionTokensUSD: row.ImageInputPricePerMillionTokensUSD,
		ImageInputPricePerImageUSD:         row.ImageInputPricePerImageUSD,
		InputCostUSD:                       row.InputCostUSD,
		ImageCostUSD:                       row.ImageCostUSD,
		OutputCostUSD:                      row.OutputCostUSD,
		TotalCostUSD:                       row.TotalCostUSD,
		UpstreamReportedInputCostUSD:       row.UpstreamReportedInputCostUSD,
		UpstreamReportedOutputCostUSD:      row.UpstreamReportedOutputCostUSD,
		UpstreamReportedTotalCostUSD:       row.UpstreamReportedTotalCostUSD,
		PricingSource:                      row.PricingSource,
		PricingUpdatedAt:                   row.PricingUpdatedAt,
		CacheEnabled:                       row.CacheEnabled,
		CacheItems:                         row.CacheItems,
		CacheBytes:                         row.CacheBytes,
		CacheMaxBytes:                      row.CacheMaxBytes,
		CacheOccupancyPct:                  row.CacheOccupancyPct,
		QuotaState:                         row.QuotaState,
		KeyState:                           row.KeyState,
		TrafficShapeApplied:                row.TrafficShapeApplied,
		TrafficShapeDecision:               row.TrafficShapeDecision,
		TrafficShapeScope:                  row.TrafficShapeScope,
		TrafficShapeBucket:                 row.TrafficShapeBucket,
		TrafficShapeRetryAfterMS:           row.TrafficShapeRetryAfterMS,
		TrafficShapeQueueWaitMS:            row.TrafficShapeQueueWaitMS,
		TrafficShapeEstimatedInputTokens:   row.TrafficShapeEstimatedInputTokens,
		TrafficShapeReservedOutputTokens:   row.TrafficShapeReservedOutputTokens,
		TrafficShapeTotalReservedTokens:    row.TrafficShapeTotalReservedTokens,
		RouterVersion:                      row.RouterVersion,
		RouterBuildDate:                    row.RouterBuildDate,
		RoutingConfigFingerprint:           row.RoutingConfigFingerprint,
		ModelGroupConfigFingerprint:        row.ModelGroupConfigFingerprint,
		RoutingPolicyFingerprint:           row.RoutingPolicyFingerprint,
		PricingCatalogFingerprint:          row.PricingCatalogFingerprint,
		Error:                              row.Error,
	}
}

func rowFromUsageRecord(record usageRecord) (usageRow, error) {
	ts, err := parseUsageTime(record.TS)
	if err != nil {
		return usageRow{}, err
	}
	return usageRow{
		TS:                                 ts,
		RequestID:                          record.RequestID,
		CallerID:                           record.CallerID,
		CallerUser:                         record.CallerUser,
		CallerProject:                      record.CallerProject,
		CallerEnvironment:                  record.CallerEnvironment,
		CallerIP:                           record.CallerIP,
		TokenID:                            record.TokenID,
		Client:                             record.Client,
		InboundDialect:                     record.InboundDialect,
		RequestedModel:                     record.RequestedModel,
		ResolvedGroup:                      record.ResolvedGroup,
		Strategy:                           record.Strategy,
		TargetProvider:                     record.TargetProvider,
		TargetModel:                        record.TargetModel,
		TargetDialect:                      record.TargetDialect,
		TargetRegion:                       record.TargetRegion,
		Stream:                             record.Stream,
		Cache:                              record.Cache,
		Status:                             record.Status,
		Attempts:                           record.Attempts,
		FallbackUsed:                       record.FallbackUsed,
		LatencyMS:                          record.LatencyMS,
		TTFBMS:                             record.TTFBMS,
		UpstreamMS:                         record.UpstreamMS,
		DownstreamMS:                       record.DownstreamMS,
		UpstreamOutputTPS:                  record.UpstreamOutputTPS,
		UpstreamTotalTPS:                   record.UpstreamTotalTPS,
		DownstreamOutputTPS:                record.DownstreamOutputTPS,
		DownstreamTotalTPS:                 record.DownstreamTotalTPS,
		InputTokens:                        record.InputTokens,
		OutputTokens:                       record.OutputTokens,
		TotalTokens:                        record.TotalTokens,
		ReasoningTokens:                    record.ReasoningTokens,
		CachedInputTokens:                  record.CachedInputTokens,
		ReasoningAttemptCount:              record.ReasoningAttemptCount,
		ReasoningSuccessfulAttemptCount:    record.ReasoningSuccessfulAttemptCount,
		ReasoningReportedAttemptCount:      record.ReasoningReportedAttemptCount,
		InputHasImage:                      record.InputHasImage,
		InputImageCount:                    record.InputImageCount,
		InputImageTokens:                   record.InputImageTokens,
		PIIFilterApplied:                   record.PIIFilterApplied,
		PIIFilterMode:                      record.PIIFilterMode,
		PIIFilterReplacements:              record.PIIFilterReplacements,
		PIIFilterRuleCount:                 record.PIIFilterRuleCount,
		ContractPresent:                    record.ContractPresent,
		ContractBucket:                     record.ContractBucket,
		ContractFailureReason:              record.ContractFailureReason,
		ContractWorkload:                   record.ContractWorkload,
		TargetValidationStatus:             record.TargetValidationStatus,
		TargetValidationWorkload:           record.TargetValidationWorkload,
		TargetValidationAgeBucket:          record.TargetValidationAgeBucket,
		InputPricePerMillionUSD:            record.InputPricePerMillionUSD,
		OutputPricePerMillionUSD:           record.OutputPricePerMillionUSD,
		CachedInputPricePerMillionUSD:      record.CachedInputPricePerMillionUSD,
		ImageInputPricePerMillionTokensUSD: record.ImageInputPricePerMillionTokensUSD,
		ImageInputPricePerImageUSD:         record.ImageInputPricePerImageUSD,
		InputCostUSD:                       record.InputCostUSD,
		ImageCostUSD:                       record.ImageCostUSD,
		OutputCostUSD:                      record.OutputCostUSD,
		TotalCostUSD:                       record.TotalCostUSD,
		UpstreamReportedInputCostUSD:       record.UpstreamReportedInputCostUSD,
		UpstreamReportedOutputCostUSD:      record.UpstreamReportedOutputCostUSD,
		UpstreamReportedTotalCostUSD:       record.UpstreamReportedTotalCostUSD,
		PricingSource:                      record.PricingSource,
		PricingUpdatedAt:                   record.PricingUpdatedAt,
		CacheEnabled:                       record.CacheEnabled,
		CacheItems:                         record.CacheItems,
		CacheBytes:                         record.CacheBytes,
		CacheMaxBytes:                      record.CacheMaxBytes,
		CacheOccupancyPct:                  record.CacheOccupancyPct,
		QuotaState:                         record.QuotaState,
		KeyState:                           record.KeyState,
		TrafficShapeApplied:                record.TrafficShapeApplied,
		TrafficShapeDecision:               record.TrafficShapeDecision,
		TrafficShapeScope:                  record.TrafficShapeScope,
		TrafficShapeBucket:                 record.TrafficShapeBucket,
		TrafficShapeRetryAfterMS:           record.TrafficShapeRetryAfterMS,
		TrafficShapeQueueWaitMS:            record.TrafficShapeQueueWaitMS,
		TrafficShapeEstimatedInputTokens:   record.TrafficShapeEstimatedInputTokens,
		TrafficShapeReservedOutputTokens:   record.TrafficShapeReservedOutputTokens,
		TrafficShapeTotalReservedTokens:    record.TrafficShapeTotalReservedTokens,
		RouterVersion:                      record.RouterVersion,
		RouterBuildDate:                    record.RouterBuildDate,
		RoutingConfigFingerprint:           record.RoutingConfigFingerprint,
		ModelGroupConfigFingerprint:        record.ModelGroupConfigFingerprint,
		RoutingPolicyFingerprint:           record.RoutingPolicyFingerprint,
		PricingCatalogFingerprint:          record.PricingCatalogFingerprint,
		Error:                              record.Error,
	}, nil
}

func securityAccessRecordFromEvent(event securityAccessEvent) *securityAccessEventRecord {
	return &securityAccessEventRecord{
		TS:                  formatUsageTime(event.TS),
		RequestID:           event.RequestID,
		EventType:           event.EventType,
		Surface:             event.Surface,
		HTTPMethod:          event.HTTPMethod,
		PathTemplate:        event.PathTemplate,
		StatusCode:          event.StatusCode,
		Outcome:             event.Outcome,
		ReasonCode:          event.ReasonCode,
		AuthSubject:         event.AuthSubject,
		AuthSource:          event.AuthSource,
		CallerID:            event.CallerID,
		CallerUser:          event.CallerUser,
		CallerProject:       event.CallerProject,
		CallerEnvironment:   event.CallerEnvironment,
		TokenID:             event.TokenID,
		AdminSubject:        event.AdminSubject,
		AdminDomain:         event.AdminDomain,
		Client:              event.Client,
		UserAgentFamily:     event.UserAgentFamily,
		IPAddress:           event.IPAddress,
		IPVersion:           event.IPVersion,
		IPSource:            event.IPSource,
		TrustedProxyApplied: event.TrustedProxyApplied,
		RequestIsPrivate:    event.RequestIsPrivate,
		RequestIsLoopback:   event.RequestIsLoopback,
		RequestIsReserved:   event.RequestIsReserved,
		ModelGroup:          event.ModelGroup,
		RequestedModel:      event.RequestedModel,
		ResolvedGroup:       event.ResolvedGroup,
		InputTokens:         event.InputTokens,
		OutputTokens:        event.OutputTokens,
		TotalTokens:         event.TotalTokens,
	}
}

func securityAccessEventFromRecord(record securityAccessEventRecord) (securityAccessEvent, error) {
	ts, err := parseUsageTime(record.TS)
	if err != nil {
		return securityAccessEvent{}, err
	}
	return securityAccessEvent{
		ID:                  record.ID,
		TS:                  ts,
		RequestID:           record.RequestID,
		EventType:           record.EventType,
		Surface:             record.Surface,
		HTTPMethod:          record.HTTPMethod,
		PathTemplate:        record.PathTemplate,
		StatusCode:          record.StatusCode,
		Outcome:             record.Outcome,
		ReasonCode:          record.ReasonCode,
		AuthSubject:         record.AuthSubject,
		AuthSource:          record.AuthSource,
		CallerID:            record.CallerID,
		CallerUser:          record.CallerUser,
		CallerProject:       record.CallerProject,
		CallerEnvironment:   record.CallerEnvironment,
		TokenID:             record.TokenID,
		AdminSubject:        record.AdminSubject,
		AdminDomain:         record.AdminDomain,
		Client:              record.Client,
		UserAgentFamily:     record.UserAgentFamily,
		IPAddress:           record.IPAddress,
		IPVersion:           record.IPVersion,
		IPSource:            record.IPSource,
		TrustedProxyApplied: record.TrustedProxyApplied,
		RequestIsPrivate:    record.RequestIsPrivate,
		RequestIsLoopback:   record.RequestIsLoopback,
		RequestIsReserved:   record.RequestIsReserved,
		ModelGroup:          record.ModelGroup,
		RequestedModel:      record.RequestedModel,
		ResolvedGroup:       record.ResolvedGroup,
		InputTokens:         record.InputTokens,
		OutputTokens:        record.OutputTokens,
		TotalTokens:         record.TotalTokens,
	}, nil
}

func ImportUsageJSONL(dbPath, logPath string) (int, error) {
	return ImportUsageJSONLTo(UsageDBConfig{Driver: "sqlite", Path: dbPath}, logPath)
}

func ImportUsageJSONLTo(cfg UsageDBConfig, logPath string) (int, error) {
	store, err := OpenUsageStore(cfg)
	if err != nil {
		return 0, err
	}
	defer store.Close()

	f, err := os.Open(logPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if usageJSONLLineHasEventType(line) {
			continue
		}
		var rec logRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return count, fmt.Errorf("parse %s line %d: %w", logPath, count+1, err)
		}
		store.Emit(rec)
		count++
	}
	return count, scanner.Err()
}

func usageJSONLLineHasEventType(line string) bool {
	var envelope struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return false
	}
	return envelope.EventType != ""
}

func GenerateUsageMarkdown(opts UsageReportOptions) (string, error) {
	if err := validateUsageReportOptions(&opts); err != nil {
		return "", err
	}
	driver := strings.ToLower(defaultString(opts.Driver, "sqlite"))
	cfg := UsageDBConfig{Driver: driver, Path: opts.DBPath, DSN: opts.DSN, MigrationPolicy: opts.MigrationPolicy}
	if opts.LogPath != "" {
		if _, err := ImportUsageJSONLTo(cfg, opts.LogPath); err != nil {
			return "", err
		}
	}
	store, err := OpenUsageStore(cfg)
	if err != nil {
		return "", err
	}
	defer store.Close()

	rows, err := store.rows(opts)
	if err != nil {
		return "", err
	}
	decisionSummary := store.decisionTelemetrySummary(rows)
	upstreamShapeEvents, err := store.upstreamShapeEventsForRows(rows, opts)
	if err != nil {
		return "", err
	}
	return renderUsageCLIMarkdown(opts.From, opts.To, rows, decisionSummary, upstreamShapeEvents), nil
}

// ExportReasoningCoverage returns the bounded scalar reasoning-usage aggregate
// for the same protected filters and time window used by usage reporting.
func ExportReasoningCoverage(opts UsageReportOptions) (ReasoningCoverageExport, error) {
	if err := validateUsageReportOptions(&opts); err != nil {
		return ReasoningCoverageExport{}, err
	}
	cfg := UsageDBConfig{Driver: strings.ToLower(defaultString(opts.Driver, "sqlite")), Path: opts.DBPath, DSN: opts.DSN, MigrationPolicy: opts.MigrationPolicy}
	if opts.LogPath != "" {
		if _, err := ImportUsageJSONLTo(cfg, opts.LogPath); err != nil {
			return ReasoningCoverageExport{}, err
		}
	}
	store, err := OpenUsageStore(cfg)
	if err != nil {
		return ReasoningCoverageExport{}, err
	}
	defer store.Close()
	rows, err := store.rowsWithoutBuckets(opts)
	if err != nil {
		return ReasoningCoverageExport{}, err
	}
	result := ReasoningCoverageExport{}
	requestIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		requestIDs = append(requestIDs, row.RequestID)
		if row.ReasoningTokens != nil {
			result.ReasoningTokens += *row.ReasoningTokens
		}
		result.ReasoningAttemptCount += row.ReasoningAttemptCount
		result.ReasoningSuccessfulAttemptCount += row.ReasoningSuccessfulAttemptCount
		result.ReasoningReportedAttemptCount += row.ReasoningReportedAttemptCount
	}
	byTarget := map[string]*ReasoningCoverageRow{}
	for start := 0; start < len(requestIDs); start += 500 {
		end := start + 500
		if end > len(requestIDs) {
			end = len(requestIDs)
		}
		var attempts []requestAttemptRecord
		if err := store.db.Where("request_id IN ?", requestIDs[start:end]).Find(&attempts).Error; err != nil {
			return ReasoningCoverageExport{}, err
		}
		for _, attempt := range attempts {
			key := attempt.Provider + "\x00" + attempt.Model + "\x00" + attempt.Dialect
			entry := byTarget[key]
			if entry == nil {
				entry = &ReasoningCoverageRow{Provider: attempt.Provider, Model: attempt.Model, Dialect: attempt.Dialect}
				byTarget[key] = entry
			}
			entry.Attempts++
			if attempt.ReasoningTokens != nil {
				entry.ReportedAttempts++
				entry.ReasoningTokens += *attempt.ReasoningTokens
			}
		}
	}
	for _, entry := range byTarget {
		result.Coverage = append(result.Coverage, *entry)
	}
	coverageAttempts := 0
	for _, entry := range result.Coverage {
		coverageAttempts += entry.Attempts
	}
	result.CoverageComplete = coverageAttempts == result.ReasoningAttemptCount
	sort.Slice(result.Coverage, func(i, j int) bool {
		if result.Coverage[i].Provider != result.Coverage[j].Provider {
			return result.Coverage[i].Provider < result.Coverage[j].Provider
		}
		if result.Coverage[i].Model != result.Coverage[j].Model {
			return result.Coverage[i].Model < result.Coverage[j].Model
		}
		return result.Coverage[i].Dialect < result.Coverage[j].Dialect
	})
	return result, nil
}

func validateUsageReportOptions(opts *UsageReportOptions) error {
	driver := strings.ToLower(defaultString(opts.Driver, "sqlite"))
	if driver == "sqlite" && opts.DBPath == "" {
		return errors.New("usage db path is required")
	}
	if (driver == "postgres" || driver == "postgresql") && opts.DSN == "" {
		return errors.New("usage db dsn is required")
	}
	if opts.To.IsZero() {
		opts.To = time.Now().UTC()
	}
	if opts.From.IsZero() {
		opts.From = opts.To.Add(-24 * time.Hour)
	}
	if !opts.From.Before(opts.To) {
		return errors.New("from must be before to")
	}
	return nil
}

func GenerateRetentionStatus(opts RetentionStatusOptions) (RetentionStatusResult, error) {
	opts.Config.DryRun = boolPtr(true)
	return generateRetentionJob(opts)
}

func GenerateRetentionRun(opts RetentionStatusOptions) (RetentionStatusResult, error) {
	return generateRetentionJob(opts)
}

func generateRetentionJob(opts RetentionStatusOptions) (RetentionStatusResult, error) {
	driver := strings.ToLower(defaultString(opts.Driver, "sqlite"))
	if driver == "sqlite" && opts.DBPath == "" {
		return RetentionStatusResult{}, errors.New("usage db path is required")
	}
	if (driver == "postgres" || driver == "postgresql") && opts.DSN == "" {
		return RetentionStatusResult{}, errors.New("usage db dsn is required")
	}
	cfg := opts.Config
	defaultRetentionConfig(&cfg)
	if err := validateRetentionConfig(cfg); err != nil {
		return RetentionStatusResult{}, err
	}
	if !cfg.Enabled {
		return RetentionStatusResult{}, errors.New("server retention is disabled")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	opts.Now = opts.Now.UTC()
	store, err := OpenUsageStore(UsageDBConfig{Driver: driver, Path: opts.DBPath, DSN: opts.DSN})
	if err != nil {
		return RetentionStatusResult{}, err
	}
	defer store.Close()
	opts.Config = cfg
	return store.runRetentionJob(opts)
}

func (s *usageStore) runRetentionStatus(opts RetentionStatusOptions) (RetentionStatusResult, error) {
	opts.Config.DryRun = boolPtr(true)
	return s.runRetentionJob(opts)
}

func (s *usageStore) runRetentionJob(opts RetentionStatusOptions) (RetentionStatusResult, error) {
	if s == nil || s.db == nil {
		return RetentionStatusResult{}, errors.New("usage store is not open")
	}
	cfg := opts.Config
	defaultRetentionConfig(&cfg)
	if err := validateRetentionConfig(cfg); err != nil {
		return RetentionStatusResult{}, err
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	startedAt := formatUsageTime(now)
	dryRun := cfg.DryRun == nil || *cfg.DryRun
	mode := "run"
	if dryRun {
		mode = "status"
	}
	result := RetentionStatusResult{Status: "completed", StartedAt: now, CompletedAt: now}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		policy, err := ensureActiveRetentionPolicy(tx, cfg, now)
		if err != nil {
			return err
		}
		job := retentionJobRecord{
			PolicyVersionID: policy.ID,
			Mode:            mode,
			Status:          "running",
			DryRun:          dryRun,
			StartedAt:       startedAt,
			RequestedBy:     opts.RequestedBy,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		for _, class := range cfg.Classes {
			if !retentionClassEnabled(class) {
				continue
			}
			cutoff := now.Add(-time.Duration(class.RetentionDays) * 24 * time.Hour)
			for _, table := range retentionTablesForClass(class.DataClass) {
				tableResult, err := runRetentionTable(tx, class, table, cutoff, dryRun)
				if err != nil {
					job.Status = "failed"
					job.ErrorMessage = err.Error()
					job.CompletedAt = formatUsageTime(time.Now().UTC())
					_ = tx.Save(&job).Error
					return err
				}
				row := retentionJobTableResultRecord{
					JobID:         job.ID,
					DataClass:     tableResult.DataClass,
					StorageTable:  tableResult.TableName,
					CutoffTS:      formatUsageTime(tableResult.Cutoff),
					RetentionDays: tableResult.RetentionDays,
					BatchSize:     tableResult.BatchSize,
					CandidateRows: tableResult.CandidateRows,
					HeldRows:      tableResult.HeldRows,
					EligibleRows:  tableResult.EligibleRows,
					BlockedRows:   tableResult.BlockedRows,
					DeletedRows:   tableResult.DeletedRows,
					Status:        tableResult.Status,
					Message:       tableResult.Message,
					StartedAt:     startedAt,
					CompletedAt:   formatUsageTime(time.Now().UTC()),
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				result.TableResults = append(result.TableResults, tableResult)
			}
		}
		completedAt := time.Now().UTC()
		job.Status = "completed"
		job.CompletedAt = formatUsageTime(completedAt)
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		result.JobID = job.ID
		result.PolicyVersionID = policy.ID
		result.CompletedAt = completedAt
		return nil
	})
	if err != nil {
		return RetentionStatusResult{}, err
	}
	return result, nil
}

func ensureActiveRetentionPolicy(tx *gorm.DB, cfg RetentionConfig, now time.Time) (retentionPolicyVersionRecord, error) {
	hash := retentionConfigHash(cfg)
	var active retentionPolicyVersionRecord
	err := tx.Where("active = ? AND config_hash = ?", true, hash).First(&active).Error
	if err == nil {
		return active, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return retentionPolicyVersionRecord{}, err
	}
	ts := formatUsageTime(now)
	if err := tx.Model(&retentionPolicyVersionRecord{}).
		Where("active = ?", true).
		Updates(map[string]any{"active": false, "deactivated_at": ts}).Error; err != nil {
		return retentionPolicyVersionRecord{}, err
	}
	version := "retention-config-" + hash[:12]
	var policy retentionPolicyVersionRecord
	err = tx.Where("version = ?", version).First(&policy).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		policy = retentionPolicyVersionRecord{
			Version:          version,
			ConfigHash:       hash,
			Source:           "config",
			Active:           true,
			DryRun:           cfg.DryRun != nil && *cfg.DryRun,
			DefaultBatchSize: cfg.DefaultBatchSize,
			CreatedAt:        ts,
			ActivatedAt:      ts,
			Notes:            "retention foundation dry-run policy",
		}
		if err := tx.Create(&policy).Error; err != nil {
			return retentionPolicyVersionRecord{}, err
		}
		for i, class := range cfg.Classes {
			rule := retentionPolicyRuleRecord{
				PolicyVersionID:        policy.ID,
				RuleOrder:              i + 1,
				DataClass:              class.DataClass,
				Enabled:                retentionClassEnabled(class),
				RetentionDays:          class.RetentionDays,
				BatchSize:              class.BatchSize,
				RequireFinalizedRollup: class.RequireFinalizedRollup,
				CreatedAt:              ts,
			}
			if err := tx.Create(&rule).Error; err != nil {
				return retentionPolicyVersionRecord{}, err
			}
		}
	case err != nil:
		return retentionPolicyVersionRecord{}, err
	default:
		policy.Active = true
		policy.ActivatedAt = ts
		policy.DeactivatedAt = ""
		if err := tx.Save(&policy).Error; err != nil {
			return retentionPolicyVersionRecord{}, err
		}
	}
	return policy, nil
}

func retentionConfigHash(cfg RetentionConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "enabled=%t\n", cfg.Enabled)
	fmt.Fprintf(&b, "dry_run=%t\n", cfg.DryRun != nil && *cfg.DryRun)
	fmt.Fprintf(&b, "default_batch_size=%d\n", cfg.DefaultBatchSize)
	for i, class := range cfg.Classes {
		fmt.Fprintf(&b, "class.%03d=%s,%t,%d,%d,%t\n", i, class.DataClass, retentionClassEnabled(class), class.RetentionDays, class.BatchSize, class.RequireFinalizedRollup)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:])
}

func retentionClassEnabled(class RetentionClassConfig) bool {
	return class.Enabled == nil || *class.Enabled
}

type retentionTableSpec struct {
	DataClass         string
	TableName         string
	TSColumn          string
	UseRequestUsageTS bool
}

func retentionTablesForClass(dataClass string) []retentionTableSpec {
	switch normalizeRetentionDataClass(dataClass) {
	case retentionDataClassUsageDiagnostics:
		return []retentionTableSpec{
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_attempts", TSColumn: "ts"},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_trace_events", TSColumn: "ts"},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_traffic_shape_events", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_upstream_shape_events", TSColumn: "ts"},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_shapes", TSColumn: "ts"},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_translation_shapes", TSColumn: "ts"},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_translation_field_events", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_errors", TSColumn: "ts"},
			{DataClass: retentionDataClassUsageDiagnostics, TableName: "request_upstream_error_details", TSColumn: "ts"},
		}
	case retentionDataClassDecisionTelemetry:
		return []retentionTableSpec{
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_decision_shape_features", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_target_candidates", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_target_filter_reasons", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_routing_decisions", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_routing_signals", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_dynamic_score_terms", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_policy_executions", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_fallback_transitions", TSColumn: "ts", UseRequestUsageTS: true},
			{DataClass: retentionDataClassDecisionTelemetry, TableName: "request_cache_reasons", TSColumn: "ts", UseRequestUsageTS: true},
		}
	case retentionDataClassSecurityAccess:
		return []retentionTableSpec{{DataClass: retentionDataClassSecurityAccess, TableName: "security_access_events", TSColumn: "ts"}}
	case retentionDataClassContentCapture:
		return []retentionTableSpec{{DataClass: retentionDataClassContentCapture, TableName: "request_content_captures", TSColumn: "ts"}}
	case retentionDataClassUsageDetail:
		return []retentionTableSpec{{DataClass: retentionDataClassUsageDetail, TableName: "request_usage", TSColumn: "ts"}}
	default:
		return nil
	}
}

func runRetentionTable(tx *gorm.DB, class RetentionClassConfig, table retentionTableSpec, cutoff time.Time, dryRun bool) (RetentionTableStatus, error) {
	cutoffText := formatUsageTime(cutoff)
	candidateRows, err := countRowsBefore(tx, table, cutoffText)
	if err != nil {
		return RetentionTableStatus{}, err
	}
	heldRows, err := countHeldRowsBefore(tx, table, cutoffText)
	if err != nil {
		return RetentionTableStatus{}, err
	}
	eligibleRows := candidateRows - heldRows
	if eligibleRows < 0 {
		eligibleRows = 0
	}
	status := "dry_run"
	message := "dry run only; no rows deleted"
	blockedRows := int64(0)
	deletedRows := int64(0)
	if table.DataClass == retentionDataClassUsageDetail && class.RequireFinalizedRollup {
		ready, err := usageDetailFinalizedRollupReady(tx, cutoffText)
		if err != nil {
			return RetentionTableStatus{}, err
		}
		if !ready {
			status = "blocked_rollup_required"
			message = "usage_detail delete requires a finalized daily rollup covering the candidate window"
			blockedRows = eligibleRows
			eligibleRows = 0
		}
	}
	if !dryRun && status == "dry_run" {
		if !retentionPurgeSupported(table.DataClass) {
			status = "blocked_not_implemented"
			message = "delete execution is not implemented for this data class"
			blockedRows = eligibleRows
			eligibleRows = 0
		} else {
			deletedRows, err = deleteRetentionBatch(tx, table, cutoffText, class.BatchSize)
			if err != nil {
				return RetentionTableStatus{}, err
			}
			status = "purged"
			message = "deleted one eligible retention batch"
			if deletedRows == 0 {
				status = "no_eligible_rows"
				message = "no eligible rows deleted"
			}
			eligibleRows -= deletedRows
			if eligibleRows < 0 {
				eligibleRows = 0
			}
		}
	}
	return RetentionTableStatus{
		DataClass:     table.DataClass,
		TableName:     table.TableName,
		Cutoff:        cutoff,
		RetentionDays: class.RetentionDays,
		BatchSize:     class.BatchSize,
		CandidateRows: candidateRows,
		HeldRows:      heldRows,
		EligibleRows:  eligibleRows,
		BlockedRows:   blockedRows,
		DeletedRows:   deletedRows,
		Status:        status,
		Message:       message,
	}, nil
}

func retentionPurgeSupported(dataClass string) bool {
	switch normalizeRetentionDataClass(dataClass) {
	case retentionDataClassUsageDiagnostics, retentionDataClassContentCapture, retentionDataClassUsageDetail:
		return true
	default:
		return false
	}
}

func countRowsBefore(tx *gorm.DB, table retentionTableSpec, cutoff string) (int64, error) {
	var count int64
	if table.UseRequestUsageTS {
		err := tx.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s r JOIN request_usage u ON u.request_id = r.request_id WHERE u.ts < ?", table.TableName), cutoff).Scan(&count).Error
		return count, err
	}
	err := tx.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s < ?", table.TableName, table.TSColumn), cutoff).Scan(&count).Error
	return count, err
}

func countHeldRowsBefore(tx *gorm.DB, table retentionTableSpec, cutoff string) (int64, error) {
	var count int64
	if table.UseRequestUsageTS {
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s r
			JOIN request_usage u ON u.request_id = r.request_id
			WHERE u.ts < ?
			AND EXISTS (
				SELECT 1 FROM legal_holds h
				WHERE h.active = ?
				AND h.data_class = ?
				AND (h.request_id = '' OR h.request_id = r.request_id)
				AND (h.start_ts = '' OR u.ts >= h.start_ts)
				AND (h.end_ts = '' OR u.ts < h.end_ts)
			)`, table.TableName)
		err := tx.Raw(query, cutoff, true, table.DataClass).Scan(&count).Error
		return count, err
	}
	if table.TableName == "request_usage" {
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s r
			WHERE r.%s < ?
			AND (
				EXISTS (
					SELECT 1 FROM legal_holds h
					WHERE h.active = ?
					AND h.data_class = ?
					AND (h.request_id = '' OR h.request_id = r.request_id)
					AND (h.start_ts = '' OR r.%s >= h.start_ts)
					AND (h.end_ts = '' OR r.%s < h.end_ts)
				)
				OR EXISTS (
					SELECT 1 FROM legal_holds h
					WHERE h.active = ?
					AND h.request_id = r.request_id
					AND (h.start_ts = '' OR r.%s >= h.start_ts)
					AND (h.end_ts = '' OR r.%s < h.end_ts)
				)
			)`, table.TableName, table.TSColumn, table.TSColumn, table.TSColumn, table.TSColumn, table.TSColumn)
		err := tx.Raw(query, cutoff, true, table.DataClass, true).Scan(&count).Error
		return count, err
	}
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s r
		WHERE r.%s < ?
		AND EXISTS (
			SELECT 1 FROM legal_holds h
			WHERE h.active = ?
			AND h.data_class = ?
			AND (h.request_id = '' OR h.request_id = r.request_id)
			AND (h.start_ts = '' OR r.%s >= h.start_ts)
			AND (h.end_ts = '' OR r.%s < h.end_ts)
		)`, table.TableName, table.TSColumn, table.TSColumn, table.TSColumn)
	err := tx.Raw(query, cutoff, true, table.DataClass).Scan(&count).Error
	return count, err
}

type retentionDeleteKey struct {
	ID           uint
	RequestID    string
	Seq          int
	AttemptIndex int
}

func deleteRetentionBatch(tx *gorm.DB, table retentionTableSpec, cutoff string, batchSize int) (int64, error) {
	keys, err := selectRetentionDeleteKeys(tx, table, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	var deleted int64
	for _, key := range keys {
		var res *gorm.DB
		switch table.TableName {
		case "request_attempts":
			res = tx.Where("request_id = ? AND attempt_index = ?", key.RequestID, key.AttemptIndex).Delete(&requestAttemptRecord{})
		case "request_trace_events":
			res = tx.Where("request_id = ? AND seq = ?", key.RequestID, key.Seq).Delete(&requestTraceEventRecord{})
		case "request_traffic_shape_events":
			res = tx.Where("request_id = ? AND seq = ?", key.RequestID, key.Seq).Delete(&requestTrafficShapeEventRecord{})
		case "request_upstream_shape_events":
			res = tx.Where("request_id = ? AND seq = ?", key.RequestID, key.Seq).Delete(&requestUpstreamShapeEventRecord{})
		case "request_shapes":
			res = tx.Where("request_id = ?", key.RequestID).Delete(&requestShapeRecord{})
		case "request_translation_shapes":
			res = tx.Where("request_id = ? AND attempt_index = ?", key.RequestID, key.AttemptIndex).Delete(&requestTranslationShapeRecord{})
		case "request_translation_field_events":
			res = tx.Where("request_id = ? AND attempt_index = ? AND seq = ?", key.RequestID, key.AttemptIndex, key.Seq).Delete(&requestTranslationFieldEventRecord{})
		case "request_errors":
			res = tx.Where("request_id = ?", key.RequestID).Delete(&requestErrorRecord{})
		case "request_upstream_error_details":
			res = tx.Where("request_id = ? AND attempt_index = ? AND seq = ?", key.RequestID, key.AttemptIndex, key.Seq).Delete(&requestUpstreamErrorDetailRecord{})
		case "request_content_captures":
			if err := tx.Where("capture_id = ?", key.ID).Delete(&contentCaptureHeaderRecord{}).Error; err != nil {
				return deleted, err
			}
			res = tx.Where("id = ?", key.ID).Delete(&contentCaptureRecord{})
		case "request_usage":
			res = tx.Where("request_id = ?", key.RequestID).Delete(&usageRecord{})
		default:
			return deleted, fmt.Errorf("retention purge is not implemented for table %s", table.TableName)
		}
		if res.Error != nil {
			return deleted, res.Error
		}
		deleted += res.RowsAffected
	}
	return deleted, nil
}

func selectRetentionDeleteKeys(tx *gorm.DB, table retentionTableSpec, cutoff string, batchSize int) ([]retentionDeleteKey, error) {
	if batchSize <= 0 {
		return nil, errors.New("retention batch size must be positive")
	}
	baseHoldClause := `AND NOT EXISTS (
		SELECT 1 FROM legal_holds h
		WHERE h.active = ?
		AND h.data_class = ?
		AND (h.request_id = '' OR h.request_id = r.request_id)
		AND (h.start_ts = '' OR r.` + table.TSColumn + ` >= h.start_ts)
		AND (h.end_ts = '' OR r.` + table.TSColumn + ` < h.end_ts)
	)`
	requestScopedHoldClause := `AND NOT EXISTS (
		SELECT 1 FROM legal_holds h
		WHERE h.active = ?
		AND h.request_id = r.request_id
		AND (h.start_ts = '' OR r.` + table.TSColumn + ` >= h.start_ts)
		AND (h.end_ts = '' OR r.` + table.TSColumn + ` < h.end_ts)
	)`
	var query string
	args := []any{cutoff, true, table.DataClass, batchSize}
	switch table.TableName {
	case "request_attempts":
		query = `SELECT r.request_id AS request_id, r.attempt_index AS attempt_index
			FROM request_attempts r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC, r.attempt_index ASC
			LIMIT ?`
	case "request_trace_events":
		query = `SELECT r.request_id AS request_id, r.seq AS seq
			FROM request_trace_events r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC, r.seq ASC
			LIMIT ?`
	case "request_traffic_shape_events":
		query = `SELECT r.request_id AS request_id, r.seq AS seq
			FROM request_traffic_shape_events r
			JOIN request_usage u ON u.request_id = r.request_id
			WHERE u.ts < ? ` + strings.ReplaceAll(baseHoldClause, "r.ts", "u.ts") + `
			ORDER BY u.ts ASC, r.request_id ASC, r.seq ASC
			LIMIT ?`
	case "request_upstream_shape_events":
		query = `SELECT r.request_id AS request_id, r.seq AS seq
			FROM request_upstream_shape_events r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC, r.seq ASC
			LIMIT ?`
	case "request_shapes":
		query = `SELECT r.request_id AS request_id
			FROM request_shapes r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC
			LIMIT ?`
	case "request_translation_shapes":
		query = `SELECT r.request_id AS request_id, r.attempt_index AS attempt_index
			FROM request_translation_shapes r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC, r.attempt_index ASC
			LIMIT ?`
	case "request_translation_field_events":
		query = `SELECT r.request_id AS request_id, r.attempt_index AS attempt_index, r.seq AS seq
			FROM request_translation_field_events r
			JOIN request_usage u ON u.request_id = r.request_id
			WHERE u.ts < ? ` + strings.ReplaceAll(baseHoldClause, "r.ts", "u.ts") + `
			ORDER BY u.ts ASC, r.request_id ASC, r.attempt_index ASC, r.seq ASC
			LIMIT ?`
	case "request_errors":
		query = `SELECT r.request_id AS request_id
			FROM request_errors r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC
			LIMIT ?`
	case "request_upstream_error_details":
		query = `SELECT r.request_id AS request_id, r.attempt_index AS attempt_index, r.seq AS seq
			FROM request_upstream_error_details r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC, r.attempt_index ASC, r.seq ASC
			LIMIT ?`
	case "request_content_captures":
		query = `SELECT r.id AS id, r.request_id AS request_id
			FROM request_content_captures r
			WHERE r.ts < ? ` + baseHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC, r.id ASC
			LIMIT ?`
	case "request_usage":
		query = `SELECT r.request_id AS request_id
			FROM request_usage r
			WHERE r.ts < ? ` + baseHoldClause + requestScopedHoldClause + `
			ORDER BY r.ts ASC, r.request_id ASC
			LIMIT ?`
		args = []any{cutoff, true, table.DataClass, true, batchSize}
	default:
		return nil, fmt.Errorf("retention purge is not implemented for table %s", table.TableName)
	}
	var keys []retentionDeleteKey
	if err := tx.Raw(query, args...).Scan(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func usageDetailFinalizedRollupReady(tx *gorm.DB, cutoff string) (bool, error) {
	var candidate struct {
		MinTS string
		Count int64
	}
	if err := tx.Raw("SELECT MIN(ts) AS min_ts, COUNT(*) AS count FROM request_usage WHERE ts < ?", cutoff).Scan(&candidate).Error; err != nil {
		return false, err
	}
	if candidate.Count == 0 {
		return true, nil
	}
	var runs []usageRollupRunRecord
	if err := tx.Model(&usageRollupRunRecord{}).
		Where("rollup_type = ? AND status = ? AND window_end > ? AND window_start < ?", "daily", "finalized", candidate.MinTS, cutoff).
		Order("window_start ASC, window_end ASC").
		Find(&runs).Error; err != nil {
		return false, err
	}
	cursor := candidate.MinTS
	for _, run := range runs {
		if run.WindowStart > cursor {
			return false, nil
		}
		if run.WindowEnd > cursor {
			cursor = run.WindowEnd
		}
		if cursor >= cutoff {
			return true, nil
		}
	}
	return false, nil
}

func CreateLegalHold(opts LegalHoldCreateOptions) (LegalHoldResult, error) {
	store, err := openLegalHoldUsageStore(opts.Driver, opts.DBPath, opts.DSN)
	if err != nil {
		return LegalHoldResult{}, err
	}
	defer store.Close()
	return store.CreateLegalHold(opts)
}

func ReleaseLegalHold(opts LegalHoldReleaseOptions) (LegalHoldResult, error) {
	store, err := openLegalHoldUsageStore(opts.Driver, opts.DBPath, opts.DSN)
	if err != nil {
		return LegalHoldResult{}, err
	}
	defer store.Close()
	return store.ReleaseLegalHold(opts)
}

func openLegalHoldUsageStore(driver, dbPath, dsn string) (*usageStore, error) {
	driver = strings.ToLower(defaultString(driver, "sqlite"))
	if driver == "sqlite" && dbPath == "" {
		return nil, errors.New("usage db path is required")
	}
	if (driver == "postgres" || driver == "postgresql") && dsn == "" {
		return nil, errors.New("usage db dsn is required")
	}
	return OpenUsageStore(UsageDBConfig{Driver: driver, Path: dbPath, DSN: dsn})
}

func (s *usageStore) CreateLegalHold(opts LegalHoldCreateOptions) (LegalHoldResult, error) {
	if s == nil || s.db == nil {
		return LegalHoldResult{}, errors.New("usage store is not open")
	}
	dataClass := normalizeRetentionDataClass(opts.DataClass)
	if !knownRetentionDataClass(dataClass) {
		return LegalHoldResult{}, fmt.Errorf("legal hold has unknown data_class %q", opts.DataClass)
	}
	start, end, err := normalizeLegalHoldRange(opts.Start, opts.End)
	if err != nil {
		return LegalHoldResult{}, err
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	holdID := strings.TrimSpace(opts.HoldID)
	if holdID == "" {
		holdID = fmt.Sprintf("hold-%d", now.UnixNano())
	}
	record := legalHoldRecord{
		HoldID:     holdID,
		DataClass:  dataClass,
		RequestID:  strings.TrimSpace(opts.RequestID),
		Active:     true,
		StartTS:    formatOptionalUsageTime(start),
		EndTS:      formatOptionalUsageTime(end),
		ReasonCode: strings.TrimSpace(opts.ReasonCode),
		Subject:    strings.TrimSpace(opts.Subject),
		CreatedBy:  strings.TrimSpace(opts.Actor),
		CreatedAt:  formatUsageTime(now),
		Notes:      strings.TrimSpace(opts.Notes),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		return createLegalHoldAuditEvent(tx, legalHoldAuditEventRecord{
			HoldID:         record.HoldID,
			EventType:      "create",
			DataClass:      record.DataClass,
			RequestID:      record.RequestID,
			StartTS:        record.StartTS,
			EndTS:          record.EndTS,
			PreviousActive: false,
			NewActive:      true,
			Actor:          record.CreatedBy,
			ReasonCode:     record.ReasonCode,
			Message:        "legal hold created",
			TS:             formatUsageTime(now),
		})
	})
	if err != nil {
		return LegalHoldResult{}, err
	}
	return legalHoldResultFromRecord(record), nil
}

func (s *usageStore) ReleaseLegalHold(opts LegalHoldReleaseOptions) (LegalHoldResult, error) {
	if s == nil || s.db == nil {
		return LegalHoldResult{}, errors.New("usage store is not open")
	}
	holdID := strings.TrimSpace(opts.HoldID)
	if holdID == "" {
		return LegalHoldResult{}, errors.New("legal hold release requires hold_id")
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var record legalHoldRecord
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("hold_id = ?", holdID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("legal hold %q not found", holdID)
			}
			return err
		}
		previousActive := record.Active
		record.Active = false
		record.ReleasedBy = strings.TrimSpace(opts.Actor)
		record.ReleasedAt = formatUsageTime(now)
		record.ReleaseReason = strings.TrimSpace(opts.ReleaseReason)
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		return createLegalHoldAuditEvent(tx, legalHoldAuditEventRecord{
			HoldID:         record.HoldID,
			EventType:      "release",
			DataClass:      record.DataClass,
			RequestID:      record.RequestID,
			StartTS:        record.StartTS,
			EndTS:          record.EndTS,
			PreviousActive: previousActive,
			NewActive:      false,
			Actor:          record.ReleasedBy,
			ReasonCode:     record.ReleaseReason,
			Message:        "legal hold released",
			TS:             formatUsageTime(now),
		})
	})
	if err != nil {
		return LegalHoldResult{}, err
	}
	return legalHoldResultFromRecord(record), nil
}

func normalizeLegalHoldRange(start, end time.Time) (time.Time, time.Time, error) {
	if !start.IsZero() {
		start = start.UTC()
	}
	if !end.IsZero() {
		end = end.UTC()
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("legal hold start must be before end")
	}
	return start, end, nil
}

func formatOptionalUsageTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatUsageTime(t.UTC())
}

func createLegalHoldAuditEvent(tx *gorm.DB, event legalHoldAuditEventRecord) error {
	return tx.Create(&event).Error
}

func legalHoldResultFromRecord(record legalHoldRecord) LegalHoldResult {
	return LegalHoldResult{
		HoldID:     record.HoldID,
		DataClass:  record.DataClass,
		RequestID:  record.RequestID,
		Active:     record.Active,
		Start:      parseOptionalUsageTime(record.StartTS),
		End:        parseOptionalUsageTime(record.EndTS),
		CreatedAt:  parseOptionalUsageTime(record.CreatedAt),
		ReleasedAt: parseOptionalUsageTime(record.ReleasedAt),
	}
}

func parseOptionalUsageTime(v string) time.Time {
	if strings.TrimSpace(v) == "" {
		return time.Time{}
	}
	t, err := parseUsageTime(v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func GenerateUsageRollup(opts UsageRollupOptions) (UsageRollupResult, error) {
	driver := strings.ToLower(defaultString(opts.Driver, "sqlite"))
	if driver == "sqlite" && opts.DBPath == "" {
		return UsageRollupResult{}, errors.New("usage db path is required")
	}
	if (driver == "postgres" || driver == "postgresql") && opts.DSN == "" {
		return UsageRollupResult{}, errors.New("usage db dsn is required")
	}
	if opts.To.IsZero() || opts.From.IsZero() {
		return UsageRollupResult{}, errors.New("from and to are required")
	}
	opts.RollupType = normalizeUsageRollupType(opts.RollupType)
	if opts.RollupType == "" {
		return UsageRollupResult{}, errors.New("rollup type must be hourly, daily, or monthly")
	}
	if err := validateUsageRollupBaselineOptions(opts); err != nil {
		return UsageRollupResult{}, err
	}
	opts.From = opts.From.UTC()
	opts.To = opts.To.UTC()
	if !opts.From.Before(opts.To) {
		return UsageRollupResult{}, errors.New("from must be before to")
	}
	store, err := OpenUsageStore(UsageDBConfig{Driver: driver, Path: opts.DBPath, DSN: opts.DSN})
	if err != nil {
		return UsageRollupResult{}, err
	}
	defer store.Close()
	return store.generateUsageRollup(opts)
}

func (s *usageStore) generateUsageRollup(opts UsageRollupOptions) (UsageRollupResult, error) {
	if s == nil || s.db == nil {
		return UsageRollupResult{}, errors.New("usage store is not open")
	}
	opts.From = opts.From.UTC()
	opts.To = opts.To.UTC()
	opts.RollupType = normalizeUsageRollupType(opts.RollupType)
	if opts.RollupType == "" {
		return UsageRollupResult{}, errors.New("rollup type must be hourly, daily, or monthly")
	}
	if err := validateUsageRollupBaselineOptions(opts); err != nil {
		return UsageRollupResult{}, err
	}
	if !opts.From.Before(opts.To) {
		return UsageRollupResult{}, errors.New("from must be before to")
	}
	rows, err := s.rows(UsageReportOptions{From: opts.From, To: opts.To})
	if err != nil {
		return UsageRollupResult{}, err
	}
	status := "draft"
	if opts.Finalize {
		status = "finalized"
	}
	now := formatUsageTime(time.Now().UTC())
	windowStart := formatUsageTime(opts.From)
	windowEnd := formatUsageTime(opts.To)
	source := usageRollupSourceSummary(rows)
	baseline := usageRollupBaselineFromOptions(opts)
	daily := []usageRollupDailyRecord(nil)
	aggregates := []usageRollupAggregateRecord(nil)
	switch opts.RollupType {
	case "daily":
		daily = dailyRollupRecords(rows, status, windowStart, windowEnd, baseline)
	default:
		aggregateDailyRecords := periodRollupRecords(rows, opts.RollupType, status, windowStart, windowEnd, baseline)
		aggregates = aggregateRollupRecordsFromDaily(aggregateDailyRecords, opts.RollupType)
	}
	decisionBuckets := decisionBucketRollupRecords(rows, opts.RollupType, status, windowStart, windowEnd)
	rollupRows := int64(len(daily) + len(aggregates))
	result := UsageRollupResult{
		RollupType:         opts.RollupType,
		Status:             status,
		WindowStart:        opts.From,
		WindowEnd:          opts.To,
		SourceRequestCount: int64(len(rows)),
		RollupRows:         rollupRows,
		DailyRows:          int64(len(daily)),
		DecisionBucketRows: int64(len(decisionBuckets)),
		SourceChecksum:     source.Checksum,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var overlappingFinalized int64
		if err := tx.Model(&usageRollupRunRecord{}).
			Where("rollup_type = ? AND status = ? AND window_start < ? AND window_end > ?", opts.RollupType, "finalized", windowEnd, windowStart).
			Count(&overlappingFinalized).Error; err != nil {
			return err
		}
		if overlappingFinalized > 0 {
			return errors.New("usage rollup window overlaps a finalized window")
		}
		var run usageRollupRunRecord
		err := tx.Where("rollup_type = ? AND window_start = ? AND window_end = ? AND status = ?", opts.RollupType, windowStart, windowEnd, "draft").First(&run).Error
		auditAction := "created"
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			run = usageRollupRunRecord{
				RollupType:  opts.RollupType,
				Status:      status,
				WindowStart: windowStart,
				WindowEnd:   windowEnd,
				SourceTable: "request_usage",
				StartedAt:   now,
			}
			if err := tx.Create(&run).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			auditAction = "draft_regenerated"
			if err := tx.Where("run_id = ?", run.ID).Delete(&usageRollupDailyRecord{}).Error; err != nil {
				return err
			}
			if table := usageRollupAggregateTable(opts.RollupType); table != "" {
				if err := tx.Table(table).Where("run_id = ?", run.ID).Delete(&usageRollupAggregateRecord{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("run_id = ?", run.ID).Delete(&usageRollupDecisionBucketRecord{}).Error; err != nil {
				return err
			}
		}
		run.Status = status
		run.SourceTable = "request_usage"
		run.SourceRequestCount = int64(len(rows))
		run.SourceMinTS = source.MinTS
		run.SourceMaxTS = source.MaxTS
		run.SourceChecksum = source.Checksum
		run.DailyRowCount = int64(len(daily))
		run.RollupRowCount = rollupRows
		run.DecisionBucketRowCount = int64(len(decisionBuckets))
		info := buildinfo.Current()
		run.RouterVersion = info.Version
		run.RouterCommit = info.Commit
		run.CompletedAt = now
		run.GeneratedAt = now
		if opts.Finalize {
			run.FinalizedAt = now
		}
		if err := tx.Save(&run).Error; err != nil {
			return err
		}
		for i := range daily {
			daily[i].RunID = run.ID
			if err := tx.Create(&daily[i]).Error; err != nil {
				return err
			}
		}
		if len(aggregates) > 0 {
			table := usageRollupAggregateTable(opts.RollupType)
			for i := range aggregates {
				aggregates[i].RunID = run.ID
				if err := tx.Table(table).Create(&aggregates[i]).Error; err != nil {
					return err
				}
			}
		}
		for i := range decisionBuckets {
			decisionBuckets[i].RunID = run.ID
			if err := tx.Create(&decisionBuckets[i]).Error; err != nil {
				return err
			}
		}
		if err := createUsageRollupAuditEvent(tx, run.ID, auditAction, now, usageRollupAuditSummary(run, auditAction)); err != nil {
			return err
		}
		if opts.Finalize {
			if err := createUsageRollupAuditEvent(tx, run.ID, "finalized", now, usageRollupAuditSummary(run, "finalized")); err != nil {
				return err
			}
		}
		result.RunID = run.ID
		return nil
	})
	if err != nil {
		return UsageRollupResult{}, err
	}
	return result, nil
}

type usageRollupSource struct {
	MinTS    string
	MaxTS    string
	Checksum string
}

type usageRollupBaseline struct {
	ID                       string
	Name                     string
	Version                  string
	InputPricePerMillionUSD  float64
	OutputPricePerMillionUSD float64
}

func normalizeUsageRollupType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "daily", "day":
		return "daily"
	case "hourly", "hour":
		return "hourly"
	case "monthly", "month", "billing_month", "monthly_billing":
		return "monthly"
	default:
		return ""
	}
}

func usageRollupAggregateTable(rollupType string) string {
	switch rollupType {
	case "hourly":
		return "usage_rollup_hourly"
	case "monthly":
		return "usage_rollup_monthly_billing"
	default:
		return ""
	}
}

func usageRollupBaselineFromOptions(opts UsageRollupOptions) usageRollupBaseline {
	id := strings.TrimSpace(opts.BaselineID)
	if id == "" {
		return usageRollupBaseline{}
	}
	name := strings.TrimSpace(opts.BaselineName)
	if name == "" {
		name = id
	}
	return usageRollupBaseline{
		ID:                       id,
		Name:                     name,
		Version:                  strings.TrimSpace(opts.BaselineVersion),
		InputPricePerMillionUSD:  opts.BaselineInputPricePerMillionUSD,
		OutputPricePerMillionUSD: opts.BaselineOutputPricePerMillionUSD,
	}
}

func validateUsageRollupBaselineOptions(opts UsageRollupOptions) error {
	if strings.TrimSpace(opts.BaselineID) == "" {
		return nil
	}
	if opts.BaselineInputPricePerMillionUSD < 0 || opts.BaselineOutputPricePerMillionUSD < 0 {
		return errors.New("rollup baseline prices must be nonnegative")
	}
	return nil
}

func usageRollupSourceSummary(rows []usageRow) usageRollupSource {
	if len(rows) == 0 {
		sum := sha256.Sum256([]byte("empty\n"))
		return usageRollupSource{Checksum: fmt.Sprintf("%x", sum[:])}
	}
	sorted := append([]usageRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TS.Equal(sorted[j].TS) {
			return sorted[i].TS.Before(sorted[j].TS)
		}
		return sorted[i].RequestID < sorted[j].RequestID
	})
	minTS := formatUsageTime(sorted[0].TS)
	maxTS := formatUsageTime(sorted[len(sorted)-1].TS)
	var b strings.Builder
	for _, row := range sorted {
		total := row.TotalTokens
		if total == 0 {
			total = row.InputTokens + row.OutputTokens
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%t|%s|%d|%d|%d|%d|%d|%t|%d|%d|%.12f|%.12f|%.12f|%.12f|%.12f|%.12f|%.12f|%s|%s|%s|%s|%s\n",
			formatUsageTime(row.TS), row.RequestID, row.CallerID, row.CallerUser, row.CallerProject, row.CallerEnvironment,
			row.TokenID, row.Client, row.InboundDialect, row.RequestedModel, row.ResolvedGroup, row.Strategy,
			row.Stream, row.Cache, row.Status, row.Attempts, row.InputTokens, row.OutputTokens, total,
			row.InputHasImage, row.InputImageCount, row.InputImageTokens, row.InputCostUSD, row.ImageCostUSD,
			row.OutputCostUSD, row.TotalCostUSD, row.UpstreamReportedInputCostUSD, row.UpstreamReportedOutputCostUSD,
			row.UpstreamReportedTotalCostUSD, row.TargetProvider, row.TargetModel, row.TargetDialect,
			row.ContractBucket, row.TargetValidationStatus)
		fmt.Fprintf(&b, "derived|fallback=%t|latency_ms=%d|ttfb_ms=%s|upstream_ms=%s|downstream_ms=%s|upstream_output_tps=%s|upstream_total_tps=%s|downstream_output_tps=%s|downstream_total_tps=%s|cache_enabled=%t|cache_items=%d|cache_bytes=%d|cache_max_bytes=%d|cache_occupancy_pct=%.12f|max_token_bucket=%s|input_token_bucket=%s|admission_reason=%s|pii_applied=%t|pii_mode=%s|pii_replacements=%d|pii_rules=%d|contract_present=%t|contract_failure=%s|contract_workload=%s|validation_workload=%s|validation_age=%s|quota_state=%s|key_state=%s|error=%s|pricing_source=%s|pricing_updated_at=%s\n",
			row.FallbackUsed, row.LatencyMS, checksumInt64Ptr(row.TTFBMS), checksumInt64Ptr(row.UpstreamMS), checksumInt64Ptr(row.DownstreamMS),
			checksumFloat64Ptr(row.UpstreamOutputTPS), checksumFloat64Ptr(row.UpstreamTotalTPS), checksumFloat64Ptr(row.DownstreamOutputTPS), checksumFloat64Ptr(row.DownstreamTotalTPS),
			row.CacheEnabled, row.CacheItems, row.CacheBytes, row.CacheMaxBytes, row.CacheOccupancyPct,
			row.MaxTokenBucket, row.InputTokenBucket, row.AdmissionReason,
			row.PIIFilterApplied, row.PIIFilterMode, row.PIIFilterReplacements, row.PIIFilterRuleCount,
			row.ContractPresent, row.ContractFailureReason, row.ContractWorkload, row.TargetValidationWorkload, row.TargetValidationAgeBucket,
			row.QuotaState, row.KeyState,
			row.Error, row.PricingSource, row.PricingUpdatedAt)
		writeSortedStrings(&b, "signals", row.EnabledSignals)
		writeSortedStrings(&b, "scores", row.ScoreBuckets)
		writeSortedStrings(&b, "thresholds", row.ThresholdBuckets)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return usageRollupSource{MinTS: minTS, MaxTS: maxTS, Checksum: fmt.Sprintf("%x", sum[:])}
}

func checksumInt64Ptr(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

func checksumFloat64Ptr(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', 12, 64)
}

func writeSortedStrings(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(b, "%s:\n", label)
		return
	}
	values = append([]string(nil), values...)
	sort.Strings(values)
	fmt.Fprintf(b, "%s:%s\n", label, strings.Join(values, ","))
}

func createUsageRollupAuditEvent(tx *gorm.DB, runID uint, action, ts, summary string) error {
	return tx.Create(&usageRollupAuditEventRecord{
		RunID:        runID,
		Action:       action,
		ActorSubject: "router-usage-report",
		TS:           ts,
		SafeSummary:  sanitizePersistedDiagnosticText(summary),
	}).Error
}

func usageRollupAuditSummary(run usageRollupRunRecord, action string) string {
	return fmt.Sprintf("%s %s rollup window=%s..%s source_requests=%d rollup_rows=%d decision_bucket_rows=%d checksum=%s",
		action, run.RollupType, run.WindowStart, run.WindowEnd, run.SourceRequestCount, run.RollupRowCount, run.DecisionBucketRowCount, run.SourceChecksum)
}

func decisionBucketRollupRecords(rows []usageRow, rollupType, status, windowStart, windowEnd string) []usageRollupDecisionBucketRecord {
	byDimension := map[string]*usageRollupDecisionBucketRecord{}
	for _, row := range rows {
		bucket := usageRollupBucketUTC(row.TS, rollupType)
		addDecisionRollupBucket(byDimension, row, rollupType, status, windowStart, windowEnd, bucket, "max_token_bucket", defaultString(row.MaxTokenBucket, "unknown"), "")
		addDecisionRollupBucket(byDimension, row, rollupType, status, windowStart, windowEnd, bucket, "input_token_bucket", defaultString(row.InputTokenBucket, "unknown"), "")
		addDecisionRollupBucket(byDimension, row, rollupType, status, windowStart, windowEnd, bucket, "admission_reason", defaultString(row.AdmissionReason, "admitted"), "")
		for _, signal := range row.EnabledSignals {
			addDecisionRollupBucket(byDimension, row, rollupType, status, windowStart, windowEnd, bucket, "enabled_signal", signal, "")
		}
		for _, bucket := range row.ScoreBuckets {
			parts := splitScoreBucketReportKey(bucket)
			addDecisionRollupBucket(byDimension, row, rollupType, status, windowStart, windowEnd, usageRollupBucketUTC(row.TS, rollupType), "score_bucket", parts[0], parts[1])
		}
		for _, bucket := range row.ThresholdBuckets {
			addDecisionRollupBucket(byDimension, row, rollupType, status, windowStart, windowEnd, usageRollupBucketUTC(row.TS, rollupType), "threshold_bucket", bucket, "")
		}
	}
	keys := make([]string, 0, len(byDimension))
	for key := range byDimension {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]usageRollupDecisionBucketRecord, 0, len(keys))
	for _, key := range keys {
		out = append(out, *byDimension[key])
	}
	return out
}

func addDecisionRollupBucket(m map[string]*usageRollupDecisionBucketRecord, row usageRow, rollupType, status, windowStart, windowEnd, bucket, kind, name, secondary string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	key := strings.Join([]string{
		bucket,
		defaultString(row.ResolvedGroup, row.RequestedModel),
		defaultString(row.Strategy, "unknown"),
		kind,
		name,
		secondary,
	}, "\x1f")
	rec := m[key]
	if rec == nil {
		rec = &usageRollupDecisionBucketRecord{
			RollupType:      rollupType,
			Status:          status,
			DayUTC:          bucket,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			ResolvedGroup:   defaultString(row.ResolvedGroup, row.RequestedModel),
			Strategy:        defaultString(row.Strategy, "unknown"),
			BucketKind:      kind,
			BucketName:      name,
			SecondaryBucket: secondary,
		}
		m[key] = rec
	}
	rec.SourceRequestCount++
	if row.Status >= 400 {
		rec.ErrorCount++
	}
	rec.InputTokens += int64(row.InputTokens)
	rec.OutputTokens += int64(row.OutputTokens)
	total := row.TotalTokens
	if total == 0 {
		total = row.InputTokens + row.OutputTokens
	}
	rec.TotalTokens += int64(total)
	rec.TotalCostUSD += row.TotalCostUSD
}

func dailyRollupRecords(rows []usageRow, status, windowStart, windowEnd string, baseline usageRollupBaseline) []usageRollupDailyRecord {
	return periodRollupRecords(rows, "daily", status, windowStart, windowEnd, baseline)
}

func periodRollupRecords(rows []usageRow, rollupType, status, windowStart, windowEnd string, baseline usageRollupBaseline) []usageRollupDailyRecord {
	byDimension := map[string]*usageRollupDailyRecord{}
	for _, row := range rows {
		bucket := usageRollupBucketUTC(row.TS, rollupType)
		key := dailyRollupDimensionKey(bucket, row, baseline.ID)
		rec := byDimension[key]
		if rec == nil {
			rec = &usageRollupDailyRecord{
				RollupType:                       rollupType,
				Status:                           status,
				DayUTC:                           bucket,
				WindowStart:                      windowStart,
				WindowEnd:                        windowEnd,
				SourceTable:                      "request_usage",
				CallerID:                         row.CallerID,
				CallerUser:                       row.CallerUser,
				CallerProject:                    row.CallerProject,
				CallerEnvironment:                row.CallerEnvironment,
				TokenID:                          row.TokenID,
				Client:                           row.Client,
				InboundDialect:                   row.InboundDialect,
				RequestedModel:                   row.RequestedModel,
				ResolvedGroup:                    row.ResolvedGroup,
				Strategy:                         row.Strategy,
				TargetProvider:                   row.TargetProvider,
				TargetModel:                      row.TargetModel,
				TargetDialect:                    row.TargetDialect,
				StatusClass:                      usageStatusClass(row.Status),
				Stream:                           row.Stream,
				Cache:                            row.Cache,
				InputHasImage:                    row.InputHasImage,
				PIIFilterApplied:                 row.PIIFilterApplied,
				ContractBucket:                   row.ContractBucket,
				TargetValidationStatus:           row.TargetValidationStatus,
				BaselineID:                       baseline.ID,
				BaselineName:                     baseline.Name,
				BaselineVersion:                  baseline.Version,
				BaselineInputPricePerMillionUSD:  baseline.InputPricePerMillionUSD,
				BaselineOutputPricePerMillionUSD: baseline.OutputPricePerMillionUSD,
			}
			byDimension[key] = rec
		}
		addUsageRowToDailyRollup(rec, row, baseline)
	}
	keys := make([]string, 0, len(byDimension))
	for key := range byDimension {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]usageRollupDailyRecord, 0, len(keys))
	for _, key := range keys {
		out = append(out, *byDimension[key])
	}
	return out
}

func usageRollupBucketUTC(ts time.Time, rollupType string) string {
	ts = ts.UTC()
	switch rollupType {
	case "hourly":
		return ts.Truncate(time.Hour).Format(time.RFC3339)
	case "monthly":
		return time.Date(ts.Year(), ts.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	default:
		return ts.Format("2006-01-02")
	}
}

func usageRollupBucketBounds(bucketUTC, rollupType string) (string, string) {
	switch rollupType {
	case "hourly":
		start, err := time.Parse(time.RFC3339, bucketUTC)
		if err != nil {
			return bucketUTC, bucketUTC
		}
		return formatUsageTime(start), formatUsageTime(start.Add(time.Hour))
	case "monthly":
		start, err := time.Parse("2006-01", bucketUTC)
		if err != nil {
			return bucketUTC, bucketUTC
		}
		return formatUsageTime(start), formatUsageTime(start.AddDate(0, 1, 0))
	default:
		start, err := time.Parse("2006-01-02", bucketUTC)
		if err != nil {
			return bucketUTC, bucketUTC
		}
		return formatUsageTime(start), formatUsageTime(start.AddDate(0, 0, 1))
	}
}

func dailyRollupDimensionKey(day string, row usageRow, baselineID string) string {
	parts := []string{
		day,
		row.CallerID,
		row.CallerUser,
		row.CallerProject,
		row.CallerEnvironment,
		row.TokenID,
		row.Client,
		row.InboundDialect,
		row.RequestedModel,
		row.ResolvedGroup,
		row.Strategy,
		row.TargetProvider,
		row.TargetModel,
		row.TargetDialect,
		usageStatusClass(row.Status),
		strconv.FormatBool(row.Stream),
		row.Cache,
		strconv.FormatBool(row.InputHasImage),
		strconv.FormatBool(row.PIIFilterApplied),
		row.ContractBucket,
		row.TargetValidationStatus,
		baselineID,
	}
	return strings.Join(parts, "\x1f")
}

func usageStatusClass(status int) string {
	if status <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%dxx", status/100)
}

func aggregateRollupRecordsFromDaily(rows []usageRollupDailyRecord, rollupType string) []usageRollupAggregateRecord {
	out := make([]usageRollupAggregateRecord, 0, len(rows))
	for _, row := range rows {
		bucketStart, bucketEnd := usageRollupBucketBounds(row.DayUTC, rollupType)
		out = append(out, usageRollupAggregateRecord{
			RollupType:                        row.RollupType,
			Status:                            row.Status,
			BucketUTC:                         row.DayUTC,
			BucketStart:                       bucketStart,
			BucketEnd:                         bucketEnd,
			WindowStart:                       row.WindowStart,
			WindowEnd:                         row.WindowEnd,
			SourceTable:                       row.SourceTable,
			CallerID:                          row.CallerID,
			CallerUser:                        row.CallerUser,
			CallerProject:                     row.CallerProject,
			CallerEnvironment:                 row.CallerEnvironment,
			TokenID:                           row.TokenID,
			Client:                            row.Client,
			InboundDialect:                    row.InboundDialect,
			RequestedModel:                    row.RequestedModel,
			ResolvedGroup:                     row.ResolvedGroup,
			Strategy:                          row.Strategy,
			TargetProvider:                    row.TargetProvider,
			TargetModel:                       row.TargetModel,
			TargetDialect:                     row.TargetDialect,
			StatusClass:                       row.StatusClass,
			Stream:                            row.Stream,
			Cache:                             row.Cache,
			InputHasImage:                     row.InputHasImage,
			PIIFilterApplied:                  row.PIIFilterApplied,
			ContractBucket:                    row.ContractBucket,
			TargetValidationStatus:            row.TargetValidationStatus,
			SourceRequestCount:                row.SourceRequestCount,
			SuccessCount:                      row.SuccessCount,
			ErrorCount:                        row.ErrorCount,
			StreamCount:                       row.StreamCount,
			CacheHitCount:                     row.CacheHitCount,
			CacheMissCount:                    row.CacheMissCount,
			CacheBypassCount:                  row.CacheBypassCount,
			FallbackCount:                     row.FallbackCount,
			AttemptCount:                      row.AttemptCount,
			InputTokens:                       row.InputTokens,
			OutputTokens:                      row.OutputTokens,
			TotalTokens:                       row.TotalTokens,
			InputImageCount:                   row.InputImageCount,
			InputImageTokens:                  row.InputImageTokens,
			InputCostUSD:                      row.InputCostUSD,
			ImageCostUSD:                      row.ImageCostUSD,
			OutputCostUSD:                     row.OutputCostUSD,
			TotalCostUSD:                      row.TotalCostUSD,
			UpstreamReportedInputCostUSD:      row.UpstreamReportedInputCostUSD,
			UpstreamReportedOutputCostUSD:     row.UpstreamReportedOutputCostUSD,
			UpstreamReportedTotalCostUSD:      row.UpstreamReportedTotalCostUSD,
			BaselineID:                        row.BaselineID,
			BaselineName:                      row.BaselineName,
			BaselineVersion:                   row.BaselineVersion,
			BaselineInputPricePerMillionUSD:   row.BaselineInputPricePerMillionUSD,
			BaselineOutputPricePerMillionUSD:  row.BaselineOutputPricePerMillionUSD,
			BaselineInputCostUSD:              row.BaselineInputCostUSD,
			BaselineOutputCostUSD:             row.BaselineOutputCostUSD,
			BaselineTotalCostUSD:              row.BaselineTotalCostUSD,
			InputSavingsUSD:                   row.InputSavingsUSD,
			OutputSavingsUSD:                  row.OutputSavingsUSD,
			TotalSavingsUSD:                   row.TotalSavingsUSD,
			LatencyMSSum:                      row.LatencyMSSum,
			LatencyMSCount:                    row.LatencyMSCount,
			LatencyMSMax:                      row.LatencyMSMax,
			TTFBMSSum:                         row.TTFBMSSum,
			TTFBMSCount:                       row.TTFBMSCount,
			TTFBMSMax:                         row.TTFBMSMax,
			UpstreamMSSum:                     row.UpstreamMSSum,
			UpstreamMSCount:                   row.UpstreamMSCount,
			UpstreamMSMax:                     row.UpstreamMSMax,
			DownstreamMSSum:                   row.DownstreamMSSum,
			DownstreamMSCount:                 row.DownstreamMSCount,
			DownstreamMSMax:                   row.DownstreamMSMax,
			UpstreamOutputTokensPerSecSum:     row.UpstreamOutputTokensPerSecSum,
			UpstreamOutputTokensPerSecCount:   row.UpstreamOutputTokensPerSecCount,
			UpstreamOutputTokensPerSecMin:     row.UpstreamOutputTokensPerSecMin,
			UpstreamOutputTokensPerSecMax:     row.UpstreamOutputTokensPerSecMax,
			UpstreamTotalTokensPerSecSum:      row.UpstreamTotalTokensPerSecSum,
			UpstreamTotalTokensPerSecCount:    row.UpstreamTotalTokensPerSecCount,
			UpstreamTotalTokensPerSecMin:      row.UpstreamTotalTokensPerSecMin,
			UpstreamTotalTokensPerSecMax:      row.UpstreamTotalTokensPerSecMax,
			DownstreamOutputTokensPerSecSum:   row.DownstreamOutputTokensPerSecSum,
			DownstreamOutputTokensPerSecCount: row.DownstreamOutputTokensPerSecCount,
			DownstreamOutputTokensPerSecMin:   row.DownstreamOutputTokensPerSecMin,
			DownstreamOutputTokensPerSecMax:   row.DownstreamOutputTokensPerSecMax,
			DownstreamTotalTokensPerSecSum:    row.DownstreamTotalTokensPerSecSum,
			DownstreamTotalTokensPerSecCount:  row.DownstreamTotalTokensPerSecCount,
			DownstreamTotalTokensPerSecMin:    row.DownstreamTotalTokensPerSecMin,
			DownstreamTotalTokensPerSecMax:    row.DownstreamTotalTokensPerSecMax,
			CacheSnapshotCount:                row.CacheSnapshotCount,
			CacheItemsMax:                     row.CacheItemsMax,
			CacheBytesSum:                     row.CacheBytesSum,
			CacheBytesMax:                     row.CacheBytesMax,
			CacheMaxBytesLatest:               row.CacheMaxBytesLatest,
			CacheOccupancyPctSum:              row.CacheOccupancyPctSum,
			CacheOccupancyPctMax:              row.CacheOccupancyPctMax,
		})
	}
	return out
}

func addUsageRowToDailyRollup(rec *usageRollupDailyRecord, row usageRow, baseline usageRollupBaseline) {
	rec.SourceRequestCount++
	if row.Status > 0 && row.Status < 400 {
		rec.SuccessCount++
	}
	if row.Status >= 400 {
		rec.ErrorCount++
	}
	if row.Stream {
		rec.StreamCount++
	}
	switch row.Cache {
	case "hit":
		rec.CacheHitCount++
	case "miss":
		rec.CacheMissCount++
	default:
		rec.CacheBypassCount++
	}
	if row.FallbackUsed {
		rec.FallbackCount++
	}
	rec.AttemptCount += int64(row.Attempts)
	rec.InputTokens += int64(row.InputTokens)
	rec.OutputTokens += int64(row.OutputTokens)
	total := row.TotalTokens
	if total == 0 {
		total = row.InputTokens + row.OutputTokens
	}
	rec.TotalTokens += int64(total)
	rec.InputImageCount += int64(row.InputImageCount)
	rec.InputImageTokens += int64(row.InputImageTokens)
	rec.InputCostUSD += row.InputCostUSD
	rec.ImageCostUSD += row.ImageCostUSD
	rec.OutputCostUSD += row.OutputCostUSD
	rec.TotalCostUSD += row.TotalCostUSD
	rec.UpstreamReportedInputCostUSD += row.UpstreamReportedInputCostUSD
	rec.UpstreamReportedOutputCostUSD += row.UpstreamReportedOutputCostUSD
	rec.UpstreamReportedTotalCostUSD += row.UpstreamReportedTotalCostUSD
	if baseline.ID != "" {
		baselineInput := float64(row.InputTokens) / 1_000_000 * baseline.InputPricePerMillionUSD
		baselineOutput := float64(row.OutputTokens) / 1_000_000 * baseline.OutputPricePerMillionUSD
		rec.BaselineInputCostUSD += baselineInput
		rec.BaselineOutputCostUSD += baselineOutput
		rec.BaselineTotalCostUSD += baselineInput + baselineOutput
		rec.InputSavingsUSD += baselineInput - row.InputCostUSD
		rec.OutputSavingsUSD += baselineOutput - row.OutputCostUSD
		rec.TotalSavingsUSD += (baselineInput + baselineOutput) - row.TotalCostUSD
	}
	rec.LatencyMSSum += row.LatencyMS
	rec.LatencyMSCount++
	if row.LatencyMS > rec.LatencyMSMax {
		rec.LatencyMSMax = row.LatencyMS
	}
	if row.TTFBMS != nil {
		rec.TTFBMSSum += *row.TTFBMS
		rec.TTFBMSCount++
		if *row.TTFBMS > rec.TTFBMSMax {
			rec.TTFBMSMax = *row.TTFBMS
		}
	}
	if row.UpstreamMS != nil {
		rec.UpstreamMSSum += *row.UpstreamMS
		rec.UpstreamMSCount++
		if *row.UpstreamMS > rec.UpstreamMSMax {
			rec.UpstreamMSMax = *row.UpstreamMS
		}
	}
	if row.DownstreamMS != nil {
		rec.DownstreamMSSum += *row.DownstreamMS
		rec.DownstreamMSCount++
		if *row.DownstreamMS > rec.DownstreamMSMax {
			rec.DownstreamMSMax = *row.DownstreamMS
		}
	}
	addFloatToRollup(row.UpstreamOutputTPS, &rec.UpstreamOutputTokensPerSecSum, &rec.UpstreamOutputTokensPerSecCount, &rec.UpstreamOutputTokensPerSecMin, &rec.UpstreamOutputTokensPerSecMax)
	addFloatToRollup(row.UpstreamTotalTPS, &rec.UpstreamTotalTokensPerSecSum, &rec.UpstreamTotalTokensPerSecCount, &rec.UpstreamTotalTokensPerSecMin, &rec.UpstreamTotalTokensPerSecMax)
	addFloatToRollup(row.DownstreamOutputTPS, &rec.DownstreamOutputTokensPerSecSum, &rec.DownstreamOutputTokensPerSecCount, &rec.DownstreamOutputTokensPerSecMin, &rec.DownstreamOutputTokensPerSecMax)
	addFloatToRollup(row.DownstreamTotalTPS, &rec.DownstreamTotalTokensPerSecSum, &rec.DownstreamTotalTokensPerSecCount, &rec.DownstreamTotalTokensPerSecMin, &rec.DownstreamTotalTokensPerSecMax)
	if row.CacheEnabled || row.CacheMaxBytes > 0 {
		rec.CacheSnapshotCount++
		if row.CacheItems > rec.CacheItemsMax {
			rec.CacheItemsMax = row.CacheItems
		}
		rec.CacheBytesSum += row.CacheBytes
		if row.CacheBytes > rec.CacheBytesMax {
			rec.CacheBytesMax = row.CacheBytes
		}
		rec.CacheMaxBytesLatest = row.CacheMaxBytes
		rec.CacheOccupancyPctSum += row.CacheOccupancyPct
		if row.CacheOccupancyPct > rec.CacheOccupancyPctMax {
			rec.CacheOccupancyPctMax = row.CacheOccupancyPct
		}
	}
}

func addFloatToRollup(v *float64, sum *float64, count *int64, minValue *float64, maxValue *float64) {
	if v == nil {
		return
	}
	*sum += *v
	*count++
	if *count == 1 || *v < *minValue {
		*minValue = *v
	}
	if *v > *maxValue {
		*maxValue = *v
	}
}

func (s *usageStore) decisionTelemetrySummary(rows []usageRow) decisionTelemetrySummary {
	summary := decisionTelemetrySummary{
		ByStrategy:         map[string]int64{},
		ByPolicyOutcome:    map[string]int64{},
		ByPolicyErrorClass: map[string]int64{},
		ByFallbackReason:   map[string]int64{},
		ByFilterReason:     map[string]int64{},
		ByCacheReason:      map[string]int64{},
		ByEnabledSignal:    map[string]int64{},
		ByScoreBucket:      map[string]int64{},
		ByThresholdBucket:  map[string]int64{},
		ByMaxTokenBucket:   map[string]int64{},
		ByInputTokenBucket: map[string]int64{},
		ByAdmissionReason:  map[string]int64{},
	}
	if s == nil || s.db == nil || len(rows) == 0 {
		return summary
	}
	requestIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.RequestID != "" {
			requestIDs = append(requestIDs, row.RequestID)
		}
	}
	if len(requestIDs) == 0 {
		return summary
	}
	_ = s.db.Model(&decisionShapeFeatureRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.ShapeFeatures).Error
	_ = s.db.Model(&decisionTargetCandidateRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.Candidates).Error
	_ = s.db.Model(&decisionTargetFilterReasonRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.FilterReasons).Error
	_ = s.db.Model(&routingDecisionRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.Decisions).Error
	_ = s.db.Model(&routingSignalRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.RoutingSignals).Error
	_ = s.db.Model(&dynamicScoreTermRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.ScoreTerms).Error
	_ = s.db.Model(&policyExecutionRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.PolicyExecutions).Error
	_ = s.db.Model(&fallbackTransitionRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.FallbackTransitions).Error
	_ = s.db.Model(&decisionCacheReasonRecord{}).Where("request_id IN ?", requestIDs).Count(&summary.CacheReasons).Error
	type countRow struct {
		Key   string
		Count int64
	}
	var strategies []countRow
	_ = s.db.Model(&routingDecisionRecord{}).Select("strategy AS key, count(*) AS count").Where("request_id IN ?", requestIDs).Group("strategy").Scan(&strategies).Error
	for _, row := range strategies {
		summary.ByStrategy[defaultString(row.Key, "unknown")] = row.Count
	}
	var policyOutcomes []countRow
	_ = s.db.Model(&policyExecutionRecord{}).Select("outcome AS key, count(*) AS count").Where("request_id IN ?", requestIDs).Group("outcome").Scan(&policyOutcomes).Error
	for _, row := range policyOutcomes {
		summary.ByPolicyOutcome[defaultString(row.Key, "unknown")] = row.Count
	}
	var policyErrors []countRow
	_ = s.db.Model(&policyExecutionRecord{}).Select("error_class AS key, count(*) AS count").Where("request_id IN ? AND error_class <> ''", requestIDs).Group("error_class").Scan(&policyErrors).Error
	for _, row := range policyErrors {
		summary.ByPolicyErrorClass[defaultString(row.Key, "unknown")] = row.Count
	}
	var fallbackReasons []countRow
	_ = s.db.Model(&fallbackTransitionRecord{}).Select("fallback_reason AS key, count(*) AS count").Where("request_id IN ?", requestIDs).Group("fallback_reason").Scan(&fallbackReasons).Error
	for _, row := range fallbackReasons {
		summary.ByFallbackReason[defaultString(row.Key, "unknown")] = row.Count
	}
	var filterReasons []countRow
	_ = s.db.Model(&decisionTargetFilterReasonRecord{}).Select("reason AS key, count(*) AS count").Where("request_id IN ?", requestIDs).Group("reason").Scan(&filterReasons).Error
	for _, row := range filterReasons {
		summary.ByFilterReason[defaultString(row.Key, "unknown")] = row.Count
	}
	var cacheReasons []countRow
	_ = s.db.Model(&decisionCacheReasonRecord{}).Select("reason AS key, count(*) AS count").Where("request_id IN ?", requestIDs).Group("reason").Scan(&cacheReasons).Error
	for _, row := range cacheReasons {
		summary.ByCacheReason[defaultString(row.Key, "unknown")] = row.Count
	}
	for _, row := range rows {
		summary.ByMaxTokenBucket[defaultString(row.MaxTokenBucket, "unknown")]++
		summary.ByInputTokenBucket[defaultString(row.InputTokenBucket, "unknown")]++
		summary.ByAdmissionReason[defaultString(row.AdmissionReason, "admitted")]++
		for _, signal := range row.EnabledSignals {
			summary.ByEnabledSignal[signal]++
		}
		for _, bucket := range row.ScoreBuckets {
			summary.ByScoreBucket[bucket]++
		}
		for _, bucket := range row.ThresholdBuckets {
			summary.ByThresholdBucket[bucket]++
		}
	}
	return summary
}

func (s *usageStore) rows(opts UsageReportOptions) ([]usageRow, error) {
	return s.rowsWithBuckets(opts, true)
}

func (s *usageStore) rowsWithoutBuckets(opts UsageReportOptions) ([]usageRow, error) {
	return s.rowsWithBuckets(opts, false)
}

func (s *usageStore) rowsWithBuckets(opts UsageReportOptions, includeBuckets bool) ([]usageRow, error) {
	var records []usageRecord
	q := s.usageRowsQuery(opts)
	if err := q.Order("ts ASC, request_id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	out, err := usageRowsFromRecords(records)
	if err != nil {
		return nil, err
	}
	if includeBuckets {
		s.loadUsageReportBuckets(out)
	}
	return out, nil
}

type tokenScalarAggRecord struct {
	Key                      string
	SecondaryKey             string
	Calls                    int64
	Errors                   int64
	Streams                  int64
	CacheHits                int64
	CacheMisses              int64
	CacheBypass              int64
	Fallbacks                int64
	Attempts                 int64
	InputTokens              int64
	OutputTokens             int64
	TotalTokens              int64
	InputImageCount          int64
	InputImageTokens         int64
	PIIFilteredRequests      int64
	PIIFilterReplacements    int64
	InputCostUSD             float64
	ImageCostUSD             float64
	OutputCostUSD            float64
	TotalCostUSD             float64
	UpstreamReportedCostUSD  float64
	BaselineCostUSD          float64
	LatencyMS                int64
	MaxLatencyMS             int64
	TTFBMS                   int64
	TTFBCount                int64
	MaxTTFBMS                int64
	UpstreamMS               int64
	UpstreamMSCount          int64
	MaxUpstreamMS            int64
	DownstreamMS             int64
	DownstreamMSCount        int64
	MaxDownstreamMS          int64
	UpstreamOutputTPS        float64
	UpstreamOutputTPSCount   int64
	UpstreamTotalTPS         float64
	UpstreamTotalTPSCount    int64
	DownstreamOutputTPS      float64
	DownstreamOutputTPSCount int64
	DownstreamTotalTPS       float64
	DownstreamTotalTPSCount  int64
	CacheItemsMax            int64
	CacheBytesMax            int64
	CacheMaxBytesLatest      int64
	CacheOccupancyMax        float64
	CacheBytesSum            int64
	CacheOccupancySum        float64
	CacheSnapshotCount       int64
}

type diagnosticAggSQLRecord struct {
	Key                     string
	SecondaryKey            string
	Status                  int
	ErrorClass              string
	Provider                string
	Model                   string
	Dialect                 string
	RequestShapeFingerprint string
	ToolSchemaFingerprint   string
	Requests                int64
	Errors                  int64
	Attempts                int64
	RetryableAttempts       int64
	TimeoutAttempts         int64
	Fallbacks               int64
	FallbackSucceeded       int64
	FallbackFailed          int64
	TerminalErrors          int64
	UpstreamErrorDetails    int64
	FieldsStripped          int64
	FieldsRewritten         int64
	UnsupportedFields       int64
	TranslationWarnings     int64
	AffectedUsers           int64
	AffectedClients         int64
	LatencyMS               int64
	UpstreamMS              int64
	UpstreamMSCount         int64
	MaxLatencyMS            int64
	MaxUpstreamMS           int64
}

type shapeAggSQLRecord struct {
	Key                string
	SecondaryKey       string
	Requests           int64
	Rejected           int64
	Queued             int64
	SkippedTargets     int64
	CooldownsStarted   int64
	RetryAfterMS       int64
	RetryAfterCount    int64
	MaxRetryAfterMS    int64
	QueueWaitMS        int64
	QueueWaitCount     int64
	MaxQueueWaitMS     int64
	EstimatedInput     int64
	ReservedOutput     int64
	TotalReserved      int64
	Upstream429        int64
	UpstreamQuota      int64
	Fallbacks          int64
	RouteAroundSuccess int64
	P50QueueWaitMS     int64
	P95QueueWaitMS     int64
}

type savingsAggRecord struct {
	Key               string
	Requests          int64
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	ActualCostUSD     float64
	BaselineCostUSD   float64
	MissingTokens     int64
	MissingActualCost int64
}

type adminOverviewSQLResult struct {
	Total          tokenScalarAggRecord
	ByHour         []tokenScalarAggRecord
	ByToken        []tokenScalarAggRecord
	ByGroup        []tokenScalarAggRecord
	ByProvider     []tokenScalarAggRecord
	ByStatus       []tokenScalarAggRecord
	RecentRequests []usageRow
	GroupHasMore   bool
	RequestHasMore bool
}

func (s *usageStore) adminOverviewSQL(opts UsageReportOptions, baseline adminSavingsBaselineDTO, limit int) (adminOverviewSQLResult, error) {
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	aggExpr := adminScalarAggSQLSelectExpr(baseline)
	var result adminOverviewSQLResult
	if err := s.usageRowsQuery(opts).Select(aggExpr).Scan(&result.Total).Error; err != nil {
		return adminOverviewSQLResult{}, err
	}
	if latest, ok, err := s.adminLatestCacheSnapshot(opts); err != nil {
		return adminOverviewSQLResult{}, err
	} else if ok {
		result.Total.CacheItemsMax = latest.CacheItems
		result.Total.CacheBytesMax = latest.CacheBytes
		result.Total.CacheMaxBytesLatest = latest.CacheMaxBytes
		result.Total.CacheOccupancyMax = latest.CacheOccupancyPct
	}
	hourExpr := "substr(ts, 1, 13) || ':00:00Z'"
	var err error
	if result.ByHour, err = s.adminOverviewGroupSQL(opts, hourExpr, aggExpr, "key ASC", 0); err != nil {
		return adminOverviewSQLResult{}, err
	}
	tokenExpr := adminSQLDefault("token_id", "unknown")
	if result.ByToken, result.GroupHasMore, err = s.adminOverviewTopNSQL(opts, tokenExpr, aggExpr, limitN); err != nil {
		return adminOverviewSQLResult{}, err
	}
	groupExpr := "COALESCE(NULLIF(resolved_group, ''), NULLIF(requested_model, ''), 'unknown')"
	var groupHasMore bool
	if result.ByGroup, groupHasMore, err = s.adminOverviewTopNSQL(opts, groupExpr, aggExpr, limitN); err != nil {
		return adminOverviewSQLResult{}, err
	}
	result.GroupHasMore = result.GroupHasMore || groupHasMore
	providerExpr := adminSQLDefault("target_provider", "unknown") + " || '/' || " + adminSQLDefault("target_model", "unknown")
	if result.ByProvider, groupHasMore, err = s.adminOverviewTopNSQL(opts, providerExpr, aggExpr, limitN); err != nil {
		return adminOverviewSQLResult{}, err
	}
	result.GroupHasMore = result.GroupHasMore || groupHasMore
	statusExpr := "CAST(status AS TEXT)"
	if result.ByStatus, groupHasMore, err = s.adminOverviewTopNSQL(opts, statusExpr, aggExpr, limitN); err != nil {
		return adminOverviewSQLResult{}, err
	}
	result.GroupHasMore = result.GroupHasMore || groupHasMore
	result.RecentRequests, result.RequestHasMore, err = s.adminOverviewRecentRequestsSQL(opts, limitN)
	if err != nil {
		return adminOverviewSQLResult{}, err
	}
	return result, nil
}

func (s *usageStore) adminOverviewTopNSQL(opts UsageReportOptions, keyExpr, aggExpr string, limit int) ([]tokenScalarAggRecord, bool, error) {
	records, err := s.adminOverviewGroupSQL(opts, keyExpr, aggExpr, "COUNT(*) DESC, key ASC", limit+1)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return records, hasMore, nil
}

func (s *usageStore) adminOverviewGroupSQL(opts UsageReportOptions, keyExpr, aggExpr, orderExpr string, limit int) ([]tokenScalarAggRecord, error) {
	selectExpr := keyExpr + " AS key, '' AS secondary_key, " + aggExpr
	q := s.usageRowsQuery(opts).Select(selectExpr).Group(keyExpr).Order(orderExpr)
	if limit > 0 {
		q = q.Limit(limit)
	}
	var records []tokenScalarAggRecord
	if err := q.Scan(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func (s *usageStore) adminLatestCacheSnapshot(opts UsageReportOptions) (usageRecord, bool, error) {
	var rec usageRecord
	err := s.usageRowsQuery(opts).
		Where("(cache_enabled = ? OR cache_max_bytes > ?)", true, 0).
		Order("ts DESC, request_id DESC").
		Limit(1).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return usageRecord{}, false, nil
	}
	if err != nil {
		return usageRecord{}, false, err
	}
	return rec, true, nil
}

func (s *usageStore) adminOverviewRecentRequestsSQL(opts UsageReportOptions, limit int) ([]usageRow, bool, error) {
	if limit <= 0 {
		return nil, false, nil
	}
	var records []usageRecord
	if err := s.usageRowsQuery(opts).Order("ts DESC, request_id DESC").Limit(limit + 1).Find(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	rows, err := usageRowsFromRecords(records)
	if err != nil {
		return nil, false, err
	}
	return rows, hasMore, nil
}

func (s *usageStore) adminSavingsAggsSQL(opts UsageReportOptions, baseline adminSavingsBaselineDTO, limit int) (adminSavingsRow, []adminSavingsRow, []adminSavingsRow, bool, int64, int64, error) {
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	selectExpr := adminSavingsAggSQLSelectExpr(baseline)
	var totalRec savingsAggRecord
	if err := s.usageRowsQuery(opts).Select(selectExpr).Scan(&totalRec).Error; err != nil {
		return adminSavingsRow{}, nil, nil, false, 0, 0, err
	}
	hourExpr := "substr(ts, 1, 13) || ':00:00Z'"
	var timeRecords []savingsAggRecord
	if err := s.usageRowsQuery(opts).Select(hourExpr + " AS key, " + selectExpr).Group(hourExpr).Order("key ASC").Scan(&timeRecords).Error; err != nil {
		return adminSavingsRow{}, nil, nil, false, 0, 0, err
	}
	groupExpr := "COALESCE(NULLIF(resolved_group, ''), NULLIF(requested_model, ''), 'unknown')"
	savingsOrderExpr := "(" + adminBaselineCostSQLExpr(baseline) + " - SUM(total_cost_usd)) DESC, key ASC"
	var groupRecords []savingsAggRecord
	if err := s.usageRowsQuery(opts).Select(groupExpr + " AS key, " + selectExpr).Group(groupExpr).Order(savingsOrderExpr).Limit(limitN + 1).Scan(&groupRecords).Error; err != nil {
		return adminSavingsRow{}, nil, nil, false, 0, 0, err
	}
	hasMore := len(groupRecords) > limitN
	if hasMore {
		groupRecords = groupRecords[:limitN]
	}
	return savingsRowFromSQLRecord("total", totalRec), savingsRowsFromSQLRecords(timeRecords), savingsRowsFromSQLRecords(groupRecords), hasMore, totalRec.MissingTokens, totalRec.MissingActualCost, nil
}

func adminSavingsAggSQLSelectExpr(baseline adminSavingsBaselineDTO) string {
	return `COUNT(*) AS requests,
		SUM(input_tokens) AS input_tokens,
		SUM(output_tokens) AS output_tokens,
		SUM(CASE WHEN total_tokens = 0 THEN input_tokens + output_tokens ELSE total_tokens END) AS total_tokens,
		SUM(total_cost_usd) AS actual_cost_usd,
		` + adminBaselineCostSQLExpr(baseline) + ` AS baseline_cost_usd,
		SUM(CASE WHEN input_tokens = 0 AND output_tokens = 0 AND total_tokens = 0 THEN 1 ELSE 0 END) AS missing_tokens,
		SUM(CASE WHEN total_cost_usd = 0 AND (input_tokens > 0 OR output_tokens > 0 OR total_tokens > 0) THEN 1 ELSE 0 END) AS missing_actual_cost`
}

func savingsRowsFromSQLRecords(records []savingsAggRecord) []adminSavingsRow {
	rows := make([]adminSavingsRow, 0, len(records))
	for _, rec := range records {
		rows = append(rows, savingsRowFromSQLRecord(rec.Key, rec))
	}
	return rows
}

func savingsRowFromSQLRecord(key string, rec savingsAggRecord) adminSavingsRow {
	if key == "" {
		key = defaultString(rec.Key, "unknown")
	}
	savings := rec.BaselineCostUSD - rec.ActualCostUSD
	return adminSavingsRow{
		Key:             key,
		Requests:        rec.Requests,
		InputTokens:     rec.InputTokens,
		OutputTokens:    rec.OutputTokens,
		TotalTokens:     rec.TotalTokens,
		ActualCostUSD:   rec.ActualCostUSD,
		BaselineCostUSD: rec.BaselineCostUSD,
		SavingsUSD:      savings,
		SavingsPct:      ratioPctFloat(savings, rec.BaselineCostUSD),
	}
}

func (s *usageStore) adminScalarAggsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, baseline adminSavingsBaselineDTO, sortKey string, limit int) (map[string]*adminScalarAgg, *agg, float64, bool, error) {
	keyExpr, ok := adminScalarDimensionSQLExpr(spec.Dimension)
	if !ok {
		return nil, nil, 0, false, fmt.Errorf("unsupported SQL scalar dimension %q", spec.Dimension)
	}
	secondaryExpr, ok := adminScalarDimensionSQLExpr(spec.Secondary)
	if !ok {
		return nil, nil, 0, false, fmt.Errorf("unsupported SQL scalar secondary dimension %q", spec.Secondary)
	}
	aggExpr := adminScalarAggSQLSelectExpr(baseline)
	groupSelect := keyExpr + ` AS key,
		` + secondaryExpr + ` AS secondary_key,
		` + aggExpr
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	var records []tokenScalarAggRecord
	grouped := s.usageRowsQuery(opts).Select(groupSelect).Order(adminScalarAggSQLOrder(sortKey, baseline)).Limit(limitN + 1)
	if groupBy := adminScalarAggSQLGroupBy(spec, keyExpr, secondaryExpr); groupBy != "" {
		grouped = grouped.Group(groupBy)
	}
	if err := grouped.Scan(&records).Error; err != nil {
		return nil, nil, 0, false, err
	}
	hasMore := len(records) > limitN
	if hasMore {
		records = records[:limitN]
	}
	var totalRec tokenScalarAggRecord
	if err := s.usageRowsQuery(opts).Select(aggExpr).Scan(&totalRec).Error; err != nil {
		return nil, nil, 0, false, err
	}
	table := make(map[string]*adminScalarAgg, len(records))
	for _, rec := range records {
		scalar := adminScalarAggFromSQLRecord(rec, baseline)
		mapKey := joinKey(scalar.Key, scalar.SecondaryKey)
		table[mapKey] = scalar
	}
	total := aggFromTokenScalarAggRecord(totalRec)
	if err := s.applyLatestCacheSnapshotsSQL(opts, spec, table, &total); err != nil {
		return nil, nil, 0, false, err
	}
	return table, &total, totalRec.BaselineCostUSD, hasMore, nil
}

type latestCacheSnapshotRecord struct {
	CacheItems        int64
	CacheBytes        int64
	CacheMaxBytes     int64
	CacheOccupancyPct float64
}

func (s *usageStore) applyLatestCacheSnapshotsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, table map[string]*adminScalarAgg, total *agg) error {
	latest, ok, err := s.latestCacheSnapshotSQL(s.usageRowsQuery(opts))
	if err != nil {
		return err
	}
	if ok {
		applyLatestCacheSnapshotToAgg(total, latest)
	}
	keyExpr, keyOK := adminScalarDimensionSQLExpr(spec.Dimension)
	secondaryExpr, secondaryOK := adminScalarDimensionSQLExpr(spec.Secondary)
	if !keyOK || !secondaryOK {
		return nil
	}
	for _, scalar := range table {
		q := s.usageRowsQuery(opts)
		if spec.Dimension != "" {
			q = q.Where(keyExpr+" = ?", scalar.Key)
		}
		if spec.Secondary != "" {
			q = q.Where(secondaryExpr+" = ?", scalar.SecondaryKey)
		}
		latest, ok, err := s.latestCacheSnapshotSQL(q)
		if err != nil {
			return err
		}
		if ok {
			applyLatestCacheSnapshotToAgg(&scalar.Agg, latest)
		}
	}
	return nil
}

func (s *usageStore) latestCacheSnapshotSQL(q *gorm.DB) (latestCacheSnapshotRecord, bool, error) {
	var rec latestCacheSnapshotRecord
	err := q.Select("cache_items, cache_bytes, cache_max_bytes, cache_occupancy_pct").
		Where("cache_enabled = ? OR cache_max_bytes > 0", true).
		Order("ts DESC, request_id DESC").
		Limit(1).
		Scan(&rec).Error
	if err != nil {
		return latestCacheSnapshotRecord{}, false, err
	}
	return rec, rec.CacheMaxBytes > 0 || rec.CacheItems > 0 || rec.CacheBytes > 0 || rec.CacheOccupancyPct > 0, nil
}

func applyLatestCacheSnapshotToAgg(a *agg, latest latestCacheSnapshotRecord) {
	if a == nil {
		return
	}
	a.CacheItemsLatest = latest.CacheItems
	a.CacheBytesLatest = latest.CacheBytes
	a.CacheMaxBytesLatest = latest.CacheMaxBytes
	a.CacheOccupancyLatest = latest.CacheOccupancyPct
}

func (s *usageStore) adminScalarMultiBucketAggsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, sortKey string, limit int) (map[string]*adminScalarAgg, *agg, bool, error) {
	secondaryExpr, ok := adminScalarDimensionSQLExprForAlias(spec.Secondary, "u")
	if !ok {
		return nil, nil, false, fmt.Errorf("unsupported SQL scalar secondary dimension %q", spec.Secondary)
	}
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	table := map[string]*adminScalarAgg{}
	var hasMore bool
	addBucketed := func(bucketed *gorm.DB) error {
		records, more, err := s.adminScalarBucketedAggsSQL(bucketed, sortKey, limitN)
		if err != nil {
			return err
		}
		hasMore = hasMore || more
		for _, rec := range records {
			scalar := adminScalarAggFromSQLRecord(rec, adminSavingsBaselineDTO{})
			mapKey := joinKey(scalar.Key, scalar.SecondaryKey)
			if existing := table[mapKey]; existing != nil {
				existing.Agg.Calls += scalar.Agg.Calls
				existing.Agg.Errors += scalar.Agg.Errors
				existing.Agg.Streams += scalar.Agg.Streams
				existing.Agg.CacheHits += scalar.Agg.CacheHits
				existing.Agg.CacheMisses += scalar.Agg.CacheMisses
				existing.Agg.CacheBypass += scalar.Agg.CacheBypass
				existing.Agg.Fallbacks += scalar.Agg.Fallbacks
				existing.Agg.Attempts += scalar.Agg.Attempts
				existing.Agg.InputTokens += scalar.Agg.InputTokens
				existing.Agg.OutputTokens += scalar.Agg.OutputTokens
				existing.Agg.TotalTokens += scalar.Agg.TotalTokens
				existing.InputImageCount += scalar.InputImageCount
				existing.InputImageTokens += scalar.InputImageTokens
				existing.PIIFilteredRequests += scalar.PIIFilteredRequests
				existing.PIIFilterReplacements += scalar.PIIFilterReplacements
				existing.UpstreamReportedCostUSD += scalar.UpstreamReportedCostUSD
				existing.Agg.InputCostUSD += scalar.Agg.InputCostUSD
				existing.Agg.ImageCostUSD += scalar.Agg.ImageCostUSD
				existing.Agg.OutputCostUSD += scalar.Agg.OutputCostUSD
				existing.Agg.TotalCostUSD += scalar.Agg.TotalCostUSD
				existing.Agg.LatencyMS += scalar.Agg.LatencyMS
				existing.Agg.MaxLatencyMS = adminMaxInt64(existing.Agg.MaxLatencyMS, scalar.Agg.MaxLatencyMS)
				continue
			}
			table[mapKey] = scalar
		}
		return nil
	}
	parent := s.usageRowsQuery(opts)
	switch spec.Dimension {
	case "dynamic_signal":
		bucketed := s.db.Table("(?) AS u", parent).
			Joins("JOIN request_routing_signals rs ON rs.request_id = u.request_id").
			Where("rs.strategy = ? AND rs.source = ? AND rs.bool_value = ?", "dynamic_score", "dynamic_score", true).
			Select("u.*, COALESCE(NULLIF(rs.signal_name, ''), 'none') AS key, " + secondaryExpr + " AS secondary_key")
		if err := addBucketed(bucketed); err != nil {
			return nil, nil, false, err
		}
	case "dynamic_score_bucket":
		valueBucketed := s.db.Table("(?) AS u", parent).
			Joins("JOIN request_dynamic_score_terms dst ON dst.request_id = u.request_id").
			Where("COALESCE(NULLIF(dst.value_bucket, ''), '') <> ''").
			Select("u.*, COALESCE(NULLIF(dst.score_name, ''), 'score') || ':' || dst.value_bucket AS key, " + secondaryExpr + " AS secondary_key")
		if err := addBucketed(valueBucketed); err != nil {
			return nil, nil, false, err
		}
		finalBucketed := s.db.Table("(?) AS u", parent).
			Joins("JOIN request_dynamic_score_terms dst ON dst.request_id = u.request_id").
			Where("COALESCE(NULLIF(dst.final_score_bucket, ''), '') <> ''").
			Select("u.*, 'final_score:' || dst.final_score_bucket AS key, " + secondaryExpr + " AS secondary_key")
		if err := addBucketed(finalBucketed); err != nil {
			return nil, nil, false, err
		}
	case "dynamic_threshold":
		bucketed := s.db.Table("(?) AS u", parent).
			Joins("JOIN request_target_filter_reasons fr ON fr.request_id = u.request_id").
			Where("fr.reason = ? OR (fr.stage = ? AND fr.reason LIKE ?)", "max-tokens-honored", "dynamic_score", "%threshold%").
			Select("u.*, CASE WHEN fr.reason = 'max-tokens-honored' THEN 'max_token_cap_filtered' ELSE COALESCE(NULLIF(fr.reason, ''), 'none') END AS key, " + secondaryExpr + " AS secondary_key")
		if err := addBucketed(bucketed); err != nil {
			return nil, nil, false, err
		}
	case "max_token_bucket", "input_token_bucket":
		feature := map[string]string{
			"max_token_bucket":   "max_token_bucket",
			"input_token_bucket": "input_token_bucket",
		}[spec.Dimension]
		bucketed := s.db.Table("(?) AS u", parent).
			Joins("JOIN request_decision_shape_features sf ON sf.request_id = u.request_id").
			Where("sf.feature_name = ?", feature).
			Select("u.*, COALESCE(NULLIF(sf.text_value, ''), 'unknown') AS key, " + secondaryExpr + " AS secondary_key")
		if err := addBucketed(bucketed); err != nil {
			return nil, nil, false, err
		}
	default:
		return nil, nil, false, fmt.Errorf("unsupported SQL multi-bucket dimension %q", spec.Dimension)
	}
	var totalRec tokenScalarAggRecord
	if err := s.usageRowsQuery(opts).Select(adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})).Scan(&totalRec).Error; err != nil {
		return nil, nil, false, err
	}
	total := aggFromTokenScalarAggRecord(totalRec)
	if len(table) > limitN {
		hasMore = true
		trimmed := adminScalarRowsFromAgg(table, sortKey, limitN)
		keep := map[string]bool{}
		for _, row := range trimmed {
			keep[joinKey(row.Key, row.SecondaryKey)] = true
		}
		for key := range table {
			if !keep[key] {
				delete(table, key)
			}
		}
	}
	return table, &total, hasMore, nil
}

func (s *usageStore) adminScalarDerivedBucketAggsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, sortKey string, limit int) (map[string]*adminScalarAgg, *agg, bool, error) {
	secondaryExpr, ok := adminScalarDimensionSQLExprForAlias(spec.Secondary, "u")
	if !ok {
		return nil, nil, false, fmt.Errorf("unsupported SQL scalar secondary dimension %q", spec.Secondary)
	}
	selectors, err := adminScalarDerivedBucketSQLSelectors(spec, secondaryExpr)
	if err != nil {
		return nil, nil, false, err
	}
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	parent := s.usageRowsQuery(opts)
	var bucketed *gorm.DB
	for _, selector := range selectors {
		q := s.db.Table("(?) AS u", parent).
			Select("u.*, "+selector.KeyExpr+" AS key, "+secondaryExpr+" AS secondary_key").
			Where(selector.WhereExpr, selector.Args...)
		if bucketed == nil {
			bucketed = q
			continue
		}
		bucketed = s.db.Raw("? UNION ALL ?", bucketed, q)
	}
	if bucketed == nil {
		return map[string]*adminScalarAgg{}, &agg{}, false, nil
	}
	records, hasMore, err := s.adminScalarBucketedAggsSQL(bucketed, sortKey, limitN)
	if err != nil {
		return nil, nil, false, err
	}
	table := make(map[string]*adminScalarAgg, len(records))
	for _, rec := range records {
		scalar := adminScalarAggFromSQLRecord(rec, adminSavingsBaselineDTO{})
		table[joinKey(scalar.Key, scalar.SecondaryKey)] = scalar
	}
	var totalRec tokenScalarAggRecord
	if err := parent.Select(adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})).Scan(&totalRec).Error; err != nil {
		return nil, nil, false, err
	}
	total := aggFromTokenScalarAggRecord(totalRec)
	if err := s.applyLatestCacheSnapshotsSQL(opts, spec, table, &total); err != nil {
		return nil, nil, false, err
	}
	return table, &total, hasMore, nil
}

type adminScalarDerivedBucketSQLSelector struct {
	KeyExpr   string
	WhereExpr string
	Args      []any
}

func adminScalarDerivedBucketSQLSelectors(spec adminScalarEndpointSpec, secondaryExpr string) ([]adminScalarDerivedBucketSQLSelector, error) {
	switch {
	case spec.Anomalies && spec.Dimension == "anomaly":
		return adminAnomalySQLSelectors(), nil
	case spec.Dimension == "troubleshooting_bucket":
		return adminTroubleshootingBucketSQLSelectors(), nil
	case spec.Dimension == "capability":
		return adminCapabilitySQLSelectors(), nil
	default:
		return nil, fmt.Errorf("unsupported SQL derived bucket dimension %q", spec.Dimension)
	}
}

func adminTroubleshootingBucketSQLSelectors() []adminScalarDerivedBucketSQLSelector {
	lowerError := "LOWER(COALESCE(u.error, ''))"
	return []adminScalarDerivedBucketSQLSelector{
		{KeyExpr: "CASE WHEN COALESCE(NULLIF(u.quota_state, ''), 'ok') <> 'ok' THEN 'quota:' || u.quota_state ELSE 'quota:error' END", WhereExpr: "(COALESCE(NULLIF(u.quota_state, ''), 'ok') <> ? OR " + lowerError + " LIKE ?)", Args: []any{"ok", "%quota%"}},
		{KeyExpr: "'tpm'", WhereExpr: "(" + lowerError + " LIKE ? OR " + lowerError + " LIKE ?)", Args: []any{"%tpm%", "%token rate%"}},
		{KeyExpr: "'rpm-rate-limit'", WhereExpr: "(" + lowerError + " LIKE ? OR " + lowerError + " LIKE ? OR u.status = ?)", Args: []any{"%rpm%", "%rate limit%", http.StatusTooManyRequests}},
		{KeyExpr: "'concurrency'", WhereExpr: "(" + lowerError + " LIKE ? OR " + lowerError + " LIKE ?)", Args: []any{"%concurrency%", "%in-flight%"}},
		{KeyExpr: "'max-token-or-context'", WhereExpr: "(" + lowerError + " LIKE ? OR " + lowerError + " LIKE ? OR " + lowerError + " LIKE ? OR " + lowerError + " LIKE ?)", Args: []any{"%max token%", "%max_tokens%", "%max_output_tokens%", "%context length%"}},
		{KeyExpr: "'upstream-quota-billing'", WhereExpr: "(" + lowerError + " LIKE ? OR " + lowerError + " LIKE ? OR " + lowerError + " LIKE ?)", Args: []any{"%upstream-quota%", "%billing%", "%credits%"}},
		{KeyExpr: "'key:' || u.key_state", WhereExpr: "COALESCE(NULLIF(u.key_state, ''), 'ok') NOT IN (?, ?)", Args: []any{"ok", "active"}},
		{KeyExpr: "'cache-hit'", WhereExpr: "u.cache = ?", Args: []any{"hit"}},
		{KeyExpr: "'cache-bypass'", WhereExpr: "u.cache = ?", Args: []any{"bypass"}},
		{KeyExpr: "'fallback'", WhereExpr: "u.fallback_used = ?", Args: []any{true}},
		{KeyExpr: "'multi-attempt'", WhereExpr: "u.attempts > ?", Args: []any{1}},
		{KeyExpr: "'server-error'", WhereExpr: "u.status >= ?", Args: []any{500}},
		{KeyExpr: "'client-error'", WhereExpr: "u.status >= ? AND u.status < ?", Args: []any{400, 500}},
		{KeyExpr: "'ok'", WhereExpr: adminTroubleshootingOKSQLWhere(), Args: []any{false, 2, 400, "hit", "bypass", "ok", "active", "ok", "%quota%", "%tpm%", "%token rate%", "%rpm%", "%rate limit%", http.StatusTooManyRequests, "%concurrency%", "%in-flight%", "%max token%", "%max_tokens%", "%max_output_tokens%", "%context length%", "%upstream-quota%", "%billing%", "%credits%"}},
	}
}

func adminTroubleshootingOKSQLWhere() string {
	lowerError := "LOWER(COALESCE(u.error, ''))"
	return `u.fallback_used = ? AND u.attempts < ? AND u.status < ? AND u.cache NOT IN (?, ?) AND COALESCE(NULLIF(u.key_state, ''), 'ok') IN (?, ?) AND COALESCE(NULLIF(u.quota_state, ''), 'ok') = ? AND ` +
		lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND u.status <> ? AND ` +
		lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` +
		lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ? AND ` + lowerError + ` NOT LIKE ?`
}

func adminCapabilitySQLSelectors() []adminScalarDerivedBucketSQLSelector {
	return []adminScalarDerivedBucketSQLSelector{
		{KeyExpr: "'image-input'", WhereExpr: "(u.input_has_image = ? OR u.input_image_count > ? OR u.input_image_tokens > ?)", Args: []any{true, 0, 0}},
		{KeyExpr: "'streaming'", WhereExpr: "u.stream = ?", Args: []any{true}},
		{KeyExpr: "'pii-filtered'", WhereExpr: "u.pii_filter_applied = ?", Args: []any{true}},
		{KeyExpr: "'cacheable'", WhereExpr: "u.cache IN (?, ?)", Args: []any{"hit", "miss"}},
		{KeyExpr: "'dialect:' || u.target_dialect", WhereExpr: "COALESCE(NULLIF(u.target_dialect, ''), '') <> ''"},
		{KeyExpr: "'text'", WhereExpr: "u.input_has_image = ? AND u.input_image_count = ? AND u.input_image_tokens = ? AND u.stream = ? AND u.pii_filter_applied = ? AND u.cache NOT IN (?, ?) AND COALESCE(NULLIF(u.target_dialect, ''), '') = ''", Args: []any{false, 0, 0, false, false, "hit", "miss"}},
	}
}

func adminAnomalySQLSelectors() []adminScalarDerivedBucketSQLSelector {
	return []adminScalarDerivedBucketSQLSelector{
		{KeyExpr: "'error'", WhereExpr: "u.status >= ?", Args: []any{400}},
		{KeyExpr: "'fallback'", WhereExpr: "u.fallback_used = ?", Args: []any{true}},
		{KeyExpr: "'multi-attempt'", WhereExpr: "u.attempts > ?", Args: []any{1}},
		{KeyExpr: "'slow-request'", WhereExpr: "u.latency_ms >= ?", Args: []any{30000}},
		{KeyExpr: "'expensive-request'", WhereExpr: "u.total_cost_usd >= ?", Args: []any{1}},
		{KeyExpr: "'quota-' || u.quota_state", WhereExpr: "COALESCE(NULLIF(u.quota_state, ''), 'ok') <> ?", Args: []any{"ok"}},
		{KeyExpr: "'key-' || u.key_state", WhereExpr: "COALESCE(NULLIF(u.key_state, ''), 'ok') NOT IN (?, ?)", Args: []any{"ok", "active"}},
	}
}

func (s *usageStore) adminScalarBucketedAggsSQL(bucketed *gorm.DB, sortKey string, limitN int) ([]tokenScalarAggRecord, bool, error) {
	var records []tokenScalarAggRecord
	selectExpr := "key, secondary_key, " + adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})
	q := s.db.Table("(?) AS b", bucketed).Select(selectExpr).Group("key, secondary_key").Order(adminScalarAggSQLOrder(sortKey, adminSavingsBaselineDTO{})).Limit(limitN + 1)
	if err := q.Scan(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limitN
	if hasMore {
		records = records[:limitN]
	}
	return records, hasMore, nil
}

func adminScalarAggSQLGroupBy(spec adminScalarEndpointSpec, keyExpr, secondaryExpr string) string {
	parts := make([]string, 0, 2)
	if spec.Dimension != "" {
		parts = append(parts, keyExpr)
	}
	if spec.Secondary != "" {
		parts = append(parts, secondaryExpr)
	}
	return strings.Join(parts, ", ")
}

func adminScalarAggSQLSelectExpr(baseline adminSavingsBaselineDTO) string {
	return `
		COUNT(*) AS calls,
		SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END) AS errors,
		SUM(CASE WHEN stream THEN 1 ELSE 0 END) AS streams,
		SUM(CASE WHEN cache = 'hit' THEN 1 ELSE 0 END) AS cache_hits,
		SUM(CASE WHEN cache = 'miss' THEN 1 ELSE 0 END) AS cache_misses,
		SUM(CASE WHEN cache != 'hit' AND cache != 'miss' THEN 1 ELSE 0 END) AS cache_bypass,
		SUM(CASE WHEN fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(attempts) AS attempts,
		SUM(input_tokens) AS input_tokens,
		SUM(output_tokens) AS output_tokens,
		SUM(CASE WHEN total_tokens = 0 THEN input_tokens + output_tokens ELSE total_tokens END) AS total_tokens,
		SUM(input_image_count) AS input_image_count,
		SUM(input_image_tokens) AS input_image_tokens,
		SUM(CASE WHEN pii_filter_applied THEN 1 ELSE 0 END) AS pii_filtered_requests,
		SUM(pii_filter_replacements) AS pii_filter_replacements,
		SUM(input_cost_usd) AS input_cost_usd,
		SUM(image_cost_usd) AS image_cost_usd,
		SUM(output_cost_usd) AS output_cost_usd,
		SUM(total_cost_usd) AS total_cost_usd,
		SUM(upstream_reported_total_cost_usd) AS upstream_reported_cost_usd,
		` + adminBaselineCostSQLExpr(baseline) + ` AS baseline_cost_usd,
		SUM(latency_ms) AS latency_ms,
		MAX(latency_ms) AS max_latency_ms,
		SUM(COALESCE(ttfb_ms, 0)) AS ttfb_ms,
		COUNT(ttfb_ms) AS ttfb_count,
		MAX(COALESCE(ttfb_ms, 0)) AS max_ttfb_ms,
		SUM(COALESCE(upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(upstream_duration_ms) AS upstream_ms_count,
		MAX(COALESCE(upstream_duration_ms, 0)) AS max_upstream_ms,
		SUM(COALESCE(downstream_duration_ms, 0)) AS downstream_ms,
		COUNT(downstream_duration_ms) AS downstream_ms_count,
		MAX(COALESCE(downstream_duration_ms, 0)) AS max_downstream_ms,
		SUM(COALESCE(upstream_output_tokens_per_sec, 0)) AS upstream_output_tps,
		COUNT(upstream_output_tokens_per_sec) AS upstream_output_tps_count,
		SUM(COALESCE(upstream_total_tokens_per_sec, 0)) AS upstream_total_tps,
		COUNT(upstream_total_tokens_per_sec) AS upstream_total_tps_count,
		SUM(COALESCE(downstream_output_tokens_per_sec, 0)) AS downstream_output_tps,
		COUNT(downstream_output_tokens_per_sec) AS downstream_output_tps_count,
		SUM(COALESCE(downstream_total_tokens_per_sec, 0)) AS downstream_total_tps,
		COUNT(downstream_total_tokens_per_sec) AS downstream_total_tps_count,
		MAX(cache_items) AS cache_items_max,
		MAX(cache_bytes) AS cache_bytes_max,
		MAX(cache_max_bytes) AS cache_max_bytes_latest,
		MAX(cache_occupancy_pct) AS cache_occupancy_max,
		SUM(CASE WHEN cache_enabled OR cache_max_bytes > 0 THEN cache_bytes ELSE 0 END) AS cache_bytes_sum,
		SUM(CASE WHEN cache_enabled OR cache_max_bytes > 0 THEN cache_occupancy_pct ELSE 0 END) AS cache_occupancy_sum,
		SUM(CASE WHEN cache_enabled OR cache_max_bytes > 0 THEN 1 ELSE 0 END) AS cache_snapshot_count`
}

func adminBaselineCostSQLExpr(baseline adminSavingsBaselineDTO) string {
	if baseline.BaselineID == "" {
		return "0"
	}
	return fmt.Sprintf("((SUM(input_tokens) / 1000000.0) * %.12g) + ((SUM(output_tokens) / 1000000.0) * %.12g)", baseline.BaselineInputPricePerMillionUSD, baseline.BaselineOutputPricePerMillionUSD)
}

func adminScalarAggSQLOrder(sortKey string, baseline adminSavingsBaselineDTO) string {
	tie := "key ASC, secondary_key ASC"
	switch sortKey {
	case "savings":
		if baseline.BaselineID != "" {
			return "(" + adminBaselineCostSQLExpr(baseline) + " - SUM(total_cost_usd)) DESC, " + tie
		}
		return "SUM(total_cost_usd) DESC, " + tie
	case "cost":
		return "SUM(total_cost_usd) DESC, " + tie
	case "tokens":
		return "SUM(CASE WHEN total_tokens = 0 THEN input_tokens + output_tokens ELSE total_tokens END) DESC, " + tie
	case "latency":
		return "CASE WHEN COUNT(*) = 0 THEN 0 ELSE SUM(latency_ms) * 1.0 / COUNT(*) END DESC, " + tie
	case "errors":
		return "SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END) DESC, " + tie
	case "fallbacks":
		return "SUM(CASE WHEN fallback_used THEN 1 ELSE 0 END) DESC, " + tie
	case "image":
		return "SUM(input_image_count) DESC, " + tie
	case "key":
		return tie
	default:
		return "COUNT(*) DESC, " + tie
	}
}

func adminScalarAggFromSQLRecord(rec tokenScalarAggRecord, baseline adminSavingsBaselineDTO) *adminScalarAgg {
	scalar := &adminScalarAgg{
		Key:                     defaultString(rec.Key, "unknown"),
		SecondaryKey:            rec.SecondaryKey,
		Agg:                     aggFromTokenScalarAggRecord(rec),
		InputImageCount:         rec.InputImageCount,
		InputImageTokens:        rec.InputImageTokens,
		PIIFilteredRequests:     rec.PIIFilteredRequests,
		PIIFilterReplacements:   rec.PIIFilterReplacements,
		UpstreamReportedCostUSD: rec.UpstreamReportedCostUSD,
	}
	if baseline.BaselineID != "" {
		scalar.HasBaseline = true
		scalar.BaselineCostUSD = rec.BaselineCostUSD
	}
	return scalar
}

func (s *usageStore) adminShapeAggsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, sortKey string, limit int) (map[string]*adminShapeAgg, *agg, bool, error) {
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	var totalRec tokenScalarAggRecord
	if err := s.usageRowsQuery(opts).Select(adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})).Scan(&totalRec).Error; err != nil {
		return nil, nil, false, err
	}
	table := map[string]*adminShapeAgg{}
	var hasMore bool
	includeCaller := spec.ShapeReport == "overview" || spec.ShapeReport == "caller"
	includeUpstream := spec.ShapeReport == "overview" || spec.ShapeReport == "upstream" || spec.ShapeReport == "adaptive"
	if includeCaller {
		records, more, err := s.adminCallerShapeAggsSQL(opts, spec, sortKey, limitN)
		if err != nil {
			return nil, nil, false, err
		}
		hasMore = hasMore || more
		for _, rec := range records {
			adminMergeShapeSQLRecord(table, rec)
		}
	}
	if includeUpstream {
		records, more, err := s.adminUpstreamShapeAggsSQL(opts, spec, sortKey, limitN)
		if err != nil {
			return nil, nil, false, err
		}
		hasMore = hasMore || more
		for _, rec := range records {
			adminMergeShapeSQLRecord(table, rec)
		}
	}
	if len(table) > limitN {
		hasMore = true
		trimmed := adminShapeRowsFromAgg(table, sortKey, limitN)
		keep := map[string]bool{}
		for _, row := range trimmed {
			keep[joinKey(row.Key, row.SecondaryKey)] = true
		}
		for key := range table {
			if !keep[key] {
				delete(table, key)
			}
		}
	}
	total := aggFromTokenScalarAggRecord(totalRec)
	return table, &total, hasMore, nil
}

type adminShapeSQLKeySpec struct {
	keyExpr       string
	secondaryExpr string
	keyFilterExpr string
}

func (s adminShapeSQLKeySpec) groupExpr() string {
	if s.keyFilterExpr == "" {
		return s.secondaryExpr
	}
	return s.keyFilterExpr + ", " + s.secondaryExpr
}

func (s adminShapeSQLKeySpec) applyRecordFilter(q *gorm.DB, key, secondary string) *gorm.DB {
	if s.keyFilterExpr == "" {
		return q.Where(s.secondaryExpr+" = ?", secondary)
	}
	return q.Where(s.keyFilterExpr+" = ? AND "+s.secondaryExpr+" = ?", key, secondary)
}

func (s *usageStore) adminCallerShapeAggsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, sortKey string, limit int) ([]shapeAggSQLRecord, bool, error) {
	keySpec, err := adminCallerShapeSQLKeyExpr(spec)
	if err != nil {
		return nil, false, err
	}
	selectExpr := keySpec.keyExpr + ` AS key,
		` + keySpec.secondaryExpr + ` AS secondary_key,
		COUNT(*) AS requests,
		SUM(CASE WHEN traffic_shape_decision = '` + trafficShapeDecisionRejected + `' THEN 1 ELSE 0 END) AS rejected,
		SUM(CASE WHEN traffic_shape_decision = '` + trafficShapeDecisionQueued + `' THEN 1 ELSE 0 END) AS queued,
		SUM(CASE WHEN traffic_shape_retry_after_ms > 0 THEN traffic_shape_retry_after_ms ELSE 0 END) AS retry_after_ms,
		SUM(CASE WHEN traffic_shape_retry_after_ms > 0 THEN 1 ELSE 0 END) AS retry_after_count,
		MAX(CASE WHEN traffic_shape_retry_after_ms > 0 THEN traffic_shape_retry_after_ms ELSE 0 END) AS max_retry_after_ms,
		SUM(CASE WHEN traffic_shape_queue_wait_ms > 0 THEN traffic_shape_queue_wait_ms ELSE 0 END) AS queue_wait_ms,
		SUM(CASE WHEN traffic_shape_queue_wait_ms > 0 THEN 1 ELSE 0 END) AS queue_wait_count,
		MAX(CASE WHEN traffic_shape_queue_wait_ms > 0 THEN traffic_shape_queue_wait_ms ELSE 0 END) AS max_queue_wait_ms,
		SUM(traffic_shape_estimated_input_tokens) AS estimated_input,
		SUM(traffic_shape_reserved_output_tokens) AS reserved_output,
		SUM(traffic_shape_total_reserved_tokens) AS total_reserved`
	var records []shapeAggSQLRecord
	q := s.usageRowsQuery(opts).Where("traffic_shape_applied = ?", true).
		Select(selectExpr).
		Group(keySpec.groupExpr()).
		Order(adminShapeAggSQLOrder(sortKey)).
		Limit(limit + 1)
	if err := q.Scan(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	for i := range records {
		if err := s.applyCallerShapeQueuePercentiles(opts, keySpec, &records[i]); err != nil {
			return nil, false, err
		}
	}
	return records, hasMore, nil
}

func (s *usageStore) adminUpstreamShapeAggsSQL(opts UsageReportOptions, spec adminScalarEndpointSpec, sortKey string, limit int) ([]shapeAggSQLRecord, bool, error) {
	keySpec, err := adminUpstreamShapeSQLKeyExpr(spec)
	if err != nil {
		return nil, false, err
	}
	parent := s.usageRowsQuery(opts)
	q := s.db.Table("request_upstream_shape_events AS child").
		Joins("JOIN (?) AS u ON u.request_id = child.request_id", parent)
	if opts.TrafficShapeScope != "" {
		q = q.Where("child.scope = ?", opts.TrafficShapeScope)
	}
	if opts.TrafficShapeBucket != "" {
		q = q.Where("child.bucket = ?", opts.TrafficShapeBucket)
	}
	if opts.TargetProvider != "" {
		q = q.Where("child.provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("child.model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("child.dialect = ?", opts.TargetDialect)
	}
	if spec.ShapeReport == "adaptive" {
		q = q.Where("child.bucket = ?", shapeBucketBackoff)
	}
	selectExpr := keySpec.keyExpr + ` AS key,
		` + keySpec.secondaryExpr + ` AS secondary_key,
		COUNT(*) AS requests,
		SUM(CASE WHEN child.decision = '` + shapeDecisionRejected + `' THEN 1 ELSE 0 END) AS rejected,
		SUM(CASE WHEN child.decision = '` + shapeDecisionSkipped + `' THEN 1 ELSE 0 END) AS skipped_targets,
		SUM(CASE WHEN child.decision = '` + shapeDecisionCooldownStarted + `' THEN 1 ELSE 0 END) AS cooldowns_started,
		SUM(CASE WHEN child.retry_after_ms > 0 THEN child.retry_after_ms ELSE 0 END) AS retry_after_ms,
		SUM(CASE WHEN child.retry_after_ms > 0 THEN 1 ELSE 0 END) AS retry_after_count,
		MAX(CASE WHEN child.retry_after_ms > 0 THEN child.retry_after_ms ELSE 0 END) AS max_retry_after_ms,
		SUM(CASE WHEN child.queue_wait_ms > 0 THEN child.queue_wait_ms ELSE 0 END) AS queue_wait_ms,
		SUM(CASE WHEN child.queue_wait_ms > 0 THEN 1 ELSE 0 END) AS queue_wait_count,
		MAX(CASE WHEN child.queue_wait_ms > 0 THEN child.queue_wait_ms ELSE 0 END) AS max_queue_wait_ms,
		SUM(child.estimated_input_tokens) AS estimated_input,
		SUM(child.reserved_output_tokens) AS reserved_output,
		SUM(child.total_reserved_tokens) AS total_reserved,
		SUM(CASE WHEN child.backoff_reason = 'adaptive-backoff-provider-429' THEN 1 ELSE 0 END) AS upstream429,
		SUM(CASE WHEN child.backoff_reason = 'adaptive-backoff-provider-quota' THEN 1 ELSE 0 END) AS upstream_quota,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(CASE WHEN child.decision = '` + shapeDecisionSkipped + `' AND u.status < 400 THEN 1 ELSE 0 END) AS route_around_success`
	var records []shapeAggSQLRecord
	q = q.Select(selectExpr).
		Group(keySpec.groupExpr()).
		Order(adminUpstreamShapeAggSQLOrder(sortKey)).
		Limit(limit + 1)
	if err := q.Scan(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	for i := range records {
		if err := s.applyUpstreamShapeQueuePercentiles(opts, spec, keySpec, &records[i]); err != nil {
			return nil, false, err
		}
	}
	return records, hasMore, nil
}

func adminCallerShapeSQLKeyExpr(spec adminScalarEndpointSpec) (adminShapeSQLKeySpec, error) {
	keyExpr := "'caller'"
	keyFilterExpr := ""
	if spec.Dimension != "shape_surface" {
		var ok bool
		keyExpr, ok = adminScalarDimensionSQLExpr(spec.Dimension)
		if !ok {
			return adminShapeSQLKeySpec{}, fmt.Errorf("unsupported SQL caller shape dimension %q", spec.Dimension)
		}
		keyFilterExpr = keyExpr
	}
	secondaryExpr := adminSQLDefault("traffic_shape_scope", "unknown") + " || '|' || " + adminSQLDefault("traffic_shape_bucket", "unknown") + " || '|' || " + adminSQLDefault("traffic_shape_decision", "unknown")
	if spec.Secondary != "shape_bucket" {
		var ok bool
		secondaryExpr, ok = adminScalarDimensionSQLExpr(spec.Secondary)
		if !ok {
			return adminShapeSQLKeySpec{}, fmt.Errorf("unsupported SQL caller shape secondary dimension %q", spec.Secondary)
		}
	}
	return adminShapeSQLKeySpec{keyExpr: keyExpr, secondaryExpr: secondaryExpr, keyFilterExpr: keyFilterExpr}, nil
}

func adminUpstreamShapeSQLKeyExpr(spec adminScalarEndpointSpec) (adminShapeSQLKeySpec, error) {
	bucketExpr := adminSQLDefault("child.scope", "unknown") + " || '|' || " + adminSQLDefault("child.bucket", "unknown") + " || '|' || " + adminSQLDefault("child.decision", "unknown")
	switch spec.Dimension {
	case "shape_surface":
		return adminShapeSQLKeySpec{keyExpr: "'provider/model'", secondaryExpr: bucketExpr}, nil
	case "provider_model":
		keyExpr := adminSQLDefault("child.provider", "unknown") + " || '/' || " + adminSQLDefault("child.model", "unknown")
		return adminShapeSQLKeySpec{keyExpr: keyExpr, secondaryExpr: bucketExpr, keyFilterExpr: keyExpr}, nil
	case "backoff_reason":
		keyExpr := adminSQLDefault("child.backoff_reason", "adaptive-backoff")
		return adminShapeSQLKeySpec{keyExpr: keyExpr, secondaryExpr: adminSQLDefault("child.provider", "unknown") + " || '|' || " + adminSQLDefault("child.model", "unknown"), keyFilterExpr: keyExpr}, nil
	default:
		keyExpr, ok := adminScalarDimensionSQLExprForAlias(spec.Dimension, "u")
		if !ok {
			return adminShapeSQLKeySpec{}, fmt.Errorf("unsupported SQL upstream shape dimension %q", spec.Dimension)
		}
		return adminShapeSQLKeySpec{keyExpr: keyExpr, secondaryExpr: bucketExpr, keyFilterExpr: keyExpr}, nil
	}
}

func adminShapeAggSQLOrder(sortKey string) string {
	tie := "key ASC, secondary_key ASC"
	switch sortKey {
	case "errors":
		return "SUM(CASE WHEN traffic_shape_decision = '" + trafficShapeDecisionRejected + "' THEN 1 ELSE 0 END) DESC, " + tie
	case "key":
		return tie
	default:
		return "COUNT(*) DESC, " + tie
	}
}

func adminUpstreamShapeAggSQLOrder(sortKey string) string {
	tie := "key ASC, secondary_key ASC"
	switch sortKey {
	case "errors":
		return "SUM(CASE WHEN child.decision = '" + shapeDecisionRejected + "' THEN 1 ELSE 0 END) DESC, " + tie
	case "key":
		return tie
	default:
		return "COUNT(*) DESC, " + tie
	}
}

func (s *usageStore) applyCallerShapeQueuePercentiles(opts UsageReportOptions, keySpec adminShapeSQLKeySpec, rec *shapeAggSQLRecord) error {
	q := s.usageRowsQuery(opts).Where("traffic_shape_applied = ? AND traffic_shape_queue_wait_ms > 0", true).
		Scopes(func(q *gorm.DB) *gorm.DB {
			return keySpec.applyRecordFilter(q, rec.Key, rec.SecondaryKey)
		})
	p50, p95, err := s.shapeQueuePercentiles(q, "traffic_shape_queue_wait_ms")
	if err != nil {
		return err
	}
	rec.P50QueueWaitMS = p50
	rec.P95QueueWaitMS = p95
	return nil
}

func (s *usageStore) applyUpstreamShapeQueuePercentiles(opts UsageReportOptions, spec adminScalarEndpointSpec, keySpec adminShapeSQLKeySpec, rec *shapeAggSQLRecord) error {
	parent := s.usageRowsQuery(opts)
	q := s.db.Table("request_upstream_shape_events AS child").
		Joins("JOIN (?) AS u ON u.request_id = child.request_id", parent).
		Where("child.queue_wait_ms > 0").
		Scopes(func(q *gorm.DB) *gorm.DB {
			return keySpec.applyRecordFilter(q, rec.Key, rec.SecondaryKey)
		})
	if opts.TrafficShapeScope != "" {
		q = q.Where("child.scope = ?", opts.TrafficShapeScope)
	}
	if opts.TrafficShapeBucket != "" {
		q = q.Where("child.bucket = ?", opts.TrafficShapeBucket)
	}
	if opts.TargetProvider != "" {
		q = q.Where("child.provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("child.model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("child.dialect = ?", opts.TargetDialect)
	}
	if spec.ShapeReport == "adaptive" {
		q = q.Where("child.bucket = ?", shapeBucketBackoff)
	}
	p50, p95, err := s.shapeQueuePercentiles(q, "child.queue_wait_ms")
	if err != nil {
		return err
	}
	rec.P50QueueWaitMS = p50
	rec.P95QueueWaitMS = p95
	return nil
}

func (s *usageStore) shapeQueuePercentiles(q *gorm.DB, column string) (int64, int64, error) {
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, 0, err
	}
	if count == 0 {
		return 0, 0, nil
	}
	percentileOffset := func(percentile int) int {
		rank := int(math.Ceil(float64(percentile)/100*float64(count))) - 1
		if rank < 0 {
			return 0
		}
		if int64(rank) >= count {
			return int(count - 1)
		}
		return rank
	}
	valueAt := func(offset int) (int64, error) {
		var value int64
		err := q.Session(&gorm.Session{}).Select(column).Order(column + " ASC").Limit(1).Offset(offset).Scan(&value).Error
		return value, err
	}
	p50, err := valueAt(percentileOffset(50))
	if err != nil {
		return 0, 0, err
	}
	p95, err := valueAt(percentileOffset(95))
	if err != nil {
		return 0, 0, err
	}
	return p50, p95, nil
}

func adminMergeShapeSQLRecord(table map[string]*adminShapeAgg, rec shapeAggSQLRecord) {
	row := adminShapeAggFor(table, rec.Key, rec.SecondaryKey)
	row.Agg.Requests += rec.Requests
	row.Agg.Rejected += rec.Rejected
	row.Agg.Queued += rec.Queued
	row.Agg.SkippedTargets += rec.SkippedTargets
	row.Agg.CooldownsStarted += rec.CooldownsStarted
	row.Agg.RetryAfterMS += rec.RetryAfterMS
	row.Agg.RetryAfterCount += rec.RetryAfterCount
	row.Agg.MaxRetryAfterMS = adminMaxInt64(row.Agg.MaxRetryAfterMS, rec.MaxRetryAfterMS)
	row.Agg.QueueWaitMS += rec.QueueWaitMS
	row.Agg.QueueWaitCount += rec.QueueWaitCount
	row.Agg.MaxQueueWaitMS = adminMaxInt64(row.Agg.MaxQueueWaitMS, rec.MaxQueueWaitMS)
	row.Agg.QueueWaitSamples = mergeShapePercentileSamples(row.Agg.QueueWaitSamples, rec.P50QueueWaitMS, rec.P95QueueWaitMS)
	row.Agg.EstimatedInput += rec.EstimatedInput
	row.Agg.ReservedOutput += rec.ReservedOutput
	row.Agg.TotalReserved += rec.TotalReserved
	row.Agg.Upstream429 += rec.Upstream429
	row.Agg.UpstreamQuota += rec.UpstreamQuota
	row.Agg.Fallbacks += rec.Fallbacks
	row.Agg.RouteAroundSuccess += rec.RouteAroundSuccess
}

func mergeShapePercentileSamples(existing []int64, p50, p95 int64) []int64 {
	if p50 > 0 {
		existing = append(existing, p50)
	}
	if p95 > 0 && p95 != p50 {
		existing = append(existing, p95)
	}
	return existing
}

func adminScalarDimensionSQLExpr(dimension string) (string, bool) {
	return adminScalarDimensionSQLExprForAlias(dimension, "")
}

func adminScalarDimensionSQLExprForAlias(dimension, alias string) (string, bool) {
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	def := func(name, fallback string) string {
		return adminSQLDefault(col(name), fallback)
	}
	switch dimension {
	case "":
		return "''", true
	case "caller_id":
		return def("caller_id", "unknown"), true
	case "caller_user":
		return def("caller_user", "unknown"), true
	case "token_id":
		return def("token_id", "unknown"), true
	case "requested_model":
		return def("requested_model", "unknown"), true
	case "model_group":
		return "COALESCE(NULLIF(" + col("resolved_group") + ", ''), NULLIF(" + col("requested_model") + ", ''), 'unknown')", true
	case "provider_model":
		return def("target_provider", "unknown") + " || '/' || " + def("target_model", "unknown"), true
	case "dialect":
		return def("target_dialect", "unknown"), true
	case "client":
		return def("client", "unknown"), true
	case "inbound_dialect":
		return def("inbound_dialect", "unknown"), true
	case "status_error":
		return "CAST(" + col("status") + " AS TEXT) || '/' || CASE WHEN " + col("status") + " >= 400 THEN " + def("error", "error") + " ELSE 'ok' END", true
	case "cache":
		return def("cache", "bypass"), true
	case "quota_key_state":
		return def("quota_state", "unknown") + " || '/' || " + def("key_state", "unknown"), true
	case "routing_decision":
		return def("strategy", "unknown") + " || '/' || COALESCE(NULLIF(" + col("resolved_group") + ", ''), NULLIF(" + col("requested_model") + ", ''), 'unknown')", true
	case "contract_bucket":
		return "CASE WHEN " + col("contract_present") + " THEN " + def("contract_bucket", "unknown") + " ELSE 'none' END", true
	case "contract_workload":
		return "CASE WHEN " + col("contract_present") + " THEN " + def("contract_workload", "unspecified") + " ELSE 'none' END", true
	case "target_validation":
		return def("target_validation_status", "missing") + " || '/' || " + def("target_validation_age_bucket", "missing"), true
	case "project":
		return def("caller_project", "unknown"), true
	case "environment":
		return def("caller_environment", "unknown"), true
	default:
		return "", false
	}
}

func adminSQLDefault(column, fallback string) string {
	return "COALESCE(NULLIF(" + column + ", ''), '" + fallback + "')"
}

func aggFromTokenScalarAggRecord(rec tokenScalarAggRecord) agg {
	return agg{
		Calls:                    rec.Calls,
		Errors:                   rec.Errors,
		Streams:                  rec.Streams,
		CacheHits:                rec.CacheHits,
		CacheMisses:              rec.CacheMisses,
		CacheBypass:              rec.CacheBypass,
		Fallbacks:                rec.Fallbacks,
		Attempts:                 rec.Attempts,
		InputTokens:              rec.InputTokens,
		OutputTokens:             rec.OutputTokens,
		TotalTokens:              rec.TotalTokens,
		InputCostUSD:             rec.InputCostUSD,
		ImageCostUSD:             rec.ImageCostUSD,
		OutputCostUSD:            rec.OutputCostUSD,
		TotalCostUSD:             rec.TotalCostUSD,
		LatencyMS:                rec.LatencyMS,
		MaxLatencyMS:             rec.MaxLatencyMS,
		TTFBMS:                   rec.TTFBMS,
		TTFBCount:                rec.TTFBCount,
		MaxTTFBMS:                rec.MaxTTFBMS,
		UpstreamMS:               rec.UpstreamMS,
		UpstreamMSCount:          rec.UpstreamMSCount,
		MaxUpstreamMS:            rec.MaxUpstreamMS,
		DownstreamMS:             rec.DownstreamMS,
		DownstreamMSCount:        rec.DownstreamMSCount,
		MaxDownstreamMS:          rec.MaxDownstreamMS,
		UpstreamOutputTPS:        rec.UpstreamOutputTPS,
		UpstreamOutputTPSCount:   rec.UpstreamOutputTPSCount,
		UpstreamTotalTPS:         rec.UpstreamTotalTPS,
		UpstreamTotalTPSCount:    rec.UpstreamTotalTPSCount,
		DownstreamOutputTPS:      rec.DownstreamOutputTPS,
		DownstreamOutputTPSCount: rec.DownstreamOutputTPSCount,
		DownstreamTotalTPS:       rec.DownstreamTotalTPS,
		DownstreamTotalTPSCount:  rec.DownstreamTotalTPSCount,
		CacheItemsLatest:         rec.CacheItemsMax,
		CacheBytesLatest:         rec.CacheBytesMax,
		CacheMaxBytesLatest:      rec.CacheMaxBytesLatest,
		CacheOccupancyLatest:     rec.CacheOccupancyMax,
		CacheItemsMax:            rec.CacheItemsMax,
		CacheBytesMax:            rec.CacheBytesMax,
		CacheOccupancyMax:        rec.CacheOccupancyMax,
		CacheBytesSum:            rec.CacheBytesSum,
		CacheOccupancySum:        rec.CacheOccupancySum,
		CacheSnapshotCount:       rec.CacheSnapshotCount,
	}
}

func (s *usageStore) adminDiagnosticAggsSQL(opts UsageReportOptions, diagnostic, sortKey string, limit int) (map[string]*adminDiagnosticAgg, *agg, bool, error) {
	parentOpts := adminDiagnosticParentOptions(opts, diagnostic)
	var totalRec tokenScalarAggRecord
	if err := s.usageRowsQuery(parentOpts).Select(adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})).Scan(&totalRec).Error; err != nil {
		return nil, nil, false, err
	}
	table := map[string]*adminDiagnosticAgg{}
	limitN := limit
	if limitN <= 0 {
		limitN = 50
	}
	var err error
	switch diagnostic {
	case "upstream_failures":
		err = s.adminUpstreamFailureAggsSQL(table, parentOpts, opts, sortKey, limitN)
	case "request_shape_mismatches":
		err = s.adminRequestShapeMismatchAggsSQL(table, parentOpts, opts, sortKey, limitN)
	case "fallback_health":
		err = s.adminFallbackHealthAggsSQL(table, parentOpts, opts, sortKey, limitN)
	case "client_impact":
		err = s.adminClientImpactAggsSQL(table, parentOpts, opts, sortKey, limitN)
	}
	if err != nil {
		return nil, nil, false, err
	}
	total := aggFromTokenScalarAggRecord(totalRec)
	return table, &total, len(table) > limitN, nil
}

func (s *usageStore) adminUpstreamFailureAggsSQL(table map[string]*adminDiagnosticAgg, parentOpts, opts UsageReportOptions, sortKey string, limit int) error {
	attempts := s.db.Table("request_attempts AS child").
		Joins("JOIN (?) AS u ON u.request_id = child.request_id", s.usageRowsQuery(parentOpts)).
		Where("(child.status_code >= 400 OR child.error_class <> '' OR child.timed_out = ? OR child.client_canceled = ?)", true, true)
	attempts = applyAdminAttemptDiagnosticFilters(attempts, opts)
	selectExpr := `
		(COALESCE(NULLIF(child.error_class, ''), CASE
			WHEN child.timed_out THEN 'upstream_timeout'
			WHEN child.client_canceled THEN 'client_canceled'
			WHEN child.status_code >= 500 THEN 'upstream_5xx'
			WHEN child.status_code >= 400 THEN 'upstream_4xx'
			ELSE 'upstream_error'
		END) || '|' || CASE WHEN child.status_code = 0 THEN 'status:none' ELSE 'status:' || CAST(child.status_code AS TEXT) END) AS key,
		(COALESCE(NULLIF(child.provider, ''), 'unknown') || '|' || COALESCE(NULLIF(child.model, ''), 'unknown') || '|' || COALESCE(NULLIF(child.dialect, ''), 'unknown')) AS secondary_key,
		child.status_code AS status,
		COALESCE(NULLIF(child.error_class, ''), CASE
			WHEN child.timed_out THEN 'upstream_timeout'
			WHEN child.client_canceled THEN 'client_canceled'
			WHEN child.status_code >= 500 THEN 'upstream_5xx'
			WHEN child.status_code >= 400 THEN 'upstream_4xx'
			ELSE 'upstream_error'
		END) AS error_class,
		child.provider AS provider,
		child.model AS model,
		child.dialect AS dialect,
		COUNT(DISTINCT u.request_id) AS requests,
		COUNT(*) AS errors,
		COUNT(*) AS attempts,
		SUM(CASE WHEN child.retryable THEN 1 ELSE 0 END) AS retryable_attempts,
		SUM(CASE WHEN child.timed_out THEN 1 ELSE 0 END) AS timeout_attempts,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		COUNT(DISTINCT CASE WHEN u.caller_user <> '' THEN u.caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN u.client <> '' THEN u.client END) AS affected_clients,
		SUM(u.latency_ms) AS latency_ms,
		SUM(COALESCE(u.upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(u.upstream_duration_ms) AS upstream_ms_count,
		MAX(u.latency_ms) AS max_latency_ms,
		MAX(COALESCE(u.upstream_duration_ms, 0)) AS max_upstream_ms`
	groupBy := `COALESCE(NULLIF(child.error_class, ''), CASE
			WHEN child.timed_out THEN 'upstream_timeout'
			WHEN child.client_canceled THEN 'client_canceled'
			WHEN child.status_code >= 500 THEN 'upstream_5xx'
			WHEN child.status_code >= 400 THEN 'upstream_4xx'
			ELSE 'upstream_error'
		END), child.status_code, child.provider, child.model, child.dialect`
	var attemptRecords []diagnosticAggSQLRecord
	if err := attempts.Select(selectExpr).Group(groupBy).Order(adminDiagnosticAggSQLOrder(sortKey)).Limit(limit + 1).Scan(&attemptRecords).Error; err != nil {
		return err
	}
	adminMergeDiagnosticSQLRecords(table, attemptRecords)

	details := s.db.Table("request_upstream_error_details AS child").
		Joins("JOIN (?) AS u ON u.request_id = child.request_id", s.usageRowsQuery(parentOpts))
	details = applyAdminUpstreamDetailDiagnosticFilters(details, opts)
	var detailRecords []diagnosticAggSQLRecord
	if err := details.Select(`
		(COALESCE(NULLIF(child.error_class, ''), 'unknown') || '|field:' || COALESCE(NULLIF(child.field_name, ''), 'unknown')) AS key,
		COALESCE(NULLIF(child.source, ''), 'upstream') AS secondary_key,
		child.status_code AS status,
		child.error_class AS error_class,
		COUNT(DISTINCT u.request_id) AS requests,
		COUNT(*) AS upstream_error_details,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		COUNT(DISTINCT CASE WHEN u.caller_user <> '' THEN u.caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN u.client <> '' THEN u.client END) AS affected_clients,
		SUM(u.latency_ms) AS latency_ms,
		SUM(COALESCE(u.upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(u.upstream_duration_ms) AS upstream_ms_count,
		MAX(u.latency_ms) AS max_latency_ms,
		MAX(COALESCE(u.upstream_duration_ms, 0)) AS max_upstream_ms`).
		Group("child.error_class, child.field_name, child.source, child.status_code").
		Order(adminDiagnosticAggSQLOrder(sortKey)).
		Limit(limit + 1).
		Scan(&detailRecords).Error; err != nil {
		return err
	}
	adminMergeDiagnosticSQLRecords(table, detailRecords)
	return nil
}

func (s *usageStore) adminRequestShapeMismatchAggsSQL(table map[string]*adminDiagnosticAgg, parentOpts, opts UsageReportOptions, sortKey string, limit int) error {
	translationSelect := func(key string) string {
		return fmt.Sprintf(`'%s' AS key,
		(COALESCE(NULLIF(child.provider, ''), 'unknown') || '|' || COALESCE(NULLIF(child.model, ''), 'unknown') || '|' || COALESCE(NULLIF(child.dialect, ''), 'unknown')) AS secondary_key,
		child.provider AS provider,
		child.model AS model,
		child.dialect AS dialect,
		COALESCE(NULLIF(child.request_shape_fingerprint, ''), shape.request_shape_fingerprint) AS request_shape_fingerprint,
		COALESCE(NULLIF(child.tool_schema_fingerprint, ''), shape.tool_schema_fingerprint) AS tool_schema_fingerprint,
		COUNT(DISTINCT u.request_id) AS requests,
		SUM(CASE WHEN child.unsupported_fields_present THEN 1 ELSE 0 END) AS errors,
		COUNT(*) AS attempts,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(child.fields_stripped_count) AS fields_stripped,
		SUM(child.fields_rewritten_count) AS fields_rewritten,
		SUM(CASE WHEN child.unsupported_fields_present THEN 1 ELSE 0 END) AS unsupported_fields,
		SUM(child.translation_warning_count) AS translation_warnings,
		COUNT(DISTINCT CASE WHEN u.caller_user <> '' THEN u.caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN u.client <> '' THEN u.client END) AS affected_clients,
		SUM(u.latency_ms) AS latency_ms,
		SUM(COALESCE(u.upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(u.upstream_duration_ms) AS upstream_ms_count,
		MAX(u.latency_ms) AS max_latency_ms,
		MAX(COALESCE(u.upstream_duration_ms, 0)) AS max_upstream_ms`, key)
	}
	translationGroup := "child.provider, child.model, child.dialect, COALESCE(NULLIF(child.request_shape_fingerprint, ''), shape.request_shape_fingerprint), COALESCE(NULLIF(child.tool_schema_fingerprint, ''), shape.tool_schema_fingerprint)"
	translationConditions := []struct {
		key       string
		condition string
	}{
		{"unsupported-fields", "child.unsupported_fields_present"},
		{"fields-stripped", "child.fields_stripped_count > 0"},
		{"fields-rewritten", "child.fields_rewritten_count > 0"},
		{"translation-warning", "child.translation_warning_count > 0"},
		{"tool-count-changed", "shape.request_id IS NOT NULL AND shape.tool_count <> child.translated_tool_count"},
		{"tool-choice-changed", "shape.tool_choice_mode <> '' AND child.translated_tool_choice_mode <> '' AND shape.tool_choice_mode <> child.translated_tool_choice_mode"},
		{"output-cap-field-changed", "shape.requested_output_cap_field <> '' AND child.translated_output_cap_field <> '' AND shape.requested_output_cap_field <> child.translated_output_cap_field"},
		{"output-cap-bucket-changed", "shape.requested_output_cap_bucket <> '' AND child.translated_output_cap_bucket <> '' AND shape.requested_output_cap_bucket <> child.translated_output_cap_bucket"},
		{"reasoning-control-translated", "shape.reasoning_present AND child.translated_reasoning_control <> ''"},
		{"request-bytes-bucket-changed", "shape.total_request_bytes_bucket <> '' AND child.translated_request_bytes_bucket <> '' AND shape.total_request_bytes_bucket <> child.translated_request_bytes_bucket"},
	}
	noMismatchCondition := `NOT (
		child.unsupported_fields_present
		OR child.fields_stripped_count > 0
		OR child.fields_rewritten_count > 0
		OR child.translation_warning_count > 0
		OR COALESCE(shape.request_id IS NOT NULL AND shape.tool_count <> child.translated_tool_count, false)
		OR COALESCE(shape.tool_choice_mode <> '' AND child.translated_tool_choice_mode <> '' AND shape.tool_choice_mode <> child.translated_tool_choice_mode, false)
		OR COALESCE(shape.requested_output_cap_field <> '' AND child.translated_output_cap_field <> '' AND shape.requested_output_cap_field <> child.translated_output_cap_field, false)
		OR COALESCE(shape.requested_output_cap_bucket <> '' AND child.translated_output_cap_bucket <> '' AND shape.requested_output_cap_bucket <> child.translated_output_cap_bucket, false)
		OR COALESCE(shape.reasoning_present AND child.translated_reasoning_control <> '', false)
		OR COALESCE(shape.total_request_bytes_bucket <> '' AND child.translated_request_bytes_bucket <> '' AND shape.total_request_bytes_bucket <> child.translated_request_bytes_bucket, false)
	)`
	translationConditions = append(translationConditions, struct {
		key       string
		condition string
	}{"translated-without-warning", noMismatchCondition})
	for _, condition := range translationConditions {
		translations := s.db.Table("request_translation_shapes AS child").
			Joins("JOIN (?) AS u ON u.request_id = child.request_id", s.usageRowsQuery(parentOpts)).
			Joins("LEFT JOIN request_shapes AS shape ON shape.request_id = child.request_id").
			Where(condition.condition)
		translations = applyAdminTranslationDiagnosticFilters(translations, opts)
		var records []diagnosticAggSQLRecord
		if err := translations.Select(translationSelect(condition.key)).
			Group(translationGroup).
			Order(adminDiagnosticAggSQLOrder(sortKey)).
			Limit(limit + 1).
			Scan(&records).Error; err != nil {
			return err
		}
		adminMergeDiagnosticSQLRecords(table, records)
	}

	events := s.db.Table("request_translation_field_events AS child").
		Joins("JOIN (?) AS u ON u.request_id = child.request_id", s.usageRowsQuery(parentOpts)).
		Joins("LEFT JOIN request_shapes AS shape ON shape.request_id = child.request_id")
	var eventRecords []diagnosticAggSQLRecord
	if err := events.Select(`
		('field|' || COALESCE(NULLIF(child.field_name, ''), 'other') || '|' || COALESCE(NULLIF(child.action, ''), 'unknown')) AS key,
		COALESCE(NULLIF(child.reason, ''), 'unspecified') AS secondary_key,
		shape.request_shape_fingerprint AS request_shape_fingerprint,
		shape.tool_schema_fingerprint AS tool_schema_fingerprint,
		COUNT(DISTINCT u.request_id) AS requests,
		SUM(CASE WHEN child.action = 'unsupported' THEN 1 ELSE 0 END) AS errors,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(CASE WHEN child.action = 'stripped' THEN 1 ELSE 0 END) AS fields_stripped,
		SUM(CASE WHEN child.action = 'rewritten' THEN 1 ELSE 0 END) AS fields_rewritten,
		SUM(CASE WHEN child.action = 'unsupported' THEN 1 ELSE 0 END) AS unsupported_fields,
		COUNT(DISTINCT CASE WHEN u.caller_user <> '' THEN u.caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN u.client <> '' THEN u.client END) AS affected_clients,
		SUM(u.latency_ms) AS latency_ms,
		SUM(COALESCE(u.upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(u.upstream_duration_ms) AS upstream_ms_count,
		MAX(u.latency_ms) AS max_latency_ms,
		MAX(COALESCE(u.upstream_duration_ms, 0)) AS max_upstream_ms`).
		Group("child.field_name, child.action, child.reason, shape.request_shape_fingerprint, shape.tool_schema_fingerprint").
		Order(adminDiagnosticAggSQLOrder(sortKey)).
		Limit(limit + 1).
		Scan(&eventRecords).Error; err != nil {
		return err
	}
	adminMergeDiagnosticSQLRecords(table, eventRecords)
	return nil
}

func (s *usageStore) adminFallbackHealthAggsSQL(table map[string]*adminDiagnosticAgg, parentOpts, opts UsageReportOptions, sortKey string, limit int) error {
	transitions := s.db.Table("request_fallback_transitions AS child").
		Joins("JOIN (?) AS u ON u.request_id = child.request_id", s.usageRowsQuery(parentOpts))
	transitions = applyAdminFallbackDiagnosticFilters(transitions, opts)
	var records []diagnosticAggSQLRecord
	if err := transitions.Select(`
		(COALESCE(NULLIF(child.fallback_reason, ''), 'fallback') || '|' || COALESCE(NULLIF(child.error_class, ''), 'unknown')) AS key,
		(COALESCE(NULLIF(child.failed_provider, ''), 'unknown') || '|' || COALESCE(NULLIF(child.failed_model, ''), 'unknown') || '|to|' || COALESCE(NULLIF(child.fallback_provider, ''), 'unknown') || '|' || COALESCE(NULLIF(child.fallback_model, ''), 'unknown')) AS secondary_key,
		child.error_class AS error_class,
		child.failed_provider AS provider,
		child.failed_model AS model,
		child.failed_dialect AS dialect,
		COUNT(DISTINCT u.request_id) AS requests,
		COUNT(*) AS errors,
		COUNT(*) AS attempts,
		SUM(CASE WHEN child.retryable THEN 1 ELSE 0 END) AS retryable_attempts,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(CASE WHEN child.fallback_succeeded THEN 1 ELSE 0 END) AS fallback_succeeded,
		SUM(CASE WHEN child.fallback_succeeded THEN 0 ELSE 1 END) AS fallback_failed,
		COUNT(DISTINCT CASE WHEN u.caller_user <> '' THEN u.caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN u.client <> '' THEN u.client END) AS affected_clients,
		SUM(u.latency_ms) AS latency_ms,
		SUM(COALESCE(u.upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(u.upstream_duration_ms) AS upstream_ms_count,
		MAX(u.latency_ms) AS max_latency_ms,
		MAX(COALESCE(u.upstream_duration_ms, 0)) AS max_upstream_ms`).
		Group("child.fallback_reason, child.error_class, child.failed_provider, child.failed_model, child.failed_dialect, child.fallback_provider, child.fallback_model").
		Order(adminDiagnosticAggSQLOrder(sortKey)).
		Limit(limit + 1).
		Scan(&records).Error; err != nil {
		return err
	}
	adminMergeDiagnosticSQLRecords(table, records)

	parentOnly := s.usageRowsQuery(parentOpts).
		Where("(fallback_used = ? OR attempts > 1)", true).
		Where("request_id NOT IN (SELECT request_id FROM request_fallback_transitions)")
	if opts.Status != 0 {
		parentOnly = parentOnly.Where("status = ?", opts.Status)
	}
	if opts.TargetProvider != "" {
		parentOnly = parentOnly.Where("target_provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		parentOnly = parentOnly.Where("target_model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		parentOnly = parentOnly.Where("target_dialect = ?", opts.TargetDialect)
	}
	var parentRecords []diagnosticAggSQLRecord
	if err := parentOnly.Select(`
		(COALESCE(NULLIF(resolved_group, ''), NULLIF(requested_model, ''), 'unknown') || '|' || CASE
			WHEN fallback_used AND status < 400 THEN 'fallback:recovered'
			WHEN fallback_used THEN 'fallback:failed'
			ELSE 'fallback:not-used'
		END || '|attempts:' || CAST(attempts AS TEXT) || '|status:' || CAST(status AS TEXT)) AS key,
		(COALESCE(NULLIF(target_provider, ''), 'unknown') || '/' || COALESCE(NULLIF(target_model, ''), 'unknown')) AS secondary_key,
		status AS status,
		target_provider AS provider,
		target_model AS model,
		target_dialect AS dialect,
		COUNT(DISTINCT request_id) AS requests,
		SUM(CASE WHEN fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(CASE WHEN fallback_used AND status < 400 THEN 1 ELSE 0 END) AS fallback_succeeded,
		SUM(CASE WHEN fallback_used AND status >= 400 THEN 1 ELSE 0 END) AS fallback_failed,
		COUNT(DISTINCT CASE WHEN caller_user <> '' THEN caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN client <> '' THEN client END) AS affected_clients,
		SUM(latency_ms) AS latency_ms,
		SUM(COALESCE(upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(upstream_duration_ms) AS upstream_ms_count,
		MAX(latency_ms) AS max_latency_ms,
		MAX(COALESCE(upstream_duration_ms, 0)) AS max_upstream_ms`).
		Group(`COALESCE(NULLIF(resolved_group, ''), NULLIF(requested_model, ''), 'unknown'), fallback_used, attempts, status, target_provider, target_model, target_dialect`).
		Order(adminDiagnosticAggSQLOrder(sortKey)).
		Limit(limit + 1).
		Scan(&parentRecords).Error; err != nil {
		return err
	}
	adminMergeDiagnosticSQLRecords(table, parentRecords)
	return nil
}

func (s *usageStore) adminClientImpactAggsSQL(table map[string]*adminDiagnosticAgg, parentOpts, opts UsageReportOptions, sortKey string, limit int) error {
	q := s.db.Table("(?) AS u", s.usageRowsQuery(parentOpts)).
		Joins("LEFT JOIN request_errors AS err ON err.request_id = u.request_id")
	if opts.TargetProvider != "" {
		q = q.Where("err.provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("err.model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("err.dialect = ?", opts.TargetDialect)
	}
	if opts.Status != 0 {
		q = q.Where("err.status = ? OR u.status = ?", opts.Status, opts.Status)
	}
	if opts.ErrorClass != "" {
		q = q.Where("err.error_class = ?", opts.ErrorClass)
	}
	var records []diagnosticAggSQLRecord
	if err := q.Select(`
		(COALESCE(NULLIF(u.client, ''), 'unknown') || '|' || COALESCE(NULLIF(u.caller_user, ''), 'unknown')) AS key,
		(COALESCE(NULLIF(u.caller_project, ''), 'unknown') || '|' || COALESCE(NULLIF(u.caller_environment, ''), 'unknown') || '|' || COALESCE(NULLIF(u.resolved_group, ''), NULLIF(u.requested_model, ''), 'unknown')) AS secondary_key,
		u.status AS status,
		COALESCE(err.error_class, '') AS error_class,
		u.target_provider AS provider,
		u.target_model AS model,
		u.target_dialect AS dialect,
		COUNT(DISTINCT u.request_id) AS requests,
		SUM(CASE WHEN u.status >= 400 THEN 1 ELSE 0 END) AS errors,
		SUM(u.attempts) AS attempts,
		SUM(CASE WHEN u.fallback_used THEN 1 ELSE 0 END) AS fallbacks,
		SUM(CASE WHEN err.request_id IS NOT NULL THEN 1 ELSE 0 END) AS terminal_errors,
		COUNT(DISTINCT CASE WHEN u.caller_user <> '' THEN u.caller_user END) AS affected_users,
		COUNT(DISTINCT CASE WHEN u.client <> '' THEN u.client END) AS affected_clients,
		SUM(u.latency_ms) AS latency_ms,
		SUM(COALESCE(u.upstream_duration_ms, 0)) AS upstream_ms,
		COUNT(u.upstream_duration_ms) AS upstream_ms_count,
		MAX(u.latency_ms) AS max_latency_ms,
		MAX(COALESCE(u.upstream_duration_ms, 0)) AS max_upstream_ms`).
		Group("u.client, u.caller_user, u.caller_project, u.caller_environment, COALESCE(NULLIF(u.resolved_group, ''), NULLIF(u.requested_model, ''), 'unknown'), u.status, err.error_class, u.target_provider, u.target_model, u.target_dialect").
		Order(adminDiagnosticAggSQLOrder(sortKey)).
		Limit(limit + 1).
		Scan(&records).Error; err != nil {
		return err
	}
	adminMergeDiagnosticSQLRecords(table, records)
	return nil
}

func adminMergeDiagnosticSQLRecords(table map[string]*adminDiagnosticAgg, records []diagnosticAggSQLRecord) {
	for _, rec := range records {
		agg := adminDiagnosticAggFor(table, rec.Key, rec.SecondaryKey)
		agg.Status = rec.Status
		agg.ErrorClass = rec.ErrorClass
		agg.Provider = rec.Provider
		agg.Model = rec.Model
		agg.Dialect = rec.Dialect
		agg.RequestShapeFingerprint = rec.RequestShapeFingerprint
		agg.ToolSchemaFingerprint = rec.ToolSchemaFingerprint
		for i := int64(0); i < rec.Requests; i++ {
			agg.RequestIDs[fmt.Sprintf("sql:%s:%s:%d", rec.Key, rec.SecondaryKey, i)] = true
		}
		for i := int64(0); i < rec.AffectedUsers; i++ {
			agg.Users[fmt.Sprintf("sql:%s:%d", rec.Key, i)] = true
		}
		for i := int64(0); i < rec.AffectedClients; i++ {
			agg.Clients[fmt.Sprintf("sql:%s:%d", rec.SecondaryKey, i)] = true
		}
		agg.Errors += rec.Errors
		agg.Attempts += rec.Attempts
		agg.RetryableAttempts += rec.RetryableAttempts
		agg.TimeoutAttempts += rec.TimeoutAttempts
		agg.Fallbacks += rec.Fallbacks
		agg.FallbackSucceeded += rec.FallbackSucceeded
		agg.FallbackFailed += rec.FallbackFailed
		agg.TerminalErrors += rec.TerminalErrors
		agg.UpstreamErrorDetails += rec.UpstreamErrorDetails
		agg.FieldsStripped += rec.FieldsStripped
		agg.FieldsRewritten += rec.FieldsRewritten
		agg.UnsupportedFields += rec.UnsupportedFields
		agg.TranslationWarnings += rec.TranslationWarnings
		agg.LatencyMS += rec.LatencyMS
		agg.UpstreamMS += rec.UpstreamMS
		agg.UpstreamMSCount += rec.UpstreamMSCount
		agg.MaxLatencyMS = adminMaxInt64(agg.MaxLatencyMS, rec.MaxLatencyMS)
		agg.MaxUpstreamMS = adminMaxInt64(agg.MaxUpstreamMS, rec.MaxUpstreamMS)
	}
}

func adminDiagnosticAggSQLOrder(sortKey string) string {
	return "1 ASC, 2 ASC"
}

func (s *usageStore) adminUsageRowsPage(opts adminUsagePageOptions) (adminUsagePage, error) {
	base := s.usageRowsQuery(opts.UsageReportOptions)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return adminUsagePage{}, err
	}
	q := s.usageRowsQuery(opts.UsageReportOptions)
	var err error
	if opts.Cursor != nil {
		q, err = applyUsagePageCursor(q, opts.Sort, opts.Direction, opts.Cursor)
		if err != nil {
			return adminUsagePage{}, err
		}
	}
	var records []usageRecord
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	if err := q.Order(usagePageOrder(opts.Sort, opts.Direction)).Limit(limit + 1).Find(&records).Error; err != nil {
		return adminUsagePage{}, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	rows, err := usageRowsFromRecords(records)
	if err != nil {
		return adminUsagePage{}, err
	}
	s.loadUsageReportBuckets(rows)
	return adminUsagePage{Rows: rows, TotalCount: total, HasMore: hasMore}, nil
}

func usageRowsFromRecords(records []usageRecord) ([]usageRow, error) {
	out := make([]usageRow, 0, len(records))
	for _, record := range records {
		row, err := rowFromUsageRecord(record)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *usageStore) usageRowsQuery(opts UsageReportOptions) *gorm.DB {
	q := s.db.Model(&usageRecord{}).Where("ts >= ? AND ts < ?", formatUsageTime(opts.From), formatUsageTime(opts.To))
	if opts.TokenID != "" {
		q = q.Where("token_id = ?", opts.TokenID)
	}
	if opts.TokenIDPrefix != "" {
		q = q.Where("token_id LIKE ?", opts.TokenIDPrefix+"%")
	}
	if opts.CallerID != "" {
		q = q.Where("caller_id = ?", opts.CallerID)
	}
	if opts.CallerIP != "" {
		q = q.Where("caller_ip = ?", opts.CallerIP)
	}
	if opts.CallerUser != "" {
		q = q.Where("caller_user = ?", opts.CallerUser)
	}
	if opts.CallerProject != "" {
		q = q.Where("caller_project = ?", opts.CallerProject)
	}
	if opts.CallerEnvironment != "" {
		q = q.Where("caller_environment = ?", opts.CallerEnvironment)
	}
	if opts.CallerEnvironment != "" {
		q = q.Where("caller_environment = ?", opts.CallerEnvironment)
	}
	if opts.RequestedModel != "" {
		q = q.Where("requested_model = ?", opts.RequestedModel)
	}
	if opts.ResolvedGroup != "" {
		q = q.Where("resolved_group = ?", opts.ResolvedGroup)
	}
	if opts.TargetProvider != "" {
		q = q.Where("target_provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("target_model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("target_dialect = ?", opts.TargetDialect)
	}
	if opts.Status != 0 {
		q = q.Where("status = ?", opts.Status)
	}
	if opts.Cache != "" {
		q = q.Where("cache = ?", opts.Cache)
	}
	if opts.Client != "" {
		q = q.Where("client = ?", opts.Client)
	}
	if opts.InboundDialect != "" {
		q = q.Where("inbound_dialect = ?", opts.InboundDialect)
	}
	if opts.StreamOnly != nil {
		q = q.Where("stream = ?", *opts.StreamOnly)
	}
	if opts.TrafficShapedOnly {
		q = q.Where("traffic_shape_applied = ? OR request_id IN (SELECT request_id FROM request_upstream_shape_events WHERE decision IN (?, ?))", true, shapeDecisionSkipped, shapeDecisionCooldownStarted)
	}
	if opts.TrafficShapeBucket != "" {
		q = q.Where("traffic_shape_bucket = ? OR request_id IN (SELECT request_id FROM request_upstream_shape_events WHERE bucket = ?)", opts.TrafficShapeBucket, opts.TrafficShapeBucket)
	}
	if opts.TrafficShapeScope != "" {
		q = q.Where("traffic_shape_scope = ? OR request_id IN (SELECT request_id FROM request_upstream_shape_events WHERE scope = ?)", opts.TrafficShapeScope, opts.TrafficShapeScope)
	}
	if opts.ToolChoiceMode != "" {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE tool_choice_mode = ? UNION SELECT request_id FROM request_translation_shapes WHERE translated_tool_choice_mode = ?)", opts.ToolChoiceMode, opts.ToolChoiceMode)
	}
	if opts.ToolCountBucket != "" {
		switch opts.ToolCountBucket {
		case "none":
			q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE tool_count = 0)")
		case "one":
			q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE tool_count = 1)")
		case "small":
			q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE tool_count BETWEEN 2 AND 8)")
		case "large":
			q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE tool_count > 8)")
		}
	}
	if opts.RequestBytesBucket != "" {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE total_request_bytes_bucket = ? UNION SELECT request_id FROM request_translation_shapes WHERE translated_request_bytes_bucket = ?)", opts.RequestBytesBucket, opts.RequestBytesBucket)
	}
	if opts.InputTokensBucket != "" {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE estimated_input_tokens_bucket = ?)", opts.InputTokensBucket)
	}
	if opts.OutputCapBucket != "" {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE requested_output_cap_bucket = ? UNION SELECT request_id FROM request_translation_shapes WHERE translated_output_cap_bucket = ?)", opts.OutputCapBucket, opts.OutputCapBucket)
	}
	if opts.ReasoningPresent != nil {
		if *opts.ReasoningPresent {
			q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE reasoning_present = ? UNION SELECT request_id FROM request_translation_shapes WHERE translated_reasoning_control != '')", true)
		} else {
			q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE reasoning_present = ?) AND request_id NOT IN (SELECT request_id FROM request_translation_shapes WHERE translated_reasoning_control != '')", false)
		}
	}
	if opts.MultimodalOnly {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE image_count > 0 OR audio_present = ? OR video_present = ?)", true, true)
	}
	if opts.RequestShapeFP != "" {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE request_shape_fingerprint = ? UNION SELECT request_id FROM request_translation_shapes WHERE request_shape_fingerprint = ?)", opts.RequestShapeFP, opts.RequestShapeFP)
	}
	if opts.ToolSchemaFP != "" {
		q = q.Where("request_id IN (SELECT request_id FROM request_shapes WHERE tool_schema_fingerprint = ? UNION SELECT request_id FROM request_translation_shapes WHERE tool_schema_fingerprint = ?)", opts.ToolSchemaFP, opts.ToolSchemaFP)
	}
	if opts.ErrorClass != "" {
		q = q.Where(`request_id IN (
			SELECT request_id FROM request_attempts WHERE error_class = ?
			UNION SELECT request_id FROM request_errors WHERE error_class = ?
			UNION SELECT request_id FROM request_fallback_transitions WHERE error_class = ?
			UNION SELECT request_id FROM request_upstream_error_details WHERE error_class = ?
		)`, opts.ErrorClass, opts.ErrorClass, opts.ErrorClass, opts.ErrorClass)
	}
	return q
}

func usagePageOrder(sortKey, direction string) string {
	dir := "DESC"
	if direction == "asc" {
		dir = "ASC"
	}
	switch sortKey {
	case "costUsd":
		return "total_cost_usd " + dir + ", ts " + dir + ", request_id " + dir
	case "latencyMs":
		return "latency_ms " + dir + ", ts " + dir + ", request_id " + dir
	case "status":
		return "status " + dir + ", ts " + dir + ", request_id " + dir
	case "requestId":
		return "request_id " + dir
	default:
		return "ts " + dir + ", request_id " + dir
	}
}

func applyUsagePageCursor(q *gorm.DB, sortKey, direction string, cursor *adminReportCursorPayload) (*gorm.DB, error) {
	op := "<"
	if direction == "asc" {
		op = ">"
	}
	switch sortKey {
	case "costUsd":
		if len(cursor.Values) != 3 {
			return nil, errors.New("invalid cursor")
		}
		value, err := strconv.ParseFloat(cursor.Values[0], 64)
		if err != nil {
			return nil, errors.New("invalid cursor")
		}
		ts, requestID := cursor.Values[1], cursor.Values[2]
		return q.Where("(total_cost_usd "+op+" ?) OR (total_cost_usd = ? AND (ts "+op+" ? OR (ts = ? AND request_id "+op+" ?)))", value, value, ts, ts, requestID), nil
	case "latencyMs", "status":
		if len(cursor.Values) != 3 {
			return nil, errors.New("invalid cursor")
		}
		value, err := strconv.ParseInt(cursor.Values[0], 10, 64)
		if err != nil {
			return nil, errors.New("invalid cursor")
		}
		column := "latency_ms"
		if sortKey == "status" {
			column = "status"
		}
		ts, requestID := cursor.Values[1], cursor.Values[2]
		return q.Where("("+column+" "+op+" ?) OR ("+column+" = ? AND (ts "+op+" ? OR (ts = ? AND request_id "+op+" ?)))", value, value, ts, ts, requestID), nil
	case "requestId":
		if len(cursor.Values) != 1 {
			return nil, errors.New("invalid cursor")
		}
		return q.Where("request_id "+op+" ?", cursor.Values[0]), nil
	default:
		if len(cursor.Values) != 2 {
			return nil, errors.New("invalid cursor")
		}
		ts, requestID := cursor.Values[0], cursor.Values[1]
		return q.Where("(ts "+op+" ?) OR (ts = ? AND request_id "+op+" ?)", ts, ts, requestID), nil
	}
}

func (s *usageStore) upstreamShapeEventsForRows(rows []usageRow, opts UsageReportOptions) ([]upstreamShapeJoinedEvent, error) {
	if s == nil || s.db == nil || len(rows) == 0 {
		return nil, nil
	}
	requestIDs := make([]string, 0, len(rows))
	rowByRequestID := map[string]usageRow{}
	for _, row := range rows {
		if row.RequestID == "" {
			continue
		}
		requestIDs = append(requestIDs, row.RequestID)
		rowByRequestID[row.RequestID] = row
	}
	if len(requestIDs) == 0 {
		return nil, nil
	}
	q := s.db.Where("request_id IN ?", requestIDs)
	if opts.TrafficShapeScope != "" {
		q = q.Where("scope = ?", opts.TrafficShapeScope)
	}
	if opts.TrafficShapeBucket != "" {
		q = q.Where("bucket = ?", opts.TrafficShapeBucket)
	}
	if opts.TargetProvider != "" {
		q = q.Where("provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("dialect = ?", opts.TargetDialect)
	}
	var records []requestUpstreamShapeEventRecord
	if err := q.Order("ts ASC, request_id ASC, seq ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	out := make([]upstreamShapeJoinedEvent, 0, len(records))
	for _, event := range records {
		out = append(out, upstreamShapeJoinedEvent{Event: event, Row: rowByRequestID[event.RequestID]})
	}
	return out, nil
}

func (s *usageStore) loadUsageReportBuckets(rows []usageRow) {
	if s == nil || s.db == nil || len(rows) == 0 {
		return
	}
	requestIDs := make([]string, 0, len(rows))
	rowByRequestID := map[string]*usageRow{}
	for i := range rows {
		rows[i].MaxTokenBucket = "unknown"
		rows[i].InputTokenBucket = inputTokenBucket(rows[i].InputTokens)
		rows[i].AdmissionReason = admissionReasonBucket(rows[i])
		if rows[i].RequestID == "" {
			continue
		}
		requestIDs = append(requestIDs, rows[i].RequestID)
		rowByRequestID[rows[i].RequestID] = &rows[i]
	}
	if len(requestIDs) == 0 {
		return
	}
	var shapes []decisionShapeFeatureRecord
	_ = s.db.Where("request_id IN ? AND feature_name IN ?", requestIDs, []string{"max_token_bucket", "input_token_bucket"}).Find(&shapes).Error
	for _, shape := range shapes {
		row := rowByRequestID[shape.RequestID]
		if row == nil {
			continue
		}
		switch shape.FeatureName {
		case "max_token_bucket":
			row.MaxTokenBucket = defaultString(shape.TextValue, "unknown")
		case "input_token_bucket":
			row.InputTokenBucket = defaultString(shape.TextValue, row.InputTokenBucket)
		}
	}
	var requestShapes []requestShapeRecord
	_ = s.db.Where("request_id IN ?", requestIDs).Find(&requestShapes).Error
	for _, shape := range requestShapes {
		row := rowByRequestID[shape.RequestID]
		if row == nil {
			continue
		}
		row.RequestShapeFingerprint = defaultString(shape.RequestShapeFingerprint, row.RequestShapeFingerprint)
		row.ToolSchemaFingerprint = defaultString(shape.ToolSchemaFingerprint, row.ToolSchemaFingerprint)
		row.RequestShapeBucket = joinKey(
			defaultString(shape.InboundDialect, "unknown"),
			boolBucket("stream", shape.Stream),
			toolCountReportBucket(shape.ToolCount),
			defaultString(shape.ToolChoiceMode, "tool-choice:none"),
			defaultString(shape.TotalRequestBytesBucket, "bytes:unknown"),
			defaultString(shape.EstimatedInputTokensBucket, "tokens:unknown"),
			defaultString(shape.RequestedOutputCapBucket, "output:unknown"),
			boolBucket("reasoning", shape.ReasoningPresent),
			boolBucket("multimodal", shape.ImageCount > 0 || shape.AudioPresent || shape.VideoPresent),
		)
	}
	var tokenEstimates []requestTokenEstimateRecord
	_ = s.db.Where("request_id IN ?", requestIDs).Find(&tokenEstimates).Error
	for _, estimate := range tokenEstimates {
		row := rowByRequestID[estimate.RequestID]
		if row == nil {
			continue
		}
		row.EstimatedInputTokens = estimate.EstimatedInputTokens
		row.EstimatedToolSchemaTokens = estimate.EstimatedToolSchemaTokens
		row.EstimatedImageTokens = estimate.EstimatedImageTokens
		row.EstimatedTotalInputTokens = estimate.EstimatedTotalInputTokens
		row.RequestedOutputCapTokens = estimate.RequestedOutputCapTokens
		row.TotalReservedTokens = estimate.TotalReservedTokens
	}
	var translationShapes []requestTranslationShapeRecord
	_ = s.db.Where("request_id IN ?", requestIDs).Find(&translationShapes).Error
	for _, shape := range translationShapes {
		row := rowByRequestID[shape.RequestID]
		if row == nil {
			continue
		}
		row.RequestShapeFingerprint = defaultString(shape.RequestShapeFingerprint, row.RequestShapeFingerprint)
		row.ToolSchemaFingerprint = defaultString(shape.ToolSchemaFingerprint, row.ToolSchemaFingerprint)
		row.TranslationShapeBucket = joinKey(
			defaultString(shape.Provider, "unknown"),
			defaultString(shape.Model, "unknown"),
			defaultString(shape.Dialect, "unknown"),
			defaultString(shape.BridgeDirection, "bridge:none"),
			boolBucket("stream", shape.TranslatedStream),
			toolCountReportBucket(shape.TranslatedToolCount),
			defaultString(shape.TranslatedToolChoiceMode, "tool-choice:none"),
			defaultString(shape.TranslatedRequestBytesBucket, "bytes:unknown"),
			defaultString(shape.TranslatedOutputCapBucket, "output:unknown"),
			defaultString(shape.TranslatedReasoningControl, "reasoning:none"),
			boolBucket("unsupported", shape.UnsupportedFieldsPresent),
		)
	}
	var upstreamErrorDetails []requestUpstreamErrorDetailRecord
	_ = s.db.Where("request_id IN ? AND field_name IN ?", requestIDs, []string{"code", "param", "message", "error"}).Order("attempt_index ASC, seq ASC").Find(&upstreamErrorDetails).Error
	for _, detail := range upstreamErrorDetails {
		row := rowByRequestID[detail.RequestID]
		if row == nil {
			continue
		}
		switch detail.FieldName {
		case "code":
			row.UpstreamErrorCode = defaultString(detail.FieldValue, row.UpstreamErrorCode)
		case "param":
			row.UpstreamErrorParam = defaultString(detail.FieldValue, row.UpstreamErrorParam)
		case "message", "error":
			row.UpstreamErrorMessageCategory = defaultString(detail.FieldValue, row.UpstreamErrorMessageCategory)
		}
	}
	var signals []routingSignalRecord
	_ = s.db.Where("request_id IN ? AND strategy = ? AND source = ? AND bool_value = ?", requestIDs, "dynamic_score", "dynamic_score", true).Find(&signals).Error
	for _, signal := range signals {
		row := rowByRequestID[signal.RequestID]
		if row != nil && signal.SignalName != "" {
			row.EnabledSignals = appendUniqueString(row.EnabledSignals, signal.SignalName)
		}
	}
	var terms []dynamicScoreTermRecord
	_ = s.db.Where("request_id IN ?", requestIDs).Find(&terms).Error
	for _, term := range terms {
		row := rowByRequestID[term.RequestID]
		if row == nil {
			continue
		}
		valueBucket := defaultString(term.ValueBucket, scoreValueBucket(term.Value))
		if term.ScoreName != "" && valueBucket != "" {
			row.ScoreBuckets = appendUniqueString(row.ScoreBuckets, scoreBucketReportKey(term.ScoreName, valueBucket))
		}
		finalScoreBucket := defaultString(term.FinalScoreBucket, scoreValueBucket(term.FinalScore))
		if finalScoreBucket != "" {
			row.ScoreBuckets = appendUniqueString(row.ScoreBuckets, scoreBucketReportKey("final_score", finalScoreBucket))
		}
	}
	var filterReasons []decisionTargetFilterReasonRecord
	_ = s.db.Where("request_id IN ?", requestIDs).Find(&filterReasons).Error
	for _, reason := range filterReasons {
		row := rowByRequestID[reason.RequestID]
		if row == nil {
			continue
		}
		switch {
		case reason.Reason == "max-tokens-honored":
			row.ThresholdBuckets = appendUniqueString(row.ThresholdBuckets, "max_token_cap_filtered")
		case reason.Stage == "dynamic_score" && strings.Contains(reason.Reason, "threshold"):
			row.ThresholdBuckets = appendUniqueString(row.ThresholdBuckets, reason.Reason)
		}
	}
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func scoreBucketReportKey(scoreName, bucket string) string {
	scoreName = strings.TrimSpace(scoreName)
	bucket = strings.TrimSpace(bucket)
	if scoreName == "" {
		return bucket
	}
	if bucket == "" {
		return scoreName
	}
	return scoreName + ":" + bucket
}

func splitScoreBucketReportKey(key string) []string {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) == 1 {
		return []string{parts[0], ""}
	}
	return parts
}

func boolBucket(name string, value bool) string {
	if value {
		return name + ":yes"
	}
	return name + ":no"
}

func toolCountReportBucket(count int) string {
	switch {
	case count <= 0:
		return "tools:none"
	case count == 1:
		return "tools:one"
	case count <= 8:
		return "tools:small"
	default:
		return "tools:large"
	}
}

func inputTokenBucket(tokens int) string {
	switch {
	case tokens > 100000:
		return "huge"
	case tokens > 32000:
		return "very_large"
	case tokens > 12000:
		return "large"
	case tokens > 3000:
		return "medium"
	case tokens > 0:
		return "small"
	default:
		return "unknown"
	}
}

func admissionReasonBucket(row usageRow) string {
	if row.Status == http.StatusTooManyRequests || row.Status == http.StatusForbidden {
		switch {
		case row.Error != "":
			return row.Error
		case row.QuotaState != "" && row.QuotaState != "ok":
			return "quota-" + row.QuotaState
		case row.KeyState != "" && row.KeyState != "ok" && row.KeyState != "active":
			return "key-" + row.KeyState
		}
	}
	if row.QuotaState != "" && row.QuotaState != "ok" {
		return "quota-" + row.QuotaState
	}
	if row.KeyState != "" && row.KeyState != "ok" && row.KeyState != "active" {
		return "key-" + row.KeyState
	}
	return "admitted"
}

func (s *usageStore) EmitSecurityAccessEvent(event securityAccessEvent) {
	if s == nil || s.db == nil {
		return
	}
	if event.TS.IsZero() {
		event.TS = time.Now().UTC()
	}
	_ = s.db.Create(securityAccessRecordFromEvent(event)).Error
}

func (s *usageStore) PurgeSecurityAccessEventsBefore(cutoff time.Time) {
	if s == nil || s.db == nil || cutoff.IsZero() {
		return
	}
	_ = s.db.Where("ts < ?", formatUsageTime(cutoff.UTC())).Delete(&securityAccessEventRecord{}).Error
}

func (s *usageStore) securityAccessEvents(opts SecurityReportOptions) ([]securityAccessEvent, error) {
	var records []securityAccessEventRecord
	q := s.securityAccessEventsQuery(opts)
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	if err := q.Order("ts DESC, id DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return securityAccessEventsFromRecords(records)
}

func (s *usageStore) adminSecurityEventsPage(opts adminSecurityPageOptions) (adminSecurityPage, error) {
	base := s.securityAccessEventsQuery(opts.SecurityReportOptions)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return adminSecurityPage{}, err
	}
	q := s.securityAccessEventsQuery(opts.SecurityReportOptions)
	var err error
	if opts.Cursor != nil {
		q, err = applySecurityPageCursor(q, opts.Sort, opts.Direction, opts.Cursor)
		if err != nil {
			return adminSecurityPage{}, err
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	var records []securityAccessEventRecord
	if err := q.Order(securityPageOrder(opts.Sort, opts.Direction)).Limit(limit + 1).Find(&records).Error; err != nil {
		return adminSecurityPage{}, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	events, err := securityAccessEventsFromRecords(records)
	if err != nil {
		return adminSecurityPage{}, err
	}
	return adminSecurityPage{Events: events, TotalCount: total, HasMore: hasMore}, nil
}

func (s *usageStore) adminSecurityTrendBucketsSQL(opts SecurityReportOptions) ([]securityTrendBucketRecord, error) {
	var records []securityTrendBucketRecord
	err := s.securityAccessEventsQuery(opts).
		Select("substr(ts, 1, 13) || ':00:00Z' AS bucket_utc, COALESCE(NULLIF(outcome, ''), 'unknown') AS outcome, COALESCE(NULLIF(surface, ''), 'unknown') AS surface, COALESCE(NULLIF(reason_code, ''), 'none') AS reason, COUNT(*) AS events").
		Group("bucket_utc, outcome, surface, reason").
		Order("bucket_utc ASC, outcome ASC, surface ASC, reason ASC").
		Scan(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (s *usageStore) securityAccessEventsQuery(opts SecurityReportOptions) *gorm.DB {
	q := s.db.Model(&securityAccessEventRecord{}).Where("ts >= ? AND ts < ?", formatUsageTime(opts.From), formatUsageTime(opts.To))
	if opts.Outcome != "" {
		q = q.Where("outcome = ?", opts.Outcome)
	}
	if opts.ReasonCode != "" {
		q = q.Where("reason_code = ?", opts.ReasonCode)
	}
	if opts.Surface != "" {
		q = q.Where("surface = ?", opts.Surface)
	}
	if opts.IPAddress != "" {
		q = q.Where("ip_address = ?", opts.IPAddress)
	}
	if opts.CallerID != "" {
		q = q.Where("caller_id = ?", opts.CallerID)
	}
	if opts.CallerUser != "" {
		q = q.Where("caller_user = ?", opts.CallerUser)
	}
	if opts.CallerProject != "" {
		q = q.Where("caller_project = ?", opts.CallerProject)
	}
	if opts.CallerEnvironment != "" {
		q = q.Where("caller_environment = ?", opts.CallerEnvironment)
	}
	if opts.TokenID != "" {
		q = q.Where("token_id = ?", opts.TokenID)
	}
	if opts.AdminSubject != "" {
		q = q.Where("admin_subject = ?", opts.AdminSubject)
	}
	if opts.Client != "" {
		q = q.Where("client = ?", opts.Client)
	}
	return q
}

func securityAccessEventsFromRecords(records []securityAccessEventRecord) ([]securityAccessEvent, error) {
	out := make([]securityAccessEvent, 0, len(records))
	for _, record := range records {
		row, err := securityAccessEventFromRecord(record)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func securityPageOrder(sortKey, direction string) string {
	dir := "DESC"
	if direction == "asc" {
		dir = "ASC"
	}
	switch sortKey {
	case "status":
		return "status_code " + dir + ", ts " + dir + ", id " + dir
	case "outcome":
		return "outcome " + dir + ", ts " + dir + ", id " + dir
	case "surface":
		return "surface " + dir + ", ts " + dir + ", id " + dir
	case "reason":
		return "reason_code " + dir + ", ts " + dir + ", id " + dir
	default:
		return "ts " + dir + ", id " + dir
	}
}

func applySecurityPageCursor(q *gorm.DB, sortKey, direction string, cursor *adminReportCursorPayload) (*gorm.DB, error) {
	op := "<"
	if direction == "asc" {
		op = ">"
	}
	switch sortKey {
	case "status":
		if len(cursor.Values) != 3 {
			return nil, errors.New("invalid cursor")
		}
		value, err := strconv.ParseInt(cursor.Values[0], 10, 64)
		if err != nil {
			return nil, errors.New("invalid cursor")
		}
		ts, id := cursor.Values[1], cursor.Values[2]
		return q.Where("(status_code "+op+" ?) OR (status_code = ? AND (ts "+op+" ? OR (ts = ? AND id "+op+" ?)))", value, value, ts, ts, id), nil
	case "outcome", "surface", "reason":
		if len(cursor.Values) != 3 {
			return nil, errors.New("invalid cursor")
		}
		column := "outcome"
		if sortKey == "surface" {
			column = "surface"
		}
		if sortKey == "reason" {
			column = "reason_code"
		}
		value, ts, id := cursor.Values[0], cursor.Values[1], cursor.Values[2]
		return q.Where("("+column+" "+op+" ?) OR ("+column+" = ? AND (ts "+op+" ? OR (ts = ? AND id "+op+" ?)))", value, value, ts, ts, id), nil
	default:
		if len(cursor.Values) != 2 {
			return nil, errors.New("invalid cursor")
		}
		ts, id := cursor.Values[0], cursor.Values[1]
		return q.Where("(ts "+op+" ?) OR (ts = ? AND id "+op+" ?)", ts, ts, id), nil
	}
}

func renderUsageMarkdown(from, to time.Time, rows []usageRow, decisionSummary decisionTelemetrySummary, upstreamShapeEvents []upstreamShapeJoinedEvent) string {
	return renderUsageMarkdownWithTimeFormatter(from, to, rows, decisionSummary, upstreamShapeEvents, formatUsageTime)
}

func renderUsageCLIMarkdown(from, to time.Time, rows []usageRow, decisionSummary decisionTelemetrySummary, upstreamShapeEvents []upstreamShapeJoinedEvent) string {
	return renderUsageMarkdownWithTimeFormatter(from, to, rows, decisionSummary, upstreamShapeEvents, formatUsageMarkdownTime)
}

func renderUsageMarkdownWithTimeFormatter(from, to time.Time, rows []usageRow, decisionSummary decisionTelemetrySummary, upstreamShapeEvents []upstreamShapeJoinedEvent, formatInstant func(time.Time) string) string {
	total := &agg{}
	byToken := map[string]*agg{}
	byTokenMeta := map[string]usageRow{}
	byModel := map[string]*agg{}
	byGroup := map[string]*agg{}
	byClient := map[string]*agg{}
	byCallerIP := map[string]*agg{}
	byStatus := map[string]*agg{}
	byDownstreamUser := map[string]*agg{}
	byUpstreamEndpoint := map[string]*agg{}
	byHour := map[string]*agg{}
	byHourIP := map[string]*agg{}
	byDay := map[string]*agg{}

	for _, row := range rows {
		total.add(row)
		tokenKey := joinKey(row.TokenID, row.CallerUser, row.CallerProject, row.CallerEnvironment, row.CallerID)
		byToken[tokenKey] = getAgg(byToken, tokenKey)
		byToken[tokenKey].add(row)
		byTokenMeta[tokenKey] = row
		modelKey := joinKey(row.TargetProvider, row.TargetModel)
		byModel[modelKey] = getAgg(byModel, modelKey)
		byModel[modelKey].add(row)
		groupKey := defaultString(row.ResolvedGroup, row.RequestedModel)
		byGroup[groupKey] = getAgg(byGroup, groupKey)
		byGroup[groupKey].add(row)
		clientKey := defaultString(row.Client, "unknown")
		byClient[clientKey] = getAgg(byClient, clientKey)
		byClient[clientKey].add(row)
		ipKey := defaultString(row.CallerIP, "unknown")
		byCallerIP[ipKey] = getAgg(byCallerIP, ipKey)
		byCallerIP[ipKey].add(row)
		statusKey := fmt.Sprint(row.Status)
		byStatus[statusKey] = getAgg(byStatus, statusKey)
		byStatus[statusKey].add(row)
		downstreamUserKey := joinKey(defaultString(row.CallerUser, "unknown"), defaultString(row.CallerProject, "unknown"), defaultString(row.CallerEnvironment, "unknown"), clientKey)
		byDownstreamUser[downstreamUserKey] = getAgg(byDownstreamUser, downstreamUserKey)
		byDownstreamUser[downstreamUserKey].add(row)
		upstreamEndpointKey := joinKey(defaultString(row.TargetProvider, "unknown"), defaultString(row.TargetModel, "unknown"), defaultString(row.TargetDialect, "unknown"))
		byUpstreamEndpoint[upstreamEndpointKey] = getAgg(byUpstreamEndpoint, upstreamEndpointKey)
		byUpstreamEndpoint[upstreamEndpointKey].add(row)
		hourKey := row.TS.UTC().Truncate(time.Hour).Format("2006-01-02 15:00")
		byHour[hourKey] = getAgg(byHour, hourKey)
		byHour[hourKey].add(row)
		hourIPKey := joinKey(hourKey, ipKey)
		byHourIP[hourIPKey] = getAgg(byHourIP, hourIPKey)
		byHourIP[hourIPKey].add(row)
		dayKey := row.TS.UTC().Format("2006-01-02")
		byDay[dayKey] = getAgg(byDay, dayKey)
		byDay[dayKey].add(row)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Metrum AI Router Usage Report\n\n")
	fmt.Fprintf(&b, "- Period UTC: `%s` to `%s`\n", formatInstant(from), formatInstant(to))
	fmt.Fprintf(&b, "- Requests: `%d`\n", total.Calls)
	fmt.Fprintf(&b, "- Errors: `%d`\n", total.Errors)
	fmt.Fprintf(&b, "- Total Tokens: `%d`; Input Tokens: `%d`; Output Tokens: `%d`\n", total.TotalTokens, total.InputTokens, total.OutputTokens)
	fmt.Fprintf(&b, "- Cost: `$%s` total, `$%s` input, `$%s` image, `$%s` output\n", fmtUSD(total.TotalCostUSD), fmtUSD(total.InputCostUSD), fmtUSD(total.ImageCostUSD), fmtUSD(total.OutputCostUSD))
	fmt.Fprintf(&b, "- Cache: `%d` hits, `%d` misses, `%d` bypass\n", total.CacheHits, total.CacheMisses, total.CacheBypass)
	fmt.Fprintf(&b, "- Upstream attempts: `%d`; fallbacks: `%d`; streaming requests: `%d`\n", total.Attempts, total.Fallbacks, total.Streams)
	fmt.Fprintf(&b, "- Latency: `%d ms` avg, `%d ms` max\n", avg(total.LatencyMS, total.Calls), total.MaxLatencyMS)
	fmt.Fprintf(&b, "- Throughput: upstream `%s` output tok/s / `%s` total tok/s; downstream write `%s` output tok/s / `%s` total tok/s\n",
		fmtFloat(avgFloat(total.UpstreamOutputTPS, total.UpstreamOutputTPSCount)),
		fmtFloat(avgFloat(total.UpstreamTotalTPS, total.UpstreamTotalTPSCount)),
		fmtFloat(avgFloat(total.DownstreamOutputTPS, total.DownstreamOutputTPSCount)),
		fmtFloat(avgFloat(total.DownstreamTotalTPS, total.DownstreamTotalTPSCount)))
	fmt.Fprintf(&b, "- Cache occupancy: latest `%s`, avg `%s`, max `%s`\n\n",
		fmtPct(total.CacheOccupancyLatest), fmtPct(avgFloat(total.CacheOccupancySum, total.CacheSnapshotCount)), fmtPct(total.CacheOccupancyMax))

	writeCacheSummary(&b, total)
	writeDecisionTelemetrySummary(&b, decisionSummary)
	writeTrafficShapingSummary(&b, rows, upstreamShapeEvents)
	writeDownstreamUserPerformanceTable(&b, byDownstreamUser)
	writeUpstreamEndpointPerformanceTable(&b, byUpstreamEndpoint)
	writeRequestThroughputTable(&b, rows, formatInstant)
	writeTokenTable(&b, "Usage By Internal API Key", byToken, byTokenMeta)
	writeAggTable(&b, "Usage By External Model", []string{"Provider", "Model"}, byModel, splitKey2)
	writeAggTable(&b, "Usage By Router Model Group", []string{"Model Group"}, byGroup, splitKey1)
	writeAggTable(&b, "Usage By Client", []string{"Client"}, byClient, splitKey1)
	writeAggTable(&b, "Usage By Caller IP", []string{"Caller IP"}, byCallerIP, splitKey1)
	writeAggTable(&b, "Usage By Status", []string{"Status"}, byStatus, splitKey1)
	writeAggTable(&b, "Hourly Usage", []string{"Hour UTC"}, byHour, splitKey1)
	writeAggTable(&b, "Hourly Usage By Caller IP", []string{"Hour UTC", "Caller IP"}, byHourIP, splitKey2)
	writeAggTable(&b, "Daily Usage", []string{"Day UTC"}, byDay, splitKey1)
	return b.String()
}

func (a *agg) add(row usageRow) {
	a.Calls++
	if row.Status >= 400 {
		a.Errors++
	}
	if row.Stream {
		a.Streams++
	}
	switch row.Cache {
	case "hit":
		a.CacheHits++
	case "miss":
		a.CacheMisses++
	default:
		a.CacheBypass++
	}
	if row.FallbackUsed {
		a.Fallbacks++
	}
	a.Attempts += int64(row.Attempts)
	a.InputTokens += int64(row.InputTokens)
	a.OutputTokens += int64(row.OutputTokens)
	a.InputCostUSD += row.InputCostUSD
	a.ImageCostUSD += row.ImageCostUSD
	a.OutputCostUSD += row.OutputCostUSD
	a.TotalCostUSD += row.TotalCostUSD
	total := row.TotalTokens
	if total == 0 {
		total = row.InputTokens + row.OutputTokens
	}
	a.TotalTokens += int64(total)
	a.LatencyMS += row.LatencyMS
	if row.LatencyMS > a.MaxLatencyMS {
		a.MaxLatencyMS = row.LatencyMS
	}
	if row.TTFBMS != nil {
		a.TTFBMS += *row.TTFBMS
		a.TTFBCount++
		if *row.TTFBMS > a.MaxTTFBMS {
			a.MaxTTFBMS = *row.TTFBMS
		}
	}
	if row.UpstreamMS != nil {
		a.UpstreamMS += *row.UpstreamMS
		a.UpstreamMSCount++
		if *row.UpstreamMS > a.MaxUpstreamMS {
			a.MaxUpstreamMS = *row.UpstreamMS
		}
	}
	if row.DownstreamMS != nil {
		a.DownstreamMS += *row.DownstreamMS
		a.DownstreamMSCount++
		if *row.DownstreamMS > a.MaxDownstreamMS {
			a.MaxDownstreamMS = *row.DownstreamMS
		}
	}
	addFloat(row.UpstreamOutputTPS, &a.UpstreamOutputTPS, &a.UpstreamOutputTPSCount)
	addFloat(row.UpstreamTotalTPS, &a.UpstreamTotalTPS, &a.UpstreamTotalTPSCount)
	addFloat(row.DownstreamOutputTPS, &a.DownstreamOutputTPS, &a.DownstreamOutputTPSCount)
	addFloat(row.DownstreamTotalTPS, &a.DownstreamTotalTPS, &a.DownstreamTotalTPSCount)
	if row.CacheEnabled || row.CacheMaxBytes > 0 {
		a.CacheSnapshotCount++
		a.CacheItemsLatest = row.CacheItems
		a.CacheBytesLatest = row.CacheBytes
		a.CacheMaxBytesLatest = row.CacheMaxBytes
		a.CacheOccupancyLatest = row.CacheOccupancyPct
		a.CacheBytesSum += row.CacheBytes
		a.CacheOccupancySum += row.CacheOccupancyPct
		if row.CacheItems > a.CacheItemsMax {
			a.CacheItemsMax = row.CacheItems
		}
		if row.CacheBytes > a.CacheBytesMax {
			a.CacheBytesMax = row.CacheBytes
		}
		if row.CacheOccupancyPct > a.CacheOccupancyMax {
			a.CacheOccupancyMax = row.CacheOccupancyPct
		}
	}
}

func (a *agg) addAgg(other agg) {
	a.Calls += other.Calls
	a.Errors += other.Errors
	a.Streams += other.Streams
	a.CacheHits += other.CacheHits
	a.CacheMisses += other.CacheMisses
	a.CacheBypass += other.CacheBypass
	a.Fallbacks += other.Fallbacks
	a.Attempts += other.Attempts
	a.InputTokens += other.InputTokens
	a.OutputTokens += other.OutputTokens
	a.TotalTokens += other.TotalTokens
	a.InputCostUSD += other.InputCostUSD
	a.ImageCostUSD += other.ImageCostUSD
	a.OutputCostUSD += other.OutputCostUSD
	a.TotalCostUSD += other.TotalCostUSD
	a.LatencyMS += other.LatencyMS
	if other.MaxLatencyMS > a.MaxLatencyMS {
		a.MaxLatencyMS = other.MaxLatencyMS
	}
	a.TTFBMS += other.TTFBMS
	a.TTFBCount += other.TTFBCount
	if other.MaxTTFBMS > a.MaxTTFBMS {
		a.MaxTTFBMS = other.MaxTTFBMS
	}
	a.UpstreamMS += other.UpstreamMS
	a.UpstreamMSCount += other.UpstreamMSCount
	if other.MaxUpstreamMS > a.MaxUpstreamMS {
		a.MaxUpstreamMS = other.MaxUpstreamMS
	}
	a.DownstreamMS += other.DownstreamMS
	a.DownstreamMSCount += other.DownstreamMSCount
	if other.MaxDownstreamMS > a.MaxDownstreamMS {
		a.MaxDownstreamMS = other.MaxDownstreamMS
	}
	a.UpstreamOutputTPS += other.UpstreamOutputTPS
	a.UpstreamOutputTPSCount += other.UpstreamOutputTPSCount
	a.UpstreamTotalTPS += other.UpstreamTotalTPS
	a.UpstreamTotalTPSCount += other.UpstreamTotalTPSCount
	a.DownstreamOutputTPS += other.DownstreamOutputTPS
	a.DownstreamOutputTPSCount += other.DownstreamOutputTPSCount
	a.DownstreamTotalTPS += other.DownstreamTotalTPS
	a.DownstreamTotalTPSCount += other.DownstreamTotalTPSCount
	a.CacheItemsLatest = max(a.CacheItemsLatest, other.CacheItemsLatest)
	a.CacheBytesLatest = max(a.CacheBytesLatest, other.CacheBytesLatest)
	a.CacheMaxBytesLatest = max(a.CacheMaxBytesLatest, other.CacheMaxBytesLatest)
	a.CacheOccupancyLatest = max(a.CacheOccupancyLatest, other.CacheOccupancyLatest)
	a.CacheItemsMax = max(a.CacheItemsMax, other.CacheItemsMax)
	a.CacheBytesMax = max(a.CacheBytesMax, other.CacheBytesMax)
	a.CacheOccupancyMax = max(a.CacheOccupancyMax, other.CacheOccupancyMax)
	a.CacheBytesSum += other.CacheBytesSum
	a.CacheOccupancySum += other.CacheOccupancySum
	a.CacheSnapshotCount += other.CacheSnapshotCount
}

func writeTokenTable(b *strings.Builder, title string, data map[string]*agg, meta map[string]usageRow) {
	fmt.Fprintf(b, "## %s\n\n", title)
	fmt.Fprintln(b, "| Token ID | Owner User | Project | Env | Caller ID | Calls | Errors | Total Tokens | Input Tokens | Output Tokens | Cost USD | Cache Hit | Cache Miss | Attempts | Fallbacks | Avg Upstream Output tok/s | Avg Downstream Write Output tok/s | Avg Latency ms | Max Latency ms |")
	fmt.Fprintln(b, "|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, key := range sortedAggKeys(data) {
		row := meta[key]
		a := data[key]
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | `%s` | %d | %d | %d | %d | %d | $%s | %d | %d | %d | %d | %s | %s | %d | %d |\n",
			esc(row.TokenID), esc(row.CallerUser), esc(row.CallerProject), esc(row.CallerEnvironment), esc(row.CallerID),
			a.Calls, a.Errors, a.TotalTokens, a.InputTokens, a.OutputTokens, fmtUSD(a.TotalCostUSD), a.CacheHits, a.CacheMisses, a.Attempts,
			a.Fallbacks, fmtFloat(avgFloat(a.UpstreamOutputTPS, a.UpstreamOutputTPSCount)),
			fmtFloat(avgFloat(a.DownstreamOutputTPS, a.DownstreamOutputTPSCount)), avg(a.LatencyMS, a.Calls), a.MaxLatencyMS)
	}
	if len(data) == 0 {
		fmt.Fprintln(b, "| _none_ |  |  |  |  | 0 | 0 | 0 | 0 | 0 | $0.000000 | 0 | 0 | 0 | 0 | n/a | n/a | 0 | 0 |")
	}
	fmt.Fprintln(b)
}

func writeAggTable(b *strings.Builder, title string, keyHeaders []string, data map[string]*agg, split func(string) []string) {
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, h := range keyHeaders {
		fmt.Fprintf(b, "| %s ", h)
	}
	fmt.Fprintln(b, "| Calls | Errors | Total Tokens | Input Tokens | Output Tokens | Cost USD | Cache Hit | Cache Miss | Cache Bypass | Attempts | Fallbacks | Streams | Avg Upstream Output tok/s | Avg Upstream Total tok/s | Avg Downstream Write Output tok/s | Avg Downstream Write Total tok/s | Avg Latency ms | Max Latency ms | Avg TTFB ms | Max TTFB ms |")
	for range keyHeaders {
		fmt.Fprint(b, "|---")
	}
	fmt.Fprintln(b, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, key := range sortedAggKeys(data) {
		parts := split(key)
		for _, part := range parts {
			fmt.Fprintf(b, "| %s ", esc(part))
		}
		a := data[key]
		fmt.Fprintf(b, "| %d | %d | %d | %d | %d | $%s | %d | %d | %d | %d | %d | %d | %s | %s | %s | %s | %d | %d | %d | %d |\n",
			a.Calls, a.Errors, a.TotalTokens, a.InputTokens, a.OutputTokens, fmtUSD(a.TotalCostUSD), a.CacheHits, a.CacheMisses, a.CacheBypass,
			a.Attempts, a.Fallbacks, a.Streams,
			fmtFloat(avgFloat(a.UpstreamOutputTPS, a.UpstreamOutputTPSCount)),
			fmtFloat(avgFloat(a.UpstreamTotalTPS, a.UpstreamTotalTPSCount)),
			fmtFloat(avgFloat(a.DownstreamOutputTPS, a.DownstreamOutputTPSCount)),
			fmtFloat(avgFloat(a.DownstreamTotalTPS, a.DownstreamTotalTPSCount)),
			avg(a.LatencyMS, a.Calls), a.MaxLatencyMS, avg(a.TTFBMS, a.TTFBCount), a.MaxTTFBMS)
	}
	if len(data) == 0 {
		for range keyHeaders {
			fmt.Fprint(b, "| _none_ ")
		}
		fmt.Fprintln(b, "| 0 | 0 | 0 | 0 | 0 | $0.000000 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | n/a | n/a | n/a | 0 | 0 | 0 | 0 |")
	}
	fmt.Fprintln(b)
}

func writeCacheSummary(b *strings.Builder, total *agg) {
	cacheable := total.CacheHits + total.CacheMisses
	fmt.Fprintln(b, "## Cache Summary")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Requests | Cacheable | Hits | Misses | Bypass | Hit Rate | Bypass Rate | Latest Items | Latest Bytes | Max Bytes | Latest Occupancy | Avg Occupancy | Max Occupancy |")
	fmt.Fprintln(b, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	fmt.Fprintf(b, "| %d | %d | %d | %d | %d | %s | %s | %d | %d | %d | %s | %s | %s |\n\n",
		total.Calls, cacheable, total.CacheHits, total.CacheMisses, total.CacheBypass,
		fmtPct(ratioPct(total.CacheHits, cacheable)), fmtPct(ratioPct(total.CacheBypass, total.Calls)),
		total.CacheItemsLatest, total.CacheBytesLatest, total.CacheMaxBytesLatest,
		fmtPct(total.CacheOccupancyLatest), fmtPct(avgFloat(total.CacheOccupancySum, total.CacheSnapshotCount)), fmtPct(total.CacheOccupancyMax))
}

func writeDecisionTelemetrySummary(b *strings.Builder, summary decisionTelemetrySummary) {
	if summary.ShapeFeatures == 0 && summary.Candidates == 0 && summary.FilterReasons == 0 && summary.Decisions == 0 && summary.RoutingSignals == 0 && summary.ScoreTerms == 0 && summary.PolicyExecutions == 0 && summary.FallbackTransitions == 0 && summary.CacheReasons == 0 {
		return
	}
	fmt.Fprintln(b, "## Decision Telemetry Summary")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Shape Feature Rows | Candidate Rows | Filter Reason Rows | Routing Decision Rows | Routing Signal Rows | Score/Ranking Term Rows | Policy Execution Rows | Fallback Transition Rows | Cache Reason Rows |")
	fmt.Fprintln(b, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	fmt.Fprintf(b, "| %d | %d | %d | %d | %d | %d | %d | %d | %d |\n\n", summary.ShapeFeatures, summary.Candidates, summary.FilterReasons, summary.Decisions, summary.RoutingSignals, summary.ScoreTerms, summary.PolicyExecutions, summary.FallbackTransitions, summary.CacheReasons)
	writeCountTable(b, "Routing Decisions By Strategy", "Strategy", summary.ByStrategy)
	writeCountTable(b, "Policy Executions By Outcome", "Outcome", summary.ByPolicyOutcome)
	writeCountTable(b, "Policy Error Classes", "Error Class", summary.ByPolicyErrorClass)
	writeCountTable(b, "Fallback Transition Reasons", "Reason", summary.ByFallbackReason)
	writeCountTable(b, "Target Filter Reasons", "Reason", summary.ByFilterReason)
	writeCountTable(b, "Cache Decision Reasons", "Reason", summary.ByCacheReason)
	writeCountTable(b, "Dynamic Score Enabled Signals", "Signal", summary.ByEnabledSignal)
	writeCountTable(b, "Dynamic Score Buckets", "Score / Bucket", summary.ByScoreBucket)
	writeCountTable(b, "Dynamic Threshold Buckets", "Threshold Bucket", summary.ByThresholdBucket)
	writeCountTable(b, "Max Token Buckets", "Bucket", summary.ByMaxTokenBucket)
	writeCountTable(b, "Input Token Buckets", "Bucket", summary.ByInputTokenBucket)
	writeCountTable(b, "Admission Reasons", "Reason", summary.ByAdmissionReason)
}

type shapingMarkdownAgg struct {
	Requests           int64
	Rejected           int64
	Queued             int64
	SkippedTargets     int64
	CooldownsStarted   int64
	RetryAfterMS       int64
	RetryAfterCount    int64
	MaxRetryAfterMS    int64
	QueueWaitMS        int64
	QueueWaitCount     int64
	MaxQueueWaitMS     int64
	QueueWaitSamples   []int64
	EstimatedInput     int64
	ReservedOutput     int64
	TotalReserved      int64
	Upstream429        int64
	UpstreamQuota      int64
	Fallbacks          int64
	RouteAroundSuccess int64
}

func (a *shapingMarkdownAgg) addRetry(ms int64) {
	if ms <= 0 {
		return
	}
	a.RetryAfterMS += ms
	a.RetryAfterCount++
	if ms > a.MaxRetryAfterMS {
		a.MaxRetryAfterMS = ms
	}
}

func (a *shapingMarkdownAgg) addQueue(ms int64) {
	if ms <= 0 {
		return
	}
	a.QueueWaitMS += ms
	a.QueueWaitCount++
	a.QueueWaitSamples = append(a.QueueWaitSamples, ms)
	if ms > a.MaxQueueWaitMS {
		a.MaxQueueWaitMS = ms
	}
}

func writeTrafficShapingSummary(b *strings.Builder, rows []usageRow, upstreamEvents []upstreamShapeJoinedEvent) {
	callerTotal := &shapingMarkdownAgg{}
	callerByBucket := map[string]*shapingMarkdownAgg{}
	callerByUser := map[string]*shapingMarkdownAgg{}
	callerByKey := map[string]*shapingMarkdownAgg{}
	callerByClient := map[string]*shapingMarkdownAgg{}
	callerByGroup := map[string]*shapingMarkdownAgg{}
	for _, row := range rows {
		if !row.TrafficShapeApplied {
			continue
		}
		addCallerShape(callerTotal, row)
		addCallerShape(getShapeAgg(callerByBucket, joinKey(defaultString(row.TrafficShapeScope, "unknown"), defaultString(row.TrafficShapeBucket, "unknown"), defaultString(row.TrafficShapeDecision, "unknown"))), row)
		addCallerShape(getShapeAgg(callerByUser, joinKey(defaultString(row.CallerUser, "unknown"), defaultString(row.CallerProject, "unknown"))), row)
		addCallerShape(getShapeAgg(callerByKey, defaultString(row.TokenID, "unknown")), row)
		addCallerShape(getShapeAgg(callerByClient, defaultString(row.Client, "unknown")), row)
		addCallerShape(getShapeAgg(callerByGroup, defaultString(row.ResolvedGroup, row.RequestedModel)), row)
	}
	upstreamTotal := &shapingMarkdownAgg{}
	upstreamByBucket := map[string]*shapingMarkdownAgg{}
	upstreamByProvider := map[string]*shapingMarkdownAgg{}
	backoffByReason := map[string]*shapingMarkdownAgg{}
	for _, joined := range upstreamEvents {
		addUpstreamShape(upstreamTotal, joined)
		event := joined.Event
		addUpstreamShape(getShapeAgg(upstreamByBucket, joinKey(defaultString(event.Scope, "unknown"), defaultString(event.Bucket, "unknown"), defaultString(event.Decision, "unknown"))), joined)
		addUpstreamShape(getShapeAgg(upstreamByProvider, joinKey(defaultString(event.Provider, "unknown"), defaultString(event.Model, "unknown"), defaultString(event.Dialect, "unknown"))), joined)
		if event.BackoffReason != "" || event.Bucket == shapeBucketBackoff {
			addUpstreamShape(getShapeAgg(backoffByReason, defaultString(event.BackoffReason, "adaptive-backoff")), joined)
		}
	}
	if callerTotal.Requests == 0 && upstreamTotal.Requests == 0 {
		return
	}
	fmt.Fprintln(b, "## Traffic Shaping Summary")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Surface | Events | Rejected | Queued | Skipped targets | Cooldowns | Avg retry-after ms | Max retry-after ms | Avg queue wait ms | P50 queue wait ms | P95 queue wait ms | Max queue wait ms | Estimated input tokens | Reserved output tokens | Total reserved tokens |")
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	writeShapeAggLine(b, "caller", callerTotal)
	writeShapeAggLine(b, "provider/model", upstreamTotal)
	fmt.Fprintln(b)
	writeShapeAggTable(b, "Traffic Shaping By Bucket", []string{"Scope", "Bucket", "Decision"}, callerByBucket, splitKey3)
	writeShapeAggTable(b, "Traffic Shaping By User / Project", []string{"User", "Project"}, callerByUser, splitKey2)
	writeShapeAggTable(b, "Traffic Shaping By Key", []string{"Token ID"}, callerByKey, splitKey1)
	writeShapeAggTable(b, "Traffic Shaping By Client", []string{"Client"}, callerByClient, splitKey1)
	writeShapeAggTable(b, "Traffic Shaping By Model Group", []string{"Model Group"}, callerByGroup, splitKey1)
	writeShapeAggTable(b, "Provider Capacity Shaping", []string{"Provider", "Model", "Dialect"}, upstreamByProvider, splitKey3)
	writeShapeAggTable(b, "Provider Capacity Shaping By Bucket", []string{"Scope", "Bucket", "Decision"}, upstreamByBucket, splitKey3)
	writeShapeAggTable(b, "Adaptive Backoff", []string{"Reason"}, backoffByReason, splitKey1)
	writeBeforeAfterShapeHelpers(b, rows, upstreamEvents)
}

func addCallerShape(a *shapingMarkdownAgg, row usageRow) {
	a.Requests++
	switch row.TrafficShapeDecision {
	case trafficShapeDecisionRejected:
		a.Rejected++
	case trafficShapeDecisionQueued:
		a.Queued++
	}
	a.addRetry(row.TrafficShapeRetryAfterMS)
	a.addQueue(row.TrafficShapeQueueWaitMS)
	a.EstimatedInput += int64(row.TrafficShapeEstimatedInputTokens)
	a.ReservedOutput += int64(row.TrafficShapeReservedOutputTokens)
	a.TotalReserved += int64(row.TrafficShapeTotalReservedTokens)
}

func addUpstreamShape(a *shapingMarkdownAgg, joined upstreamShapeJoinedEvent) {
	a.Requests++
	event := joined.Event
	switch event.Decision {
	case shapeDecisionSkipped:
		a.SkippedTargets++
	case shapeDecisionCooldownStarted:
		a.CooldownsStarted++
	case shapeDecisionRejected:
		a.Rejected++
	}
	a.addRetry(event.RetryAfterMS)
	a.addQueue(event.QueueWaitMS)
	a.EstimatedInput += int64(event.EstimatedInputTokens)
	a.ReservedOutput += int64(event.ReservedOutputTokens)
	a.TotalReserved += int64(event.TotalReservedTokens)
	switch event.BackoffReason {
	case "adaptive-backoff-provider-429":
		a.Upstream429++
	case "adaptive-backoff-provider-quota":
		a.UpstreamQuota++
	}
	if joined.Row.FallbackUsed {
		a.Fallbacks++
	}
	if event.Decision == shapeDecisionSkipped && joined.Row.Status < 400 {
		a.RouteAroundSuccess++
	}
}

func getShapeAgg(m map[string]*shapingMarkdownAgg, key string) *shapingMarkdownAgg {
	if m[key] == nil {
		m[key] = &shapingMarkdownAgg{}
	}
	return m[key]
}

func writeShapeAggLine(b *strings.Builder, label string, a *shapingMarkdownAgg) {
	fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
		esc(label), a.Requests, a.Rejected, a.Queued, a.SkippedTargets, a.CooldownsStarted,
		avg(a.RetryAfterMS, a.RetryAfterCount), a.MaxRetryAfterMS,
		avg(a.QueueWaitMS, a.QueueWaitCount), percentileInt64(a.QueueWaitSamples, 50), percentileInt64(a.QueueWaitSamples, 95), a.MaxQueueWaitMS,
		a.EstimatedInput, a.ReservedOutput, a.TotalReserved)
}

func writeShapeAggTable(b *strings.Builder, title string, keyHeaders []string, data map[string]*shapingMarkdownAgg, split func(string) []string) {
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, h := range keyHeaders {
		fmt.Fprintf(b, "| %s ", h)
	}
	fmt.Fprintln(b, "| Events | Rejected | Queued | Skipped Targets | Cooldowns | Avg Retry-After ms | Max Retry-After ms | Avg Queue Wait ms | P50 Queue Wait ms | P95 Queue Wait ms | Max Queue Wait ms | Upstream 429 | Upstream Quota | Fallbacks | Route-Around OK | Total Reserved Tokens |")
	for range keyHeaders {
		fmt.Fprint(b, "|---")
	}
	fmt.Fprintln(b, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	if len(data) == 0 {
		for range keyHeaders {
			fmt.Fprint(b, "| _none_ ")
		}
		fmt.Fprintln(b, "| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |")
		fmt.Fprintln(b)
		return
	}
	for _, key := range sortedShapeKeys(data) {
		for _, part := range split(key) {
			fmt.Fprintf(b, "| %s ", esc(part))
		}
		a := data[key]
		fmt.Fprintf(b, "| %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			a.Requests, a.Rejected, a.Queued, a.SkippedTargets, a.CooldownsStarted,
			avg(a.RetryAfterMS, a.RetryAfterCount), a.MaxRetryAfterMS,
			avg(a.QueueWaitMS, a.QueueWaitCount), percentileInt64(a.QueueWaitSamples, 50), percentileInt64(a.QueueWaitSamples, 95), a.MaxQueueWaitMS,
			a.Upstream429, a.UpstreamQuota, a.Fallbacks, a.RouteAroundSuccess, a.TotalReserved)
	}
	fmt.Fprintln(b)
}

func percentileInt64(values []int64, percentile int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(float64(percentile)/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func sortedShapeKeys(data map[string]*shapingMarkdownAgg) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if data[keys[i]].Requests == data[keys[j]].Requests {
			return keys[i] < keys[j]
		}
		return data[keys[i]].Requests > data[keys[j]].Requests
	})
	return keys
}

func writeBeforeAfterShapeHelpers(b *strings.Builder, rows []usageRow, upstreamEvents []upstreamShapeJoinedEvent) {
	if len(rows) == 0 {
		return
	}
	mid := rows[0].TS.Add(rows[len(rows)-1].TS.Sub(rows[0].TS) / 2)
	before := &shapingMarkdownAgg{}
	after := &shapingMarkdownAgg{}
	for _, row := range rows {
		target := after
		if row.TS.Before(mid) {
			target = before
		}
		if row.Status == http.StatusTooManyRequests || row.Error == "upstream-rate-limited" {
			target.Upstream429++
		}
		if row.Error == "upstream-quota-exhausted" {
			target.UpstreamQuota++
		}
		if row.FallbackUsed {
			target.Fallbacks++
		}
	}
	for _, event := range upstreamEvents {
		target := after
		if event.Row.TS.Before(mid) {
			target = before
		}
		if event.Event.Decision == shapeDecisionSkipped && event.Row.Status < 400 {
			target.RouteAroundSuccess++
		}
	}
	fmt.Fprintln(b, "## Before/After Investigation Helpers")
	fmt.Fprintln(b)
	fmt.Fprintf(b, "Window split point UTC: `%s`\n\n", formatUsageTime(mid))
	fmt.Fprintln(b, "| Window | Upstream 429 / caller 429 | Upstream quota | Fallbacks | Successful route-arounds |")
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|")
	fmt.Fprintf(b, "| Before | %d | %d | %d | %d |\n", before.Upstream429, before.UpstreamQuota, before.Fallbacks, before.RouteAroundSuccess)
	fmt.Fprintf(b, "| After | %d | %d | %d | %d |\n\n", after.Upstream429, after.UpstreamQuota, after.Fallbacks, after.RouteAroundSuccess)
}

func writeCountTable(b *strings.Builder, title, keyHeader string, counts map[string]int64) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", title)
	fmt.Fprintf(b, "| %s | Count |\n", keyHeader)
	fmt.Fprintln(b, "|---|---:|")
	for _, key := range sortedCountKeys(counts) {
		fmt.Fprintf(b, "| %s | %d |\n", esc(key), counts[key])
	}
	fmt.Fprintln(b)
}

func writeDownstreamUserPerformanceTable(b *strings.Builder, data map[string]*agg) {
	fmt.Fprintln(b, "## Downstream User Performance")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Owner User | Project | Env | Client | Calls | Errors | Streams | Total Tokens | Output Tokens | Avg Latency ms | Max Latency ms | Avg TTFB ms | Max TTFB ms | Avg Downstream ms | Max Downstream ms | Avg Downstream Write Output tok/s | Avg Downstream Write Total tok/s | Fallbacks |")
	fmt.Fprintln(b, "|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, key := range sortedAggKeysByMetric(data, func(a *agg) int64 { return avg(a.LatencyMS, a.Calls) }) {
		parts := splitKey4(key)
		a := data[key]
		fmt.Fprintf(b, "| %s | %s | %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %s | %s | %d |\n",
			esc(parts[0]), esc(parts[1]), esc(parts[2]), esc(parts[3]),
			a.Calls, a.Errors, a.Streams, a.TotalTokens, a.OutputTokens,
			avg(a.LatencyMS, a.Calls), a.MaxLatencyMS, avg(a.TTFBMS, a.TTFBCount), a.MaxTTFBMS,
			avg(a.DownstreamMS, a.DownstreamMSCount), a.MaxDownstreamMS,
			fmtFloat(avgFloat(a.DownstreamOutputTPS, a.DownstreamOutputTPSCount)),
			fmtFloat(avgFloat(a.DownstreamTotalTPS, a.DownstreamTotalTPSCount)),
			a.Fallbacks)
	}
	if len(data) == 0 {
		fmt.Fprintln(b, "| _none_ |  |  |  | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | n/a | 0 |")
	}
	fmt.Fprintln(b)
}

func writeUpstreamEndpointPerformanceTable(b *strings.Builder, data map[string]*agg) {
	fmt.Fprintln(b, "## Upstream Endpoint Performance")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Provider | Model | Dialect | Calls | Errors | Attempts | Fallbacks | Streams | Total Tokens | Output Tokens | Cost USD | Avg Upstream ms | Max Upstream ms | Avg Latency ms | Max Latency ms | Avg TTFB ms | Max TTFB ms | Avg Upstream Output tok/s | Avg Upstream Total tok/s |")
	fmt.Fprintln(b, "|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, key := range sortedAggKeysByMetric(data, func(a *agg) int64 { return avg(a.UpstreamMS, a.UpstreamMSCount) }) {
		parts := splitKey3(key)
		a := data[key]
		fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %d | %d | %d | %d | %d | $%s | %d | %d | %d | %d | %d | %d | %s | %s |\n",
			esc(parts[0]), esc(parts[1]), esc(parts[2]),
			a.Calls, a.Errors, a.Attempts, a.Fallbacks, a.Streams, a.TotalTokens, a.OutputTokens, fmtUSD(a.TotalCostUSD),
			avg(a.UpstreamMS, a.UpstreamMSCount), a.MaxUpstreamMS,
			avg(a.LatencyMS, a.Calls), a.MaxLatencyMS, avg(a.TTFBMS, a.TTFBCount), a.MaxTTFBMS,
			fmtFloat(avgFloat(a.UpstreamOutputTPS, a.UpstreamOutputTPSCount)),
			fmtFloat(avgFloat(a.UpstreamTotalTPS, a.UpstreamTotalTPSCount)))
	}
	if len(data) == 0 {
		fmt.Fprintln(b, "| _none_ |  |  | 0 | 0 | 0 | 0 | 0 | 0 | 0 | $0.000000 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | n/a |")
	}
	fmt.Fprintln(b)
}

func writeRequestThroughputTable(b *strings.Builder, rows []usageRow, formatInstant func(time.Time) string) {
	fmt.Fprintln(b, "## Per-Request Throughput")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Time UTC | Caller IP | Request ID | Token ID | Model Group | Provider | Model | Status | Cache | Output Tokens | Total Tokens | Cost USD | Upstream ms | Downstream ms | Upstream Output tok/s | Upstream Total tok/s | Downstream Write Output tok/s | Downstream Write Total tok/s |")
	fmt.Fprintln(b, "|---|---|---|---|---|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, row := range rows {
		fmt.Fprintf(b, "| %s | `%s` | `%s` | `%s` | %s | %s | %s | %d | %s | %d | %d | $%s | %s | %s | %s | %s | %s | %s |\n",
			formatInstant(row.TS), esc(defaultString(row.CallerIP, "unknown")), esc(row.RequestID), esc(row.TokenID), esc(defaultString(row.ResolvedGroup, row.RequestedModel)),
			esc(row.TargetProvider), esc(row.TargetModel), row.Status, esc(row.Cache), row.OutputTokens, totalTokens(Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.TotalTokens}),
			fmtUSD(row.TotalCostUSD), fmtIntPtr(row.UpstreamMS), fmtIntPtr(row.DownstreamMS), fmtFloatPtr(row.UpstreamOutputTPS), fmtFloatPtr(row.UpstreamTotalTPS),
			fmtFloatPtr(row.DownstreamOutputTPS), fmtFloatPtr(row.DownstreamTotalTPS))
	}
	if len(rows) == 0 {
		fmt.Fprintln(b, "| _none_ |  |  |  |  |  |  | 0 |  | 0 | 0 | $0.000000 | n/a | n/a | n/a | n/a | n/a | n/a |")
	}
	fmt.Fprintln(b)
}

func getAgg(m map[string]*agg, key string) *agg {
	if m[key] == nil {
		m[key] = &agg{}
	}
	return m[key]
}

func sortedAggKeys(m map[string]*agg) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := m[keys[i]], m[keys[j]]
		if ai.TotalTokens != aj.TotalTokens {
			return ai.TotalTokens > aj.TotalTokens
		}
		if ai.Calls != aj.Calls {
			return ai.Calls > aj.Calls
		}
		return keys[i] < keys[j]
	})
	return keys
}

func sortedCountKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] == m[keys[j]] {
			return keys[i] < keys[j]
		}
		return m[keys[i]] > m[keys[j]]
	})
	return keys
}

func sortedAggKeysByMetric(m map[string]*agg, metric func(*agg) int64) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := m[keys[i]], m[keys[j]]
		mi, mj := metric(ai), metric(aj)
		if mi != mj {
			return mi > mj
		}
		if ai.MaxLatencyMS != aj.MaxLatencyMS {
			return ai.MaxLatencyMS > aj.MaxLatencyMS
		}
		if ai.Calls != aj.Calls {
			return ai.Calls > aj.Calls
		}
		return keys[i] < keys[j]
	})
	return keys
}

func joinKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}

func splitKey1(key string) []string {
	return []string{key}
}

func splitKey2(key string) []string {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) == 1 {
		return []string{parts[0], ""}
	}
	return parts
}

func splitKey3(key string) []string {
	return splitKeyN(key, 3)
}

func splitKey4(key string) []string {
	return splitKeyN(key, 4)
}

func splitKeyN(key string, n int) []string {
	parts := strings.SplitN(key, "\x00", n)
	for len(parts) < n {
		parts = append(parts, "")
	}
	return parts
}

func esc(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "|", `\|`)
	v = strings.ReplaceAll(v, "\n", " ")
	v = html.EscapeString(v)
	for _, ch := range []string{"[", "]", "(", ")", "`"} {
		v = strings.ReplaceAll(v, ch, `\`+ch)
	}
	if v == "" {
		return ""
	}
	return v
}

func avg(sum, n int64) int64 {
	if n <= 0 {
		return 0
	}
	return sum / n
}

func addFloat(v *float64, sum *float64, count *int64) {
	if v == nil {
		return
	}
	*sum += *v
	*count++
}

func avgFloat(sum float64, n int64) float64 {
	if n <= 0 {
		return -1
	}
	return sum / float64(n)
}

func ratioPct(part, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(part) * 100 / float64(total)
}

func fmtIntPtr(v *int64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprint(*v)
}

func fmtFloatPtr(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmtFloat(*v)
}

func fmtFloat(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", v)
}

func fmtUSD(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.6f", v)
}

func fmtPct(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", v)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func formatUsageTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// formatUsageMarkdownTime is the stable, presentation-only timestamp format
// for CLI Markdown report bounds and per-request rows. Storage and export
// timestamps retain nanosecond precision through formatUsageTime.
func formatUsageMarkdownTime(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func parseUsageTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, errors.New("empty time")
	}
	layouts := []string{"2006-01-02T15:04:05.000000000Z", "2006-01-02T15:04:05.000Z", time.RFC3339Nano, time.RFC3339}
	var last error
	for _, layout := range layouts {
		t, err := time.Parse(layout, v)
		if err == nil {
			return t.UTC(), nil
		}
		last = err
	}
	return time.Time{}, last
}
