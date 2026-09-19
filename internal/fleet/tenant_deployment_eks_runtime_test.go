// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestEKSRuntimeBindingCreatesExactConfigAndEnvironmentSecret(t *testing.T) {
	const configYAML = "server:\n  listen: :8080\nproviders: {}\n"
	const envJSON = `{"PROVIDER_API_KEY":"synthetic-test-value"}`
	const runtimeRef = "aws-secretsmanager:///safe/runtime-bundle"
	plan := TenantDeploymentPlan{InstanceID: "instance-a", Namespace: "tenant-a", runtimeBundleRef: runtimeRef}
	adapter := &EKSTenantDeploymentAdapters{
		kube: k8sfake.NewSimpleClientset(),
		resolveReference: func(_ context.Context, ref string) ([]byte, error) {
			if ref != runtimeRef {
				t.Fatalf("runtime resolver ref = %q", ref)
			}
			return protectedRuntimeBundle(t, configYAML, envJSON), nil
		},
	}
	if ref, err := adapter.EnsureSecretBinding(context.Background(), plan); err != nil || ref != tenantDeploymentResourceRef(plan.Namespace, "secret", "router-runtime") {
		t.Fatalf("runtime binding ref=%q err=%v", ref, err)
	}
	secret, err := adapter.kube.CoreV1().Secrets(plan.Namespace).Get(context.Background(), "router-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.Data) != 2 {
		t.Fatal("runtime secret does not contain exactly config.yaml and env.json")
	}
	if string(secret.Data["env.json"]) != envJSON {
		t.Fatal("SQLite path must preserve env.json except stripped usage DSN")
	}
	if !strings.Contains(string(secret.Data["config.yaml"]), "driver: sqlite") ||
		!strings.Contains(string(secret.Data["config.yaml"]), "migration_policy: auto-safe") ||
		!strings.Contains(string(secret.Data["config.yaml"]), tenantDeploymentSQLiteUsageDBPath) {
		t.Fatalf("SQLite path must rewrite usage_db: %s", secret.Data["config.yaml"])
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(planJSON), runtimeRef) || strings.Contains(string(planJSON), "synthetic-test-value") {
		t.Fatalf("runtime reference or contents leaked into plan: %s", planJSON)
	}
}

func TestEKSRuntimeBindingRejectsInvalidBundleWithoutCreatingSecret(t *testing.T) {
	const canary = "synthetic-runtime-secret-must-not-escape"
	plan := TenantDeploymentPlan{InstanceID: "instance-a", Namespace: "tenant-a", runtimeBundleRef: "aws-ssm:///safe/runtime-bundle"}
	adapter := &EKSTenantDeploymentAdapters{
		kube: k8sfake.NewSimpleClientset(),
		resolveReference: func(context.Context, string) ([]byte, error) {
			return []byte(`{"config.yaml":"server: {}","env.json":"{}","extra":"` + canary + `"}`), nil
		},
	}
	_, err := adapter.EnsureSecretBinding(context.Background(), plan)
	if err != errInvalidTenantDeploymentRuntimeBundle || strings.Contains(err.Error(), canary) {
		t.Fatalf("invalid bundle error leaked protected data: %v", err)
	}
	secrets, listErr := adapter.kube.CoreV1().Secrets(plan.Namespace).List(context.Background(), metav1.ListOptions{})
	if listErr != nil || len(secrets.Items) != 0 {
		t.Fatalf("invalid bundle created runtime secret: items=%d err=%v", len(secrets.Items), listErr)
	}
}

