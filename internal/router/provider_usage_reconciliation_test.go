// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

type fixedProviderUsageSource struct {
	cost float64
	err  error
}

type mappedProviderUsageSource map[string]fixedProviderUsageSource

func (s mappedProviderUsageSource) ProviderCost(_ context.Context, provider string, _, _ time.Time) (float64, error) {
	result := s[provider]
	return result.cost, result.err
}

type deadlineProviderUsageSource struct{ deadline time.Time }

func (s *deadlineProviderUsageSource) ProviderCost(ctx context.Context, _ string, _, _ time.Time) (float64, error) {
	var ok bool
	s.deadline, ok = ctx.Deadline()
	if !ok {
		return 0, errors.New("provider deadline missing")
	}
	return 0, nil
}

type blockingProviderUsageSource struct{}

func (blockingProviderUsageSource) ProviderCost(ctx context.Context, _ string, _, _ time.Time) (float64, error) {
	<-ctx.Done()
	return 0, errors.New("provider-admin-secret timeout response")
}

func TestProviderUsageDailyJobAppliesConfiguredProviderTimeout(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &Service{usage: store, cfg: &Config{Server: ServerConfig{
		ProviderUsageReconciliation: ProviderUsageReconciliationConfig{Enabled: true, TimeoutMS: 250, Providers: []ProviderUsageReconciliationEntry{{Provider: "primary"}}},
	}}}
	source := &deadlineProviderUsageSource{}
	if err := service.runProviderUsageReconciliation(context.Background(), source, time.Now()); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(source.deadline); remaining <= 0 || remaining > 250*time.Millisecond {
		t.Fatalf("provider deadline remaining = %s, want at most 250ms and in the future", remaining)
	}
}

func TestProviderUsageDailyJobRecordsSafeTimeoutStatus(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &Service{usage: store, cfg: &Config{Server: ServerConfig{
		ProviderUsageReconciliation: ProviderUsageReconciliationConfig{Enabled: true, TimeoutMS: 5, Providers: []ProviderUsageReconciliationEntry{{Provider: "primary"}}},
	}}}
	if err := service.runProviderUsageReconciliation(context.Background(), blockingProviderUsageSource{}, time.Now()); err == nil {
		t.Fatal("expected provider timeout")
	}
	var event securityAccessEventRecord
	if err := store.db.Where("event_type = ?", "provider_usage_failure").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.StatusCode != 504 || event.ReasonCode != "provider_usage_timeout" || event.Outcome != "failed" {
		t.Fatalf("unexpected timeout event: %+v", event)
	}
	if serialized := fmt.Sprintf("%+v", event); strings.Contains(serialized, "provider-admin-secret") || strings.Contains(serialized, "timeout response") {
		t.Fatalf("provider timeout event leaked details: %s", serialized)
	}
}

func TestProviderUsageDailyJobLogDoesNotExposeCredential(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &Service{usage: store, cfg: &Config{Server: ServerConfig{
		ProviderUsageReconciliation: ProviderUsageReconciliationConfig{Enabled: true, Providers: []ProviderUsageReconciliationEntry{{Provider: "primary"}}},
	}}}
	var captured bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&captured)
	defer log.SetOutput(previousWriter)
	service.runProviderUsageReconciliationScheduled(context.Background(), fixedProviderUsageSource{err: errors.New("provider-admin-secret-and-response-body")}, time.Now())
	if strings.Contains(captured.String(), "provider-admin-secret") || strings.Contains(captured.String(), "response-body") {
		t.Fatalf("provider usage log leaked source details: %q", captured.String())
	}
}

