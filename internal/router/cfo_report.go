// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// cfoReport is an aggregate-only report. Projects are the recorded allocation
// dimension; a separate team mapping is not present in usage records.
type cfoReport struct {
	Period       adminReportPeriod         `json:"period"`
	Baseline     adminSavingsBaselineDTO   `json:"baseline"`
	Summary      adminReportSummary        `json:"summary"`
	ByTeam       []cfoTeam                 `json:"byTeam"`
	ByProject    adminScalarReportResponse `json:"byProject"`
	ByKey        adminScalarReportResponse `json:"byKey"`
	ByModelGroup adminScalarReportResponse `json:"byModelGroup"`
	ByUser       adminScalarReportResponse `json:"byUser"`
	Trend        []adminReportSeries       `json:"trend"`
	TopUsers     cfoTopUsers               `json:"topUsers"`
	Budgets      cfoBudgetSnapshot         `json:"budgets"`
	Warnings     []string                  `json:"warnings"`
	GeneratedUTC string                    `json:"generatedUtc"`
}

type cfoTopUsers struct {
	UserCount     int64   `json:"userCount"`
	TopUserCount  int64   `json:"topUserCount"`
	TopTokens     int64   `json:"topTokens"`
	TotalTokens   int64   `json:"totalTokens"`
	TokenSharePct float64 `json:"tokenSharePct"`
}

