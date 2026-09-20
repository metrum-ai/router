// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

const (
	FleetTenantActive  = "active"
	FleetTenantDeleted = "deleted"

	FleetLicenseBindingIntended = "intended"
	FleetLicenseBindingBound    = "bound"
	FleetLicenseBindingRetired  = "retired"

	FleetDatabaseModeSQLite       = "sqlite"
	FleetDatabaseModeDedicatedRDS = "dedicated-rds"
)

// FleetTenantRecord is the operator inventory row for one customer_id in a Fleet registry.
type FleetTenantRecord struct {
	CustomerID        string    `gorm:"primaryKey;column:customer_id;type:text" json:"customer_id"`
	ProfileID         string    `gorm:"column:profile_id;type:text;not null" json:"profile_id"`
	Stage             string    `gorm:"column:stage;type:text;not null" json:"stage"`
	Environment       string    `gorm:"column:environment;type:text;not null" json:"environment"`
	Namespace         string    `gorm:"column:namespace;type:text;not null" json:"namespace"`
	Hostname          string    `gorm:"column:hostname;type:text;not null" json:"hostname"`
	PrimaryInstanceID string    `gorm:"column:primary_instance_id;type:text;not null" json:"primary_instance_id"`
	LifecycleState    string    `gorm:"column:lifecycle_state;type:text;not null" json:"lifecycle_state"`
	LatestJobID       string    `gorm:"column:latest_job_id;type:text;not null" json:"latest_job_id"`
	LatestJobState    string    `gorm:"column:latest_job_state;type:text;not null" json:"latest_job_state"`
	CreatedAt         time.Time `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (FleetTenantRecord) TableName() string { return "fleet_tenants" }

// FleetTenantInstanceRecord tracks one instance identity under a tenant.
type FleetTenantInstanceRecord struct {
	InstanceID     string    `gorm:"primaryKey;column:instance_id;type:text" json:"instance_id"`
	CustomerID     string    `gorm:"index;column:customer_id;type:text;not null" json:"customer_id"`
	ProfileID      string    `gorm:"column:profile_id;type:text;not null" json:"profile_id"`
	Stage          string    `gorm:"column:stage;type:text;not null" json:"stage"`
	Environment    string    `gorm:"column:environment;type:text;not null" json:"environment"`
	Region         string    `gorm:"column:region;type:text;not null" json:"region"`
	ClusterAlias   string    `gorm:"column:cluster_alias;type:text;not null" json:"cluster_alias"`
	Namespace      string    `gorm:"column:namespace;type:text;not null" json:"namespace"`
	Hostname       string    `gorm:"column:hostname;type:text;not null" json:"hostname"`
	CurrentJobID   string    `gorm:"column:current_job_id;type:text;not null" json:"current_job_id"`
	ReleaseDigest  string    `gorm:"column:release_digest;type:text;not null" json:"release_digest"`
	ConfigRevision string    `gorm:"column:config_revision;type:text;not null" json:"config_revision"`
	ComputeProfile string    `gorm:"column:compute_profile;type:text;not null" json:"compute_profile"`
	DatabaseMode   string    `gorm:"column:database_mode;type:text;not null" json:"database_mode"`
	LifecycleState string    `gorm:"column:lifecycle_state;type:text;not null" json:"lifecycle_state"`
	CreatedAt      time.Time `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (FleetTenantInstanceRecord) TableName() string { return "fleet_tenant_instances" }

// FleetLicenseBindingRecord records deploy-time license_ref binding state for an instance/job.
type FleetLicenseBindingRecord struct {
	ID                  uint      `gorm:"primaryKey;column:id" json:"id"`
	CustomerID          string    `gorm:"index;column:customer_id;type:text;not null" json:"customer_id"`
	InstanceID          string    `gorm:"uniqueIndex:instance_license_binding;column:instance_id;type:text;not null" json:"instance_id"`
	JobID               string    `gorm:"column:job_id;type:text;not null" json:"job_id"`
	LicenseID           string    `gorm:"index;column:license_id;type:text;not null" json:"license_id"`
	BindingState        string    `gorm:"column:binding_state;type:text;not null" json:"binding_state"`
	DesiredRevision     string    `gorm:"column:desired_revision;type:text;not null" json:"desired_revision"`
	ValidityHours       int       `gorm:"column:validity_hours;not null" json:"validity_hours"`
	RequestRefDigest    string    `gorm:"column:request_ref_digest;type:text;not null" json:"request_ref_digest"`
	ObservedSecretAlias string    `gorm:"column:observed_secret_alias;type:text;not null" json:"observed_secret_alias"`
	CreatedAt           time.Time `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (FleetLicenseBindingRecord) TableName() string { return "fleet_license_bindings" }

// FleetTenantView is the safe CLI/list projection for one tenant.
type FleetTenantView struct {
	Schema            string                    `json:"schema"`
	CustomerID        string                    `json:"customer_id"`
	ProfileID         string                    `json:"profile_id"`
	Stage             string                    `json:"stage"`
	Environment       string                    `json:"environment"`
	Namespace         string                    `json:"namespace"`
	Hostname          string                    `json:"hostname"`
	PrimaryInstanceID string                    `json:"primary_instance_id"`
	LifecycleState    string                    `json:"lifecycle_state"`
	LatestJobID       string                    `json:"latest_job_id"`
	LatestJobState    string                    `json:"latest_job_state"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
	Instances         []FleetTenantInstanceView `json:"instances,omitempty"`
}

// FleetTenantInstanceView is a safe instance projection.
type FleetTenantInstanceView struct {
	InstanceID     string    `json:"instance_id"`
	CurrentJobID   string    `json:"current_job_id"`
	ReleaseDigest  string    `json:"release_digest"`
	ConfigRevision string    `json:"config_revision"`
	ComputeProfile string    `json:"compute_profile"`
	DatabaseMode   string    `json:"database_mode"`
	LifecycleState string    `json:"lifecycle_state"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func fleetInventoryDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS fleet_tenants (
			customer_id TEXT PRIMARY KEY,
			profile_id TEXT NOT NULL,
			stage TEXT NOT NULL,
			environment TEXT NOT NULL,
			namespace TEXT NOT NULL,
			hostname TEXT NOT NULL,
			primary_instance_id TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL,
			latest_job_id TEXT NOT NULL,
			latest_job_state TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS fleet_tenant_instances (
			instance_id TEXT PRIMARY KEY,
			customer_id TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			stage TEXT NOT NULL,
			environment TEXT NOT NULL,
			region TEXT NOT NULL,
			cluster_alias TEXT NOT NULL,
			namespace TEXT NOT NULL,
			hostname TEXT NOT NULL,
			current_job_id TEXT NOT NULL,
			release_digest TEXT NOT NULL,
			config_revision TEXT NOT NULL,
			compute_profile TEXT NOT NULL,
			database_mode TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY(customer_id) REFERENCES fleet_tenants(customer_id) ON UPDATE CASCADE ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_tenant_instances_customer_id ON fleet_tenant_instances(customer_id)`,
		`CREATE TABLE IF NOT EXISTS fleet_license_bindings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			customer_id TEXT NOT NULL,
			instance_id TEXT NOT NULL UNIQUE,
			job_id TEXT NOT NULL,
			license_id TEXT NOT NULL,
			binding_state TEXT NOT NULL,
			desired_revision TEXT NOT NULL,
			validity_hours INTEGER NOT NULL,
			request_ref_digest TEXT NOT NULL,
			observed_secret_alias TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_license_bindings_customer_id ON fleet_license_bindings(customer_id)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_license_bindings_license_id ON fleet_license_bindings(license_id)`,
	}
}

