// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/router"
	"github.com/metrum-ai/router/internal/smartrouterctl"
	"gopkg.in/yaml.v3"
)

type securityDoctorFinding struct {
	Check    string `json:"check"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

type securityDoctorReport struct {
	Schema    string                  `json:"schema"`
	Generated string                  `json:"generated_at"`
	Target    string                  `json:"target,omitempty"`
	Findings  []securityDoctorFinding `json:"findings"`
}

func securityDoctorCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "router configuration file")
	security := fs.Bool("security", false, "run security posture checks")
	format := fs.String("format", "text", "output format: text or json")
	failOn := fs.String("fail-on", "fail", "exit non-zero on: fail or warn")
	target := fs.String("target", "", "router base URL for bounded unauthenticated probes")
	advisories := fs.String("advisories", "", "engine advisory snapshot JSON file (default: snapshot beside the release binary)")
	reportLatest := fs.Bool("report-latest", false, "store the safe summary as the latest admin security posture report")
	routerURL := fs.String("router-url", "", "router admin API base URL (or ROUTER_URL)")
	socketPath := fs.String("socket", "", "admin Unix socket path")
	adminKeyFile := fs.String("admin-key-file", "", "mode-0600 admin key file (or ROUTER_ADMIN_KEY_FILE)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*security || strings.TrimSpace(*configPath) == "" {
		fmt.Fprintln(stderr, "usage: metrum-ai-routerctl doctor --security --config <file> [--target <url>] [--format text|json] [--fail-on fail|warn]")
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(stderr, "doctor: --format must be text or json")
		return 2
	}
	if *failOn != "fail" && *failOn != "warn" {
		fmt.Fprintln(stderr, "doctor: --fail-on must be fail or warn")
		return 2
	}
	cfg, err := router.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "doctor: config invalid: %v\n", err)
		return 2
	}
	report := securityDoctorReport{Schema: "metrum.ai/router-security-posture/v1", Generated: time.Now().UTC().Format(time.RFC3339), Target: safeTarget(*target)}
	report.Findings = securityDoctorConfigFindings(cfg)
	report.Findings = append(report.Findings, engineAdvisoryFinding(*configPath, *advisories))
	if strings.TrimSpace(*target) != "" {
		report.Findings = append(report.Findings, probeSecurityTarget(*target)...)
	}
	if *reportLatest {
		if err := submitSecurityDoctorReport(*routerURL, *adminKeyFile, *socketPath, report); err != nil {
			fmt.Fprintln(stderr, "doctor: could not submit latest security posture report")
			return 2
		}
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(true)
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "doctor: encode report: %v\n", err)
			return 2
		}
	} else {
		fmt.Fprintf(stdout, "Security posture (%s)\n", report.Generated)
		for _, f := range report.Findings {
			fmt.Fprintf(stdout, "%-5s %-28s %s", strings.ToUpper(f.Status), f.Check, f.Detail)
			if f.Fix != "" {
				fmt.Fprintf(stdout, " Fix: %s", f.Fix)
			}
			fmt.Fprintln(stdout)
		}
	}
	for _, f := range report.Findings {
		if f.Status == "fail" || (*failOn == "warn" && f.Status == "warn") {
			return 1
		}
	}
	return 0
}

func submitSecurityDoctorReport(routerURL, adminKeyFile, socketPath string, report securityDoctorReport) error {
	client, err := smartrouterctl.NewAdminAPIClientWithSocket(routerURL, adminKeyFile, socketPath)
	if err != nil {
		return err
	}
	latest := router.SecurityPostureReport{
		Schema: "metrum.ai/router-security-posture-summary/v1", GeneratedAt: report.Generated,
		Outcome: "pass", Findings: make([]router.SecurityPostureReportCheck, 0, len(report.Findings)),
	}
	for _, finding := range report.Findings {
		latest.Findings = append(latest.Findings, router.SecurityPostureReportCheck{Check: finding.Check, Status: finding.Status, Severity: finding.Severity})
		if finding.Status == "fail" {
			latest.Outcome = "fail"
		} else if finding.Status == "warn" && latest.Outcome == "pass" {
			latest.Outcome = "warn"
		}
	}
	_, _, err = client.Do(http.MethodPost, "/admin/v1/reports/security/posture/latest", latest, nil)
	return err
}

const (
	engineAdvisorySchema      = "metrum.ai/engine-advisory-gate/v1"
	engineAdvisoryMaxOutput   = 5 << 20
	engineAdvisoryMaxFindings = 10_000
	engineAdvisoryMaxImages   = 200
)

const callerKeyMaxAge = 90 * 24 * time.Hour

var engineImageDefaultValue = regexp.MustCompile(`\$\{[^}:]+:-([^}]+)\}`)

type engineAdvisoryImage struct {
	Image   string `json:"image"`
	Engine  string `json:"engine"`
	Package string `json:"package"`
	Version string `json:"version"`
}

type engineAdvisoryRecord struct {
	Image     string `json:"image"`
	Advisory  string `json:"advisory"`
	Exception string `json:"exception"`
}

type engineAdvisorySnapshot struct {
	Schema        string                 `json:"schema"`
	CheckedImages int                    `json:"checked_images"`
	Advisories    int                    `json:"advisories"`
	Unexcepted    int                    `json:"unexcepted"`
	Images        []engineAdvisoryImage  `json:"images"`
	Findings      []engineAdvisoryRecord `json:"findings"`
}

func engineAdvisoryFinding(configPath, advisoryPath string) securityDoctorFinding {
	pins, err := configuredEngineImages(configPath)
	if err != nil {
		return doctorFinding("engine-pin-advisories", "fail", "could not read engine image pins from the operator config", "Check that the config is readable YAML and that image pins use an image: field.")
	}
	if len(pins) == 0 {
		return doctorInfo("engine-pin-advisories", "no vLLM or SGLang image pins are present in the operator config")
	}
	if strings.TrimSpace(advisoryPath) == "" {
		advisoryPath = defaultEngineAdvisorySnapshot()
	}
	if advisoryPath == "" {
		return doctorNotChecked("engine-pin-advisories", "engine image pins were not checked because this release has no advisory snapshot", "Build or download the release advisory snapshot, or pass --advisories <file>.")
	}
	file, err := os.Open(advisoryPath)
	if err != nil {
		return doctorFinding("engine-pin-advisories", "fail", "engine advisory snapshot could not be read", "Pass a readable snapshot generated by scripts/engine_advisory_gate.py.")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, engineAdvisoryMaxOutput+1))
	if err != nil || len(raw) > engineAdvisoryMaxOutput {
		return doctorFinding("engine-pin-advisories", "fail", "engine advisory snapshot is unreadable or exceeds the size limit", "Regenerate the bounded snapshot with scripts/engine_advisory_gate.py.")
	}
	var snapshot engineAdvisorySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || !validEngineAdvisorySnapshot(snapshot) {
		return doctorFinding("engine-pin-advisories", "fail", "engine advisory snapshot has an invalid schema or contents", "Regenerate the snapshot with scripts/engine_advisory_gate.py.")
	}
	known := make(map[string]struct{}, len(snapshot.Images))
	for _, image := range snapshot.Images {
		known[image.Image] = struct{}{}
	}
	findings := make(map[string][]engineAdvisoryRecord)
	for _, finding := range snapshot.Findings {
		findings[finding.Image] = append(findings[finding.Image], finding)
	}
	unmatched, unexcepted, excepted := 0, 0, 0
	for _, pin := range pins {
		if _, ok := known[pin]; !ok {
			unmatched++
			continue
		}
		for _, finding := range findings[pin] {
			if finding.Exception == "" {
				unexcepted++
			} else {
				excepted++
			}
		}
	}
	if unexcepted > 0 {
		return doctorFinding("engine-pin-advisories", "fail", fmt.Sprintf("%d unexcepted advisory finding(s) match configured engine image pins", unexcepted), "Update the affected image pins or review the scoped exception in the release snapshot source.")
	}
	if unmatched > 0 {
		return doctorFinding("engine-pin-advisories", "warn", fmt.Sprintf("%d configured engine image pin(s) are absent from the advisory snapshot", unmatched), "Regenerate the release snapshot for the deployment image pins or pass a matching --advisories file.")
	}
	if excepted > 0 {
		return doctorFinding("engine-pin-advisories", "warn", fmt.Sprintf("%d matching advisory finding(s) are covered by scoped exceptions", excepted), "Review the scoped exception before changing the pinned engine version.")
	}
	return doctorPass("engine-pin-advisories", fmt.Sprintf("%d configured engine image pin(s) match the advisory snapshot without findings", len(pins)))
}

func validEngineAdvisorySnapshot(snapshot engineAdvisorySnapshot) bool {
	if snapshot.Schema != engineAdvisorySchema || snapshot.CheckedImages < 0 || snapshot.CheckedImages > engineAdvisoryMaxImages || snapshot.Advisories < 0 || snapshot.Advisories > engineAdvisoryMaxFindings || snapshot.Unexcepted < 0 || snapshot.Unexcepted > snapshot.Advisories || len(snapshot.Images) > engineAdvisoryMaxImages || len(snapshot.Findings) > engineAdvisoryMaxFindings || snapshot.CheckedImages != len(snapshot.Images) || snapshot.Advisories != len(snapshot.Findings) {
		return false
	}
	imageRefs := make(map[string]struct{}, len(snapshot.Images))
	for _, image := range snapshot.Images {
		if image.Image == "" || image.Version == "" || (image.Engine != "vllm" && image.Engine != "sglang") {
			return false
		}
		imageRefs[image.Image] = struct{}{}
	}
	unexcepted := 0
	for _, finding := range snapshot.Findings {
		if finding.Image == "" || finding.Advisory == "" {
			return false
		}
		if _, ok := imageRefs[finding.Image]; !ok {
			return false
		}
		if finding.Exception == "" {
			unexcepted++
		}
	}
	return snapshot.Unexcepted == unexcepted
}

func defaultEngineAdvisorySnapshot() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	path := filepath.Join(filepath.Dir(executable), "engine-advisories.json")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

func configuredEngineImages(configPath string) ([]string, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, engineAdvisoryMaxOutput+1))
	if err != nil || len(raw) > engineAdvisoryMaxOutput {
		return nil, fmt.Errorf("operator configuration exceeds the size limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	images := map[string]struct{}{}
	for {
		var document any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		collectEngineImages(document, images)
		if len(images) > engineAdvisoryMaxImages {
			return nil, fmt.Errorf("too many engine image pins")
		}
	}
	out := make([]string, 0, len(images))
	for image := range images {
		out = append(out, image)
	}
	sort.Strings(out)
	return out, nil
}

func collectEngineImages(value any, images map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "image" {
				if image, ok := child.(string); ok {
					image = engineImageDefaultValue.ReplaceAllString(image, "$1")
					if strings.Contains(strings.ToLower(image), "vllm") || strings.Contains(strings.ToLower(image), "sglang") {
						images[image] = struct{}{}
					}
				}
			}
			collectEngineImages(child, images)
		}
	case map[any]any:
		for key, child := range typed {
			if key == "image" {
				if image, ok := child.(string); ok {
					image = engineImageDefaultValue.ReplaceAllString(image, "$1")
					if strings.Contains(strings.ToLower(image), "vllm") || strings.Contains(strings.ToLower(image), "sglang") {
						images[image] = struct{}{}
					}
				}
			}
			collectEngineImages(child, images)
		}
	case []any:
		for _, child := range typed {
			collectEngineImages(child, images)
		}
	}
}

func securityDoctorConfigFindings(cfg *router.Config) []securityDoctorFinding {
	var out []securityDoctorFinding
	if strings.TrimSpace(cfg.Server.Listen) != "" && !router.AdminListenIsLoopback(cfg.Server.Listen) && !cfg.Server.TLS.Enabled() {
		out = append(out, doctorFinding("serving-tls", "warn", "serving listener is not loopback-bound and Router TLS is disabled", "Enable server.tls with a trusted certificate or terminate TLS at a trusted proxy."))
	} else {
		out = append(out, doctorPass("serving-tls", "serving listener is loopback-bound or Router TLS is enabled"))
	}
	unsafeProviderKeySources := 0
	providersWithoutSpendCeiling := 0
	for _, provider := range cfg.Provider {
		if strings.TrimSpace(provider.APIKey) != "" && strings.TrimSpace(provider.APIKeyEnv) == "" {
			unsafeProviderKeySources++
		}
		if provider.SpendBreaker.HourlyUSD <= 0 && provider.SpendBreaker.DailyUSD <= 0 {
			providersWithoutSpendCeiling++
		}
	}
	if unsafeProviderKeySources == 0 {
		out = append(out, doctorPass("provider-key-source", "provider keys are not configured as literal values"))
	} else {
		out = append(out, doctorFinding("provider-key-source", "warn", fmt.Sprintf("%d provider key(s) use literal config values instead of api_key_env", unsafeProviderKeySources), "Move provider key values to the deployment secret store and configure providers.<name>.api_key_env."))
	}
	if providersWithoutSpendCeiling == 0 {
		out = append(out, doctorPass("provider-spend-ceiling", "all configured providers have an hourly or daily spend ceiling"))
	} else {
		out = append(out, doctorFinding("provider-spend-ceiling", "warn", fmt.Sprintf("%d configured provider(s) have no hourly or daily spend ceiling", providersWithoutSpendCeiling), "Configure providers.<name>.spend_breaker.hourly_usd or daily_usd where a provider spend cap is required."))
	}
	admin := cfg.Admin
	if !admin.Enabled || router.AdminListenIsLoopback(admin.Listen) || admin.TLS.ClientAuth == "require" {
		out = append(out, doctorPass("admin-listener", "admin listener is disabled, loopback-bound, or requires client certificates"))
	} else {
		out = append(out, doctorFinding("admin-listener", "fail", "admin listener is reachable beyond loopback without required mutual TLS", "Bind to loopback or configure TLS client_auth: require with a trusted client CA."))
	}
	adminTLSOff := strings.TrimSpace(admin.TLS.Mode) == "" || strings.EqualFold(strings.TrimSpace(admin.TLS.Mode), "off")
	if admin.Enabled && !router.AdminListenIsLoopback(admin.Listen) && adminTLSOff && admin.AllowPlaintextNonLoopback {
		out = append(out, doctorFinding("admin-plaintext", "fail", "admin listener permits plaintext traffic beyond loopback", "Disable admin.allow_plaintext_non_loopback or configure admin TLS."))
	} else {
		out = append(out, doctorPass("admin-plaintext", "admin listener does not permit plaintext traffic beyond loopback"))
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Server.ConfigSource.Mode), "database") {
		out = append(out, doctorFinding("db-stored-callers", "warn", "DB-stored callers and token issuance metadata were not checked", "Run doctor against the active file configuration or verify caller key metadata through the control-plane admin API."))
		out = append(out, doctorNotChecked("metrics-auth-config", "metrics authorization could not be verified because callers are stored in the control-plane database", "Configure an active metrics_admin caller in the canonical database configuration."))
	} else {
		out = append(out, metricsAuthConfigFinding(cfg.Callers))
	}
	if !cfg.Server.AdminReports.Enabled || !cfg.Server.AdminReports.Security.Enabled {
		out = append(out, doctorFinding("security-reports", "warn", "security reports are disabled", "Enable server.admin_reports and server.admin_reports.security for authorized security reporting."))
	} else {
		out = append(out, doctorPass("security-reports", "security reports are enabled"))
	}
	unbounded := 0
	for _, caller := range cfg.Callers {
		if !activeDoctorCaller(caller) {
			continue
		}
		if caller.Rate.RPM <= 0 && caller.Rate.TPM <= 0 && caller.Rate.Concurrent <= 0 && caller.Quota.Day.Requests <= 0 && caller.Quota.Day.Tokens <= 0 && caller.Quota.Month.Requests <= 0 && caller.Quota.Month.Tokens <= 0 {
			unbounded++
		}
	}
	if unbounded == 0 {
		out = append(out, doctorPass("caller-limits", "all configured callers have at least one rate or quota limit"))
	} else {
		out = append(out, doctorFinding("caller-limits", "fail", fmt.Sprintf("%d configured caller(s) have no rate or quota limit", unbounded), "Configure at least one caller rate limit or daily/monthly quota for every caller."))
	}
	legacyKeys, weakKeys := 0, 0
	entropyFloor := cfg.Security.KeyHygiene.MinEntropyBits
	if entropyFloor == 0 {
		entropyFloor = 128
	}
	for _, caller := range cfg.Callers {
		if !activeDoctorCaller(caller) || !callerHasVerifier(caller) {
			continue
		}
		if strings.TrimSpace(caller.TokenSHA256) != "" || caller.TokenFormatVersion != 2 {
			legacyKeys++
		}
		if caller.TokenEntropyBits < entropyFloor {
			weakKeys++
		}
	}
	if legacyKeys == 0 && weakKeys == 0 {
		out = append(out, doctorPass("caller-key-format", fmt.Sprintf("all active caller keys use v2 format with at least %d recorded entropy bits", entropyFloor)))
	} else {
		out = append(out, doctorFinding("caller-key-format", "warn", fmt.Sprintf("%d active caller key(s) use a legacy or unknown token format; %d have unknown or below-floor entropy (minimum %d bits)", legacyKeys, weakKeys, entropyFloor), "Rotate legacy keys to checksummed v2 tokens and record token_format_version: 2 and token_entropy_bits at or above security.key_hygiene.min_entropy_bits."))
	}
	developmentKeys := 0
	for _, caller := range cfg.Callers {
		if !activeDoctorCaller(caller) {
			continue
		}
		hasKey := strings.TrimSpace(caller.TokenHash) != "" || strings.TrimSpace(caller.TokenSHA256) != ""
		switch strings.ToLower(strings.TrimSpace(caller.Environment)) {
		case "dev", "development", "test", "testing":
			if hasKey {
				developmentKeys++
			}
		}
	}
	if developmentKeys == 0 {
		out = append(out, doctorPass("caller-key-environment", "no caller keys are assigned to a development or test environment"))
	} else {
		out = append(out, doctorFinding("caller-key-environment", "warn", fmt.Sprintf("%d caller key(s) are assigned to a development or test environment", developmentKeys), "Issue separate caller keys for production workloads and keep development keys scoped to non-production systems."))
	}
	out = append(out, callerKeyAgeFinding(cfg.Callers, time.Now().UTC()))
	sensitiveGroups := 0
	for _, group := range cfg.Models {
		sensitive := false
		for _, target := range group.Targets {
			for _, tag := range target.Tags {
				if strings.EqualFold(strings.TrimSpace(tag), "sensitive") {
					sensitive = true
				}
			}
		}
		if sensitive && !group.PIIFilter.Enabled {
			sensitiveGroups++
		}
	}
	if sensitiveGroups == 0 {
		out = append(out, doctorPass("sensitive-group-dlp", "groups containing targets tagged sensitive have PII filtering enabled or none are declared"))
	} else {
		out = append(out, doctorFinding("sensitive-group-dlp", "fail", fmt.Sprintf("%d group(s) containing targets tagged sensitive have PII filtering disabled", sensitiveGroups), "Enable a validated PII filter for each sensitive group."))
	}
	return out
}

func callerKeyAgeFinding(callers []router.CallerConfig, now time.Time) securityDoctorFinding {
	keys, unknown, stale, future := 0, 0, 0, 0
	for _, caller := range callers {
		if !activeDoctorCaller(caller) || !callerHasVerifier(caller) {
			continue
		}
		keys++
		issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(caller.TokenIssuedAt))
		if err != nil {
			unknown++
			continue
		}
		age := now.Sub(issuedAt)
		if age < 0 {
			future++
		} else if age > callerKeyMaxAge {
			stale++
		}
	}
	if keys == 0 {
		return doctorFinding("caller-key-age", "pass", "no caller keys are configured", "")
	}
	if stale > 0 {
		return doctorFinding("caller-key-age", "fail", fmt.Sprintf("%d caller key(s) exceed the 90-day maximum age", stale), "Rotate keys older than 90 days and update callers[].token_issued_at when recording externally issued keys.")
	}
	if unknown > 0 || future > 0 {
		return doctorFinding("caller-key-age", "warn", fmt.Sprintf("key age is unknown for %d caller key(s) and has a future date for %d", unknown, future), "Set callers[].token_issued_at to the UTC RFC3339 issuance time, or rotate the key to record a new issuance time.")
	}
	return doctorPass("caller-key-age", "all configured caller keys are within the 90-day maximum age")
}

func probeSecurityTarget(raw string) []securityDoctorFinding {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return []securityDoctorFinding{doctorFinding("target-url", "fail", "target must be an http(s) base URL without embedded credentials", "Pass only the router URL; do not include tokens in the URL.")}
	}
	if u.Scheme == "http" && !isLoopbackTarget(u.Hostname()) {
		return []securityDoctorFinding{doctorFinding("target-url", "fail", "plain HTTP targets are allowed only for loopback addresses", "Use HTTPS for remote targets or a loopback URL for local probes.")}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/metrics"
	u.RawQuery = ""
	u.Fragment = ""
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return []securityDoctorFinding{doctorFinding("metrics-auth", "fail", "could not construct target probe", "Check the target URL.")}
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return []securityDoctorFinding{doctorFinding("metrics-auth", "fail", "metrics probe failed or timed out", "Verify the target address and network path; the probe never sends credentials.")}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return []securityDoctorFinding{doctorFinding("metrics-auth", "fail", "unauthenticated GET /metrics returned success", "Require caller-token or admin authentication for metrics.")}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return []securityDoctorFinding{doctorPass("metrics-auth", "unauthenticated GET /metrics was rejected")}
	}
	return []securityDoctorFinding{doctorFinding("metrics-auth", "warn", fmt.Sprintf("metrics probe returned HTTP %d; access control could not be confirmed", resp.StatusCode), "Check the metrics listener and repeat the probe from an authorized network.")}
}

func isLoopbackTarget(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func doctorPass(check, detail string) securityDoctorFinding {
	return securityDoctorFinding{Check: check, Status: "pass", Severity: "info", Detail: detail}
}

func doctorFinding(check, status, detail, fix string) securityDoctorFinding {
	severity := "high"
	if status == "warn" {
		severity = "medium"
	}
	return securityDoctorFinding{Check: check, Status: status, Severity: severity, Detail: detail, Fix: fix}
}

func doctorInfo(check, detail string) securityDoctorFinding {
	return securityDoctorFinding{Check: check, Status: "not checked", Severity: "info", Detail: detail}
}

func doctorNotChecked(check, detail, fix string) securityDoctorFinding {
	finding := doctorInfo(check, detail)
	finding.Fix = fix
	return finding
}

func activeDoctorCaller(caller router.CallerConfig) bool {
	status := strings.ToLower(strings.TrimSpace(caller.Status))
	return status == "" || status == "active"
}

func callerHasVerifier(caller router.CallerConfig) bool {
	return strings.TrimSpace(caller.TokenHash) != "" || strings.TrimSpace(caller.TokenSHA256) != ""
}

func metricsAuthConfigFinding(callers []router.CallerConfig) securityDoctorFinding {
	for _, caller := range callers {
		if activeDoctorCaller(caller) && caller.MetricsAdmin && callerHasVerifier(caller) {
			return doctorPass("metrics-auth-config", "an active caller is configured for authorized serving-port metrics")
		}
	}
	return doctorFinding("metrics-auth-config", "warn", "no active caller has the metrics_admin authorization setting", "Configure a dedicated active caller with metrics_admin: true and a scoped token for scrapes; the serving-port /metrics endpoint remains authenticated.")
}

func safeTarget(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.Scheme + "://" + u.Host
}
