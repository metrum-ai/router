// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type requestLogger struct {
	mu   sync.Mutex
	path string
	file *os.File
}

type adminReportQueryFailureLogRecord struct {
	TS             string `json:"ts"`
	EventType      string `json:"event_type"`
	Report         string `json:"report"`
	Handler        string `json:"handler"`
	DBDriver       string `json:"db_driver,omitempty"`
	AdminRequestID string `json:"admin_request_id,omitempty"`
	From           string `json:"from,omitempty"`
	To             string `json:"to,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Sort           string `json:"sort,omitempty"`
	Direction      string `json:"direction,omitempty"`
	ErrorClass     string `json:"error_class"`
	ErrorMessage   string `json:"error_message"`
	PGCode         string `json:"pg_code,omitempty"`
	PGSeverity     string `json:"pg_severity,omitempty"`
	PGMessage      string `json:"pg_message,omitempty"`
}

type logRecord struct {
	TS                                 string                           `json:"ts"`
	RequestID                          string                           `json:"request_id"`
	CallerID                           string                           `json:"caller_id"`
	CallerUser                         string                           `json:"caller_user"`
	CallerProject                      string                           `json:"caller_project"`
	CallerEnvironment                  string                           `json:"caller_environment"`
	CallerIP                           string                           `json:"caller_ip"`
	TokenID                            string                           `json:"token_id"`
	Client                             string                           `json:"client"`
	InboundDialect                     string                           `json:"inbound_dialect"`
	RequestedModel                     string                           `json:"requested_model"`
	ResolvedGroup                      string                           `json:"resolved_group"`
	Strategy                           string                           `json:"strategy"`
	ClassLabel                         *string                          `json:"class_label"`
	TargetProvider                     string                           `json:"target_provider"`
	TargetModel                        string                           `json:"target_model"`
	TargetDialect                      string                           `json:"target_dialect"`
	TargetRegion                       string                           `json:"target_region,omitempty"`
	StreamMode                         string                           `json:"stream_mode,omitempty"`
	StreamUnknownEvents                int                              `json:"stream_unknown_events,omitempty"`
	Stream                             bool                             `json:"stream"`
	Cache                              string                           `json:"cache"`
	Status                             int                              `json:"status"`
	Attempts                           int                              `json:"attempts"`
	FallbackUsed                       bool                             `json:"fallback_used"`
	LatencyMS                          int64                            `json:"latency_ms"`
	TTFBMS                             *int64                           `json:"ttfb_ms"`
	UpstreamMS                         *int64                           `json:"upstream_duration_ms"`
	DownstreamMS                       *int64                           `json:"downstream_duration_ms"`
	UpstreamOutputTPS                  *float64                         `json:"upstream_output_tokens_per_sec"`
	UpstreamTotalTPS                   *float64                         `json:"upstream_total_tokens_per_sec"`
	DownstreamOutputTPS                *float64                         `json:"downstream_output_tokens_per_sec"`
	DownstreamTotalTPS                 *float64                         `json:"downstream_total_tokens_per_sec"`
	Usage                              Usage                            `json:"usage"`
	ReasoningCoverageMeasured          bool                             `json:"reasoning_coverage_measured,omitempty"`
	ReasoningAttemptCount              int                              `json:"reasoning_attempt_count,omitempty"`
	ReasoningSuccessfulAttemptCount    int                              `json:"reasoning_successful_attempt_count,omitempty"`
	ReasoningReportedAttemptCount      int                              `json:"reasoning_reported_attempt_count,omitempty"`
	ReasoningTokens                    *int                             `json:"reasoning_tokens,omitempty"`
	InputHasImage                      bool                             `json:"input_has_image,omitempty"`
	InputImageCount                    int                              `json:"input_image_count,omitempty"`
	InputImageTokens                   int                              `json:"input_image_tokens,omitempty"`
	PIIFilterApplied                   bool                             `json:"pii_filter_applied,omitempty"`
	PIIFilterMode                      string                           `json:"pii_filter_mode,omitempty"`
	PIIFilterReplacements              int                              `json:"pii_filter_replacements,omitempty"`
	PIIFilterRuleCount                 int                              `json:"pii_filter_rule_count,omitempty"`
	ContractPresent                    bool                             `json:"contract_present,omitempty"`
	ContractBucket                     string                           `json:"contract_bucket,omitempty"`
	ContractFailureReason              string                           `json:"contract_failure_reason,omitempty"`
	ContractWorkload                   string                           `json:"contract_workload,omitempty"`
	TargetValidationStatus             string                           `json:"target_validation_status,omitempty"`
	TargetValidationWorkload           string                           `json:"target_validation_workload,omitempty"`
	TargetValidationAgeBucket          string                           `json:"target_validation_age_bucket,omitempty"`
	InputPricePerMillionUSD            float64                          `json:"input_price_per_million_usd,omitempty"`
	OutputPricePerMillionUSD           float64                          `json:"output_price_per_million_usd,omitempty"`
	CachedInputPricePerMillionUSD      *float64                         `json:"cached_input_price_per_million_usd,omitempty"`
	ImageInputPricePerMillionTokensUSD float64                          `json:"image_input_price_per_million_tokens_usd,omitempty"`
	ImageInputPricePerImageUSD         float64                          `json:"image_input_price_per_image_usd,omitempty"`
	InputCostUSD                       float64                          `json:"input_cost_usd,omitempty"`
	ImageCostUSD                       float64                          `json:"image_cost_usd,omitempty"`
	OutputCostUSD                      float64                          `json:"output_cost_usd,omitempty"`
	TotalCostUSD                       float64                          `json:"total_cost_usd,omitempty"`
	UpstreamReportedInputCostUSD       float64                          `json:"upstream_reported_input_cost_usd,omitempty"`
	UpstreamReportedOutputCostUSD      float64                          `json:"upstream_reported_output_cost_usd,omitempty"`
	UpstreamReportedTotalCostUSD       float64                          `json:"upstream_reported_total_cost_usd,omitempty"`
	PricingSource                      string                           `json:"pricing_source,omitempty"`
	PricingUpdatedAt                   string                           `json:"pricing_updated_at,omitempty"`
	CacheEnabled                       bool                             `json:"cache_enabled"`
	CacheItems                         int64                            `json:"cache_items"`
	CacheBytes                         int64                            `json:"cache_bytes"`
	CacheMaxBytes                      int64                            `json:"cache_max_bytes"`
	CacheOccupancyPct                  float64                          `json:"cache_occupancy_pct"`
	QuotaState                         string                           `json:"quota_state"`
	KeyState                           string                           `json:"key_state"`
	TrafficShapeApplied                bool                             `json:"traffic_shape_applied,omitempty"`
	TrafficShapeDecision               string                           `json:"traffic_shape_decision,omitempty"`
	TrafficShapeScope                  string                           `json:"traffic_shape_scope,omitempty"`
	TrafficShapeBucket                 string                           `json:"traffic_shape_bucket,omitempty"`
	TrafficShapeRetryAfterMS           int64                            `json:"traffic_shape_retry_after_ms,omitempty"`
	TrafficShapeQueueWaitMS            int64                            `json:"traffic_shape_queue_wait_ms,omitempty"`
	TrafficShapeEstimatedInputTokens   int                              `json:"traffic_shape_estimated_input_tokens,omitempty"`
	TrafficShapeReservedOutputTokens   int                              `json:"traffic_shape_reserved_output_tokens,omitempty"`
	TrafficShapeTotalReservedTokens    int                              `json:"traffic_shape_total_reserved_tokens,omitempty"`
	RouterVersion                      string                           `json:"router_version,omitempty"`
	RouterBuildDate                    string                           `json:"router_build_date,omitempty"`
	RoutingConfigFingerprint           string                           `json:"routing_config_fingerprint,omitempty"`
	ModelGroupConfigFingerprint        string                           `json:"model_group_config_fingerprint,omitempty"`
	RoutingPolicyFingerprint           string                           `json:"routing_policy_fingerprint,omitempty"`
	PricingCatalogFingerprint          string                           `json:"pricing_catalog_fingerprint,omitempty"`
	Warnings                           []string                         `json:"warnings"`
	Error                              *string                          `json:"error"`
	ErrorClass                         string                           `json:"error_class,omitempty"`
	ErrorMessage                       string                           `json:"error_message,omitempty"`
	AttemptsDetail                     []attemptLogRecord               `json:"attempts_detail,omitempty"`
	TraceEvents                        []traceLogRecord                 `json:"trace_events,omitempty"`
	RequestShape                       *requestShapeLogRecord           `json:"request_shape,omitempty"`
	TokenEstimate                      *requestTokenEstimateLogRecord   `json:"request_token_estimate,omitempty"`
	TranslationShapes                  []translationShapeLogRecord      `json:"translation_shapes,omitempty"`
	TranslationFieldEvents             []translationFieldEventLogRecord `json:"translation_field_events,omitempty"`
	DecisionShapeFeatures              []decisionShapeFeatureLogRecord  `json:"decision_shape_features,omitempty"`
	DecisionCandidates                 []decisionCandidateLogRecord     `json:"decision_candidates,omitempty"`
	DecisionFilterReasons              []decisionFilterReasonLogRecord  `json:"decision_filter_reasons,omitempty"`
	RoutingDecisions                   []routingDecisionLogRecord       `json:"routing_decisions,omitempty"`
	RoutingSignals                     []routingSignalLogRecord         `json:"routing_signals,omitempty"`
	DynamicScoreTerms                  []dynamicScoreTermLogRecord      `json:"dynamic_score_terms,omitempty"`
	PolicyExecutions                   []policyExecutionLogRecord       `json:"policy_executions,omitempty"`
	FallbackTransitions                []fallbackTransitionLogRecord    `json:"fallback_transitions,omitempty"`
	CacheReasons                       []cacheReasonLogRecord           `json:"cache_reasons,omitempty"`
	TrafficShapeEvents                 []trafficShapeEventLogRecord     `json:"traffic_shape_events,omitempty"`
	UpstreamShapeEvents                []upstreamShapeEventLogRecord    `json:"upstream_shape_events,omitempty"`
}

type attemptLogRecord struct {
	Index            int                            `json:"index"`
	TS               string                         `json:"ts"`
	Provider         string                         `json:"provider"`
	Model            string                         `json:"model"`
	Dialect          string                         `json:"dialect"`
	EndpointHost     string                         `json:"endpoint_host,omitempty"`
	DurationMS       int64                          `json:"duration_ms"`
	StatusCode       int                            `json:"status_code,omitempty"`
	ErrorClass       string                         `json:"error_class,omitempty"`
	ErrorMessage     string                         `json:"error_message,omitempty"`
	Retryable        bool                           `json:"retryable,omitempty"`
	TimedOut         bool                           `json:"timed_out,omitempty"`
	ClientCanceled   bool                           `json:"client_canceled,omitempty"`
	Selected         bool                           `json:"selected,omitempty"`
	FallbackReason   string                         `json:"fallback_reason,omitempty"`
	RequestBytes     int64                          `json:"request_bytes,omitempty"`
	ResponseBytes    int64                          `json:"response_bytes,omitempty"`
	AttemptTimeoutMS int                            `json:"attempt_timeout_ms,omitempty"`
	RetryAfterMS     int64                          `json:"retry_after_ms,omitempty"`
	ReasoningTokens  *int                           `json:"reasoning_tokens,omitempty"`
	ErrorDetails     []upstreamErrorDetailLogRecord `json:"error_details,omitempty"`
}

type upstreamErrorDetailLogRecord struct {
	Seq          int    `json:"seq"`
	TS           string `json:"ts"`
	AttemptIndex int    `json:"attempt_index"`
	StatusCode   int    `json:"status_code"`
	ErrorClass   string `json:"error_class"`
	FieldName    string `json:"field_name"`
	FieldValue   string `json:"field_value"`
	Source       string `json:"source"`
	Truncated    bool   `json:"truncated,omitempty"`
}

type traceLogRecord struct {
	Seq        int    `json:"seq"`
	TS         string `json:"ts"`
	Event      string `json:"event"`
	Message    string `json:"message,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Dialect    string `json:"dialect,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
}

