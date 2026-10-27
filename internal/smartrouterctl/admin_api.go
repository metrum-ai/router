// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Admin API HTTP client for metrum-ai-routerctl (#395). The CLI drives the
// Admin API over HTTP with the dedicated admin key from #350; it never opens
// the config database directly in --api mode and never accepts the admin key
// as a flag value (environment variable or mode-0600 file only).

package smartrouterctl

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// AdminAPIClient is a minimal authenticated Admin API client.
type AdminAPIClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// ResolveAdminCredential reads the admin key from fileFlag, then the
// ROUTER_ADMIN_KEY environment variable, then ROUTER_ADMIN_KEY_FILE. The key
// is never taken from a plain flag value.
func ResolveAdminCredential(fileFlag string) (string, error) {
	if strings.TrimSpace(fileFlag) != "" {
		raw, err := os.ReadFile(strings.TrimSpace(fileFlag))
		if err != nil {
			return "", fmt.Errorf("read admin key file: %w", err)
		}
		key := strings.TrimSpace(string(raw))
		if key == "" {
			return "", fmt.Errorf("admin key file is empty")
		}
		return key, nil
	}
	if key := strings.TrimSpace(os.Getenv("ROUTER_ADMIN_KEY")); key != "" {
		return key, nil
	}
	if path := strings.TrimSpace(os.Getenv("ROUTER_ADMIN_KEY_FILE")); path != "" {
		return ResolveAdminCredential(path)
	}
	return "", fmt.Errorf("admin credential required: set ROUTER_ADMIN_KEY, ROUTER_ADMIN_KEY_FILE, or --admin-key-file")
}

// NewAdminAPIClient builds the client from --router-url / ROUTER_URL plus the
// resolved admin credential.
func NewAdminAPIClient(routerURL, keyFile string) (*AdminAPIClient, error) {
	base := strings.TrimSpace(routerURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("ROUTER_URL"))
	}
	if base == "" {
		return nil, fmt.Errorf("router URL required: --router-url or ROUTER_URL")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("router URL %q is invalid", base)
	}
	token, err := ResolveAdminCredential(keyFile)
	if err != nil {
		return nil, err
	}
	return &AdminAPIClient{
		BaseURL: strings.TrimRight(base, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// AdminAPIError is one typed Admin API error response.
type AdminAPIError struct {
	Status  int
	Type    string
	Message string
	Raw     string
}

func (e *AdminAPIError) Error() string {
	return fmt.Sprintf("admin API %d %s: %s", e.Status, e.Type, e.Message)
}

// Do issues one Admin API call. On a non-2xx status it returns an
// AdminAPIError with the typed error body. The response body is returned
// raw on success. The admin token never appears in errors.
func (c *AdminAPIClient) Do(method, path string, body any, headers map[string]string) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "metrum-ai-routerctl")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("admin API request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		apiErr := &AdminAPIError{Status: resp.StatusCode, Type: "error", Raw: string(raw)}
		var parsed struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Error.Type != "" {
			apiErr.Type = parsed.Error.Type
			apiErr.Message = parsed.Error.Message
		}
		return nil, resp.StatusCode, apiErr
	}
	return raw, resp.StatusCode, nil
}

// NewIdempotencyKey returns a random idempotency key for a mutating call.
func NewIdempotencyKey() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	return "routerctl-" + hex.EncodeToString(raw)
}

// ParseGroupTargetFlag parses "provider/model_ref" (or "provider/model") CLI
// flags; the provider name ends at the first slash so catalog model refs may
// contain slashes.
func ParseGroupTargetFlag(raw string) (provider, ref string, err error) {
	raw = strings.TrimSpace(raw)
	provider, ref, ok := strings.Cut(raw, "/")
	if !ok || provider == "" || ref == "" {
		return "", "", fmt.Errorf("invalid target %q: use provider/model_ref", raw)
	}
	return provider, ref, nil
}
