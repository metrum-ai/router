// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

//go:embed admindist/index.html admindist/static
var embeddedAdminReports embed.FS

type adminReportFilters struct {
	From time.Time
	To   time.Time
	UsageReportOptions
	Limit     int
	Offset    int
	Sort      string
	Direction string
	Cursor    string
}

type adminReportResponse struct {
	Period       adminReportPeriod     `json:"period"`
	Summary      adminReportSummary    `json:"summary"`
	Series       []adminReportSeries   `json:"series"`
	Charts       []adminReportChart    `json:"charts"`
	ByToken      []adminReportTableRow `json:"byToken"`
	ByGroup      []adminReportTableRow `json:"byGroup"`
	ByProvider   []adminReportTableRow `json:"byProvider"`
	ByStatus     []adminReportTableRow `json:"byStatus"`
	Cache        adminReportCache      `json:"cache"`
	Requests     []adminReportRequest  `json:"requests"`
	Pagination   adminReportPagination `json:"pagination"`
	GeneratedUTC string                `json:"generatedUtc"`
}

type adminSavingsResponse struct {
	Period       adminReportPeriod         `json:"period"`
	Baseline     adminSavingsBaselineDTO   `json:"baseline"`
	Baselines    []adminSavingsBaselineDTO `json:"baselines"`
	Summary      adminSavingsRow           `json:"summary"`
	ByTime       []adminSavingsRow         `json:"byTime"`
	ByGroup      []adminSavingsRow         `json:"byGroup"`
	Charts       []adminReportChart        `json:"charts"`
	Warnings     []string                  `json:"warnings,omitempty"`
	Pagination   adminReportPagination     `json:"pagination"`
	GeneratedUTC string                    `json:"generatedUtc"`
}

type adminScalarReportResponse struct {
	Period       adminReportPeriod        `json:"period"`
	Report       string                   `json:"report"`
	Baseline     *adminSavingsBaselineDTO `json:"baseline,omitempty"`
	Summary      adminReportSummary       `json:"summary"`
	Rows         []adminScalarReportRow   `json:"rows"`
	Charts       []adminReportChart       `json:"charts,omitempty"`
	Requests     []adminReportRequest     `json:"requests,omitempty"`
	Pagination   adminReportPagination    `json:"pagination"`
	GeneratedUTC string                   `json:"generatedUtc"`
}

type adminSecurityReportResponse struct {
	Period       adminReportPeriod       `json:"period"`
	Report       string                  `json:"report"`
	Summary      adminSecuritySummary    `json:"summary"`
	Rows         []adminSecurityEventRow `json:"rows"`
	Charts       []adminReportChart      `json:"charts,omitempty"`
	Pagination   adminReportPagination   `json:"pagination"`
	GeneratedUTC string                  `json:"generatedUtc"`
}

type adminRetentionStatusResponse struct {
	GeneratedUTC string                   `json:"generatedUtc"`
	Enabled      bool                     `json:"enabled"`
	DryRun       bool                     `json:"dryRun"`
	LatestJob    *adminRetentionJobRow    `json:"latestJob,omitempty"`
	Tables       []adminRetentionTableRow `json:"tables"`
	Rollups      []adminRollupStatusRow   `json:"rollups"`
	Charts       []adminReportChart       `json:"charts,omitempty"`
}

type adminRetentionJobRow struct {
	JobID           uint   `json:"jobId"`
	PolicyVersionID uint   `json:"policyVersionId"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	DryRun          bool   `json:"dryRun"`
	StartedAt       string `json:"startedAt"`
	CompletedAt     string `json:"completedAt,omitempty"`
	RequestedBy     string `json:"requestedBy,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

type adminRetentionTableRow struct {
	DataClass     string `json:"dataClass"`
	TableName     string `json:"tableName"`
	Cutoff        string `json:"cutoff"`
	RetentionDays int    `json:"retentionDays"`
	BatchSize     int    `json:"batchSize"`
	CandidateRows int64  `json:"candidateRows"`
	HeldRows      int64  `json:"heldRows"`
	EligibleRows  int64  `json:"eligibleRows"`
	BlockedRows   int64  `json:"blockedRows"`
	DeletedRows   int64  `json:"deletedRows"`
	Status        string `json:"status"`
	Message       string `json:"message,omitempty"`
}

type adminRollupStatusRow struct {
	RunID              uint   `json:"runId"`
	RollupType         string `json:"rollupType"`
	Status             string `json:"status"`
	WindowStart        string `json:"windowStart"`
	WindowEnd          string `json:"windowEnd"`
	SourceRequestCount int64  `json:"sourceRequestCount"`
	DailyRows          int64  `json:"dailyRows"`
	RollupRows         int64  `json:"rollupRows"`
	CompletedAt        string `json:"completedAt,omitempty"`
	FinalizedAt        string `json:"finalizedAt,omitempty"`
}

type adminCatalogStatusResponse struct {
	GeneratedUTC string                  `json:"generatedUtc"`
	Summary      adminCatalogSummary     `json:"summary"`
	GroupSummary []adminGroupEligibility `json:"groupSummary,omitempty"`
	Rows         []adminCatalogStatusRow `json:"rows"`
	Charts       []adminReportChart      `json:"charts,omitempty"`
}

// adminMigrationStatusResponse intentionally exposes only checked-in contract
// metadata and scalar ledger/job status. It is not a migration control plane.
type adminMigrationStatusResponse struct {
	GeneratedUTC string                    `json:"generatedUtc"`
	Summary      map[string]any            `json:"summary"`
	Rows         []adminMigrationStatusRow `json:"rows"`
}

type adminMigrationStatusRow struct {
	Scope           string `json:"scope"`
	MigrationID     int    `json:"migrationId"`
	Name            string `json:"name"`
	Release         string `json:"release"`
	SchemaVersion   int    `json:"schemaVersion"`
	DataVersion     int    `json:"dataVersion"`
	State           string `json:"state"`
	RollbackClass   string `json:"rollbackClass"`
	MaintenanceMode string `json:"maintenanceMode"`
	ExecutionMode   string `json:"executionMode"`
	LockClass       string `json:"lockClass"`
	TimeoutClass    string `json:"timeoutClass"`
	DataJobKey      string `json:"dataJobKey,omitempty"`
	DataJobState    string `json:"dataJobState,omitempty"`
	DurationMS      int64  `json:"durationMs,omitempty"`
	ErrorClass      string `json:"errorClass,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
	ValidationState string `json:"validationState"`
	Postcondition   string `json:"postcondition"`
	RowsScanned     int64  `json:"rowsScanned"`
	RowsUpdated     int64  `json:"rowsUpdated"`
	RowsSkipped     int64  `json:"rowsSkipped"`
	RowsFailed      int64  `json:"rowsFailed"`
	Checkpoints     int    `json:"checkpoints"`
}

type adminReportVersionResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

type adminReportPagination struct {
	Limit      int    `json:"limit"`
	Returned   int    `json:"returned"`
	TotalCount *int64 `json:"total_count"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
	PrevCursor string `json:"prev_cursor,omitempty"`
	Sort       string `json:"sort"`
	Direction  string `json:"direction"`
	Mode       string `json:"mode"`
	Offset     *int   `json:"offset,omitempty"`
	Note       string `json:"note,omitempty"`
}

type adminReportCursorPayload struct {
	Version   int      `json:"v"`
	Endpoint  string   `json:"e"`
	Sort      string   `json:"s"`
	Direction string   `json:"d"`
	Values    []string `json:"vls"`
}

type adminUsagePageOptions struct {
	UsageReportOptions UsageReportOptions
	Limit              int
	Sort               string
	Direction          string
	Cursor             *adminReportCursorPayload
}

type adminUsagePage struct {
	Rows       []usageRow
	TotalCount int64
	HasMore    bool
}

type adminSecurityPageOptions struct {
	SecurityReportOptions SecurityReportOptions
	Limit                 int
	Sort                  string
	Direction             string
	Cursor                *adminReportCursorPayload
}

type adminSecurityPage struct {
	Events     []securityAccessEvent
	TotalCount int64
	HasMore    bool
}

type adminCatalogSummary struct {
	Providers                 int `json:"providers"`
	CatalogModels             int `json:"catalogModels"`
	ActiveTargets             int `json:"activeTargets"`
	ValidatedTargets          int `json:"validatedTargets"`
	PassedTargets             int `json:"passedTargets"`
	MissingPricing            int `json:"missingPricing"`
	InactiveMetadataSurfaces  int `json:"inactiveMetadataSurfaces"`
	TargetsWithInactiveSkins  int `json:"targetsWithInactiveSkins"`
	GroupsWithSingleSkinTools int `json:"groupsWithSingleSkinTools"`
}

type adminGroupEligibility struct {
	Group                              string `json:"group"`
	OpenAIChatTargets                  int    `json:"openaiChatTargets"`
	OpenAIChatToolTargets              int    `json:"openaiChatToolTargets"`
	OpenAIChatStructuredOutputTargets  int    `json:"openaiChatStructuredOutputTargets"`
	OpenAIChatImageTargets             int    `json:"openaiChatImageTargets"`
	OpenAIChatImageToolTargets         int    `json:"openaiChatImageToolTargets"`
	OpenAIChatReasoningTargets         int    `json:"openaiChatReasoningTargets"`
	OpenAIResponsesTargets             int    `json:"openaiResponsesTargets"`
	OpenAIResponsesToolTargets         int    `json:"openaiResponsesToolTargets"`
	OpenAIResponsesStructuredTargets   int    `json:"openaiResponsesStructuredOutputTargets"`
	OpenAIResponsesImageTargets        int    `json:"openaiResponsesImageTargets"`
	OpenAIResponsesImageToolTargets    int    `json:"openaiResponsesImageToolTargets"`
	OpenAIResponsesReasoningTargets    int    `json:"openaiResponsesReasoningTargets"`
	AnthropicMessagesTargets           int    `json:"anthropicMessagesTargets"`
	AnthropicMessagesToolTargets       int    `json:"anthropicMessagesToolTargets"`
	AnthropicMessagesStructuredTargets int    `json:"anthropicMessagesStructuredOutputTargets"`
	AnthropicMessagesImageTargets      int    `json:"anthropicMessagesImageTargets"`
	AnthropicMessagesImageToolTargets  int    `json:"anthropicMessagesImageToolTargets"`
	AnthropicMessagesReasoningTargets  int    `json:"anthropicMessagesReasoningTargets"`
}

type adminCatalogStatusRow struct {
	Source                   string   `json:"source"`
	Provider                 string   `json:"provider"`
	ModelRef                 string   `json:"modelRef,omitempty"`
	Model                    string   `json:"model"`
	Dialect                  string   `json:"dialect"`
	DisplayName              string   `json:"displayName,omitempty"`
	ActiveGroups             []string `json:"activeGroups"`
	ActiveTargetCount        int      `json:"activeTargetCount"`
	GroupTargetIndex         int      `json:"groupTargetIndex,omitempty"`
	ValidationStatus         string   `json:"validationStatus"`
	ValidationWorkload       string   `json:"validationWorkload,omitempty"`
	ValidationAgeBucket      string   `json:"validationAgeBucket,omitempty"`
	ValidatedAt              string   `json:"validatedAt,omitempty"`
	QualityScore             float64  `json:"qualityScore,omitempty"`
	PassRate                 float64  `json:"passRate,omitempty"`
	Harness                  string   `json:"harness,omitempty"`
	ContextTokens            int      `json:"contextTokens,omitempty"`
	InputModalities          []string `json:"inputModalities,omitempty"`
	OutputModalities         []string `json:"outputModalities,omitempty"`
	ToolSupport              []string `json:"toolSupport,omitempty"`
	ActiveEligibilitySkin    string   `json:"activeEligibilitySkin,omitempty"`
	EffectiveToolSupport     []string `json:"effectiveToolSupport,omitempty"`
	InactiveToolSupport      []string `json:"inactiveToolSupport,omitempty"`
	EffectiveStructured      bool     `json:"effectiveStructuredOutputs,omitempty"`
	EffectiveReasoning       bool     `json:"effectiveReasoning,omitempty"`
	EffectiveImageInput      bool     `json:"effectiveImageInput,omitempty"`
	EligibilityWarning       string   `json:"eligibilityWarning,omitempty"`
	InputPricePerMillionUSD  float64  `json:"inputPricePerMillionUsd,omitempty"`
	OutputPricePerMillionUSD float64  `json:"outputPricePerMillionUsd,omitempty"`
	PricingSource            string   `json:"pricingSource,omitempty"`
	PricingUpdatedAt         string   `json:"pricingUpdatedAt,omitempty"`
	PricingMissing           bool     `json:"pricingMissing"`
	HonorsMaxTokens          *bool    `json:"honorsMaxTokens,omitempty"`
	ForceStoreFalse          bool     `json:"forceStoreFalse,omitempty"`
	OutputTokenField         string   `json:"outputTokenField,omitempty"`
}

type adminSecuritySummary struct {
	Events       int64 `json:"events"`
	Allowed      int64 `json:"allowed"`
	Unauthorized int64 `json:"unauthorized"`
	Forbidden    int64 `json:"forbidden"`
	Denied       int64 `json:"denied"`
	Errors       int64 `json:"errors"`
	UniqueIPs    int64 `json:"uniqueIps"`
}

type adminSecurityEventRow struct {
	TimeUTC             string `json:"timeUtc"`
	RequestID           string `json:"requestId,omitempty"`
	EventType           string `json:"eventType"`
	Surface             string `json:"surface"`
	Method              string `json:"method"`
	Path                string `json:"path"`
	Status              int    `json:"status"`
	Outcome             string `json:"outcome"`
	Reason              string `json:"reason"`
	AuthSubject         string `json:"authSubject,omitempty"`
	AuthSource          string `json:"authSource"`
	CallerID            string `json:"callerId,omitempty"`
	CallerUser          string `json:"callerUser,omitempty"`
	Project             string `json:"project,omitempty"`
	TokenID             string `json:"tokenId,omitempty"`
	AdminSubject        string `json:"adminSubject,omitempty"`
	Client              string `json:"client,omitempty"`
	UserAgentFamily     string `json:"userAgentFamily,omitempty"`
	IPAddress           string `json:"ipAddress,omitempty"`
	IPSource            string `json:"ipSource,omitempty"`
	TrustedProxyApplied bool   `json:"trustedProxyApplied"`
	PrivateIP           bool   `json:"privateIp"`
	LoopbackIP          bool   `json:"loopbackIp"`
	ReservedIP          bool   `json:"reservedIp"`
	ModelGroup          string `json:"modelGroup,omitempty"`
	RequestedModel      string `json:"requestedModel,omitempty"`
	ResolvedGroup       string `json:"resolvedGroup,omitempty"`
	InputTokens         int    `json:"inputTokens"`
	OutputTokens        int    `json:"outputTokens"`
	TotalTokens         int    `json:"totalTokens"`
}

type adminScalarReportRow struct {
	Key                                  string   `json:"key"`
	SecondaryKey                         string   `json:"secondaryKey,omitempty"`
	Status                               int      `json:"status,omitempty"`
	ErrorClass                           string   `json:"errorClass,omitempty"`
	Provider                             string   `json:"provider,omitempty"`
	Model                                string   `json:"model,omitempty"`
	Dialect                              string   `json:"dialect,omitempty"`
	RequestShapeFingerprint              string   `json:"requestShapeFingerprint,omitempty"`
	ToolSchemaFingerprint                string   `json:"toolSchemaFingerprint,omitempty"`
	Requests                             int64    `json:"requests"`
	Errors                               int64    `json:"errors"`
	ErrorRatePct                         float64  `json:"errorRatePct"`
	Streams                              int64    `json:"streams"`
	Attempts                             int64    `json:"attempts"`
	RetryableAttempts                    int64    `json:"retryableAttempts,omitempty"`
	TimeoutAttempts                      int64    `json:"timeoutAttempts,omitempty"`
	Fallbacks                            int64    `json:"fallbacks"`
	FallbackRatePct                      float64  `json:"fallbackRatePct"`
	FallbackSucceeded                    int64    `json:"fallbackSucceeded,omitempty"`
	FallbackFailed                       int64    `json:"fallbackFailed,omitempty"`
	TerminalErrors                       int64    `json:"terminalErrors,omitempty"`
	UpstreamErrorDetails                 int64    `json:"upstreamErrorDetails,omitempty"`
	FieldsStrippedCount                  int64    `json:"fieldsStrippedCount,omitempty"`
	FieldsRewrittenCount                 int64    `json:"fieldsRewrittenCount,omitempty"`
	UnsupportedFieldsCount               int64    `json:"unsupportedFieldsCount,omitempty"`
	TranslationWarningCount              int64    `json:"translationWarningCount,omitempty"`
	AffectedUsers                        int64    `json:"affectedUsers,omitempty"`
	AffectedClients                      int64    `json:"affectedClients,omitempty"`
	CacheHits                            int64    `json:"cacheHits"`
	CacheMisses                          int64    `json:"cacheMisses"`
	CacheBypass                          int64    `json:"cacheBypass"`
	CacheHitRatePct                      float64  `json:"cacheHitRatePct"`
	InputTokens                          int64    `json:"inputTokens"`
	OutputTokens                         int64    `json:"outputTokens"`
	Tokens                               int64    `json:"tokens"`
	TotalTokens                          int64    `json:"totalTokens"`
	InputImageCount                      int64    `json:"inputImageCount"`
	InputImageTokens                     int64    `json:"inputImageTokens"`
	PIIFilteredRequests                  int64    `json:"piiFilteredRequests"`
	PIIFilterReplacements                int64    `json:"piiFilterReplacements"`
	CostUSD                              float64  `json:"costUsd"`
	ActualCostUSD                        *float64 `json:"actualCostUsd,omitempty"`
	InputCostUSD                         float64  `json:"inputCostUsd"`
	ImageCostUSD                         float64  `json:"imageCostUsd"`
	OutputCostUSD                        float64  `json:"outputCostUsd"`
	TotalCostUSD                         float64  `json:"totalCostUsd"`
	UpstreamReportedCostUSD              float64  `json:"upstreamReportedCostUsd"`
	BaselineCostUSD                      *float64 `json:"baselineCostUsd,omitempty"`
	SavingsUSD                           *float64 `json:"savingsUsd,omitempty"`
	SavingsPct                           *float64 `json:"savingsPct,omitempty"`
	AvgCostUSD                           float64  `json:"avgCostUsd"`
	AvgLatencyMS                         int64    `json:"avgLatencyMs"`
	MaxLatencyMS                         int64    `json:"maxLatencyMs"`
	AvgTTFBMS                            int64    `json:"avgTtfbMs"`
	MaxTTFBMS                            int64    `json:"maxTtfbMs"`
	AvgUpstreamMS                        int64    `json:"avgUpstreamMs"`
	MaxUpstreamMS                        int64    `json:"maxUpstreamMs"`
	AvgDownstreamMS                      int64    `json:"avgDownstreamMs"`
	MaxDownstreamMS                      int64    `json:"maxDownstreamMs"`
	AvgUpstreamTokensPerSec              float64  `json:"avgUpstreamTokensPerSec"`
	AvgUpstreamOutputTokensPerSec        float64  `json:"avgUpstreamOutputTokensPerSec"`
	AvgUpstreamTotalTokensPerSec         float64  `json:"avgUpstreamTotalTokensPerSec"`
	AvgDownstreamTokensPerSec            float64  `json:"avgDownstreamTokensPerSec"`
	AvgDownstreamWriteOutputTokensPerSec float64  `json:"avgDownstreamWriteOutputTokensPerSec"`
	AvgDownstreamWriteTotalTokensPerSec  float64  `json:"avgDownstreamWriteTotalTokensPerSec"`
	LatestCacheItems                     int64    `json:"latestCacheItems"`
	LatestCacheBytes                     int64    `json:"latestCacheBytes"`
	LatestCacheMaxBytes                  int64    `json:"latestCacheMaxBytes"`
	LatestCacheOccupancyPct              float64  `json:"latestCacheOccupancyPct"`
	Rejections                           int64    `json:"rejections,omitempty"`
	Queued                               int64    `json:"queued,omitempty"`
	SkippedTargets                       int64    `json:"skippedTargets,omitempty"`
	CooldownsStarted                     int64    `json:"cooldownsStarted,omitempty"`
	AvgRetryAfterMS                      int64    `json:"avgRetryAfterMs,omitempty"`
	MaxRetryAfterMS                      int64    `json:"maxRetryAfterMs,omitempty"`
	AvgQueueWaitMS                       int64    `json:"avgQueueWaitMs,omitempty"`
	P50QueueWaitMS                       int64    `json:"p50QueueWaitMs,omitempty"`
	P95QueueWaitMS                       int64    `json:"p95QueueWaitMs,omitempty"`
	MaxQueueWaitMS                       int64    `json:"maxQueueWaitMs,omitempty"`
	EstimatedInputTokens                 int64    `json:"estimatedInputTokens,omitempty"`
	ReservedOutputTokens                 int64    `json:"reservedOutputTokens,omitempty"`
	TotalReservedTokens                  int64    `json:"totalReservedTokens,omitempty"`
	Upstream429Attempts                  int64    `json:"upstream429Attempts,omitempty"`
	UpstreamQuotaAttempts                int64    `json:"upstreamQuotaAttempts,omitempty"`
	RouteAroundSuccesses                 int64    `json:"routeAroundSuccesses,omitempty"`
	Recommendation                       string   `json:"recommendation,omitempty"`
	Severity                             string   `json:"severity,omitempty"`
	Successes                            int64    `json:"successes,omitempty"`
	SuccessRatePct                       float64  `json:"successRatePct,omitempty"`
	Router429                            int64    `json:"router429,omitempty"`
	TrafficShaped                        int64    `json:"trafficShaped,omitempty"`
	Upstream400                          int64    `json:"upstream400,omitempty"`
	Upstream5xx                          int64    `json:"upstream5xx,omitempty"`
	UpstreamTimeouts                     int64    `json:"upstreamTimeouts,omitempty"`
	ClientCanceled                       int64    `json:"clientCanceled,omitempty"`
	SuccessAfterFallbacks                int64    `json:"successAfterFallbacks,omitempty"`
	ObservedValues                       string   `json:"observedValues,omitempty"`
	Threshold                            string   `json:"threshold,omitempty"`
	ConfigFields                         []string `json:"configFields,omitempty"`
	Explanation                          string   `json:"explanation,omitempty"`
	Docs                                 string   `json:"docs,omitempty"`
}

type adminSavingsBaselineDTO struct {
	BaselineID                       string  `json:"baseline_id"`
	BaselineName                     string  `json:"baseline_name"`
	PricingSource                    string  `json:"pricing_source"`
	PricingUpdatedAt                 string  `json:"pricing_updated_at"`
	BaselineInputPricePerMillionUSD  float64 `json:"baseline_input_price_per_million_usd"`
	BaselineOutputPricePerMillionUSD float64 `json:"baseline_output_price_per_million_usd"`
	Notes                            string  `json:"notes,omitempty"`
	Custom                           bool    `json:"custom,omitempty"`
}

type adminDecisionTelemetryDetail struct {
	ShapeFeatures       []decisionShapeFeatureRecord       `json:"shapeFeatures,omitempty"`
	Candidates          []decisionTargetCandidateRecord    `json:"candidates,omitempty"`
	FilterReasons       []decisionTargetFilterReasonRecord `json:"filterReasons,omitempty"`
	RoutingDecisions    []routingDecisionRecord            `json:"routingDecisions,omitempty"`
	RoutingSignals      []routingSignalRecord              `json:"routingSignals,omitempty"`
	DynamicScoreTerms   []dynamicScoreTermRecord           `json:"dynamicScoreTerms,omitempty"`
	PolicyExecutions    []policyExecutionRecord            `json:"policyExecutions,omitempty"`
	FallbackTransitions []fallbackTransitionRecord         `json:"fallbackTransitions,omitempty"`
	CacheReasons        []decisionCacheReasonRecord        `json:"cacheReasons,omitempty"`
}

type adminShapingTelemetryDetail struct {
	TrafficShapeEvents  []requestTrafficShapeEventRecord  `json:"trafficShapeEvents,omitempty"`
	UpstreamShapeEvents []requestUpstreamShapeEventRecord `json:"upstreamShapeEvents,omitempty"`
}

type adminRequestShapeTelemetryDetail struct {
	RequestShape      *requestShapeRecord                  `json:"requestShape,omitempty"`
	TokenEstimate     *requestTokenEstimateRecord          `json:"tokenEstimate,omitempty"`
	TranslationShapes []requestTranslationShapeRecord      `json:"translationShapes,omitempty"`
	FieldEvents       []requestTranslationFieldEventRecord `json:"fieldEvents,omitempty"`
}

type adminUpstreamErrorTelemetryDetail struct {
	Details []adminReportUpstreamErrorDetail `json:"details,omitempty"`
}

type adminEvidenceBundleResponse struct {
	Request                     adminReportRequest                `json:"request"`
	Attempts                    []adminReportAttempt              `json:"attempts"`
	Trace                       []adminReportTraceEvent           `json:"trace"`
	Errors                      []adminReportError                `json:"errors"`
	UpstreamErrorDetails        []adminReportUpstreamErrorDetail  `json:"upstreamErrorDetails"`
	ShapingTelemetry            adminShapingTelemetryDetail       `json:"shapingTelemetry"`
	RequestShapeTelemetry       adminRequestShapeTelemetryDetail  `json:"requestShapeTelemetry"`
	UpstreamErrorTelemetry      adminUpstreamErrorTelemetryDetail `json:"upstreamErrorTelemetry"`
	DecisionTelemetry           adminDecisionTelemetryDetail      `json:"decisionTelemetry"`
	EvidenceBundle              adminRequestEvidenceBundle        `json:"evidenceBundle"`
	DiagnosticCompleteness      string                            `json:"diagnosticCompleteness"`
	EvidenceSections            []adminEvidenceSection            `json:"evidenceSections"`
	DiagnosticCompletenessScore int                               `json:"diagnosticCompletenessScore"`
}

type adminRequestEvidenceBundle struct {
	RequestSummary      adminReportRequest             `json:"requestSummary"`
	CostAccounting      adminEvidenceCostAccounting    `json:"costAccounting"`
	AdmissionAndShaping adminEvidenceAdmissionShaping  `json:"admissionAndShaping"`
	TargetEligibility   adminEvidenceTargetEligibility `json:"targetEligibility"`
	Completeness        adminEvidenceCompleteness      `json:"completeness"`
	PrivacyBoundaries   []string                       `json:"privacyBoundaries"`
}

type adminEvidenceCostAccounting struct {
	InputTokens                        int     `json:"inputTokens"`
	OutputTokens                       int     `json:"outputTokens"`
	TotalTokens                        int     `json:"totalTokens"`
	InputImageCount                    int     `json:"inputImageCount"`
	InputImageTokens                   int     `json:"inputImageTokens"`
	InputPricePerMillionUSD            float64 `json:"inputPricePerMillionUsd"`
	OutputPricePerMillionUSD           float64 `json:"outputPricePerMillionUsd"`
	ImageInputPricePerMillionTokensUSD float64 `json:"imageInputPricePerMillionTokensUsd"`
	ImageInputPricePerImageUSD         float64 `json:"imageInputPricePerImageUsd"`
	InputCostUSD                       float64 `json:"inputCostUsd"`
	ImageCostUSD                       float64 `json:"imageCostUsd"`
	OutputCostUSD                      float64 `json:"outputCostUsd"`
	TotalCostUSD                       float64 `json:"totalCostUsd"`
	UpstreamReportedInputCostUSD       float64 `json:"upstreamReportedInputCostUsd"`
	UpstreamReportedOutputCostUSD      float64 `json:"upstreamReportedOutputCostUsd"`
	UpstreamReportedTotalCostUSD       float64 `json:"upstreamReportedTotalCostUsd"`
	PricingSource                      string  `json:"pricingSource,omitempty"`
	PricingUpdatedAt                   string  `json:"pricingUpdatedAt,omitempty"`
	StoredRequestTimeValues            bool    `json:"storedRequestTimeValues"`
	UnknownPricing                     bool    `json:"unknownPricing"`
}

type adminEvidenceAdmissionShaping struct {
	QuotaState                       string `json:"quotaState,omitempty"`
	KeyState                         string `json:"keyState,omitempty"`
	Cache                            string `json:"cache,omitempty"`
	CacheEnabled                     bool   `json:"cacheEnabled"`
	TrafficShapeApplied              bool   `json:"trafficShapeApplied"`
	TrafficShapeDecision             string `json:"trafficShapeDecision,omitempty"`
	TrafficShapeScope                string `json:"trafficShapeScope,omitempty"`
	TrafficShapeBucket               string `json:"trafficShapeBucket,omitempty"`
	TrafficShapeRetryAfterMS         int64  `json:"trafficShapeRetryAfterMs"`
	TrafficShapeQueueWaitMS          int64  `json:"trafficShapeQueueWaitMs"`
	TrafficShapeEstimatedInputTokens int    `json:"trafficShapeEstimatedInputTokens"`
	TrafficShapeReservedOutputTokens int    `json:"trafficShapeReservedOutputTokens"`
	TrafficShapeTotalReservedTokens  int    `json:"trafficShapeTotalReservedTokens"`
}

type adminEvidenceTargetEligibility struct {
	SelectedProvider        string `json:"selectedProvider,omitempty"`
	SelectedModel           string `json:"selectedModel,omitempty"`
	SelectedDialect         string `json:"selectedDialect,omitempty"`
	CandidateCount          int    `json:"candidateCount"`
	EligibleCandidateCount  int    `json:"eligibleCandidateCount"`
	FilterReasonCount       int    `json:"filterReasonCount"`
	RoutingDecisionCount    int    `json:"routingDecisionCount"`
	FallbackTransitionCount int    `json:"fallbackTransitionCount"`
}

type adminEvidenceCompleteness struct {
	Status   string                 `json:"status"`
	Score    int                    `json:"score"`
	Sections []adminEvidenceSection `json:"sections"`
}

type adminEvidenceSection struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Rows     int    `json:"rows"`
	Expected bool   `json:"expected"`
}

func optionalRequestShapeRecord(rec requestShapeRecord, ok bool) *requestShapeRecord {
	if !ok {
		return nil
	}
	return &rec
}

func optionalRequestTokenEstimateRecord(rec requestTokenEstimateRecord, ok bool) *requestTokenEstimateRecord {
	if !ok {
		return nil
	}
	return &rec
}