type trafficShapeEventLogRecord struct {
	Seq                  int    `json:"seq"`
	Scope                string `json:"scope"`
	Bucket               string `json:"bucket"`
	Decision             string `json:"decision"`
	Cost                 int    `json:"cost"`
	RetryAfterMS         int64  `json:"retry_after_ms,omitempty"`
	QueueWaitMS          int64  `json:"queue_wait_ms,omitempty"`
	EstimatedInputTokens int    `json:"estimated_input_tokens,omitempty"`
	ReservedOutputTokens int    `json:"reserved_output_tokens,omitempty"`
	TotalReservedTokens  int    `json:"total_reserved_tokens,omitempty"`
}

type upstreamShapeEventLogRecord struct {
	Seq                  int    `json:"seq"`
	TS                   string `json:"ts"`
	Scope                string `json:"scope"`
	Provider             string `json:"provider"`
	ModelRef             string `json:"model_ref,omitempty"`
	Model                string `json:"model"`
	Dialect              string `json:"dialect"`
	Bucket               string `json:"bucket"`
	Decision             string `json:"decision"`
	RetryAfterMS         int64  `json:"retry_after_ms,omitempty"`
	EstimatedInputTokens int    `json:"estimated_input_tokens,omitempty"`
	ReservedOutputTokens int    `json:"reserved_output_tokens,omitempty"`
	TotalReservedTokens  int    `json:"total_reserved_tokens,omitempty"`
	BackoffReason        string `json:"backoff_reason,omitempty"`
	QueueWaitMS          int64  `json:"queue_wait_ms,omitempty"`
}

