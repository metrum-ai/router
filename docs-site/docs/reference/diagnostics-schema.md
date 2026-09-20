---
title: Diagnostics Schema
doc_type: reference
---

# Diagnostics Schema

This reference documents the relational diagnostics, usage, reporting, retention, security-access, and governed content-capture tables used by Metrum AI Router. It is derived from the current router diagnostics schema definitions and is intended for operators who troubleshoot by `X-Request-Id`, build reports, or review what data is safe to persist.

The ordinary diagnostics schema stores scalar metadata only. It is designed for request tracing, usage accounting, latency and fallback triage, provider/model health, quota and traffic-shaping analysis, and report generation without storing raw request content.

## Generated Schema

<!-- @generated diagnostics-schema -->
This section is generated from the router usage and content-capture schema used by the running product.

| Table | Schema type | Retention class | Foreign keys | Indexes |
|---|---|---|---|---|
| `request_usage` | `usageRecord` | `usage_detail` | none | `idx_request_usage_caller_ip`, `idx_request_usage_contract_bucket`, `idx_request_usage_contract_failure`, `idx_request_usage_contract_workload`, `idx_request_usage_contract`, `idx_request_usage_group_fp`, `idx_request_usage_group`, `idx_request_usage_input_image`, `idx_request_usage_pii_filter`, `idx_request_usage_policy_fp`, `idx_request_usage_pricing_fp`, `idx_request_usage_provider_model`, `idx_request_usage_router_version`, `idx_request_usage_routing_fp`, `idx_request_usage_token`, `idx_request_usage_ts`, `idx_request_usage_validation_age`, `idx_request_usage_validation_status`, `idx_request_usage_validation_workload`, primary key |
| `request_attempts` | `requestAttemptRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_attempt_cancel`, `idx_request_attempt_error`, `idx_request_attempt_provider_model`, `idx_request_attempt_request`, `idx_request_attempt_status`, `idx_request_attempt_timeout`, `idx_request_attempt_ts`, primary key |
| `request_trace_events` | `requestTraceEventRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_trace_error`, `idx_request_trace_event`, `idx_request_trace_request`, `idx_request_trace_ts`, primary key |
| `request_errors` | `requestErrorRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_error_class`, `idx_request_error_status`, `idx_request_error_ts`, `idx_request_error_type`, primary key |
| `request_upstream_error_details` | `requestUpstreamErrorDetailRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_upstream_error_detail_attempt`, `idx_upstream_error_detail_class`, `idx_upstream_error_detail_field`, `idx_upstream_error_detail_request`, `idx_upstream_error_detail_status`, `idx_upstream_error_detail_ts`, primary key |
| `request_traffic_shape_events` | `requestTrafficShapeEventRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | primary key |
| `request_upstream_shape_events` | `requestUpstreamShapeEventRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_upstream_shape_request`, `idx_request_upstream_shape_ts`, primary key |
| `request_shapes` | `requestShapeRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_shape_bytes_bucket`, `idx_request_shape_client`, `idx_request_shape_fp`, `idx_request_shape_group`, `idx_request_shape_image_count`, `idx_request_shape_inbound`, `idx_request_shape_input_token_bucket`, `idx_request_shape_output_cap_bucket`, `idx_request_shape_reasoning`, `idx_request_shape_request`, `idx_request_shape_requested_model`, `idx_request_shape_stream`, `idx_request_shape_structured`, `idx_request_shape_tool_choice`, `idx_request_shape_tool_count`, `idx_request_shape_tool_schema_bucket`, `idx_request_shape_tool_schema_fp`, `idx_request_shape_ts`, primary key |
| `request_translation_shapes` | `requestTranslationShapeRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_translation_shape_bridge`, `idx_request_translation_shape_bytes`, `idx_request_translation_shape_dialect`, `idx_request_translation_shape_output_cap`, `idx_request_translation_shape_provider_model`, `idx_request_translation_shape_reasoning`, `idx_request_translation_shape_request_fp`, `idx_request_translation_shape_request`, `idx_request_translation_shape_stream`, `idx_request_translation_shape_tool_choice`, `idx_request_translation_shape_tool_count`, `idx_request_translation_shape_tool_fp`, `idx_request_translation_shape_ts`, `idx_request_translation_shape_unsupported`, primary key |
| `request_translation_field_events` | `requestTranslationFieldEventRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_translation_field_action`, `idx_request_translation_field_attempt`, `idx_request_translation_field_name`, `idx_request_translation_field_request`, primary key |
| `legal_hold_audit_events` | `legalHoldAuditEventRecord` | `retention_control` | `hold_id` joins `legal_holds.hold_id`; `request_id` joins `request_usage.request_id` when a usage row exists | `idx_legal_hold_audit_event`, `idx_legal_hold_audit_hold`, `idx_legal_hold_audit_ts`, primary key |
| `legal_holds` | `legalHoldRecord` | `retention_control` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_legal_hold_active`, `idx_legal_hold_class_range`, `idx_legal_hold_created`, `idx_legal_hold_id`, `idx_legal_hold_request`, primary key |
| `request_cache_reasons` | `decisionCacheReasonRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_decision_cache_reason`, `idx_decision_cache_request`, `idx_decision_cache_status`, primary key |
| `request_content_audit_events` | `contentCaptureAuditRecord` | `content_capture` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_content_audit_action`, `idx_request_content_audit_request`, `idx_request_content_audit_ts`, primary key |
| `request_content_captures` | `contentCaptureRecord` | `content_capture` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_content_group`, `idx_request_content_request`, `idx_request_content_retention`, `idx_request_content_scope`, `idx_request_content_ts`, primary key |
| `request_content_headers` | `contentCaptureHeaderRecord` | `content_capture` | `capture_id` joins `request_content_captures.id`; `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_content_header_capture`, `idx_request_content_header_request`, primary key |
| `request_decision_shape_features` | `decisionShapeFeatureRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_decision_shape_feature`, `idx_decision_shape_request`, primary key |
| `request_dynamic_score_terms` | `dynamicScoreTermRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_dynamic_score_contribution_bucket`, `idx_dynamic_score_final_bucket`, `idx_dynamic_score_score_name`, `idx_dynamic_score_term_candidate`, `idx_dynamic_score_term_name`, `idx_dynamic_score_term_provider_model`, `idx_dynamic_score_term_rank`, `idx_dynamic_score_term_request`, `idx_dynamic_score_term_selected`, `idx_dynamic_score_value_bucket`, primary key |
| `request_fallback_transitions` | `fallbackTransitionRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_fallback_transition_attempt`, `idx_fallback_transition_error_class`, `idx_fallback_transition_failed_candidate`, `idx_fallback_transition_failed`, `idx_fallback_transition_fallback_candidate`, `idx_fallback_transition_fallback`, `idx_fallback_transition_reason`, `idx_fallback_transition_request`, `idx_fallback_transition_retryable`, `idx_fallback_transition_succeeded`, primary key |
| `request_policy_executions` | `policyExecutionRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_policy_execution_kind`, `idx_policy_execution_outcome`, `idx_policy_execution_request`, `idx_policy_execution_strategy`, `idx_policy_execution_terminal`, primary key |
| `request_routing_decisions` | `routingDecisionRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_routing_decision_provider_model`, `idx_routing_decision_request`, `idx_routing_decision_strategy`, primary key |
| `request_routing_signals` | `routingSignalRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_routing_signal_candidate`, `idx_routing_signal_name`, `idx_routing_signal_request`, `idx_routing_signal_strategy`, primary key |
| `request_target_candidates` | `decisionTargetCandidateRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_decision_candidate_context_fit`, `idx_decision_candidate_eligibility_decision`, `idx_decision_candidate_eligibility_reason`, `idx_decision_candidate_eligible`, `idx_decision_candidate_input_image`, `idx_decision_candidate_provider_model`, `idx_decision_candidate_reasoning_support`, `idx_decision_candidate_request`, `idx_decision_candidate_selected`, `idx_decision_candidate_tool_support`, `idx_decision_candidate_validation_status`, primary key |
| `request_target_filter_reasons` | `decisionTargetFilterReasonRecord` | `decision_telemetry` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_decision_filter_candidate`, `idx_decision_filter_reason`, `idx_decision_filter_request`, `idx_decision_filter_stage`, primary key |
| `request_token_estimates` | `requestTokenEstimateRecord` | `usage_diagnostics` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_request_token_estimate_bytes`, `idx_request_token_estimate_confidence`, `idx_request_token_estimate_group`, `idx_request_token_estimate_inbound`, `idx_request_token_estimate_input`, `idx_request_token_estimate_output_cap`, `idx_request_token_estimate_request`, `idx_request_token_estimate_requested_model`, `idx_request_token_estimate_reserved`, `idx_request_token_estimate_total_input`, `idx_request_token_estimate_ts`, primary key |
| `retention_job_table_results` | `retentionJobTableResultRecord` | `retention_control` | `job_id` joins `retention_jobs.id` | `idx_retention_result_class`, `idx_retention_result_cutoff`, `idx_retention_result_job`, `idx_retention_result_status`, `idx_retention_result_unique`, primary key |
| `retention_jobs` | `retentionJobRecord` | `retention_control` | `policy_version_id` joins `retention_policy_versions.id` | `idx_retention_job_mode`, `idx_retention_job_policy`, `idx_retention_job_started`, `idx_retention_job_status`, primary key |
| `retention_policy_rules` | `retentionPolicyRuleRecord` | `retention_control` | `policy_version_id` joins `retention_policy_versions.id` | `idx_retention_rule_class`, `idx_retention_rule_enabled`, `idx_retention_rule_policy`, `idx_retention_rule_unique`, primary key |
| `retention_policy_versions` | `retentionPolicyVersionRecord` | `retention_control` | none | `idx_retention_policy_active`, `idx_retention_policy_hash`, `idx_retention_policy_version`, primary key |
| `security_access_events` | `securityAccessEventRecord` | `security_access_events` | `request_id` joins `request_usage.request_id` when a usage row exists | `idx_security_access_admin`, `idx_security_access_caller`, `idx_security_access_event_type`, `idx_security_access_group`, `idx_security_access_ip`, `idx_security_access_outcome`, `idx_security_access_project`, `idx_security_access_reason`, `idx_security_access_request`, `idx_security_access_status`, `idx_security_access_surface`, `idx_security_access_token`, `idx_security_access_ts`, primary key |
| `usage_rollup_audit_events` | `usageRollupAuditEventRecord` | `usage_rollup` | `run_id` joins `usage_rollup_runs.id` | `idx_usage_rollup_audit_action`, `idx_usage_rollup_audit_run`, `idx_usage_rollup_audit_ts`, primary key |
| `usage_rollup_daily` | `usageRollupDailyRecord` | `usage_rollup` | `run_id` joins `usage_rollup_runs.id` | `idx_usage_rollup_daily_caller`, `idx_usage_rollup_daily_client`, `idx_usage_rollup_daily_day`, `idx_usage_rollup_daily_group`, `idx_usage_rollup_daily_provider_model`, `idx_usage_rollup_daily_run`, `idx_usage_rollup_daily_status_class`, `idx_usage_rollup_daily_status`, `idx_usage_rollup_daily_token`, `idx_usage_rollup_daily_unique`, primary key |
| `usage_rollup_decision_buckets` | `usageRollupDecisionBucketRecord` | `usage_rollup` | `run_id` joins `usage_rollup_runs.id` | `idx_usage_rollup_decision_bucket`, `idx_usage_rollup_decision_day`, `idx_usage_rollup_decision_group`, `idx_usage_rollup_decision_kind`, `idx_usage_rollup_decision_run`, `idx_usage_rollup_decision_status`, `idx_usage_rollup_decision_unique`, primary key |
| `usage_rollup_hourly` | `usageRollupHourlyRecord` | `usage_rollup` | `run_id` joins `usage_rollup_runs.id` | primary key |
| `usage_rollup_monthly_billing` | `usageRollupMonthlyBillingRecord` | `usage_rollup` | `run_id` joins `usage_rollup_runs.id` | primary key |
| `usage_rollup_runs` | `usageRollupRunRecord` | `usage_rollup` | none | `idx_usage_rollup_run_checksum`, `idx_usage_rollup_run_status`, `idx_usage_rollup_run_unique`, `idx_usage_rollup_run_window`, primary key |

