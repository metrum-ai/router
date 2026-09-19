// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	tenantDeploymentOwnerLabel                  = "metrum.ai/smartrouter-instance"
	tenantDeploymentRuntimeBundleHashAnnotation = "metrum.ai/runtime-bundle-sha256"
)

// tenantDeploymentActivationWait bounds live readiness polling before hostname
// publish. Tests set it to zero so fake clients fail closed immediately.
var tenantDeploymentActivationWait = 3 * time.Minute
var tenantDeploymentActivationPoll = 5 * time.Second

// EKS tenant deployment adapters use typed AWS and Kubernetes clients. No
// command runner exists in this implementation.
type EKSTenantDeploymentAdapters struct {
	profile          TenantDeploymentProfile
	kube             kubernetes.Interface
	ssm              *ssm.Client
	secrets          *secretsmanager.Client
	database         TenantDatabaseAdapter
	resolveReference func(context.Context, string) ([]byte, error)
}

// ObserveTenantDeployment obtains a bounded live readback for status. It does
// not mutate the registry or the cluster and returns only safe scalar fields.
func ObserveTenantDeployment(ctx context.Context, profile TenantDeploymentProfile, status TenantDeploymentStatus) (TenantDeploymentStatus, error) {
	adapter, _, err := NewEKSTenantDeploymentAdapters(ctx, profile)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "accessdenied") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "expired") {
			status.ObservedState = "access_denied"
			status.Workload = &TenantDeploymentWorkloadStatus{Ownership: TenantOwnershipAccessDenied}
			status.PVC = &TenantDeploymentResourceStatus{NameAlias: "router-state", Ownership: TenantOwnershipAccessDenied}
			status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipAccessDenied}
			status.Service = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipAccessDenied}
			if status.Database == nil {
				status.Database = &TenantDeploymentDatabaseStatus{Ownership: TenantOwnershipNotApplicable}
			}
			return status, nil
		}
		return status, err
	}
	plan := TenantDeploymentPlan{InstanceID: status.InstanceID, Namespace: status.Namespace, DatabaseID: "", ComputeProfile: status.ComputeProfile}
	if status.ComputeProfile == "" {
		status.ComputeProfile = DefaultTenantComputeProfile
	}
	if policy, ok := profile.ApprovedComputeProfiles[status.ComputeProfile]; ok {
		status.NodeClassAlias = policy.NodeClassAlias
		status.Architecture = policy.Architecture
		status.CPURequest = policy.CPURequest
		status.CPULimit = policy.CPULimit
		status.MemoryRequest = policy.MemoryRequest
		status.MemoryLimit = policy.MemoryLimit
		plan.computePolicy = policy
	}
	status.Database = &TenantDeploymentDatabaseStatus{Ownership: TenantOwnershipNotApplicable}
	if status.DatabaseState != "" {
		status.Database = &TenantDeploymentDatabaseStatus{
			DatabaseIDAlias:        "rds-" + strings.TrimPrefix(status.InstanceID, "instance-"),
			Status:                 status.DatabaseState,
			OwnershipBindingResult: status.DatabaseState,
			Ownership:              TenantOwnershipOwned,
		}
	}

	deployment, err := adapter.kube.AppsV1().Deployments(plan.Namespace).Get(ctx, "router", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		status.ObservedState = "absent"
		status.Workload = &TenantDeploymentWorkloadStatus{Ownership: TenantOwnershipExpectedMissing}
		status.PVC = &TenantDeploymentResourceStatus{NameAlias: "router-state", Ownership: TenantOwnershipExpectedMissing}
		status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipExpectedMissing}
		status.Service = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipExpectedMissing}
		return status, nil
	}
	if err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			status.ObservedState = "access_denied"
			status.Workload = &TenantDeploymentWorkloadStatus{Ownership: TenantOwnershipAccessDenied}
			return status, nil
		}
		return status, err
	}
	if !owned(deployment.Labels, plan) {
		status.ObservedState = "ownership_or_replica_mismatch"
		status.Workload = &TenantDeploymentWorkloadStatus{Ownership: TenantOwnershipForeignOrUnverified}
		return status, nil
	}
	desired := int32(0)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired != 1 {
		status.ObservedState = "ownership_or_replica_mismatch"
		status.Workload = &TenantDeploymentWorkloadStatus{
			DesiredReplicas: desired, ReadyReplicas: deployment.Status.ReadyReplicas,
			AvailableReplicas: deployment.Status.AvailableReplicas, Ownership: TenantOwnershipForeignOrUnverified,
		}
		return status, nil
	}
	phaseCounts := map[string]int{}
	var restarts int32
	pods, err := adapter.kube.CoreV1().Pods(plan.Namespace).List(ctx, metav1.ListOptions{LabelSelector: tenantDeploymentOwnerLabel + "=" + plan.InstanceID})
	if err == nil {
		for _, pod := range pods.Items {
			if !owned(pod.Labels, plan) {
				continue
			}
			phaseCounts[string(pod.Status.Phase)]++
			for _, cs := range pod.Status.ContainerStatuses {
				restarts += cs.RestartCount
			}
		}
	}
	status.Workload = &TenantDeploymentWorkloadStatus{
		DesiredReplicas: desired, ReadyReplicas: deployment.Status.ReadyReplicas,
		AvailableReplicas: deployment.Status.AvailableReplicas, PodPhaseCounts: phaseCounts,
		RestartCount: restarts, Ownership: TenantOwnershipOwned,
	}

	pvc, err := adapter.kube.CoreV1().PersistentVolumeClaims(plan.Namespace).Get(ctx, "router-state", metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		status.PVC = &TenantDeploymentResourceStatus{NameAlias: "router-state", Ownership: TenantOwnershipExpectedMissing}
		status.ObservedState = "not_ready"
	case err != nil:
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			status.PVC = &TenantDeploymentResourceStatus{NameAlias: "router-state", Ownership: TenantOwnershipAccessDenied}
			status.ObservedState = "access_denied"
			return status, nil
		}
		return status, err
	case !owned(pvc.Labels, plan):
		status.PVC = &TenantDeploymentResourceStatus{NameAlias: "router-state", Ownership: TenantOwnershipForeignOrUnverified}
		status.ObservedState = "ownership_or_replica_mismatch"
		return status, nil
	default:
		sc := ""
		if pvc.Spec.StorageClassName != nil {
			sc = *pvc.Spec.StorageClassName
		}
		capBucket := ""
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			capBucket = q.String()
		}
		status.PVC = &TenantDeploymentResourceStatus{
			NameAlias: "router-state", Phase: string(pvc.Status.Phase), StorageClassAlias: sc,
			CapacityBucket: capBucket, Ownership: TenantOwnershipOwned,
		}
		if pvc.Status.Phase != corev1.ClaimBound || deployment.Status.AvailableReplicas != 1 {
			status.ObservedState = "not_ready"
		}
	}

	svc, err := adapter.kube.CoreV1().Services(plan.Namespace).Get(ctx, "router", metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		status.Service = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipExpectedMissing, ReadyClass: "absent"}
	case err != nil:
		status.Service = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipAccessDenied, ReadyClass: "access_denied"}
	case !owned(svc.Labels, plan):
		status.Service = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipForeignOrUnverified, ReadyClass: "foreign"}
	default:
		status.Service = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipOwned, ReadyClass: "present"}
	}

	if status.State == TenantDeploymentReady {
		ingress, err := adapter.kube.NetworkingV1().Ingresses(plan.Namespace).Get(ctx, "router", metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipExpectedMissing, ReadyClass: "absent"}
			status.ObservedState = "activation_or_ingress_mismatch"
		case err != nil:
			status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipAccessDenied, ReadyClass: "access_denied"}
			status.ObservedState = "access_denied"
		case !owned(ingress.Labels, plan):
			status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipForeignOrUnverified, ReadyClass: "foreign"}
			status.ObservedState = "activation_or_ingress_mismatch"
		default:
			status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipOwned, ReadyClass: "present"}
		}
	} else if status.Ingress == nil {
		status.Ingress = &TenantDeploymentResourceStatus{NameAlias: "router", Ownership: TenantOwnershipNotApplicable, ReadyClass: "not_applicable"}
	}

	if status.ObservedState == "" {
		if status.PVC != nil && status.PVC.Ownership == TenantOwnershipOwned &&
			status.Workload != nil && status.Workload.AvailableReplicas == 1 {
			status.ObservedState = "ready"
		} else {
			status.ObservedState = "not_ready"
		}
	}
	return status, nil
}