type cfoTeam struct {
	Team         string  `json:"team"`
	Unassigned   bool    `json:"unassigned"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	BaselineUSD  float64 `json:"baselineCostUsd"`
	SavingsUSD   float64 `json:"savingsUsd"`
}

// buildCFOReport deliberately uses the same SQL aggregates and cost column as
// the administrative overview. No request payloads, tokens or token hashes are
// copied into the report. Individual breakdowns are bounded top-N lists.
func (s *Service) buildCFOReport(filters adminReportFilters, baseline adminSavingsBaselineDTO) (cfoReport, error) {
	if filters.RollupHistory {
		return cfoReport{}, fmt.Errorf("CFO reports require retained raw usage rows")
	}
	var report cfoReport
	err := s.usage.db.Transaction(func(tx *gorm.DB) error {
		var err error
		isolated := &Service{usage: &usageStore{db: tx}, cfg: s.cfg}
		report, err = isolated.buildCFOReportSnapshot(filters, baseline)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return report, err
}

func (s *Service) buildCFOReportSnapshot(filters adminReportFilters, baseline adminSavingsBaselineDTO) (cfoReport, error) {
	// Every bounded breakdown is consistently ranked by recorded billed cost,
	// independently of a sort parameter carried over from another report tab.
	filters.Sort = "cfo_cost"
	report := cfoReport{
		Warnings:     []string{"Recorded costs are usage-derived, not provider invoices; missing or estimated usage and expired raw rows can make savings incomplete.", "Team mappings are current configuration, not historical attribution. Token budgets are live, not filtered by the report period or model."},
		Period:       adminReportPeriod{From: formatUsageTime(filters.From), To: formatUsageTime(filters.To)},
		Baseline:     baseline,
		GeneratedUTC: formatUsageTime(time.Now().UTC()),
	}
	for _, dimension := range []struct {
		name string
		dest *adminScalarReportResponse
	}{
		{"project", &report.ByProject},
		{"token_id", &report.ByKey},
		{"model_group", &report.ByModelGroup},
		{"caller_user", &report.ByUser},
	} {
		spec := adminScalarEndpointSpec{Report: "cfo-" + dimension.name, Dimension: dimension.name, Sort: "cfo_cost", WithBaseline: true}
		part, err := s.buildAdminScalarReportResponseSQL(filters, spec, baseline)
		if err != nil {
			return cfoReport{}, err
		}
		// Generic scalar charts calculate zero-denominator rates as 0%; CFO
		// exposes the underlying aggregates instead of misleading charts.
		part.Charts = nil
		part.Requests = nil
		if dimension.name == "token_id" {
			for i := range part.Rows {
				part.Rows[i].Key, part.Rows[i].SecondaryKey = fmt.Sprintf("key-%d", i+1), ""
			}
		}
		if part.Summary.BaselineCostUSD != nil && *part.Summary.BaselineCostUSD == 0 {
			part.Summary.SavingsPct = nil
		}
		for i := range part.Rows {
			if part.Rows[i].BaselineCostUSD != nil && *part.Rows[i].BaselineCostUSD == 0 {
				part.Rows[i].SavingsPct = nil
			}
		}
		*dimension.dest = part
	}
	report.Summary = report.ByProject.Summary
	teams := map[string][]string{}
	projects := make([]string, 0, len(s.cfg.Server.AdminReports.CFOTeams))
	for project, team := range s.cfg.Server.AdminReports.CFOTeams {
		teams[team] = append(teams[team], project)
		projects = append(projects, project)
	}
	names := make([]string, 0, len(teams))
	for team := range teams {
		names = append(names, team)
	}
	sort.Strings(names)
	sort.Strings(projects)
	for _, team := range names {
		sort.Strings(teams[team])
		row, err := s.cfoTeamAggregate(filters.UsageReportOptions, baseline, team, false, teams[team])
		if err != nil {
			return cfoReport{}, err
		}
		// Do not disclose configured team names outside the authorized usage scope.
		if row.Requests > 0 {
			report.ByTeam = append(report.ByTeam, row)
		}
	}
	unassigned, err := s.cfoTeamAggregate(filters.UsageReportOptions, baseline, "Unassigned", true, projects)
	if err != nil {
		return cfoReport{}, err
	}
	report.ByTeam = append(report.ByTeam, unassigned)
	overview, err := s.usage.adminOverviewSQL(filters.UsageReportOptions, baseline, filters.Limit)
	if err != nil {
		return cfoReport{}, err
	}
	report.Trend = buildAdminReportResponseSQL(filters, overview, baseline).Series
	report.TopUsers, err = s.cfoTopUsers(filters.UsageReportOptions, report.Summary.TotalTokens)
	if err != nil {
		return cfoReport{}, err
	}
	return report, nil
}

func (s *Service) cfoTopUsers(opts UsageReportOptions, totalTokens int64) (cfoTopUsers, error) {
	const userKey = "COALESCE(NULLIF(caller_user, ''), 'unknown')"
	users := s.usage.usageRowsQuery(opts).Select(userKey + " AS key, SUM(CASE WHEN total_tokens = 0 THEN input_tokens + output_tokens ELSE total_tokens END) AS tokens").Group(userKey)
	var count int64
	if err := s.usage.db.Table("(?) AS cfo_users", users).Count(&count).Error; err != nil {
		return cfoTopUsers{}, err
	}
	result := cfoTopUsers{UserCount: count, TotalTokens: totalTokens}
	if count == 0 {
		return result, nil
	}
	// ceil(5% of users); count is bounded before conversion to the SQL limit.
	top := count / 20
	if count%20 != 0 {
		top++
	}
	if top > int64(math.MaxInt) {
		return cfoTopUsers{}, fmt.Errorf("too many users for CFO concentration report")
	}
	var totals struct{ Tokens int64 }
	selected := users.Order("tokens DESC, key ASC").Limit(int(top))
	if err := s.usage.db.Table("(?) AS cfo_top_users", selected).Select("COALESCE(SUM(tokens), 0) AS tokens").Scan(&totals).Error; err != nil {
		return cfoTopUsers{}, err
	}
	result.TopUserCount, result.TopTokens = top, totals.Tokens
	if totalTokens > 0 {
		result.TokenSharePct = 100 * float64(totals.Tokens) / float64(totalTokens)
	}
	return result, nil
}

func (s *Service) cfoTeamAggregate(opts UsageReportOptions, baseline adminSavingsBaselineDTO, name string, unassigned bool, projects []string) (cfoTeam, error) {
	query := s.usage.usageRowsQuery(opts)
	if unassigned && len(projects) > 0 {
		query = query.Where("COALESCE(NULLIF(caller_project, ''), 'unknown') NOT IN ?", projects)
	} else if !unassigned {
		query = query.Where("COALESCE(NULLIF(caller_project, ''), 'unknown') IN ?", projects)
	}
	var values tokenScalarAggRecord
	if err := query.Select(adminScalarAggSQLSelectExpr(baseline)).Scan(&values).Error; err != nil {
		return cfoTeam{}, err
	}
	return cfoTeam{Team: name, Unassigned: unassigned, Requests: values.Calls, InputTokens: values.InputTokens, OutputTokens: values.OutputTokens, CostUSD: values.TotalCostUSD, BaselineUSD: values.BaselineCostUSD, SavingsUSD: values.BaselineCostUSD - values.TotalCostUSD}, nil
}

func (s *Service) handleCFOReport(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, global bool, format string) {
	if format == "markdown" && !s.cfg.Server.AdminReports.ExportMarkdown {
		http.NotFound(w, r)
		return
	}
	filters, ok := s.parseAdminReportFilters(w, r, false, subject, global)
	if !ok || !validateAdminTopNPageParams(w, filters) {
		return
	}
	if filters.TokenIDPrefix != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "CFO reports do not support token_id_prefix"}})
		return
	}
	if filters.RollupHistory {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "CFO reports require retained raw usage rows"}})
		return
	}
	// CFO includes several breakdowns in one response; bound each list even
	// if the deployment allows larger general-purpose admin reports.
	if filters.Limit > 500 {
		if r.URL.Query().Get("limit") != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "CFO limit must be at most 500"}})
			return
		}
		filters.Limit = 500
	}
	baseline, ok := s.parseAdminSavingsBaseline(w, r)
	if !ok {
		return
	}
	if baseline.Custom {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "CFO reports require a configured source-dated baseline"}})
		return
	}
	if math.IsNaN(baseline.BaselineInputPricePerMillionUSD) || math.IsNaN(baseline.BaselineOutputPricePerMillionUSD) || math.IsInf(baseline.BaselineInputPricePerMillionUSD, 0) || math.IsInf(baseline.BaselineOutputPricePerMillionUSD, 0) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "invalid-report-filter", "message": "baseline prices must be finite"}})
		return
	}
	report, err := s.buildCFOReport(filters, baseline)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "cfo", "handleCFOReport", &filters, err)
		return
	}
	report.Budgets = s.cfoBudgets(subject, global, filters.UsageReportOptions)
	// Validate every numeric field before emitting any format. In particular,
	// JSON cannot represent NaN/Inf; never return a partial successful report.
	serialized, err := json.Marshal(report)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "cfo", "handleCFOReport", &filters, err)
		return
	}
	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(serialized, '\n'))
		return
	}
	if format == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="cfo-report.md"`)
		_, _ = w.Write([]byte(cfoMarkdown(report)))
		return
	}
	data, err := cfoCSV(report)
	if err != nil {
		s.writeAdminReportQueryFailed(w, r, "cfo", "handleCFOReport", &filters, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="cfo-report.csv"`)
	_, _ = w.Write(data)
}

func fmtCFOSignedUSD(value float64) string { return fmt.Sprintf("%.6f", value) }

func cfoMarkdown(report cfoReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# CFO report\n\nPeriod UTC: %s to %s (end exclusive).\n\n", esc(report.Period.From), esc(report.Period.To))
	fmt.Fprintf(&b, "Recorded usage-derived cost: $%s (not an invoice). Baseline %s: source %s, dated %s; prices $%s input / $%s output per million tokens.\n\n", fmtUSD(report.Summary.TotalCostUSD), esc(report.Baseline.BaselineName), esc(report.Baseline.PricingSource), esc(report.Baseline.PricingUpdatedAt), fmtUSD(report.Baseline.BaselineInputPricePerMillionUSD), fmtUSD(report.Baseline.BaselineOutputPricePerMillionUSD))
	if report.Summary.BaselineCostUSD != nil && report.Summary.SavingsUSD != nil {
		fmt.Fprintf(&b, "Baseline $%s; hypothetical savings $%s.\n\n", fmtUSD(*report.Summary.BaselineCostUSD), fmtCFOSignedUSD(*report.Summary.SavingsUSD))
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(&b, "> %s\n\n", esc(warning))
	}
	fmt.Fprint(&b, "## Teams\n\nCurrent project-to-team mapping; unmapped usage is separate.\n\n| Team | Unassigned | Requests | Cost USD | Baseline USD | Savings USD |\n|---|---|---:|---:|---:|---:|\n")
	for _, team := range report.ByTeam {
		fmt.Fprintf(&b, "| %s | %t | %d | %s | %s | %s |\n", esc(team.Team), team.Unassigned, team.Requests, fmtUSD(team.CostUSD), fmtUSD(team.BaselineUSD), fmtCFOSignedUSD(team.SavingsUSD))
	}
	for _, section := range []struct {
		name string
		data adminScalarReportResponse
	}{{"Projects", report.ByProject}, {"Keys", report.ByKey}, {"Model groups", report.ByModelGroup}, {"Users", report.ByUser}} {
		fmt.Fprintf(&b, "\n## %s\n\nTop-N; more rows: %t.\n\n| Key | Requests | Cost USD | Baseline USD | Savings USD |\n|---|---:|---:|---:|---:|\n", section.name, section.data.Pagination.HasMore)
		for _, row := range section.data.Rows {
			base, savings := "", ""
			if row.BaselineCostUSD != nil {
				base = fmtUSD(*row.BaselineCostUSD)
			}
			if row.SavingsUSD != nil {
				savings = fmtCFOSignedUSD(*row.SavingsUSD)
			}
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", esc(row.Key), row.Requests, fmtUSD(row.TotalCostUSD), base, savings)
		}
	}
	fmt.Fprintf(&b, "\nTop 5%%: %d of %d user buckets, %d of %d tokens (%.2f%%); unknown users share one bucket.\n\n", report.TopUsers.TopUserCount, report.TopUsers.UserCount, report.TopUsers.TopTokens, report.TopUsers.TotalTokens, report.TopUsers.TokenSharePct)
	fmt.Fprintf(&b, "## Live token quotas\n\nSnapshot %s; current quota windows, not a historical USD budget. Available: %t; truncated: %t.\n\n| Caller | Window | Used | Limit | Utilization %% |\n|---|---|---:|---:|---:|\n", esc(report.Budgets.CapturedAt), report.Budgets.Available, report.Budgets.Truncated)
	for _, row := range report.Budgets.Rows {
		pct := "unlimited"
		if row.UtilPct != nil {
			pct = strconv.FormatFloat(*row.UtilPct, 'f', 2, 64)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s |\n", esc(row.CallerID), esc(row.Window), row.Used, row.Limit, pct)
	}
	fmt.Fprint(&b, "\n## Hourly trend\n\n| UTC | Tokens | Cost USD | Baseline USD | Savings USD |\n|---|---:|---:|---:|---:|\n")
	for _, point := range report.Trend {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", esc(point.TimeUTC), point.Tokens, fmtUSD(point.CostUSD), fmtUSD(point.BaselineCostUSD), fmtCFOSignedUSD(point.SavingsUSD))
	}
	return b.String()
}

// cfoCSV exports the same aggregate rows as JSON. Formula-leading text is
// neutralized using the same policy as the existing security CSV export.
func cfoCSV(report cfoReport) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write([]string{"section", "key", "requests", "input_tokens", "output_tokens", "cost_usd", "baseline_cost_usd", "savings_usd", "from_utc", "to_utc", "baseline_id", "pricing_source", "pricing_updated_at", "section_has_more", "measure", "value", "caller_id", "token_id", "budget_window", "baseline_input_usd_per_million", "baseline_output_usd_per_million", "budget_snapshot_utc", "budget_available", "budget_truncated"}); err != nil {
		return nil, err
	}
	writeRow := func(section, key string, requests, input, output int64, cost float64, baseline, savings *float64, hasMore bool) error {
		formatOptional := func(value *float64) string {
			if value == nil {
				return ""
			}
			return strconv.FormatFloat(*value, 'g', -1, 64)
		}
		return writer.Write([]string{
			csvSafeCell(section), csvSafeCell(key), strconv.FormatInt(requests, 10), strconv.FormatInt(input, 10), strconv.FormatInt(output, 10),
			strconv.FormatFloat(cost, 'g', -1, 64), formatOptional(baseline), formatOptional(savings),
			csvSafeCell(report.Period.From), csvSafeCell(report.Period.To), csvSafeCell(report.Baseline.BaselineID), csvSafeCell(report.Baseline.PricingSource), csvSafeCell(report.Baseline.PricingUpdatedAt),
			strconv.FormatBool(hasMore), "", "", "", "", "", strconv.FormatFloat(report.Baseline.BaselineInputPricePerMillionUSD, 'g', -1, 64), strconv.FormatFloat(report.Baseline.BaselineOutputPricePerMillionUSD, 'g', -1, 64), csvSafeCell(report.Budgets.CapturedAt), strconv.FormatBool(report.Budgets.Available), strconv.FormatBool(report.Budgets.Truncated),
		})
	}
	if err := writeRow("summary", "all", report.Summary.Requests, report.Summary.InputTokens, report.Summary.OutputTokens, report.Summary.TotalCostUSD, report.Summary.BaselineCostUSD, report.Summary.SavingsUSD, false); err != nil {
		return nil, err
	}
	for _, team := range report.ByTeam {
		section := "team"
		if team.Unassigned {
			section = "unassigned"
		}
		if err := writeRow(section, team.Team, team.Requests, team.InputTokens, team.OutputTokens, team.CostUSD, &team.BaselineUSD, &team.SavingsUSD, false); err != nil {
			return nil, err
		}
	}
	for _, group := range []struct {
		name    string
		rows    []adminScalarReportRow
		hasMore bool
	}{
		{"project", report.ByProject.Rows, report.ByProject.Pagination.HasMore}, {"key", report.ByKey.Rows, report.ByKey.Pagination.HasMore}, {"model_group", report.ByModelGroup.Rows, report.ByModelGroup.Pagination.HasMore}, {"user", report.ByUser.Rows, report.ByUser.Pagination.HasMore},
	} {
		for _, row := range group.rows {
			if err := writeRow(group.name, row.Key, row.Requests, row.InputTokens, row.OutputTokens, row.TotalCostUSD, row.BaselineCostUSD, row.SavingsUSD, group.hasMore); err != nil {
				return nil, err
			}
		}
	}
	writeMeasure := func(section, key, measure, value, callerID, tokenID, window string) error {
		return writer.Write([]string{csvSafeCell(section), csvSafeCell(key), "", "", "", "", "", "", csvSafeCell(report.Period.From), csvSafeCell(report.Period.To), csvSafeCell(report.Baseline.BaselineID), csvSafeCell(report.Baseline.PricingSource), csvSafeCell(report.Baseline.PricingUpdatedAt), "", csvSafeCell(measure), value, csvSafeCell(callerID), csvSafeCell(tokenID), csvSafeCell(window), strconv.FormatFloat(report.Baseline.BaselineInputPricePerMillionUSD, 'g', -1, 64), strconv.FormatFloat(report.Baseline.BaselineOutputPricePerMillionUSD, 'g', -1, 64), csvSafeCell(report.Budgets.CapturedAt), strconv.FormatBool(report.Budgets.Available), strconv.FormatBool(report.Budgets.Truncated)})
	}
	for _, warning := range report.Warnings {
		if err := writeMeasure("warning", "", "note", csvSafeCell(warning), "", "", ""); err != nil {
			return nil, err
		}
	}
	for _, measure := range []struct{ name, value string }{
		{"user_count", strconv.FormatInt(report.TopUsers.UserCount, 10)}, {"top_user_count", strconv.FormatInt(report.TopUsers.TopUserCount, 10)}, {"top_tokens", strconv.FormatInt(report.TopUsers.TopTokens, 10)}, {"total_tokens", strconv.FormatInt(report.TopUsers.TotalTokens, 10)}, {"token_share_pct", strconv.FormatFloat(report.TopUsers.TokenSharePct, 'g', -1, 64)},
	} {
		if err := writeMeasure("top_users", "", measure.name, measure.value, "", "", ""); err != nil {
			return nil, err
		}
	}
	for _, row := range report.Budgets.Rows {
		for _, measure := range []struct{ name, value string }{{"used_tokens", strconv.FormatInt(row.Used, 10)}, {"limit_tokens", strconv.FormatInt(row.Limit, 10)}} {
			if err := writeMeasure("live_token_budget", row.Project, measure.name, measure.value, row.CallerID, "", row.Window); err != nil {
				return nil, err
			}
		}
		if row.UtilPct != nil {
			if err := writeMeasure("live_token_budget", row.Project, "utilization_pct", strconv.FormatFloat(*row.UtilPct, 'g', -1, 64), row.CallerID, "", row.Window); err != nil {
				return nil, err
			}
		}
	}
	for _, point := range report.Trend {
		for _, measure := range []struct{ name, value string }{
			{"cost_usd", strconv.FormatFloat(point.CostUSD, 'g', -1, 64)},
			{"baseline_usd", strconv.FormatFloat(point.BaselineCostUSD, 'g', -1, 64)},
			{"savings_usd", strconv.FormatFloat(point.SavingsUSD, 'g', -1, 64)},
			{"tokens", strconv.FormatInt(point.Tokens, 10)},
		} {
			if err := writeMeasure("hourly_trend", point.TimeUTC, measure.name, measure.value, "", "", ""); err != nil {
				return nil, err
			}
		}
	}
	writer.Flush()
	return buf.Bytes(), writer.Error()
}