func TestProviderUsageClientsFilterKeyAndBoundPagination(t *testing.T) {
	t.Setenv("ROUTER_PROVIDER_USAGE_TEST_TOKEN", "admin-secret-token")
	var openAICalls, anthropicCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer admin-secret-token" {
			openAICalls++
			if got := r.URL.Query().Get("api_key_ids[]"); got != "key-123" {
				t.Errorf("OpenAI API-key filter = %q", got)
			}
			if openAICalls == 1 {
				_, _ = w.Write([]byte(`{"data":[{"results":[{"amount":{"value":1.25,"currency":"usd"}}]}],"has_more":true,"next_page":"page-2"}`))
				return
			}
			if r.URL.Query().Get("page") != "page-2" {
				t.Errorf("OpenAI cursor = %q", r.URL.Query().Get("page"))
			}
			_, _ = w.Write([]byte(`{"data":[{"results":[{"amount":{"value":2.5,"currency":"usd"}}]}],"has_more":false}`))
			return
		}
		if r.Header.Get("x-api-key") != "admin-secret-token" {
			t.Errorf("Anthropic credential header missing")
		}
		anthropicCalls++
		if got := r.URL.Query().Get("api_key_ids[]"); got != "key-123" {
			t.Errorf("Anthropic API-key filter = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"cost":{"amount":"250.5","currency":"USD"}}],"has_more":false}`))
	}))
	defer server.Close()
	client := newProviderUsageClient(ProviderUsageReconciliationConfig{
		MaxPages:  2,
		Providers: []ProviderUsageReconciliationEntry{{Provider: "openai", Kind: "openai", CredentialEnv: "ROUTER_PROVIDER_USAGE_TEST_TOKEN", ProviderKeyID: "key-123"}, {Provider: "anthropic", Kind: "anthropic", CredentialEnv: "ROUTER_PROVIDER_USAGE_TEST_TOKEN", ProviderKeyID: "key-123"}},
	}, nil, server.Client())
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.openAIEndpoint, client.anthropicEndpoint = endpoint.String(), endpoint.String()
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	openAICost, err := client.ProviderCost(context.Background(), "openai", from, from.Add(24*time.Hour))
	if err != nil || openAICost != 3.75 || openAICalls != 2 {
		t.Fatalf("OpenAI cost=%v calls=%d err=%v", openAICost, openAICalls, err)
	}
	anthropicCost, err := client.ProviderCost(context.Background(), "anthropic", from, from.Add(24*time.Hour))
	if err != nil || anthropicCost != 2.505 || anthropicCalls != 1 {
		t.Fatalf("Anthropic cost=%v calls=%d err=%v", anthropicCost, anthropicCalls, err)
	}
}

func TestProviderUsageClientDoesNotExposeHTTPErrorBody(t *testing.T) {
	t.Setenv("ROUTER_PROVIDER_USAGE_TEST_TOKEN", "admin-secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("admin-secret-token internal response details"))
	}))
	defer server.Close()
	client := newProviderUsageClient(ProviderUsageReconciliationConfig{Providers: []ProviderUsageReconciliationEntry{{Provider: "openai", Kind: "openai", CredentialEnv: "ROUTER_PROVIDER_USAGE_TEST_TOKEN", ProviderKeyID: "key-123"}}}, nil, server.Client())
	client.openAIEndpoint = server.URL
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	_, err := client.ProviderCost(context.Background(), "openai", from, from.Add(24*time.Hour))
	if err == nil || strings.Contains(err.Error(), "admin-secret-token") || strings.Contains(err.Error(), "internal response details") {
		t.Fatalf("provider API error leaked: %v", err)
	}
}

func TestProviderUsageClientTimeoutDoesNotExposeCredential(t *testing.T) {
	t.Setenv("ROUTER_PROVIDER_USAGE_TEST_TOKEN", "admin-secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newProviderUsageClient(ProviderUsageReconciliationConfig{Providers: []ProviderUsageReconciliationEntry{{Provider: "openai", Kind: "openai", CredentialEnv: "ROUTER_PROVIDER_USAGE_TEST_TOKEN", ProviderKeyID: "key-123"}}}, nil, &http.Client{Timeout: 5 * time.Millisecond})
	client.openAIEndpoint = server.URL
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	_, err := client.ProviderCost(context.Background(), "openai", from, from.Add(24*time.Hour))
	if err == nil || strings.Contains(err.Error(), "admin-secret-token") || strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("provider API timeout error leaked details: %v", err)
	}
}

type fakeBedrockCloudWatch struct {
	calls int
}

func (f *fakeBedrockCloudWatch) GetMetricData(_ context.Context, input *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.calls++
	if input.StartTime == nil || input.EndTime == nil || len(input.MetricDataQueries) != 2 {
		return nil, fmt.Errorf("unexpected metric input")
	}
	results := make([]cwtypes.MetricDataResult, 0, len(input.MetricDataQueries))
	for _, query := range input.MetricDataQueries {
		if query.MetricStat == nil || query.MetricStat.Period == nil || *query.MetricStat.Period != 86400 || query.MetricStat.Metric == nil || len(query.MetricStat.Metric.Dimensions) != 1 || aws.ToString(query.MetricStat.Metric.Dimensions[0].Value) != "anthropic.claude-3" {
			return nil, fmt.Errorf("unexpected Bedrock metric query")
		}
		value := 1_000_000.0
		if aws.ToString(query.MetricStat.Metric.MetricName) == "OutputTokenCount" {
			value = 500_000
		}
		results = append(results, cwtypes.MetricDataResult{Id: query.Id, StatusCode: cwtypes.StatusCodeComplete, Values: []float64{value}})
	}
	return &cloudwatch.GetMetricDataOutput{MetricDataResults: results}, nil
}