func TestEKSRuntimeBindingEnforcesProfileRequiredAdminReports(t *testing.T) {
	const runtimeRef = "aws-secretsmanager:///safe/runtime-bundle"
	plan := TenantDeploymentPlan{
		InstanceID:          "instance-a",
		Namespace:           "tenant-a",
		runtimeBundleRef:    runtimeRef,
		requireAdminReports: true,
	}
	adapter := &EKSTenantDeploymentAdapters{
		kube: k8sfake.NewSimpleClientset(),
		resolveReference: func(context.Context, string) ([]byte, error) {
			return protectedRuntimeBundle(t, "server:\n  listen: :8080\n", "{}"), nil
		},
	}
	_, err := adapter.EnsureSecretBinding(context.Background(), plan)
	var adapterErr *TenantDeploymentAdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Class != "runtime_bundle_policy_failed" {
		t.Fatalf("missing required admin reports err=%v", err)
	}
	secrets, listErr := adapter.kube.CoreV1().Secrets(plan.Namespace).List(context.Background(), metav1.ListOptions{})
	if listErr != nil || len(secrets.Items) != 0 {
		t.Fatalf("policy failure created runtime secret: items=%d err=%v", len(secrets.Items), listErr)
	}

	const enabledConfig = `server:
  admin_auth:
    basic:
      enabled: true
    authorization:
      enabled: true
  admin_reports:
    enabled: true
    path_prefix: /admin/reports
`
	adapter.resolveReference = func(context.Context, string) ([]byte, error) {
		return protectedRuntimeBundle(t, enabledConfig, "{}"), nil
	}
	if _, err := adapter.EnsureSecretBinding(context.Background(), plan); err != nil {
		t.Fatalf("valid required admin reports config rejected: %v", err)
	}
}

// TestEKSRuntimeBindingEnforcesProfileAdminReportsProxyCIDRs reproduces the
// 2026-09-08 production defect: admin reports and Basic Auth were enabled, but
// the bundle trusted a local kind/Docker range instead of the cluster ingress
// network, so every admin request was answered with a Basic challenge whether
// or not the password was correct.
func TestEKSRuntimeBindingEnforcesProfileAdminReportsProxyCIDRs(t *testing.T) {
	const runtimeRef = "aws-secretsmanager:///safe/runtime-bundle"
	adminConfig := func(trusted string) string {
		return `server:
  admin_auth:
    basic:
      enabled: true
      allow_insecure_http: false
      trusted_proxy_cidrs:
        - ` + trusted + `
    authorization:
      enabled: true
  admin_reports:
    enabled: true
    path_prefix: /admin/reports
`
	}
	plan := TenantDeploymentPlan{
		InstanceID:             "instance-a",
		Namespace:              "tenant-a",
		runtimeBundleRef:       runtimeRef,
		requireAdminReports:    true,
		adminReportsProxyCIDRs: []string{"192.168.0.0/16"},
	}
	adapter := &EKSTenantDeploymentAdapters{
		kube: k8sfake.NewSimpleClientset(),
		resolveReference: func(context.Context, string) ([]byte, error) {
			return protectedRuntimeBundle(t, adminConfig("172.18.0.0/16"), "{}"), nil
		},
	}
	_, err := adapter.EnsureSecretBinding(context.Background(), plan)
	var adapterErr *TenantDeploymentAdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Class != "runtime_bundle_policy_failed" {
		t.Fatalf("untrusted reverse proxy network accepted: err=%v", err)
	}
	secrets, listErr := adapter.kube.CoreV1().Secrets(plan.Namespace).List(context.Background(), metav1.ListOptions{})
	if listErr != nil || len(secrets.Items) != 0 {
		t.Fatalf("policy failure created runtime secret: items=%d err=%v", len(secrets.Items), listErr)
	}

	// A narrower trusted range than the approved proxy network still leaves
	// part of the ingress fleet untrusted.
	adapter.resolveReference = func(context.Context, string) ([]byte, error) {
		return protectedRuntimeBundle(t, adminConfig("192.168.121.0/24"), "{}"), nil
	}
	if _, err := adapter.EnsureSecretBinding(context.Background(), plan); !errors.As(err, &adapterErr) || adapterErr.Class != "runtime_bundle_policy_failed" {
		t.Fatalf("partially trusted reverse proxy network accepted: err=%v", err)
	}

	adapter.resolveReference = func(context.Context, string) ([]byte, error) {
		return protectedRuntimeBundle(t, adminConfig("192.168.0.0/16"), "{}"), nil
	}
	if _, err := adapter.EnsureSecretBinding(context.Background(), plan); err != nil {
		t.Fatalf("trusted reverse proxy network rejected: %v", err)
	}

	// allow_insecure_http bypasses the forwarded-HTTPS gate entirely, so the
	// proxy range is not a precondition for reaching the password check.
	insecure := `server:
  admin_auth:
    basic:
      enabled: true
      allow_insecure_http: true
    authorization:
      enabled: true
  admin_reports:
    enabled: true
    path_prefix: /admin/reports
`
	adapter.resolveReference = func(context.Context, string) ([]byte, error) {
		return protectedRuntimeBundle(t, insecure, "{}"), nil
	}
	if _, err := adapter.EnsureSecretBinding(context.Background(), plan); err != nil {
		t.Fatalf("allow_insecure_http config rejected: %v", err)
	}
}