### `request_usage`

- Schema type: `usageRecord`
- Retention class: `usage_detail`
- Foreign keys: none
- Indexes: `idx_request_usage_caller_ip`, `idx_request_usage_contract_bucket`, `idx_request_usage_contract_failure`, `idx_request_usage_contract_workload`, `idx_request_usage_contract`, `idx_request_usage_group_fp`, `idx_request_usage_group`, `idx_request_usage_input_image`, `idx_request_usage_pii_filter`, `idx_request_usage_policy_fp`, `idx_request_usage_pricing_fp`, `idx_request_usage_provider_model`, `idx_request_usage_router_version`, `idx_request_usage_routing_fp`, `idx_request_usage_token`, `idx_request_usage_ts`, `idx_request_usage_validation_age`, `idx_request_usage_validation_status`, `idx_request_usage_validation_workload`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `ts` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `caller_id` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_user` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_project` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_environment` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_ip` | string | yes | once per request at terminal request accounting | restricted; operational metadata, may identify a client network | deployment-defined scalar value |
| `token_id` | string | no | once per request at terminal request accounting | safe public token ID only; never a raw token or token hash | deployment-defined scalar value |
| `client` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `inbound_dialect` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `requested_model` | string | no | once per request at terminal request accounting | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `strategy` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `target_provider` | string | no | once per request at terminal request accounting | safe deployment metadata | `openrouter` |
| `target_model` | string | no | once per request at terminal request accounting | safe deployment metadata | deployment-defined model or group label |
| `target_dialect` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages`, `gemini-generate-content`, `replicate` |
| `target_region` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `stream` | boolean | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `true` or `false` |
| `cache` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `attempts` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `fallback_used` | boolean | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `true` or `false` |
| `latency_ms` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `ttfb_ms` | integer | yes | once per request at terminal request accounting | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms` | integer | yes | once per request at terminal request accounting | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms` | integer | yes | once per request at terminal request accounting | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_output_tokens_per_sec` | decimal | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec` | decimal | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec` | decimal | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec` | decimal | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `input_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `output_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `total_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `reasoning_tokens` | integer | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `cached_input_tokens` | integer | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `reasoning_attempt_count` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `reasoning_successful_attempt_count` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `reasoning_reported_attempt_count` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `input_has_image` | boolean | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `true` or `false` |
| `input_image_count` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `pii_filter_applied` | boolean | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `true` or `false` |
| `pii_filter_mode` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `pii_filter_replacements` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `pii_filter_rule_count` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `contract_present` | boolean | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `true` or `false` |
| `contract_bucket` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `contract_failure_reason` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `contract_workload` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `target_validation_status` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `target_validation_workload` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `target_validation_age_bucket` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `input_price_per_million_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `output_price_per_million_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `cached_input_price_per_million_usd` | decimal | yes | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `image_input_price_per_million_tokens_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `image_input_price_per_image_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `input_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `image_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `output_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `total_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_input_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_output_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_total_cost_usd` | decimal | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `pricing_source` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `pricing_updated_at` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `cache_enabled` | boolean | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `true` or `false` |
| `cache_items` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `cache_bytes` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_max_bytes` | integer | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_occupancy_pct` | decimal | no | once per request at terminal request accounting | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `quota_state` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `key_state` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `traffic_shape_applied` | boolean | no | during caller traffic-shaping admission | safe scalar diagnostics metadata | `true` or `false` |
| `traffic_shape_decision` | string | no | during caller traffic-shaping admission | safe scalar diagnostics metadata | deployment-defined scalar value |
| `traffic_shape_scope` | string | no | during caller traffic-shaping admission | safe scalar diagnostics metadata | deployment-defined scalar value |
| `traffic_shape_bucket` | string | no | during caller traffic-shaping admission | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `traffic_shape_retry_after_ms` | integer | no | during caller traffic-shaping admission | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `traffic_shape_queue_wait_ms` | integer | no | during caller traffic-shaping admission | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `traffic_shape_estimated_input_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `traffic_shape_reserved_output_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `traffic_shape_total_reserved_tokens` | integer | no | after usage and request-time cost accounting | safe scalar diagnostics metadata | `0` or positive integer |
| `router_version` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `router_build_date` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |
| `routing_config_fingerprint` | string | no | once per request at terminal request accounting | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |
| `model_group_config_fingerprint` | string | no | once per request at terminal request accounting | safe non-reversible HMAC/fingerprint value | deployment-defined model or group label |
| `routing_policy_fingerprint` | string | no | once per request at terminal request accounting | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |
| `pricing_catalog_fingerprint` | string | no | once per request at terminal request accounting | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |
| `error` | string | no | once per request at terminal request accounting | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_attempts`

