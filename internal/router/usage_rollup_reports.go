// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"gorm.io/gorm"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Historical reads are explicit: never blend retained raw rows with aggregates
// or silently reinterpret a row-level predicate as an aggregate predicate.
func configureRollupHistory(o *UsageReportOptions, q url.Values, path, prefix string) error {
	source := strings.TrimSpace(q.Get("history_source"))
	if source == "" || source == "raw" {
		return nil
	}
	if source != "rollup" {
		return fmt.Errorf("history_source must be raw or rollup")
	}
	relPath := strings.TrimPrefix(path, prefix)
	spec, scalar := adminScalarEndpointSpecs(relPath)
	_, dimensionOK := rollupDimension(spec.Dimension)
	_, secondaryOK := rollupDimension(spec.Secondary)
	if relPath != "/api/summary" && relPath != "/api/overview" && (!scalar || spec.Requests || spec.Anomalies || spec.ShapeReport != "" || spec.Diagnostic != "" || !dimensionOK || !secondaryOK) {
		return fmt.Errorf("rollup history is unavailable for this report")
	}
	if q.Get("from") == "" {
		o.From = o.From.UTC().Truncate(24 * time.Hour)
	}
	if q.Get("to") == "" {
		o.To = o.To.UTC().Truncate(24 * time.Hour)
	}
	o.RollupHistory = true
	return validateRollupReportOptions(*o)
}

func validateRollupReportOptions(o UsageReportOptions) error {
	if !o.From.Equal(o.From.UTC().Truncate(24*time.Hour)) || !o.To.Equal(o.To.UTC().Truncate(24*time.Hour)) {
		return fmt.Errorf("rollup history requires whole UTC days")
	}
	if o.CallerIP != "" || o.TokenIDPrefix != "" || o.Status != 0 || o.UsageSource != "" || o.ReasoningPresent != nil || o.CachePresent != nil || o.TrafficShapedOnly || o.TrafficShapeBucket != "" || o.TrafficShapeScope != "" || o.ToolChoiceMode != "" || o.ToolCountBucket != "" || o.RequestBytesBucket != "" || o.InputTokensBucket != "" || o.OutputCapBucket != "" || o.RequestShapeFP != "" || o.ToolSchemaFP != "" || o.ErrorClass != "" {
		return fmt.Errorf("row-level filter is unavailable in finalized daily rollups; use raw history")
	}
	return nil
}
func (s *usageStore) rollupReportQuery(o UsageReportOptions) *gorm.DB {
	q := s.db.Model(&usageRollupDailyRecord{}).Where("rollup_type = ? AND status = ? AND day_utc >= ? AND day_utc < ?", "daily", "finalized", o.From.UTC().Format("2006-01-02"), o.To.UTC().Format("2006-01-02"))
	for column, value := range map[string]string{"caller_id": o.CallerID, "caller_user": o.CallerUser, "caller_project": o.CallerProject, "caller_environment": o.CallerEnvironment, "token_id": o.TokenID, "requested_model": o.RequestedModel, "resolved_group": o.ResolvedGroup, "target_provider": o.TargetProvider, "target_model": o.TargetModel, "target_dialect": o.TargetDialect, "client": o.Client, "inbound_dialect": o.InboundDialect, "cache": o.Cache} {
		if value != "" {
			q = q.Where(column+" = ?", value)
		}
	}
	if o.StreamOnly != nil {
		q = q.Where("stream = ?", *o.StreamOnly)
	}
	if o.MultimodalOnly {
		q = q.Where("input_has_image = ?", true)
	}
	return q
}
func rollupListCostColumn(column string) string {
	// A pre-migration rollup has no provider-list attribution evidence. Fall
	// back to billed cost, never invent a negative response-cache saving.
	return "CASE WHEN cost_component_reported_request_count = 0 AND " + column + " = 0 THEN total_cost_usd ELSE " + column + " END"
}