type adminSavingsRow struct {
	Key             string  `json:"key"`
	Requests        int64   `json:"requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	ActualCostUSD   float64 `json:"actual_cost_usd"`
	BaselineCostUSD float64 `json:"baseline_cost_usd"`
	SavingsUSD      float64 `json:"savings_usd"`
	SavingsPct      float64 `json:"savings_pct"`
}

type adminReportPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type adminReportSummary struct {
	Requests                    int64    `json:"requests"`
	Errors                      int64    `json:"errors"`
	Tokens                      int64    `json:"tokens"`
	TotalTokens                 int64    `json:"totalTokens"`
	InputTokens                 int64    `json:"inputTokens"`
	OutputTokens                int64    `json:"outputTokens"`
	CostUSD                     float64  `json:"costUsd"`
	ActualCostUSD               *float64 `json:"actualCostUsd,omitempty"`
	InputCostUSD                float64  `json:"inputCostUsd"`
	ImageCostUSD                float64  `json:"imageCostUsd"`
	OutputCostUSD               float64  `json:"outputCostUsd"`
	TotalCostUSD                float64  `json:"totalCostUsd"`
	BaselineCostUSD             *float64 `json:"baselineCostUsd,omitempty"`
	SavingsUSD                  *float64 `json:"savingsUsd,omitempty"`
	SavingsPct                  *float64 `json:"savingsPct,omitempty"`
	Attempts                    int64    `json:"attempts"`
	Fallbacks                   int64    `json:"fallbacks"`
	Streams                     int64    `json:"streams"`
	AvgLatencyMS                int64    `json:"avgLatencyMs"`
	MaxLatencyMS                int64    `json:"maxLatencyMs"`
	AvgTTFBMS                   int64    `json:"avgTtfbMs"`
	AvgUpstreamTPS              float64  `json:"avgUpstreamTokensPerSec"`
	AvgUpstreamOutputTPS        float64  `json:"avgUpstreamOutputTokensPerSec"`
	AvgUpstreamTotalTPS         float64  `json:"avgUpstreamTotalTokensPerSec"`
	AvgDownstreamTPS            float64  `json:"avgDownstreamTokensPerSec"`
	AvgDownstreamWriteOutputTPS float64  `json:"avgDownstreamWriteOutputTokensPerSec"`
	AvgDownstreamWriteTotalTPS  float64  `json:"avgDownstreamWriteTotalTokensPerSec"`
}

type adminReportCache struct {
	Hits          int64   `json:"hits"`
	Misses        int64   `json:"misses"`
	Bypass        int64   `json:"bypass"`
	HitRate       float64 `json:"hitRate"`
	LatestItems   int64   `json:"latestItems"`
	LatestBytes   int64   `json:"latestBytes"`
	MaxBytes      int64   `json:"maxBytes"`
	LatestPercent float64 `json:"latestPercent"`
}

type adminReportSeries struct {
	TimeUTC             string  `json:"timeUtc"`
	Requests            int64   `json:"requests"`
	Successes           int64   `json:"successes"`
	Errors              int64   `json:"errors"`
	CostUSD             float64 `json:"costUsd"`
	BaselineCostUSD     float64 `json:"baselineCostUsd,omitempty"`
	SavingsUSD          float64 `json:"savingsUsd,omitempty"`
	SavingsPct          float64 `json:"savingsPct,omitempty"`
	Tokens              int64   `json:"tokens"`
	LatencyMS           int64   `json:"latencyMs"`
	TTFBMS              int64   `json:"ttfbMs"`
	UpstreamMS          int64   `json:"upstreamMs"`
	DownstreamMS        int64   `json:"downstreamMs"`
	UpstreamOutputTPS   float64 `json:"upstreamOutputTokensPerSec,omitempty"`
	UpstreamTotalTPS    float64 `json:"upstreamTotalTokensPerSec,omitempty"`
	DownstreamOutputTPS float64 `json:"downstreamOutputTokensPerSec,omitempty"`
	DownstreamTotalTPS  float64 `json:"downstreamTotalTokensPerSec,omitempty"`
	ErrorRatePct        float64 `json:"errorRatePct"`
	FallbackRatePct     float64 `json:"fallbackRatePct"`
	CacheHits           int64   `json:"cacheHits"`
	CacheMisses         int64   `json:"cacheMisses"`
	CacheBypass         int64   `json:"cacheBypass"`
	CacheHitRatePct     float64 `json:"cacheHitRatePct"`
	Fallbacks           int64   `json:"fallbacks"`
}

type adminReportChart struct {
	ChartID     string                   `json:"chart_id"`
	Title       string                   `json:"title"`
	XAxis       adminReportChartAxis     `json:"x_axis"`
	YAxis       adminReportChartAxis     `json:"y_axis"`
	Series      []adminReportChartSeries `json:"series"`
	GeneratedAt string                   `json:"generated_at"`
	From        string                   `json:"from"`
	To          string                   `json:"to"`
	Filters     adminReportFilterDTO     `json:"filters"`
}

type adminReportChartAxis struct {
	Label string `json:"label"`
	Type  string `json:"type"`
	Unit  string `json:"unit,omitempty"`
}

type adminReportChartSeries struct {
	Name     string                  `json:"name"`
	Unit     string                  `json:"unit"`
	ColorKey string                  `json:"color_key"`
	Points   []adminReportChartPoint `json:"points"`
}

type adminReportChartPoint struct {
	X       string  `json:"x"`
	XUnixMs *int64  `json:"x_unix_ms,omitempty"`
	Y       float64 `json:"y"`
}

type adminReportFilterDTO struct {
	CallerID          string `json:"caller_id,omitempty"`
	CallerIP          string `json:"caller_ip,omitempty"`
	TokenID           string `json:"token_id,omitempty"`
	TokenIDPrefix     string `json:"token_id_prefix,omitempty"`
	CallerUser        string `json:"caller_user,omitempty"`
	CallerProject     string `json:"caller_project,omitempty"`
	CallerEnvironment string `json:"caller_environment,omitempty"`
	RequestedModel    string `json:"requested_model,omitempty"`
	ResolvedGroup     string `json:"resolved_group,omitempty"`
	TargetProvider    string `json:"provider,omitempty"`
	TargetModel       string `json:"target_model,omitempty"`
	TargetDialect     string `json:"dialect,omitempty"`
	Status            int    `json:"status,omitempty"`
	Cache             string `json:"cache,omitempty"`
	Client            string `json:"client,omitempty"`
	ErrorClass        string `json:"error_class,omitempty"`
}

type adminReportTableRow struct {
	Key           string  `json:"key"`
	Requests      int64   `json:"requests"`
	Errors        int64   `json:"errors"`
	Tokens        int64   `json:"tokens"`
	TotalTokens   int64   `json:"totalTokens"`
	InputTokens   int64   `json:"inputTokens"`
	OutputTokens  int64   `json:"outputTokens"`
	CostUSD       float64 `json:"costUsd"`
	InputCostUSD  float64 `json:"inputCostUsd"`
	ImageCostUSD  float64 `json:"imageCostUsd"`
	OutputCostUSD float64 `json:"outputCostUsd"`
	TotalCostUSD  float64 `json:"totalCostUsd"`
	Attempts      int64   `json:"attempts"`
	Fallbacks     int64   `json:"fallbacks"`
	AvgLatencyMS  int64   `json:"avgLatencyMs"`
	MaxLatencyMS  int64   `json:"maxLatencyMs"`
}

type adminReportRequest struct {
	TimeUTC                         string  `json:"timeUtc"`
	RequestID                       string  `json:"requestId"`
	CallerID                        string  `json:"callerId"`
	CallerUser                      string  `json:"callerUser"`
	Project                         string  `json:"project"`
	Environment                     string  `json:"environment"`
	TokenID                         string  `json:"tokenId"`
	CallerIP                        string  `json:"callerIp"`
	Client                          string  `json:"client"`
	RequestedModel                  string  `json:"requestedModel"`
	ModelGroup                      string  `json:"modelGroup"`
	Provider                        string  `json:"provider"`
	Model                           string  `json:"model"`
	Dialect                         string  `json:"dialect"`
	Status                          int     `json:"status"`
	Error                           string  `json:"error,omitempty"`
	Cache                           string  `json:"cache"`
	Attempts                        int     `json:"attempts"`
	Fallback                        bool    `json:"fallback"`
	LatencyMS                       int64   `json:"latencyMs"`
	TTFBMS                          *int64  `json:"ttfbMs,omitempty"`
	UpstreamMS                      *int64  `json:"upstreamMs,omitempty"`
	DownstreamMS                    *int64  `json:"downstreamMs,omitempty"`
	Tokens                          int     `json:"tokens"`
	TotalTokens                     int     `json:"totalTokens"`
	InputTokens                     int     `json:"inputTokens"`
	OutputTokens                    int     `json:"outputTokens"`
	ReasoningTokens                 *int    `json:"reasoningTokens,omitempty"`
	ReasoningAttemptCount           int     `json:"reasoningAttemptCount"`
	ReasoningSuccessfulAttemptCount int     `json:"reasoningSuccessfulAttemptCount"`
	ReasoningReportedAttemptCount   int     `json:"reasoningReportedAttemptCount"`
	CostUSD                         float64 `json:"costUsd"`
	TotalCostUSD                    float64 `json:"totalCostUsd"`
}

type adminReportAttempt struct {
	RequestID        string `json:"requestId"`
	AttemptIndex     int    `json:"attemptIndex"`
	TimeUTC          string `json:"timeUtc"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Dialect          string `json:"dialect"`
	EndpointHost     string `json:"endpointHost"`
	DurationMS       int64  `json:"durationMs"`
	StatusCode       int    `json:"statusCode"`
	ErrorClass       string `json:"errorClass"`
	ErrorMessage     string `json:"errorMessage,omitempty"`
	Retryable        bool   `json:"retryable"`
	TimedOut         bool   `json:"timedOut"`
	ClientCanceled   bool   `json:"clientCanceled"`
	Selected         bool   `json:"selected"`
	FallbackReason   string `json:"fallbackReason,omitempty"`
	RequestBytes     int64  `json:"requestBytes"`
	ResponseBytes    int64  `json:"responseBytes"`
	AttemptTimeoutMS int    `json:"attemptTimeoutMs"`
	RetryAfterMS     int64  `json:"retryAfterMs"`
	ReasoningTokens  *int   `json:"reasoningTokens,omitempty"`
}

type adminReportTraceEvent struct {
	RequestID  string `json:"requestId"`
	Seq        int    `json:"seq"`
	TimeUTC    string `json:"timeUtc"`
	Event      string `json:"event"`
	Message    string `json:"message,omitempty"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Dialect    string `json:"dialect"`
	DurationMS int64  `json:"durationMs"`
	StatusCode int    `json:"statusCode"`
	ErrorClass string `json:"errorClass"`
	Retryable  bool   `json:"retryable"`
	Attempt    int    `json:"attempt"`
}

type adminReportError struct {
	RequestID    string `json:"requestId"`
	TimeUTC      string `json:"timeUtc"`
	Status       int    `json:"status"`
	ErrorType    string `json:"errorType"`
	ErrorClass   string `json:"errorClass"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Retryable    bool   `json:"retryable"`
	Attempts     int    `json:"attempts"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Dialect      string `json:"dialect"`
}

type adminReportUpstreamErrorDetail struct {
	RequestID    string `json:"requestId"`
	AttemptIndex int    `json:"attemptIndex"`
	Seq          int    `json:"seq"`
	TimeUTC      string `json:"timeUtc"`
	Status       int    `json:"status"`
	ErrorClass   string `json:"errorClass"`
	FieldName    string `json:"fieldName"`
	FieldValue   string `json:"fieldValue"`
	Source       string `json:"source"`
	Truncated    bool   `json:"truncated"`
}

type adminScalarEndpointSpec struct {
	Report       string
	Dimension    string
	Secondary    string
	Sort         string
	Requests     bool
	Anomalies    bool
	WithBaseline bool
	ShapeReport  string
	Diagnostic   string
}

func (s *Service) handleAdminReports(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Server.AdminReports.Enabled {
		http.NotFound(w, r)
		return
	}
	if s.adminReportBearerForbidden(w, r) {
		return
	}
	prefix := cleanAdminReportsPrefix(s.cfg.Server.AdminReports.PathPrefix)
	relPath := strings.TrimPrefix(r.URL.Path, prefix)
	securityReport := strings.HasPrefix(relPath, "/api/security/") || strings.HasPrefix(relPath, "/security/")
	subject, ok := s.authenticateAdminSubject(w, r)
	if !ok {
		s.recordAdminSecurityAccess(r, adminAuthSubject{}, http.StatusUnauthorized, "unauthorized", authzObjectAdminReports, authzActionRead)
		return
	}
	action := "read"
	if strings.HasSuffix(r.URL.Path, "/export.md") || strings.HasSuffix(r.URL.Path, "/export.csv") {
		action = "export"
	}
	if strings.HasPrefix(relPath, "/api/request/") || relPath == "/api/request-evidence" {
		action = authzActionDrilldown
	}
	object := authzObjectAdminReports
	if securityReport {
		object = authzObjectSecurityReports
	}
	if !s.authorizeAdmin(subject, object, action) {
		s.recordAdminSecurityAccess(r, subject, http.StatusForbidden, "reports-forbidden", object, action)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"type": "reports-forbidden", "message": "reports-forbidden"}})
		return
	}
	globalReports := s.authorizeGlobalAdmin(subject, object, action)
	s.recordAdminSecurityAccess(r, subject, http.StatusOK, "", object, action)
	s.setAdminReportHeaders(w, strings.HasPrefix(r.URL.Path, cleanAdminReportsPrefix(s.cfg.Server.AdminReports.PathPrefix)+"/static/"))
	scalarSpec, scalarOK := adminScalarEndpointSpecs(strings.TrimPrefix(r.URL.Path, prefix))
	switch {
	case r.URL.Path == prefix:
		http.Redirect(w, r, prefix+"/", http.StatusTemporaryRedirect)
	case r.URL.Path == prefix+"/" || r.URL.Path == prefix+"/usage":
		s.serveAdminReportAsset(w, r, "index.html")
	case strings.HasPrefix(r.URL.Path, prefix+"/static/"):
		s.serveAdminReportAsset(w, r, strings.TrimPrefix(r.URL.Path, prefix+"/"))
	case r.URL.Path == prefix+"/api/version":
		s.handleAdminReportVersion(w, r)
	case r.URL.Path == prefix+"/api/quota-status":
		s.handleAdminQuotaStatus(w, r, subject, globalReports)
	case r.URL.Path == prefix+"/api/summary":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportSummary(w, r, subject, globalReports)
	case r.URL.Path == prefix+"/api/overview":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportSummary(w, r, subject, globalReports)
	case r.URL.Path == prefix+"/api/savings":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportSavings(w, r, subject, globalReports)
	case scalarOK:
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminScalarEndpoint(w, r, scalarSpec, subject, globalReports)
	case r.URL.Path == prefix+"/api/security/events" || r.URL.Path == prefix+"/api/security/overview":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		if !s.requireAdminSecurityReports(w) {
			return
		}
		s.handleAdminSecurityEvents(w, r, subject, globalReports)
	case r.URL.Path == prefix+"/api/retention-status":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminRetentionStatus(w, r)
	case r.URL.Path == prefix+"/api/provider-catalog-status":
		s.handleAdminProviderCatalogStatus(w, r)
	case r.URL.Path == prefix+"/api/migrations":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminMigrationStatus(w, r)
	case r.URL.Path == prefix+"/security/export.csv":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		if !s.requireAdminSecurityReports(w) {
			return
		}
		s.handleAdminSecurityCSV(w, r, subject, globalReports)
	case r.URL.Path == prefix+"/api/requests":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportRequests(w, r, subject, globalReports)
	case strings.HasPrefix(r.URL.Path, prefix+"/api/request/"):
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportRequestDetail(w, r, strings.TrimPrefix(r.URL.Path, prefix+"/api/request/"), subject, globalReports)
	case r.URL.Path == prefix+"/api/request-evidence":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportRequestDetail(w, r, r.URL.Query().Get("request_id"), subject, globalReports)
	case r.URL.Path == prefix+"/export.md":
		if !s.requireAdminReportUsageStore(w) {
			return
		}
		s.handleAdminReportMarkdown(w, r, subject, globalReports)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) handleAdminMigrationStatus(w http.ResponseWriter, r *http.Request) {
	statusFn := s.usage.migrationStatus
	if s.migrationStatusFn != nil {
		statusFn = s.migrationStatusFn
	}
	status, err := statusFn()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "migration-status-unavailable", "message": "migration-status-unavailable"}})
		return
	}
	entries := make(map[int]MigrationLedgerEntry, len(status.Entries))
	for _, entry := range status.Entries {
		entries[entry.MigrationID] = entry
	}
	jobs := make(map[int]MigrationDataJobStatus, len(status.Jobs))
	for _, job := range status.Jobs {
		jobs[job.MigrationID] = job
	}
	rows := make([]adminMigrationStatusRow, 0, len(usageMigrationDefinitions))
	for _, definition := range usageMigrationDefinitions {
		entry, applied := entries[definition.ID]
		job, hasJob := jobs[definition.ID]
		row := adminMigrationStatusRow{Scope: definition.Scope, MigrationID: definition.ID, Name: definition.Name, Release: definition.Release, SchemaVersion: definition.SchemaVersion, DataVersion: definition.DataVersion, RollbackClass: definition.RollbackClass, MaintenanceMode: definition.MaintenanceMode, ExecutionMode: definition.ExecutionMode, LockClass: definition.LockClass, TimeoutClass: definition.TimeoutClass, DataJobKey: definition.DataJobKey, DurationMS: entry.DurationMS, ErrorClass: entry.ErrorCode, ErrorMessage: safeMigrationText(entry.ErrorText), StartedAt: formatUsageTime(entry.StartedAt), CompletedAt: formatUsageTime(entry.CompletedAt), Postcondition: definition.PostconditionKey, RowsScanned: job.RowsScanned, RowsUpdated: job.RowsUpdated, RowsSkipped: job.RowsSkipped, RowsFailed: job.RowsFailed, Checkpoints: job.Checkpoints}
		adminProjectMigrationRowState(&row, applied && entry.State == "applied", entry.State, hasJob && job.Present, job)
		if !adminMigrationRowMatches(row, r.URL.Query()) {
			continue
		}
		rows = append(rows, row)
	}
	summary := map[string]any{"scope": status.Scope, "schemaVersion": status.SchemaVersion, "dataVersion": status.DataVersion, "compatible": status.Compatible, "state": adminMigrationSummaryState(status, rows), "jobs": len(status.Jobs), "pending": 0, "inProgress": 0, "failed": 0, "verified": 0, "missingDataJobs": 0}
	for _, row := range rows {
		switch row.State {
		case "pending":
			summary["pending"] = summary["pending"].(int) + 1
		case "running", "in-progress":
			summary["inProgress"] = summary["inProgress"].(int) + 1
		case "failed":
			summary["failed"] = summary["failed"].(int) + 1
		case "applied":
			summary["verified"] = summary["verified"].(int) + 1
		}
		if row.DataJobState == "missing" {
			summary["missingDataJobs"] = summary["missingDataJobs"].(int) + 1
		}
	}
	writeJSON(w, http.StatusOK, adminMigrationStatusResponse{GeneratedUTC: formatUsageTime(time.Now().UTC()), Summary: summary, Rows: rows})
}

// adminProjectMigrationRowState makes a bound data job authoritative only after
// the schema ledger records applied. Failed or running schema work is the
// authoritative effective state even if a contradictory job record exists.
// An applied schema is not verified until its declared data job durably
// validates.
func adminProjectMigrationRowState(row *adminMigrationStatusRow, ledgerApplied bool, ledgerState string, hasJob bool, job MigrationDataJobStatus) {
	row.State = "pending"
	if ledgerState != "" {
		row.State = ledgerState
	}
	if !ledgerApplied || row.DataJobKey == "" {
		if hasJob {
			row.DataJobState = safeMigrationText(job.State)
		}
		row.ValidationState = adminMigrationValidationState(row.State)
		return
	}
	if !hasJob {
		row.DataJobState = "missing"
		row.State = "pending"
		row.ValidationState = "not-validated"
		return
	}
	row.DataJobState = safeMigrationText(job.State)
	if job.ErrorClass != "" {
		row.ErrorClass = safeMigrationText(job.ErrorClass)
		row.ErrorMessage = safeMigrationText(job.ErrorClass)
	}
	switch job.State {
	case migrationDataJobValidated:
		row.State = "applied"
	case migrationDataJobRunning:
		row.State = "in-progress"
	case migrationDataJobFailed:
		row.State = "failed"
	case migrationDataJobPending, migrationDataJobPaused, migrationDataJobCancelled:
		row.State = "pending"
	default:
		row.State = "incompatible"
	}
	row.ValidationState = adminMigrationValidationState(row.State)
}

func adminMigrationValidationState(state string) string {
	if validation, ok := map[string]string{"applied": "verified", "pending": "not-validated", "running": "in-progress", "in-progress": "in-progress", "failed": "failed"}[state]; ok {
		return validation
	}
	return "incompatible"
}

func adminMigrationSummaryState(status MigrationStatus, rows []adminMigrationStatusRow) string {
	if !status.Compatible || status.State == "incompatible" {
		return "incompatible"
	}
	for _, wanted := range []string{"failed", "running", "in-progress", "pending"} {
		for _, row := range rows {
			if row.State == wanted {
				return wanted
			}
		}
	}
	return "current"
}

func adminMigrationRowMatches(row adminMigrationStatusRow, q url.Values) bool {
	for key, actual := range map[string]string{"scope": row.Scope, "release": row.Release, "state": row.State, "type": row.ExecutionMode, "date": row.StartedAt} {
		if want := strings.TrimSpace(q.Get(key)); want != "" && !strings.Contains(strings.ToLower(actual), strings.ToLower(want)) {
			return false
		}
	}
	return true
}

func (s *Service) handleAdminReportVersion(w http.ResponseWriter, r *http.Request) {
	info := buildinfo.Current()
	writeJSON(w, http.StatusOK, adminReportVersionResponse{
		Version:   info.Version,
		Commit:    info.Commit,
		BuildDate: info.BuildDate,
		GoVersion: info.GoVersion,
		GOOS:      info.GOOS,
		GOARCH:    info.GOARCH,
	})
}

func adminReportUsesSavingsBaseline(relPath string) bool {
	if relPath == "/api/savings" {
		return true
	}
	spec, ok := adminScalarEndpointSpecs(relPath)
	return ok && spec.WithBaseline
}

func adminScalarEndpointSpecs(path string) (adminScalarEndpointSpec, bool) {
	specs := map[string]adminScalarEndpointSpec{
		"/api/savings-by-user":           {Report: "savings-by-user", Dimension: "caller_user", Sort: "savings", WithBaseline: true},
		"/api/savings-by-key":            {Report: "savings-by-key", Dimension: "token_id", Sort: "savings", WithBaseline: true},
		"/api/savings-by-group":          {Report: "savings-by-group", Dimension: "model_group", Sort: "savings", WithBaseline: true},
		"/api/savings-by-project":        {Report: "savings-by-project", Dimension: "project", Secondary: "environment", Sort: "savings", WithBaseline: true},
		"/api/savings-by-provider-model": {Report: "savings-by-provider-model", Dimension: "provider_model", Secondary: "dialect", Sort: "savings", WithBaseline: true},
		"/api/model-groups-by-user":      {Report: "model-groups-by-user", Dimension: "caller_user", Secondary: "model_group", Sort: "requests"},
		"/api/usage-by-key":              {Report: "usage-by-key", Dimension: "token_id", Sort: "cost"},
		"/api/usage-by-caller":           {Report: "usage-by-caller", Dimension: "caller_id", Secondary: "project", Sort: "cost"},
		"/api/requested-models":          {Report: "requested-models", Dimension: "requested_model", Secondary: "model_group", Sort: "requests"},
		"/api/provider-model-mix":        {Report: "provider-model-mix", Dimension: "provider_model", Secondary: "dialect", Sort: "tokens"},
		"/api/latency-throughput":        {Report: "latency-throughput", Dimension: "provider_model", Secondary: "client", Sort: "latency"},
		"/api/errors-fallbacks":          {Report: "errors-fallbacks", Dimension: "status_error", Secondary: "provider_model", Sort: "errors"},
		"/api/upstream-failures":         {Report: "upstream-failures", Sort: "errors", Diagnostic: "upstream_failures"},
		"/api/request-shape-failures":    {Report: "request-shape-failures", Sort: "errors", Diagnostic: "request_shape_mismatches"},
		"/api/request-shape-mismatches":  {Report: "request-shape-mismatches", Sort: "errors", Diagnostic: "request_shape_mismatches"},
		"/api/fallback-health":           {Report: "fallback-health", Sort: "fallbacks", Diagnostic: "fallback_health"},
		"/api/user-client-impact":        {Report: "user-client-impact", Sort: "errors", Diagnostic: "client_impact"},
		"/api/client-impact":             {Report: "client-impact", Sort: "errors", Diagnostic: "client_impact"},
		"/api/cache":                     {Report: "cache", Dimension: "cache", Secondary: "model_group", Sort: "requests"},
		"/api/quotas-budgets":            {Report: "quotas-budgets", Dimension: "quota_key_state", Secondary: "token_id", Sort: "requests"},
		"/api/troubleshooting-buckets":   {Report: "troubleshooting-buckets", Dimension: "troubleshooting_bucket", Secondary: "provider_model", Sort: "requests"},
		"/api/routing-decisions":         {Report: "routing-decisions", Dimension: "routing_decision", Secondary: "provider_model", Sort: "requests"},
		"/api/dynamic-signals":           {Report: "dynamic-signals", Dimension: "dynamic_signal", Secondary: "model_group", Sort: "requests"},
		"/api/dynamic-score-buckets":     {Report: "dynamic-score-buckets", Dimension: "dynamic_score_bucket", Secondary: "model_group", Sort: "requests"},
		"/api/dynamic-thresholds":        {Report: "dynamic-thresholds", Dimension: "dynamic_threshold", Secondary: "model_group", Sort: "requests"},
		"/api/max-token-buckets":         {Report: "max-token-buckets", Dimension: "max_token_bucket", Secondary: "model_group", Sort: "requests"},
		"/api/input-token-buckets":       {Report: "input-token-buckets", Dimension: "input_token_bucket", Secondary: "model_group", Sort: "requests"},
		"/api/admission-reasons":         {Report: "admission-reasons", Dimension: "admission_reason", Secondary: "model_group", Sort: "requests"},
		"/api/contract-buckets":          {Report: "contract-buckets", Dimension: "contract_bucket", Secondary: "model_group", Sort: "requests"},
		"/api/contract-workloads":        {Report: "contract-workloads", Dimension: "contract_workload", Secondary: "model_group", Sort: "requests"},
		"/api/target-validation":         {Report: "target-validation", Dimension: "target_validation", Secondary: "provider_model", Sort: "requests"},
		"/api/expensive-requests":        {Report: "expensive-requests", Sort: "cost", Requests: true},
		"/api/client-breakdown":          {Report: "client-breakdown", Dimension: "client", Secondary: "inbound_dialect", Sort: "requests"},
		"/api/project-chargeback":        {Report: "project-chargeback", Dimension: "project", Secondary: "environment", Sort: "cost"},
		"/api/capability-usage":          {Report: "capability-usage", Dimension: "capability", Secondary: "model_group", Sort: "image"},
		"/api/anomalies":                 {Report: "anomalies", Dimension: "anomaly", Secondary: "provider_model", Sort: "requests", Anomalies: true},
		"/api/traffic-shaping-overview":  {Report: "traffic-shaping-overview", Dimension: "shape_surface", Secondary: "shape_bucket", Sort: "requests", ShapeReport: "overview"},
		"/api/traffic-shaping-by-user":   {Report: "traffic-shaping-by-user", Dimension: "caller_user", Secondary: "shape_bucket", Sort: "requests", ShapeReport: "caller"},
		"/api/traffic-shaping-by-key":    {Report: "traffic-shaping-by-key", Dimension: "token_id", Secondary: "shape_bucket", Sort: "requests", ShapeReport: "caller"},
		"/api/traffic-shaping-by-client": {Report: "traffic-shaping-by-client", Dimension: "client", Secondary: "shape_bucket", Sort: "requests", ShapeReport: "caller"},
		"/api/traffic-shaping-by-group":  {Report: "traffic-shaping-by-group", Dimension: "model_group", Secondary: "shape_bucket", Sort: "requests", ShapeReport: "caller"},
		"/api/provider-capacity-shaping": {Report: "provider-capacity-shaping", Dimension: "provider_model", Secondary: "shape_bucket", Sort: "requests", ShapeReport: "upstream"},
		"/api/adaptive-upstream-backoff": {Report: "adaptive-upstream-backoff", Dimension: "backoff_reason", Secondary: "provider_model", Sort: "requests", ShapeReport: "adaptive"},
		"/api/traffic-tuning-advisor":    {Report: "traffic-tuning-advisor", Sort: "severity"},
	}
	spec, ok := specs[path]
	return spec, ok
}

func (s *Service) requireAdminReportUsageStore(w http.ResponseWriter) bool {
	if s.usage != nil {
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "reports-disabled", "message": "reports-disabled"}})
	return false
}

func (s *Service) requireAdminSecurityReports(w http.ResponseWriter) bool {
	if s.cfg.Server.AdminReports.Security.Enabled {
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"type": "security-reports-disabled", "message": "security-reports-disabled"}})
	return false
}

func (s *Service) adminReportBearerForbidden(w http.ResponseWriter, r *http.Request) bool {
	hasBearer := strings.HasPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	hasAPIKey := strings.TrimSpace(r.Header.Get("X-API-Key")) != ""
	if !hasBearer && !hasAPIKey {
		return false
	}
	caller, _, err := s.authenticate(r.Header.Get("Authorization"), r.Header.Get("X-API-Key"))
	if err != nil || caller == nil {
		return false
	}
	s.setAdminReportHeaders(w, false)
	subject := authzSubjectForCaller(caller)
	s.recordAdminSecurityAccess(r, adminAuthSubject{subject: subject.subject, domain: subject.domain, source: "caller_token"}, http.StatusForbidden, "reports-forbidden", authzObjectAdminReports, authzActionRead)
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"type": "reports-forbidden", "message": "reports-forbidden"}})
	return true
}

func (s *Service) authorizeAdmin(subject adminAuthSubject, object, action string) bool {
	return s.authorizer.enforce(authzSubjectForAdmin(subject), object, action)
}

func (s *Service) authorizeGlobalAdmin(subject adminAuthSubject, object, action string) bool {
	global := subject
	global.domain = "*"
	return s.authorizeAdmin(global, object, action)
}

func (s *Service) authorizeCaller(caller *callerRuntime, object, action string) bool {
	return s.authorizer.enforce(authzSubjectForCaller(caller), object, action)
}

func (s *Service) authorizeCallerInDomain(caller *callerRuntime, domain, object, action string) bool {
	return s.authorizer.enforceInDomain(authzSubjectForCaller(caller), domain, object, action)
}

func (s *Service) setAdminReportHeaders(w http.ResponseWriter, static bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	if static {
		w.Header().Set("Cache-Control", "private, max-age=3600")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (s *Service) writeAdminReportQueryFailed(w http.ResponseWriter, r *http.Request, report, handler string, filters *adminReportFilters, err error) {
	s.logAdminReportQueryFailure(r, report, handler, filters, err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"type": "report-query-failed", "message": "report-query-failed"}})
}

func (s *Service) logAdminReportQueryFailure(r *http.Request, report, handler string, filters *adminReportFilters, err error) {
	if err == nil {
		return
	}
	rec := adminReportQueryFailureLogRecord{
		EventType:      "admin_report_query_failed",
		Report:         report,
		Handler:        handler,
		DBDriver:       s.adminReportDBDriver(),
		AdminRequestID: adminReportFailureRequestID(r),
		ErrorClass:     adminReportErrorClass(err),
		ErrorMessage:   sanitizeAdminReportDiagnosticMessage(err.Error(), 512),
	}
	if pgErr := adminReportPGError(err); pgErr != nil {
		rec.PGCode = sanitizeAdminReportDiagnosticMessage(pgErr.Code, 32)
		rec.PGSeverity = sanitizeAdminReportDiagnosticMessage(pgErr.Severity, 64)
		rec.PGMessage = sanitizeAdminReportDiagnosticMessage(pgErr.Message, 512)
	}
	if filters != nil {
		rec.From = filters.From.UTC().Format(time.RFC3339)
		rec.To = filters.To.UTC().Format(time.RFC3339)
		rec.Limit = filters.Limit
		rec.Sort = filters.Sort
		rec.Direction = filters.Direction
	}
	s.logger.EmitAdminReportQueryFailure(rec)
}

func (s *Service) adminReportDBDriver() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return strings.ToLower(defaultString(s.cfg.Server.UsageDB.Driver, "sqlite"))
}

func adminReportFailureRequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	if id := strings.TrimSpace(r.URL.Query().Get("request_id")); id != "" {
		return id
	}
	const marker = "/api/request/"
	if idx := strings.Index(r.URL.Path, marker); idx >= 0 {
		return strings.TrimSpace(r.URL.Path[idx+len(marker):])
	}
	return ""
}

func adminReportErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if adminReportPGError(err) != nil {
		return "*pgconn.PgError"
	}
	t := reflect.TypeOf(err)
	if t == nil {
		return "error"
	}
	return t.String()
}

func adminReportPGError(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

func (s *Service) serveAdminReportAsset(w http.ResponseWriter, r *http.Request, name string) {
	sub, err := fs.Sub(embeddedAdminReports, "admindist")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !serveEmbeddedDoc(w, r, sub, path.Clean("/" + name)[1:]) {
		http.NotFound(w, r)
	}
}

func (s *Service) handleAdminReportSummary(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) {
	filters, ok := s.parseAdminReportFilters(w, r, false, subject, global)
	if !ok {
		return
	}
	if !validateAdminTopNPageParams(w, filters) {
		return
	}
	filters.Sort = normalizeAdminAggregateSortOrDefault(filters.Sort, "savings")
	baseline := s.adminOverviewBaseline(r)
	overview, err := s.usage.adminOverviewSQL(filters.UsageReportOptions, baseline, filters.Limit)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "summary", "handleAdminReportSummary", &filters, err)
		return
	}
	resp := buildAdminReportResponseSQL(filters, overview, baseline)
	if s.adminOverviewCanIncludeSecurityCharts(subject) {
		globalSecurity := s.authorizeGlobalAdmin(subject, authzObjectSecurityReports, authzActionRead)
		charts, err := s.adminOverviewSecurityCharts(filters, subject, globalSecurity)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, "overview-security", "handleAdminReportSummary", &filters, err)
			return
		}
		resp.Charts = append(resp.Charts, charts...)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) adminOverviewCanIncludeSecurityCharts(subject adminAuthSubject) bool {
	return s.cfg.Server.AdminReports.Security.Enabled && s.authorizeAdmin(subject, authzObjectSecurityReports, authzActionRead)
}

func (s *Service) adminOverviewSecurityCharts(filters adminReportFilters, subject adminAuthSubject, global bool) ([]adminReportChart, error) {
	opts := SecurityReportOptions{
		From:              filters.From,
		To:                filters.To,
		Limit:             filters.Limit,
		CallerID:          filters.CallerID,
		CallerUser:        filters.CallerUser,
		CallerProject:     filters.CallerProject,
		CallerEnvironment: filters.CallerEnvironment,
		TokenID:           filters.TokenID,
		Client:            filters.Client,
	}
	if !global {
		applyAdminSecurityDomainScope(&opts, subject.domain)
	}
	trends, err := s.usage.adminSecurityTrendBucketsSQL(opts)
	if err != nil {
		return nil, err
	}
	return adminSecurityTrendCharts(filters, formatUsageTime(time.Now().UTC()), trends), nil
}

func (s *Service) handleAdminReportSavings(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) {
	filters, ok := s.parseAdminReportFilters(w, r, false, subject, global)
	if !ok {
		return
	}
	if !validateAdminTopNPageParams(w, filters) {
		return
	}
	filters.Sort = normalizeAdminAggregateSortOrDefault(filters.Sort, "")
	baseline, ok := s.parseAdminSavingsBaseline(w, r)
	if !ok {
		return
	}
	resp, err := s.buildAdminSavingsResponseSQL(filters, baseline)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "savings", "handleAdminReportSavings", &filters, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleAdminScalarEndpoint(w http.ResponseWriter, r *http.Request, spec adminScalarEndpointSpec, subject adminAuthSubject, global bool) {
	filters, ok := s.parseAdminReportFilters(w, r, spec.Requests, subject, global)
	if !ok {
		return
	}
	if spec.Requests {
		if !validateAdminCursorPageParams(w, filters) {
			return
		}
		sortKey := normalizeAdminRequestSortOrDefault(filters.Sort, "costUsd")
		cursor, ok := s.adminReportCursorFromRequest(w, filters.Cursor, spec.Report, sortKey, filters.Direction)
		if !ok {
			return
		}
		page, err := s.usage.adminUsageRowsPage(adminUsagePageOptions{
			UsageReportOptions: filters.UsageReportOptions,
			Limit:              filters.Limit,
			Sort:               sortKey,
			Direction:          filters.Direction,
			Cursor:             cursor,
		})
		if err != nil {
			filters.Sort = sortKey
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		filters.Sort = sortKey
		resp := buildAdminScalarReportResponse(filters, page.Rows, spec, adminSavingsBaselineDTO{})
		resp.Pagination = s.adminCursorPagination(filters, spec.Report, page.Rows, page.TotalCount, page.HasMore, adminUsageCursorValues)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !validateAdminTopNPageParams(w, filters) {
		return
	}
	if spec.Diagnostic != "" {
		filters.Sort = normalizeAdminAggregateSortOrDefault(filters.Sort, spec.Sort)
		resp, err := s.buildAdminDiagnosticReportResponseSQL(filters, spec)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if spec.Report == "traffic-tuning-advisor" {
		advisorRows, total, hasMore, err := s.usage.trafficTuningAdvisorRowsSQL(filters.UsageReportOptions, filters.Limit)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		writeJSON(w, http.StatusOK, buildAdminTrafficTuningAdvisorResponse(filters, advisorRows, total, hasMore))
		return
	}
	if spec.ShapeReport != "" {
		filters.Sort = normalizeAdminAggregateSortOrDefault(filters.Sort, spec.Sort)
		resp, err := s.buildAdminShapeReportResponseSQL(filters, spec)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	var baseline adminSavingsBaselineDTO
	if spec.WithBaseline {
		var baselineOK bool
		baseline, baselineOK = s.parseAdminSavingsBaseline(w, r)
		if !baselineOK {
			return
		}
	}
	filters.Sort = normalizeAdminAggregateSortOrDefault(filters.Sort, "")
	if adminScalarSpecCanUseSQLMultiAgg(spec) {
		resp, err := s.buildAdminScalarMultiReportResponseSQL(filters, spec, baseline)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if adminScalarSpecCanUseSQLDerivedBucketAgg(spec) {
		resp, err := s.buildAdminScalarDerivedBucketReportResponseSQL(filters, spec, baseline)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if adminScalarSpecCanUseSQLAgg(spec) {
		resp, err := s.buildAdminScalarReportResponseSQL(filters, spec, baseline)
		if err != nil {
			s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	rows, err := s.adminScalarReportRows(filters.UsageReportOptions, spec)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, spec.Report, "handleAdminScalarEndpoint", &filters, err)
		return
	}
	writeJSON(w, http.StatusOK, buildAdminScalarReportResponse(filters, rows, spec, baseline))
}

func (s *Service) adminScalarReportRows(opts UsageReportOptions, spec adminScalarEndpointSpec) ([]usageRow, error) {
	if adminScalarSpecNeedsUsageBuckets(spec) {
		return s.usage.rows(opts)
	}
	return s.usage.rowsWithoutBuckets(opts)
}

func adminScalarSpecNeedsUsageBuckets(spec adminScalarEndpointSpec) bool {
	switch spec.Dimension {
	case "dynamic_signal", "dynamic_score_bucket", "dynamic_threshold", "max_token_bucket", "input_token_bucket", "admission_reason", "contract_bucket", "contract_workload", "target_validation":
		return true
	}
	switch spec.Secondary {
	case "dynamic_signal", "dynamic_score_bucket", "dynamic_threshold", "max_token_bucket", "input_token_bucket", "admission_reason", "contract_bucket", "contract_workload", "target_validation":
		return true
	}
	return false
}

func adminScalarSpecCanUseSQLAgg(spec adminScalarEndpointSpec) bool {
	if spec.Requests || spec.Anomalies || spec.Diagnostic != "" || spec.ShapeReport != "" {
		return false
	}
	_, ok := adminScalarDimensionSQLExpr(spec.Dimension)
	if !ok {
		return false
	}
	_, ok = adminScalarDimensionSQLExpr(spec.Secondary)
	return ok
}

func adminScalarSpecCanUseSQLMultiAgg(spec adminScalarEndpointSpec) bool {
	if spec.Requests || spec.Anomalies || spec.Diagnostic != "" || spec.ShapeReport != "" || spec.WithBaseline {
		return false
	}
	switch spec.Dimension {
	case "dynamic_signal", "dynamic_score_bucket", "dynamic_threshold", "max_token_bucket", "input_token_bucket":
	default:
		return false
	}
	_, ok := adminScalarDimensionSQLExprForAlias(spec.Secondary, "u")
	return ok
}

func adminScalarSpecCanUseSQLDerivedBucketAgg(spec adminScalarEndpointSpec) bool {
	if spec.Requests || spec.Diagnostic != "" || spec.ShapeReport != "" || spec.WithBaseline {
		return false
	}
	switch {
	case spec.Anomalies:
		return spec.Report == "anomalies" && spec.Dimension == "anomaly"
	case spec.Dimension == "troubleshooting_bucket":
		return spec.Report == "troubleshooting-buckets"
	case spec.Dimension == "capability":
		return spec.Report == "capability-usage"
	default:
		return false
	}
}

func (s *Service) buildAdminScalarMultiReportResponseSQL(filters adminReportFilters, spec adminScalarEndpointSpec, baseline adminSavingsBaselineDTO) (adminScalarReportResponse, error) {
	table, total, hasMore, err := s.usage.adminScalarMultiBucketAggsSQL(filters.UsageReportOptions, spec, defaultString(filters.Sort, spec.Sort), filters.Limit)
	if err != nil {
		return adminScalarReportResponse{}, err
	}
	resp := buildAdminScalarReportResponseFromAgg(filters, table, total, spec, baseline)
	resp.Pagination = adminTopNPagination(filters, len(resp.Rows), hasMore, "Aggregate rows are top-N for the selected filters.")
	return resp, nil
}

func (s *Service) buildAdminScalarDerivedBucketReportResponseSQL(filters adminReportFilters, spec adminScalarEndpointSpec, baseline adminSavingsBaselineDTO) (adminScalarReportResponse, error) {
	table, total, hasMore, err := s.usage.adminScalarDerivedBucketAggsSQL(filters.UsageReportOptions, spec, defaultString(filters.Sort, spec.Sort), filters.Limit)
	if err != nil {
		return adminScalarReportResponse{}, err
	}
	resp := buildAdminScalarReportResponseFromAgg(filters, table, total, spec, baseline)
	resp.Pagination = adminTopNPagination(filters, len(resp.Rows), hasMore, "Aggregate rows are top-N for the selected filters.")
	return resp, nil
}

func (s *Service) buildAdminScalarReportResponseSQL(filters adminReportFilters, spec adminScalarEndpointSpec, baseline adminSavingsBaselineDTO) (adminScalarReportResponse, error) {
	table, total, totalBaselineCost, hasMore, err := s.usage.adminScalarAggsSQL(filters.UsageReportOptions, spec, baseline, defaultString(filters.Sort, spec.Sort), filters.Limit)
	if err != nil {
		return adminScalarReportResponse{}, err
	}
	resp := buildAdminScalarReportResponseFromAgg(filters, table, total, spec, baseline)
	if baseline.BaselineID != "" {
		actualCost := resp.Summary.TotalCostUSD
		savings := totalBaselineCost - actualCost
		savingsPct := ratioPctFloat(savings, totalBaselineCost)
		resp.Summary.ActualCostUSD = &actualCost
		resp.Summary.BaselineCostUSD = &totalBaselineCost
		resp.Summary.SavingsUSD = &savings
		resp.Summary.SavingsPct = &savingsPct
	}
	resp.Pagination = adminTopNPagination(filters, len(resp.Rows), hasMore, "Aggregate rows are top-N for the selected filters.")
	return resp, nil
}

func (s *Service) buildAdminDiagnosticReportResponseSQL(filters adminReportFilters, spec adminScalarEndpointSpec) (adminScalarReportResponse, error) {
	table, total, hasMore, err := s.usage.adminDiagnosticAggsSQL(filters.UsageReportOptions, spec.Diagnostic, defaultString(filters.Sort, spec.Sort), filters.Limit)
	if err != nil {
		return adminScalarReportResponse{}, err
	}
	generatedAt := formatUsageTime(time.Now().UTC())
	reportRows := adminDiagnosticRowsFromAgg(table, defaultString(filters.Sort, spec.Sort), filters.Limit)
	resp := adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       spec.Report,
		Summary:      adminSummaryFromAgg(total),
		Rows:         reportRows,
		Charts:       adminDiagnosticCharts(filters, generatedAt, spec, reportRows),
		Pagination:   adminTopNPagination(filters, len(reportRows), hasMore, "Diagnostic aggregate rows are top-N for the selected filters."),
		GeneratedUTC: generatedAt,
	}
	return resp, nil
}

func (s *Service) buildAdminShapeReportResponseSQL(filters adminReportFilters, spec adminScalarEndpointSpec) (adminScalarReportResponse, error) {
	table, total, hasMore, err := s.usage.adminShapeAggsSQL(filters.UsageReportOptions, spec, defaultString(filters.Sort, spec.Sort), filters.Limit)
	if err != nil {
		return adminScalarReportResponse{}, err
	}
	generatedAt := formatUsageTime(time.Now().UTC())
	reportRows := adminShapeRowsFromAgg(table, defaultString(filters.Sort, spec.Sort), filters.Limit)
	resp := adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       spec.Report,
		Summary:      adminSummaryFromAgg(total),
		Rows:         reportRows,
		Charts:       adminShapeCharts(filters, generatedAt, spec, reportRows),
		Pagination:   adminTopNPagination(filters, len(reportRows), hasMore, "Traffic shaping aggregate rows are top-N for the selected filters."),
		GeneratedUTC: generatedAt,
	}
	return resp, nil
}

func (s *Service) handleAdminSecurityEvents(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) {
	opts, filters, ok := s.parseAdminSecurityFilters(w, r, subject, global)
	if !ok {
		return
	}
	if !validateAdminCursorPageParams(w, filters) {
		return
	}
	sortKey := normalizeAdminSecuritySortOrDefault(filters.Sort, "timeUtc")
	cursor, ok := s.adminReportCursorFromRequest(w, filters.Cursor, "security-events", sortKey, filters.Direction)
	if !ok {
		return
	}
	page, err := s.usage.adminSecurityEventsPage(adminSecurityPageOptions{
		SecurityReportOptions: opts,
		Limit:                 filters.Limit,
		Sort:                  sortKey,
		Direction:             filters.Direction,
		Cursor:                cursor,
	})
	if err != nil {
		filters.Sort = sortKey
		s.writeAdminReportQueryFailed(w, r, "security-events", "handleAdminSecurityEvents", &filters, err)
		return
	}
	filters.Sort = sortKey
	resp := buildAdminSecurityReportResponse(filters, page.Events)
	resp.Pagination = s.adminSecurityCursorPagination(filters, "security-events", page.Events, page.TotalCount, page.HasMore)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleAdminSecurityCSV(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) {
	opts, filters, ok := s.parseAdminSecurityFilters(w, r, subject, global)
	if !ok {
		return
	}
	events, err := s.usage.securityAccessEvents(opts)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "security-export", "handleAdminSecurityCSV", &filters, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="security-access-events.csv"`)
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time_utc", "request_id", "event_type", "surface", "method", "path", "status", "outcome", "reason", "auth_subject", "auth_source", "caller_id", "caller_user", "project", "token_id", "admin_subject", "client", "user_agent_family", "ip_address", "ip_source", "trusted_proxy_applied", "private_ip", "loopback_ip", "reserved_ip", "model_group", "requested_model", "resolved_group", "input_tokens", "output_tokens", "total_tokens"})
	for _, row := range adminSecurityRows(events) {
		_ = cw.Write(csvSafeRow([]string{row.TimeUTC, row.RequestID, row.EventType, row.Surface, row.Method, row.Path, strconv.Itoa(row.Status), row.Outcome, row.Reason, row.AuthSubject, row.AuthSource, row.CallerID, row.CallerUser, row.Project, row.TokenID, row.AdminSubject, row.Client, row.UserAgentFamily, row.IPAddress, row.IPSource, strconv.FormatBool(row.TrustedProxyApplied), strconv.FormatBool(row.PrivateIP), strconv.FormatBool(row.LoopbackIP), strconv.FormatBool(row.ReservedIP), row.ModelGroup, row.RequestedModel, row.ResolvedGroup, strconv.Itoa(row.InputTokens), strconv.Itoa(row.OutputTokens), strconv.Itoa(row.TotalTokens)}))
	}
	cw.Flush()
}

