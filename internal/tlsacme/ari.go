// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// errARIUnsupported means the CA directory does not advertise ARI.
var errARIUnsupported = errors.New("ari: CA does not advertise renewal information")

// ariClient reads RFC 9773 renewal information. It is best effort: the manager
// falls back to the two-thirds renewal ratio for every error.
type ariClient struct {
	httpClient *http.Client
}

type ariDirectory struct {
	RenewalInfo string `json:"renewalInfo"`
}

type ariResponse struct {
	SuggestedWindow struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"suggestedWindow"`
	ExplanationURL string `json:"explanationURL"`
}

// window returns the CA's suggested renewal window for leaf. The ARI URL is
// directory.renewalInfo + "/" + certID per RFC 9773.
func (a *ariClient) window(ctx context.Context, directoryURL string, leaf *x509.Certificate) (time.Time, time.Time, error) {
	if a == nil || a.httpClient == nil {
		return time.Time{}, time.Time{}, errors.New("ari: no HTTP client")
	}
	certID, err := ariCertID(leaf)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var directory ariDirectory
	if err := a.getJSON(ctx, directoryURL, &directory); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if strings.TrimSpace(directory.RenewalInfo) == "" {
		return time.Time{}, time.Time{}, errARIUnsupported
	}
	var response ariResponse
	url := strings.TrimSuffix(directory.RenewalInfo, "/") + "/" + certID
	if err := a.getJSON(ctx, url, &response); err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := response.SuggestedWindow.Start
	end := response.SuggestedWindow.End
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("ari: malformed suggested window")
	}
	if !leaf.NotAfter.IsZero() && end.After(leaf.NotAfter) {
		end = leaf.NotAfter
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("ari: suggested window outside the certificate lifetime")
	}
	return start, end, nil
}

func (a *ariClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("ari: GET %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out); err != nil {
		return fmt.Errorf("ari: decode %s: %w", url, err)
	}
	return nil
}

// ariCertID builds the RFC 9773 certificate identifier:
// base64url(AKI keyIdentifier) "." base64url(DER INTEGER value bytes).
func ariCertID(leaf *x509.Certificate) (string, error) {
	if leaf == nil {
		return "", errors.New("ari: nil certificate")
	}
	if len(leaf.AuthorityKeyId) == 0 {
		return "", errors.New("ari: certificate has no authority key identifier")
	}
	serial := derIntegerBytes(leaf.SerialNumber)
	return base64.RawURLEncoding.EncodeToString(leaf.AuthorityKeyId) + "." + base64.RawURLEncoding.EncodeToString(serial), nil
}

// derIntegerBytes returns the DER content octets of a positive INTEGER: the
// minimal big-endian value, with a leading zero when the high bit is set.
func derIntegerBytes(n *big.Int) []byte {
	if n == nil {
		return []byte{0}
	}
	raw := n.Bytes()
	if len(raw) == 0 {
		return []byte{0}
	}
	if raw[0]&0x80 != 0 {
		return append([]byte{0}, raw...)
	}
	return raw
}
