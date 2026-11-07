// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeFreshnessSource is a test stand-in for #813's revision values.
type fakeFreshnessSource struct {
	configDesired, configApplied int64
	policyDesired, policyApplied int64
	ok                           bool
}

func (f fakeFreshnessSource) ConfigFreshness() (int64, int64, time.Time, bool) {
	return f.configDesired, f.configApplied, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), f.ok
}

func (f fakeFreshnessSource) PolicyFreshness() (int64, int64, time.Time, bool) {
	return f.policyDesired, f.policyApplied, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), f.ok
}

// TestAdminMetricsSeries proves the admin series carry registry-bounded labels and
// the values the mount observed (ACP-N01).
func TestAdminMetricsSeries(t *testing.T) {
	store := newAdminMetricsStore()
	store.Observe(AdminObservation{
		OperationID: "replaceConfigGroupTargets", Object: "admin:config", Action: "write",
		Status: 200, Duration: 12 * time.Millisecond,
	})
	store.Observe(AdminObservation{
		OperationID: "replaceConfigGroupTargets", Object: "admin:config", Action: "write",
		Status: 403, Duration: 3 * time.Millisecond, AuthFailed: true,
	})
	store.Observe(AdminObservation{
		OperationID: "listConfigSets", Object: "admin:config", Action: "read",
		Status: 429, Duration: time.Millisecond, RateLimited: true,
	})
	store.RecordAuthentication("adminkey", "failure")
	store.RecordIdempotentReplay("replaceConfigGroupTargets")
	store.RecordAuditFailure("change_event")

	rendered := store.Prometheus(fakeFreshnessSource{configDesired: 12, configApplied: 11, policyDesired: 3, policyApplied: 3, ok: true})
	for _, want := range []string{
		`router_admin_requests_total{operation="replaceConfigGroupTargets",object="admin:config",action="write",status="200"} 1`,
		`router_admin_requests_total{operation="replaceConfigGroupTargets",object="admin:config",action="write",status="403"} 1`,
		`router_admin_authz_denials_total{object="admin:config",action="write",reason="forbidden"} 1`,
		`router_admin_rate_limited_total{operation="listConfigSets"} 1`,
		`router_admin_authentications_total{kind="adminkey",outcome="failure"} 1`,
		`router_admin_idempotent_replays_total{operation="replaceConfigGroupTargets"} 1`,
		`router_admin_audit_write_failures_total{kind="change_event"} 1`,
		`router_admin_request_duration_seconds_bucket{operation="replaceConfigGroupTargets",le="0.025"} 2`,
		`router_admin_revision{scope="config",kind="desired"} 12`,
		`router_admin_revision{scope="config",kind="applied"} 11`,
		`router_admin_revision{scope="policy",kind="desired"} 3`,
		`router_admin_revision{scope="policy",kind="applied"} 3`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered metrics missing %q\n%s", want, rendered)
		}
	}
	// No replica label and no unbounded label values.
	if strings.Contains(rendered, `replica=`) {
		t.Fatal("revision gauge carries a replica label; this process is the replica")
	}
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "router_admin_revision{") && !strings.Contains(line, `scope="config"`) && !strings.Contains(line, `scope="policy"`) {
			t.Fatalf("revision gauge has an unexpected label set: %s", line)
		}
	}
}

// TestAdminMetricsFreshnessUnknownOmitsSeries proves an unavailable freshness source
// omits the gauges instead of publishing a misleading zero.
func TestAdminMetricsFreshnessUnknownOmitsSeries(t *testing.T) {
	store := newAdminMetricsStore()
	store.Observe(AdminObservation{OperationID: "listConfigSets", Object: "admin:config", Action: "read", Status: 200})
	rendered := store.Prometheus(fakeFreshnessSource{ok: false})
	if strings.Contains(rendered, `router_admin_revision{`) {
		t.Fatalf("unknown freshness published a revision gauge:\n%s", rendered)
	}
	if !strings.Contains(rendered, "router_admin_requests_total") {
		t.Fatal("request series missing")
	}
	if got := store.Prometheus(nil); strings.Contains(got, `router_admin_revision{`) {
		t.Fatal("a nil freshness source published a revision gauge")
	}
}

// TestAdminMetricsAbsentFromServingScrape proves the serving-port metrics payload
// does not carry the admin series, so a v4.7 scrape target sees unchanged output.
func TestAdminMetricsAbsentFromServingScrape(t *testing.T) {
	service := &Service{}
	if got := service.adminMetrics().Prometheus(nil); strings.Contains(got, "router_request") {
		t.Fatal("admin series leaked serving metric names")
	}
	// isAdminPlaneRequest is what gates the append in writeMetricsPayload.
	if adminPlaneAuthorized(nil) {
		t.Fatal("a nil request was treated as an admin-plane request")
	}
	request := httptestNewRequestForAdminMetrics()
	if adminPlaneAuthorized(request) {
		t.Fatal("a request without a principal was treated as an admin-plane request")
	}
	request = request.WithContext(WithPrincipal(request.Context(), Principal{Subject: "adminkey:x", Kind: "adminkey"}))
	if adminPlaneAuthorized(request) {
		t.Fatal("a principal without a recorded decision was treated as authorized")
	}
	request = request.WithContext(WithAdminV1Decision(request.Context(), adminV1Decision{
		Taken: []adminV1Taken{{Object: authzObjectAdminConfig, Action: authzActionRead, Domain: AdminGlobalDomain}},
	}))
	if !adminPlaneAuthorized(request) {
		t.Fatal("an admin-plane request was not recognised")
	}
}

func httptestNewRequestForAdminMetrics() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/metrics", nil)
}

// TestMetricsPayloadIncludesAdminSeriesOnlyOnTheAdminPlane proves the end-to-end
// gate: an admin-plane scrape carries the admin series and a serving scrape does not.
func TestMetricsPayloadIncludesAdminSeriesOnlyOnTheAdminPlane(t *testing.T) {
	service := &Service{metrics: newMetricsStore(), trafficShape: &trafficShapeManager{}}
	service.adminMetrics().Observe(AdminObservation{
		OperationID: "listConfigSets", Object: "admin:config", Action: "read", Status: 200, Duration: time.Millisecond,
	})

	serving := httptest.NewRecorder()
	servingRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	service.writeMetricsPayload(serving, servingRequest, nil)
	if strings.Contains(serving.Body.String(), "router_admin_") {
		t.Fatal("the serving /metrics payload carries admin series")
	}

	admin := httptest.NewRecorder()
	adminRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/metrics", nil)
	adminRequest = adminRequest.WithContext(WithPrincipal(adminRequest.Context(), Principal{Subject: "adminkey:x", Kind: "adminkey"}))
	adminRequest = adminRequest.WithContext(WithAdminV1Decision(adminRequest.Context(), adminV1Decision{
		Taken: []adminV1Taken{{Object: authzObjectMetrics, Action: authzActionRead, Domain: AdminGlobalDomain}},
	}))
	service.writeMetricsPayload(admin, adminRequest, nil)
	if !strings.Contains(admin.Body.String(), `router_admin_requests_total{operation="listConfigSets"`) {
		t.Fatalf("the admin scrape is missing the admin series:\n%s", admin.Body.String())
	}
}