- Schema type: `requestAttemptRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_attempt_cancel`, `idx_request_attempt_error`, `idx_request_attempt_provider_model`, `idx_request_attempt_request`, `idx_request_attempt_status`, `idx_request_attempt_timeout`, `idx_request_attempt_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `attempt_index` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0` or positive integer |
| `ts` | string | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `provider` | string | no | after each upstream attempt completes or fails | safe deployment metadata | `openrouter` |
| `model` | string | no | after each upstream attempt completes or fails | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `endpoint_host` | string | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | deployment-defined scalar value |
| `duration_ms` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `status_code` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `error_class` | string | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_message` | string | no | after each upstream attempt completes or fails | safe only after router sanitization | deployment-defined scalar value |
| `retryable` | boolean | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `true` or `false` |
| `timed_out` | boolean | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `true` or `false` |
| `client_canceled` | boolean | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `true` or `false` |
| `selected` | boolean | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `true` or `false` |
| `fallback_reason` | string | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | deployment-defined scalar value |
| `request_bytes` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0` or positive integer |
| `response_bytes` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0` or positive integer |
| `attempt_timeout_ms` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `retry_after_ms` | integer | no | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `reasoning_tokens` | integer | yes | after each upstream attempt completes or fails | safe scalar diagnostics metadata | `0` or positive integer |

### `request_trace_events`

- Schema type: `requestTraceEventRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_trace_error`, `idx_request_trace_event`, `idx_request_trace_request`, `idx_request_trace_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | as ordered router decisions occur | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | as ordered router decisions occur | safe scalar diagnostics metadata | `0` or positive integer |
| `ts` | string | no | as ordered router decisions occur | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `event` | string | no | as ordered router decisions occur | safe scalar diagnostics metadata | deployment-defined scalar value |
| `message` | string | no | as ordered router decisions occur | safe scalar diagnostics metadata | deployment-defined scalar value |
| `provider` | string | no | as ordered router decisions occur | safe deployment metadata | `openrouter` |
| `model` | string | no | as ordered router decisions occur | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | as ordered router decisions occur | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `duration_ms` | integer | no | as ordered router decisions occur | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `status_code` | integer | no | as ordered router decisions occur | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `error_class` | string | no | as ordered router decisions occur | safe scalar diagnostics metadata | deployment-defined scalar value |
| `retryable` | boolean | no | as ordered router decisions occur | safe scalar diagnostics metadata | `true` or `false` |
| `attempt` | integer | no | as ordered router decisions occur | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_errors`

- Schema type: `requestErrorRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_error_class`, `idx_request_error_status`, `idx_request_error_ts`, `idx_request_error_type`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `ts` | string | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `status` | integer | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `error_type` | string | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_class` | string | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_message` | string | no | when the request ends in a caller-visible terminal error | safe only after router sanitization | deployment-defined scalar value |
| `retryable` | boolean | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | `true` or `false` |
| `attempts` | integer | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | deployment-defined scalar value |
| `provider` | string | no | when the request ends in a caller-visible terminal error | safe deployment metadata | `openrouter` |
| `model` | string | no | when the request ends in a caller-visible terminal error | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | when the request ends in a caller-visible terminal error | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |

### `request_upstream_error_details`

- Schema type: `requestUpstreamErrorDetailRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_upstream_error_detail_attempt`, `idx_upstream_error_detail_class`, `idx_upstream_error_detail_field`, `idx_upstream_error_detail_request`, `idx_upstream_error_detail_status`, `idx_upstream_error_detail_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `attempt_index` | integer | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | `0` or positive integer |
| `seq` | integer | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | `0` or positive integer |
| `ts` | string | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `status_code` | integer | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `error_class` | string | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | deployment-defined scalar value |
| `field_name` | string | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | deployment-defined scalar value |
| `field_value` | string | no | when sanitized allowlisted upstream error-detail capture is enabled | restricted; bounded allowlisted and sanitized upstream error field | deployment-defined scalar value |
| `source` | string | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | deployment-defined scalar value |
| `truncated` | boolean | no | when sanitized allowlisted upstream error-detail capture is enabled | safe scalar diagnostics metadata | `true` or `false` |