func NewEKSTenantDeploymentAdapters(ctx context.Context, profile TenantDeploymentProfile) (*EKSTenantDeploymentAdapters, TenantDeploymentAdapters, error) {
	return newEKSTenantDeploymentAdapters(ctx, profile, nil)
}

// NewApprovedEKSTenantDeploymentAdapters is intentionally separate from the
// default constructor. Only a validated, time-bounded non-production admission
// can attach the typed RDS adapter. The Fleet CLI reaches this constructor only
// after it consumes an externally issued, protected admission document; it
// never constructs that admission.
func NewApprovedEKSTenantDeploymentAdapters(ctx context.Context, profile TenantDeploymentProfile, admission TenantDeploymentRDSAdmission) (*EKSTenantDeploymentAdapters, TenantDeploymentAdapters, error) {
	if err := validateTenantDeploymentRDSAdmission(profile, admission, time.Now().UTC()); err != nil {
		return nil, TenantDeploymentAdapters{}, err
	}
	return newEKSTenantDeploymentAdapters(ctx, profile, &admission)
}

func newEKSTenantDeploymentAdapters(ctx context.Context, profile TenantDeploymentProfile, admission *TenantDeploymentRDSAdmission) (*EKSTenantDeploymentAdapters, TenantDeploymentAdapters, error) {
	cfg, err := tenantDeploymentAWSConfig(ctx, profile)
	if err != nil {
		return nil, TenantDeploymentAdapters{}, err
	}
	cluster, err := eks.NewFromConfig(cfg).DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(profile.ClusterAlias)})
	if err != nil || cluster.Cluster == nil || cluster.Cluster.Endpoint == nil || cluster.Cluster.CertificateAuthority == nil || cluster.Cluster.CertificateAuthority.Data == nil {
		return nil, TenantDeploymentAdapters{}, errors.New("describe approved EKS cluster")
	}
	if cluster.Cluster.Status == "" || string(cluster.Cluster.Status) != "ACTIVE" {
		return nil, TenantDeploymentAdapters{}, errors.New("approved EKS cluster is not active")
	}
	token, err := eksAuthenticationToken(ctx, cfg, profile.ClusterAlias)
	if err != nil {
		return nil, TenantDeploymentAdapters{}, err
	}
	ca, err := base64.StdEncoding.DecodeString(*cluster.Cluster.CertificateAuthority.Data)
	if err != nil {
		return nil, TenantDeploymentAdapters{}, errors.New("decode EKS certificate authority")
	}
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: *cluster.Cluster.Endpoint, BearerToken: token, TLSClientConfig: rest.TLSClientConfig{CAData: ca}, Timeout: 30 * time.Second})
	if err != nil {
		return nil, TenantDeploymentAdapters{}, errors.New("construct typed Kubernetes client")
	}
	a := &EKSTenantDeploymentAdapters{profile: profile, kube: kube, ssm: ssm.NewFromConfig(cfg), secrets: secretsmanager.NewFromConfig(cfg)}
	a.resolveReference = a.referenceValue
	a.database = a
	if admission != nil {
		database, err := NewTenantDeploymentRDSAdapter(profile, *admission, rds.NewFromConfig(cfg))
		if err != nil {
			return nil, TenantDeploymentAdapters{}, err
		}
		a.database = database
	}
	return a, TenantDeploymentAdapters{Namespace: a, NetworkPolicy: a, SecretBinding: a, LicenseBinding: a, State: a, Database: a.database, Router: a, Activation: a, Hostname: a, OwnershipTransition: a}, nil
}

