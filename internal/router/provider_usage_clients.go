// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	providerUsageMaxResponseBytes = 1 << 20
	providerUsageMaxPages         = 100
	providerUsageDefaultPages     = 20
	openAIOrganizationCostsURL    = "https://api.openai.com/v1/organization/costs"
	anthropicAnalyticsCostsURL    = "https://api.anthropic.com/v1/organizations/analytics/cost"
	anthropicAnalyticsVersion     = "2026-10-21"
	anthropicAnalyticsBeta        = "analytics-usage-cost-2026-08-24"
)

type providerUsageClient struct {
	entries           map[string]ProviderUsageReconciliationEntry
	providers         map[string]ProviderConfig
	maxPages          int
	httpClient        *http.Client
	openAIEndpoint    string
	anthropicEndpoint string
	cloudWatchFactory func(context.Context, ProviderUsageReconciliationEntry) (cloudWatchGetMetricDataAPI, error)
	httpsOnly         bool
}

func newProviderUsageClient(cfg ProviderUsageReconciliationConfig, providers map[string]ProviderConfig, client *http.Client) *providerUsageClient {
	entries := make(map[string]ProviderUsageReconciliationEntry, len(cfg.Providers))
	for _, entry := range cfg.Providers {
		entries[strings.TrimSpace(entry.Provider)] = entry
	}
	maxPages := cfg.MaxPages
	if maxPages == 0 {
		maxPages = providerUsageDefaultPages
	}
	httpsOnly := client == nil
	if client == nil {
		timeout := cfg.TimeoutMS
		if timeout == 0 {
			timeout = 10000
		}
		client = newEgressHTTPClientWithOptions(egressHTTPClientOptions{Timeout: time.Duration(timeout) * time.Millisecond, AllowHosts: []string{"api.openai.com", "api.anthropic.com"}, Label: "provider usage", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }})
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &providerUsageClient{entries: entries, providers: providers, maxPages: maxPages, httpClient: client, openAIEndpoint: openAIOrganizationCostsURL, anthropicEndpoint: anthropicAnalyticsCostsURL, cloudWatchFactory: newBedrockCloudWatchClient, httpsOnly: httpsOnly}
}

type providerUsageWatermarkTransport struct {
	base      http.RoundTripper
	watermark *time.Time
}