### `request_traffic_shape_events`

- Schema type: `requestTrafficShapeEventRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0` or positive integer |
| `scope` | string | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | deployment-defined scalar value |
| `bucket` | string | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `decision` | string | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | deployment-defined scalar value |
| `cost` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `retry_after_ms` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `queue_wait_ms` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `estimated_input_tokens` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0` or positive integer |
| `reserved_output_tokens` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0` or positive integer |
| `total_reserved_tokens` | integer | no | during caller traffic-shaping admission and queue handling | safe scalar diagnostics metadata | `0` or positive integer |

### `request_upstream_shape_events`

- Schema type: `requestUpstreamShapeEventRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_upstream_shape_request`, `idx_request_upstream_shape_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `0` or positive integer |
| `ts` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `scope` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | deployment-defined scalar value |
| `provider` | string | no | during provider/model/target shared admission or adaptive backoff | safe deployment metadata | `openrouter` |
| `model_ref` | string | no | during provider/model/target shared admission or adaptive backoff | safe deployment metadata | deployment-defined model or group label |
| `model` | string | no | during provider/model/target shared admission or adaptive backoff | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `bucket` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `decision` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | deployment-defined scalar value |
| `retry_after_ms` | integer | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `estimated_input_tokens` | integer | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `0` or positive integer |
| `reserved_output_tokens` | integer | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `0` or positive integer |
| `total_reserved_tokens` | integer | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `0` or positive integer |
| `backoff_reason` | string | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | deployment-defined scalar value |
| `queue_wait_ms` | integer | no | during provider/model/target shared admission or adaptive backoff | safe scalar diagnostics metadata | `0..600000` milliseconds |

### `request_shapes`

- Schema type: `requestShapeRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_shape_bytes_bucket`, `idx_request_shape_client`, `idx_request_shape_fp`, `idx_request_shape_group`, `idx_request_shape_image_count`, `idx_request_shape_inbound`, `idx_request_shape_input_token_bucket`, `idx_request_shape_output_cap_bucket`, `idx_request_shape_reasoning`, `idx_request_shape_request`, `idx_request_shape_requested_model`, `idx_request_shape_stream`, `idx_request_shape_structured`, `idx_request_shape_tool_choice`, `idx_request_shape_tool_count`, `idx_request_shape_tool_schema_bucket`, `idx_request_shape_tool_schema_fp`, `idx_request_shape_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `ts` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `inbound_dialect` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `requested_model` | string | no | after safe inbound request-shape extraction | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | deployment-defined scalar value |
| `client` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | deployment-defined scalar value |
| `stream` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `input_item_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `message_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `system_message_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `developer_message_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `user_message_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `assistant_message_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `tool_result_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `function_call_output_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `tool_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `tool_choice_mode` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | deployment-defined scalar value |
| `parallel_tool_calls_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `response_format_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `structured_output_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `reasoning_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `reasoning_effort_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `reasoning_budget_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `include_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `truncation_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `metadata_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `store_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `previous_response_id_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `image_count` | integer | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `audio_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `video_present` | boolean | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `true` or `false` |
| `input_text_bytes_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `tool_schema_bytes_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `total_request_bytes_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_input_tokens_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `0` or positive integer |
| `requested_output_cap_field` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | deployment-defined scalar value |
| `requested_output_cap_bucket` | string | no | after safe inbound request-shape extraction | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `tool_schema_fingerprint` | string | no | after safe inbound request-shape extraction | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |
| `request_shape_fingerprint` | string | no | after safe inbound request-shape extraction | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |

### `request_translation_shapes`