func csvSafeRow(row []string) []string {
	out := make([]string, len(row))
	for i, cell := range row {
		out[i] = csvSafeCell(cell)
	}
	return out
}

func csvSafeCell(cell string) string {
	trimmed := strings.TrimLeft(cell, " \t\r\n")
	if trimmed == "" {
		return cell
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + cell
	default:
		return cell
	}
}

func (s *Service) handleAdminRetentionStatus(w http.ResponseWriter, r *http.Request) {
	var latest retentionJobRecord
	var latestJob *adminRetentionJobRow
	err := s.usage.db.Order("started_at DESC, id DESC").First(&latest).Error
	if err == nil {
		latestJob = &adminRetentionJobRow{
			JobID:           latest.ID,
			PolicyVersionID: latest.PolicyVersionID,
			Mode:            latest.Mode,
			Status:          latest.Status,
			DryRun:          latest.DryRun,
			StartedAt:       latest.StartedAt,
			CompletedAt:     latest.CompletedAt,
			RequestedBy:     latest.RequestedBy,
			ErrorMessage:    sanitizePersistedDiagnosticText(latest.ErrorMessage),
		}
	}
	var tableRecords []retentionJobTableResultRecord
	if latestJob != nil {
		_ = s.usage.db.Where("job_id = ?", latest.ID).Order("data_class ASC, table_name ASC").Find(&tableRecords).Error
	}
	tables := make([]adminRetentionTableRow, 0, len(tableRecords))
	for _, record := range tableRecords {
		tables = append(tables, adminRetentionTableRow{
			DataClass:     record.DataClass,
			TableName:     record.StorageTable,
			Cutoff:        record.CutoffTS,
			RetentionDays: record.RetentionDays,
			BatchSize:     record.BatchSize,
			CandidateRows: record.CandidateRows,
			HeldRows:      record.HeldRows,
			EligibleRows:  record.EligibleRows,
			BlockedRows:   record.BlockedRows,
			DeletedRows:   record.DeletedRows,
			Status:        record.Status,
			Message:       sanitizePersistedDiagnosticText(record.Message),
		})
	}
	var rollupRecords []usageRollupRunRecord
	_ = s.usage.db.Order("window_end DESC, id DESC").Limit(20).Find(&rollupRecords).Error
	rollups := make([]adminRollupStatusRow, 0, len(rollupRecords))
	for _, record := range rollupRecords {
		rollups = append(rollups, adminRollupStatusRow{
			RunID:              record.ID,
			RollupType:         record.RollupType,
			Status:             record.Status,
			WindowStart:        record.WindowStart,
			WindowEnd:          record.WindowEnd,
			SourceRequestCount: record.SourceRequestCount,
			DailyRows:          record.DailyRowCount,
			RollupRows:         record.RollupRowCount,
			CompletedAt:        record.CompletedAt,
			FinalizedAt:        record.FinalizedAt,
		})
	}
	dryRun := s.cfg.Server.Retention.DryRun != nil && *s.cfg.Server.Retention.DryRun
	generatedAt := formatUsageTime(time.Now().UTC())
	writeJSON(w, http.StatusOK, adminRetentionStatusResponse{
		GeneratedUTC: generatedAt,
		Enabled:      s.cfg.Server.Retention.Enabled,
		DryRun:       dryRun,
		LatestJob:    latestJob,
		Tables:       tables,
		Rollups:      rollups,
		Charts:       adminRetentionStatusCharts(generatedAt, tables, rollups),
	})
}

func (s *Service) handleAdminProviderCatalogStatus(w http.ResponseWriter, r *http.Request) {
	resp := buildAdminCatalogStatusResponse(*s.cfg)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) parseAdminSavingsBaseline(w http.ResponseWriter, r *http.Request) (adminSavingsBaselineDTO, bool) {
	q := r.URL.Query()
	if strings.EqualFold(strings.TrimSpace(q.Get("baseline")), "custom") {
		name := strings.TrimSpace(q.Get("baseline_name"))
		if name == "" {
			name = "Custom baseline"
		}
		input, errIn := strconv.ParseFloat(strings.TrimSpace(q.Get("baseline_input_price_per_million_usd")), 64)
		output, errOut := strconv.ParseFloat(strings.TrimSpace(q.Get("baseline_output_price_per_million_usd")), 64)
		if errIn != nil || errOut != nil || input < 0 || output < 0 || input > 100000 || output > 100000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid custom baseline pricing"}})
			return adminSavingsBaselineDTO{}, false
		}
		return adminSavingsBaselineDTO{
			BaselineID:                       "custom",
			BaselineName:                     name,
			PricingSource:                    "custom-session",
			PricingUpdatedAt:                 formatUsageTime(time.Now().UTC()),
			BaselineInputPricePerMillionUSD:  input,
			BaselineOutputPricePerMillionUSD: output,
			Custom:                           true,
		}, true
	}
	baselineID := strings.TrimSpace(q.Get("baseline"))
	if baselineID == "" {
		baselineID = "gpt-5.5"
	}
	for _, baseline := range s.adminSavingsBaselines() {
		if baseline.BaselineID == baselineID {
			return baseline, true
		}
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "unknown savings baseline"}})
	return adminSavingsBaselineDTO{}, false
}

func (s *Service) adminSavingsBaselines() []adminSavingsBaselineDTO {
	configured := s.cfg.Server.AdminReports.Baselines
	if len(configured) == 0 {
		configured = defaultAdminReportBaselines()
	}
	out := make([]adminSavingsBaselineDTO, 0, len(configured))
	for _, baseline := range configured {
		out = append(out, adminSavingsBaselineDTO{
			BaselineID:                       baseline.ID,
			BaselineName:                     baseline.Name,
			PricingSource:                    baseline.PricingSource,
			PricingUpdatedAt:                 baseline.PricingUpdatedAt,
			BaselineInputPricePerMillionUSD:  baseline.InputPricePerMillionUSD,
			BaselineOutputPricePerMillionUSD: baseline.OutputPricePerMillionUSD,
			Notes:                            baseline.Notes,
		})
	}
	return out
}

func (s *Service) adminOverviewBaseline(r *http.Request) adminSavingsBaselineDTO {
	baselineID := strings.TrimSpace(r.URL.Query().Get("baseline"))
	if strings.EqualFold(baselineID, "custom") {
		input, errIn := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("baseline_input_price_per_million_usd")), 64)
		output, errOut := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("baseline_output_price_per_million_usd")), 64)
		if errIn == nil && errOut == nil && input >= 0 && output >= 0 && input <= 100000 && output <= 100000 {
			name := strings.TrimSpace(r.URL.Query().Get("baseline_name"))
			if name == "" {
				name = "Custom baseline"
			}
			return adminSavingsBaselineDTO{
				BaselineID:                       "custom",
				BaselineName:                     name,
				PricingSource:                    "custom-session",
				PricingUpdatedAt:                 formatUsageTime(time.Now().UTC()),
				BaselineInputPricePerMillionUSD:  input,
				BaselineOutputPricePerMillionUSD: output,
				Custom:                           true,
			}
		}
	}
	if baselineID == "" {
		baselineID = "gpt-5.5"
	}
	for _, baseline := range s.adminSavingsBaselines() {
		if baseline.BaselineID == baselineID {
			return baseline
		}
	}
	baselines := s.adminSavingsBaselines()
	if len(baselines) > 0 {
		return baselines[0]
	}
	return adminSavingsBaselineDTO{}
}

func (s *Service) handleAdminReportRequests(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) {
	filters, ok := s.parseAdminReportFilters(w, r, true, subject, global)
	if !ok {
		return
	}
	if !validateAdminCursorPageParams(w, filters) {
		return
	}
	sortKey := normalizeAdminRequestSortOrDefault(filters.Sort, "timeUtc")
	cursor, ok := s.adminReportCursorFromRequest(w, filters.Cursor, "requests", sortKey, filters.Direction)
	if !ok {
		return
	}
	page, err := s.usage.adminUsageRowsPage(adminUsagePageOptions{
		UsageReportOptions: filters.UsageReportOptions,
		Limit:              filters.Limit,
		Sort:               sortKey,
		Direction:          filters.Direction,
		Cursor:             cursor,
	})
	if err != nil {
		filters.Sort = sortKey
		s.writeAdminReportQueryFailed(w, r, "requests", "handleAdminReportRequests", &filters, err)
		return
	}
	filters.Sort = sortKey
	resp := buildAdminReportResponse(filters, page.Rows)
	resp.ByToken = nil
	resp.ByGroup = nil
	resp.ByProvider = nil
	resp.ByStatus = nil
	resp.Series = nil
	resp.Charts = nil
	resp.Pagination = s.adminCursorPagination(filters, "requests", page.Rows, page.TotalCount, page.HasMore, adminUsageCursorValues)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleAdminReportRequestDetail(w http.ResponseWriter, r *http.Request, requestID string, subject adminAuthSubject, global bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
		return
	}
	resp, found, err := s.adminRequestEvidenceResponse(requestID, subject, global)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "request-evidence", "handleAdminReportRequestDetail", nil, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) adminRequestEvidenceResponse(requestID string, subject adminAuthSubject, global bool) (adminEvidenceBundleResponse, bool, error) {
	var usage usageRecord
	if err := s.usage.db.Where("request_id = ?", requestID).First(&usage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return adminEvidenceBundleResponse{}, false, nil
		}
		return adminEvidenceBundleResponse{}, false, err
	}
	row, err := rowFromUsageRecord(usage)
	if err != nil {
		return adminEvidenceBundleResponse{}, false, err
	}
	if !global && !adminDomainAllowsUsageRow(subject.domain, row) {
		return adminEvidenceBundleResponse{}, false, nil
	}
	var attempts []requestAttemptRecord
	var traces []requestTraceEventRecord
	var errors []requestErrorRecord
	var upstreamErrorDetails []requestUpstreamErrorDetailRecord
	var shapeFeatures []decisionShapeFeatureRecord
	var candidates []decisionTargetCandidateRecord
	var filterReasons []decisionTargetFilterReasonRecord
	var routingDecisions []routingDecisionRecord
	var routingSignals []routingSignalRecord
	var dynamicScoreTerms []dynamicScoreTermRecord
	var policyExecutions []policyExecutionRecord
	var fallbackTransitions []fallbackTransitionRecord
	var cacheReasons []decisionCacheReasonRecord
	var trafficShapeEvents []requestTrafficShapeEventRecord
	var upstreamShapeEvents []requestUpstreamShapeEventRecord
	var requestShape requestShapeRecord
	var tokenEstimate requestTokenEstimateRecord
	var translationShapes []requestTranslationShapeRecord
	var translationFieldEvents []requestTranslationFieldEventRecord
	_ = s.usage.db.Where("request_id = ?", requestID).Order("attempt_index ASC").Find(&attempts).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&traces).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Find(&errors).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("attempt_index ASC, seq ASC").Find(&upstreamErrorDetails).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&trafficShapeEvents).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&upstreamShapeEvents).Error
	requestShapeFound := s.usage.db.Where("request_id = ?", requestID).First(&requestShape).Error == nil
	tokenEstimateFound := s.usage.db.Where("request_id = ?", requestID).First(&tokenEstimate).Error == nil
	_ = s.usage.db.Where("request_id = ?", requestID).Order("attempt_index ASC").Find(&translationShapes).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("attempt_index ASC, seq ASC").Find(&translationFieldEvents).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&shapeFeatures).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("candidate_index ASC").Find(&candidates).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&filterReasons).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&routingDecisions).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&routingSignals).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("rank ASC, seq ASC").Find(&dynamicScoreTerms).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&policyExecutions).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&fallbackTransitions).Error
	_ = s.usage.db.Where("request_id = ?", requestID).Order("seq ASC").Find(&cacheReasons).Error

	requestDTO := adminRequestFromRow(row)
	attemptDTOs := adminAttemptsFromRecords(attempts)
	traceDTOs := adminTraceFromRecords(traces)
	errorDTOs := adminErrorsFromRecords(errors)
	upstreamDetailRows := adminUpstreamErrorDetailsFromRecords(upstreamErrorDetails)
	shaping := adminShapingTelemetryDetail{
		TrafficShapeEvents:  trafficShapeEvents,
		UpstreamShapeEvents: upstreamShapeEvents,
	}
	requestShapeTelemetry := adminRequestShapeTelemetryDetail{
		RequestShape:      optionalRequestShapeRecord(requestShape, requestShapeFound),
		TokenEstimate:     optionalRequestTokenEstimateRecord(tokenEstimate, tokenEstimateFound),
		TranslationShapes: translationShapes,
		FieldEvents:       translationFieldEvents,
	}
	upstreamErrorTelemetry := adminUpstreamErrorTelemetryDetail{Details: upstreamDetailRows}
	decisionTelemetry := adminDecisionTelemetryDetail{
		ShapeFeatures:       shapeFeatures,
		Candidates:          candidates,
		FilterReasons:       filterReasons,
		RoutingDecisions:    routingDecisions,
		RoutingSignals:      routingSignals,
		DynamicScoreTerms:   dynamicScoreTerms,
		PolicyExecutions:    policyExecutions,
		FallbackTransitions: fallbackTransitions,
		CacheReasons:        cacheReasons,
	}
	sections, completeness, score := adminEvidenceCompletenessFor(row, s.cfg.Server.DecisionTelemetry.Enabled, requestShapeFound, attempts, traces, errors, upstreamErrorDetails, trafficShapeEvents, upstreamShapeEvents, translationShapes, translationFieldEvents, shapeFeatures, candidates, filterReasons, routingDecisions, routingSignals, dynamicScoreTerms, policyExecutions, fallbackTransitions, cacheReasons)
	evidence := adminEvidenceBundleFrom(row, requestDTO, sections, completeness, score, candidates, filterReasons, routingDecisions, fallbackTransitions)
	return adminEvidenceBundleResponse{
		Request:                     requestDTO,
		Attempts:                    attemptDTOs,
		Trace:                       traceDTOs,
		Errors:                      errorDTOs,
		UpstreamErrorDetails:        upstreamDetailRows,
		ShapingTelemetry:            shaping,
		RequestShapeTelemetry:       requestShapeTelemetry,
		UpstreamErrorTelemetry:      upstreamErrorTelemetry,
		DecisionTelemetry:           decisionTelemetry,
		EvidenceBundle:              evidence,
		DiagnosticCompleteness:      completeness,
		EvidenceSections:            sections,
		DiagnosticCompletenessScore: score,
	}, true, nil
}