type requestShapeLogRecord struct {
	TS                         string `json:"ts"`
	InboundDialect             string `json:"inbound_dialect"`
	RequestedModel             string `json:"requested_model"`
	ResolvedGroup              string `json:"resolved_group,omitempty"`
	Client                     string `json:"client,omitempty"`
	Stream                     bool   `json:"stream"`
	InputItemCount             int    `json:"input_item_count,omitempty"`
	MessageCount               int    `json:"message_count,omitempty"`
	SystemMessageCount         int    `json:"system_message_count,omitempty"`
	DeveloperMessageCount      int    `json:"developer_message_count,omitempty"`
	UserMessageCount           int    `json:"user_message_count,omitempty"`
	AssistantMessageCount      int    `json:"assistant_message_count,omitempty"`
	ToolResultCount            int    `json:"tool_result_count,omitempty"`
	FunctionCallOutputCount    int    `json:"function_call_output_count,omitempty"`
	ToolCount                  int    `json:"tool_count,omitempty"`
	ToolChoiceMode             string `json:"tool_choice_mode,omitempty"`
	ParallelToolCallsPresent   bool   `json:"parallel_tool_calls_present,omitempty"`
	ResponseFormatPresent      bool   `json:"response_format_present,omitempty"`
	StructuredOutputPresent    bool   `json:"structured_output_present,omitempty"`
	ReasoningPresent           bool   `json:"reasoning_present,omitempty"`
	ReasoningEffortBucket      string `json:"reasoning_effort_bucket,omitempty"`
	ReasoningBudgetBucket      string `json:"reasoning_budget_bucket,omitempty"`
	IncludePresent             bool   `json:"include_present,omitempty"`
	TruncationPresent          bool   `json:"truncation_present,omitempty"`
	MetadataPresent            bool   `json:"metadata_present,omitempty"`
	StorePresent               bool   `json:"store_present,omitempty"`
	PreviousResponseIDPresent  bool   `json:"previous_response_id_present,omitempty"`
	ImageCount                 int    `json:"image_count,omitempty"`
	AudioPresent               bool   `json:"audio_present,omitempty"`
	VideoPresent               bool   `json:"video_present,omitempty"`
	InputTextBytesBucket       string `json:"input_text_bytes_bucket,omitempty"`
	ToolSchemaBytesBucket      string `json:"tool_schema_bytes_bucket,omitempty"`
	TotalRequestBytesBucket    string `json:"total_request_bytes_bucket,omitempty"`
	EstimatedInputTokensBucket string `json:"estimated_input_tokens_bucket,omitempty"`
	RequestedOutputCapField    string `json:"requested_output_cap_field,omitempty"`
	RequestedOutputCapBucket   string `json:"requested_output_cap_bucket,omitempty"`
	ToolSchemaFingerprint      string `json:"tool_schema_fingerprint,omitempty"`
	RequestShapeFingerprint    string `json:"request_shape_fingerprint,omitempty"`
}