- Schema type: `requestTranslationShapeRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_translation_shape_bridge`, `idx_request_translation_shape_bytes`, `idx_request_translation_shape_dialect`, `idx_request_translation_shape_output_cap`, `idx_request_translation_shape_provider_model`, `idx_request_translation_shape_reasoning`, `idx_request_translation_shape_request_fp`, `idx_request_translation_shape_request`, `idx_request_translation_shape_stream`, `idx_request_translation_shape_tool_choice`, `idx_request_translation_shape_tool_count`, `idx_request_translation_shape_tool_fp`, `idx_request_translation_shape_ts`, `idx_request_translation_shape_unsupported`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `attempt_index` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `ts` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `provider` | string | no | after upstream dialect translation for an attempt | safe deployment metadata | `openrouter` |
| `model` | string | no | after upstream dialect translation for an attempt | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `bridge_direction` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `endpoint_path` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `translated_stream` | boolean | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `true` or `false` |
| `translated_tool_count` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `translated_tool_choice_mode` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `translated_output_cap_field` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `translated_output_cap_bucket` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `translated_reasoning_control` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `translated_request_bytes_bucket` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `fields_stripped_count` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `fields_rewritten_count` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `unsupported_fields_present` | boolean | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `true` or `false` |
| `translation_warning_count` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `request_shape_fingerprint` | string | no | after upstream dialect translation for an attempt | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |
| `tool_schema_fingerprint` | string | no | after upstream dialect translation for an attempt | safe non-reversible HMAC/fingerprint value | opaque non-reversible fingerprint |

### `request_translation_field_events`

- Schema type: `requestTranslationFieldEventRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_translation_field_action`, `idx_request_translation_field_attempt`, `idx_request_translation_field_name`, `idx_request_translation_field_request`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `attempt_index` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `seq` | integer | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | `0` or positive integer |
| `field_name` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `action` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reason` | string | no | after upstream dialect translation for an attempt | safe scalar diagnostics metadata | deployment-defined scalar value |

### `legal_hold_audit_events`

- Schema type: `legalHoldAuditEventRecord`
- Retention class: `retention_control`
- Foreign keys: `hold_id` joins `legal_holds.hold_id`; `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_legal_hold_audit_event`, `idx_legal_hold_audit_hold`, `idx_legal_hold_audit_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `hold_id` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `event_type` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `data_class` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `request_id` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `start_ts` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `end_ts` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `previous_active` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `new_active` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `actor` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reason_code` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `message` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ts` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |

### `legal_holds`

- Schema type: `legalHoldRecord`
- Retention class: `retention_control`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_legal_hold_active`, `idx_legal_hold_class_range`, `idx_legal_hold_created`, `idx_legal_hold_id`, `idx_legal_hold_request`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `hold_id` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `data_class` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `request_id` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `active` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `start_ts` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `end_ts` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reason_code` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `subject` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `created_by` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `created_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `released_by` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `released_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `release_reason` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `notes` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_cache_reasons`

- Schema type: `decisionCacheReasonRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_decision_cache_reason`, `idx_decision_cache_request`, `idx_decision_cache_status`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `status` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `reason` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `provider` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | `openrouter` |
| `model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |

### `request_content_audit_events`

- Schema type: `contentCaptureAuditRecord`
- Retention class: `content_capture`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_content_audit_action`, `idx_request_content_audit_request`, `idx_request_content_audit_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |
| `ts` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | `2026-06-29T12:34:56Z` |
| `action` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |
| `request_id` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | `req_0123456789abcdef0123456789abcdef` |
| `actor_caller_id` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |
| `actor_token_id` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |
| `scope` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |
| `rows_affected` | integer | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |
| `reason` | string | no | when a content-capture maintenance action is recorded | safe metadata under content-capture authorization | deployment-defined scalar value |

### `request_content_captures`

- Schema type: `contentCaptureRecord`
- Retention class: `content_capture`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_content_group`, `idx_request_content_request`, `idx_request_content_retention`, `idx_request_content_scope`, `idx_request_content_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `request_id` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `req_0123456789abcdef0123456789abcdef` |
| `ts` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `2026-06-29T12:34:56Z` |
| `scope` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `sequence` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `caller_id` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `token_id` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `resolved_group` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `target_provider` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `openrouter` |
| `target_model` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined model or group label |
| `inbound_dialect` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `target_dialect` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `openai-chat`, `openai-responses`, `anthropic-messages`, `gemini-generate-content`, `replicate` |
| `content_type` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `content_text` | string | no | only when governed content capture is explicitly enabled | restricted; redacted and AES-256-GCM-encrypted governed content, not ordinary diagnostics | deployment-defined scalar value |
| `encryption_nonce` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `encryption_kms_key_id` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `encrypted` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |
| `content_bytes` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `0` or positive integer |
| `truncated` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |
| `redacted` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |
| `redaction_count` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `0` or positive integer |
| `images_captured` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |
| `image_count` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `0` or positive integer |
| `source_status` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `200`, `403`, `502` |
| `retention_until` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `deletion_requested` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |

### `request_content_headers`

- Schema type: `contentCaptureHeaderRecord`
- Retention class: `content_capture`
- Foreign keys: `capture_id` joins `request_content_captures.id`; `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_content_header_capture`, `idx_request_content_header_request`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `capture_id` | integer | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `request_id` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `req_0123456789abcdef0123456789abcdef` |
| `scope` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `name` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `value` | string | no | only when governed content capture is explicitly enabled | restricted; redacted and AES-256-GCM-encrypted governed content, not ordinary diagnostics | deployment-defined scalar value |
| `encryption_nonce` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `encryption_kms_key_id` | string | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | deployment-defined scalar value |
| `encrypted` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |
| `redacted` | boolean | no | only when governed content capture is explicitly enabled | safe metadata under content-capture authorization | `true` or `false` |

### `request_decision_shape_features`

- Schema type: `decisionShapeFeatureRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_decision_shape_feature`, `idx_decision_shape_request`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `feature_name` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `bool_value` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `int_value` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `text_value` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_dynamic_score_terms`

- Schema type: `dynamicScoreTermRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_dynamic_score_contribution_bucket`, `idx_dynamic_score_final_bucket`, `idx_dynamic_score_score_name`, `idx_dynamic_score_term_candidate`, `idx_dynamic_score_term_name`, `idx_dynamic_score_term_provider_model`, `idx_dynamic_score_term_rank`, `idx_dynamic_score_term_request`, `idx_dynamic_score_term_selected`, `idx_dynamic_score_value_bucket`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `rank` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `provider` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | `openrouter` |
| `model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `term_name` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `score_name` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `weight` | decimal | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `value` | decimal | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `contribution` | decimal | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `final_score` | decimal | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `value_bucket` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `contribution_bucket` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `final_score_bucket` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `observation_count` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `selected` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |

### `request_fallback_transitions`

