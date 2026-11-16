// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type requestTraceContext struct {
	traceID      string
	spanID       string
	parentSpanID string
	traceFlags   string
}

func validateTraceConfig(cfg TraceConfig) error {
	for i, value := range cfg.TrustedParentCIDRs {
		cidr := strings.TrimSpace(value)
		if cidr == "" {
			return fmt.Errorf("server trace trusted_parent_cidrs[%d] cannot be empty", i)
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("server trace trusted_parent_cidrs[%d] must be a CIDR", i)
		}
	}
	return nil
}

func newRequestTraceContext() requestTraceContext {
	return requestTraceContext{traceID: randomTraceHex(16), spanID: randomTraceHex(8), traceFlags: "00"}
}

func randomTraceHex(size int) string {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		sum := sha256.Sum256([]byte(requestID() + "/trace"))
		copy(data, sum[:size])
	}
	allZero := true
	for _, b := range data {
		allZero = allZero && b == 0
	}
	if allZero {
		data[len(data)-1] = 1
	}
	return hex.EncodeToString(data)
}

func parseTraceparent(value string) (traceID, parentSpanID, flags string, ok bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", "", "", false
	}
	traceID, parentSpanID, flags = strings.ToLower(parts[1]), strings.ToLower(parts[2]), strings.ToLower(parts[3])
	traceBytes, traceErr := hex.DecodeString(traceID)
	spanBytes, spanErr := hex.DecodeString(parentSpanID)
	if traceErr != nil || spanErr != nil {
		return "", "", "", false
	}
	if _, err := hex.DecodeString(flags); err != nil || allZeroBytes(traceBytes) || allZeroBytes(spanBytes) {
		return "", "", "", false
	}
	return traceID, parentSpanID, flags, true
}

func allZeroBytes(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

func traceparentFromRequest(r *http.Request, caller *callerRuntime, authenticated bool, cfg TraceConfig) requestTraceContext {
	trace := newRequestTraceContext()
	if r == nil || !authenticated {
		return trace
	}
	trusted := caller != nil && caller.cfg.TrustTraceparent
	if !trusted {
		trusted = tracePeerTrusted(r.RemoteAddr, cfg.TrustedParentCIDRs)
	}
	if !trusted {
		return trace
	}
	values := r.Header.Values("traceparent")
	if len(values) != 1 {
		return trace
	}
	traceID, parentSpanID, flags, ok := parseTraceparent(values[0])
	if !ok {
		return trace
	}
	trace.traceID = traceID
	trace.parentSpanID = parentSpanID
	trace.traceFlags = flags
	return trace
}

func tracePeerTrusted(remoteAddr string, cidrs []string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, value := range cidrs {
		_, prefix, err := net.ParseCIDR(strings.TrimSpace(value))
		if err == nil && prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (c requestTraceContext) traceparent() string {
	if c.traceID == "" || c.spanID == "" {
		return ""
	}
	flags := c.traceFlags
	if len(flags) != 2 {
		flags = "00"
	}
	return "00-" + c.traceID + "-" + c.spanID + "-" + flags
}