type requestTokenEstimateLogRecord struct {
	TS                            string `json:"ts"`
	InboundDialect                string `json:"inbound_dialect"`
	RequestedModel                string `json:"requested_model"`
	ResolvedGroup                 string `json:"resolved_group,omitempty"`
	EstimateMethod                string `json:"estimate_method"`
	EstimateVersion               string `json:"estimate_version"`
	EstimatedInputTokens          int    `json:"estimated_input_tokens,omitempty"`
	EstimatedToolSchemaTokens     int    `json:"estimated_tool_schema_tokens,omitempty"`
	EstimatedImageTokens          int    `json:"estimated_image_tokens,omitempty"`
	EstimatedAudioTokens          int    `json:"estimated_audio_tokens,omitempty"`
	EstimatedTotalInputTokens     int    `json:"estimated_total_input_tokens,omitempty"`
	RequestedOutputCapTokens      int    `json:"requested_output_cap_tokens,omitempty"`
	RequestedOutputCapField       string `json:"requested_output_cap_field,omitempty"`
	RouterDefaultOutputCapApplied bool   `json:"router_default_output_cap_applied,omitempty"`
	TotalReservedTokens           int    `json:"total_reserved_tokens,omitempty"`
	RequestBytes                  int    `json:"request_bytes,omitempty"`
	TranslatedRequestBytes        int    `json:"translated_request_bytes,omitempty"`
	EstimateWarningCount          int    `json:"estimate_warning_count,omitempty"`
	EstimateConfidenceBucket      string `json:"estimate_confidence_bucket,omitempty"`
}

