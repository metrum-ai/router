// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metric names exported by the TLS manager. They follow the existing
// metrum_ai_router_ prefix and the text exposition format the router already
// serves from /metrics.
const (
	metricCertExpiry         = "metrum_ai_router_tls_certificate_expiry_timestamp_seconds"
	metricCertNotBefore      = "metrum_ai_router_tls_certificate_not_before_timestamp_seconds"
	metricCertRenewalDue     = "metrum_ai_router_tls_certificate_renewal_due_timestamp_seconds"
	metricCertValid          = "metrum_ai_router_tls_certificate_valid"
	metricACMEOrders         = "metrum_ai_router_tls_acme_orders_total"
	metricRenewalFailures    = "metrum_ai_router_tls_renewal_failures_total"
	metricDNSPresentFailures = "metrum_ai_router_tls_dns_present_failures_total"
	metricDNSConflicts       = "metrum_ai_router_tls_dns_conflict_total"
	metricDNSRecordsManaged  = "metrum_ai_router_tls_dns_records_managed"
	metricIssuerLockFailures = "metrum_ai_router_tls_issuer_lock_failures_total"
	metricARIFailures        = "metrum_ai_router_tls_ari_failures_total"
)

var metricHelp = map[string]string{
	metricCertExpiry:         "Expiry time of the certificate served for this listener.",
	metricCertNotBefore:      "Start of validity of the certificate served for this listener.",
	metricCertRenewalDue:     "Time at which this certificate is scheduled for renewal.",
	metricCertValid:          "Whether the served certificate is currently inside its validity period.",
	metricACMEOrders:         "ACME orders attempted by this listener by result.",
	metricRenewalFailures:    "Certificate renewal failures by reason.",
	metricDNSPresentFailures: "DNS-01 challenge presentation failures by DNS provider.",
	metricDNSConflicts:       "Managed DNS records left untouched because another owner created them.",
	metricDNSRecordsManaged:  "DNS records currently owned and managed by this router.",
	metricIssuerLockFailures: "Times a replica could not take the issuer lock.",
	metricARIFailures:        "Failed ACME renewal-information (ARI) lookups; the renewal ratio applied instead.",
}

type seriesKey struct {
	name   string
	labels string
}

// metrics is the TLS manager's metric state. It is a plain text exposition on
// purpose: the router does not use a Prometheus client library.
type metrics struct {
	mu       sync.Mutex
	listener string
	domains  string
	series   map[seriesKey]float64
}

func newMetrics(listener string, domains []string) *metrics {
	return &metrics{
		listener: listener,
		domains:  strings.Join(domains, ","),
		series:   map[seriesKey]float64{},
	}
}

// baseLabels are the labels every TLS series carries.
func (m *metrics) baseLabels() [][2]string {
	return [][2]string{{"listener", m.listener}, {"domains", m.domains}}
}

func mergeLabels(base [][2]string, extra ...[2]string) [][2]string {
	out := make([][2]string, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}

// set stores an absolute value.
func (m *metrics) set(name string, labels [][2]string, value float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.series[seriesKey{name: name, labels: renderLabels(labels)}] = value
}

// add increments a counter.
func (m *metrics) add(name string, labels [][2]string, delta float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := seriesKey{name: name, labels: renderLabels(labels)}
	m.series[key] += delta
}

// value returns the current value of one series (tests and diagnostics).
func (m *metrics) value(name string, labels [][2]string) float64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.series[seriesKey{name: name, labels: renderLabels(labels)}]
}

// observeCertificate publishes the certificate gauges.
func (m *metrics) observeCertificate(rec *CertificateRecord, renewalDue time.Time, now time.Time) {
	if m == nil {
		return
	}
	labels := m.baseLabels()
	if rec == nil {
		m.set(metricCertValid, labels, 0)
		return
	}
	m.set(metricCertExpiry, labels, float64(rec.NotAfter.Unix()))
	m.set(metricCertNotBefore, labels, float64(rec.NotBefore.Unix()))
	if !renewalDue.IsZero() {
		m.set(metricCertRenewalDue, labels, float64(renewalDue.Unix()))
	}
	valid := 0.0
	if now.Before(rec.NotAfter) && !now.Before(rec.NotBefore) {
		valid = 1
	}
	m.set(metricCertValid, labels, valid)
}

// prometheus renders every series in a deterministic order.
func (m *metrics) prometheus() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	keys := make([]seriesKey, 0, len(m.series))
	values := make(map[seriesKey]float64, len(m.series))
	for key, value := range m.series {
		keys = append(keys, key)
		values[key] = value
	}
	m.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].labels < keys[j].labels
	})
	var b strings.Builder
	seenHelp := map[string]bool{}
	for _, key := range keys {
		if !seenHelp[key.name] {
			kind := "counter"
			if isGaugeMetric(key.name) {
				kind = "gauge"
			}
			fmt.Fprintf(&b, "# HELP %s %s\n", key.name, metricHelp[key.name])
			fmt.Fprintf(&b, "# TYPE %s %s\n", key.name, kind)
			seenHelp[key.name] = true
		}
		rendered := key.labels
		if rendered != "" {
			rendered = "{" + rendered + "}"
		}
		fmt.Fprintf(&b, "%s%s %s\n", key.name, rendered, formatMetricValue(values[key]))
	}
	return b.String()
}

func isGaugeMetric(name string) bool {
	switch name {
	case metricCertExpiry, metricCertNotBefore, metricCertRenewalDue, metricCertValid, metricDNSRecordsManaged:
		return true
	}
	return false
}

func formatMetricValue(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
}

func renderLabels(labels [][2]string) string {
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		if label[0] == "" {
			continue
		}
		parts = append(parts, label[0]+`="`+escapeMetricLabel(label[1])+`"`)
	}
	return strings.Join(parts, ",")
}

func escapeMetricLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
