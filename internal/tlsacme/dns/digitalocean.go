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
)

// DigitalOceanAPIBase is the DigitalOcean v2 API base URL.
const DigitalOceanAPIBase = "https://api.digitalocean.com/v2"

// DigitalOcean is the DigitalOcean v2 provider.
type DigitalOcean struct {
	baseURL string
	client  HTTPDoer
	token   string
	opts    Options
}

// NewDigitalOcean builds the provider.
func NewDigitalOcean(token string, opts Options) (*DigitalOcean, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("dns: digitalocean needs an API token")
	}
	base := strings.TrimSuffix(strings.TrimSpace(opts.Endpoint), "/")
	if base == "" {
		base = DigitalOceanAPIBase
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &DigitalOcean{baseURL: base, client: client, token: token, opts: opts}, nil
}

// Name implements Provider.
func (d *DigitalOcean) Name() string { return "digitalocean" }

type digitalOceanDomain struct {
	Name string `json:"name"`
}

type digitalOceanRecord struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
	TTL  int    `json:"ttl"`
}

type digitalOceanError struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// Zones lists the domains in the account.
func (d *DigitalOcean) Zones(ctx context.Context) ([]string, error) {
	var zones []string
	for page := 1; ; page++ {
		var response struct {
			Domains []digitalOceanDomain `json:"domains"`
			Links   struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		path := "/domains?per_page=200&page=" + strconv.Itoa(page)
		if err := d.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		for _, domain := range response.Domains {
			zones = append(zones, fqdn(domain.Name))
		}
		if response.Links.Pages.Next == "" {
			break
		}
	}
	return zones, nil
}

// Records lists the records of a zone.
func (d *DigitalOcean) Records(ctx context.Context, zone string) ([]Record, error) {
	zone = fqdn(zone)
	var out []Record
	for page := 1; ; page++ {
		var response struct {
			Records []digitalOceanRecord `json:"domain_records"`
			Links   struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		path := "/domains/" + url.PathEscape(zone) + "/records?per_page=200&page=" + strconv.Itoa(page)
		if err := d.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		for _, record := range response.Records {
			out = append(out, Record{
				ID:    strconv.FormatInt(record.ID, 10),
				Type:  strings.ToUpper(record.Type),
				Name:  absoluteName(record.Name, zone),
				Value: strings.Trim(record.Data, "\""),
				TTL:   record.TTL,
			})
		}
		if response.Links.Pages.Next == "" {
			break
		}
	}
	sortRecords(out)
	return out, nil
}

// Create adds a record and returns its id.
func (d *DigitalOcean) Create(ctx context.Context, zone string, record Record) (string, error) {
	zone = fqdn(zone)
	body := map[string]any{
		"type": strings.ToUpper(record.Type),
		"name": relativeName(record.Name, zone),
		"data": record.Value,
		"ttl":  record.TTL,
	}
	if record.TTL <= 0 {
		body["ttl"] = 1800
	}
	var response struct {
		Record digitalOceanRecord `json:"domain_record"`
	}
	if err := d.do(ctx, http.MethodPost, "/domains/"+url.PathEscape(zone)+"/records", body, &response); err != nil {
		return "", err
	}
	if response.Record.ID == 0 {
		return "", fmt.Errorf("dns: digitalocean create %s %s returned no record id", record.Type, record.Name)
	}
	return strconv.FormatInt(response.Record.ID, 10), nil
}

// Update replaces a record.
func (d *DigitalOcean) Update(ctx context.Context, zone string, id string, record Record) error {
	zone = fqdn(zone)
	ttl := record.TTL
	if ttl <= 0 {
		ttl = 1800
	}
	body := map[string]any{
		"type": strings.ToUpper(record.Type),
		"name": relativeName(record.Name, zone),
		"data": record.Value,
		"ttl":  ttl,
	}
	return d.do(ctx, http.MethodPut, "/domains/"+url.PathEscape(zone)+"/records/"+url.PathEscape(id), body, nil)
}

// Delete removes a record.
func (d *DigitalOcean) Delete(ctx context.Context, zone string, id string) error {
	return d.do(ctx, http.MethodDelete, "/domains/"+url.PathEscape(fqdn(zone))+"/records/"+url.PathEscape(id), nil, nil)
}

func (d *DigitalOcean) do(ctx context.Context, method, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("dns: digitalocean encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("dns: digitalocean build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("dns: digitalocean %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("dns: digitalocean read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		detail := truncateBody(raw)
		var problem digitalOceanError
		if json.Unmarshal(raw, &problem) == nil && problem.Message != "" {
			detail = problem.Message
		}
		return fmt.Errorf("dns: digitalocean %s %s: status %d: %s", method, path, resp.StatusCode, detail)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("dns: digitalocean %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}
