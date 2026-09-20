// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestTenantDeploymentInstanceIDMatchesLiveStagingOwner(t *testing.T) {
	got := TenantDeploymentInstanceID("staging-fleet-nonprod", "llm-api", "nonproduction")
	if got != "instance-fdcca5e10ce3f145c4a7" {
		t.Fatalf("staging owner = %q", got)
	}
	got = TenantDeploymentInstanceID("metrum-production", "llm-api", "production")
	if got != "instance-2278b384bf563c59df11" {
		t.Fatalf("production owner = %q", got)
	}
}

func TestBuildTenantDeploymentPlanRequiresMatchingStage(t *testing.T) {
	profile, manifest := loadTenantDeploymentFixture(t)
	manifest.Stage = "production"
	if _, err := BuildTenantDeploymentPlan(profile, manifest, "intent-stage"); err == nil || !strings.Contains(err.Error(), "nonproduction profiles require stage") {
		t.Fatalf("expected stage mismatch error, got %v", err)
	}
}

func TestBuildTenantDeploymentPlanOwnershipTransition(t *testing.T) {
	profile, manifest := loadTenantDeploymentFixture(t)
	profile.Environment = "production"
	profile.ProfileID = "metrum-production"
	profile.OwnershipTransition = &TenantOwnershipTransition{
		CustomerID:      "customer-a",
		SourceProfileID: "staging-fleet-nonprod",
		SourceStage:     "nonproduction",
		ChangeReference: "#1052",
		ExpiresAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	manifest.CustomerID = "customer-a"
	manifest.Stage = "production"

	plan, err := BuildTenantDeploymentPlan(profile, manifest, "intent-transition")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0] != "ownership_transition" {
		t.Fatalf("actions = %v", plan.Actions)
	}
	wantSource := TenantDeploymentInstanceID("staging-fleet-nonprod", "customer-a", "nonproduction")
	if plan.SourceInstanceID != wantSource {
		t.Fatalf("source instance = %q want %q", plan.SourceInstanceID, wantSource)
	}
	if plan.OwnershipTransitionChangeReference != "#1052" {
		t.Fatalf("change reference = %q", plan.OwnershipTransitionChangeReference)
	}
	if plan.InstanceID == plan.SourceInstanceID {
		t.Fatal("source and target owners must differ")
	}
}

func TestBuildTenantDeploymentPlanRejectsExpiredOwnershipTransition(t *testing.T) {
	profile, manifest := loadTenantDeploymentFixture(t)
	profile.Environment = "production"
	profile.ProfileID = "metrum-production"
	profile.OwnershipTransition = &TenantOwnershipTransition{
		CustomerID:      "customer-a",
		SourceProfileID: "staging-fleet-nonprod",
		SourceStage:     "nonproduction",
		ChangeReference: "issue-1052",
		ExpiresAt:       time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}
	manifest.CustomerID = "customer-a"
	manifest.Stage = "production"
	if _, err := BuildTenantDeploymentPlanAt(profile, manifest, "intent-expired", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry rejection, got %v", err)
	}
}

func TestBuildTenantDeploymentPlanRejectsWrongTransitionCustomer(t *testing.T) {
	profile, manifest := loadTenantDeploymentFixture(t)
	profile.Environment = "production"
	profile.ProfileID = "metrum-production"
	profile.OwnershipTransition = &TenantOwnershipTransition{
		CustomerID:      "other-customer",
		SourceProfileID: "staging-fleet-nonprod",
		SourceStage:     "nonproduction",
		ChangeReference: "issue-1052",
		ExpiresAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	manifest.CustomerID = "customer-a"
	manifest.Stage = "production"
	if _, err := BuildTenantDeploymentPlan(profile, manifest, "intent-wrong-customer"); err == nil || !strings.Contains(err.Error(), "customer_id") {
		t.Fatalf("expected customer mismatch rejection, got %v", err)
	}
}

func TestFakeOwnershipTransitionDeployPreservesActionOrder(t *testing.T) {
	profile, manifest := loadTenantDeploymentFixture(t)
	profile.Environment = "production"
	profile.ProfileID = "metrum-production"
	profile.OwnershipTransition = &TenantOwnershipTransition{
		CustomerID:      "customer-a",
		SourceProfileID: "staging-fleet-nonprod",
		SourceStage:     "nonproduction",
		ChangeReference: "issue-1052",
		ExpiresAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	manifest.CustomerID = "customer-a"
	manifest.Stage = "production"
	plan, err := BuildTenantDeploymentPlan(profile, manifest, "intent-fake-transition")
	if err != nil {
		t.Fatal(err)
	}

	store, err := OpenTenantDeploymentStore(filepath.Join(t.TempDir(), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fake, adapters := NewFakeTenantDeploymentAdapters()
	engine, err := NewTenantDeploymentEngine(store, adapters)
	if err != nil {
		t.Fatal(err)
	}
	status, err := engine.Deploy(context.Background(), plan, "intent-fake-transition")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != TenantDeploymentReady || status.SourceInstanceID != plan.SourceInstanceID {
		t.Fatalf("status=%+v", status)
	}
	calls := fake.SnapshotCalls()
	if len(calls) == 0 || calls[0] != "ensure:ownership_transition" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestEKSTransitionOwnershipRelabelsAndPreservesPVC(t *testing.T) {
	source := "instance-fdcca5e10ce3f145c4a7"
	target := "instance-2278b384bf563c59df11"
	labels := map[string]string{
		tenantDeploymentOwnerLabel:     source,
		"app.kubernetes.io/name":       "smart-llmrouter",
		"app.kubernetes.io/managed-by": "metrum-fleetctl",
	}
	kube := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "llm-api", Labels: cloneStringMap(labels)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "router-runtime", Namespace: "llm-api", Labels: cloneStringMap(labels)}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "router-state", Namespace: "llm-api", Labels: cloneStringMap(labels)},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "llm-api", Labels: cloneStringMap(labels)},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{tenantDeploymentOwnerLabel: source}},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "router-ingress", Namespace: "llm-api", Labels: cloneStringMap(labels)},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{tenantDeploymentOwnerLabel: source}},
			},
		},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "llm-api", Labels: cloneStringMap(labels)}},
	)
	adapter := &EKSTenantDeploymentAdapters{kube: kube}
	plan := TenantDeploymentPlan{
		InstanceID:                         target,
		Namespace:                          "llm-api",
		SourceInstanceID:                   source,
		OwnershipTransitionChangeReference: "#1052",
	}
	if _, err := adapter.TransitionOwnership(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	ns, err := kube.CoreV1().Namespaces().Get(context.Background(), "llm-api", metav1.GetOptions{})
	if err != nil || ownerOf(ns.Labels) != target {
		t.Fatalf("namespace owner=%q err=%v", ownerOf(ns.Labels), err)
	}
	pvc, err := kube.CoreV1().PersistentVolumeClaims("llm-api").Get(context.Background(), "router-state", metav1.GetOptions{})
	if err != nil || ownerOf(pvc.Labels) != target {
		t.Fatalf("pvc owner=%q err=%v", ownerOf(pvc.Labels), err)
	}
	svc, err := kube.CoreV1().Services("llm-api").Get(context.Background(), "router", metav1.GetOptions{})
	if err != nil || svc.Spec.Selector[tenantDeploymentOwnerLabel] != target {
		t.Fatalf("service selector=%v err=%v", svc.Spec.Selector, err)
	}
	// Resume must accept already-transitioned labels.
	if _, err := adapter.TransitionOwnership(context.Background(), plan); err != nil {
		t.Fatalf("resume transition: %v", err)
	}
}

