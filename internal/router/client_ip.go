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
		if ip := firstHeaderIP(r, header); ip != nil {
			return applyIPStoragePolicy(classifyClientIP(ip, header, true), cfg)
		}
	}
	return applyIPStoragePolicy(info, cfg)
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