type translationShapeLogRecord struct {
	TS                           string `json:"ts"`
	AttemptIndex                 int    `json:"attempt_index"`
	Provider                     string `json:"provider"`
	Model                        string `json:"model"`
	Dialect                      string `json:"dialect"`
	BridgeDirection              string `json:"bridge_direction,omitempty"`
	EndpointPath                 string `json:"endpoint_path,omitempty"`
	TranslatedStream             bool   `json:"translated_stream"`
	TranslatedToolCount          int    `json:"translated_tool_count,omitempty"`
	TranslatedToolChoiceMode     string `json:"translated_tool_choice_mode,omitempty"`
	TranslatedOutputCapField     string `json:"translated_output_cap_field,omitempty"`
	TranslatedOutputCapBucket    string `json:"translated_output_cap_bucket,omitempty"`
	TranslatedReasoningControl   string `json:"translated_reasoning_control,omitempty"`
	TranslatedRequestBytesBucket string `json:"translated_request_bytes_bucket,omitempty"`
	FieldsStrippedCount          int    `json:"fields_stripped_count,omitempty"`
	FieldsRewrittenCount         int    `json:"fields_rewritten_count,omitempty"`
	UnsupportedFieldsPresent     bool   `json:"unsupported_fields_present,omitempty"`
	TranslationWarningCount      int    `json:"translation_warning_count,omitempty"`
	RequestShapeFingerprint      string `json:"request_shape_fingerprint,omitempty"`
	ToolSchemaFingerprint        string `json:"tool_schema_fingerprint,omitempty"`
}