func (t providerUsageWatermarkTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, providerUsageMaxResponseBytes+1))
	_ = response.Body.Close()
	if readErr != nil || len(body) > providerUsageMaxResponseBytes {
		return nil, errors.New("provider usage response could not be read")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) == nil && len(fields["data_refreshed_at"]) > 0 {
		var stamp string
		if json.Unmarshal(fields["data_refreshed_at"], &stamp) != nil {
			return nil, errors.New("provider usage refresh watermark is invalid")
		}
		parsed, parseErr := time.Parse(time.RFC3339, stamp)
		if parseErr != nil {
			return nil, errors.New("provider usage refresh watermark is invalid")
		}
		if t.watermark.IsZero() || parsed.Before(*t.watermark) {
			*t.watermark = parsed
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func (c *providerUsageClient) ProviderCostWithWatermark(ctx context.Context, provider string, from, to time.Time) (ProviderUsageResult, error) {
	clone := *c
	watermark := time.Time{}
	base := c.httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient := *c.httpClient
	httpClient.Transport = providerUsageWatermarkTransport{base: base, watermark: &watermark}
	clone.httpClient = &httpClient
	cost, err := clone.ProviderCost(ctx, provider, from, to)
	return ProviderUsageResult{CostUSD: cost, DataRefreshedAt: watermark}, err
}

func (c *providerUsageClient) ProviderCost(ctx context.Context, provider string, from, to time.Time) (float64, error) {
	entry, ok := c.entries[provider]
	if !ok {
		return 0, errors.New("provider usage source is not configured")
	}
	switch strings.ToLower(strings.TrimSpace(entry.Kind)) {
	case "openai":
		credential, ok := os.LookupEnv(strings.TrimSpace(entry.CredentialEnv))
		if !ok || strings.TrimSpace(credential) == "" {
			return 0, errors.New("provider usage credential is unavailable")
		}
		return c.openAICost(ctx, credential, entry.ProviderKeyID, from, to)
	case "anthropic":
		credential, ok := os.LookupEnv(strings.TrimSpace(entry.CredentialEnv))
		if !ok || strings.TrimSpace(credential) == "" {
			return 0, errors.New("provider usage credential is unavailable")
		}
		return c.anthropicCost(ctx, credential, entry.ProviderKeyID, from, to)
	case "bedrock":
		return c.bedrockCost(ctx, entry, from, to)
	default:
		return 0, errors.New("provider usage API is unavailable for this provider kind")
	}
}

func (c *providerUsageClient) openAICost(ctx context.Context, credential, keyID string, from, to time.Time) (float64, error) {
	page := ""
	total := 0.0
	for pageIndex := 0; pageIndex < c.maxPages; pageIndex++ {
		parsed, err := url.Parse(c.openAIEndpoint)
		if err != nil {
			return 0, errors.New("provider usage endpoint is invalid")
		}
		if c.httpsOnly && parsed.Scheme != "https" {
			return 0, errors.New("provider usage endpoint must use HTTPS")
		}
		query := parsed.Query()
		query.Set("start_time", strconv.FormatInt(from.Unix(), 10))
		query.Set("end_time", strconv.FormatInt(to.Unix(), 10))
		query.Set("bucket_width", "1d")
		query.Set("limit", "180")
		query.Add("api_key_ids[]", keyID)
		if page != "" {
			query.Set("page", page)
		}
		parsed.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return 0, errors.New("provider usage request could not be created")
		}
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("Content-Type", "application/json")
		response, err := c.httpClient.Do(request)
		if err != nil {
			return 0, errors.New("provider usage request failed")
		}
		body, readErr := readProviderUsageBody(response)
		if readErr != nil {
			return 0, readErr
		}
		var result openAICostResponse
		if json.Unmarshal(body, &result) != nil {
			return 0, errors.New("provider usage response is invalid")
		}
		for _, bucket := range result.Data {
			for _, row := range bucket.Results {
				if !strings.EqualFold(row.Amount.Currency, "usd") || math.IsNaN(row.Amount.Value) || math.IsInf(row.Amount.Value, 0) || row.Amount.Value < 0 {
					return 0, errors.New("provider usage response contains an invalid amount")
				}
				total += row.Amount.Value
			}
		}
		if !result.HasMore {
			return total, nil
		}
		if strings.TrimSpace(result.NextPage) == "" {
			return 0, errors.New("provider usage pagination cursor is missing")
		}
		page = result.NextPage
	}
	return 0, errors.New("provider usage page limit exceeded")
}

func (c *providerUsageClient) anthropicCost(ctx context.Context, credential, keyID string, from, to time.Time) (float64, error) {
	page := ""
	total := 0.0
	for pageIndex := 0; pageIndex < c.maxPages; pageIndex++ {
		parsed, err := url.Parse(c.anthropicEndpoint)
		if err != nil {
			return 0, errors.New("provider usage endpoint is invalid")
		}
		if c.httpsOnly && parsed.Scheme != "https" {
			return 0, errors.New("provider usage endpoint must use HTTPS")
		}
		query := parsed.Query()
		query.Set("starting_at", from.Format(time.RFC3339))
		query.Set("ending_at", to.Format(time.RFC3339))
		query.Set("time_bucket_width", "1d")
		query.Set("limit", "1000")
		query.Add("api_key_ids[]", keyID)
		if page != "" {
			query.Set("page", page)
		}
		parsed.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return 0, errors.New("provider usage request could not be created")
		}
		request.Header.Set("x-api-key", credential)
		request.Header.Set("anthropic-version", anthropicAnalyticsVersion)
		request.Header.Set("anthropic-beta", anthropicAnalyticsBeta)
		response, err := c.httpClient.Do(request)
		if err != nil {
			return 0, errors.New("provider usage request failed")
		}
		body, readErr := readProviderUsageBody(response)
		if readErr != nil {
			return 0, readErr
		}
		var result anthropicCostResponse
		if json.Unmarshal(body, &result) != nil {
			return 0, errors.New("provider usage response is invalid")
		}
		for _, row := range result.Data {
			if !strings.EqualFold(row.Cost.Currency, "usd") {
				return 0, errors.New("provider usage response contains an invalid currency")
			}
			amount, err := strconv.ParseFloat(row.Cost.Amount, 64)
			if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
				return 0, errors.New("provider usage response contains an invalid amount")
			}
			// Anthropic reports fractional cents; convert to USD.
			total += amount / 100
		}
		if !result.HasMore {
			return total, nil
		}
		if strings.TrimSpace(result.NextPage) == "" {
			return 0, errors.New("provider usage pagination cursor is missing")
		}
		page = result.NextPage
	}
	return 0, errors.New("provider usage page limit exceeded")
}

type openAICostResponse struct {
	Data []struct {
		Results []struct {
			Amount struct {
				Value    float64 `json:"value"`
				Currency string  `json:"currency"`
			} `json:"amount"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

type anthropicCostResponse struct {
	Data []struct {
		Cost struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"cost"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

func readProviderUsageBody(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("provider usage API returned a non-success status")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, providerUsageMaxResponseBytes+1))
	if err != nil || len(body) > providerUsageMaxResponseBytes {
		return nil, errors.New("provider usage response could not be read")
	}
	return body, nil
}
