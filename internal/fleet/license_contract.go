// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package fleet

// License wire DTOs remain here for Fleet inventory until Phase B removes
// license inventory. Runtime licensing no longer uses these types.

const LicenseIssuer = "self-managed"

type LicenseLimits struct {
	MaxModelGroups        int      `json:"max_model_groups,omitempty"`
	MaxCallers            int      `json:"max_callers,omitempty"`
	MaxMonthlyRequests    int      `json:"max_monthly_requests,omitempty"`
	MaxTotalTokens        int64    `json:"max_total_tokens,omitempty"`
	MaxTotalRequests      int64    `json:"max_total_requests,omitempty"`
	WindowTokens          int64    `json:"window_tokens,omitempty"`
	WindowRequests        int64    `json:"window_requests,omitempty"`
	WindowDurationSeconds int64    `json:"window_duration_seconds,omitempty"`
	MaxConcurrent         int      `json:"max_concurrent,omitempty"`
	MaxAdmins             int      `json:"max_admins,omitempty"`
	MaxRetentionDays      int      `json:"max_retention_days,omitempty"`
	MaxInstances          int      `json:"max_instances,omitempty"`
	AllowedSkins          []string `json:"allowed_skins,omitempty"`
}

type LicenseDeployment struct {
	Mode                        string   `json:"mode,omitempty"`
	DeploymentID                string   `json:"deployment_id,omitempty" yaml:"deployment_id,omitempty"`
	AllowedEnvironments         []string `json:"allowed_environments,omitempty"`
	InstanceFingerprintRequired bool     `json:"instance_fingerprint_required,omitempty"`
	InstanceFingerprint         string   `json:"instance_fingerprint,omitempty"`
	AllowedInstances            []string `json:"allowed_instances,omitempty"`
	BindingVersion              int      `json:"binding_version,omitempty" yaml:"binding_version,omitempty"`
	AllowedInstanceFingerprints []string `json:"allowed_instance_fingerprints,omitempty" yaml:"allowed_instance_fingerprints,omitempty"`
	MaxOfflineInstances         int      `json:"max_offline_instances,omitempty" yaml:"max_offline_instances,omitempty"`
}

type LicenseSafeSummary struct {
	SchemaVersion int               `json:"schema_version"`
	LicenseID     string            `json:"license_id"`
	CustomerID    string            `json:"customer_id"`
	CustomerName  string            `json:"customer_name,omitempty"`
	Product       string            `json:"product"`
	SKU           string            `json:"sku"`
	Features      []string          `json:"features"`
	Limits        LicenseLimits     `json:"limits,omitempty"`
	Deployment    LicenseDeployment `json:"deployment,omitempty"`
	IssuedAt      string            `json:"issued_at"`
	NotBefore     string            `json:"not_before"`
	ExpiresAt     string            `json:"expires_at"`
	GraceUntil    string            `json:"grace_until,omitempty"`
	KeyID         string            `json:"key_id"`
	Issuer        string            `json:"issuer"`
}