func TestProviderUsageBedrockCloudWatchEstimatesAccountSpend(t *testing.T) {
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	fake := &fakeBedrockCloudWatch{}
	provider := ProviderConfig{Models: map[string]ProviderModel{"claude": {Model: "anthropic.claude-3", InputPricePerMillionUSD: 2, OutputPricePerMillionUSD: 4}}}
	client := newProviderUsageClient(ProviderUsageReconciliationConfig{MaxPages: 2, Providers: []ProviderUsageReconciliationEntry{{Provider: "primary", Kind: "bedrock", AWSRegion: "us-east-1", AWSRoleARN: "arn:aws:iam::123456789012:role/reconciliation"}}}, map[string]ProviderConfig{"primary": provider}, nil)
	client.cloudWatchFactory = func(_ context.Context, entry ProviderUsageReconciliationEntry) (cloudWatchGetMetricDataAPI, error) {
		if entry.AWSRegion != "us-east-1" || entry.AWSRoleARN == "" {
			return nil, fmt.Errorf("Bedrock AWS identity missing")
		}
		return fake, nil
	}
	got, err := client.ProviderCost(context.Background(), "primary", from, to)
	if err != nil || got != 4 || fake.calls != 1 {
		t.Fatalf("Bedrock estimated cost=%v calls=%d err=%v", got, fake.calls, err)
	}
}

type blockingBedrockCloudWatch struct{}

