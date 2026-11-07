// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Principal is an authenticated machine identity, not an authorization grant.
// Subject is an explicitly mapped policy subject. CredentialID and Issuer must
// contain only public identifiers, never a credential or stored verifier.
// Kind is mtls, oauth, federated, adminkey, or emergency. Every kind, including
// emergency, must pass through the admin Authorizer before an operation runs.
type Principal struct {
	Subject      string
	Kind         string
	CredentialID string
	Issuer       string
}

type principalContextKey struct{}

// WithPrincipal returns a derived context carrying an authenticated principal.
// Only authentication middleware should call this after successful validation.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFrom retrieves the machine identity without consulting legacy
// browser sessions, Basic credentials, or serving-port authentication state.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

var (
	// ErrNoCredential permits trying another mechanism. A mechanism must return
	// this only when its credential is absent, never when validation fails.
	ErrNoCredential = errors.New("authentication_required")
	// ErrInvalidCredential is terminal. It intentionally contains no credential
	// details and can safely cross the authentication/error-rendering boundary.
	ErrInvalidCredential = errors.New("invalid_credential")
	// ErrUnsupportedAuthentication rejects browser and ambiguous credentials on
	// the machine-only listener. Serving-port authentication is unchanged.
	ErrUnsupportedAuthentication = errors.New("unsupported_authentication")
)

// Authenticator validates a request credential and resolves its mapped identity.
// It does not authorize access. Invalid presented credentials must return an
// error other than ErrNoCredential; errors must never contain raw credentials.
type Authenticator interface {
	Authenticate(context.Context, *http.Request) (Principal, error)
}

// EmergencyCredentialType distinguishes emergency key and certificate
// mechanisms without mixing credential transport metadata into Principal.
type EmergencyCredentialType string

const (
	EmergencyCertificate EmergencyCredentialType = "certificate"
	EmergencyBearerKey   EmergencyCredentialType = "bearer_key"
)

type emergencyCredentialAuthenticator struct {
	Authenticator
	credentialType EmergencyCredentialType
}

// WithEmergencyCredentialType declares the single credential class validated
// by an emergency mechanism. The wrapped mechanism must return success only
// after validating that class, including activation and expiry. Unknown types
// fail closed. Emergency results without this wrapper also fail closed: Kind
// alone cannot distinguish a certificate from a bearer key.
func WithEmergencyCredentialType(a Authenticator, kind EmergencyCredentialType) Authenticator {
	return &emergencyCredentialAuthenticator{Authenticator: a, credentialType: kind}
}

func (a *emergencyCredentialAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if a.Authenticator == nil || (a.credentialType != EmergencyCertificate && a.credentialType != EmergencyBearerKey) {
		return Principal{}, ErrInvalidCredential
	}
	// The declaration must correspond to a presented credential. It is static
	// registration metadata, never inferred from some other request credential.
	if a.credentialType == EmergencyCertificate && (r.TLS == nil || len(r.TLS.PeerCertificates) == 0) {
		return Principal{}, ErrNoCredential
	}
	if a.credentialType == EmergencyBearerKey && bearerTokenFromRequest(r) == "" {
		return Principal{}, ErrNoCredential
	}
	p, err := a.Authenticator.Authenticate(ctx, r)
	if err == nil && p.Kind != "emergency" {
		return Principal{}, ErrInvalidCredential
	}
	return p, err
}

type authenticatorChain struct{ mechanisms []Authenticator }

// NewAuthenticatorChain creates a machine-only ordered authenticator chain.
// The first matching identity wins, but every presented credential is checked:
// a valid credential cannot mask another invalid one. Sender-constrained JWT
// mechanisms must validate the certificate/proof themselves. A presented TLS
// certificate also requires a successful mtls/certificate-only emergency
// validator OR typed RFC 8705 possession evidence from the successful token
// mechanism. After typed token-bound possession or a declared certificate
// emergency proof/Use succeeds, only concrete pure mTLS identity mappers are
// redundant and skipped. Other validators,
// especially emergency activation/Use acknowledgements, are never skipped.
// Possession evidence never requires an additional SAN identity.
// Register the token mechanism first when its identity
// should win over the certificate identity. Validators must return their actual
// credential kind; this is how the chain checks credential coverage.
// The chain is opt-in for the new listener and never changes legacy handlers.
func NewAuthenticatorChain(mechanisms ...Authenticator) Authenticator {
	return &authenticatorChain{mechanisms: append([]Authenticator(nil), mechanisms...)}
}