func (s *TenantDeploymentStore) upsertFleetInventoryFromPlan(ctx context.Context, plan TenantDeploymentPlan, jobState string) error {
	if s == nil || s.db == nil {
		return errors.New("deployment registry is not open")
	}
	now := time.Now().UTC()
	databaseMode := FleetDatabaseModeSQLite
	if strings.TrimSpace(plan.DatabaseID) != "" {
		databaseMode = FleetDatabaseModeDedicatedRDS
	}
	lifecycle := FleetTenantActive
	if jobState == TenantDeploymentDeleted {
		lifecycle = FleetTenantDeleted
	}
	tenant := FleetTenantRecord{
		CustomerID: plan.CustomerID, ProfileID: plan.ProfileID, Stage: plan.Stage, Environment: plan.Environment,
		Namespace: plan.Namespace, Hostname: plan.Hostname, PrimaryInstanceID: plan.InstanceID,
		LifecycleState: lifecycle, LatestJobID: plan.JobID, LatestJobState: jobState,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "customer_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"profile_id": plan.ProfileID, "stage": plan.Stage, "environment": plan.Environment,
			"namespace": plan.Namespace, "hostname": plan.Hostname, "primary_instance_id": plan.InstanceID,
			"lifecycle_state": lifecycle, "latest_job_id": plan.JobID, "latest_job_state": jobState, "updated_at": now,
		}),
	}).Create(&tenant).Error; err != nil {
		return fmt.Errorf("upsert fleet tenant: %w", err)
	}

	instance := FleetTenantInstanceRecord{
		InstanceID: plan.InstanceID, CustomerID: plan.CustomerID, ProfileID: plan.ProfileID, Stage: plan.Stage,
		Environment: plan.Environment, Region: plan.Region, ClusterAlias: plan.ClusterAlias,
		Namespace: plan.Namespace, Hostname: plan.Hostname, CurrentJobID: plan.JobID,
		ReleaseDigest: plan.ReleaseDigest, ConfigRevision: plan.ConfigRevision, ComputeProfile: plan.ComputeProfile,
		DatabaseMode: databaseMode, LifecycleState: lifecycle, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "instance_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"customer_id": plan.CustomerID, "profile_id": plan.ProfileID, "stage": plan.Stage, "environment": plan.Environment,
			"region": plan.Region, "cluster_alias": plan.ClusterAlias, "namespace": plan.Namespace, "hostname": plan.Hostname,
			"current_job_id": plan.JobID, "release_digest": plan.ReleaseDigest, "config_revision": plan.ConfigRevision,
			"compute_profile": plan.ComputeProfile, "database_mode": databaseMode, "lifecycle_state": lifecycle, "updated_at": now,
		}),
	}).Create(&instance).Error; err != nil {
		return fmt.Errorf("upsert fleet tenant instance: %w", err)
	}

	validityHours := plan.LicenseValidityHours
	if validityHours < 0 {
		return errors.New("license validity hours must be non-negative")
	}
	bindingState := FleetLicenseBindingIntended
	if jobState == TenantDeploymentReady {
		bindingState = FleetLicenseBindingBound
	}
	if jobState == TenantDeploymentDeleted {
		bindingState = FleetLicenseBindingRetired
	}
	binding := FleetLicenseBindingRecord{
		CustomerID: plan.CustomerID, InstanceID: plan.InstanceID, JobID: plan.JobID,
		LicenseID: "", BindingState: bindingState, DesiredRevision: plan.ManifestSHA256,
		ValidityHours: validityHours, RequestRefDigest: plan.LicenseRefDigest,
		ObservedSecretAlias: "secret/router-license", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "instance_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"customer_id": plan.CustomerID, "job_id": plan.JobID, "binding_state": bindingState,
			"desired_revision": plan.ManifestSHA256, "validity_hours": validityHours,
			"request_ref_digest": plan.LicenseRefDigest, "observed_secret_alias": "secret/router-license",
			"updated_at": now,
		}),
	}).Create(&binding).Error; err != nil {
		return fmt.Errorf("upsert fleet license binding: %w", err)
	}
	return nil
}

