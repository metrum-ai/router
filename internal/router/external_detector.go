// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

const externalDetectorDefaultTimeout = 500 * time.Millisecond

type externalDetector struct {
	url    string
	client *http.Client
}

func newExternalDetector(endpoint string, allowHosts []string, allowHTTP bool, timeout time.Duration) (*externalDetector, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Path != "/v1/detect" {
		return nil, errors.New("invalid detector URL")
	}
	if err := validateEgressURL(u, allowHosts, allowHTTP, "detector"); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = externalDetectorDefaultTimeout
	}
	return &externalDetector{url: u.String(), client: newEgressHTTPClient(timeout, allowHosts, allowHTTP, "detector")}, nil
}

func (d *externalDetector) Detect(ctx context.Context, event detector.Event) ([]detector.Signal, error) {
	if detector.ValidateEvent(event) != nil {
		return nil, errors.New("invalid detector event")
	}
	body, err := json.Marshal(event)
	if err != nil || len(body) > detector.MaxRequestBytes {
		return nil, errors.New("invalid detector event")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid detector request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("detector request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("detector returned HTTP %d", resp.StatusCode)
	}
	signals, err := detector.DecodeSignals(io.LimitReader(resp.Body, detector.MaxRequestBytes+1))
	if err != nil {
		return nil, errors.New("invalid detector response")
	}
	return signals, nil
}