func eksAuthenticationToken(ctx context.Context, cfg aws.Config, cluster string) (string, error) {
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", errors.New("retrieve AWS operator credentials")
	}
	// EKS bearer tokens are a base64url-wrapped STS GetCallerIdentity presign.
	// X-Amz-Expires must be present before signing; an empty-body SHA-256 is required.
	const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	u, err := url.Parse("https://sts." + cfg.Region + ".amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Expires=60")
	if err != nil {
		return "", errors.New("build EKS authentication URL")
	}
	req := &http.Request{Method: http.MethodGet, URL: u, Header: http.Header{"x-k8s-aws-id": []string{cluster}}}
	presigned, _, err := v4.NewSigner().PresignHTTP(ctx, credentials, req, emptyPayloadHash, "sts", cfg.Region, time.Now().UTC())
	if err != nil {
		return "", errors.New("presign EKS authentication token")
	}
	return "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(presigned)), nil
}

func (a *EKSTenantDeploymentAdapters) labels(plan TenantDeploymentPlan) map[string]string {
	return map[string]string{tenantDeploymentOwnerLabel: plan.InstanceID, "app.kubernetes.io/name": "smart-llmrouter", "app.kubernetes.io/managed-by": "metrum-fleetctl"}
}
func owned(labels map[string]string, plan TenantDeploymentPlan) bool {
	return labels != nil &&
		labels[tenantDeploymentOwnerLabel] == plan.InstanceID &&
		labels["app.kubernetes.io/managed-by"] == "metrum-fleetctl"
}

func tenantDeploymentNetworkPolicyRef(namespace, name string) string {
	return tenantDeploymentResourceRef(namespace, "networkpolicy", name)
}

func tenantDeploymentResourceRef(namespace, kind, name string) string {
	return kind + "/" + namespace + "/" + name
}

func ownershipError() error {
	return &TenantDeploymentAdapterError{Class: "ownership_conflict", UnknownOutcome: true, Err: errors.New("Kubernetes object is not owned by this deployment")}
}

func (a *EKSTenantDeploymentAdapters) EnsureNamespace(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	ns, err := a.kube.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = a.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: a.labels(p)}}, metav1.CreateOptions{})
		return "namespace/" + p.Namespace, err
	}
	if err != nil {
		return "", err
	}
	if !owned(ns.Labels, p) {
		return "", ownershipError()
	}
	return "namespace/" + p.Namespace, nil
}
func (a *EKSTenantDeploymentAdapters) DeleteNamespace(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	ns, err := a.kube.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !owned(ns.Labels, p) {
		return ownershipError()
	}
	return a.kube.CoreV1().Namespaces().Delete(ctx, p.Namespace, metav1.DeleteOptions{})
}

func (a *EKSTenantDeploymentAdapters) EnsureNetworkPolicy(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	name := "router-ingress"
	client := a.kube.NetworkingV1().NetworkPolicies(p.Namespace)
	current, err := client.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if !owned(current.Labels, p) {
			return "", ownershipError()
		}
		return tenantDeploymentNetworkPolicyRef(p.Namespace, name), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: a.labels(p)}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{tenantDeploymentOwnerLabel: p.InstanceID}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": a.profile.IngressNamespace}}}}}}}}
	_, err = client.Create(ctx, policy, metav1.CreateOptions{})
	return tenantDeploymentNetworkPolicyRef(p.Namespace, name), err
}
func (a *EKSTenantDeploymentAdapters) DeleteNetworkPolicy(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	return a.deleteNetworkPolicy(ctx, p, "router-ingress")
}
func (a *EKSTenantDeploymentAdapters) deleteNetworkPolicy(ctx context.Context, p TenantDeploymentPlan, name string) error {
	c := a.kube.NetworkingV1().NetworkPolicies(p.Namespace)
	o, e := c.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !owned(o.Labels, p) {
		return ownershipError()
	}
	return c.Delete(ctx, name, metav1.DeleteOptions{})
}

func (a *EKSTenantDeploymentAdapters) EnsureSecretBinding(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	return a.ensureRuntimeBundleSecret(ctx, p, pRuntimeBundleRef(p))
}
func (a *EKSTenantDeploymentAdapters) DeleteSecretBinding(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	return a.deleteSecret(ctx, p, "router-runtime")
}

// EnsureLicenseBinding is a no-op after runtime licensing removal (3.0.0).
// Deploy plans may still carry licenseRequestRef for schema compatibility,
// but EKS must not resolve or mount license material.
func (a *EKSTenantDeploymentAdapters) EnsureLicenseBinding(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	_ = ctx
	_ = p
	return "", nil
}
func (a *EKSTenantDeploymentAdapters) DeleteLicenseBinding(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	_ = ctx
	_ = p
	return nil
}

// References are retained only as private plan fields and resolved by the
// typed adapter in memory. This prevents protected references from entering
// status or resource rows.
func pRuntimeBundleRef(p TenantDeploymentPlan) string { return p.runtimeBundleRef }