- Schema type: `fallbackTransitionRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_fallback_transition_attempt`, `idx_fallback_transition_error_class`, `idx_fallback_transition_failed_candidate`, `idx_fallback_transition_failed`, `idx_fallback_transition_fallback_candidate`, `idx_fallback_transition_fallback`, `idx_fallback_transition_reason`, `idx_fallback_transition_request`, `idx_fallback_transition_retryable`, `idx_fallback_transition_succeeded`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `attempt_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `failed_candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `fallback_candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `failed_provider` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | `openrouter` |
| `failed_model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `failed_dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `fallback_provider` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | `openrouter` |
| `fallback_model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `fallback_dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `fallback_reason` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_class` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `retryable` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `fallback_succeeded` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |

### `request_policy_executions`

- Schema type: `policyExecutionRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_policy_execution_kind`, `idx_policy_execution_outcome`, `idx_policy_execution_request`, `idx_policy_execution_strategy`, `idx_policy_execution_terminal`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `strategy` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `policy_kind` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `outcome` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `success`, `fallback`, `shadow_recommended`, `baseline`, `error` |
| `duration_ms` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `eligible_target_count` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `all_target_count` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `selected_candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `fallback_count` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `class_label` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_class` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_message` | string | no | when optional decision telemetry records the routing decision | safe only after router sanitization | deployment-defined scalar value |
| `terminal_error_type` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_routing_decisions`

- Schema type: `routingDecisionRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_routing_decision_provider_model`, `idx_routing_decision_request`, `idx_routing_decision_strategy`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `strategy` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `selected_candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `provider` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | `openrouter` |
| `model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `fallback_count` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `class_label` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_routing_signals`

- Schema type: `routingSignalRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_routing_signal_candidate`, `idx_routing_signal_name`, `idx_routing_signal_request`, `idx_routing_signal_strategy`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `strategy` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `signal_name` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `affinity_hit`, `affinity_miss`, `affinity_expired`, `affinity_ineligible`, `affinity_disabled`, `shadow_recommended_candidate` |
| `source` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `bool_value` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `int_value` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `float_value` | decimal | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `text_value` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_target_candidates`

- Schema type: `decisionTargetCandidateRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_decision_candidate_context_fit`, `idx_decision_candidate_eligibility_decision`, `idx_decision_candidate_eligibility_reason`, `idx_decision_candidate_eligible`, `idx_decision_candidate_input_image`, `idx_decision_candidate_provider_model`, `idx_decision_candidate_reasoning_support`, `idx_decision_candidate_request`, `idx_decision_candidate_selected`, `idx_decision_candidate_tool_support`, `idx_decision_candidate_validation_status`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `group_target_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `provider` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | `openrouter` |
| `model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `model_ref` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `weight` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `tool_only` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `context_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `max_estimated_input_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `max_requested_output_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `max_request_bytes` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `max_tool_schema_bytes` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_total_input_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `requested_output_cap_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_total_with_output_cap` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `request_bytes` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `tool_schema_bytes` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `context_headroom_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `context_fit` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `request_bytes_fit` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `tool_schema_fit` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `eligibility_decision` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `eligibility_reason` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `input_image` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `output_image` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `tool_support` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `forced_tool_choice` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `structured_output` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `honors_max_tokens` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `reasoning_support` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `reasoning_mode` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reasoning_control` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reasoning_default` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `reasoning_stream_block` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `validation_status` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `validation_age_bucket` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `eligible` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `selected` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |

### `request_target_filter_reasons`

- Schema type: `decisionTargetFilterReasonRecord`
- Retention class: `decision_telemetry`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_decision_filter_candidate`, `idx_decision_filter_reason`, `idx_decision_filter_request`, `idx_decision_filter_stage`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `seq` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `candidate_index` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `stage` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reason` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |

### `request_token_estimates`

- Schema type: `requestTokenEstimateRecord`
- Retention class: `usage_diagnostics`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_request_token_estimate_bytes`, `idx_request_token_estimate_confidence`, `idx_request_token_estimate_group`, `idx_request_token_estimate_inbound`, `idx_request_token_estimate_input`, `idx_request_token_estimate_output_cap`, `idx_request_token_estimate_request`, `idx_request_token_estimate_requested_model`, `idx_request_token_estimate_reserved`, `idx_request_token_estimate_total_input`, `idx_request_token_estimate_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `request_id` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `ts` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `inbound_dialect` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `requested_model` | string | no | when optional decision telemetry records the routing decision | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `estimate_method` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `estimate_version` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `estimated_input_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_tool_schema_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_image_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_audio_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimated_total_input_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `requested_output_cap_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `requested_output_cap_field` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | deployment-defined scalar value |
| `router_default_output_cap_applied` | boolean | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `true` or `false` |
| `total_reserved_tokens` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `request_bytes` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `translated_request_bytes` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimate_warning_count` | integer | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `0` or positive integer |
| `estimate_confidence_bucket` | string | no | when optional decision telemetry records the routing decision | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |

### `retention_job_table_results`

- Schema type: `retentionJobTableResultRecord`
- Retention class: `retention_control`
- Foreign keys: `job_id` joins `retention_jobs.id`
- Indexes: `idx_retention_result_class`, `idx_retention_result_cutoff`, `idx_retention_result_job`, `idx_retention_result_status`, `idx_retention_result_unique`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `job_id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `data_class` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `table_name` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `cutoff_ts` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `retention_days` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `batch_size` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `candidate_rows` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `held_rows` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `eligible_rows` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `blocked_rows` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `deleted_rows` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `message` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `started_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `completed_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |

### `retention_jobs`

