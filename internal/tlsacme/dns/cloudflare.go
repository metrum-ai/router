// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// CloudflareAPIBase is the Cloudflare v4 API base URL.
const CloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// CloudflareCredentials is the resolved Cloudflare credential pair. Either a
// scoped API token (preferred) or the global key plus the account email.
type CloudflareCredentials struct {
	APIToken string
	APIKey   string
	Email    string
}

// Cloudflare is the Cloudflare v4 provider.
type Cloudflare struct {
	baseURL string
	client  HTTPDoer
	creds   CloudflareCredentials
	opts    Options

	mu      sync.Mutex
	zoneIDs map[string]string
}

// NewCloudflare builds the provider.
func NewCloudflare(creds CloudflareCredentials, opts Options) (*Cloudflare, error) {
	if creds.APIToken == "" && (creds.APIKey == "" || creds.Email == "") {
		return nil, fmt.Errorf("dns: cloudflare needs an API token or an API key with an email")
	}
	base := strings.TrimSuffix(strings.TrimSpace(opts.Endpoint), "/")
	if base == "" {
		base = CloudflareAPIBase
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Cloudflare{baseURL: base, client: client, creds: creds, opts: opts, zoneIDs: map[string]string{}}, nil
}

// Name implements Provider.
func (c *Cloudflare) Name() string { return "cloudflare" }

type cloudflareResultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

type cloudflareEnvelope struct {
	Success    bool                 `json:"success"`
	Errors     []cloudflareError    `json:"errors"`
	Result     json.RawMessage      `json:"result"`
	ResultInfo cloudflareResultInfo `json:"result_info"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloudflareRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

// Zones lists the zones the credentials can read.
func (c *Cloudflare) Zones(ctx context.Context) ([]string, error) {
	var zones []string
	for page := 1; ; page++ {
		var result []cloudflareZone
		info, err := c.do(ctx, http.MethodGet, "/zones?per_page=50&page="+strconv.Itoa(page), nil, &result)
		if err != nil {
			return nil, err
		}
		for _, zone := range result {
			zones = append(zones, fqdn(zone.Name))
			c.rememberZone(zone.Name, zone.ID)
		}
		if info.TotalPages == 0 || page >= info.TotalPages {
			break
		}
	}
	return zones, nil
}

// Records lists the DNS records of a zone.
func (c *Cloudflare) Records(ctx context.Context, zone string) ([]Record, error) {
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}
	var out []Record
	for page := 1; ; page++ {
		var result []cloudflareRecord
		info, err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records?per_page=100&page="+strconv.Itoa(page), nil, &result)
		if err != nil {
			return nil, err
		}
		for _, record := range result {
			out = append(out, Record{
				ID:    record.ID,
				Type:  strings.ToUpper(record.Type),
				Name:  fqdn(record.Name),
				Value: strings.Trim(record.Content, "\""),
				TTL:   record.TTL,
			})
		}
		if info.TotalPages == 0 || page >= info.TotalPages {
			break
		}
	}
	sortRecords(out)
	return out, nil
}

// Create adds a record and returns its id.
func (c *Cloudflare) Create(ctx context.Context, zone string, record Record) (string, error) {
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"type":    strings.ToUpper(record.Type),
		"name":    fqdn(record.Name),
		"content": record.Value,
		"ttl":     ttlOrDefault(record.TTL),
	}
	var result cloudflareRecord
	if _, err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", body, &result); err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("dns: cloudflare create %s %s returned no record id", record.Type, record.Name)
	}
	return result.ID, nil
}

// Update replaces a record.
func (c *Cloudflare) Update(ctx context.Context, zone string, id string, record Record) error {
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	body := map[string]any{
		"type":    strings.ToUpper(record.Type),
		"name":    fqdn(record.Name),
		"content": record.Value,
		"ttl":     ttlOrDefault(record.TTL),
	}
	_, err = c.do(ctx, http.MethodPut, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), body, nil)
	return err
}

// Delete removes a record.
func (c *Cloudflare) Delete(ctx context.Context, zone string, id string) error {
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, nil)
	return err
}

func (c *Cloudflare) zoneID(ctx context.Context, zone string) (string, error) {
	zone = fqdn(zone)
	c.mu.Lock()
	id := c.zoneIDs[zone]
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	var result []cloudflareZone
	if _, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil, &result); err != nil {
		return "", err
	}
	for _, candidate := range result {
		if fqdn(candidate.Name) == zone && candidate.ID != "" {
			c.rememberZone(candidate.Name, candidate.ID)
			return candidate.ID, nil
		}
	}
	return "", fmt.Errorf("dns: cloudflare zone %q not found for these credentials", zone)
}

func (c *Cloudflare) rememberZone(name, id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	c.zoneIDs[fqdn(name)] = id
	c.mu.Unlock()
}

// do performs one API call and decodes the envelope. Cloudflare signals failure
// with a 2xx and success:false, so both are checked.
func (c *Cloudflare) do(ctx context.Context, method, path string, body any, out any) (cloudflareResultInfo, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.creds.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.creds.APIToken)
	} else {
		req.Header.Set("X-Auth-Email", c.creds.Email)
		req.Header.Set("X-Auth-Key", c.creds.APIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare read response: %w", err)
	}
	var envelope cloudflareEnvelope
	decodeErr := json.Unmarshal(raw, &envelope)
	if resp.StatusCode >= 300 || (decodeErr == nil && !envelope.Success) {
		detail := ""
		if decodeErr == nil {
			messages := make([]string, 0, len(envelope.Errors))
			for _, problem := range envelope.Errors {
				messages = append(messages, fmt.Sprintf("%d %s", problem.Code, problem.Message))
			}
			detail = strings.Join(messages, "; ")
		}
		if detail == "" {
			detail = truncateBody(raw)
		}
		return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare %s %s: status %d: %s", method, path, resp.StatusCode, detail)
	}
	if decodeErr != nil {
		return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare %s %s: decode response: %w", method, path, decodeErr)
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return cloudflareResultInfo{}, fmt.Errorf("dns: cloudflare %s %s: decode result: %w", method, path, err)
		}
	}
	return envelope.ResultInfo, nil
}

// truncateBody limits a provider error body to a short, single-line excerpt.
func truncateBody(raw []byte) string {
	text := strings.Join(strings.Fields(string(raw)), " ")
	if len(text) > 256 {
		text = text[:256] + "..."
	}
	return text
}

// ttlOrDefault maps a zero TTL to the provider default.
func ttlOrDefault(ttl int) int {
	if ttl <= 0 {
		return 1
	}
	return ttl
}