func (c *authenticatorChain) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	if err := validateMachineAuthEnvelope(r); err != nil {
		return Principal{}, err
	}
	var first Principal
	found := false
	var authorization string
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") && len(values) == 1 {
			authorization = values[0]
		}
	}
	authorizationFields := strings.Fields(authorization)
	dpopPresented := len(authorizationFields) > 0 && strings.EqualFold(authorizationFields[0], "DPoP")
	certificatePresented := r.TLS != nil && len(r.TLS.PeerCertificates) > 0
	certificateValidated, authorizationValidated := false, false
	certificateIdentityRedundant := false
	for _, mechanism := range c.mechanisms {
		if ctx.Err() != nil || mechanism == nil {
			return Principal{}, ErrInvalidCredential
		}
		if certificateIdentityRedundant && adminPureCertificateIdentity(mechanism) {
			continue
		}
		stepCtx, certificateCoverage := newAdminCertificateCoverageContext(ctx)
		p, err := mechanism.Authenticate(stepCtx, r)
		switch {
		case err == ErrNoCredential:
			continue
		case errors.Is(err, ErrSenderConstraintFailed):
			return Principal{}, ErrSenderConstraintFailed
		case err != nil:
			return Principal{}, ErrInvalidCredential
		}
		if !validMachinePrincipal(p) {
			return Principal{}, ErrInvalidCredential
		}
		if certificateCoverage.consume(p, r) {
			certificateValidated = true
			certificateIdentityRedundant = true
		}
		switch p.Kind {
		case "mtls":
			certificateValidated = true
		case "oauth", "federated":
			authorizationValidated = true
		case "adminkey":
			if !dpopPresented {
				authorizationValidated = true
			}
		case "emergency":
			declared, ok := mechanism.(*emergencyCredentialAuthenticator)
			if !ok {
				return Principal{}, ErrInvalidCredential
			}
			switch declared.credentialType {
			case EmergencyCertificate:
				certificateValidated = true
				// This declared certificate mechanism has already proved native
				// possession and durably acknowledged Use. Do not require a
				// separate normal SAN identity, nor skip any other obligation.
				certificateIdentityRedundant = true
			case EmergencyBearerKey:
				if dpopPresented {
					return Principal{}, ErrInvalidCredential
				}
				authorizationValidated = true
			default:
				return Principal{}, ErrInvalidCredential
			}
		}
		if !found {
			first = p
			found = true
		}
	}
	if ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	if authorization != "" && !authorizationValidated || certificatePresented && !certificateValidated {
		return Principal{}, ErrInvalidCredential
	}
	if !found {
		return Principal{}, ErrNoCredential
	}
	return first, nil
}

func validMachinePrincipal(p Principal) bool {
	if strings.TrimSpace(p.Subject) == "" || strings.TrimSpace(p.Subject) == securityAdministratorRole {
		return false
	}
	switch p.Kind {
	case "mtls", "oauth", "federated", "adminkey", "emergency":
		return true
	}
	return false
}

// Bound work before passing attacker-controlled tokens to any parser. These
// limits are authentication limits, not replacements for listener header limits.
const maxMachineCredentialBytes = 16 * 1024

func validateMachineAuthEnvelope(r *http.Request) error {
	var auth []string
	proofs := 0
	for name, values := range r.Header {
		switch {
		case strings.EqualFold(name, "Cookie"):
			return ErrUnsupportedAuthentication
		case strings.EqualFold(name, "Authorization"):
			auth = append(auth, values...)
		case strings.EqualFold(name, "DPoP"):
			proofs += len(values)
			if proofs != 1 || len(values) != 1 || len(values[0]) == 0 || len(values[0]) > maxMachineCredentialBytes {
				return ErrInvalidCredential
			}
		}
	}
	if r.URL != nil {
		// URL.Query returns valid pairs even when another pair is malformed.
		query := r.URL.Query()
		for name := range query {
			switch strings.ToLower(name) {
			case "access_token", "api_key", "token":
				return ErrUnsupportedAuthentication
			}
		}
		if r.URL.User != nil {
			return ErrUnsupportedAuthentication
		}
	}
	if len(auth) > 1 {
		return ErrUnsupportedAuthentication
	}
	if len(auth) == 0 {
		if proofs != 0 {
			return ErrInvalidCredential
		}
		return nil
	}
	if len(auth[0]) > maxMachineCredentialBytes {
		return ErrInvalidCredential
	}
	parts := strings.Fields(auth[0])
	if len(parts) != 2 || (!strings.EqualFold(parts[0], "Bearer") && !strings.EqualFold(parts[0], "DPoP")) {
		return ErrUnsupportedAuthentication
	}
	if strings.EqualFold(parts[0], "DPoP") != (proofs == 1) {
		return ErrInvalidCredential
	}
	return nil
}
