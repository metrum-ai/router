// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// AdminMachineAuthenticationConfig owns the machine listener's complete schema.
// These descriptors contain no raw secrets. Decoding is not runtime installation;
// constructors must validate bounds, resolve verifier references and build one
// complete immutable generation before publication.
type AdminMachineAuthenticationConfig struct {
	MTLS      AdminMTLSTrustConfig        `yaml:"mtls" json:"mtls"`
	Issuers   []AdminMachineIssuerConfig  `yaml:"issuers" json:"issuers,omitempty"`
	JWKS      AdminMachineJWKSConfig      `yaml:"jwks" json:"jwks"`
	DPoP      AdminMachineDPoPConfig      `yaml:"dpop" json:"dpop"`
	APIKeys   AdminMachineAPIKeysConfig   `yaml:"api_keys" json:"apiKeys"`
	Emergency AdminMachineEmergencyConfig `yaml:"emergency" json:"emergency"`
}

type AdminMachineIssuerConfig struct {
	IssuerURL            string                         `yaml:"issuer_url" json:"issuerURL"`
	Audience             string                         `yaml:"audience" json:"audience"`
	Kind                 string                         `yaml:"kind" json:"kind,omitempty"`
	Algorithms           []string                       `yaml:"algorithms" json:"algorithms,omitempty"`
	MaxLifetime          string                         `yaml:"max_lifetime" json:"maxLifetime,omitempty"`
	ClockSkew            string                         `yaml:"clock_skew" json:"clockSkew,omitempty"`
	Subjects             []AdminMachineJWTSubjectConfig `yaml:"subjects" json:"subjects"`
	SenderConstraint     string                         `yaml:"sender_constraint" json:"senderConstraint,omitempty"`
	AllowedJWKSHosts     []string                       `yaml:"allowed_jwks_hosts" json:"allowedJWKSHosts,omitempty"`
	AllowPrivateNetwork  bool                           `yaml:"allow_private_network" json:"allowPrivateNetwork,omitempty"`
	AllowLegacyTokenType bool                           `yaml:"allow_legacy_token_type" json:"allowLegacyTokenType,omitempty"`
}
type AdminMachineJWTSubjectConfig struct {
	Claim   string `yaml:"claim" json:"claim"`
	Value   string `yaml:"value" json:"value"`
	Subject string `yaml:"subject" json:"subject"`
}
type AdminMachineJWKSConfig struct {
	CacheTTL           string `yaml:"cache_ttl" json:"cacheTTL,omitempty"`
	RefreshMinInterval string `yaml:"refresh_min_interval" json:"refreshMinInterval,omitempty"`
	RequestTimeout     string `yaml:"request_timeout" json:"requestTimeout,omitempty"`
}
type AdminMachineDPoPConfig struct {
	ProofMaxAge    string `yaml:"proof_max_age" json:"proofMaxAge,omitempty"`
	ReplayCapacity int    `yaml:"replay_capacity" json:"replayCapacity,omitempty"`
}
type AdminMachineAPIKeysConfig struct {
	// Nil means the approved default true; an explicit false remains distinct.
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
	// API-issued key lifetimes. Configured keys keep v4.7 behavior.
	MaxLifetime            string `yaml:"max_lifetime" json:"maxLifetime,omitempty"`
	DefaultRotationOverlap string `yaml:"default_rotation_overlap" json:"defaultRotationOverlap,omitempty"`
	MaxRotationOverlap     string `yaml:"max_rotation_overlap" json:"maxRotationOverlap,omitempty"`
}
type AdminMachineEmergencyConfig struct {
	Credentials []AdminMachineEmergencyCredentialConfig `yaml:"credentials" json:"credentials,omitempty"`
}

// AdminMachineEmergencyCredentialConfig is trusted offline enrollment metadata.
// Kind selects api_key or certificate; key_hash_env names a PBKDF2 verifier,
// certificate_sha256 is the exact raw-URL SHA256 leaf-DER pin. Neither activates
// a credential. IssuedAt/ExpiresAt are absolute RFC3339 times, bounded at runtime.
type AdminMachineEmergencyCredentialConfig struct {
	ID                string `yaml:"id" json:"id"`
	Subject           string `yaml:"subject" json:"subject"`
	Issuer            string `yaml:"issuer" json:"issuer"`
	Kind              string `yaml:"kind" json:"kind"`
	KeyHashEnv        string `yaml:"key_hash_env" json:"keyHashEnv,omitempty"`
	CertificateSHA256 string `yaml:"certificate_sha256" json:"certificateSHA256,omitempty"`
	IssuedAt          string `yaml:"issued_at" json:"issuedAt"`
	ExpiresAt         string `yaml:"expires_at" json:"expiresAt"`
}
