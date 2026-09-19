// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFleetInventoryTracksTenantsAndLicenseBindings(t *testing.T) {
	store, _, engine, _ := openTenantDeploymentTestEngine(t)
	profile, manifest, plan := tenantDeploymentFixture(t)
	_ = profile

	status, err := engine.Deploy(context.Background(), plan, "intent-inventory")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != TenantDeploymentReady {
		t.Fatalf("state=%s", status.State)
	}

	tenants, err := store.ListFleetTenants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 1 || tenants[0].CustomerID != plan.CustomerID || tenants[0].LifecycleState != FleetTenantActive {
		t.Fatalf("tenants=%+v", tenants)
	}
	if plan.LicenseRefDigest == "" || plan.LicenseValidityHours < 1 {
		t.Fatalf("plan missing safe license binding fields: %+v", plan)
	}
	tenant, err := store.GetFleetTenant(context.Background(), plan.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tenant.Instances) != 1 || tenant.Instances[0].InstanceID != plan.InstanceID {
		t.Fatalf("instances=%+v", tenant.Instances)
	}
	encoded, _ := json.Marshal(tenant)
	for _, forbidden := range []string{manifest.License.RequestRef, "aws-ssm:///", "aws-secretsmanager:///"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("tenant inventory leaked protected ref %q: %s", forbidden, encoded)
		}
	}

	var binding FleetLicenseBindingRecord
	if err := store.db.Where("instance_id = ?", plan.InstanceID).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding.BindingState != FleetLicenseBindingBound {
		t.Fatalf("binding=%+v", binding)
	}
	if binding.RequestRefDigest != plan.LicenseRefDigest {
		t.Fatalf("digest mismatch %s vs %s", binding.RequestRefDigest, plan.LicenseRefDigest)
	}

	approval := TenantDeletionApproval{
		APIVersion:     TenantDeletionApprovalAPIVersion,
		Action:         "delete",
		JobID:          plan.JobID,
		Nonce:          "nonce-inventory-delete",
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		RetainPVC:      false,
		RetainDatabase: false,
		authenticated:  true,
	}
	deleted, err := engine.Delete(context.Background(), plan, approval, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.State != TenantDeploymentDeleted {
		t.Fatalf("deleted state=%s", deleted.State)
	}
	tenant, err = store.GetFleetTenant(context.Background(), plan.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.LifecycleState != FleetTenantDeleted || tenant.LatestJobState != TenantDeploymentDeleted {
		t.Fatalf("deleted tenant=%+v", tenant)
	}
	if err := store.db.Where("instance_id = ?", plan.InstanceID).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding.BindingState != FleetLicenseBindingRetired {
		t.Fatalf("retired binding=%+v", binding)
	}
}

func TestFleetInventorySyncFromLegacyJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	store, err := OpenTenantDeploymentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	job := tenantDeploymentJobRecord{
		JobID: "job-legacy", InstanceID: "instance-legacy", IdempotencyKey: "idem-legacy",
		ManifestSHA256: strings.Repeat("a", 64), ProfileID: "profile-a", CustomerID: "customer-a",
		Stage: "test", Environment: "nonproduction", Region: "us-east-1", ClusterAlias: "cluster-a",
		Namespace: "customer-a", Hostname: "customer-a.example.test", ReleaseDigest: "sha256:" + strings.Repeat("b", 64),
		ResourceProfile: "small", StateProfile: "sqlite-rwo-small", ComputeProfile: "t3a.medium",
		ConfigRevision: "r1", State: TenantDeploymentReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	count, err := store.SyncFleetInventoryFromJobs(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("sync count=%d err=%v", count, err)
	}
	tenants, err := store.ListFleetTenants(context.Background())
	if err != nil || len(tenants) != 1 || tenants[0].CustomerID != "customer-a" {
		t.Fatalf("tenants=%+v err=%v", tenants, err)
	}
}