func adminEvidenceBundleFrom(row usageRow, request adminReportRequest, sections []adminEvidenceSection, completeness string, score int, candidates []decisionTargetCandidateRecord, filterReasons []decisionTargetFilterReasonRecord, routingDecisions []routingDecisionRecord, fallbackTransitions []fallbackTransitionRecord) adminRequestEvidenceBundle {
	eligibleCandidates := 0
	for _, candidate := range candidates {
		if candidate.Eligible {
			eligibleCandidates++
		}
	}
	unknownPricing := row.PricingSource == "" && row.InputPricePerMillionUSD == 0 && row.OutputPricePerMillionUSD == 0 && row.ImageInputPricePerMillionTokensUSD == 0 && row.ImageInputPricePerImageUSD == 0 && row.TotalCostUSD == 0
	return adminRequestEvidenceBundle{
		RequestSummary: request,
		CostAccounting: adminEvidenceCostAccounting{
			InputTokens:                        row.InputTokens,
			OutputTokens:                       row.OutputTokens,
			TotalTokens:                        row.TotalTokens,
			InputImageCount:                    row.InputImageCount,
			InputImageTokens:                   row.InputImageTokens,
			InputPricePerMillionUSD:            row.InputPricePerMillionUSD,
			OutputPricePerMillionUSD:           row.OutputPricePerMillionUSD,
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
			StoredRequestTimeValues:            true,
			UnknownPricing:                     unknownPricing,
		},
		AdmissionAndShaping: adminEvidenceAdmissionShaping{
			QuotaState:                       row.QuotaState,
			KeyState:                         row.KeyState,
			Cache:                            row.Cache,
			CacheEnabled:                     row.CacheEnabled,
			TrafficShapeApplied:              row.TrafficShapeApplied,
			TrafficShapeDecision:             row.TrafficShapeDecision,
			TrafficShapeScope:                row.TrafficShapeScope,
			TrafficShapeBucket:               row.TrafficShapeBucket,
			TrafficShapeRetryAfterMS:         row.TrafficShapeRetryAfterMS,
			TrafficShapeQueueWaitMS:          row.TrafficShapeQueueWaitMS,
			TrafficShapeEstimatedInputTokens: row.TrafficShapeEstimatedInputTokens,
			TrafficShapeReservedOutputTokens: row.TrafficShapeReservedOutputTokens,
			TrafficShapeTotalReservedTokens:  row.TrafficShapeTotalReservedTokens,
		},
		TargetEligibility: adminEvidenceTargetEligibility{
			SelectedProvider:        row.TargetProvider,
			SelectedModel:           row.TargetModel,
			SelectedDialect:         row.TargetDialect,
			CandidateCount:          len(candidates),
			EligibleCandidateCount:  eligibleCandidates,
			FilterReasonCount:       len(filterReasons),
			RoutingDecisionCount:    len(routingDecisions),
			FallbackTransitionCount: len(fallbackTransitions),
		},
		Completeness: adminEvidenceCompleteness{
			Status:   completeness,
			Score:    score,
			Sections: sections,
		},
		PrivacyBoundaries: []string{
			"no raw prompts",
			"no raw responses",
			"no raw tool schemas or outputs",
			"no raw image URLs or payloads",
			"no provider keys",
			"no router tokens or token hashes",
			"no unsanitized upstream bodies",
		},
	}
}

func adminEvidenceCompletenessFor(row usageRow, decisionTelemetryEnabled bool, requestShapeFound bool, attempts []requestAttemptRecord, traces []requestTraceEventRecord, errors []requestErrorRecord, upstreamErrorDetails []requestUpstreamErrorDetailRecord, trafficShapeEvents []requestTrafficShapeEventRecord, upstreamShapeEvents []requestUpstreamShapeEventRecord, translationShapes []requestTranslationShapeRecord, translationFieldEvents []requestTranslationFieldEventRecord, shapeFeatures []decisionShapeFeatureRecord, candidates []decisionTargetCandidateRecord, filterReasons []decisionTargetFilterReasonRecord, routingDecisions []routingDecisionRecord, routingSignals []routingSignalRecord, dynamicScoreTerms []dynamicScoreTermRecord, policyExecutions []policyExecutionRecord, fallbackTransitions []fallbackTransitionRecord, cacheReasons []decisionCacheReasonRecord) ([]adminEvidenceSection, string, int) {
	sections := make([]adminEvidenceSection, 0, 9)
	add := func(name, status, reason string, rows int, expected bool) {
		sections = append(sections, adminEvidenceSection{Name: name, Status: status, Reason: reason, Rows: rows, Expected: expected})
	}
	addPresentOrMissing := func(name string, rows int, expected bool, missingReason string) {
		if rows > 0 {
			add(name, "present", "", rows, expected)
			return
		}
		if expected {
			add(name, "missing", missingReason, 0, true)
			return
		}
		add(name, "not_applicable", "section was not expected for this request path", 0, false)
	}

	add("request_summary", "present", "", 1, true)
	add("cost_accounting", "present", "stored request-time usage and cost values", 1, true)
	add("admission_and_shaping", evidenceStatus(row.TrafficShapeApplied || len(trafficShapeEvents) > 0 || len(upstreamShapeEvents) > 0), evidenceReason(row.TrafficShapeApplied || len(trafficShapeEvents) > 0 || len(upstreamShapeEvents) > 0, "traffic shaping evaluated or recorded", "traffic shaping was not applied or not enabled for this request"), len(trafficShapeEvents)+len(upstreamShapeEvents), row.TrafficShapeApplied || len(trafficShapeEvents) > 0 || len(upstreamShapeEvents) > 0)

	requestShapeRows := 0
	if requestShapeFound {
		requestShapeRows++
	}
	requestShapeRows += len(translationShapes) + len(translationFieldEvents)
	addPresentOrMissing("request_shape", requestShapeRows, row.Status >= 400, "errored requests should include safe shape telemetry or an explicit marker")

	targetRows := len(candidates) + len(filterReasons) + len(routingDecisions) + len(routingSignals) + len(dynamicScoreTerms) + len(policyExecutions) + len(fallbackTransitions) + len(cacheReasons)
	targetExpected := decisionTelemetryEnabled && (row.TargetProvider != "" || row.Attempts > 0 || row.Status >= 500)
	addPresentOrMissing("target_eligibility", targetRows, targetExpected, "target selection happened but candidate/filter/routing rows are absent")

	addPresentOrMissing("attempts", len(attempts), row.Attempts > 0, "usage row reports upstream attempts but request_attempts rows are absent")
	addPresentOrMissing("terminal_errors", len(errors), row.Status >= 400, "non-2xx request is missing request_errors rows")

	upstreamErrorExpected := false
	for _, attempt := range attempts {
		if attempt.StatusCode >= 400 || attempt.ErrorClass != "" || attempt.TimedOut || attempt.ClientCanceled {
			upstreamErrorExpected = true
			break
		}
	}
	addPresentOrMissing("sanitized_upstream_errors", len(upstreamErrorDetails), upstreamErrorExpected, "failed upstream attempt is missing sanitized provider error detail rows")
	addPresentOrMissing("trace_timeline", len(traces), row.Status >= 400 || row.FallbackUsed, "errored or fallback request is missing trace event rows")

	expected := 0
	present := 0
	missing := 0
	notApplicable := 0
	for _, section := range sections {
		if section.Expected {
			expected++
			if section.Status == "present" {
				present++
			}
			if section.Status == "missing" {
				missing++
			}
			continue
		}
		if section.Status == "not_applicable" {
			notApplicable++
		}
	}
	score := 100
	if expected > 0 {
		score = int(math.Round(float64(present) * 100 / float64(expected)))
	}
	status := "complete"
	switch {
	case missing > 0 && present <= 2:
		status = "minimal"
	case missing > 0:
		status = "partial_missing"
	case notApplicable > 0:
		status = "partial_expected"
	}
	return sections, status, score
}

func evidenceStatus(present bool) string {
	if present {
		return "present"
	}
	return "not_applicable"
}

func evidenceReason(present bool, presentReason, absentReason string) string {
	if present {
		return presentReason
	}
	return absentReason
}

func (s *Service) handleAdminReportMarkdown(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) {
	if !s.cfg.Server.AdminReports.ExportMarkdown {
		http.NotFound(w, r)
		return
	}
	filters, ok := s.parseAdminReportFilters(w, r, false, subject, global)
	if !ok {
		return
	}
	if !validateAdminTopNPageParams(w, filters) {
		return
	}
	switch mode := strings.TrimSpace(r.URL.Query().Get("mode")); mode {
	case "", "detail", "raw":
	case "summary":
		md, err := s.renderAdminReportSummaryMarkdown(filters)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"type": "report-query-failed", "message": "report-query-failed"}})
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(md))
		return
	default:
		writeInvalidReportFilter(w, "unsupported markdown export mode")
		return
	}
	page, err := s.usage.adminUsageRowsPage(adminUsagePageOptions{
		UsageReportOptions: filters.UsageReportOptions,
		Limit:              filters.Limit,
		Sort:               "timeUtc",
		Direction:          "desc",
	})
	if err != nil {
		filters.Sort = "timeUtc"
		filters.Direction = "desc"
		s.writeAdminReportQueryFailed(w, r, "markdown-export", "handleAdminReportMarkdown", &filters, err)
		return
	}
	rows := page.Rows
	decisionSummary := s.usage.decisionTelemetrySummary(rows)
	upstreamShapeEvents, err := s.usage.upstreamShapeEventsForRows(rows, filters.UsageReportOptions)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "markdown-export", "handleAdminReportMarkdown", &filters, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	md := renderUsageMarkdown(filters.From, filters.To, rows, decisionSummary, upstreamShapeEvents)
	if page.HasMore {
		md = strings.Replace(md, "\n\n", fmt.Sprintf("\n\n> Export scope: showing the most recent %d request rows for this filter; more rows matched the selected window. Use cursor-paged request APIs or narrower filters for row-by-row review.\n\n", len(rows)), 1)
	}
	_, _ = w.Write([]byte(md))
}

type adminMarkdownSummarySection struct {
	Title   string
	Columns []string
	Rows    []adminScalarReportRow
	HasMore bool
	Sort    string
	Limit   int
}

func (s *Service) renderAdminReportSummaryMarkdown(filters adminReportFilters) (string, error) {
	limit := filters.Limit
	if limit <= 0 {
		limit = 50
	}
	totalSpec := adminScalarEndpointSpec{Report: "summary-totals", Sort: "requests"}
	_, total, _, _, err := s.usage.adminScalarAggsSQL(filters.UsageReportOptions, totalSpec, adminSavingsBaselineDTO{}, "requests", 1)
	if err != nil {
		return "", err
	}
	sectionSpecs := []struct {
		title   string
		columns []string
		spec    adminScalarEndpointSpec
	}{
		{"Top Model Groups By Requests", []string{"Model Group"}, adminScalarEndpointSpec{Report: "summary-model-groups", Dimension: "model_group", Sort: "requests"}},
		{"Top Provider Models By Tokens", []string{"Provider / Model", "Dialect"}, adminScalarEndpointSpec{Report: "summary-provider-models", Dimension: "provider_model", Secondary: "dialect", Sort: "tokens"}},
		{"Top Callers By Cost", []string{"Caller ID", "Project"}, adminScalarEndpointSpec{Report: "summary-callers", Dimension: "caller_id", Secondary: "project", Sort: "cost"}},
		{"Top Clients By Requests", []string{"Client", "Inbound Dialect"}, adminScalarEndpointSpec{Report: "summary-clients", Dimension: "client", Secondary: "inbound_dialect", Sort: "requests"}},
		{"Top Projects By Cost", []string{"Project", "Environment"}, adminScalarEndpointSpec{Report: "summary-projects", Dimension: "project", Secondary: "environment", Sort: "cost"}},
		{"Top Requested Models By Requests", []string{"Requested Model", "Resolved Group"}, adminScalarEndpointSpec{Report: "summary-requested-models", Dimension: "requested_model", Secondary: "model_group", Sort: "requests"}},
		{"Top Status/Error Buckets By Errors", []string{"Status / Error", "Provider / Model"}, adminScalarEndpointSpec{Report: "summary-status-errors", Dimension: "status_error", Secondary: "provider_model", Sort: "errors"}},
	}
	sections := make([]adminMarkdownSummarySection, 0, len(sectionSpecs))
	for _, sectionSpec := range sectionSpecs {
		table, _, _, hasMore, err := s.usage.adminScalarAggsSQL(filters.UsageReportOptions, sectionSpec.spec, adminSavingsBaselineDTO{}, sectionSpec.spec.Sort, limit)
		if err != nil {
			return "", err
		}
		rows := adminScalarRowsFromAgg(table, sectionSpec.spec.Sort, limit)
		sections = append(sections, adminMarkdownSummarySection{
			Title:   sectionSpec.title,
			Columns: sectionSpec.columns,
			Rows:    rows,
			HasMore: hasMore,
			Sort:    sectionSpec.spec.Sort,
			Limit:   limit,
		})
	}
	return renderAdminReportSummaryMarkdown(filters.From, filters.To, total, sections), nil
}

func renderAdminReportSummaryMarkdown(from, to time.Time, total *agg, sections []adminMarkdownSummarySection) string {
	if total == nil {
		total = &agg{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Metrum AI Router Usage Summary\n\n")
	fmt.Fprintf(&b, "- Mode: `summary`\n")
	fmt.Fprintf(&b, "- Period UTC: `%s` to `%s`\n", formatUsageTime(from), formatUsageTime(to))
	fmt.Fprintf(&b, "- Scope: full-window SQL totals with bounded top-N aggregate sections; no raw request rows are included.\n")
	fmt.Fprintf(&b, "- Requests: `%d`\n", total.Calls)
	fmt.Fprintf(&b, "- Errors: `%d`\n", total.Errors)
	fmt.Fprintf(&b, "- Total Tokens: `%d`; Input Tokens: `%d`; Output Tokens: `%d`\n", total.TotalTokens, total.InputTokens, total.OutputTokens)
	fmt.Fprintf(&b, "- Cost: `$%s` total, `$%s` input, `$%s` image, `$%s` output\n", fmtUSD(total.TotalCostUSD), fmtUSD(total.InputCostUSD), fmtUSD(total.ImageCostUSD), fmtUSD(total.OutputCostUSD))
	fmt.Fprintf(&b, "- Cache: `%d` hits, `%d` misses, `%d` bypass\n", total.CacheHits, total.CacheMisses, total.CacheBypass)
	fmt.Fprintf(&b, "- Upstream attempts: `%d`; fallbacks: `%d`; streaming requests: `%d`\n", total.Attempts, total.Fallbacks, total.Streams)
	fmt.Fprintf(&b, "- Latency: `%d ms` avg, `%d ms` max\n\n", avg(total.LatencyMS, total.Calls), total.MaxLatencyMS)

	for _, section := range sections {
		writeAdminMarkdownSummarySection(&b, section)
	}
	return b.String()
}

func writeAdminMarkdownSummarySection(b *strings.Builder, section adminMarkdownSummarySection) {
	fmt.Fprintf(b, "## %s\n\n", section.Title)
	sortLabel := defaultString(section.Sort, "requests")
	fmt.Fprintf(b, "> Top %d aggregate rows ranked by `%s`. Full-window totals above are not limited by this top-N bound.", section.Limit, sortLabel)
	if section.HasMore {
		fmt.Fprint(b, " More aggregate buckets matched the selected window.")
	}
	fmt.Fprint(b, "\n\n")
	for _, column := range section.Columns {
		fmt.Fprintf(b, "| %s ", column)
	}
	fmt.Fprintln(b, "| Requests | Errors | Total Tokens | Input Tokens | Output Tokens | Total Cost USD | Cache Hits | Cache Misses | Cache Bypass | Attempts | Fallbacks | Avg Latency ms | Max Latency ms |")
	for range section.Columns {
		fmt.Fprint(b, "|---")
	}
	fmt.Fprintln(b, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, row := range section.Rows {
		values := []string{row.Key}
		if len(section.Columns) > 1 {
			values = append(values, row.SecondaryKey)
		}
		for i := 0; i < len(section.Columns); i++ {
			value := ""
			if i < len(values) {
				value = values[i]
			}
			fmt.Fprintf(b, "| %s ", esc(value))
		}
		fmt.Fprintf(b, "| %d | %d | %d | %d | %d | $%s | %d | %d | %d | %d | %d | %d | %d |\n",
			row.Requests, row.Errors, row.TotalTokens, row.InputTokens, row.OutputTokens, fmtUSD(row.TotalCostUSD),
			row.CacheHits, row.CacheMisses, row.CacheBypass, row.Attempts, row.Fallbacks, row.AvgLatencyMS, row.MaxLatencyMS)
	}
	if len(section.Rows) == 0 {
		for range section.Columns {
			fmt.Fprint(b, "| _none_ ")
		}
		fmt.Fprintln(b, "| 0 | 0 | 0 | 0 | 0 | $0.000000 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |")
	}
	fmt.Fprintln(b)
}

func (s *Service) parseAdminReportFilters(w http.ResponseWriter, r *http.Request, _ bool, subject adminAuthSubject, global bool) (adminReportFilters, bool) {
	to := time.Now().UTC()
	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
			return adminReportFilters{}, false
		}
		to = parsed.UTC()
	}
	sinceText := defaultString(q.Get("since"), s.cfg.Server.AdminReports.DefaultSince)
	since, err := parseReportDuration(sinceText)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
		return adminReportFilters{}, false
	}
	from := to.Add(-since)
	if raw := strings.TrimSpace(q.Get("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
			return adminReportFilters{}, false
		}
		from = parsed.UTC()
	}
	maxRange, _ := parseReportDuration(s.cfg.Server.AdminReports.MaxRange)
	if !from.Before(to) || to.Sub(from) > maxRange {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
		return adminReportFilters{}, false
	}
	limit := s.cfg.Server.AdminReports.MaxRows
	if strings.TrimSpace(q.Get("limit")) != "" {
		parsed, err := strconv.Atoi(q.Get("limit"))
		if err != nil || parsed <= 0 || parsed > s.cfg.Server.AdminReports.MaxRows {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
			return adminReportFilters{}, false
		}
		limit = parsed
	}
	offset := 0
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeInvalidReportFilter(w, "invalid offset")
			return adminReportFilters{}, false
		}
		offset = parsed
	}
	direction := strings.ToLower(strings.TrimSpace(q.Get("direction")))
	if direction == "" {
		direction = "desc"
	}
	if direction != "asc" && direction != "desc" {
		writeInvalidReportFilter(w, "invalid direction")
		return adminReportFilters{}, false
	}
	status := 0
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 100 || parsed > 599 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "invalid-report-filter"}})
			return adminReportFilters{}, false
		}
		status = parsed
	}
	var streamOnly *bool
	if raw := strings.TrimSpace(q.Get("stream")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeInvalidReportFilter(w, "invalid stream")
			return adminReportFilters{}, false
		}
		streamOnly = &parsed
	}
	var reasoningPresent *bool
	if raw := strings.TrimSpace(q.Get("reasoning_present")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeInvalidReportFilter(w, "invalid reasoning_present")
			return adminReportFilters{}, false
		}
		reasoningPresent = &parsed
	}
	opts := UsageReportOptions{
		From:               from,
		To:                 to,
		CallerID:           strings.TrimSpace(q.Get("caller_id")),
		CallerIP:           strings.TrimSpace(q.Get("caller_ip")),
		TokenID:            q.Get("token_id"),
		TokenIDPrefix:      q.Get("token_id_prefix"),
		CallerUser:         q.Get("caller_user"),
		CallerProject:      q.Get("caller_project"),
		CallerEnvironment:  q.Get("caller_environment"),
		RequestedModel:     strings.TrimSpace(q.Get("requested_model")),
		ResolvedGroup:      q.Get("resolved_group"),
		TargetProvider:     strings.TrimSpace(q.Get("provider")),
		TargetModel:        strings.TrimSpace(q.Get("target_model")),
		TargetDialect:      strings.TrimSpace(q.Get("dialect")),
		Status:             status,
		Cache:              strings.TrimSpace(q.Get("cache")),
		Client:             q.Get("client"),
		TrafficShapeBucket: strings.TrimSpace(q.Get("traffic_shape_bucket")),
		TrafficShapeScope:  strings.TrimSpace(q.Get("traffic_shape_scope")),
		InboundDialect:     strings.TrimSpace(q.Get("inbound_dialect")),
		StreamOnly:         streamOnly,
		ToolChoiceMode:     strings.TrimSpace(q.Get("tool_choice_mode")),
		ToolCountBucket:    strings.TrimSpace(q.Get("tool_count_bucket")),
		RequestBytesBucket: strings.TrimSpace(q.Get("request_bytes_bucket")),
		InputTokensBucket:  strings.TrimSpace(q.Get("estimated_input_tokens_bucket")),
		OutputCapBucket:    strings.TrimSpace(q.Get("output_cap_bucket")),
		ReasoningPresent:   reasoningPresent,
		MultimodalOnly:     parseTruthy(q.Get("multimodal")),
		RequestShapeFP:     strings.TrimSpace(q.Get("request_shape_fingerprint")),
		ToolSchemaFP:       strings.TrimSpace(q.Get("tool_schema_fingerprint")),
		ErrorClass:         strings.TrimSpace(q.Get("error_class")),
	}
	if !global {
		applyAdminDomainScope(&opts, subject.domain)
	}
	return adminReportFilters{
		From:               from,
		To:                 to,
		UsageReportOptions: opts,
		Limit:              limit,
		Offset:             offset,
		Sort:               strings.TrimSpace(q.Get("sort")),
		Direction:          direction,
		Cursor:             strings.TrimSpace(q.Get("cursor")),
	}, true
}

func (s *Service) parseAdminSecurityFilters(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool) (SecurityReportOptions, adminReportFilters, bool) {
	filters, ok := s.parseAdminReportFilters(w, r, true, subject, global)
	if !ok {
		return SecurityReportOptions{}, adminReportFilters{}, false
	}
	q := r.URL.Query()
	opts := SecurityReportOptions{
		From:              filters.From,
		To:                filters.To,
		Limit:             filters.Limit,
		Outcome:           strings.TrimSpace(q.Get("outcome")),
		ReasonCode:        strings.TrimSpace(q.Get("reason_code")),
		Surface:           strings.TrimSpace(q.Get("surface")),
		IPAddress:         strings.TrimSpace(q.Get("ip_address")),
		CallerID:          strings.TrimSpace(q.Get("caller_id")),
		CallerUser:        strings.TrimSpace(q.Get("caller_user")),
		CallerProject:     strings.TrimSpace(q.Get("caller_project")),
		CallerEnvironment: strings.TrimSpace(q.Get("caller_environment")),
		TokenID:           strings.TrimSpace(q.Get("token_id")),
		AdminSubject:      strings.TrimSpace(q.Get("admin_subject")),
		Client:            strings.TrimSpace(q.Get("client")),
	}
	if !global {
		applyAdminSecurityDomainScope(&opts, subject.domain)
	}
	return opts, filters, true
}

func applyAdminDomainScope(opts *UsageReportOptions, domain string) {
	project, environment := splitAuthzDomain(domain)
	if project != "" {
		opts.CallerProject = project
	}
	if environment != "" {
		opts.CallerEnvironment = environment
	}
}

func applyAdminSecurityDomainScope(opts *SecurityReportOptions, domain string) {
	project, environment := splitAuthzDomain(domain)
	if project != "" {
		opts.CallerProject = project
	}
	if environment != "" {
		opts.CallerEnvironment = environment
	}
}

func adminDomainAllowsUsageRow(domain string, row usageRow) bool {
	project, environment := splitAuthzDomain(domain)
	if project != "" && row.CallerProject != project {
		return false
	}
	if environment != "" && row.CallerEnvironment != environment {
		return false
	}
	return project != "" || environment != ""
}

func writeInvalidReportFilter(w http.ResponseWriter, message string) {
	if strings.TrimSpace(message) == "" {
		message = "invalid-report-filter"
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": message}})
}

func parseTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func validateAdminCursorPageParams(w http.ResponseWriter, filters adminReportFilters) bool {
	if filters.Offset != 0 {
		writeInvalidReportFilter(w, "offset is not supported for cursor-paged reports")
		return false
	}
	return true
}

func validateAdminTopNPageParams(w http.ResponseWriter, filters adminReportFilters) bool {
	if filters.Cursor != "" {
		writeInvalidReportFilter(w, "cursor is not supported for top-N reports")
		return false
	}
	if filters.Offset != 0 {
		writeInvalidReportFilter(w, "offset is not supported for top-N reports")
		return false
	}
	return true
}

func normalizeAdminRequestSort(sortKey string) (string, bool) {
	switch strings.TrimSpace(sortKey) {
	case "", "timeUtc", "ts", "time":
		return "timeUtc", true
	case "costUsd", "totalCostUsd", "cost":
		return "costUsd", true
	case "latencyMs", "latency":
		return "latencyMs", true
	case "status":
		return "status", true
	case "requestId":
		return "requestId", true
	default:
		return "", false
	}
}

func normalizeAdminRequestSortOrDefault(sortKey, fallback string) string {
	normalized, ok := normalizeAdminRequestSort(defaultString(sortKey, fallback))
	if ok {
		return normalized
	}
	normalized, _ = normalizeAdminRequestSort(fallback)
	return normalized
}

func normalizeAdminSecuritySort(sortKey string) (string, bool) {
	switch strings.TrimSpace(sortKey) {
	case "", "timeUtc", "ts", "time":
		return "timeUtc", true
	case "status":
		return "status", true
	case "outcome":
		return "outcome", true
	case "surface":
		return "surface", true
	case "reason", "reasonCode":
		return "reason", true
	default:
		return "", false
	}
}

func normalizeAdminSecuritySortOrDefault(sortKey, fallback string) string {
	normalized, ok := normalizeAdminSecuritySort(defaultString(sortKey, fallback))
	if ok {
		return normalized
	}
	normalized, _ = normalizeAdminSecuritySort(fallback)
	return normalized
}

func normalizeAdminAggregateSort(sortKey string) (string, bool) {
	switch strings.TrimSpace(sortKey) {
	case "", "requests", "key":
		return defaultString(strings.TrimSpace(sortKey), "requests"), true
	case "cost", "costUsd", "totalCostUsd":
		return "cost", true
	case "tokens", "totalTokens":
		return "tokens", true
	case "latency", "latencyMs", "avgLatencyMs":
		return "latency", true
	case "errors":
		return "errors", true
	case "fallbacks":
		return "fallbacks", true
	case "savings", "savingsUsd":
		return "savings", true
	case "image", "inputImageCount":
		return "image", true
	default:
		return "", false
	}
}

func normalizeAdminAggregateSortOrDefault(sortKey, fallback string) string {
	if strings.TrimSpace(sortKey) == "" && strings.TrimSpace(fallback) == "" {
		return ""
	}
	normalized, ok := normalizeAdminAggregateSort(defaultString(sortKey, fallback))
	if ok {
		return normalized
	}
	if strings.TrimSpace(fallback) == "" {
		return ""
	}
	normalized, _ = normalizeAdminAggregateSort(fallback)
	return normalized
}

func (s *Service) adminReportCursorFromRequest(w http.ResponseWriter, raw, endpoint, sortKey, direction string) (*adminReportCursorPayload, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	payload, err := s.decodeAdminReportCursor(raw)
	if err != nil || payload.Endpoint != endpoint || payload.Sort != sortKey || payload.Direction != direction {
		writeInvalidReportFilter(w, "invalid cursor")
		return nil, false
	}
	return payload, true
}

func (s *Service) encodeAdminReportCursor(payload adminReportCursorPayload) (string, error) {
	payload.Version = 1
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.reportCursor[:])
	_, _ = mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, nil
}

func (s *Service) decodeAdminReportCursor(raw string) (*adminReportCursorPayload, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, strconv.ErrSyntax
	}
	mac := hmac.New(sha256.New, s.reportCursor[:])
	_, _ = mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return nil, strconv.ErrSyntax
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var payload adminReportCursorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Version != 1 || payload.Endpoint == "" || payload.Sort == "" || payload.Direction == "" {
		return nil, strconv.ErrSyntax
	}
	return &payload, nil
}

