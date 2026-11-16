// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net"
	"net/http"
	"strings"
)

type clientIPInfo struct {
	Address             string
	Version             int
	Source              string
	TrustedProxyApplied bool
	IsPrivate           bool
	IsLoopback          bool
	IsReserved          bool
}

func resolveClientIP(r *http.Request, cfg ClientIPConfig) clientIPInfo {
	remote := parseIPFromHostPort("")
	if r != nil {
		remote = parseIPFromHostPort(r.RemoteAddr)
	}
	info := classifyClientIP(remote, "remote_addr", false)
	if r == nil || remote == nil || !remoteTrusted(remote, cfg.TrustedProxyCIDRs) {
		return applyIPStoragePolicy(info, cfg)
	}
	for _, header := range clientIPHeaderOrder(cfg.HeaderOrder) {
		var ip net.IP
		if strings.EqualFold(strings.TrimSpace(header), "X-Forwarded-For") {
			ip = forwardedClientIP(r, remote, cfg.TrustedProxyCIDRs)
		} else {
			ip = firstHeaderIP(r, header)
		}
		if ip != nil {
			return applyIPStoragePolicy(classifyClientIP(ip, header, true), cfg)
		}
	}
	return applyIPStoragePolicy(info, cfg)
}

// forwardedClientIP walks the proxy chain from the socket peer toward the
// client. Each trusted proxy appends its peer to X-Forwarded-For, so the
// rightmost untrusted address is the client. Leftmost values are client
// supplied and must never override a later address added by a trusted proxy.
func forwardedClientIP(r *http.Request, remote net.IP, trusted []string) net.IP {
	var chain []net.IP
	for _, value := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(value, ",") {
			if ip := parseIPFromHostPort(strings.TrimSpace(part)); ip != nil {
				chain = append(chain, ip)
			}
		}
	}
	if len(chain) == 0 {
		return nil
	}
	hop := remote
	for i := len(chain) - 1; i >= 0; i-- {
		if !remoteTrusted(hop, trusted) {
			return hop
		}
		hop = chain[i]
		if !remoteTrusted(hop, trusted) {
			return hop
		}
	}
	return hop
}

func clientIPHeaderOrder(configured []string) []string {
	if len(configured) == 0 {
		return []string{"X-Forwarded-For", "X-Real-IP"}
	}
	return configured
}

func firstHeaderIP(r *http.Request, header string) net.IP {
	for _, value := range r.Header.Values(header) {
		for _, part := range strings.Split(value, ",") {
			if ip := parseIPFromHostPort(strings.TrimSpace(part)); ip != nil {
				return ip
			}
		}
	}
	return nil
}

func parseIPFromHostPort(value string) net.IP {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return net.ParseIP(strings.TrimSpace(host))
	}
	return nil
}

func remoteTrusted(ip net.IP, cidrs []string) bool {
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func classifyClientIP(ip net.IP, source string, trustedProxy bool) clientIPInfo {
	if ip == nil {
		return clientIPInfo{Source: source, TrustedProxyApplied: trustedProxy, IsReserved: true}
	}
	version := 6
	if ip.To4() != nil {
		version = 4
	}
	return clientIPInfo{
		Address:             ip.String(),
		Version:             version,
		Source:              strings.ToLower(strings.TrimSpace(source)),
		TrustedProxyApplied: trustedProxy,
		IsPrivate:           ip.IsPrivate(),
		IsLoopback:          ip.IsLoopback(),
		IsReserved:          !ip.IsGlobalUnicast() || ip.IsUnspecified(),
	}
}

func applyIPStoragePolicy(info clientIPInfo, cfg ClientIPConfig) clientIPInfo {
	if cfg.StoreIP != nil && !*cfg.StoreIP {
		info.Address = ""
		info.Version = 0
	}
	return info
}