func (a *EKSTenantDeploymentAdapters) ensureRuntimeBundleSecret(ctx context.Context, p TenantDeploymentPlan, ref string) (string, error) {
	value, err := a.resolveProtectedReference(ctx, ref)
	if err != nil {
		return "", errors.New("read protected runtime bundle")
	}
	bundle, err := parseTenantDeploymentRuntimeBundle(value)
	if err != nil {
		return "", err
	}
	if p.requireAdminReports {
		if err := validateRequiredAdminReports(bundle.ConfigYAML, p.adminReportsProxyCIDRs); err != nil {
			return "", err
		}
	}
	configYAML := bundle.ConfigYAML
	envJSON := bundle.EnvJSON
	if p.DatabaseID != "" {
		rdsAdapter, ok := a.database.(*TenantDeploymentRDSAdapter)
		if !ok || rdsAdapter == nil {
			return "", &TenantDeploymentAdapterError{Class: "rds_credential_binding_failed", Err: errors.New("dedicated RDS adapter is required for usage DSN binding")}
		}
		dsn, err := rdsAdapter.UsageDSN(ctx, p, a.secrets)
		if err != nil {
			return "", err
		}
		envJSON, err = injectRuntimeEnvJSONValue(envJSON, tenantDeploymentUsageDSNEnvKey, dsn)
		if err != nil {
			return "", &TenantDeploymentAdapterError{Class: "rds_credential_binding_failed", Err: errors.New("bind dedicated RDS usage DSN")}
		}
	} else {
		// Default Fleet path: SQLite on the owned PVC. Rewrite postgres /
		// deployment-job bundles so deploy is self-contained and repeatable.
		rewritten, err := applySQLiteUsageDBConfig(configYAML)
		if err != nil {
			return "", err
		}
		configYAML = rewritten
		envJSON, err = stripRuntimeEnvJSONKey(envJSON, tenantDeploymentUsageDSNEnvKey)
		if err != nil {
			return "", err
		}
	}
	data := map[string][]byte{"config.yaml": []byte(configYAML), "env.json": []byte(envJSON)}
	c := a.kube.CoreV1().Secrets(p.Namespace)
	existing, err := c.Get(ctx, "router-runtime", metav1.GetOptions{})
	if err == nil {
		if !owned(existing.Labels, p) {
			return "", ownershipError()
		}
		existing.Data = data
		if _, err := c.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return "", errors.New("write protected runtime bundle")
		}
		return tenantDeploymentResourceRef(p.Namespace, "secret", "router-runtime"), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", errors.New("read protected runtime secret")
	}
	if _, err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "router-runtime", Namespace: p.Namespace, Labels: a.labels(p)}, Type: corev1.SecretTypeOpaque, Data: data}, metav1.CreateOptions{}); err != nil {
		return "", errors.New("write protected runtime bundle")
	}
	return tenantDeploymentResourceRef(p.Namespace, "secret", "router-runtime"), nil
}

func validateRequiredAdminReports(configYAML string, proxyCIDRs []string) error {
	var cfg struct {
		Server struct {
			AdminAuth struct {
				Basic struct {
					Enabled           bool     `yaml:"enabled"`
					AllowInsecureHTTP bool     `yaml:"allow_insecure_http"`
					TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`
				} `yaml:"basic"`
				OIDC struct {
					Enabled bool `yaml:"enabled"`
				} `yaml:"oidc"`
				Authorization struct {
					Enabled bool `yaml:"enabled"`
				} `yaml:"authorization"`
			} `yaml:"admin_auth"`
			AdminReports struct {
				Enabled    bool   `yaml:"enabled"`
				PathPrefix string `yaml:"path_prefix"`
			} `yaml:"admin_reports"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		return &TenantDeploymentAdapterError{Class: "runtime_bundle_policy_failed", Err: errors.New("required admin reports configuration is invalid")}
	}
	if !cfg.Server.AdminReports.Enabled ||
		cleanAdminReportsPrefix(cfg.Server.AdminReports.PathPrefix) != "/admin/reports" ||
		(!cfg.Server.AdminAuth.Basic.Enabled && !cfg.Server.AdminAuth.OIDC.Enabled) ||
		!cfg.Server.AdminAuth.Authorization.Enabled {
		return &TenantDeploymentAdapterError{Class: "runtime_bundle_policy_failed", Err: errors.New("required admin reports configuration is missing")}
	}
	basic := cfg.Server.AdminAuth.Basic
	if !basic.Enabled || basic.AllowInsecureHTTP || len(proxyCIDRs) == 0 {
		return nil
	}
	// Basic Auth evaluates the forwarded-HTTPS check before comparing the
	// password, so a bundle that does not trust the deployment reverse proxy
	// answers every admin request with a challenge that is indistinguishable
	// from a wrong password.
	trusted := make([]*net.IPNet, 0, len(basic.TrustedProxyCIDRs))
	for _, raw := range basic.TrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return &TenantDeploymentAdapterError{Class: "runtime_bundle_policy_failed", Err: errors.New("required admin reports trusted proxy range is invalid")}
		}
		trusted = append(trusted, network)
	}
	for _, raw := range proxyCIDRs {
		_, proxy, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return &TenantDeploymentAdapterError{Class: "runtime_bundle_policy_failed", Err: errors.New("approved admin reports proxy range is invalid")}
		}
		if !anyCIDRCovers(trusted, proxy) {
			return &TenantDeploymentAdapterError{Class: "runtime_bundle_policy_failed", Err: errors.New("required admin reports configuration does not trust the deployment reverse proxy network")}
		}
	}
	return nil
}

// anyCIDRCovers reports whether one of the trusted networks fully contains the
// proxy network, so every address the reverse proxy can present is trusted.
func anyCIDRCovers(trusted []*net.IPNet, proxy *net.IPNet) bool {
	proxyOnes, proxyBits := proxy.Mask.Size()
	for _, network := range trusted {
		ones, bits := network.Mask.Size()
		if bits != proxyBits || ones > proxyOnes {
			continue
		}
		if network.Contains(proxy.IP) {
			return true
		}
	}
	return false
}