func (s *Service) adminCursorPagination(filters adminReportFilters, endpoint string, rows []usageRow, total int64, hasMore bool, values func(usageRow, string) []string) adminReportPagination {
	next := ""
	if hasMore && len(rows) > 0 {
		cursor, err := s.encodeAdminReportCursor(adminReportCursorPayload{
			Endpoint:  endpoint,
			Sort:      filters.Sort,
			Direction: filters.Direction,
			Values:    values(rows[len(rows)-1], filters.Sort),
		})
		if err == nil {
			next = cursor
		}
	}
	return adminReportPagination{
		Limit:      filters.Limit,
		Returned:   len(rows),
		TotalCount: &total,
		HasMore:    hasMore,
		NextCursor: next,
		Sort:       filters.Sort,
		Direction:  filters.Direction,
		Mode:       "cursor",
	}
}

func (s *Service) adminSecurityCursorPagination(filters adminReportFilters, endpoint string, events []securityAccessEvent, total int64, hasMore bool) adminReportPagination {
	next := ""
	if hasMore && len(events) > 0 {
		cursor, err := s.encodeAdminReportCursor(adminReportCursorPayload{
			Endpoint:  endpoint,
			Sort:      filters.Sort,
			Direction: filters.Direction,
			Values:    adminSecurityCursorValues(events[len(events)-1], filters.Sort),
		})
		if err == nil {
			next = cursor
		}
	}
	return adminReportPagination{
		Limit:      filters.Limit,
		Returned:   len(events),
		TotalCount: &total,
		HasMore:    hasMore,
		NextCursor: next,
		Sort:       filters.Sort,
		Direction:  filters.Direction,
		Mode:       "cursor",
	}
}

func adminTopNPagination(filters adminReportFilters, returned int, hasMore bool, note string) adminReportPagination {
	sortKey := filters.Sort
	if sortKey == "" {
		sortKey = "requests"
	}
	return adminReportPagination{
		Limit:     filters.Limit,
		Returned:  returned,
		HasMore:   hasMore,
		Sort:      sortKey,
		Direction: filters.Direction,
		Mode:      "top_n",
		Note:      note,
	}
}

func adminUsageCursorValues(row usageRow, sortKey string) []string {
	switch sortKey {
	case "costUsd":
		return []string{strconv.FormatFloat(row.TotalCostUSD, 'g', -1, 64), formatUsageTime(row.TS), row.RequestID}
	case "latencyMs":
		return []string{strconv.FormatInt(row.LatencyMS, 10), formatUsageTime(row.TS), row.RequestID}
	case "status":
		return []string{strconv.Itoa(row.Status), formatUsageTime(row.TS), row.RequestID}
	case "requestId":
		return []string{row.RequestID}
	default:
		return []string{formatUsageTime(row.TS), row.RequestID}
	}
}

func adminSecurityCursorValues(event securityAccessEvent, sortKey string) []string {
	id := strconv.FormatUint(uint64(event.ID), 10)
	switch sortKey {
	case "status":
		return []string{strconv.Itoa(event.StatusCode), formatUsageTime(event.TS), id}
	case "outcome":
		return []string{event.Outcome, formatUsageTime(event.TS), id}
	case "surface":
		return []string{event.Surface, formatUsageTime(event.TS), id}
	case "reason":
		return []string{event.ReasonCode, formatUsageTime(event.TS), id}
	default:
		return []string{formatUsageTime(event.TS), id}
	}
}

func splitAuthzDomain(domain string) (string, string) {
	domain = strings.TrimSpace(domain)
	if domain == "" || domain == "*" {
		return "", ""
	}
	parts := strings.SplitN(domain, "/", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func buildAdminReportResponse(filters adminReportFilters, rows []usageRow) adminReportResponse {
	total := &agg{}
	byHour := map[string]*agg{}
	byToken := map[string]*agg{}
	byGroup := map[string]*agg{}
	byProvider := map[string]*agg{}
	byStatus := map[string]*agg{}
	for _, row := range rows {
		total.add(row)
		addAdminAgg(byHour, row.TS.UTC().Truncate(time.Hour).Format(time.RFC3339), row)
		addAdminAgg(byToken, defaultString(row.TokenID, "unknown"), row)
		addAdminAgg(byGroup, defaultString(row.ResolvedGroup, row.RequestedModel), row)
		addAdminAgg(byProvider, joinKey(defaultString(row.TargetProvider, "unknown"), defaultString(row.TargetModel, "unknown")), row)
		addAdminAgg(byStatus, strconv.Itoa(row.Status), row)
	}
	series := adminSeriesFromAgg(byHour)
	generatedAt := formatUsageTime(time.Now().UTC())
	requests := adminRecentRequestsFromRows(rows, filters.Limit)
	return adminReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Summary:      adminSummaryFromAgg(total),
		Series:       series,
		Charts:       adminChartsFromSeries(filters, generatedAt, series, adminRowsFromAgg(byProvider), false),
		ByToken:      adminRowsFromAgg(byToken),
		ByGroup:      adminRowsFromAgg(byGroup),
		ByProvider:   adminRowsFromAgg(byProvider),
		ByStatus:     adminRowsFromAgg(byStatus),
		Cache:        adminCacheFromAgg(total),
		Requests:     requests,
		Pagination:   adminTopNPagination(filters, len(requests), len(rows) > filters.Limit, "Summary request rows are a top-N recent sample for the selected filters."),
		GeneratedUTC: generatedAt,
	}
}

func buildAdminReportResponseSQL(filters adminReportFilters, overview adminOverviewSQLResult, baseline adminSavingsBaselineDTO) adminReportResponse {
	total := aggFromTokenScalarAggRecord(overview.Total)
	series := adminSeriesFromSQLRecords(overview.ByHour, baseline)
	generatedAt := formatUsageTime(time.Now().UTC())
	requests := make([]adminReportRequest, 0, len(overview.RecentRequests))
	for _, row := range overview.RecentRequests {
		requests = append(requests, adminRequestFromRow(row))
	}
	summary := adminSummaryFromAgg(&total)
	if baseline.BaselineID != "" {
		actualCost := summary.TotalCostUSD
		baselineCost := overview.Total.BaselineCostUSD
		savings := baselineCost - actualCost
		savingsPct := ratioPctFloat(savings, baselineCost)
		summary.ActualCostUSD = &actualCost
		summary.BaselineCostUSD = &baselineCost
		summary.SavingsUSD = &savings
		summary.SavingsPct = &savingsPct
	}
	return adminReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Summary:      summary,
		Series:       series,
		Charts:       adminChartsFromSeries(filters, generatedAt, series, adminRowsFromSQLRecords(overview.ByProvider), baseline.BaselineID != ""),
		ByToken:      adminRowsFromSQLRecords(overview.ByToken),
		ByGroup:      adminRowsFromSQLRecords(overview.ByGroup),
		ByProvider:   adminRowsFromSQLRecords(overview.ByProvider),
		ByStatus:     adminRowsFromSQLRecords(overview.ByStatus),
		Cache:        adminCacheFromAgg(&total),
		Requests:     requests,
		Pagination:   adminTopNPagination(filters, len(requests), overview.RequestHasMore || overview.GroupHasMore, "Overview summaries and breakdowns use full-window SQL aggregates; request rows are a bounded recent sample for the selected filters."),
		GeneratedUTC: generatedAt,
	}
}

func buildAdminSecurityReportResponse(filters adminReportFilters, events []securityAccessEvent) adminSecurityReportResponse {
	generatedAt := formatUsageTime(time.Now().UTC())
	rows := adminSecurityRows(events)
	return adminSecurityReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       "security-events",
		Summary:      adminSecuritySummaryFromEvents(events),
		Rows:         rows,
		Charts:       adminSecurityCharts(filters, generatedAt, rows),
		Pagination:   adminTopNPagination(filters, len(rows), false, "Security rows are bounded by limit when cursor pagination is not used."),
		GeneratedUTC: generatedAt,
	}
}

func buildAdminCatalogStatusResponse(cfg Config) adminCatalogStatusResponse {
	generatedAt := formatUsageTime(time.Now().UTC())
	rows := []adminCatalogStatusRow{}
	summary := adminCatalogSummary{Providers: len(cfg.Provider)}
	groupSummaries := map[string]*adminGroupEligibility{}
	for providerName, provider := range cfg.Provider {
		for modelRef, model := range provider.Models {
			row := adminCatalogRowFromProviderModel(providerName, modelRef, provider, model)
			rows = append(rows, row)
		}
	}
	for groupName, group := range cfg.Models {
		for targetIndex, target := range group.Targets {
			resolved, err := cfg.resolveTarget(groupName, target)
			if err != nil {
				continue
			}
			provider := cfg.Provider[resolved.Provider]
			row := adminCatalogRowFromTarget(resolved.Provider, provider, resolved)
			row.ActiveGroups = []string{groupName}
			row.ActiveTargetCount = 1
			row.GroupTargetIndex = targetIndex
			eligibilityDialect := targetDialect(provider, resolved)
			row.ActiveEligibilitySkin = nativeEligibilitySkin(eligibilityDialect)
			effective, inactive := toolSupportByActiveDialect(resolved.ToolSupport, eligibilityDialect)
			row.EffectiveToolSupport = effective
			row.InactiveToolSupport = inactive
			row.EffectiveStructured = targetSupportsStructuredOutput(resolved, eligibilityDialect, eligibilityDialect, true)
			row.EffectiveReasoning = targetSupportsReasoningForDialect(resolved, eligibilityDialect)
			row.EffectiveImageInput = stringSliceContains(defaultModalities(resolved.InputModalities), "image")
			if len(row.InactiveToolSupport) > 0 {
				row.EligibilityWarning = "metadata-for-inactive-provider-skin"
			}
			addGroupEligibility(groupSummaries, groupName, resolved, eligibilityDialect)
			if resolved.Validation != nil {
				row.ValidationStatus = defaultString(strings.ToLower(strings.TrimSpace(resolved.Validation.Status)), "missing")
				row.ValidationWorkload = resolved.Validation.Workload
				row.ValidationAgeBucket = validationAgeBucket(resolved.Validation, time.Now().UTC())
				row.ValidatedAt = resolved.Validation.ValidatedAt
				row.QualityScore = resolved.Validation.QualityScore
				row.PassRate = resolved.Validation.PassRate
				row.Harness = resolved.Validation.Harness
			}
			rows = append(rows, row)
		}
	}
	for i := range rows {
		row := &rows[i]
		row.ActiveGroups = dedupeSortedStrings(row.ActiveGroups)
		row.InputModalities = dedupeSortedStrings(row.InputModalities)
		row.OutputModalities = dedupeSortedStrings(row.OutputModalities)
		row.ToolSupport = dedupeSortedStrings(row.ToolSupport)
		row.EffectiveToolSupport = dedupeSortedStrings(row.EffectiveToolSupport)
		row.InactiveToolSupport = dedupeSortedStrings(row.InactiveToolSupport)
		if row.ValidationStatus == "" {
			row.ValidationStatus = "missing"
		}
		row.PricingMissing = row.InputPricePerMillionUSD == 0 && row.OutputPricePerMillionUSD == 0 && row.PricingSource == ""
		if row.Source == "catalog" {
			summary.CatalogModels++
		}
		if row.Source == "active_target" {
			summary.ActiveTargets++
			if row.ValidationStatus != "missing" {
				summary.ValidatedTargets++
			}
			if row.ValidationStatus == "passed" {
				summary.PassedTargets++
			}
		}
		if row.Source == "active_target" && row.PricingMissing {
			summary.MissingPricing++
		}
		if row.Source == "active_target" && len(row.InactiveToolSupport) > 0 {
			summary.InactiveMetadataSurfaces += len(inactiveToolSurfaces(row.InactiveToolSupport))
			summary.TargetsWithInactiveSkins++
		}
	}
	groupSummary := make([]adminGroupEligibility, 0, len(groupSummaries))
	for _, group := range groupSummaries {
		if group.OpenAIChatToolTargets+group.OpenAIResponsesToolTargets+group.AnthropicMessagesToolTargets > 0 {
			activeToolSkins := 0
			for _, count := range []int{group.OpenAIChatToolTargets, group.OpenAIResponsesToolTargets, group.AnthropicMessagesToolTargets} {
				if count > 0 {
					activeToolSkins++
				}
			}
			if activeToolSkins == 1 {
				summary.GroupsWithSingleSkinTools++
			}
		}
		groupSummary = append(groupSummary, *group)
	}
	sort.Slice(groupSummary, func(i, j int) bool {
		return groupSummary[i].Group < groupSummary[j].Group
	})
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Provider == rows[j].Provider {
			if rows[i].ModelRef == rows[j].ModelRef {
				if rows[i].Source == rows[j].Source {
					if strings.Join(rows[i].ActiveGroups, ",") == strings.Join(rows[j].ActiveGroups, ",") {
						return rows[i].GroupTargetIndex < rows[j].GroupTargetIndex
					}
					return strings.Join(rows[i].ActiveGroups, ",") < strings.Join(rows[j].ActiveGroups, ",")
				}
				return rows[i].Source < rows[j].Source
			}
			if rows[i].Model == rows[j].Model {
				return rows[i].Dialect < rows[j].Dialect
			}
			return rows[i].Model < rows[j].Model
		}
		return rows[i].Provider < rows[j].Provider
	})
	return adminCatalogStatusResponse{GeneratedUTC: generatedAt, Summary: summary, GroupSummary: groupSummary, Rows: rows, Charts: adminCatalogStatusCharts(generatedAt, rows)}
}

func adminCatalogRowFromProviderModel(providerName, modelRef string, provider ProviderConfig, model ProviderModel) adminCatalogStatusRow {
	dialect := defaultString(model.Dialect, provider.Dialect)
	return adminCatalogStatusRow{
		Source:                   "catalog",
		Provider:                 providerName,
		ModelRef:                 modelRef,
		Model:                    model.Model,
		Dialect:                  dialect,
		DisplayName:              model.DisplayName,
		ValidationStatus:         "missing",
		ContextTokens:            model.ContextTokens,
		InputModalities:          append([]string(nil), model.InputModalities...),
		OutputModalities:         append([]string(nil), model.OutputModalities...),
		ToolSupport:              flattenToolSupport(model.ToolSupport),
		InputPricePerMillionUSD:  model.InputPricePerMillionUSD,
		OutputPricePerMillionUSD: model.OutputPricePerMillionUSD,
		PricingSource:            model.PricingSource,
		PricingUpdatedAt:         model.PricingUpdatedAt,
		HonorsMaxTokens:          model.HonorsMaxTokens,
		ForceStoreFalse:          model.ForceStoreFalse,
		OutputTokenField:         model.OutputTokenField,
	}
}

func adminCatalogRowFromTarget(providerName string, provider ProviderConfig, target Target) adminCatalogStatusRow {
	dialect := defaultString(target.Dialect, provider.Dialect)
	return adminCatalogStatusRow{
		Source:                   "active_target",
		Provider:                 providerName,
		ModelRef:                 target.ModelRef,
		Model:                    target.Model,
		Dialect:                  dialect,
		DisplayName:              target.DisplayName,
		ValidationStatus:         "missing",
		ContextTokens:            target.ContextTokens,
		InputModalities:          append([]string(nil), target.InputModalities...),
		OutputModalities:         append([]string(nil), target.OutputModalities...),
		ToolSupport:              flattenToolSupport(target.ToolSupport),
		InputPricePerMillionUSD:  target.InputPricePerMillionUSD,
		OutputPricePerMillionUSD: target.OutputPricePerMillionUSD,
		PricingSource:            target.PricingSource,
		PricingUpdatedAt:         target.PricingUpdatedAt,
		HonorsMaxTokens:          target.HonorsMaxTokens,
		ForceStoreFalse:          target.ForceStoreFalse,
		OutputTokenField:         target.OutputTokenField,
	}
}

func flattenToolSupport(ts ToolSupport) []string {
	out := []string{}
	for _, value := range ts.OpenAIChat {
		out = append(out, "openai_chat:"+value)
	}
	for _, value := range ts.OpenAIResponses {
		out = append(out, "openai_responses:"+value)
	}
	for _, value := range ts.AnthropicMessages {
		out = append(out, "anthropic_messages:"+value)
	}
	for _, value := range ts.ProviderHosted {
		out = append(out, "provider_hosted:"+value)
	}
	return out
}

func nativeEligibilitySkin(dialect string) string {
	if normalized := normalizeDialect(dialect); normalized != "" {
		return "native:" + normalized
	}
	return ""
}

func toolSupportByActiveDialect(ts ToolSupport, dialect string) ([]string, []string) {
	activeSurface := toolSurfaceForDialect(dialect)
	effective := []string{}
	inactive := []string{}
	for _, entry := range flattenToolSupport(ts) {
		surface, _, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		if surface == activeSurface || surface == "provider_hosted" {
			effective = append(effective, entry)
			continue
		}
		inactive = append(inactive, entry)
	}
	return effective, inactive
}

func toolSurfaceForDialect(dialect string) string {
	switch normalizeDialect(dialect) {
	case "openai-chat":
		return "openai_chat"
	case "openai-responses":
		return "openai_responses"
	case "anthropic":
		return "anthropic_messages"
	default:
		return ""
	}
}

func inactiveToolSurfaces(entries []string) []string {
	surfaces := []string{}
	for _, entry := range entries {
		surface, _, ok := strings.Cut(entry, ":")
		if ok {
			surfaces = append(surfaces, surface)
		}
	}
	return dedupeSortedStrings(surfaces)
}

func targetSupportsReasoningForDialect(target Target, dialect string) bool {
	if !targetSupportsReasoning(target) {
		return false
	}
	control := strings.ToLower(strings.TrimSpace(target.Reasoning.Control))
	switch normalizeDialect(dialect) {
	case "anthropic":
		return control == reasoningControlTokenBudget || defaultThinkingEnabled(target)
	case "openai-chat", "openai-responses":
		return control == reasoningControlEffortEnum || control == reasoningControlTokenBudget
	default:
		return false
	}
}

func addGroupEligibility(groups map[string]*adminGroupEligibility, groupName string, target Target, dialect string) {
	summary := groups[groupName]
	if summary == nil {
		summary = &adminGroupEligibility{Group: groupName}
		groups[groupName] = summary
	}
	tools := targetSupportsTools(target, dialect)
	structured := targetSupportsStructuredOutput(target, dialect, dialect, true)
	image := stringSliceContains(defaultModalities(target.InputModalities), "image")
	reasoning := targetSupportsReasoningForDialect(target, dialect)
	switch normalizeDialect(dialect) {
	case "openai-chat":
		if !target.ToolOnly {
			summary.OpenAIChatTargets++
		}
		if tools {
			summary.OpenAIChatToolTargets++
		}
		if structured {
			summary.OpenAIChatStructuredOutputTargets++
		}
		if image {
			summary.OpenAIChatImageTargets++
		}
		if image && tools {
			summary.OpenAIChatImageToolTargets++
		}
		if reasoning {
			summary.OpenAIChatReasoningTargets++
		}
	case "openai-responses":
		if !target.ToolOnly {
			summary.OpenAIResponsesTargets++
		}
		if tools {
			summary.OpenAIResponsesToolTargets++
		}
		if structured {
			summary.OpenAIResponsesStructuredTargets++
		}
		if image {
			summary.OpenAIResponsesImageTargets++
		}
		if image && tools {
			summary.OpenAIResponsesImageToolTargets++
		}
		if reasoning {
			summary.OpenAIResponsesReasoningTargets++
		}
	case "anthropic":
		if !target.ToolOnly {
			summary.AnthropicMessagesTargets++
		}
		if tools {
			summary.AnthropicMessagesToolTargets++
		}
		if structured {
			summary.AnthropicMessagesStructuredTargets++
		}
		if image {
			summary.AnthropicMessagesImageTargets++
		}
		if image && tools {
			summary.AnthropicMessagesImageToolTargets++
		}
		if reasoning {
			summary.AnthropicMessagesReasoningTargets++
		}
	}
}

func dedupeSortedStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func adminSecurityRows(events []securityAccessEvent) []adminSecurityEventRow {
	rows := make([]adminSecurityEventRow, 0, len(events))
	for _, event := range events {
		rows = append(rows, adminSecurityEventRow{
			TimeUTC:             formatUsageTime(event.TS),
			RequestID:           event.RequestID,
			EventType:           event.EventType,
			Surface:             event.Surface,
			Method:              event.HTTPMethod,
			Path:                event.PathTemplate,
			Status:              event.StatusCode,
			Outcome:             event.Outcome,
			Reason:              event.ReasonCode,
			AuthSubject:         event.AuthSubject,
			AuthSource:          event.AuthSource,
			CallerID:            event.CallerID,
			CallerUser:          event.CallerUser,
			Project:             event.CallerProject,
			TokenID:             event.TokenID,
			AdminSubject:        event.AdminSubject,
			Client:              event.Client,
			UserAgentFamily:     event.UserAgentFamily,
			IPAddress:           event.IPAddress,
			IPSource:            event.IPSource,
			TrustedProxyApplied: event.TrustedProxyApplied,
			PrivateIP:           event.RequestIsPrivate,
			LoopbackIP:          event.RequestIsLoopback,
			ReservedIP:          event.RequestIsReserved,
			ModelGroup:          event.ModelGroup,
			RequestedModel:      event.RequestedModel,
			ResolvedGroup:       event.ResolvedGroup,
			InputTokens:         event.InputTokens,
			OutputTokens:        event.OutputTokens,
			TotalTokens:         event.TotalTokens,
		})
	}
	return rows
}

func adminSecuritySummaryFromEvents(events []securityAccessEvent) adminSecuritySummary {
	ips := map[string]bool{}
	var summary adminSecuritySummary
	for _, event := range events {
		summary.Events++
		if event.IPAddress != "" {
			ips[event.IPAddress] = true
		}
		switch event.Outcome {
		case "allowed":
			summary.Allowed++
		case "unauthorized":
			summary.Unauthorized++
		case "forbidden":
			summary.Forbidden++
		case "denied":
			summary.Denied++
		case "error":
			summary.Errors++
		}
	}
	summary.UniqueIPs = int64(len(ips))
	return summary
}

func adminSecurityCharts(filters adminReportFilters, generatedAt string, rows []adminSecurityEventRow) []adminReportChart {
	byOutcome := map[string]float64{}
	bySurface := map[string]float64{}
	byReason := map[string]float64{}
	for _, row := range rows {
		byOutcome[defaultString(row.Outcome, "unknown")]++
		bySurface[defaultString(row.Surface, "unknown")]++
		if row.Reason != "" {
			byReason[row.Reason]++
		}
	}
	return []adminReportChart{
		adminCategoryChart(filters, generatedAt, "security_outcomes", "Security outcomes", "Outcome", "Events", "count", []adminReportChartSeries{adminSeriesFromCounts("Events", "count", "magenta", byOutcome)}),
		adminCategoryChart(filters, generatedAt, "security_surfaces", "Security surfaces", "Surface", "Events", "count", []adminReportChartSeries{adminSeriesFromCounts("Events", "count", "blue", bySurface)}),
		adminCategoryChart(filters, generatedAt, "security_reasons", "Security reasons", "Reason", "Events", "count", []adminReportChartSeries{adminSeriesFromCounts("Events", "count", "red", byReason)}),
	}
}

func adminSecurityTrendCharts(filters adminReportFilters, generatedAt string, records []securityTrendBucketRecord) []adminReportChart {
	if len(records) == 0 {
		return nil
	}
	outcomeSeries := adminSecurityTrendSeries(records, func(record securityTrendBucketRecord) (string, bool) {
		return defaultString(record.Outcome, "unknown"), true
	})
	denialSeries := adminSecurityTrendSeries(records, func(record securityTrendBucketRecord) (string, bool) {
		switch record.Outcome {
		case "unauthorized", "forbidden", "denied", "error":
			return defaultString(record.Surface, "unknown"), true
		default:
			return "", false
		}
	})
	charts := []adminReportChart{
		adminTimeChart(filters, generatedAt, "security_events_over_time", "Security Events Over Time", "Events", "count", outcomeSeries),
	}
	if len(denialSeries) > 0 {
		charts = append(charts, adminTimeChart(filters, generatedAt, "security_denials_over_time", "Security Denials Over Time", "Events", "count", denialSeries))
	}
	return charts
}

func adminSecurityTrendSeries(records []securityTrendBucketRecord, label func(securityTrendBucketRecord) (string, bool)) []adminReportChartSeries {
	pointsByLabel := map[string]map[string]float64{}
	buckets := map[string]bool{}
	for _, record := range records {
		key, ok := label(record)
		if !ok || key == "" {
			continue
		}
		if pointsByLabel[key] == nil {
			pointsByLabel[key] = map[string]float64{}
		}
		pointsByLabel[key][record.BucketUTC] += float64(record.Events)
		buckets[record.BucketUTC] = true
	}
	bucketList := make([]string, 0, len(buckets))
	for bucket := range buckets {
		bucketList = append(bucketList, bucket)
	}
	sort.Strings(bucketList)
	labels := make([]string, 0, len(pointsByLabel))
	for key := range pointsByLabel {
		labels = append(labels, key)
	}
	sort.Strings(labels)
	colors := []string{"magenta", "blue", "red", "warning", "purple", "text"}
	series := make([]adminReportChartSeries, 0, len(labels))
	for i, key := range labels {
		points := make([]adminReportChartPoint, 0, len(bucketList))
		for _, bucket := range bucketList {
			points = append(points, adminReportChartPoint{X: bucket, XUnixMs: adminReportTimeUnixMs(bucket), Y: pointsByLabel[key][bucket]})
		}
		series = append(series, adminReportChartSeries{Name: key, Unit: "count", ColorKey: colors[i%len(colors)], Points: points})
	}
	return series
}

func adminCatalogStatusCharts(generatedAt string, rows []adminCatalogStatusRow) []adminReportChart {
	if len(rows) == 0 {
		return nil
	}
	bySource := map[string]float64{}
	byValidation := map[string]float64{}
	byProvider := map[string]float64{}
	for _, row := range rows {
		bySource[defaultString(row.Source, "unknown")]++
		byValidation[defaultString(row.ValidationStatus, "missing")]++
		if row.Source == "active_target" {
			byProvider[defaultString(row.Provider, "unknown")]++
		}
	}
	return []adminReportChart{
		adminStaticCategoryChart(generatedAt, "catalog_sources", "Catalog row sources", "Source", "Rows", "count", []adminReportChartSeries{
			adminSeriesFromCounts("Rows", "count", "magenta", bySource),
		}),
		adminStaticCategoryChart(generatedAt, "catalog_validation_status", "Validation status", "Status", "Rows", "count", []adminReportChartSeries{
			adminSeriesFromCounts("Rows", "count", "blue", byValidation),
		}),
		adminStaticCategoryChart(generatedAt, "catalog_active_targets", "Active targets by provider", "Provider", "Targets", "count", []adminReportChartSeries{
			adminSeriesFromCounts("Targets", "count", "purple", byProvider),
		}),
	}
}

func adminRetentionStatusCharts(generatedAt string, tables []adminRetentionTableRow, rollups []adminRollupStatusRow) []adminReportChart {
	charts := []adminReportChart{}
	if len(tables) > 0 {
		charts = append(charts, adminStaticCategoryChart(generatedAt, "retention_table_rows", "Retention row eligibility", "Data class / table", "Rows", "count", []adminReportChartSeries{
			adminRetentionTableSeries("Candidate rows", "magenta", tables, func(row adminRetentionTableRow) float64 { return float64(row.CandidateRows) }),
			adminRetentionTableSeries("Eligible rows", "blue", tables, func(row adminRetentionTableRow) float64 { return float64(row.EligibleRows) }),
			adminRetentionTableSeries("Deleted rows", "red", tables, func(row adminRetentionTableRow) float64 { return float64(row.DeletedRows) }),
		}))
	}
	if len(rollups) > 0 {
		byStatus := map[string]float64{}
		byType := map[string]float64{}
		for _, row := range rollups {
			byStatus[defaultString(row.Status, "unknown")]++
			byType[defaultString(row.RollupType, "unknown")]++
		}
		charts = append(charts,
			adminStaticCategoryChart(generatedAt, "retention_rollup_status", "Rollup status", "Status", "Runs", "count", []adminReportChartSeries{
				adminSeriesFromCounts("Runs", "count", "warning", byStatus),
			}),
			adminStaticCategoryChart(generatedAt, "retention_rollup_type", "Rollup runs by type", "Rollup type", "Runs", "count", []adminReportChartSeries{
				adminSeriesFromCounts("Runs", "count", "purple", byType),
			}),
		)
	}
	return charts
}

func adminRetentionTableSeries(name, colorKey string, rows []adminRetentionTableRow, value func(adminRetentionTableRow) float64) adminReportChartSeries {
	points := make([]adminReportChartPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, adminReportChartPoint{X: row.DataClass + " / " + row.TableName, Y: value(row)})
	}
	return adminReportChartSeries{Name: name, Unit: "count", ColorKey: colorKey, Points: points}
}

func adminStaticCategoryChart(generatedAt, id, title, xLabel, yLabel, yUnit string, chartSeries []adminReportChartSeries) adminReportChart {
	return adminReportChart{
		ChartID:     id,
		Title:       title,
		XAxis:       adminReportChartAxis{Label: xLabel, Type: "category"},
		YAxis:       adminReportChartAxis{Label: yLabel, Type: "linear", Unit: yUnit},
		Series:      chartSeries,
		GeneratedAt: generatedAt,
	}
}

func adminSeriesFromCounts(name, unit, colorKey string, counts map[string]float64) adminReportChartSeries {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	points := make([]adminReportChartPoint, 0, len(keys))
	for _, key := range keys {
		points = append(points, adminReportChartPoint{X: key, Y: counts[key]})
	}
	return adminReportChartSeries{Name: name, Unit: unit, ColorKey: colorKey, Points: points}
}

func adminMaxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func buildAdminSavingsResponse(filters adminReportFilters, rows []usageRow, baseline adminSavingsBaselineDTO, baselines []adminSavingsBaselineDTO) adminSavingsResponse {
	total := &adminSavingsAgg{}
	byHour := map[string]*adminSavingsAgg{}
	byGroupAgg := map[string]*adminSavingsAgg{}
	missingTokens := 0
	missingActualCost := 0
	for _, row := range rows {
		if row.InputTokens == 0 && row.OutputTokens == 0 && row.TotalTokens == 0 {
			missingTokens++
		}
		if row.TotalCostUSD == 0 && (row.InputTokens > 0 || row.OutputTokens > 0 || row.TotalTokens > 0) {
			missingActualCost++
		}
		total.add(row, baseline)
		adminSavingsAggFor(byHour, row.TS.UTC().Truncate(time.Hour).Format(time.RFC3339)).add(row, baseline)
		adminSavingsAggFor(byGroupAgg, defaultString(row.ResolvedGroup, row.RequestedModel)).add(row, baseline)
	}
	generatedAt := formatUsageTime(time.Now().UTC())
	byTime := adminSavingsRowsFromAgg(byHour)
	byGroup := adminSavingsRowsFromAgg(byGroupAgg)
	sort.Slice(byGroup, func(i, j int) bool {
		if byGroup[i].SavingsUSD == byGroup[j].SavingsUSD {
			return byGroup[i].Key < byGroup[j].Key
		}
		return byGroup[i].SavingsUSD > byGroup[j].SavingsUSD
	})
	byGroupHasMore := len(byGroup) > filters.Limit
	if byGroupHasMore {
		byGroup = byGroup[:filters.Limit]
	}
	warnings := []string{}
	if missingTokens > 0 {
		warnings = append(warnings, strconv.Itoa(missingTokens)+" row(s) had no stored input/output token usage")
	}
	if missingActualCost > 0 {
		warnings = append(warnings, strconv.Itoa(missingActualCost)+" row(s) had token usage but zero stored actual cost")
	}
	return adminSavingsResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Baseline:     baseline,
		Baselines:    baselines,
		Summary:      total.row("total"),
		ByTime:       byTime,
		ByGroup:      byGroup,
		Charts:       adminSavingsCharts(filters, generatedAt, byTime),
		Warnings:     warnings,
		Pagination:   adminTopNPagination(filters, len(byGroup), byGroupHasMore, "Savings aggregate rows are top-N by savings for the selected filters."),
		GeneratedUTC: generatedAt,
	}
}

func (s *Service) buildAdminSavingsResponseSQL(filters adminReportFilters, baseline adminSavingsBaselineDTO) (adminSavingsResponse, error) {
	total, byTime, byGroup, hasMore, missingTokens, missingActualCost, err := s.usage.adminSavingsAggsSQL(filters.UsageReportOptions, baseline, filters.Limit)
	if err != nil {
		return adminSavingsResponse{}, err
	}
	generatedAt := formatUsageTime(time.Now().UTC())
	warnings := []string{}
	if missingTokens > 0 {
		warnings = append(warnings, strconv.FormatInt(missingTokens, 10)+" row(s) had no stored input/output token usage")
	}
	if missingActualCost > 0 {
		warnings = append(warnings, strconv.FormatInt(missingActualCost, 10)+" row(s) had token usage but zero stored actual cost")
	}
	return adminSavingsResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Baseline:     baseline,
		Baselines:    s.adminSavingsBaselines(),
		Summary:      total,
		ByTime:       byTime,
		ByGroup:      byGroup,
		Charts:       adminSavingsCharts(filters, generatedAt, byTime),
		Warnings:     warnings,
		Pagination:   adminTopNPagination(filters, len(byGroup), hasMore, "Savings aggregate rows are top-N by savings for the selected filters."),
		GeneratedUTC: generatedAt,
	}, nil
}

func buildAdminScalarReportResponse(filters adminReportFilters, rows []usageRow, spec adminScalarEndpointSpec, baseline adminSavingsBaselineDTO) adminScalarReportResponse {
	total := &agg{}
	for _, row := range rows {
		total.add(row)
	}
	generatedAt := formatUsageTime(time.Now().UTC())
	resp := adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       spec.Report,
		Summary:      adminSummaryFromAgg(total),
		GeneratedUTC: generatedAt,
	}
	if baseline.BaselineID != "" {
		resp.Baseline = &baseline
	}
	if spec.Requests {
		resp.Requests = make([]adminReportRequest, 0, len(rows))
		for _, row := range rows {
			resp.Requests = append(resp.Requests, adminRequestFromRow(row))
		}
		resp.Charts = adminRequestCharts(filters, generatedAt, spec, resp.Requests)
		resp.Pagination = adminTopNPagination(filters, len(resp.Requests), false, "Request rows are cursor-paged when served from the admin API.")
		return resp
	}
	table := map[string]*adminScalarAgg{}
	for _, row := range rows {
		keys := adminScalarKeys(row, spec)
		if len(keys) == 0 {
			continue
		}
		for _, key := range keys {
			if key.Key == "" {
				key.Key = "unknown"
			}
			mapKey := joinKey(key.Key, key.Secondary)
			if table[mapKey] == nil {
				table[mapKey] = &adminScalarAgg{Key: key.Key, SecondaryKey: key.Secondary}
			}
			table[mapKey].add(row, baseline)
		}
	}
	return buildAdminScalarReportResponseFromAgg(filters, table, total, spec, baseline)
}

func adminSummaryWithSavings(summary adminReportSummary, table map[string]*adminScalarAgg) adminReportSummary {
	actualCost := summary.TotalCostUSD
	baselineCost := 0.0
	for _, row := range table {
		if row != nil && row.HasBaseline {
			baselineCost += row.BaselineCostUSD
		}
	}
	savings := baselineCost - actualCost
	savingsPct := ratioPctFloat(savings, baselineCost)
	summary.ActualCostUSD = &actualCost
	summary.BaselineCostUSD = &baselineCost
	summary.SavingsUSD = &savings
	summary.SavingsPct = &savingsPct
	return summary
}

func buildAdminScalarReportResponseFromAgg(filters adminReportFilters, table map[string]*adminScalarAgg, total *agg, spec adminScalarEndpointSpec, baseline adminSavingsBaselineDTO) adminScalarReportResponse {
	generatedAt := formatUsageTime(time.Now().UTC())
	resp := adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       spec.Report,
		Summary:      adminSummaryFromAgg(total),
		GeneratedUTC: generatedAt,
	}
	if baseline.BaselineID != "" {
		resp.Baseline = &baseline
		resp.Summary = adminSummaryWithSavings(resp.Summary, table)
	}
	sortKey := defaultString(filters.Sort, spec.Sort)
	filters.Sort = sortKey
	resp.Rows = adminScalarRowsFromAgg(table, sortKey, filters.Limit)
	resp.Charts = adminScalarCharts(filters, generatedAt, spec, resp.Rows)
	resp.Pagination = adminTopNPagination(filters, len(resp.Rows), len(table) > filters.Limit, "Aggregate rows are top-N for the selected filters.")
	return resp
}

func buildAdminShapeReportResponse(filters adminReportFilters, rows []usageRow, upstreamEvents []upstreamShapeJoinedEvent, spec adminScalarEndpointSpec) adminScalarReportResponse {
	generatedAt := formatUsageTime(time.Now().UTC())
	if filters.Sort == "" {
		filters.Sort = spec.Sort
	}
	table := map[string]*adminShapeAgg{}
	includeCaller := spec.ShapeReport == "overview" || spec.ShapeReport == "caller"
	includeUpstream := spec.ShapeReport == "overview" || spec.ShapeReport == "upstream" || spec.ShapeReport == "adaptive"
	if includeCaller {
		for _, row := range rows {
			if !row.TrafficShapeApplied {
				continue
			}
			key, secondary := adminShapeCallerKeys(row, spec)
			adminShapeAggFor(table, key, secondary).addCaller(row)
		}
	}
	if includeUpstream {
		for _, event := range upstreamEvents {
			if spec.ShapeReport == "adaptive" && event.Event.Bucket != shapeBucketBackoff {
				continue
			}
			key, secondary := adminShapeUpstreamKeys(event, spec)
			adminShapeAggFor(table, key, secondary).addUpstream(event)
		}
	}
	shapeRows := adminShapeRowsFromAgg(table, filters.Sort, filters.Limit)
	resp := adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       spec.Report,
		Summary:      adminSummaryFromAgg(&agg{Calls: int64(len(rows))}),
		Rows:         shapeRows,
		Pagination:   adminTopNPagination(filters, len(shapeRows), len(table) > filters.Limit, "Traffic shaping aggregate rows are top-N for the selected filters."),
		GeneratedUTC: generatedAt,
	}
	resp.Charts = adminShapeCharts(filters, generatedAt, spec, resp.Rows)
	return resp
}

func buildAdminTrafficTuningAdvisorResponse(filters adminReportFilters, advisorRows []TrafficTuningAdvisorRow, total *agg, hasMore bool) adminScalarReportResponse {
	generatedAt := formatUsageTime(time.Now().UTC())
	if total == nil {
		total = &agg{}
	}
	respRows := make([]adminScalarReportRow, 0, len(advisorRows))
	for _, row := range advisorRows {
		respRows = append(respRows, adminScalarReportRow{
			Key:                   row.Key,
			SecondaryKey:          row.SecondaryKey,
			Requests:              row.Requests,
			Errors:                row.TerminalErrors,
			ErrorRatePct:          row.ErrorRatePct,
			Rejections:            row.Rejected,
			Queued:                row.Queued,
			AvgRetryAfterMS:       row.RetryAfterP50MS,
			MaxRetryAfterMS:       row.RetryAfterMaxMS,
			P50QueueWaitMS:        row.QueueWaitP50MS,
			P95QueueWaitMS:        row.QueueWaitP95MS,
			MaxQueueWaitMS:        row.QueueWaitMaxMS,
			EstimatedInputTokens:  row.EstimatedInputP95,
			ReservedOutputTokens:  row.ReservedOutputP95,
			TotalReservedTokens:   row.TotalReservedP95,
			Upstream429Attempts:   row.Upstream429,
			Recommendation:        row.Recommendation,
			Severity:              row.Severity,
			Successes:             row.Successes,
			SuccessRatePct:        row.SuccessRatePct,
			Router429:             row.Router429,
			TrafficShaped:         row.TrafficShaped,
			Upstream400:           row.Upstream400,
			Upstream5xx:           row.Upstream5xx,
			UpstreamTimeouts:      row.UpstreamTimeouts,
			ClientCanceled:        row.ClientCanceled,
			FallbackRatePct:       row.FallbackRatePct,
			SuccessAfterFallbacks: row.SuccessAfterFallbacks,
			AvgLatencyMS:          row.LatencyP50MS,
			MaxLatencyMS:          row.LatencyMaxMS,
			AffectedUsers:         row.AffectedUsers,
			AffectedClients:       row.AffectedClients,
			ObservedValues:        row.ObservedValues,
			Threshold:             row.Threshold,
			ConfigFields:          row.ConfigFields,
			Explanation:           row.Explanation,
			Docs:                  row.Docs,
		})
	}
	return adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       "traffic-tuning-advisor",
		Summary:      adminSummaryFromAgg(total),
		Rows:         respRows,
		Pagination:   adminTopNPagination(filters, len(respRows), hasMore, "Traffic tuning advisor rows are grouped SQL feature rows ranked by severity and request count; raw request rows are not materialized."),
		GeneratedUTC: generatedAt,
	}
}

func adminDiagnosticParentOptions(opts UsageReportOptions, diagnostic string) UsageReportOptions {
	opts.TargetProvider = ""
	opts.TargetModel = ""
	opts.TargetDialect = ""
	if diagnostic == "upstream_failures" {
		opts.Status = 0
	}
	opts.ErrorClass = ""
	return opts
}

func (s *Service) buildAdminDiagnosticReportResponse(filters adminReportFilters, rows []usageRow, spec adminScalarEndpointSpec) (adminScalarReportResponse, error) {
	generatedAt := formatUsageTime(time.Now().UTC())
	total := &agg{}
	for _, row := range rows {
		total.add(row)
	}
	resp := adminScalarReportResponse{
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Report:       spec.Report,
		Summary:      adminSummaryFromAgg(total),
		GeneratedUTC: generatedAt,
	}
	rowByRequestID := adminUsageRowsByRequestID(rows)
	requestIDs := sortedRequestIDs(rowByRequestID)
	if len(requestIDs) == 0 {
		resp.Pagination = adminTopNPagination(filters, 0, false, "Diagnostic aggregate rows are top-N for the selected filters.")
		return resp, nil
	}
	var table map[string]*adminDiagnosticAgg
	var err error
	switch spec.Diagnostic {
	case "upstream_failures":
		table, err = s.adminUpstreamFailureAggs(requestIDs, rowByRequestID, filters.UsageReportOptions)
	case "request_shape_mismatches":
		table, err = s.adminRequestShapeMismatchAggs(requestIDs, rowByRequestID, filters.UsageReportOptions)
	case "fallback_health":
		table, err = s.adminFallbackHealthAggs(requestIDs, rowByRequestID, filters.UsageReportOptions)
	case "client_impact":
		table, err = s.adminClientImpactAggs(requestIDs, rowByRequestID, filters.UsageReportOptions)
	default:
		table = map[string]*adminDiagnosticAgg{}
	}
	if err != nil {
		return adminScalarReportResponse{}, err
	}
	reportRows := adminDiagnosticRowsFromAgg(table, defaultString(filters.Sort, spec.Sort), filters.Limit)
	resp.Rows = reportRows
	resp.Charts = adminDiagnosticCharts(filters, generatedAt, spec, reportRows)
	resp.Pagination = adminTopNPagination(filters, len(reportRows), len(table) > filters.Limit, "Diagnostic aggregate rows are top-N for the selected filters.")
	return resp, nil
}

type adminDiagnosticAgg struct {
	Key                     string
	SecondaryKey            string
	Status                  int
	ErrorClass              string
	Provider                string
	Model                   string
	Dialect                 string
	RequestShapeFingerprint string
	ToolSchemaFingerprint   string
	RequestIDs              map[string]bool
	Users                   map[string]bool
	Clients                 map[string]bool
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
	LatencyMS               int64
	UpstreamMS              int64
	UpstreamMSCount         int64
	MaxLatencyMS            int64
	MaxUpstreamMS           int64
}

func adminDiagnosticAggFor(table map[string]*adminDiagnosticAgg, key, secondary string) *adminDiagnosticAgg {
	key = defaultString(key, "unknown")
	mapKey := joinKey(key, secondary)
	if table[mapKey] == nil {
		table[mapKey] = &adminDiagnosticAgg{
			Key:          key,
			SecondaryKey: secondary,
			RequestIDs:   map[string]bool{},
			Users:        map[string]bool{},
			Clients:      map[string]bool{},
		}
	}
	return table[mapKey]
}

func (a *adminDiagnosticAgg) addRequest(row usageRow) {
	if row.RequestID != "" {
		a.RequestIDs[row.RequestID] = true
	}
	if row.CallerUser != "" {
		a.Users[row.CallerUser] = true
	}
	if row.Client != "" {
		a.Clients[row.Client] = true
	}
	a.LatencyMS += row.LatencyMS
	a.MaxLatencyMS = adminMaxInt64(a.MaxLatencyMS, row.LatencyMS)
	if row.UpstreamMS != nil {
		a.UpstreamMS += *row.UpstreamMS
		a.UpstreamMSCount++
		a.MaxUpstreamMS = adminMaxInt64(a.MaxUpstreamMS, *row.UpstreamMS)
	}
	if row.FallbackUsed {
		a.Fallbacks++
	}
}

func (a *adminDiagnosticAgg) row() adminScalarReportRow {
	requests := int64(len(a.RequestIDs))
	return adminScalarReportRow{
		Key:                     a.Key,
		SecondaryKey:            a.SecondaryKey,
		Status:                  a.Status,
		ErrorClass:              a.ErrorClass,
		Provider:                a.Provider,
		Model:                   a.Model,
		Dialect:                 a.Dialect,
		RequestShapeFingerprint: a.RequestShapeFingerprint,
		ToolSchemaFingerprint:   a.ToolSchemaFingerprint,
		Requests:                requests,
		Errors:                  a.Errors,
		ErrorRatePct:            ratioPct(a.Errors, adminMaxInt64(requests, 1)),
		Attempts:                a.Attempts,
		RetryableAttempts:       a.RetryableAttempts,
		TimeoutAttempts:         a.TimeoutAttempts,
		Fallbacks:               a.Fallbacks,
		FallbackRatePct:         ratioPct(a.Fallbacks, adminMaxInt64(requests, 1)),
		FallbackSucceeded:       a.FallbackSucceeded,
		FallbackFailed:          a.FallbackFailed,
		TerminalErrors:          a.TerminalErrors,
		UpstreamErrorDetails:    a.UpstreamErrorDetails,
		FieldsStrippedCount:     a.FieldsStripped,
		FieldsRewrittenCount:    a.FieldsRewritten,
		UnsupportedFieldsCount:  a.UnsupportedFields,
		TranslationWarningCount: a.TranslationWarnings,
		AffectedUsers:           int64(len(a.Users)),
		AffectedClients:         int64(len(a.Clients)),
		AvgLatencyMS:            avg(a.LatencyMS, requests),
		MaxLatencyMS:            a.MaxLatencyMS,
		AvgUpstreamMS:           avg(a.UpstreamMS, a.UpstreamMSCount),
		MaxUpstreamMS:           a.MaxUpstreamMS,
	}
}

func adminUsageRowsByRequestID(rows []usageRow) map[string]usageRow {
	out := make(map[string]usageRow, len(rows))
	for _, row := range rows {
		if row.RequestID != "" {
			out[row.RequestID] = row
		}
	}
	return out
}

func sortedRequestIDs(rows map[string]usageRow) []string {
	out := make([]string, 0, len(rows))
	for requestID := range rows {
		out = append(out, requestID)
	}
	sort.Strings(out)
	return out
}

func adminDiagnosticRowsFromAgg(table map[string]*adminDiagnosticAgg, sortBy string, limit int) []adminScalarReportRow {
	out := make([]adminScalarReportRow, 0, len(table))
	for _, agg := range table {
		out = append(out, agg.row())
	}
	sort.Slice(out, func(i, j int) bool {
		switch sortBy {
		case "errors":
			if out[i].Errors != out[j].Errors {
				return out[i].Errors > out[j].Errors
			}
		case "fallbacks":
			if out[i].Fallbacks != out[j].Fallbacks {
				return out[i].Fallbacks > out[j].Fallbacks
			}
		case "latency":
			if out[i].AvgLatencyMS != out[j].AvgLatencyMS {
				return out[i].AvgLatencyMS > out[j].AvgLatencyMS
			}
		default:
			if out[i].Requests != out[j].Requests {
				return out[i].Requests > out[j].Requests
			}
		}
		if out[i].Key == out[j].Key {
			return out[i].SecondaryKey < out[j].SecondaryKey
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func (s *Service) adminUpstreamFailureAggs(requestIDs []string, rows map[string]usageRow, opts UsageReportOptions) (map[string]*adminDiagnosticAgg, error) {
	var attempts []requestAttemptRecord
	q := s.usage.db.Where("request_id IN ? AND (status_code >= 400 OR error_class <> '' OR timed_out = ? OR client_canceled = ?)", requestIDs, true, true)
	q = applyAdminAttemptDiagnosticFilters(q, opts)
	if err := q.Order("ts ASC, request_id ASC, attempt_index ASC").Find(&attempts).Error; err != nil {
		return nil, err
	}
	table := map[string]*adminDiagnosticAgg{}
	for _, attempt := range attempts {
		row, ok := rows[attempt.RequestID]
		if !ok {
			continue
		}
		errorClass := defaultString(attempt.ErrorClass, upstreamFailureStatusClass(attempt.StatusCode, attempt.TimedOut, attempt.ClientCanceled))
		statusKey := "status:" + strconv.Itoa(attempt.StatusCode)
		if attempt.StatusCode == 0 {
			statusKey = "status:none"
		}
		key := joinKey(errorClass, statusKey)
		secondary := joinKey(defaultString(attempt.Provider, "unknown"), defaultString(attempt.Model, "unknown"), defaultString(attempt.Dialect, "unknown"))
		agg := adminDiagnosticAggFor(table, key, secondary)
		agg.ErrorClass = errorClass
		agg.Status = attempt.StatusCode
		agg.Provider = attempt.Provider
		agg.Model = attempt.Model
		agg.Dialect = attempt.Dialect
		agg.addRequest(row)
		agg.Errors++
		agg.Attempts++
		if attempt.Retryable {
			agg.RetryableAttempts++
		}
		if attempt.TimedOut {
			agg.TimeoutAttempts++
		}
	}
	if len(table) == 0 {
		return table, nil
	}
	var details []requestUpstreamErrorDetailRecord
	dq := s.usage.db.Where("request_id IN ?", requestIDs)
	dq = applyAdminUpstreamDetailDiagnosticFilters(dq, opts)
	if err := dq.Find(&details).Error; err != nil {
		return nil, err
	}
	for _, detail := range details {
		row, ok := rows[detail.RequestID]
		if !ok {
			continue
		}
		key := joinKey(defaultString(detail.ErrorClass, "unknown"), "field:"+defaultString(detail.FieldName, "unknown"))
		agg := adminDiagnosticAggFor(table, key, defaultString(detail.Source, "upstream"))
		agg.ErrorClass = detail.ErrorClass
		agg.Status = detail.StatusCode
		agg.addRequest(row)
		agg.UpstreamErrorDetails++
	}
	return table, nil
}

func upstreamFailureStatusClass(status int, timedOut, clientCanceled bool) string {
	switch {
	case timedOut:
		return "upstream_timeout"
	case clientCanceled:
		return "client_canceled"
	case status >= 500:
		return "upstream_5xx"
	case status >= 400:
		return "upstream_4xx"
	default:
		return "upstream_error"
	}
}

func (s *Service) adminRequestShapeMismatchAggs(requestIDs []string, rows map[string]usageRow, opts UsageReportOptions) (map[string]*adminDiagnosticAgg, error) {
	var shapes []requestShapeRecord
	sq := s.usage.db.Where("request_id IN ?", requestIDs)
	if opts.RequestShapeFP != "" {
		sq = sq.Where("request_shape_fingerprint = ?", opts.RequestShapeFP)
	}
	if opts.ToolSchemaFP != "" {
		sq = sq.Where("tool_schema_fingerprint = ?", opts.ToolSchemaFP)
	}
	if err := sq.Find(&shapes).Error; err != nil {
		return nil, err
	}
	shapeByRequestID := map[string]requestShapeRecord{}
	for _, shape := range shapes {
		shapeByRequestID[shape.RequestID] = shape
	}
	var translations []requestTranslationShapeRecord
	tq := s.usage.db.Where("request_id IN ?", requestIDs)
	tq = applyAdminTranslationDiagnosticFilters(tq, opts)
	if err := tq.Order("request_id ASC, attempt_index ASC").Find(&translations).Error; err != nil {
		return nil, err
	}
	table := map[string]*adminDiagnosticAgg{}
	for _, translated := range translations {
		row, ok := rows[translated.RequestID]
		if !ok {
			continue
		}
		shape := shapeByRequestID[translated.RequestID]
		for _, key := range adminTranslationMismatchKeys(shape, translated) {
			secondary := joinKey(defaultString(translated.Provider, "unknown"), defaultString(translated.Model, "unknown"), defaultString(translated.Dialect, "unknown"))
			agg := adminDiagnosticAggFor(table, key, secondary)
			agg.Provider = translated.Provider
			agg.Model = translated.Model
			agg.Dialect = translated.Dialect
			agg.RequestShapeFingerprint = defaultString(translated.RequestShapeFingerprint, shape.RequestShapeFingerprint)
			agg.ToolSchemaFingerprint = defaultString(translated.ToolSchemaFingerprint, shape.ToolSchemaFingerprint)
			agg.addRequest(row)
			agg.Attempts++
			if translated.UnsupportedFieldsPresent {
				agg.Errors++
			}
			agg.FieldsStripped += int64(translated.FieldsStrippedCount)
			agg.FieldsRewritten += int64(translated.FieldsRewrittenCount)
			if translated.UnsupportedFieldsPresent {
				agg.UnsupportedFields++
			}
			agg.TranslationWarnings += int64(translated.TranslationWarningCount)
		}
	}
	var events []requestTranslationFieldEventRecord
	eq := s.usage.db.Where("request_id IN ?", requestIDs)
	if err := eq.Order("request_id ASC, attempt_index ASC, seq ASC").Find(&events).Error; err != nil {
		return nil, err
	}
	for _, event := range events {
		row, ok := rows[event.RequestID]
		if !ok {
			continue
		}
		shape := shapeByRequestID[event.RequestID]
		key := joinKey("field", defaultString(event.FieldName, "other"), defaultString(event.Action, "unknown"))
		agg := adminDiagnosticAggFor(table, key, defaultString(event.Reason, "unspecified"))
		agg.RequestShapeFingerprint = shape.RequestShapeFingerprint
		agg.ToolSchemaFingerprint = shape.ToolSchemaFingerprint
		agg.addRequest(row)
		switch event.Action {
		case "stripped":
			agg.FieldsStripped++
		case "rewritten":
			agg.FieldsRewritten++
		case "unsupported":
			agg.UnsupportedFields++
			agg.Errors++
		}
	}
	return table, nil
}

func adminTranslationMismatchKeys(shape requestShapeRecord, translated requestTranslationShapeRecord) []string {
	keys := []string{}
	if translated.UnsupportedFieldsPresent {
		keys = append(keys, "unsupported-fields")
	}
	if translated.FieldsStrippedCount > 0 {
		keys = append(keys, "fields-stripped")
	}
	if translated.FieldsRewrittenCount > 0 {
		keys = append(keys, "fields-rewritten")
	}
	if translated.TranslationWarningCount > 0 {
		keys = append(keys, "translation-warning")
	}
	if shape.RequestID != "" {
		if shape.ToolCount != translated.TranslatedToolCount {
			keys = append(keys, "tool-count-changed")
		}
		if shape.ToolChoiceMode != "" && translated.TranslatedToolChoiceMode != "" && shape.ToolChoiceMode != translated.TranslatedToolChoiceMode {
			keys = append(keys, "tool-choice-changed")
		}
		if shape.RequestedOutputCapField != "" && translated.TranslatedOutputCapField != "" && shape.RequestedOutputCapField != translated.TranslatedOutputCapField {
			keys = append(keys, "output-cap-field-changed")
		}
		if shape.RequestedOutputCapBucket != "" && translated.TranslatedOutputCapBucket != "" && shape.RequestedOutputCapBucket != translated.TranslatedOutputCapBucket {
			keys = append(keys, "output-cap-bucket-changed")
		}
		if shape.ReasoningPresent && translated.TranslatedReasoningControl != "" {
			keys = append(keys, "reasoning-control-translated")
		}
		if shape.TotalRequestBytesBucket != "" && translated.TranslatedRequestBytesBucket != "" && shape.TotalRequestBytesBucket != translated.TranslatedRequestBytesBucket {
			keys = append(keys, "request-bytes-bucket-changed")
		}
	}
	if len(keys) == 0 {
		keys = append(keys, "translated-without-warning")
	}
	return dedupeSortedStrings(keys)
}

func (s *Service) adminFallbackHealthAggs(requestIDs []string, rows map[string]usageRow, opts UsageReportOptions) (map[string]*adminDiagnosticAgg, error) {
	var transitions []fallbackTransitionRecord
	q := s.usage.db.Where("request_id IN ?", requestIDs)
	q = applyAdminFallbackDiagnosticFilters(q, opts)
	if err := q.Order("request_id ASC, seq ASC").Find(&transitions).Error; err != nil {
		return nil, err
	}
	table := map[string]*adminDiagnosticAgg{}
	if len(transitions) == 0 {
		for _, row := range rows {
			if !adminFallbackParentRowMatches(row, opts) {
				continue
			}
			adminAddFallbackParentRow(table, row)
		}
		return table, nil
	}
	covered := map[string]bool{}
	for _, transition := range transitions {
		row, ok := rows[transition.RequestID]
		if !ok {
			continue
		}
		covered[transition.RequestID] = true
		key := joinKey(defaultString(transition.FallbackReason, "fallback"), defaultString(transition.ErrorClass, "unknown"))
		secondary := joinKey(defaultString(transition.FailedProvider, "unknown"), defaultString(transition.FailedModel, "unknown"), "to", defaultString(transition.FallbackProvider, "unknown"), defaultString(transition.FallbackModel, "unknown"))
		agg := adminDiagnosticAggFor(table, key, secondary)
		agg.ErrorClass = transition.ErrorClass
		agg.Provider = transition.FailedProvider
		agg.Model = transition.FailedModel
		agg.Dialect = transition.FailedDialect
		agg.addRequest(row)
		agg.Errors++
		agg.Attempts++
		if !row.FallbackUsed {
			agg.Fallbacks++
		}
		if transition.Retryable {
			agg.RetryableAttempts++
		}
		if transition.FallbackSucceeded {
			agg.FallbackSucceeded++
		} else {
			agg.FallbackFailed++
		}
	}
	for _, row := range rows {
		if covered[row.RequestID] || !adminFallbackParentRowMatches(row, opts) {
			continue
		}
		adminAddFallbackParentRow(table, row)
	}
	return table, nil
}

func adminFallbackParentRowMatches(row usageRow, opts UsageReportOptions) bool {
	if !row.FallbackUsed && row.Attempts <= 1 {
		return false
	}
	if opts.Status != 0 && row.Status != opts.Status {
		return false
	}
	if opts.TargetProvider != "" && row.TargetProvider != opts.TargetProvider {
		return false
	}
	if opts.TargetModel != "" && row.TargetModel != opts.TargetModel {
		return false
	}
	if opts.TargetDialect != "" && row.TargetDialect != opts.TargetDialect {
		return false
	}
	return true
}

func adminAddFallbackParentRow(table map[string]*adminDiagnosticAgg, row usageRow) {
	key := adminScalarDimension(row, "fallback_health")
	secondary := adminScalarDimension(row, "provider_model")
	agg := adminDiagnosticAggFor(table, key, secondary)
	agg.Provider = row.TargetProvider
	agg.Model = row.TargetModel
	agg.Dialect = row.TargetDialect
	agg.addRequest(row)
	if row.FallbackUsed {
		if row.Status < 400 {
			agg.FallbackSucceeded++
		} else {
			agg.FallbackFailed++
		}
	}
}

func (s *Service) adminClientImpactAggs(requestIDs []string, rows map[string]usageRow, opts UsageReportOptions) (map[string]*adminDiagnosticAgg, error) {
	var errors []requestErrorRecord
	q := s.usage.db.Where("request_id IN ?", requestIDs)
	q = applyAdminRequestErrorDiagnosticFilters(q, opts)
	if err := q.Find(&errors).Error; err != nil {
		return nil, err
	}
	errorByRequestID := map[string]requestErrorRecord{}
	for _, record := range errors {
		errorByRequestID[record.RequestID] = record
	}
	table := map[string]*adminDiagnosticAgg{}
	for _, row := range rows {
		record := errorByRequestID[row.RequestID]
		if opts.ErrorClass != "" && record.ErrorClass != opts.ErrorClass {
			continue
		}
		key := joinKey(defaultString(row.Client, "unknown"), defaultString(row.CallerUser, "unknown"))
		secondary := joinKey(defaultString(row.CallerProject, "unknown"), defaultString(row.CallerEnvironment, "unknown"), defaultString(row.ResolvedGroup, row.RequestedModel))
		agg := adminDiagnosticAggFor(table, key, secondary)
		agg.Status = row.Status
		agg.ErrorClass = record.ErrorClass
		agg.Provider = row.TargetProvider
		agg.Model = row.TargetModel
		agg.Dialect = row.TargetDialect
		agg.addRequest(row)
		agg.Attempts += int64(row.Attempts)
		if row.Status >= 400 {
			agg.Errors++
		}
		if record.RequestID != "" {
			agg.TerminalErrors++
		}
	}
	return table, nil
}

func applyAdminAttemptDiagnosticFilters(q *gorm.DB, opts UsageReportOptions) *gorm.DB {
	if opts.TargetProvider != "" {
		q = q.Where("provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("dialect = ?", opts.TargetDialect)
	}
	if opts.Status != 0 {
		q = q.Where("status_code = ?", opts.Status)
	}
	if opts.ErrorClass != "" {
		q = q.Where("error_class = ?", opts.ErrorClass)
	}
	return q
}

func applyAdminUpstreamDetailDiagnosticFilters(q *gorm.DB, opts UsageReportOptions) *gorm.DB {
	if opts.Status != 0 {
		q = q.Where("status_code = ?", opts.Status)
	}
	if opts.ErrorClass != "" {
		q = q.Where("error_class = ?", opts.ErrorClass)
	}
	return q
}

func applyAdminTranslationDiagnosticFilters(q *gorm.DB, opts UsageReportOptions) *gorm.DB {
	if opts.TargetProvider != "" {
		q = q.Where("provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("dialect = ?", opts.TargetDialect)
	}
	if opts.RequestShapeFP != "" {
		q = q.Where("request_shape_fingerprint = ?", opts.RequestShapeFP)
	}
	if opts.ToolSchemaFP != "" {
		q = q.Where("tool_schema_fingerprint = ?", opts.ToolSchemaFP)
	}
	return q
}

func applyAdminFallbackDiagnosticFilters(q *gorm.DB, opts UsageReportOptions) *gorm.DB {
	if opts.TargetProvider != "" {
		q = q.Where("(failed_provider = ? OR fallback_provider = ?)", opts.TargetProvider, opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("(failed_model = ? OR fallback_model = ?)", opts.TargetModel, opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("(failed_dialect = ? OR fallback_dialect = ?)", opts.TargetDialect, opts.TargetDialect)
	}
	if opts.ErrorClass != "" {
		q = q.Where("error_class = ?", opts.ErrorClass)
	}
	return q
}

func applyAdminRequestErrorDiagnosticFilters(q *gorm.DB, opts UsageReportOptions) *gorm.DB {
	if opts.TargetProvider != "" {
		q = q.Where("provider = ?", opts.TargetProvider)
	}
	if opts.TargetModel != "" {
		q = q.Where("model = ?", opts.TargetModel)
	}
	if opts.TargetDialect != "" {
		q = q.Where("dialect = ?", opts.TargetDialect)
	}
	if opts.Status != 0 {
		q = q.Where("status = ?", opts.Status)
	}
	if opts.ErrorClass != "" {
		q = q.Where("error_class = ?", opts.ErrorClass)
	}
	return q
}

func adminDiagnosticCharts(filters adminReportFilters, generatedAt string, spec adminScalarEndpointSpec, rows []adminScalarReportRow) []adminReportChart {
	if len(rows) == 0 {
		return nil
	}
	charts := []adminReportChart{
		adminCategoryChart(filters, generatedAt, spec.Report+"_requests", spec.Report+" affected requests", "Bucket", "Requests", "count", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Affected requests", "count", "magenta", rows, func(row adminScalarReportRow) float64 { return float64(row.Requests) }),
		}),
		adminCategoryChart(filters, generatedAt, spec.Report+"_errors", spec.Report+" errors", "Bucket", "Errors", "count", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Errors", "count", "red", rows, func(row adminScalarReportRow) float64 { return float64(row.Errors) }),
			adminChartSeriesFromScalarRows("Fallbacks", "count", "warning", rows, func(row adminScalarReportRow) float64 { return float64(row.Fallbacks) }),
		}),
	}
	if spec.Diagnostic == "request_shape_mismatches" {
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_translation_counts", spec.Report+" translation changes", "Bucket", "Events", "count", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Stripped", "count", "warning", rows, func(row adminScalarReportRow) float64 { return float64(row.FieldsStrippedCount) }),
			adminChartSeriesFromScalarRows("Rewritten", "count", "blue", rows, func(row adminScalarReportRow) float64 { return float64(row.FieldsRewrittenCount) }),
			adminChartSeriesFromScalarRows("Unsupported", "count", "red", rows, func(row adminScalarReportRow) float64 { return float64(row.UnsupportedFieldsCount) }),
		}))
	}
	if spec.Diagnostic == "fallback_health" {
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_outcomes", spec.Report+" outcomes", "Bucket", "Fallbacks", "count", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Succeeded", "count", "success", rows, func(row adminScalarReportRow) float64 { return float64(row.FallbackSucceeded) }),
			adminChartSeriesFromScalarRows("Failed", "count", "red", rows, func(row adminScalarReportRow) float64 { return float64(row.FallbackFailed) }),
		}))
	}
	return charts
}