type translationFieldEventLogRecord struct {
	AttemptIndex int    `json:"attempt_index"`
	Seq          int    `json:"seq"`
	FieldName    string `json:"field_name"`
	Action       string `json:"action"`
	Reason       string `json:"reason,omitempty"`
}

type decisionShapeFeatureLogRecord struct {
	Seq       int    `json:"seq"`
	Name      string `json:"name"`
	BoolValue bool   `json:"bool_value,omitempty"`
	IntValue  int    `json:"int_value,omitempty"`
	TextValue string `json:"text_value,omitempty"`
}

type decisionCandidateLogRecord struct {
	CandidateIndex              int    `json:"candidate_index"`
	GroupTargetIndex            int    `json:"group_target_index"`
	Provider                    string `json:"provider"`
	Model                       string `json:"model"`
	ModelRef                    string `json:"model_ref,omitempty"`
	Dialect                     string `json:"dialect"`
	Weight                      int    `json:"weight,omitempty"`
	ToolOnly                    bool   `json:"tool_only,omitempty"`
	ContextTokens               int    `json:"context_tokens,omitempty"`
	MaxEstimatedInputTokens     int    `json:"max_estimated_input_tokens,omitempty"`
	MaxRequestedOutputTokens    int    `json:"max_requested_output_tokens,omitempty"`
	MaxRequestBytes             int    `json:"max_request_bytes,omitempty"`
	MaxToolSchemaBytes          int    `json:"max_tool_schema_bytes,omitempty"`
	EstimatedTotalInputTokens   int    `json:"estimated_total_input_tokens,omitempty"`
	RequestedOutputCapTokens    int    `json:"requested_output_cap_tokens,omitempty"`
	EstimatedTotalWithOutputCap int    `json:"estimated_total_with_output_cap,omitempty"`
	RequestBytes                int    `json:"request_bytes,omitempty"`
	ToolSchemaBytes             int    `json:"tool_schema_bytes,omitempty"`
	ContextHeadroomTokens       int    `json:"context_headroom_tokens,omitempty"`
	ContextFit                  bool   `json:"context_fit,omitempty"`
	RequestBytesFit             bool   `json:"request_bytes_fit,omitempty"`
	ToolSchemaFit               bool   `json:"tool_schema_fit,omitempty"`
	EligibilityDecision         string `json:"eligibility_decision,omitempty"`
	EligibilityReason           string `json:"eligibility_reason,omitempty"`
	InputImage                  bool   `json:"input_image,omitempty"`
	OutputImage                 bool   `json:"output_image,omitempty"`
	ToolSupport                 bool   `json:"tool_support,omitempty"`
	ForcedToolChoice            bool   `json:"forced_tool_choice,omitempty"`
	StructuredOutput            bool   `json:"structured_output,omitempty"`
	HonorsMaxTokens             bool   `json:"honors_max_tokens,omitempty"`
	ReasoningSupport            bool   `json:"reasoning_support,omitempty"`
	ReasoningMode               string `json:"reasoning_mode,omitempty"`
	ReasoningControl            string `json:"reasoning_control,omitempty"`
	ReasoningDefault            bool   `json:"reasoning_default,omitempty"`
	ReasoningStream             string `json:"reasoning_stream_block,omitempty"`
	ValidationStatus            string `json:"validation_status,omitempty"`
	ValidationAge               string `json:"validation_age_bucket,omitempty"`
	Eligible                    bool   `json:"eligible"`
	Selected                    bool   `json:"selected,omitempty"`
}

type decisionFilterReasonLogRecord struct {
	Seq            int    `json:"seq"`
	CandidateIndex int    `json:"candidate_index"`
	Stage          string `json:"stage"`
	Reason         string `json:"reason"`
}

type routingDecisionLogRecord struct {
	Seq                    int     `json:"seq"`
	Strategy               string  `json:"strategy"`
	SelectedCandidateIndex int     `json:"selected_candidate_index"`
	Provider               string  `json:"provider"`
	Model                  string  `json:"model"`
	Dialect                string  `json:"dialect"`
	FallbackCount          int     `json:"fallback_count"`
	ClassLabel             *string `json:"class_label,omitempty"`
}

