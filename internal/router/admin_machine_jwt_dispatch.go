// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type adminMachineJWTBinding struct {
	Authenticator     *adminMachineJWTAuthenticator
	MinimumConstraint string
}
type adminMachineJWTDispatcher struct {
	issuers map[string]adminMachineJWTBinding
	dpop    *adminDPoPValidator
	roots   func() *x509.CertPool
}

func newAdminMachineJWTDispatcher(bindings []adminMachineJWTBinding, dpop *adminDPoPValidator, roots func() *x509.CertPool) (Authenticator, error) {
	if len(bindings) == 0 || len(bindings) > 32 {
		return nil, errors.New("admin JWT issuer count invalid")
	}
	d := &adminMachineJWTDispatcher{issuers: map[string]adminMachineJWTBinding{}, dpop: dpop, roots: roots}
	for _, binding := range bindings {
		a := binding.Authenticator
		if a == nil || a.cfg.Issuer == "" {
			return nil, errors.New("admin JWT validator missing")
		}
		if _, exists := d.issuers[a.cfg.Issuer]; exists {
			return nil, errors.New("admin JWT issuer duplicated")
		}
		if binding.MinimumConstraint == "" {
			binding.MinimumConstraint = "none"
		}
		switch binding.MinimumConstraint {
		case "none":
		case "mtls":
			if roots == nil {
				return nil, errors.New("admin JWT client roots missing")
			}
		case "dpop":
			if dpop == nil {
				return nil, errors.New("admin JWT DPoP validator missing")
			}
		default:
			return nil, errors.New("admin JWT sender constraint invalid")
		}
		d.issuers[a.cfg.Issuer] = binding
	}
	return d, nil
}
func (d *adminMachineJWTDispatcher) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	authorization := adminSingleAuthorization(r)
	if authorization == "" {
		for name := range r.Header {
			if strings.EqualFold(name, "Authorization") {
				return Principal{}, ErrInvalidCredential
			}
		}
		return Principal{}, ErrNoCredential
	}
	fields := strings.Fields(authorization)
	if len(authorization) > maxMachineCredentialBytes || len(fields) != 2 || (!strings.EqualFold(fields[0], "Bearer") && !strings.EqualFold(fields[0], "DPoP")) {
		return Principal{}, ErrInvalidCredential
	}
	raw := fields[1]
	if strings.HasPrefix(raw, adminAPIKeyPrefix) {
		if strings.EqualFold(fields[0], "DPoP") {
			return Principal{}, ErrSenderConstraintFailed
		}
		return Principal{}, ErrNoCredential
	}
	// Unverified iss is ONLY an exact dispatch hint, never identity or authority.
	// Exactly one configured validator then verifies signature and exact issuer.
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	var hint struct {
		Issuer string `json:"iss"`
	}
	if token.UnsafeClaimsWithoutVerification(&hint) != nil || len(hint.Issuer) > 2048 {
		return Principal{}, ErrInvalidCredential
	}
	binding, ok := d.issuers[hint.Issuer]
	if !ok {
		return Principal{}, ErrInvalidCredential
	}
	verified, err := binding.Authenticator.verifyToken(ctx, raw)
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	if len(verified.confirmation) == 0 {
		if binding.MinimumConstraint != "none" || !strings.EqualFold(fields[0], "Bearer") {
			return Principal{}, ErrSenderConstraintFailed
		}
	} else {
		mode, value, ok := adminJWTConfirmation(verified.confirmation)
		if !ok || binding.MinimumConstraint != "none" && binding.MinimumConstraint != mode {
			return Principal{}, ErrSenderConstraintFailed
		}
		switch mode {
		case "mtls":
			if !strings.EqualFold(fields[0], "Bearer") || !validateAdminCertificateBoundToken(ctx, r, d.roots, value) {
				return Principal{}, ErrSenderConstraintFailed
			}
		case "dpop":
			// RFC 9449 requires the DPoP scheme for a token bound to a DPoP key
			// thumbprint. The scheme is checked here, before the proof validator,
			// so a Bearer presentation can never reach (or consume) proof state.
			if !strings.EqualFold(fields[0], "DPoP") || d.dpop == nil || d.dpop.Validate(ctx, r, raw, value) != nil {
				return Principal{}, ErrSenderConstraintFailed
			}
		default:
			return Principal{}, ErrSenderConstraintFailed
		}
	}
	if ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return verified.principal, nil
}

// Exactly one recognized, nonempty string binding; unknown, duplicate, null,
// combined or trailing representations fail closed even when signed.
func adminJWTConfirmation(raw []byte) (string, string, bool) {
	if len(raw) == 0 || len(raw) > 1024 {
		return "", "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", "", false
	}
	mode, value := "", ""
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil || mode != "" {
			return "", "", false
		}
		switch key {
		case "jkt":
			mode = "dpop"
		case "x5t#S256":
			mode = "mtls"
		default:
			return "", "", false
		}
		if decoder.Decode(&value) != nil || value == "" || len(value) > 256 {
			return "", "", false
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || mode == "" {
		return "", "", false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", "", false
	}
	return mode, value, true
}