type adminShapeAgg struct {
	Key       string
	Secondary string
	Agg       shapingMarkdownAgg
}

func adminShapeAggFor(table map[string]*adminShapeAgg, key, secondary string) *adminShapeAgg {
	key = defaultString(key, "unknown")
	mapKey := joinKey(key, secondary)
	if table[mapKey] == nil {
		table[mapKey] = &adminShapeAgg{Key: key, Secondary: secondary}
	}
	return table[mapKey]
}

func (a *adminShapeAgg) addCaller(row usageRow) {
	addCallerShape(&a.Agg, row)
}

func (a *adminShapeAgg) addUpstream(event upstreamShapeJoinedEvent) {
	addUpstreamShape(&a.Agg, event)
}

func (a *adminShapeAgg) row() adminScalarReportRow {
	return adminScalarReportRow{
		Key:                   a.Key,
		SecondaryKey:          a.Secondary,
		Requests:              a.Agg.Requests,
		Errors:                a.Agg.Rejected,
		ErrorRatePct:          ratioPct(a.Agg.Rejected, a.Agg.Requests),
		Fallbacks:             a.Agg.Fallbacks,
		FallbackRatePct:       ratioPct(a.Agg.Fallbacks, a.Agg.Requests),
		InputTokens:           a.Agg.EstimatedInput,
		OutputTokens:          a.Agg.ReservedOutput,
		Tokens:                a.Agg.TotalReserved,
		TotalTokens:           a.Agg.TotalReserved,
		Rejections:            a.Agg.Rejected,
		Queued:                a.Agg.Queued,
		SkippedTargets:        a.Agg.SkippedTargets,
		CooldownsStarted:      a.Agg.CooldownsStarted,
		AvgRetryAfterMS:       avg(a.Agg.RetryAfterMS, a.Agg.RetryAfterCount),
		MaxRetryAfterMS:       a.Agg.MaxRetryAfterMS,
		AvgQueueWaitMS:        avg(a.Agg.QueueWaitMS, a.Agg.QueueWaitCount),
		P50QueueWaitMS:        percentileInt64(a.Agg.QueueWaitSamples, 50),
		P95QueueWaitMS:        percentileInt64(a.Agg.QueueWaitSamples, 95),
		MaxQueueWaitMS:        a.Agg.MaxQueueWaitMS,
		EstimatedInputTokens:  a.Agg.EstimatedInput,
		ReservedOutputTokens:  a.Agg.ReservedOutput,
		TotalReservedTokens:   a.Agg.TotalReserved,
		Upstream429Attempts:   a.Agg.Upstream429,
		UpstreamQuotaAttempts: a.Agg.UpstreamQuota,
		RouteAroundSuccesses:  a.Agg.RouteAroundSuccess,
	}
}

func adminShapeRowsFromAgg(table map[string]*adminShapeAgg, sortKey string, limit int) []adminScalarReportRow {
	rows := make([]adminScalarReportRow, 0, len(table))
	for _, agg := range table {
		rows = append(rows, agg.row())
	}
	sort.Slice(rows, func(i, j int) bool {
		switch sortKey {
		case "errors":
			if rows[i].Errors != rows[j].Errors {
				return rows[i].Errors > rows[j].Errors
			}
		case "key":
			if rows[i].Key != rows[j].Key {
				return rows[i].Key < rows[j].Key
			}
			if rows[i].SecondaryKey != rows[j].SecondaryKey {
				return rows[i].SecondaryKey < rows[j].SecondaryKey
			}
			return rows[i].Requests > rows[j].Requests
		}
		if rows[i].Requests == rows[j].Requests {
			if rows[i].Key == rows[j].Key {
				return rows[i].SecondaryKey < rows[j].SecondaryKey
			}
			return rows[i].Key < rows[j].Key
		}
		return rows[i].Requests > rows[j].Requests
	})
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func adminShapeCallerKeys(row usageRow, spec adminScalarEndpointSpec) (string, string) {
	key := adminScalarDimension(row, spec.Dimension)
	secondary := adminShapeBucketKey(row.TrafficShapeScope, row.TrafficShapeBucket, row.TrafficShapeDecision)
	if spec.Secondary != "shape_bucket" {
		secondary = adminScalarDimension(row, spec.Secondary)
	}
	if spec.Dimension == "shape_surface" {
		key = "caller"
	}
	return key, secondary
}

func adminShapeUpstreamKeys(joined upstreamShapeJoinedEvent, spec adminScalarEndpointSpec) (string, string) {
	event := joined.Event
	switch spec.Dimension {
	case "shape_surface":
		return "provider/model", adminShapeBucketKey(event.Scope, event.Bucket, event.Decision)
	case "provider_model":
		return joinKey(defaultString(event.Provider, "unknown"), defaultString(event.Model, "unknown")), adminShapeBucketKey(event.Scope, event.Bucket, event.Decision)
	case "backoff_reason":
		return defaultString(event.BackoffReason, "adaptive-backoff"), joinKey(defaultString(event.Provider, "unknown"), defaultString(event.Model, "unknown"))
	default:
		return adminScalarDimension(joined.Row, spec.Dimension), adminShapeBucketKey(event.Scope, event.Bucket, event.Decision)
	}
}

func adminShapeBucketKey(scope, bucket, decision string) string {
	return joinKey(defaultString(scope, "unknown"), defaultString(bucket, "unknown"), defaultString(decision, "unknown"))
}

func adminShapeCharts(filters adminReportFilters, generatedAt string, spec adminScalarEndpointSpec, rows []adminScalarReportRow) []adminReportChart {
	byKey := map[string]float64{}
	byRejected := map[string]float64{}
	for _, row := range rows {
		byKey[row.Key] += float64(row.Requests)
		if row.Rejections > 0 {
			byRejected[row.Key] += float64(row.Rejections)
		}
		if row.SkippedTargets > 0 {
			byRejected[row.Key] += float64(row.SkippedTargets)
		}
	}
	return []adminReportChart{
		adminCategoryChart(filters, generatedAt, spec.Report+"_events", "Shaping events", "Category", "Events", "count", []adminReportChartSeries{
			adminSeriesFromCounts("Events", "count", "magenta", byKey),
		}),
		adminCategoryChart(filters, generatedAt, spec.Report+"_limited", "Limited or skipped", "Category", "Events", "count", []adminReportChartSeries{
			adminSeriesFromCounts("Limited", "count", "red", byRejected),
		}),
	}
}

type adminScalarKey struct {
	Key       string
	Secondary string
}

type adminScalarAgg struct {
	Key                     string
	SecondaryKey            string
	Agg                     agg
	InputImageCount         int64
	InputImageTokens        int64
	PIIFilteredRequests     int64
	PIIFilterReplacements   int64
	UpstreamReportedCostUSD float64
	BaselineCostUSD         float64
	HasBaseline             bool
}

func (a *adminScalarAgg) add(row usageRow, baseline adminSavingsBaselineDTO) {
	a.Agg.add(row)
	a.InputImageCount += int64(row.InputImageCount)
	a.InputImageTokens += int64(row.InputImageTokens)
	if row.PIIFilterApplied {
		a.PIIFilteredRequests++
	}
	a.PIIFilterReplacements += int64(row.PIIFilterReplacements)
	a.UpstreamReportedCostUSD += row.UpstreamReportedTotalCostUSD
	if baseline.BaselineID != "" {
		a.HasBaseline = true
		a.BaselineCostUSD += adminBaselineCost(row, baseline)
	}
}

func (a *adminScalarAgg) row() adminScalarReportRow {
	cacheable := a.Agg.CacheHits + a.Agg.CacheMisses
	savings := a.BaselineCostUSD - a.Agg.TotalCostUSD
	row := adminScalarReportRow{
		Key:                                  a.Key,
		SecondaryKey:                         a.SecondaryKey,
		Requests:                             a.Agg.Calls,
		Errors:                               a.Agg.Errors,
		ErrorRatePct:                         ratioPct(a.Agg.Errors, a.Agg.Calls),
		Streams:                              a.Agg.Streams,
		Attempts:                             a.Agg.Attempts,
		Fallbacks:                            a.Agg.Fallbacks,
		FallbackRatePct:                      ratioPct(a.Agg.Fallbacks, a.Agg.Calls),
		CacheHits:                            a.Agg.CacheHits,
		CacheMisses:                          a.Agg.CacheMisses,
		CacheBypass:                          a.Agg.CacheBypass,
		CacheHitRatePct:                      ratioPct(a.Agg.CacheHits, cacheable),
		InputTokens:                          a.Agg.InputTokens,
		OutputTokens:                         a.Agg.OutputTokens,
		Tokens:                               a.Agg.TotalTokens,
		TotalTokens:                          a.Agg.TotalTokens,
		InputImageCount:                      a.InputImageCount,
		InputImageTokens:                     a.InputImageTokens,
		PIIFilteredRequests:                  a.PIIFilteredRequests,
		PIIFilterReplacements:                a.PIIFilterReplacements,
		CostUSD:                              a.Agg.TotalCostUSD,
		InputCostUSD:                         a.Agg.InputCostUSD,
		ImageCostUSD:                         a.Agg.ImageCostUSD,
		OutputCostUSD:                        a.Agg.OutputCostUSD,
		TotalCostUSD:                         a.Agg.TotalCostUSD,
		UpstreamReportedCostUSD:              a.UpstreamReportedCostUSD,
		AvgCostUSD:                           avgFloat(a.Agg.TotalCostUSD, a.Agg.Calls),
		AvgLatencyMS:                         avg(a.Agg.LatencyMS, a.Agg.Calls),
		MaxLatencyMS:                         a.Agg.MaxLatencyMS,
		AvgTTFBMS:                            avg(a.Agg.TTFBMS, a.Agg.TTFBCount),
		MaxTTFBMS:                            a.Agg.MaxTTFBMS,
		AvgUpstreamMS:                        avg(a.Agg.UpstreamMS, a.Agg.UpstreamMSCount),
		MaxUpstreamMS:                        a.Agg.MaxUpstreamMS,
		AvgDownstreamMS:                      avg(a.Agg.DownstreamMS, a.Agg.DownstreamMSCount),
		MaxDownstreamMS:                      a.Agg.MaxDownstreamMS,
		AvgUpstreamTokensPerSec:              avgFloat(a.Agg.UpstreamOutputTPS, a.Agg.UpstreamOutputTPSCount),
		AvgUpstreamOutputTokensPerSec:        avgFloat(a.Agg.UpstreamOutputTPS, a.Agg.UpstreamOutputTPSCount),
		AvgUpstreamTotalTokensPerSec:         avgFloat(a.Agg.UpstreamTotalTPS, a.Agg.UpstreamTotalTPSCount),
		AvgDownstreamTokensPerSec:            avgFloat(a.Agg.DownstreamOutputTPS, a.Agg.DownstreamOutputTPSCount),
		AvgDownstreamWriteOutputTokensPerSec: avgFloat(a.Agg.DownstreamOutputTPS, a.Agg.DownstreamOutputTPSCount),
		AvgDownstreamWriteTotalTokensPerSec:  avgFloat(a.Agg.DownstreamTotalTPS, a.Agg.DownstreamTotalTPSCount),
		LatestCacheItems:                     a.Agg.CacheItemsLatest,
		LatestCacheBytes:                     a.Agg.CacheBytesLatest,
		LatestCacheMaxBytes:                  a.Agg.CacheMaxBytesLatest,
		LatestCacheOccupancyPct:              a.Agg.CacheOccupancyLatest,
	}
	if a.HasBaseline {
		actualCost := a.Agg.TotalCostUSD
		savingsPct := ratioPctFloat(savings, a.BaselineCostUSD)
		row.ActualCostUSD = &actualCost
		row.BaselineCostUSD = &a.BaselineCostUSD
		row.SavingsUSD = &savings
		row.SavingsPct = &savingsPct
	}
	return row
}

func adminScalarRowsFromAgg(data map[string]*adminScalarAgg, sortBy string, limit int) []adminScalarReportRow {
	out := make([]adminScalarReportRow, 0, len(data))
	for _, a := range data {
		out = append(out, a.row())
	}
	sort.Slice(out, func(i, j int) bool {
		switch sortBy {
		case "savings":
			iSavings := scalarSavingsValue(out[i])
			jSavings := scalarSavingsValue(out[j])
			if iSavings != jSavings {
				return iSavings > jSavings
			}
		case "cost":
			if out[i].CostUSD != out[j].CostUSD {
				return out[i].CostUSD > out[j].CostUSD
			}
		case "tokens":
			if out[i].Tokens != out[j].Tokens {
				return out[i].Tokens > out[j].Tokens
			}
		case "latency":
			if out[i].AvgLatencyMS != out[j].AvgLatencyMS {
				return out[i].AvgLatencyMS > out[j].AvgLatencyMS
			}
		case "errors":
			if out[i].Errors != out[j].Errors {
				return out[i].Errors > out[j].Errors
			}
		case "fallbacks":
			if out[i].Fallbacks != out[j].Fallbacks {
				return out[i].Fallbacks > out[j].Fallbacks
			}
		case "image":
			if out[i].InputImageCount != out[j].InputImageCount {
				return out[i].InputImageCount > out[j].InputImageCount
			}
		case "key":
		default:
			if out[i].Requests != out[j].Requests {
				return out[i].Requests > out[j].Requests
			}
		}
		if out[i].Key == out[j].Key {
			return out[i].SecondaryKey < out[j].SecondaryKey
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func adminScalarKeys(row usageRow, spec adminScalarEndpointSpec) []adminScalarKey {
	if spec.Anomalies {
		return adminAnomalyKeys(row, spec)
	}
	if spec.Dimension == "troubleshooting_bucket" {
		secondary := adminScalarDimension(row, spec.Secondary)
		buckets := adminTroubleshootingBuckets(row)
		keys := make([]adminScalarKey, 0, len(buckets))
		for _, bucket := range buckets {
			keys = append(keys, adminScalarKey{Key: bucket, Secondary: secondary})
		}
		return keys
	}
	if spec.Dimension == "capability" {
		secondary := adminScalarDimension(row, spec.Secondary)
		caps := adminCapabilityKeys(row)
		keys := make([]adminScalarKey, 0, len(caps))
		for _, cap := range caps {
			keys = append(keys, adminScalarKey{Key: cap, Secondary: secondary})
		}
		return keys
	}
	if spec.Dimension == "dynamic_signal" || spec.Dimension == "dynamic_score_bucket" || spec.Dimension == "dynamic_threshold" {
		secondary := adminScalarDimension(row, spec.Secondary)
		values := adminMultiBucketValues(row, spec.Dimension)
		keys := make([]adminScalarKey, 0, len(values))
		for _, value := range values {
			keys = append(keys, adminScalarKey{Key: value, Secondary: secondary})
		}
		return keys
	}
	key := adminScalarDimension(row, spec.Dimension)
	secondary := adminScalarDimension(row, spec.Secondary)
	return []adminScalarKey{{Key: key, Secondary: secondary}}
}

func adminScalarDimension(row usageRow, dimension string) string {
	switch dimension {
	case "":
		return ""
	case "caller_id":
		return defaultString(row.CallerID, "unknown")
	case "caller_user":
		return defaultString(row.CallerUser, "unknown")
	case "token_id":
		return defaultString(row.TokenID, "unknown")
	case "requested_model":
		return defaultString(row.RequestedModel, "unknown")
	case "model_group":
		return defaultString(row.ResolvedGroup, row.RequestedModel)
	case "provider_model":
		return joinKey(defaultString(row.TargetProvider, "unknown"), defaultString(row.TargetModel, "unknown"))
	case "dialect":
		return defaultString(row.TargetDialect, "unknown")
	case "client":
		return defaultString(row.Client, "unknown")
	case "inbound_dialect":
		return defaultString(row.InboundDialect, "unknown")
	case "status_error":
		errorKey := "ok"
		if row.Status >= 400 {
			errorKey = defaultString(row.Error, "error")
		}
		return joinKey(strconv.Itoa(row.Status), errorKey)
	case "upstream_failure":
		status := strconv.Itoa(row.Status)
		if row.Status == 0 {
			status = "status:unknown"
		}
		code := defaultString(row.UpstreamErrorCode, row.Error)
		param := defaultString(row.UpstreamErrorParam, "param:none")
		messageCategory := defaultString(row.UpstreamErrorMessageCategory, "provider_message:none")
		return joinKey(defaultString(row.TargetProvider, "unknown"), defaultString(row.TargetModel, "unknown"), defaultString(row.TargetDialect, "unknown"), status, defaultString(code, "error:none"), param, messageCategory)
	case "request_shape_failure":
		shape := defaultString(row.TranslationShapeBucket, row.RequestShapeBucket)
		if shape == "" {
			shape = joinKey(defaultString(row.InboundDialect, "unknown"), defaultString(row.TargetDialect, "unknown"), defaultString(row.InputTokenBucket, "tokens:unknown"), defaultString(row.MaxTokenBucket, "output:unknown"))
		}
		fp := defaultString(row.RequestShapeFingerprint, "shape-fp:none")
		toolFP := defaultString(row.ToolSchemaFingerprint, "tool-fp:none")
		return joinKey(shape, fp, toolFP)
	case "fallback_health":
		status := "success"
		switch {
		case row.Status >= 500:
			status = "terminal-5xx"
		case row.Status >= 400:
			status = "terminal-4xx"
		}
		fallback := "fallback:not-used"
		if row.FallbackUsed {
			if row.Status < 400 {
				fallback = "fallback:recovered"
			} else {
				fallback = "fallback:failed"
			}
		}
		attempts := "attempts:single"
		if row.Attempts > 1 {
			attempts = "attempts:multi"
		}
		return joinKey(defaultString(row.ResolvedGroup, row.RequestedModel), fallback, attempts, status)
	case "caller_user_client":
		return joinKey(defaultString(row.CallerUser, "unknown"), defaultString(row.Client, "unknown"))
	case "cache":
		return defaultString(row.Cache, "bypass")
	case "quota_key_state":
		return joinKey(defaultString(row.QuotaState, "unknown"), defaultString(row.KeyState, "unknown"))
	case "routing_decision":
		return joinKey(defaultString(row.Strategy, "unknown"), defaultString(row.ResolvedGroup, row.RequestedModel))
	case "max_token_bucket":
		return defaultString(row.MaxTokenBucket, "unknown")
	case "input_token_bucket":
		return defaultString(row.InputTokenBucket, "unknown")
	case "admission_reason":
		return defaultString(row.AdmissionReason, "admitted")
	case "contract_bucket":
		if !row.ContractPresent {
			return "none"
		}
		return defaultString(row.ContractBucket, "unknown")
	case "contract_workload":
		if !row.ContractPresent {
			return "none"
		}
		return defaultString(row.ContractWorkload, "unspecified")
	case "target_validation":
		return joinKey(defaultString(row.TargetValidationStatus, "missing"), defaultString(row.TargetValidationAgeBucket, "missing"))
	case "project":
		return defaultString(row.CallerProject, "unknown")
	case "environment":
		return defaultString(row.CallerEnvironment, "unknown")
	case "capability":
		return "capability"
	case "troubleshooting_bucket":
		return "troubleshooting"
	default:
		return "unknown"
	}
}

func adminMultiBucketValues(row usageRow, dimension string) []string {
	var values []string
	switch dimension {
	case "dynamic_signal":
		values = append(values, row.EnabledSignals...)
	case "dynamic_score_bucket":
		values = append(values, row.ScoreBuckets...)
	case "dynamic_threshold":
		values = append(values, row.ThresholdBuckets...)
	}
	if len(values) == 0 {
		return []string{"none"}
	}
	sort.Strings(values)
	return values
}

func adminTroubleshootingBuckets(row usageRow) []string {
	buckets := []string{}
	errorText := strings.ToLower(row.Error)
	quotaState := strings.ToLower(row.QuotaState)
	keyState := strings.ToLower(row.KeyState)
	switch {
	case quotaState != "" && quotaState != "ok":
		buckets = append(buckets, "quota:"+quotaState)
	case strings.Contains(errorText, "quota"):
		buckets = append(buckets, "quota:error")
	}
	switch {
	case strings.Contains(errorText, "tpm") || strings.Contains(errorText, "token rate"):
		buckets = append(buckets, "tpm")
	case strings.Contains(errorText, "rpm") || strings.Contains(errorText, "rate limit") || row.Status == http.StatusTooManyRequests:
		buckets = append(buckets, "rpm-rate-limit")
	}
	if strings.Contains(errorText, "concurrency") || strings.Contains(errorText, "in-flight") {
		buckets = append(buckets, "concurrency")
	}
	if strings.Contains(errorText, "max token") || strings.Contains(errorText, "max_tokens") || strings.Contains(errorText, "max_output_tokens") || strings.Contains(errorText, "context length") {
		buckets = append(buckets, "max-token-or-context")
	}
	if strings.Contains(errorText, "upstream-quota") || strings.Contains(errorText, "billing") || strings.Contains(errorText, "credits") {
		buckets = append(buckets, "upstream-quota-billing")
	}
	if keyState != "" && keyState != "ok" && keyState != "active" {
		buckets = append(buckets, "key:"+keyState)
	}
	if row.Cache == "hit" {
		buckets = append(buckets, "cache-hit")
	}
	if row.Cache == "bypass" {
		buckets = append(buckets, "cache-bypass")
	}
	if row.FallbackUsed {
		buckets = append(buckets, "fallback")
	}
	if row.Attempts > 1 {
		buckets = append(buckets, "multi-attempt")
	}
	if row.Status >= 500 {
		buckets = append(buckets, "server-error")
	} else if row.Status >= 400 {
		buckets = append(buckets, "client-error")
	}
	if len(buckets) == 0 {
		buckets = append(buckets, "ok")
	}
	return dedupeSortedStrings(buckets)
}

func adminCapabilityKeys(row usageRow) []string {
	caps := []string{}
	if row.InputHasImage || row.InputImageCount > 0 || row.InputImageTokens > 0 {
		caps = append(caps, "image-input")
	}
	if row.Stream {
		caps = append(caps, "streaming")
	}
	if row.PIIFilterApplied {
		caps = append(caps, "pii-filtered")
	}
	if row.Cache == "hit" || row.Cache == "miss" {
		caps = append(caps, "cacheable")
	}
	if row.TargetDialect != "" {
		caps = append(caps, "dialect:"+row.TargetDialect)
	}
	if len(caps) == 0 {
		caps = append(caps, "text")
	}
	return caps
}

func adminChartSeriesFromScalarRows(name, unit, colorKey string, rows []adminScalarReportRow, value func(adminScalarReportRow) float64) adminReportChartSeries {
	points := make([]adminReportChartPoint, 0, len(rows))
	for _, row := range rows {
		key := row.Key
		if row.SecondaryKey != "" {
			key = joinKey(row.Key, row.SecondaryKey)
		}
		points = append(points, adminReportChartPoint{X: key, Y: value(row)})
	}
	return adminReportChartSeries{Name: name, Unit: unit, ColorKey: colorKey, Points: points}
}

func adminAnomalyKeys(row usageRow, spec adminScalarEndpointSpec) []adminScalarKey {
	secondary := adminScalarDimension(row, spec.Secondary)
	keys := []adminScalarKey{}
	if row.Status >= 400 {
		keys = append(keys, adminScalarKey{Key: "error", Secondary: secondary})
	}
	if row.FallbackUsed {
		keys = append(keys, adminScalarKey{Key: "fallback", Secondary: secondary})
	}
	if row.Attempts > 1 {
		keys = append(keys, adminScalarKey{Key: "multi-attempt", Secondary: secondary})
	}
	if row.LatencyMS >= 30000 {
		keys = append(keys, adminScalarKey{Key: "slow-request", Secondary: secondary})
	}
	if row.TotalCostUSD >= 1 {
		keys = append(keys, adminScalarKey{Key: "expensive-request", Secondary: secondary})
	}
	if row.QuotaState != "" && row.QuotaState != "ok" {
		keys = append(keys, adminScalarKey{Key: "quota-" + row.QuotaState, Secondary: secondary})
	}
	if row.KeyState != "" && row.KeyState != "ok" && row.KeyState != "active" {
		keys = append(keys, adminScalarKey{Key: "key-" + row.KeyState, Secondary: secondary})
	}
	return keys
}

func scalarSavingsValue(row adminScalarReportRow) float64 {
	if row.SavingsUSD == nil {
		return 0
	}
	return *row.SavingsUSD
}

func scalarBaselineCostValue(row adminScalarReportRow) float64 {
	if row.BaselineCostUSD == nil {
		return 0
	}
	return *row.BaselineCostUSD
}

func scalarSavingsPctValue(row adminScalarReportRow) float64 {
	if row.SavingsPct == nil {
		return 0
	}
	return *row.SavingsPct
}

func adminScalarCharts(filters adminReportFilters, generatedAt string, spec adminScalarEndpointSpec, rows []adminScalarReportRow) []adminReportChart {
	if len(rows) == 0 {
		return nil
	}
	charts := []adminReportChart{
		adminCategoryChart(filters, generatedAt, spec.Report+"_requests", spec.Report+" requests", "Key", "Requests", "count", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Requests", "count", "magenta", rows, func(row adminScalarReportRow) float64 { return float64(row.Requests) }),
		}),
	}
	switch spec.Sort {
	case "cost":
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_cost", spec.Report+" cost", "Key", "USD", "usd", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Cost", "usd", "red", rows, func(row adminScalarReportRow) float64 { return row.CostUSD }),
		}))
	case "savings":
		if spec.WithBaseline && scalarRowsHaveBaseline(rows) {
			charts = append(charts,
				adminCategoryChart(filters, generatedAt, spec.Report+"_cost", "Actual vs baseline cost", "Key", "USD", "usd", []adminReportChartSeries{
					adminChartSeriesFromScalarRows("Actual cost", "usd", "red", rows, func(row adminScalarReportRow) float64 { return row.CostUSD }),
					adminChartSeriesFromScalarRows("Baseline cost", "usd", "blue", rows, scalarBaselineCostValue),
				}),
				adminCategoryChart(filters, generatedAt, spec.Report+"_savings", "Savings", "Key", "USD", "usd", []adminReportChartSeries{
					adminChartSeriesFromScalarRows("Savings", "usd", "magenta", rows, scalarSavingsValue),
				}),
				adminCategoryChart(filters, generatedAt, spec.Report+"_savings_pct", "Savings rate", "Key", "Percent", "percent", []adminReportChartSeries{
					adminChartSeriesFromScalarRows("Savings rate", "percent", "purple", rows, scalarSavingsPctValue),
				}),
			)
			break
		}
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_cost", spec.Report+" cost", "Key", "USD", "usd", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Cost", "usd", "red", rows, func(row adminScalarReportRow) float64 { return row.CostUSD }),
		}))
	case "latency":
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_latency", spec.Report+" latency", "Key", "Milliseconds", "ms", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Avg latency", "ms", "violet", rows, func(row adminScalarReportRow) float64 { return float64(row.AvgLatencyMS) }),
			adminChartSeriesFromScalarRows("Max latency", "ms", "red", rows, func(row adminScalarReportRow) float64 { return float64(row.MaxLatencyMS) }),
		}))
	case "errors":
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_errors", spec.Report+" errors", "Key", "Errors", "count", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Errors", "count", "red", rows, func(row adminScalarReportRow) float64 { return float64(row.Errors) }),
			adminChartSeriesFromScalarRows("Fallbacks", "count", "warning", rows, func(row adminScalarReportRow) float64 { return float64(row.Fallbacks) }),
		}))
	default:
		charts = append(charts, adminCategoryChart(filters, generatedAt, spec.Report+"_tokens", spec.Report+" token volume", "Key", "Token count", "tokens", []adminReportChartSeries{
			adminChartSeriesFromScalarRows("Input Tokens", "tokens", "blue", rows, func(row adminScalarReportRow) float64 { return float64(row.InputTokens) }),
			adminChartSeriesFromScalarRows("Output Tokens", "tokens", "magenta", rows, func(row adminScalarReportRow) float64 { return float64(row.OutputTokens) }),
			adminChartSeriesFromScalarRows("Total Tokens", "tokens", "purple", rows, func(row adminScalarReportRow) float64 { return float64(row.TotalTokens) }),
		}))
	}
	return charts
}