func (a *EKSTenantDeploymentAdapters) ensureReferenceSecret(ctx context.Context, p TenantDeploymentPlan, name, key, ref string) (string, error) {
	value, err := a.resolveProtectedReference(ctx, ref)
	if err != nil {
		return "", err
	}
	data := map[string][]byte{key: value}
	c := a.kube.CoreV1().Secrets(p.Namespace)
	existing, err := c.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if !owned(existing.Labels, p) {
			return "", ownershipError()
		}
		existing.Data = data
		if _, err := c.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return "", errors.New("write protected reference secret")
		}
		return tenantDeploymentResourceRef(p.Namespace, "secret", name), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	_, err = c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: a.labels(p)}, Type: corev1.SecretTypeOpaque, Data: data}, metav1.CreateOptions{})
	return tenantDeploymentResourceRef(p.Namespace, "secret", name), err
}
func (a *EKSTenantDeploymentAdapters) deleteSecret(ctx context.Context, p TenantDeploymentPlan, name string) error {
	c := a.kube.CoreV1().Secrets(p.Namespace)
	o, e := c.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !owned(o.Labels, p) {
		return ownershipError()
	}
	return c.Delete(ctx, name, metav1.DeleteOptions{})
}
func (a *EKSTenantDeploymentAdapters) resolveProtectedReference(ctx context.Context, ref string) ([]byte, error) {
	if a.resolveReference != nil {
		return a.resolveReference(ctx, ref)
	}
	return a.referenceValue(ctx, ref)
}

func (a *EKSTenantDeploymentAdapters) referenceValue(ctx context.Context, ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "aws-ssm:///") {
		name := strings.TrimPrefix(ref, "aws-ssm:///")
		if name != "" && !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		r, e := a.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: aws.Bool(true)})
		if e != nil || r.Parameter == nil || r.Parameter.Value == nil {
			return nil, errors.New("read protected deployment reference")
		}
		return []byte(*r.Parameter.Value), nil
	}
	if strings.HasPrefix(ref, "aws-secretsmanager:///") {
		id := strings.TrimPrefix(ref, "aws-secretsmanager:///")
		r, e := a.secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &id})
		if e != nil || r.SecretString == nil {
			return nil, errors.New("read protected deployment reference")
		}
		return []byte(*r.SecretString), nil
	}
	return nil, errors.New("unsupported protected deployment reference")
}

func (a *EKSTenantDeploymentAdapters) EnsureStatePVC(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	name := "router-state"
	c := a.kube.CoreV1().PersistentVolumeClaims(p.Namespace)
	o, e := c.Get(ctx, name, metav1.GetOptions{})
	if e == nil {
		if !owned(o.Labels, p) || len(o.Spec.AccessModes) != 1 || o.Spec.AccessModes[0] != corev1.ReadWriteOnce {
			return "", ownershipError()
		}
		return tenantDeploymentResourceRef(p.Namespace, "pvc", name), nil
	}
	if !apierrors.IsNotFound(e) {
		return "", e
	}
	q := resource.MustParse(fmt.Sprintf("%dGi", a.profile.StateStorageGiB))
	sc := a.profile.StorageClass
	_, e = c.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: a.labels(p)}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: &sc, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: q}}}}, metav1.CreateOptions{})
	return tenantDeploymentResourceRef(p.Namespace, "pvc", name), e
}
func (a *EKSTenantDeploymentAdapters) DeleteStatePVC(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	c := a.kube.CoreV1().PersistentVolumeClaims(p.Namespace)
	o, e := c.Get(ctx, "router-state", metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !owned(o.Labels, p) {
		return ownershipError()
	}
	return c.Delete(ctx, "router-state", metav1.DeleteOptions{})
}

// EnsureDedicatedRDS is deliberately fail-closed. The typed EKS lifecycle can
// carry a dedicated-RDS plan and its fake adapter covers retry/retention
// semantics, but live RDS mutation remains disabled until the #555 recorded
// non-production RDS adapter, credential-binding, and activation review gates
// are accepted. It never resolves or emits a DSN.
func (a *EKSTenantDeploymentAdapters) EnsureDedicatedRDS(_ context.Context, plan TenantDeploymentPlan) (string, error) {
	if a.profile.DatabaseMode != "dedicated-rds" || plan.DatabaseID == "" || plan.DatabaseProfile != a.profile.ApprovedDatabaseProfile {
		return "", &TenantDeploymentAdapterError{Class: "rds_profile_invalid", Err: errors.New("dedicated RDS database profile is required")}
	}
	return "", &TenantDeploymentAdapterError{Class: "rds_live_admission_required", Err: errors.New("dedicated RDS live adapter is not approved")}
}

func (a *EKSTenantDeploymentAdapters) DeleteDedicatedRDS(_ context.Context, _ TenantDeploymentPlan, _ string) error {
	return &TenantDeploymentAdapterError{Class: "rds_live_admission_required", Err: errors.New("dedicated RDS live adapter is not approved")}
}

func (a *EKSTenantDeploymentAdapters) EnsureRouter(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	if err := a.rejectHPA(ctx, p); err != nil {
		return "", err
	}
	const name = "router"
	client := a.kube.AppsV1().Deployments(p.Namespace)
	bundleHash, err := a.runtimeBundleSHA256(ctx, p)
	if err != nil {
		return "", err
	}
	existing, err := client.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if !owned(existing.Labels, p) || existing.Spec.Replicas == nil || *existing.Spec.Replicas != 1 {
			return "", ownershipError()
		}
		if err := a.applyRouterPodTemplate(existing, p, bundleHash); err != nil {
			return "", err
		}
		if _, err := client.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return "", errors.New("update router deployment")
		}
		return tenantDeploymentResourceRef(p.Namespace, "deployment", name), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	one := int32(1)
	labels := a.labels(p)
	recreate := appsv1.RecreateDeploymentStrategyType
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Strategy: appsv1.DeploymentStrategy{Type: recreate},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{tenantDeploymentOwnerLabel: p.InstanceID}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{}},
		},
	}
	if err := a.applyRouterPodTemplate(deployment, p, bundleHash); err != nil {
		return "", err
	}
	_, err = client.Create(ctx, deployment, metav1.CreateOptions{})
	return tenantDeploymentResourceRef(p.Namespace, "deployment", name), err
}