- Schema type: `retentionJobRecord`
- Retention class: `retention_control`
- Foreign keys: `policy_version_id` joins `retention_policy_versions.id`
- Indexes: `idx_retention_job_mode`, `idx_retention_job_policy`, `idx_retention_job_started`, `idx_retention_job_status`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `policy_version_id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `mode` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `dry_run` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `started_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `completed_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `requested_by` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `error_message` | string | no | during retention policy, legal-hold, or purge workflows | safe only after router sanitization | deployment-defined scalar value |

### `retention_policy_rules`

- Schema type: `retentionPolicyRuleRecord`
- Retention class: `retention_control`
- Foreign keys: `policy_version_id` joins `retention_policy_versions.id`
- Indexes: `idx_retention_rule_class`, `idx_retention_rule_enabled`, `idx_retention_rule_policy`, `idx_retention_rule_unique`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `policy_version_id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `rule_order` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `data_class` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `enabled` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `retention_days` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `batch_size` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `require_finalized_rollup` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `created_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |

### `retention_policy_versions`

- Schema type: `retentionPolicyVersionRecord`
- Retention class: `retention_control`
- Foreign keys: none
- Indexes: `idx_retention_policy_active`, `idx_retention_policy_hash`, `idx_retention_policy_version`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `version` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `config_hash` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `source` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `active` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `dry_run` | boolean | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `true` or `false` |
| `default_batch_size` | integer | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |
| `created_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `activated_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `deactivated_at` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `notes` | string | no | during retention policy, legal-hold, or purge workflows | safe scalar diagnostics metadata | deployment-defined scalar value |

### `security_access_events`

- Schema type: `securityAccessEventRecord`
- Retention class: `security_access_events`
- Foreign keys: `request_id` joins `request_usage.request_id` when a usage row exists
- Indexes: `idx_security_access_admin`, `idx_security_access_caller`, `idx_security_access_event_type`, `idx_security_access_group`, `idx_security_access_ip`, `idx_security_access_outcome`, `idx_security_access_project`, `idx_security_access_reason`, `idx_security_access_request`, `idx_security_access_status`, `idx_security_access_surface`, `idx_security_access_token`, `idx_security_access_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ts` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `request_id` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `req_0123456789abcdef0123456789abcdef` |
| `event_type` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `surface` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `http_method` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `path_template` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status_code` | integer | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `outcome` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `reason_code` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `auth_subject` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `auth_source` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_id` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_user` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_project` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_environment` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `token_id` | string | no | when an authenticated or denied admin/security surface is evaluated | safe public token ID only; never a raw token or token hash | deployment-defined scalar value |
| `admin_subject` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `admin_domain` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `client` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `user_agent_family` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ip_address` | string | no | when an authenticated or denied admin/security surface is evaluated | restricted; operational metadata, may identify a client network | deployment-defined scalar value |
| `ip_version` | integer | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ip_source` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `trusted_proxy_applied` | boolean | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `true` or `false` |
| `request_is_private` | boolean | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `true` or `false` |
| `request_is_loopback` | boolean | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `true` or `false` |
| `request_is_reserved` | boolean | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `true` or `false` |
| `model_group` | string | no | when an authenticated or denied admin/security surface is evaluated | safe deployment metadata | deployment-defined model or group label |
| `requested_model` | string | no | when an authenticated or denied admin/security surface is evaluated | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | deployment-defined scalar value |
| `input_tokens` | integer | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `0` or positive integer |
| `output_tokens` | integer | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `0` or positive integer |
| `total_tokens` | integer | no | when an authenticated or denied admin/security surface is evaluated | safe scalar diagnostics metadata | `0` or positive integer |

### `usage_rollup_audit_events`

- Schema type: `usageRollupAuditEventRecord`
- Retention class: `usage_rollup`
- Foreign keys: `run_id` joins `usage_rollup_runs.id`
- Indexes: `idx_usage_rollup_audit_action`, `idx_usage_rollup_audit_run`, `idx_usage_rollup_audit_ts`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `run_id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `action` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `actor_subject` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ts` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `safe_summary` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |

### `usage_rollup_daily`

- Schema type: `usageRollupDailyRecord`
- Retention class: `usage_rollup`
- Foreign keys: `run_id` joins `usage_rollup_runs.id`
- Indexes: `idx_usage_rollup_daily_caller`, `idx_usage_rollup_daily_client`, `idx_usage_rollup_daily_day`, `idx_usage_rollup_daily_group`, `idx_usage_rollup_daily_provider_model`, `idx_usage_rollup_daily_run`, `idx_usage_rollup_daily_status_class`, `idx_usage_rollup_daily_status`, `idx_usage_rollup_daily_token`, `idx_usage_rollup_daily_unique`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `run_id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `rollup_type` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `day_utc` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `window_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `window_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `source_table` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_id` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_user` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_project` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_environment` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `token_id` | string | no | during usage rollup generation or finalization | safe public token ID only; never a raw token or token hash | deployment-defined scalar value |
| `client` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `inbound_dialect` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `requested_model` | string | no | during usage rollup generation or finalization | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `strategy` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `target_provider` | string | no | during usage rollup generation or finalization | safe deployment metadata | `openrouter` |
| `target_model` | string | no | during usage rollup generation or finalization | safe deployment metadata | deployment-defined model or group label |
| `target_dialect` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages`, `gemini-generate-content`, `replicate` |
| `status_class` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `stream` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `cache` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `input_has_image` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `pii_filter_applied` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `contract_bucket` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `target_validation_status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `source_request_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `success_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `error_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `stream_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_hit_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_miss_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_bypass_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `fallback_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `attempt_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `output_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `total_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `image_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_id` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_name` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_version` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_input_price_per_million_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_output_price_per_million_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `input_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `output_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `total_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `latency_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `latency_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `latency_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `ttfb_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ttfb_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `ttfb_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `upstream_duration_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_output_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_snapshot_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_items_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `cache_bytes_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_bytes_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_max_bytes_latest` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_occupancy_pct_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `cache_occupancy_pct_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |

### `usage_rollup_decision_buckets`

- Schema type: `usageRollupDecisionBucketRecord`
- Retention class: `usage_rollup`
- Foreign keys: `run_id` joins `usage_rollup_runs.id`
- Indexes: `idx_usage_rollup_decision_bucket`, `idx_usage_rollup_decision_day`, `idx_usage_rollup_decision_group`, `idx_usage_rollup_decision_kind`, `idx_usage_rollup_decision_run`, `idx_usage_rollup_decision_status`, `idx_usage_rollup_decision_unique`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `run_id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `rollup_type` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `day_utc` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `window_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `window_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `resolved_group` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `strategy` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `bucket_kind` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `bucket_name` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `secondary_bucket` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `source_request_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `error_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `output_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `total_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |

### `usage_rollup_hourly`