func TestEKSRuntimeBindingSanitizesResolverErrors(t *testing.T) {
	const canary = "synthetic-runtime-secret-must-not-escape"
	adapter := &EKSTenantDeploymentAdapters{
		kube: k8sfake.NewSimpleClientset(),
		resolveReference: func(context.Context, string) ([]byte, error) {
			return nil, errors.New(canary)
		},
	}
	_, err := adapter.EnsureSecretBinding(context.Background(), TenantDeploymentPlan{InstanceID: "instance-a", Namespace: "tenant-a", runtimeBundleRef: "aws-ssm:///safe/runtime-bundle"})
	if err == nil || strings.Contains(err.Error(), canary) || err.Error() != "read protected runtime bundle" {
		t.Fatalf("unsafe runtime resolver error: %v", err)
	}
}

func TestEKSRouterDeploymentMountsRuntimeBundleAtImageConfigPath(t *testing.T) {
	plan := TenantDeploymentPlan{InstanceID: "instance-a", Namespace: "tenant-a", ReleaseDigest: "example/router@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	adapter := &EKSTenantDeploymentAdapters{kube: k8sfake.NewSimpleClientset()}
	if _, err := adapter.EnsureRouter(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	deployment, err := adapter.kube.AppsV1().Deployments(plan.Namespace).Get(context.Background(), "router", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	mounted := false
	for _, mount := range container.VolumeMounts {
		if mount.Name == "runtime" {
			mounted = mount.MountPath == "/app/config" && mount.ReadOnly
		}
	}
	if !mounted {
		t.Fatalf("router runtime mount = %#v, want read-only /app/config", container.VolumeMounts)
	}
	runtimeVolume := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "runtime" && volume.Secret != nil && volume.Secret.SecretName == "router-runtime" {
			runtimeVolume = true
		}
		if volume.Name == "license" {
			t.Fatalf("router deployment must not mount license volume after licensing removal: %#v", volume)
		}
	}
	if !runtimeVolume {
		t.Fatalf("router deployment secret volumes runtime=%t", runtimeVolume)
	}
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("router deployment strategy=%q want Recreate", deployment.Spec.Strategy.Type)
	}
}

func TestEKSRouterDeploymentRecreateStrategyOnUpdate(t *testing.T) {
	plan := TenantDeploymentPlan{InstanceID: "instance-a", Namespace: "tenant-a", ReleaseDigest: "example/router@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	adapter := &EKSTenantDeploymentAdapters{kube: k8sfake.NewSimpleClientset()}
	if _, err := adapter.EnsureRouter(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	client := adapter.kube.AppsV1().Deployments(plan.Namespace)
	deployment, err := client.Get(context.Background(), "router", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rolling := appsv1.RollingUpdateDeploymentStrategyType
	deployment.Spec.Strategy.Type = rolling
	if _, err := client.Update(context.Background(), deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	plan.ReleaseDigest = "example/router@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := adapter.EnsureRouter(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	updated, err := client.Get(context.Background(), "router", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("updated strategy=%q want Recreate", updated.Spec.Strategy.Type)
	}
}

func TestEKSLicenseBindingIsNoOpAfterLicensingRemoval(t *testing.T) {
	plan := TenantDeploymentPlan{InstanceID: "instance-a", Namespace: "tenant-a", licenseRequestRef: "aws-ssm:///safe/license-request"}
	adapter := &EKSTenantDeploymentAdapters{
		kube: k8sfake.NewSimpleClientset(),
		resolveReference: func(_ context.Context, ref string) ([]byte, error) {
			t.Fatalf("license resolver must not run after licensing removal; ref=%q", ref)
			return nil, nil
		},
	}
	if _, err := adapter.EnsureLicenseBinding(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := adapter.DeleteLicenseBinding(context.Background(), plan, ""); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.kube.CoreV1().Secrets(plan.Namespace).Get(context.Background(), "router-license", metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected router-license secret to remain absent")
	}
}