func (blockingBedrockCloudWatch) GetMetricData(ctx context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestProviderUsageBedrockCloudWatchTimeoutIsSafe(t *testing.T) {
	client := newProviderUsageClient(ProviderUsageReconciliationConfig{
		MaxPages:  1,
		Providers: []ProviderUsageReconciliationEntry{{Provider: "primary", Kind: "bedrock", AWSRegion: "us-east-1"}},
	}, map[string]ProviderConfig{"primary": {Models: map[string]ProviderModel{"claude": {Model: "anthropic.claude-3", InputPricePerMillionUSD: 2, OutputPricePerMillionUSD: 4}}}}, nil)
	client.cloudWatchFactory = func(context.Context, ProviderUsageReconciliationEntry) (cloudWatchGetMetricDataAPI, error) {
		return blockingBedrockCloudWatch{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	_, err := client.ProviderCost(ctx, "primary", from, from.Add(24*time.Hour))
	if err == nil || strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Bedrock timeout error leaked details: %v", err)
	}
}

func (s fixedProviderUsageSource) ProviderCost(context.Context, string, time.Time, time.Time) (float64, error) {
	return s.cost, s.err
}

func TestProviderUsageDailyJobReportsGapAndContinuesAfterProviderFailure(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	store.Emit(logRecord{RequestID: "daily-reconcile-router-row", TS: formatUsageTime(dayStart.Add(time.Hour)), TargetProvider: "provider-b", TotalCostUSD: 1, Status: 200})
	svc := &Service{
		usage: store,
		cfg: &Config{Server: ServerConfig{
			AdminReports:                AdminReportsConfig{Security: AdminSecurityReportsConfig{Enabled: true}},
			ProviderUsageReconciliation: ProviderUsageReconciliationConfig{Enabled: true, ThresholdUSD: 1, Providers: []ProviderUsageReconciliationEntry{{Provider: "provider-a"}, {Provider: "provider-b"}}},
		}},
	}
	source := mappedProviderUsageSource{
		"provider-a": {err: errors.New("credential secret and response body")},
		"provider-b": {cost: 10},
	}
	if err := svc.runProviderUsageReconciliation(context.Background(), source, now); err == nil {
		t.Fatal("expected first provider failure to be returned")
	}
	var events []securityAccessEventRecord
	if err := store.db.Where("event_type = ?", "provider_usage_gap").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ModelGroup != "provider-b" {
		t.Fatalf("events after provider failure = %+v", events)
	}
	var failure securityAccessEventRecord
	if err := store.db.Where("event_type = ? AND model_group = ?", "provider_usage_failure", "provider-a").First(&failure).Error; err != nil {
		t.Fatalf("provider failure status event missing: %v", err)
	}
	if failure.StatusCode != 502 || failure.ReasonCode != "provider_usage_source_failed" || failure.Outcome != "failed" {
		t.Fatalf("unexpected provider failure status event: %+v", failure)
	}
	serialized := fmt.Sprintf("%+v", events[0])
	if strings.Contains(serialized, "credential secret") || strings.Contains(serialized, "response body") {
		t.Fatalf("event contains source details or amounts: %s", serialized)
	}
}

func TestProviderUsageReconciliationReportsSyntheticGap(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	store.Emit(logRecord{RequestID: "reconcile-router-row", TS: formatUsageTime(from.Add(time.Hour)), TargetProvider: "openai-key-a", TotalCostUSD: 1.25, Status: 200})
	var signals []providerUsageGap
	reconciler := providerUsageReconciler{
		usage: store, source: fixedProviderUsageSource{cost: 10}, threshold: 2,
		onGap: func(gap providerUsageGap) error { signals = append(signals, gap); return nil },
	}
	reported, err := reconciler.reconcile(context.Background(), "openai-key-a", from, to)
	if err != nil || !reported {
		t.Fatalf("reconcile reported=%t err=%v", reported, err)
	}
	if len(signals) != 1 || signals[0].Provider != "openai-key-a" || signals[0].RouterUSD != 1.25 || signals[0].ProviderUSD != 10 || signals[0].GapUSD != 8.75 {
		t.Fatalf("unexpected synthetic gap signal: %+v", signals)
	}
}

func TestProviderUsageReconciliationNoSignalBelowThreshold(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var called bool
	reconciler := providerUsageReconciler{
		usage: store, source: fixedProviderUsageSource{cost: 1}, threshold: 1,
		onGap: func(providerUsageGap) error { called = true; return nil },
	}
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	reported, err := reconciler.reconcile(context.Background(), "provider", from, from.Add(24*time.Hour))
	if err != nil || reported || called {
		t.Fatalf("reconcile reported=%t callback=%t err=%v", reported, called, err)
	}
}

func TestProviderUsageReconciliationFailureDoesNotLeakSourceError(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reconciler := providerUsageReconciler{usage: store, source: fixedProviderUsageSource{err: errors.New("secret-admin-key response body")}}
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	_, err = reconciler.reconcile(context.Background(), "provider", from, from.Add(24*time.Hour))
	if err == nil || strings.Contains(err.Error(), "secret-admin-key") || strings.Contains(err.Error(), "response body") {
		t.Fatalf("unsafe provider error: %v", err)
	}
}

func TestProviderUsageCredentialIsNotPersisted(t *testing.T) {
	const secret = "provider-admin-secret-test-value"
	t.Setenv("ROUTER_PROVIDER_USAGE_ADMIN_TEST", secret)
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("provider usage credential missing")
		}
		_, _ = w.Write([]byte(`{"data":[{"results":[{"amount":{"value":10,"currency":"usd"}}]}]}`))
	}))
	defer server.Close()
	entry := ProviderUsageReconciliationEntry{Provider: "openai-primary", Kind: "openai", CredentialEnv: "ROUTER_PROVIDER_USAGE_ADMIN_TEST", ProviderKeyID: "key-123"}
	reconciliation := ProviderUsageReconciliationConfig{Enabled: true, ThresholdUSD: 1, Providers: []ProviderUsageReconciliationEntry{entry}}
	client := newProviderUsageClient(reconciliation, nil, server.Client())
	client.openAIEndpoint = server.URL
	store.Emit(logRecord{RequestID: "credential-redaction-router-row", TS: "2026-11-15T12:00:00Z", TargetProvider: entry.Provider, TotalCostUSD: 1, Status: 200})
	svc := &Service{usage: store, cfg: &Config{Provider: map[string]ProviderConfig{}, Server: ServerConfig{ProviderUsageReconciliation: reconciliation}}}
	if err := svc.runProviderUsageReconciliation(context.Background(), client, time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
	var events []securityAccessEventRecord
	if err := store.db.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	var rows []usageRecord
	if err := store.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if serialized := fmt.Sprintf("%+v %+v", events, rows); strings.Contains(serialized, secret) {
		t.Fatal("provider usage credential was persisted")
	}
}

func TestProviderUsageGapWritesSecuritySignalWithoutAmounts(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := &Service{usage: store}
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if err := svc.recordProviderUsageGap(providerUsageGap{Provider: "provider", WindowFrom: from, WindowTo: from.Add(24 * time.Hour), RouterUSD: 1, ProviderUSD: 9, GapUSD: 8}); err != nil {
		t.Fatal(err)
	}
	var event securityAccessEventRecord
	if err := store.db.Where("event_type = ?", "provider_usage_gap").First(&event).Error; err != nil {
		t.Fatalf("provider usage signal missing: %v", err)
	}
	if event.ModelGroup != "provider" || event.ReasonCode != "provider_usage_above_router_usage" || strings.Contains(event.RequestedModel, "9") || strings.Contains(event.RequestedModel, "8") {
		t.Fatalf("unexpected or sensitive provider usage signal: %+v", event)
	}
}