func (a *EKSTenantDeploymentAdapters) runtimeBundleSHA256(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	secret, err := a.kube.CoreV1().Secrets(p.Namespace).Get(ctx, "router-runtime", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("read router-runtime secret for rollout hash")
	}
	sum := sha256.New()
	_, _ = sum.Write(secret.Data["config.yaml"])
	_, _ = sum.Write([]byte{0})
	_, _ = sum.Write(secret.Data["env.json"])
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func (a *EKSTenantDeploymentAdapters) applyRouterPodTemplate(deployment *appsv1.Deployment, p TenantDeploymentPlan, bundleHash string) error {
	recreate := appsv1.RecreateDeploymentStrategyType
	deployment.Spec.Strategy.Type = recreate
	nonRoot := int64(65532)
	labels := a.labels(p)
	if deployment.Spec.Template.ObjectMeta.Labels == nil {
		deployment.Spec.Template.ObjectMeta.Labels = map[string]string{}
	}
	for k, v := range labels {
		deployment.Spec.Template.ObjectMeta.Labels[k] = v
	}
	if deployment.Spec.Template.ObjectMeta.Annotations == nil {
		deployment.Spec.Template.ObjectMeta.Annotations = map[string]string{}
	}
	if bundleHash != "" {
		deployment.Spec.Template.ObjectMeta.Annotations[tenantDeploymentRuntimeBundleHashAnnotation] = bundleHash
	}
	deployment.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: aws.Bool(true), RunAsUser: &nonRoot, RunAsGroup: &nonRoot, FSGroup: &nonRoot}
	container := corev1.Container{
		Name:  "router",
		Image: p.ReleaseDigest,
		Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             aws.Bool(true),
			RunAsUser:                &nonRoot,
			RunAsGroup:               &nonRoot,
			AllowPrivilegeEscalation: aws.Bool(false),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "state", MountPath: "/var/lib/smart-llmrouter"},
			{Name: "runtime", MountPath: "/app/config", ReadOnly: true},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt(8080)}},
			InitialDelaySeconds: 5,
			PeriodSeconds:       5,
		},
	}
	policy := p.computePolicy
	if policy.CPURequest != "" || policy.MemoryRequest != "" {
		requests := corev1.ResourceList{}
		limits := corev1.ResourceList{}
		if policy.CPURequest != "" {
			requests[corev1.ResourceCPU] = resource.MustParse(policy.CPURequest)
		}
		if policy.MemoryRequest != "" {
			requests[corev1.ResourceMemory] = resource.MustParse(policy.MemoryRequest)
		}
		if policy.CPULimit != "" {
			limits[corev1.ResourceCPU] = resource.MustParse(policy.CPULimit)
		}
		if policy.MemoryLimit != "" {
			limits[corev1.ResourceMemory] = resource.MustParse(policy.MemoryLimit)
		}
		container.Resources = corev1.ResourceRequirements{Requests: requests, Limits: limits}
	}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{container}
	if len(policy.NodeSelector) > 0 {
		deployment.Spec.Template.Spec.NodeSelector = map[string]string{}
		for k, v := range policy.NodeSelector {
			deployment.Spec.Template.Spec.NodeSelector[k] = v
		}
	} else {
		deployment.Spec.Template.Spec.NodeSelector = nil
	}
	if policy.Architecture == "amd64" || policy.Architecture == "arm64" {
		deployment.Spec.Template.Spec.Affinity = &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{policy.Architecture},
						}},
					}},
				},
			},
		}
	}
	if len(policy.Tolerations) > 0 {
		tols := make([]corev1.Toleration, 0, len(policy.Tolerations))
		for _, t := range policy.Tolerations {
			tol := corev1.Toleration{Key: t.Key, Value: t.Value}
			switch strings.ToLower(t.Operator) {
			case "exists":
				tol.Operator = corev1.TolerationOpExists
			case "equal", "":
				tol.Operator = corev1.TolerationOpEqual
			}
			switch strings.ToLower(t.Effect) {
			case "noschedule":
				tol.Effect = corev1.TaintEffectNoSchedule
			case "prefernoschedule":
				tol.Effect = corev1.TaintEffectPreferNoSchedule
			case "noexecute":
				tol.Effect = corev1.TaintEffectNoExecute
			}
			tols = append(tols, tol)
		}
		deployment.Spec.Template.Spec.Tolerations = tols
	} else {
		deployment.Spec.Template.Spec.Tolerations = nil
	}
	deployment.Spec.Template.Spec.Volumes = []corev1.Volume{
		{Name: "state", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "router-state"}}},
		{Name: "runtime", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "router-runtime"}}},
	}
	return nil
}
func (a *EKSTenantDeploymentAdapters) DeleteRouter(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	c := a.kube.AppsV1().Deployments(p.Namespace)
	o, e := c.Get(ctx, "router", metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !owned(o.Labels, p) {
		return ownershipError()
	}
	return c.Delete(ctx, "router", metav1.DeleteOptions{})
}
func (a *EKSTenantDeploymentAdapters) rejectHPA(ctx context.Context, p TenantDeploymentPlan) error {
	items, e := a.kube.AutoscalingV2().HorizontalPodAutoscalers(p.Namespace).List(ctx, metav1.ListOptions{LabelSelector: tenantDeploymentOwnerLabel + "=" + p.InstanceID})
	if e != nil {
		return e
	}
	for _, h := range items.Items {
		if h.Spec.ScaleTargetRef.Kind == "Deployment" && h.Spec.ScaleTargetRef.Name == "router" {
			return &TenantDeploymentAdapterError{Class: "hpa_forbidden", Err: errors.New("SQLite state requires one replica")}
		}
	}
	return nil
}

func (a *EKSTenantDeploymentAdapters) ValidateActivation(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	if err := a.rejectHPA(ctx, p); err != nil {
		return "", err
	}
	deadline := time.Now().UTC().Add(tenantDeploymentActivationWait)
	var last error
	for {
		pvc, e := a.kube.CoreV1().PersistentVolumeClaims(p.Namespace).Get(ctx, "router-state", metav1.GetOptions{})
		if e != nil || pvc.Status.Phase != corev1.ClaimBound {
			last = &TenantDeploymentAdapterError{Class: "state_not_bound", Err: errors.New("state PVC is not bound")}
		} else {
			d, e := a.kube.AppsV1().Deployments(p.Namespace).Get(ctx, "router", metav1.GetOptions{})
			if e != nil || !owned(d.Labels, p) || d.Status.AvailableReplicas != 1 {
				last = &TenantDeploymentAdapterError{Class: "router_not_ready", Err: errors.New("router deployment is not ready")}
			} else if d.Status.UpdatedReplicas != 1 || d.Status.ReadyReplicas != 1 || d.Status.ObservedGeneration < d.Generation {
				last = &TenantDeploymentAdapterError{Class: "router_not_ready", Err: errors.New("router deployment rollout is not complete")}
			} else {
				return tenantDeploymentResourceRef(p.Namespace, "activation", "router-ready"), nil
			}
		}
		if !deadline.After(time.Now().UTC()) {
			return "", last
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(tenantDeploymentActivationPoll):
		}
	}
}
func (a *EKSTenantDeploymentAdapters) DeleteActivation(context.Context, TenantDeploymentPlan, string) error {
	return nil
}

func (a *EKSTenantDeploymentAdapters) EnableHostname(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	const name = "router"
	services := a.kube.CoreV1().Services(p.Namespace)
	service, err := services.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = services.Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: a.labels(p)}, Spec: corev1.ServiceSpec{Selector: map[string]string{tenantDeploymentOwnerLabel: p.InstanceID}, Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}}}}, metav1.CreateOptions{})
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if !owned(service.Labels, p) {
		return "", ownershipError()
	}
	class := a.profile.IngressClassName
	desired := tenantDeploymentIngressSpec(class, p.IngressHostnames(a.profile.TLSSecretName), name)
	ingresses := a.kube.NetworkingV1().Ingresses(p.Namespace)
	existing, err := ingresses.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if !owned(existing.Labels, p) {
			return "", ownershipError()
		}
		existing.Labels = a.labels(p)
		existing.Spec = desired
		_, err = ingresses.Update(ctx, existing, metav1.UpdateOptions{})
		return tenantDeploymentResourceRef(p.Namespace, "ingress", name), err
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: a.labels(p)},
		Spec:       desired,
	}
	_, err = ingresses.Create(ctx, ingress, metav1.CreateOptions{})
	return tenantDeploymentResourceRef(p.Namespace, "ingress", name), err
}
func (a *EKSTenantDeploymentAdapters) DisableHostname(ctx context.Context, p TenantDeploymentPlan, _ string) error {
	c := a.kube.NetworkingV1().Ingresses(p.Namespace)
	o, e := c.Get(ctx, "router", metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !owned(o.Labels, p) {
		return ownershipError()
	}
	return c.Delete(ctx, "router", metav1.DeleteOptions{})
}

// TransitionOwnership relabels exact Fleet-managed objects from a predecessor
// owner onto the plan's target instance. Mixed source/target labels are allowed
// only so a resumed transition can finish; foreign owners fail closed. The PVC
// claim is never deleted or recreated.
func (a *EKSTenantDeploymentAdapters) TransitionOwnership(ctx context.Context, p TenantDeploymentPlan) (string, error) {
	source := strings.TrimSpace(p.SourceInstanceID)
	if source == "" || source == p.InstanceID {
		return "", &TenantDeploymentAdapterError{Class: "ownership_transition_invalid", Err: errors.New("ownership transition requires a distinct source instance")}
	}
	if strings.TrimSpace(p.OwnershipTransitionChangeReference) == "" {
		return "", &TenantDeploymentAdapterError{Class: "ownership_transition_invalid", Err: errors.New("ownership transition requires a change reference")}
	}
	ns, err := a.kube.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if err := a.transitionObjectLabels(ctx, "namespace", ns.GetName(), ns.Labels, nil, source, p, func(labels map[string]string) error {
		ns.Labels = labels
		_, err := a.kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return "", err
	}

	if err := a.transitionNetworkPolicy(ctx, p, source); err != nil {
		return "", err
	}
	for _, secretName := range []string{"router-runtime"} {
		if err := a.transitionSecret(ctx, p, secretName, source); err != nil {
			return "", err
		}
	}
	if err := a.transitionPVC(ctx, p, source); err != nil {
		return "", err
	}
	if err := a.transitionDeployment(ctx, p, source); err != nil {
		return "", err
	}
	if err := a.transitionService(ctx, p, source); err != nil {
		return "", err
	}
	if err := a.transitionIngress(ctx, p, source); err != nil {
		return "", err
	}
	return tenantDeploymentResourceRef(p.Namespace, "ownership-transition", p.InstanceID), nil
}

func fleetManaged(labels map[string]string) bool {
	return labels != nil && labels["app.kubernetes.io/managed-by"] == "metrum-fleetctl"
}

func ownerOf(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	return labels[tenantDeploymentOwnerLabel]
}

func (a *EKSTenantDeploymentAdapters) transitionObjectLabels(ctx context.Context, kind, name string, labels map[string]string, requiredShape func(map[string]string) error, source string, p TenantDeploymentPlan, update func(map[string]string) error) error {
	_ = ctx
	if !fleetManaged(labels) {
		return ownershipError()
	}
	owner := ownerOf(labels)
	switch owner {
	case p.InstanceID:
		if requiredShape != nil {
			if err := requiredShape(labels); err != nil {
				return err
			}
		}
		return nil
	case source:
		next := a.labels(p)
		if requiredShape != nil {
			if err := requiredShape(next); err != nil {
				return err
			}
		}
		if err := update(next); err != nil {
			return fmt.Errorf("transition %s/%s ownership", kind, name)
		}
		return nil
	default:
		return ownershipError()
	}
}

func (a *EKSTenantDeploymentAdapters) transitionSecret(ctx context.Context, p TenantDeploymentPlan, name, source string) error {
	c := a.kube.CoreV1().Secrets(p.Namespace)
	o, err := c.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.transitionObjectLabels(ctx, "secret", name, o.Labels, nil, source, p, func(labels map[string]string) error {
		o.Labels = labels
		_, err := c.Update(ctx, o, metav1.UpdateOptions{})
		return err
	})
}

func (a *EKSTenantDeploymentAdapters) transitionPVC(ctx context.Context, p TenantDeploymentPlan, source string) error {
	c := a.kube.CoreV1().PersistentVolumeClaims(p.Namespace)
	o, err := c.Get(ctx, "router-state", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ownershipError()
	}
	if err != nil {
		return err
	}
	if len(o.Spec.AccessModes) != 1 || o.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		return ownershipError()
	}
	return a.transitionObjectLabels(ctx, "pvc", "router-state", o.Labels, nil, source, p, func(labels map[string]string) error {
		o.Labels = labels
		_, err := c.Update(ctx, o, metav1.UpdateOptions{})
		return err
	})
}

func (a *EKSTenantDeploymentAdapters) transitionNetworkPolicy(ctx context.Context, p TenantDeploymentPlan, source string) error {
	c := a.kube.NetworkingV1().NetworkPolicies(p.Namespace)
	o, err := c.Get(ctx, "router-ingress", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.transitionObjectLabels(ctx, "networkpolicy", "router-ingress", o.Labels, nil, source, p, func(labels map[string]string) error {
		o.Labels = labels
		if o.Spec.PodSelector.MatchLabels == nil {
			o.Spec.PodSelector.MatchLabels = map[string]string{}
		}
		o.Spec.PodSelector.MatchLabels[tenantDeploymentOwnerLabel] = p.InstanceID
		_, err := c.Update(ctx, o, metav1.UpdateOptions{})
		return err
	})
}

func (a *EKSTenantDeploymentAdapters) transitionDeployment(ctx context.Context, p TenantDeploymentPlan, source string) error {
	c := a.kube.AppsV1().Deployments(p.Namespace)
	o, err := c.Get(ctx, "router", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fleetManaged(o.Labels) {
		return ownershipError()
	}
	switch ownerOf(o.Labels) {
	case p.InstanceID:
		if o.Spec.Replicas == nil || *o.Spec.Replicas != 1 {
			return ownershipError()
		}
		return nil
	case source:
		// Deployment selectors are immutable; delete the source-owned Deployment
		// and let EnsureRouter recreate it under the target owner. PVC is retained.
		return c.Delete(ctx, "router", metav1.DeleteOptions{})
	default:
		return ownershipError()
	}
}

func (a *EKSTenantDeploymentAdapters) transitionService(ctx context.Context, p TenantDeploymentPlan, source string) error {
	c := a.kube.CoreV1().Services(p.Namespace)
	o, err := c.Get(ctx, "router", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.transitionObjectLabels(ctx, "service", "router", o.Labels, nil, source, p, func(labels map[string]string) error {
		o.Labels = labels
		if o.Spec.Selector == nil {
			o.Spec.Selector = map[string]string{}
		}
		o.Spec.Selector[tenantDeploymentOwnerLabel] = p.InstanceID
		_, err := c.Update(ctx, o, metav1.UpdateOptions{})
		return err
	})
}

func (a *EKSTenantDeploymentAdapters) transitionIngress(ctx context.Context, p TenantDeploymentPlan, source string) error {
	c := a.kube.NetworkingV1().Ingresses(p.Namespace)
	o, err := c.Get(ctx, "router", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.transitionObjectLabels(ctx, "ingress", "router", o.Labels, nil, source, p, func(labels map[string]string) error {
		o.Labels = labels
		_, err := c.Update(ctx, o, metav1.UpdateOptions{})
		return err
	})
}

// Compile-time guard: typed HPA API remains part of the adapter contract.
var _ = autoscalingv2.HorizontalPodAutoscaler{}

func tenantDeploymentIngressSpec(class string, hostnames []TenantDeploymentHostnameAlias, serviceName string) networkingv1.IngressSpec {
	pathType := networkingv1.PathTypePrefix
	rules := make([]networkingv1.IngressRule, 0, len(hostnames))
	for _, host := range hostnames {
		rules = append(rules, networkingv1.IngressRule{
			Host: host.Hostname,
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Path:     "/",
				PathType: &pathType,
				Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{
						Name: serviceName,
						Port: networkingv1.ServiceBackendPort{Number: 80},
					},
				},
			}}}},
		})
	}
	secretHosts := make(map[string][]string)
	for _, host := range hostnames {
		secretHosts[host.TLSSecretName] = append(secretHosts[host.TLSSecretName], host.Hostname)
	}
	tlsEntries := make([]networkingv1.IngressTLS, 0, len(secretHosts))
	for secretName, hosts := range secretHosts {
		tlsEntries = append(tlsEntries, networkingv1.IngressTLS{
			Hosts:      append([]string(nil), hosts...),
			SecretName: secretName,
		})
	}
	sort.Slice(tlsEntries, func(i, j int) bool {
		return tlsEntries[i].SecretName < tlsEntries[j].SecretName
	})
	return networkingv1.IngressSpec{
		IngressClassName: &class,
		TLS:              tlsEntries,
		Rules:            rules,
	}
}