func scalarRowsHaveBaseline(rows []adminScalarReportRow) bool {
	for _, row := range rows {
		if row.BaselineCostUSD != nil || row.SavingsUSD != nil || row.SavingsPct != nil {
			return true
		}
	}
	return false
}

func adminRequestCharts(filters adminReportFilters, generatedAt string, spec adminScalarEndpointSpec, rows []adminReportRequest) []adminReportChart {
	if len(rows) == 0 {
		return nil
	}
	return []adminReportChart{
		adminCategoryChart(filters, generatedAt, spec.Report+"_request_cost", spec.Report+" request cost", "Request", "USD", "usd", []adminReportChartSeries{
			adminChartSeriesFromRequestRows("Cost", "usd", "red", rows, func(row adminReportRequest) float64 { return row.TotalCostUSD }),
		}),
		adminCategoryChart(filters, generatedAt, spec.Report+"_request_latency", spec.Report+" request latency", "Request", "Milliseconds", "ms", []adminReportChartSeries{
			adminChartSeriesFromRequestRows("Latency", "ms", "violet", rows, func(row adminReportRequest) float64 { return float64(row.LatencyMS) }),
			adminChartSeriesFromRequestRows("TTFB", "ms", "blue", rows, func(row adminReportRequest) float64 {
				if row.TTFBMS == nil {
					return 0
				}
				return float64(*row.TTFBMS)
			}),
		}),
		adminCategoryChart(filters, generatedAt, spec.Report+"_request_tokens", spec.Report+" request token volume", "Request", "Token count", "tokens", []adminReportChartSeries{
			adminChartSeriesFromRequestRows("Input Tokens", "tokens", "blue", rows, func(row adminReportRequest) float64 { return float64(row.InputTokens) }),
			adminChartSeriesFromRequestRows("Output Tokens", "tokens", "magenta", rows, func(row adminReportRequest) float64 { return float64(row.OutputTokens) }),
			adminChartSeriesFromRequestRows("Total Tokens", "tokens", "purple", rows, func(row adminReportRequest) float64 { return float64(row.TotalTokens) }),
		}),
	}
}

func adminChartSeriesFromRequestRows(name, unit, colorKey string, rows []adminReportRequest, value func(adminReportRequest) float64) adminReportChartSeries {
	points := make([]adminReportChartPoint, 0, len(rows))
	for _, row := range rows {
		label := row.RequestID
		if label == "" {
			label = row.TimeUTC
		}
		points = append(points, adminReportChartPoint{X: label, Y: value(row)})
	}
	return adminReportChartSeries{Name: name, Unit: unit, ColorKey: colorKey, Points: points}
}

type adminSavingsAgg struct {
	Requests        int64
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	ActualCostUSD   float64
	BaselineCostUSD float64
}

func adminSavingsAggFor(data map[string]*adminSavingsAgg, key string) *adminSavingsAgg {
	if data[key] == nil {
		data[key] = &adminSavingsAgg{}
	}
	return data[key]
}

func (a *adminSavingsAgg) add(row usageRow, baseline adminSavingsBaselineDTO) {
	a.Requests++
	a.InputTokens += int64(row.InputTokens)
	a.OutputTokens += int64(row.OutputTokens)
	a.TotalTokens += int64(totalTokens(Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.TotalTokens}))
	a.ActualCostUSD += row.TotalCostUSD
	a.BaselineCostUSD += adminBaselineCost(row, baseline)
}

func (a *adminSavingsAgg) row(key string) adminSavingsRow {
	savings := a.BaselineCostUSD - a.ActualCostUSD
	return adminSavingsRow{
		Key:             key,
		Requests:        a.Requests,
		InputTokens:     a.InputTokens,
		OutputTokens:    a.OutputTokens,
		TotalTokens:     a.TotalTokens,
		ActualCostUSD:   a.ActualCostUSD,
		BaselineCostUSD: a.BaselineCostUSD,
		SavingsUSD:      savings,
		SavingsPct:      ratioPctFloat(savings, a.BaselineCostUSD),
	}
}

func adminBaselineCost(row usageRow, baseline adminSavingsBaselineDTO) float64 {
	return (float64(row.InputTokens) / 1_000_000 * baseline.BaselineInputPricePerMillionUSD) +
		(float64(row.OutputTokens) / 1_000_000 * baseline.BaselineOutputPricePerMillionUSD)
}

func ratioPctFloat(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return part / total * 100
}

func adminSavingsRowsFromAgg(data map[string]*adminSavingsAgg) []adminSavingsRow {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]adminSavingsRow, 0, len(keys))
	for _, key := range keys {
		out = append(out, data[key].row(key))
	}
	return out
}

func adminSavingsCharts(filters adminReportFilters, generatedAt string, rows []adminSavingsRow) []adminReportChart {
	return []adminReportChart{
		adminTimeSavingsChart(filters, generatedAt, "savings_cost", "Actual vs baseline cost", "USD", "usd", []adminReportChartSeries{
			adminSavingsChartSeries("Actual cost", "usd", "red", rows, func(row adminSavingsRow) float64 { return row.ActualCostUSD }),
			adminSavingsChartSeries("Baseline cost", "usd", "blue", rows, func(row adminSavingsRow) float64 { return row.BaselineCostUSD }),
		}),
		adminTimeSavingsChart(filters, generatedAt, "savings_usd", "Savings over time", "USD", "usd", []adminReportChartSeries{
			adminSavingsChartSeries("Savings", "usd", "magenta", rows, func(row adminSavingsRow) float64 { return row.SavingsUSD }),
		}),
		adminTimeSavingsChart(filters, generatedAt, "savings_pct", "Savings rate over time", "Percent", "percent", []adminReportChartSeries{
			adminSavingsChartSeries("Savings rate", "percent", "purple", rows, func(row adminSavingsRow) float64 { return row.SavingsPct }),
		}),
	}
}

func adminTimeSavingsChart(filters adminReportFilters, generatedAt, id, title, yLabel, yUnit string, series []adminReportChartSeries) adminReportChart {
	return adminReportChart{
		ChartID:     id,
		Title:       title,
		XAxis:       adminReportChartAxis{Label: "Time", Type: "time", Unit: "UTC hour"},
		YAxis:       adminReportChartAxis{Label: yLabel, Type: "linear", Unit: yUnit},
		Series:      series,
		GeneratedAt: generatedAt,
		From:        formatUsageTime(filters.From),
		To:          formatUsageTime(filters.To),
		Filters:     adminFilterDTO(filters),
	}
}

func adminSavingsChartSeries(name, unit, colorKey string, rows []adminSavingsRow, value func(adminSavingsRow) float64) adminReportChartSeries {
	points := make([]adminReportChartPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, adminReportChartPoint{X: row.Key, XUnixMs: adminReportTimeUnixMs(row.Key), Y: value(row)})
	}
	return adminReportChartSeries{Name: name, Unit: unit, ColorKey: colorKey, Points: points}
}

func adminRecentRequestsFromRows(rows []usageRow, limit int) []adminReportRequest {
	if limit <= 0 || len(rows) == 0 {
		return nil
	}
	recent := append([]usageRow(nil), rows...)
	sort.Slice(recent, func(i, j int) bool { return recent[i].TS.After(recent[j].TS) })
	if len(recent) > limit {
		recent = recent[:limit]
	}
	requests := make([]adminReportRequest, 0, len(recent))
	for _, row := range recent {
		requests = append(requests, adminRequestFromRow(row))
	}
	return requests
}

func addAdminAgg(m map[string]*agg, key string, row usageRow) {
	a := getAgg(m, key)
	a.add(row)
}

func adminSummaryFromAgg(a *agg) adminReportSummary {
	return adminReportSummary{
		Requests:                    a.Calls,
		Errors:                      a.Errors,
		Tokens:                      a.TotalTokens,
		TotalTokens:                 a.TotalTokens,
		InputTokens:                 a.InputTokens,
		OutputTokens:                a.OutputTokens,
		CostUSD:                     a.TotalCostUSD,
		TotalCostUSD:                a.TotalCostUSD,
		ImageCostUSD:                a.ImageCostUSD,
		Attempts:                    a.Attempts,
		Fallbacks:                   a.Fallbacks,
		Streams:                     a.Streams,
		AvgLatencyMS:                avg(a.LatencyMS, a.Calls),
		MaxLatencyMS:                a.MaxLatencyMS,
		AvgTTFBMS:                   avg(a.TTFBMS, a.TTFBCount),
		AvgUpstreamTPS:              avgFloat(a.UpstreamOutputTPS, a.UpstreamOutputTPSCount),
		AvgUpstreamOutputTPS:        avgFloat(a.UpstreamOutputTPS, a.UpstreamOutputTPSCount),
		AvgUpstreamTotalTPS:         avgFloat(a.UpstreamTotalTPS, a.UpstreamTotalTPSCount),
		AvgDownstreamTPS:            avgFloat(a.DownstreamOutputTPS, a.DownstreamOutputTPSCount),
		AvgDownstreamWriteOutputTPS: avgFloat(a.DownstreamOutputTPS, a.DownstreamOutputTPSCount),
		AvgDownstreamWriteTotalTPS:  avgFloat(a.DownstreamTotalTPS, a.DownstreamTotalTPSCount),
	}
}

func adminCacheFromAgg(a *agg) adminReportCache {
	cacheable := a.CacheHits + a.CacheMisses
	return adminReportCache{
		Hits:          a.CacheHits,
		Misses:        a.CacheMisses,
		Bypass:        a.CacheBypass,
		HitRate:       ratioPct(a.CacheHits, cacheable),
		LatestItems:   a.CacheItemsLatest,
		LatestBytes:   a.CacheBytesLatest,
		MaxBytes:      a.CacheMaxBytesLatest,
		LatestPercent: a.CacheOccupancyLatest,
	}
}

func adminRowsFromAgg(data map[string]*agg) []adminReportTableRow {
	out := make([]adminReportTableRow, 0, len(data))
	for _, key := range sortedAggKeys(data) {
		a := data[key]
		out = append(out, adminReportTableRow{Key: key, Requests: a.Calls, Errors: a.Errors, Tokens: a.TotalTokens, TotalTokens: a.TotalTokens, InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CostUSD: a.TotalCostUSD, InputCostUSD: a.InputCostUSD, ImageCostUSD: a.ImageCostUSD, OutputCostUSD: a.OutputCostUSD, TotalCostUSD: a.TotalCostUSD, Attempts: a.Attempts, Fallbacks: a.Fallbacks, AvgLatencyMS: avg(a.LatencyMS, a.Calls), MaxLatencyMS: a.MaxLatencyMS})
	}
	return out
}

func adminRowsFromSQLRecords(records []tokenScalarAggRecord) []adminReportTableRow {
	out := make([]adminReportTableRow, 0, len(records))
	for _, rec := range records {
		a := aggFromTokenScalarAggRecord(rec)
		key := defaultString(rec.Key, "unknown")
		out = append(out, adminReportTableRow{Key: key, Requests: a.Calls, Errors: a.Errors, Tokens: a.TotalTokens, TotalTokens: a.TotalTokens, InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CostUSD: a.TotalCostUSD, InputCostUSD: a.InputCostUSD, ImageCostUSD: a.ImageCostUSD, OutputCostUSD: a.OutputCostUSD, TotalCostUSD: a.TotalCostUSD, Attempts: a.Attempts, Fallbacks: a.Fallbacks, AvgLatencyMS: avg(a.LatencyMS, a.Calls), MaxLatencyMS: a.MaxLatencyMS})
	}
	return out
}

func adminSeriesFromAgg(data map[string]*agg) []adminReportSeries {
	keys := sortedAggKeys(data)
	sort.Strings(keys)
	out := make([]adminReportSeries, 0, len(keys))
	for _, key := range keys {
		a := data[key]
		out = append(out, adminSeriesFromAggValue(key, *a, 0, false))
	}
	return out
}

func adminSeriesFromSQLRecords(records []tokenScalarAggRecord, baseline adminSavingsBaselineDTO) []adminReportSeries {
	out := make([]adminReportSeries, 0, len(records))
	for _, rec := range records {
		out = append(out, adminSeriesFromAggValue(defaultString(rec.Key, "unknown"), aggFromTokenScalarAggRecord(rec), rec.BaselineCostUSD, baseline.BaselineID != ""))
	}
	return out
}

func adminSeriesFromAggValue(key string, a agg, baselineCost float64, hasBaseline bool) adminReportSeries {
	successes := a.Calls - a.Errors
	if successes < 0 {
		successes = 0
	}
	row := adminReportSeries{
		TimeUTC:             key,
		Requests:            a.Calls,
		Successes:           successes,
		Errors:              a.Errors,
		CostUSD:             a.TotalCostUSD,
		Tokens:              a.TotalTokens,
		LatencyMS:           avg(a.LatencyMS, a.Calls),
		TTFBMS:              avg(a.TTFBMS, a.TTFBCount),
		UpstreamMS:          avg(a.UpstreamMS, a.UpstreamMSCount),
		DownstreamMS:        avg(a.DownstreamMS, a.DownstreamMSCount),
		UpstreamOutputTPS:   avgFloat(a.UpstreamOutputTPS, a.UpstreamOutputTPSCount),
		UpstreamTotalTPS:    avgFloat(a.UpstreamTotalTPS, a.UpstreamTotalTPSCount),
		DownstreamOutputTPS: avgFloat(a.DownstreamOutputTPS, a.DownstreamOutputTPSCount),
		DownstreamTotalTPS:  avgFloat(a.DownstreamTotalTPS, a.DownstreamTotalTPSCount),
		ErrorRatePct:        ratioPct(a.Errors, a.Calls),
		FallbackRatePct:     ratioPct(a.Fallbacks, a.Calls),
		CacheHits:           a.CacheHits,
		CacheMisses:         a.CacheMisses,
		CacheBypass:         a.CacheBypass,
		CacheHitRatePct:     ratioPct(a.CacheHits, a.CacheHits+a.CacheMisses),
		Fallbacks:           a.Fallbacks,
	}
	if hasBaseline {
		row.BaselineCostUSD = baselineCost
		row.SavingsUSD = baselineCost - a.TotalCostUSD
		row.SavingsPct = ratioPctFloat(row.SavingsUSD, baselineCost)
	}
	return row
}

func adminChartsFromSeries(filters adminReportFilters, generatedAt string, series []adminReportSeries, providers []adminReportTableRow, hasBaseline bool) []adminReportChart {
	charts := []adminReportChart{
		adminTimeChart(filters, generatedAt, "requests", "Requests", "Requests", "count", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Requests", "count", "magenta", series, func(row adminReportSeries) float64 { return float64(row.Requests) }),
			adminChartSeriesFromTimeRows("Successes", "count", "success", series, func(row adminReportSeries) float64 { return float64(row.Successes) }),
			adminChartSeriesFromTimeRows("Errors", "count", "red", series, func(row adminReportSeries) float64 { return float64(row.Errors) }),
		}),
		adminTimeChart(filters, generatedAt, "cost", "Cost", "USD", "usd", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Cost", "usd", "red", series, func(row adminReportSeries) float64 { return row.CostUSD }),
		}),
		adminTimeChart(filters, generatedAt, "latency", "Latency And Duration", "Milliseconds", "ms", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Latency", "ms", "violet", series, func(row adminReportSeries) float64 { return float64(row.LatencyMS) }),
			adminChartSeriesFromTimeRows("TTFB", "ms", "blue", series, func(row adminReportSeries) float64 { return float64(row.TTFBMS) }),
			adminChartSeriesFromTimeRows("Upstream duration", "ms", "magenta", series, func(row adminReportSeries) float64 { return float64(row.UpstreamMS) }),
			adminChartSeriesFromTimeRows("Downstream duration", "ms", "text", series, func(row adminReportSeries) float64 { return float64(row.DownstreamMS) }),
		}),
		adminTimeChart(filters, generatedAt, "throughput", "Throughput", "Tokens per second", "tokens_per_sec", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Upstream output", "tokens_per_sec", "blue", series, func(row adminReportSeries) float64 { return row.UpstreamOutputTPS }),
			adminChartSeriesFromTimeRows("Upstream total", "tokens_per_sec", "magenta", series, func(row adminReportSeries) float64 { return row.UpstreamTotalTPS }),
			adminChartSeriesFromTimeRows("Downstream output", "tokens_per_sec", "success", series, func(row adminReportSeries) float64 { return row.DownstreamOutputTPS }),
			adminChartSeriesFromTimeRows("Downstream total", "tokens_per_sec", "warning", series, func(row adminReportSeries) float64 { return row.DownstreamTotalTPS }),
		}),
		adminTimeChart(filters, generatedAt, "cache", "Cache", "Requests", "count", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Hits", "count", "success", series, func(row adminReportSeries) float64 { return float64(row.CacheHits) }),
			adminChartSeriesFromTimeRows("Misses", "count", "warning", series, func(row adminReportSeries) float64 { return float64(row.CacheMisses) }),
			adminChartSeriesFromTimeRows("Bypass", "count", "text", series, func(row adminReportSeries) float64 { return float64(row.CacheBypass) }),
		}),
		adminTimeChart(filters, generatedAt, "cache_hit_rate", "Cache Hit Rate", "Percent", "percent", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Hit rate", "percent", "success", series, func(row adminReportSeries) float64 { return row.CacheHitRatePct }),
		}),
		adminCategoryChart(filters, generatedAt, "provider_tokens", "Provider token volume", "Provider / model", "Token count", "tokens", []adminReportChartSeries{
			adminChartSeriesFromTableRows("Input Tokens", "tokens", "blue", providers, func(row adminReportTableRow) float64 { return float64(row.InputTokens) }),
			adminChartSeriesFromTableRows("Output Tokens", "tokens", "magenta", providers, func(row adminReportTableRow) float64 { return float64(row.OutputTokens) }),
			adminChartSeriesFromTableRows("Total Tokens", "tokens", "purple", providers, func(row adminReportTableRow) float64 { return float64(row.TotalTokens) }),
		}),
		adminTimeChart(filters, generatedAt, "errors_fallbacks", "Errors And Fallbacks", "Requests", "count", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Errors", "count", "red", series, func(row adminReportSeries) float64 { return float64(row.Errors) }),
			adminChartSeriesFromTimeRows("Fallbacks", "count", "warning", series, func(row adminReportSeries) float64 { return float64(row.Fallbacks) }),
		}),
		adminTimeChart(filters, generatedAt, "error_fallback_rates", "Error And Fallback Rates", "Percent", "percent", []adminReportChartSeries{
			adminChartSeriesFromTimeRows("Error rate", "percent", "red", series, func(row adminReportSeries) float64 { return row.ErrorRatePct }),
			adminChartSeriesFromTimeRows("Fallback rate", "percent", "warning", series, func(row adminReportSeries) float64 { return row.FallbackRatePct }),
		}),
	}
	if hasBaseline {
		charts = append(charts,
			adminTimeChart(filters, generatedAt, "cost_baseline_savings", "Actual Cost, Baseline, And Savings", "USD", "usd", []adminReportChartSeries{
				adminChartSeriesFromTimeRows("Actual cost", "usd", "red", series, func(row adminReportSeries) float64 { return row.CostUSD }),
				adminChartSeriesFromTimeRows("Baseline cost", "usd", "blue", series, func(row adminReportSeries) float64 { return row.BaselineCostUSD }),
				adminChartSeriesFromTimeRows("Savings", "usd", "magenta", series, func(row adminReportSeries) float64 { return row.SavingsUSD }),
			}),
			adminTimeChart(filters, generatedAt, "savings_pct", "Savings Rate", "Percent", "percent", []adminReportChartSeries{
				adminChartSeriesFromTimeRows("Savings rate", "percent", "purple", series, func(row adminReportSeries) float64 { return row.SavingsPct }),
			}),
		)
	}
	return charts
}

func adminTimeChart(filters adminReportFilters, generatedAt, id, title, yLabel, yUnit string, chartSeries []adminReportChartSeries) adminReportChart {
	return adminReportChart{
		ChartID:     id,
		Title:       title,
		XAxis:       adminReportChartAxis{Label: "Time", Type: "time", Unit: "UTC hour"},
		YAxis:       adminReportChartAxis{Label: yLabel, Type: "linear", Unit: yUnit},
		Series:      chartSeries,
		GeneratedAt: generatedAt,
		From:        formatUsageTime(filters.From),
		To:          formatUsageTime(filters.To),
		Filters:     adminFilterDTO(filters),
	}
}

func adminCategoryChart(filters adminReportFilters, generatedAt, id, title, xLabel, yLabel, yUnit string, chartSeries []adminReportChartSeries) adminReportChart {
	return adminReportChart{
		ChartID:     id,
		Title:       title,
		XAxis:       adminReportChartAxis{Label: xLabel, Type: "category"},
		YAxis:       adminReportChartAxis{Label: yLabel, Type: "linear", Unit: yUnit},
		Series:      chartSeries,
		GeneratedAt: generatedAt,
		From:        formatUsageTime(filters.From),
		To:          formatUsageTime(filters.To),
		Filters:     adminFilterDTO(filters),
	}
}

func adminChartSeriesFromTimeRows(name, unit, colorKey string, rows []adminReportSeries, value func(adminReportSeries) float64) adminReportChartSeries {
	points := make([]adminReportChartPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, adminReportChartPoint{X: row.TimeUTC, XUnixMs: adminReportTimeUnixMs(row.TimeUTC), Y: value(row)})
	}
	return adminReportChartSeries{Name: name, Unit: unit, ColorKey: colorKey, Points: points}
}

func adminReportTimeUnixMs(value string) *int64 {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	ms := t.UTC().UnixMilli()
	return &ms
}

func adminChartSeriesFromTableRows(name, unit, colorKey string, rows []adminReportTableRow, value func(adminReportTableRow) float64) adminReportChartSeries {
	points := make([]adminReportChartPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, adminReportChartPoint{X: row.Key, Y: value(row)})
	}
	return adminReportChartSeries{Name: name, Unit: unit, ColorKey: colorKey, Points: points}
}

func adminFilterDTO(filters adminReportFilters) adminReportFilterDTO {
	return adminReportFilterDTO{
		CallerID:          filters.CallerID,
		CallerIP:          filters.CallerIP,
		TokenID:           filters.TokenID,
		TokenIDPrefix:     filters.TokenIDPrefix,
		CallerUser:        filters.CallerUser,
		CallerProject:     filters.CallerProject,
		CallerEnvironment: filters.CallerEnvironment,
		RequestedModel:    filters.RequestedModel,
		ResolvedGroup:     filters.ResolvedGroup,
		TargetProvider:    filters.TargetProvider,
		TargetModel:       filters.TargetModel,
		TargetDialect:     filters.TargetDialect,
		Status:            filters.Status,
		Cache:             filters.Cache,
		Client:            filters.Client,
		ErrorClass:        filters.ErrorClass,
	}
}

func adminRequestFromRow(row usageRow) adminReportRequest {
	return adminReportRequest{
		TimeUTC:                         formatUsageTime(row.TS),
		RequestID:                       row.RequestID,
		CallerID:                        row.CallerID,
		CallerUser:                      row.CallerUser,
		Project:                         row.CallerProject,
		Environment:                     row.CallerEnvironment,
		TokenID:                         row.TokenID,
		CallerIP:                        row.CallerIP,
		Client:                          row.Client,
		RequestedModel:                  row.RequestedModel,
		ModelGroup:                      defaultString(row.ResolvedGroup, row.RequestedModel),
		Provider:                        row.TargetProvider,
		Model:                           row.TargetModel,
		Dialect:                         row.TargetDialect,
		Status:                          row.Status,
		Error:                           sanitizePersistedDiagnosticText(row.Error),
		Cache:                           row.Cache,
		Attempts:                        row.Attempts,
		Fallback:                        row.FallbackUsed,
		LatencyMS:                       row.LatencyMS,
		TTFBMS:                          row.TTFBMS,
		UpstreamMS:                      row.UpstreamMS,
		DownstreamMS:                    row.DownstreamMS,
		Tokens:                          totalTokens(Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.TotalTokens}),
		TotalTokens:                     totalTokens(Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.TotalTokens}),
		InputTokens:                     row.InputTokens,
		OutputTokens:                    row.OutputTokens,
		ReasoningTokens:                 row.ReasoningTokens,
		ReasoningAttemptCount:           row.ReasoningAttemptCount,
		ReasoningSuccessfulAttemptCount: row.ReasoningSuccessfulAttemptCount,
		ReasoningReportedAttemptCount:   row.ReasoningReportedAttemptCount,
		CostUSD:                         row.TotalCostUSD,
		TotalCostUSD:                    row.TotalCostUSD,
	}
}

func adminAttemptsFromRecords(records []requestAttemptRecord) []adminReportAttempt {
	out := make([]adminReportAttempt, 0, len(records))
	for _, record := range records {
		out = append(out, adminReportAttempt{
			RequestID:        record.RequestID,
			AttemptIndex:     record.AttemptIndex,
			TimeUTC:          record.TS,
			Provider:         record.Provider,
			Model:            record.Model,
			Dialect:          record.Dialect,
			EndpointHost:     record.EndpointHost,
			DurationMS:       record.DurationMS,
			StatusCode:       record.StatusCode,
			ErrorClass:       record.ErrorClass,
			ErrorMessage:     sanitizePersistedDiagnosticText(record.ErrorMessage),
			Retryable:        record.Retryable,
			TimedOut:         record.TimedOut,
			ClientCanceled:   record.ClientCanceled,
			Selected:         record.Selected,
			FallbackReason:   record.FallbackReason,
			RequestBytes:     record.RequestBytes,
			ResponseBytes:    record.ResponseBytes,
			AttemptTimeoutMS: record.AttemptTimeoutMS,
			RetryAfterMS:     record.RetryAfterMS,
			ReasoningTokens:  record.ReasoningTokens,
		})
	}
	return out
}

func adminTraceFromRecords(records []requestTraceEventRecord) []adminReportTraceEvent {
	out := make([]adminReportTraceEvent, 0, len(records))
	for _, record := range records {
		out = append(out, adminReportTraceEvent{
			RequestID:  record.RequestID,
			Seq:        record.Seq,
			TimeUTC:    record.TS,
			Event:      record.Event,
			Message:    sanitizePersistedDiagnosticText(record.Message),
			Provider:   record.Provider,
			Model:      record.Model,
			Dialect:    record.Dialect,
			DurationMS: record.DurationMS,
			StatusCode: record.StatusCode,
			ErrorClass: record.ErrorClass,
			Retryable:  record.Retryable,
			Attempt:    record.Attempt,
		})
	}
	return out
}

func adminErrorsFromRecords(records []requestErrorRecord) []adminReportError {
	out := make([]adminReportError, 0, len(records))
	for _, record := range records {
		out = append(out, adminReportError{
			RequestID:    record.RequestID,
			TimeUTC:      record.TS,
			Status:       record.Status,
			ErrorType:    record.ErrorType,
			ErrorClass:   record.ErrorClass,
			ErrorMessage: sanitizePersistedDiagnosticText(record.ErrorMessage),
			Retryable:    record.Retryable,
			Attempts:     record.Attempts,
			Provider:     record.Provider,
			Model:        record.Model,
			Dialect:      record.Dialect,
		})
	}
	return out
}

func adminUpstreamErrorDetailsFromRecords(records []requestUpstreamErrorDetailRecord) []adminReportUpstreamErrorDetail {
	out := make([]adminReportUpstreamErrorDetail, 0, len(records))
	for _, record := range records {
		out = append(out, adminReportUpstreamErrorDetail{
			RequestID:    record.RequestID,
			AttemptIndex: record.AttemptIndex,
			Seq:          record.Seq,
			TimeUTC:      record.TS,
			Status:       record.StatusCode,
			ErrorClass:   record.ErrorClass,
			FieldName:    safeOptionalReasonToken(record.FieldName),
			FieldValue:   sanitizePersistedUpstreamErrorDetailScalar(record.FieldValue),
			Source:       sanitizePersistedUpstreamErrorDetailScalar(record.Source),
			Truncated:    record.Truncated,
		})
	}
	return out
}