func rollupReportSelect(b adminSavingsBaselineDTO) string {
	parts := []string{adminBaselineCostSQLExpr(b) + " AS baseline_cost_usd", "SUM(CASE WHEN pii_filter_applied THEN source_request_count ELSE 0 END) AS pii_filtered_requests"}
	columns := make([]string, 0, len(tokenBreakdownFields))
	for _, column := range tokenBreakdownFields {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	for _, column := range columns {
		expr := column
		if column == "provider_list_cost_usd" || column == "routing_list_cost_usd" {
			expr = rollupListCostColumn(column)
		}
		parts = append(parts, "SUM("+expr+") AS "+column)
	}
	for _, pair := range [][2]string{
		{"source_request_count", "calls"}, {"error_count", "errors"}, {"stream_count", "streams"}, {"cache_hit_count", "cache_hits"}, {"cache_miss_count", "cache_misses"}, {"cache_bypass_count", "cache_bypass"}, {"fallback_count", "fallbacks"}, {"attempt_count", "attempts"},
		{"input_tokens", "input_tokens"}, {"output_tokens", "output_tokens"}, {"total_tokens", "total_tokens"}, {"input_image_count", "input_image_count"}, {"input_image_tokens", "input_image_tokens"},
		{"input_cost_usd", "input_cost_usd"}, {"output_cost_usd", "output_cost_usd"}, {"image_cost_usd", "image_cost_usd"}, {"total_cost_usd", "total_cost_usd"}, {"provider_list_cost_usd", "cost_list_usd"},
		{"latency_ms_sum", "latency_ms"}, {"ttfb_ms_sum", "ttfb_ms"}, {"ttfb_ms_count", "ttfb_count"}, {"upstream_duration_ms_sum", "upstream_ms"}, {"upstream_duration_ms_count", "upstream_ms_count"}, {"downstream_duration_ms_sum", "downstream_ms"}, {"downstream_duration_ms_count", "downstream_ms_count"},
		{"upstream_output_tokens_per_sec_sum", "upstream_output_tps"}, {"upstream_output_tokens_per_sec_count", "upstream_output_tps_count"}, {"upstream_total_tokens_per_sec_sum", "upstream_total_tps"}, {"upstream_total_tokens_per_sec_count", "upstream_total_tps_count"}, {"downstream_output_tokens_per_sec_sum", "downstream_output_tps"}, {"downstream_output_tokens_per_sec_count", "downstream_output_tps_count"}, {"downstream_total_tokens_per_sec_sum", "downstream_total_tps"}, {"downstream_total_tokens_per_sec_count", "downstream_total_tps_count"},
		{"estimated_request_count", "estimated_request_count"}, {"estimated_input_tokens", "estimated_input_tokens"}, {"estimated_output_tokens", "estimated_output_tokens"},
	} {
		expr := pair[0]
		if pair[1] == "cost_list_usd" {
			expr = rollupListCostColumn(expr)
		}
		parts = append(parts, "SUM("+expr+") AS "+pair[1])
	}
	for _, pair := range [][2]string{{"latency_ms_max", "max_latency_ms"}, {"ttfb_ms_max", "max_ttfb_ms"}, {"upstream_duration_ms_max", "max_upstream_ms"}, {"downstream_duration_ms_max", "max_downstream_ms"}} {
		parts = append(parts, "MAX("+pair[0]+") AS "+pair[1])
	}
	return strings.Join(parts, ",")
}
func (s *usageStore) rollupReportGroup(o UsageReportOptions, b adminSavingsBaselineDTO, key, secondary, order string, limit int) ([]tokenScalarAggRecord, error) {
	q := s.rollupReportQuery(o).Select(key + " AS key," + secondary + " AS secondary_key," + rollupReportSelect(b))
	groups := []string{}
	if key != "''" {
		groups = append(groups, key)
	}
	if secondary != "''" && secondary != key {
		groups = append(groups, secondary)
	}
	if len(groups) > 0 {
		q = q.Group(strings.Join(groups, ","))
	}
	if order != "" {
		q = q.Order(order)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []tokenScalarAggRecord
	err := q.Scan(&rows).Error
	return rows, err
}
func (s *usageStore) rollupOverview(o UsageReportOptions, b adminSavingsBaselineDTO, limit int) (adminOverviewSQLResult, error) {
	var out adminOverviewSQLResult
	if err := validateRollupReportOptions(o); err != nil {
		return out, err
	}
	if limit <= 0 {
		limit = 50
	}
	total, err := s.rollupReportGroup(o, b, "''", "''", "", 0)
	if err != nil {
		return out, err
	}
	if len(total) > 0 {
		out.Total = total[0]
	}
	for _, group := range []struct {
		key   string
		dest  *[]tokenScalarAggRecord
		limit int
	}{
		{"day_utc || 'T00:00:00Z'", &out.ByHour, 0}, {adminSQLDefault("token_id", "unknown"), &out.ByToken, limit + 1}, {"COALESCE(NULLIF(resolved_group,''),NULLIF(requested_model,''),'unknown')", &out.ByGroup, limit + 1}, {adminSQLDefault("target_provider", "unknown") + " || '/' || " + adminSQLDefault("target_model", "unknown"), &out.ByProvider, limit + 1}, {"status_class", &out.ByStatus, limit + 1},
	} {
		order := "calls DESC,key ASC"
		if group.limit == 0 {
			order = "key ASC"
		}
		rows, err := s.rollupReportGroup(o, b, group.key, "''", order, group.limit)
		if err != nil {
			return out, err
		}
		if group.limit > 0 && len(rows) > limit {
			rows = rows[:limit]
			out.GroupHasMore = true
		}
		*group.dest = rows
	}
	out.RecentRequests = []usageRow{}
	return out, nil
}
func rollupDimension(dimension string) (string, bool) {
	switch dimension {
	case "", "caller_id", "caller_user", "token_id", "requested_model", "model_group", "provider_model", "dialect", "client", "inbound_dialect", "cache", "project", "environment":
		return adminScalarDimensionSQLExpr(dimension)
	}
	return "", false
}
func (s *usageStore) rollupScalarAggs(o UsageReportOptions, spec adminScalarEndpointSpec, b adminSavingsBaselineDTO, sortKey string, limit int) (map[string]*adminScalarAgg, *agg, float64, bool, error) {
	if err := validateRollupReportOptions(o); err != nil {
		return nil, nil, 0, false, err
	}
	key, ok := rollupDimension(spec.Dimension)
	if !ok {
		return nil, nil, 0, false, fmt.Errorf("unsupported rollup dimension %q", spec.Dimension)
	}
	secondary, ok := rollupDimension(spec.Secondary)
	if !ok {
		return nil, nil, 0, false, fmt.Errorf("unsupported rollup dimension %q", spec.Secondary)
	}
	order := "calls DESC,key ASC,secondary_key ASC"
	switch sortKey {
	case "cost":
		order = "total_cost_usd DESC,key ASC,secondary_key ASC"
	case "tokens":
		order = "total_tokens DESC,key ASC,secondary_key ASC"
	case "errors":
		order = "errors DESC,key ASC,secondary_key ASC"
	case "latency":
		order = "SUM(latency_ms_sum) * 1.0 / NULLIF(SUM(source_request_count),0) DESC,key ASC,secondary_key ASC"
	case "savings":
		order = "(" + adminBaselineCostSQLExpr(b) + " - SUM(total_cost_usd)) DESC,key ASC,secondary_key ASC"
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.rollupReportGroup(o, b, key, secondary, order, limit+1)
	if err != nil {
		return nil, nil, 0, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	totalRows, err := s.rollupReportGroup(o, b, "''", "''", "", 0)
	if err != nil {
		return nil, nil, 0, false, err
	}
	var totalRec tokenScalarAggRecord
	if len(totalRows) > 0 {
		totalRec = totalRows[0]
	}
	total := aggFromTokenScalarAggRecord(totalRec)
	table := map[string]*adminScalarAgg{}
	for _, row := range rows {
		a := adminScalarAggFromSQLRecord(row, b)
		table[joinKey(a.Key, a.SecondaryKey)] = a
	}
	return table, &total, totalRec.BaselineCostUSD, hasMore, nil
}