type routingSignalLogRecord struct {
	Seq            int     `json:"seq"`
	Strategy       string  `json:"strategy"`
	SignalName     string  `json:"signal_name"`
	Source         string  `json:"source,omitempty"`
	CandidateIndex int     `json:"candidate_index,omitempty"`
	BoolValue      bool    `json:"bool_value,omitempty"`
	IntValue       int     `json:"int_value,omitempty"`
	FloatValue     float64 `json:"float_value,omitempty"`
	TextValue      string  `json:"text_value,omitempty"`
}

type dynamicScoreTermLogRecord struct {
	Seq                int     `json:"seq"`
	CandidateIndex     int     `json:"candidate_index"`
	Rank               int     `json:"rank"`
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	Dialect            string  `json:"dialect"`
	TermName           string  `json:"term_name"`
	ScoreName          string  `json:"score_name,omitempty"`
	Weight             float64 `json:"weight,omitempty"`
	Value              float64 `json:"value,omitempty"`
	Contribution       float64 `json:"contribution,omitempty"`
	FinalScore         float64 `json:"final_score,omitempty"`
	ValueBucket        string  `json:"value_bucket,omitempty"`
	ContributionBucket string  `json:"contribution_bucket,omitempty"`
	FinalScoreBucket   string  `json:"final_score_bucket,omitempty"`
	ObservationCount   int     `json:"observation_count,omitempty"`
	Selected           bool    `json:"selected,omitempty"`
}

type policyExecutionLogRecord struct {
	Seq                    int     `json:"seq"`
	Strategy               string  `json:"strategy"`
	PolicyKind             string  `json:"policy_kind"`
	Outcome                string  `json:"outcome"`
	DurationMS             int64   `json:"duration_ms"`
	EligibleTargetCount    int     `json:"eligible_target_count"`
	AllTargetCount         int     `json:"all_target_count,omitempty"`
	SelectedCandidateIndex int     `json:"selected_candidate_index"`
	FallbackCount          int     `json:"fallback_count"`
	ClassLabel             *string `json:"class_label,omitempty"`
	ErrorClass             string  `json:"error_class,omitempty"`
	ErrorMessage           string  `json:"error_message,omitempty"`
	TerminalErrorType      string  `json:"terminal_error_type,omitempty"`
}

type fallbackTransitionLogRecord struct {
	Seq                    int    `json:"seq"`
	AttemptIndex           int    `json:"attempt_index"`
	FailedCandidateIndex   int    `json:"failed_candidate_index"`
	FallbackCandidateIndex int    `json:"fallback_candidate_index"`
	FailedProvider         string `json:"failed_provider"`
	FailedModel            string `json:"failed_model"`
	FailedDialect          string `json:"failed_dialect"`
	FallbackProvider       string `json:"fallback_provider"`
	FallbackModel          string `json:"fallback_model"`
	FallbackDialect        string `json:"fallback_dialect"`
	FallbackReason         string `json:"fallback_reason"`
	ErrorClass             string `json:"error_class"`
	Retryable              bool   `json:"retryable"`
	FallbackSucceeded      bool   `json:"fallback_succeeded"`
}

type cacheReasonLogRecord struct {
	Seq            int    `json:"seq"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	CandidateIndex int    `json:"candidate_index"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Dialect        string `json:"dialect,omitempty"`
}

func newRequestLogger(path string) (*requestLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &requestLogger{path: path, file: f}, nil
}

func (l *requestLogger) Emit(rec logRecord) {
	if l == nil {
		return
	}
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(raw, '\n'))
}

func (l *requestLogger) EmitAdminReportQueryFailure(rec adminReportQueryFailureLogRecord) {
	if l == nil {
		return
	}
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if rec.EventType == "" {
		rec.EventType = "admin_report_query_failed"
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(raw, '\n'))
}

func (l *requestLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