func parseLicenseValidityHours(validity string) (int, error) {
	validity = strings.TrimSpace(validity)
	if validity == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(validity)
	if err != nil {
		return 0, fmt.Errorf("license validity is not a duration")
	}
	hours := int(d / time.Hour)
	if hours < 1 {
		hours = 1
	}
	return hours, nil
}

// ListFleetTenants returns all tenants recorded in this registry (operator inventory, not cloud scan).
func (s *TenantDeploymentStore) ListFleetTenants(ctx context.Context) ([]FleetTenantView, error) {
	var rows []FleetTenantRecord
	if err := s.db.WithContext(ctx).Order("customer_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]FleetTenantView, 0, len(rows))
	for _, row := range rows {
		out = append(out, fleetTenantViewFromRecord(row))
	}
	return out, nil
}

// GetFleetTenant returns one tenant with instances.
func (s *TenantDeploymentStore) GetFleetTenant(ctx context.Context, customerID string) (FleetTenantView, error) {
	if err := validateDeploymentID("customer_id", customerID); err != nil {
		return FleetTenantView{}, err
	}
	var row FleetTenantRecord
	if err := s.db.WithContext(ctx).Where("customer_id = ?", customerID).First(&row).Error; err != nil {
		return FleetTenantView{}, err
	}
	view := fleetTenantViewFromRecord(row)
	var instances []FleetTenantInstanceRecord
	if err := s.db.WithContext(ctx).Where("customer_id = ?", customerID).Order("instance_id ASC").Find(&instances).Error; err != nil {
		return FleetTenantView{}, err
	}
	for _, instance := range instances {
		view.Instances = append(view.Instances, FleetTenantInstanceView{
			InstanceID: instance.InstanceID, CurrentJobID: instance.CurrentJobID,
			ReleaseDigest: instance.ReleaseDigest, ConfigRevision: instance.ConfigRevision,
			ComputeProfile: instance.ComputeProfile, DatabaseMode: instance.DatabaseMode,
			LifecycleState: instance.LifecycleState, UpdatedAt: instance.UpdatedAt,
		})
	}
	return view, nil
}

func fleetTenantViewFromRecord(row FleetTenantRecord) FleetTenantView {
	return FleetTenantView{
		Schema:     "metrum.ai/smartrouter-fleet-tenant/v1",
		CustomerID: row.CustomerID, ProfileID: row.ProfileID, Stage: row.Stage, Environment: row.Environment,
		Namespace: row.Namespace, Hostname: row.Hostname, PrimaryInstanceID: row.PrimaryInstanceID,
		LifecycleState: row.LifecycleState, LatestJobID: row.LatestJobID, LatestJobState: row.LatestJobState,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func digestProtectedRef(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// SyncFleetInventoryFromJobs rebuilds tenant/instance inventory from existing job rows
// for registries created before inventory tables existed.
func (s *TenantDeploymentStore) SyncFleetInventoryFromJobs(ctx context.Context) (int, error) {
	var jobs []tenantDeploymentJobRecord
	if err := s.db.WithContext(ctx).Order("updated_at ASC").Find(&jobs).Error; err != nil {
		return 0, err
	}
	count := 0
	for _, job := range jobs {
		plan := TenantDeploymentPlan{
			JobID: job.JobID, InstanceID: job.InstanceID, ProfileID: job.ProfileID, CustomerID: job.CustomerID,
			Stage: job.Stage, Environment: job.Environment, Region: job.Region, ClusterAlias: job.ClusterAlias,
			Namespace: job.Namespace, Hostname: job.Hostname, ReleaseDigest: job.ReleaseDigest,
			ConfigRevision: job.ConfigRevision, ComputeProfile: job.ComputeProfile, ManifestSHA256: job.ManifestSHA256,
		}
		if err := s.upsertFleetInventoryFromPlan(ctx, plan, job.State); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