- Schema type: `usageRollupHourlyRecord`
- Retention class: `usage_rollup`
- Foreign keys: `run_id` joins `usage_rollup_runs.id`
- Indexes: primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `run_id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `rollup_type` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `bucket_utc` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `bucket_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `bucket_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `window_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `window_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `source_table` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_id` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_user` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_project` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_environment` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `token_id` | string | no | during usage rollup generation or finalization | safe public token ID only; never a raw token or token hash | deployment-defined scalar value |
| `client` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `inbound_dialect` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `requested_model` | string | no | during usage rollup generation or finalization | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `strategy` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `target_provider` | string | no | during usage rollup generation or finalization | safe deployment metadata | `openrouter` |
| `target_model` | string | no | during usage rollup generation or finalization | safe deployment metadata | deployment-defined model or group label |
| `target_dialect` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages`, `gemini-generate-content`, `replicate` |
| `status_class` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `stream` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `cache` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `input_has_image` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `pii_filter_applied` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `contract_bucket` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `target_validation_status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `source_request_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `success_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `error_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `stream_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_hit_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_miss_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_bypass_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `fallback_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `attempt_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `output_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `total_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `image_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_id` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_name` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_version` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_input_price_per_million_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_output_price_per_million_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `input_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `output_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `total_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `latency_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `latency_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `latency_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `ttfb_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ttfb_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `ttfb_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `upstream_duration_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_output_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_snapshot_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_items_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `cache_bytes_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_bytes_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_max_bytes_latest` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_occupancy_pct_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `cache_occupancy_pct_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |

### `usage_rollup_monthly_billing`

- Schema type: `usageRollupMonthlyBillingRecord`
- Retention class: `usage_rollup`
- Foreign keys: `run_id` joins `usage_rollup_runs.id`
- Indexes: primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `run_id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `rollup_type` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `bucket_utc` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `bucket_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `bucket_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `window_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `window_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `source_table` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_id` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_user` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_project` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `caller_environment` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `token_id` | string | no | during usage rollup generation or finalization | safe public token ID only; never a raw token or token hash | deployment-defined scalar value |
| `client` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `inbound_dialect` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages` |
| `requested_model` | string | no | during usage rollup generation or finalization | safe deployment metadata | deployment-defined model or group label |
| `resolved_group` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `strategy` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `target_provider` | string | no | during usage rollup generation or finalization | safe deployment metadata | `openrouter` |
| `target_model` | string | no | during usage rollup generation or finalization | safe deployment metadata | deployment-defined model or group label |
| `target_dialect` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `openai-chat`, `openai-responses`, `anthropic-messages`, `gemini-generate-content`, `replicate` |
| `status_class` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `stream` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `cache` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `input_has_image` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `pii_filter_applied` | boolean | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `true` or `false` |
| `contract_bucket` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `none`, `small`, `large`, or deployment bucket |
| `target_validation_status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `source_request_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `success_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `error_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `stream_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_hit_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_miss_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_bypass_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `fallback_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `attempt_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `output_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `total_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_image_tokens` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `image_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `upstream_reported_total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_id` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_name` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_version` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `baseline_input_price_per_million_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_output_price_per_million_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_input_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_output_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `baseline_total_cost_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `input_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `output_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `total_savings_usd` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `latency_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `latency_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `latency_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `ttfb_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `ttfb_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `ttfb_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `upstream_duration_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_duration_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `downstream_duration_ms_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0..600000` milliseconds |
| `upstream_output_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_output_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `upstream_total_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_output_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_min` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `downstream_total_tokens_per_sec_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_snapshot_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_items_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `cache_bytes_sum` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_bytes_max` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_max_bytes_latest` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `cache_occupancy_pct_sum` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |
| `cache_occupancy_pct_max` | decimal | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0.0` or positive decimal |

### `usage_rollup_runs`

- Schema type: `usageRollupRunRecord`
- Retention class: `usage_rollup`
- Foreign keys: none
- Indexes: `idx_usage_rollup_run_checksum`, `idx_usage_rollup_run_status`, `idx_usage_rollup_run_unique`, `idx_usage_rollup_run_window`, primary key

| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |
|---|---|---|---|---|---|
| `id` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `rollup_type` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `status` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `200`, `403`, `502` |
| `window_start` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `window_end` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `source_table` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `source_request_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `source_min_ts` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `source_max_ts` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `source_checksum` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `daily_row_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `rollup_row_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `decision_bucket_row_count` | integer | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `0` or positive integer |
| `router_version` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `router_commit` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | deployment-defined scalar value |
| `safe_error` | string | no | during usage rollup generation or finalization | safe only after router sanitization | deployment-defined scalar value |
| `started_at` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `completed_at` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `generated_at` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |
| `finalized_at` | string | no | during usage rollup generation or finalization | safe scalar diagnostics metadata | `2026-06-29T12:34:56Z` |

<!-- /@generated diagnostics-schema -->

## Fields Intentionally Not Persisted

Ordinary usage, diagnostics, decision telemetry, logs, and reports must not persist these values:

| Data | Persisted? | Redaction or boundary |
|---|---|---|
| Raw prompts and message text | No | PII filtering applies before routing through the configured filtering pipeline; ordinary diagnostics record counts, buckets, flags, and fingerprints instead of text. |
| Raw image payloads or image bytes | No | Request-shape and usage rows record image presence, counts, and token/cost fields only. Governed content capture must be explicitly enabled before any redacted content can be stored. |
| Raw tool schemas and tool outputs | No | Shape telemetry stores tool counts, size buckets, and non-reversible fingerprints. |
| Raw response bodies | No | Usage rows store token/cost/latency/status metadata; terminal errors are sanitized summaries. |
| Bearer tokens | No | Caller authentication stores and compares derived credential state; diagnostics may record public `token_id` labels only. |
| Provider API keys | No | Provider credentials remain in runtime configuration or environment-backed secret files and are never copied into usage tables. |
| Token hashes | No | Token hashes are secret credential material; diagnostic tables use public token IDs rather than hashes. |
| Full upstream headers | No | Header capture is allowlisted and redacted when governed content capture is enabled; ordinary diagnostics exclude full upstream headers. |
| Full upstream response bodies | No | `request_upstream_error_details` stores only bounded, allowlisted, sanitized fields when diagnostics are enabled and upstream-error detail storage is not explicitly disabled; full bodies are excluded. |
| PII placeholder maps and regex captures | No | `pii_filter` mappings are in-memory only; usage stores safe counters such as applied flag, mode, replacement count, and matched-rule count. |

Governed content capture is separate from ordinary diagnostics. It is disabled by default and writes to `request_content_captures`, `request_content_headers`, and `request_content_audit_events` only when explicitly enabled by the operator. Captured content remains authorization-controlled, redacted according to capture policy, retention-managed, and outside ordinary usage reports.

## Schema Currency

Release validation checks that this reference matches the shipped diagnostics schema. If a deployed router version changes diagnostic tables or columns, the matching product documentation should be regenerated and reviewed as part of that release.