func TestEKSTransitionOwnershipRejectsForeignOwner(t *testing.T) {
	labels := map[string]string{
		tenantDeploymentOwnerLabel:     "instance-foreign",
		"app.kubernetes.io/name":       "smart-llmrouter",
		"app.kubernetes.io/managed-by": "metrum-fleetctl",
	}
	kube := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "llm-api", Labels: labels}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "router-state", Namespace: "llm-api", Labels: cloneStringMap(labels)},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
		},
	)
	adapter := &EKSTenantDeploymentAdapters{kube: kube}
	_, err := adapter.TransitionOwnership(context.Background(), TenantDeploymentPlan{
		InstanceID:                         "instance-target",
		Namespace:                          "llm-api",
		SourceInstanceID:                   "instance-source",
		OwnershipTransitionChangeReference: "issue-1052",
	})
	if err == nil || !strings.Contains(err.Error(), "ownership_conflict") {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func loadTenantDeploymentFixture(t *testing.T) (TenantDeploymentProfile, TenantDeploymentManifest) {
	t.Helper()
	profile, manifest, _ := tenantDeploymentFixture(t)
	return profile, manifest
}

func TestTransferPredecessorResourcesReassignsUniqueRefs(t *testing.T) {
	store, err := OpenTenantDeploymentStore(filepath.Join(t.TempDir(), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	source := "instance-source"
	target := "instance-target"
	if err := store.db.Create(&tenantDeploymentJobRecord{
		JobID: "job-source", InstanceID: source, IdempotencyKey: "idem-s", ManifestSHA256: "old",
		ProfileID: "p", CustomerID: "c", Stage: "nonproduction", Environment: "nonproduction",
		Region: "us-east-1", ClusterAlias: "metrum", Namespace: "llm-api", Hostname: "llm-api.apps.example.test",
		ReleaseDigest: "d", ResourceProfile: "small", StateProfile: "sqlite-rwo-small", ConfigRevision: "old",
		State: TenantDeploymentReady, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&tenantDeploymentJobRecord{
		JobID: "job-target", InstanceID: target, IdempotencyKey: "idem-t", ManifestSHA256: "new",
		ProfileID: "p2", CustomerID: "c", Stage: "production", Environment: "production",
		Region: "us-east-1", ClusterAlias: "metrum", Namespace: "llm-api", Hostname: "llm-api.apps.example.test",
		ReleaseDigest: "d2", ResourceProfile: "small", StateProfile: "sqlite-rwo-small", ConfigRevision: "new",
		State: TenantDeploymentProvisioning, SourceInstanceID: source, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&tenantDeploymentResourceRecord{
		InstanceID: source, JobID: "job-source", ResourceKind: "namespace", ResourceRef: "namespace/llm-api",
		OwnershipKey: source + ":namespace", DesiredRevision: "old", State: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	engine := &TenantDeploymentEngine{store: store}
	plan := TenantDeploymentPlan{InstanceID: target, SourceInstanceID: source, Namespace: "llm-api"}
	if err := engine.transferPredecessorResources(context.Background(), "job-target", plan); err != nil {
		t.Fatal(err)
	}
	var got tenantDeploymentResourceRecord
	if err := store.db.Where("resource_ref = ?", "namespace/llm-api").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != target || got.JobID != "job-target" || got.DesiredRevision != "" || got.OwnershipKey != target+":namespace" {
		t.Fatalf("got=%+v", got)
	}
}
