// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"
)

// Defaults for the admin control plane (ACP-001, ACP-002, ACP-007, ACP-N02).
// An absent or disabled admin block keeps the v4.7 surface untouched: no
// second listener, legacy /admin/* on the serving port, /metrics on the
// serving port.
const (
	defaultAdminListenAddr          = "127.0.0.1:8081"
	defaultAdminLegacySunset        = "2027-03-01T00:00:00Z"
	defaultAdminMaxBodyBytes        = 1 << 20 // 1 MiB
	defaultAdminMaxHeaderBytes      = 64 << 10
	defaultAdminReadHeaderTimeoutMS = 5000
	defaultAdminReadTimeoutMS       = 30000
	defaultAdminWriteTimeoutMS      = 60000
	defaultAdminIdleTimeoutMS       = 60000
	defaultAdminShutdownTimeoutMS   = 10000
	adminTLSModeOff                 = "off"
	adminTLSModeRouter              = "router"
	adminTLSModeProxy               = "proxy"
	adminClientAuthNone             = "none"
	adminClientAuthOptional         = "optional"
	adminClientAuthRequire          = "require"
	adminTLSMinVersion12            = "1.2"
	adminTLSMinVersion13            = "1.3"
	adminCertificateSourceFiles     = "files"
	adminCertificateSourceACME      = "acme"
	// adminLegacySunsetFloor is the earliest Sunset date a v4.8.0 deployment
	// may advertise: the v4.8.0 release date plus the 90-day minimum the epic
	// requires before legacy removal in v5.0.0.
	adminLegacySunsetFloor = "2027-02-18T00:00:00Z"
)

// AdminPlaneConfig is the top-level `admin:` block: the control plane's own
// listener (ACP-001), its TLS and client-certificate material (ACP-002/003),
// hardening limits (ACP-N02), the legacy-route flag (ACP-009) and the
// /metrics placement flag (ACP-007).
//
// The block is opt-in: an absent block is exactly v4.7 behaviour. Enabling it
// adds a listener and moves /metrics to it by default; the serving port keeps
// the human reports UI it already serves.
type AdminPlaneConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Listen is the admin listener address. Loopback by default; remote
	// binding is an explicit opt-in that also needs TLS or the plaintext
	// override below.
	Listen string `yaml:"listen" json:"listen"`
	// AllowPlaintextNonLoopback permits a non-loopback bind without TLS. It
	// exists for controlled test networks and logs a startup warning.
	AllowPlaintextNonLoopback bool `yaml:"allow_plaintext_non_loopback" json:"allowPlaintextNonLoopback"`
	// LegacyRoutesOnServingPort keeps the legacy machine admin routes on the
	// serving port (ACP-009). Default true in v4.8.0. It never covers the
	// human reports UI, which is not part of the control plane.
	LegacyRoutesOnServingPort *bool `yaml:"legacy_routes_on_serving_port" json:"legacyRoutesOnServingPort,omitempty"`
	// LegacySunset is the Sunset header value advertised on legacy routes.
	LegacySunset string `yaml:"legacy_sunset" json:"legacySunset,omitempty"`
	// ExternalURL is the configured base URL of the admin plane. It is the
	// only source of absolute request-URL construction (for example DPoP htu
	// in #814); the listener never derives it from Host or Forwarded headers.
	ExternalURL string             `yaml:"external_url" json:"externalUrl,omitempty"`
	TLS         AdminTLSConfig     `yaml:"tls" json:"tls"`
	Limits      AdminLimitsConfig  `yaml:"limits" json:"limits"`
	Metrics     AdminMetricsConfig `yaml:"metrics" json:"metrics"`
	// Authentication is owned by #814. This lane nests the block here so the
	// whole plane configures in one place, and consumes the client-CA
	// material for TLS handshake verification.
	Authentication AdminAuthenticationConfig `yaml:"authentication" json:"authentication"`
}

// AdminTLSConfig configures how the admin listener terminates TLS.
type AdminTLSConfig struct {
	// Mode is off (plaintext, loopback only by default), router (the router
	// serves its own certificate) or proxy (a reverse proxy terminates TLS).
	Mode string `yaml:"mode" json:"mode"`
	// CertificateSource selects where the router-native certificate comes
	// from: files (default, CertFile/KeyFile) or acme (the #810 issuer, whose
	// manager is installed into the listener's single handshake hook).
	CertificateSource string `yaml:"certificate_source" json:"certificateSource,omitempty"`
	// CertFile and KeyFile are required for mode router with certificate_source
	// files.
	CertFile string `yaml:"cert_file" json:"certFile,omitempty"`
	KeyFile  string `yaml:"key_file" json:"keyFile,omitempty"`
	// ACME is owned by #810. It is declared as an opaque block so a
	// configuration that sets it is never silently ignored; the TLS lane
	// replaces the type with its ListenerTLSConfig.acme and this lane keeps
	// only hasACMEBlock.
	ACME map[string]any `yaml:"acme" json:"acme,omitempty"`
	// MinVersion is the minimum negotiated TLS version: 1.2 (default) or 1.3.
	MinVersion string `yaml:"min_version" json:"minVersion,omitempty"`
	// ClientCAFile is the bootstrap client CA bundle for mTLS. #814's
	// reloadable CA snapshot replaces it once installed.
	ClientCAFile string `yaml:"client_ca_file" json:"clientCaFile,omitempty"`
	// ClientAuth is none, optional or require. optional verifies a presented
	// certificate and still lets API-key and JWT clients connect; require
	// fails the handshake without one. Both verify against the current CA
	// snapshot, so a wrong CA always fails the handshake.
	ClientAuth string `yaml:"client_auth" json:"clientAuth,omitempty"`
	// TrustedProxyCIDRs lists the reverse proxies allowed to terminate TLS for
	// mode proxy. Non-loopback peers outside the list are rejected.
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs" json:"trustedProxyCidrs,omitempty"`
}

// AdminLimitsConfig holds the admin listener's hardening limits. They are
// independent of the serving listener's streaming deadlines (ACP-N02).
type AdminLimitsConfig struct {
	MaxBodyBytes        int `yaml:"max_body_bytes" json:"maxBodyBytes,omitempty"`
	MaxHeaderBytes      int `yaml:"max_header_bytes" json:"maxHeaderBytes,omitempty"`
	ReadHeaderTimeoutMS int `yaml:"read_header_timeout_ms" json:"readHeaderTimeoutMs,omitempty"`
	ReadTimeoutMS       int `yaml:"read_timeout_ms" json:"readTimeoutMs,omitempty"`
	WriteTimeoutMS      int `yaml:"write_timeout_ms" json:"writeTimeoutMs,omitempty"`
	IdleTimeoutMS       int `yaml:"idle_timeout_ms" json:"idleTimeoutMs,omitempty"`
	ShutdownTimeoutMS   int `yaml:"shutdown_timeout_ms" json:"shutdownTimeoutMs,omitempty"`
}

// AdminMetricsConfig controls the /metrics placement (ACP-007).
type AdminMetricsConfig struct {
	// OnServingPort keeps /metrics on the serving port with v4.7 caller-token
	// authorization. Unset means "keep v4.7": the exposition stays on the
	// serving port only while the admin plane is disabled, and moves to the
	// admin listener once the plane is enabled.
	OnServingPort *bool `yaml:"on_serving_port" json:"onServingPort,omitempty"`
}

// AdminAuthenticationConfig is owned by #814. Only the client-CA material
// needed for the listener's TLS handshake verification is declared here; #814
// extends this type with its issuer, JWKS, DPoP and emergency configuration.
type AdminAuthenticationConfig struct {
	MTLS AdminAuthenticationMTLSConfig `yaml:"mtls" json:"mtls"`
}

// AdminAuthenticationMTLSConfig carries the reloadable client CA bundle #814
// owns. The listener reads it through the shared, immutable CA snapshot so the
// handshake and the per-request revalidation always agree.
type AdminAuthenticationMTLSConfig struct {
	CAFiles        []string `yaml:"ca_files" json:"caFiles,omitempty"`
	ReloadInterval string   `yaml:"reload_interval" json:"reloadInterval,omitempty"`
}

// hasACMEBlock reports whether an acme sub-block is configured. #810 owns the
// block's type; this seam keeps the listener's checks independent of it.
func (c AdminTLSConfig) hasACMEBlock() bool {
	return len(c.ACME) > 0
}

func defaultAdminPlaneConfig(cfg *AdminPlaneConfig) {
	if cfg == nil {
		return
	}
	if cfg.LegacyRoutesOnServingPort == nil {
		enabled := true
		cfg.LegacyRoutesOnServingPort = &enabled
	}
	if strings.TrimSpace(cfg.LegacySunset) == "" {
		cfg.LegacySunset = defaultAdminLegacySunset
	}
	// A disabled block keeps the v4.7 surface. Only enabled planes get listener
	// defaults, so "block present but disabled" stays visibly a no-op.
	if !cfg.Enabled {
		return
	}
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = defaultAdminListenAddr
	}
	if strings.TrimSpace(cfg.TLS.Mode) == "" {
		cfg.TLS.Mode = adminTLSModeOff
	}
	if strings.TrimSpace(cfg.TLS.MinVersion) == "" {
		cfg.TLS.MinVersion = adminTLSMinVersion12
	}
	if strings.TrimSpace(cfg.TLS.CertificateSource) == "" {
		cfg.TLS.CertificateSource = adminCertificateSourceFiles
	}
	if strings.TrimSpace(cfg.TLS.ClientAuth) == "" {
		cfg.TLS.ClientAuth = adminClientAuthNone
	}
	if cfg.Limits.MaxBodyBytes == 0 {
		cfg.Limits.MaxBodyBytes = defaultAdminMaxBodyBytes
	}
	if cfg.Limits.MaxHeaderBytes == 0 {
		cfg.Limits.MaxHeaderBytes = defaultAdminMaxHeaderBytes
	}
	if cfg.Limits.ReadHeaderTimeoutMS == 0 {
		cfg.Limits.ReadHeaderTimeoutMS = defaultAdminReadHeaderTimeoutMS
	}
	if cfg.Limits.ReadTimeoutMS == 0 {
		cfg.Limits.ReadTimeoutMS = defaultAdminReadTimeoutMS
	}
	if cfg.Limits.WriteTimeoutMS == 0 {
		cfg.Limits.WriteTimeoutMS = defaultAdminWriteTimeoutMS
	}
	if cfg.Limits.IdleTimeoutMS == 0 {
		cfg.Limits.IdleTimeoutMS = defaultAdminIdleTimeoutMS
	}
	if cfg.Limits.ShutdownTimeoutMS == 0 {
		cfg.Limits.ShutdownTimeoutMS = defaultAdminShutdownTimeoutMS
	}
}

// legacyRoutesEnabled reports whether the legacy machine admin routes stay on
// the serving port. It is independent of Enabled: disabling the plane never
// silently changes the serving-port surface (ACP-005), and enabling it never
// forces the legacy routes off (ACP-009).
func (c *AdminPlaneConfig) legacyRoutesEnabled() bool {
	if c == nil || c.LegacyRoutesOnServingPort == nil {
		return true
	}
	return *c.LegacyRoutesOnServingPort
}

// metricsOnServingPort reports whether /metrics stays on the serving port with
// v4.7 caller-token authorization (ACP-007).
func (c *AdminPlaneConfig) metricsOnServingPort() bool {
	if c == nil {
		return true
	}
	if c.Metrics.OnServingPort != nil {
		return *c.Metrics.OnServingPort
	}
	return !c.Enabled
}

func (c *AdminPlaneConfig) legacySunsetTime() time.Time {
	if c == nil {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(c.LegacySunset))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func (c *AdminPlaneConfig) externalBaseURL() *url.URL {
	if c == nil || strings.TrimSpace(c.ExternalURL) == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(c.ExternalURL))
	if err != nil {
		return nil
	}
	return parsed
}

// adminListenIsLoopback reports whether the configured admin address binds a
// loopback interface only. A bare ":port" binds every interface and is not
// loopback.
func adminListenIsLoopback(listen string) (bool, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return false, fmt.Errorf("admin.listen must be host:port")
	}
	if strings.TrimSpace(port) == "" {
		return false, fmt.Errorf("admin.listen requires a port")
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	if host == "" {
		return false, nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		// A hostname cannot be proven loopback at validation time.
		return false, nil
	}
	return ip.IsLoopback(), nil
}

// validateAdminPlane fails closed on combinations that would expose the
// control plane unintentionally or make its configuration a no-op. It runs at
// config load, so a bad admin block stops the process before any listener
// binds.
func validateAdminPlane(cfg *AdminPlaneConfig) error {
	if cfg == nil {
		return nil
	}
	if !cfg.Enabled {
		if cfg.Metrics.OnServingPort != nil && !*cfg.Metrics.OnServingPort {
			return fmt.Errorf("admin.metrics.on_serving_port: false requires admin.enabled: true (metrics would otherwise be served nowhere)")
		}
		if adminPlaneBlockLooksConfigured(cfg) {
			log.Printf("WARNING: admin block is configured but admin.enabled is false; the admin listener is not started and /metrics stays on the serving port")
		}
		return nil
	}
	loopback, err := adminListenIsLoopback(cfg.Listen)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(cfg.TLS.Mode)) {
	case adminTLSModeOff:
		if !loopback && !cfg.AllowPlaintextNonLoopback {
			return fmt.Errorf("admin.listen %q is not loopback and admin.tls.mode is off; configure admin.tls.mode router or proxy, bind a loopback address, or set admin.allow_plaintext_non_loopback (not recommended)", cfg.Listen)
		}
		if strings.TrimSpace(cfg.TLS.CertFile) != "" || strings.TrimSpace(cfg.TLS.KeyFile) != "" {
			return fmt.Errorf("admin.tls.cert_file/key_file require admin.tls.mode router")
		}
		if strings.EqualFold(strings.TrimSpace(cfg.TLS.CertificateSource), adminCertificateSourceACME) {
			return fmt.Errorf("admin.tls.certificate_source acme requires admin.tls.mode router")
		}
	case adminTLSModeRouter:
		switch strings.ToLower(strings.TrimSpace(cfg.TLS.CertificateSource)) {
		case adminCertificateSourceFiles:
			if strings.TrimSpace(cfg.TLS.CertFile) == "" || strings.TrimSpace(cfg.TLS.KeyFile) == "" {
				return fmt.Errorf("admin.tls.mode router requires admin.tls.cert_file and admin.tls.key_file")
			}
			if cfg.TLS.hasACMEBlock() {
				return fmt.Errorf("admin.tls.acme requires admin.tls.certificate_source acme")
			}
		case adminCertificateSourceACME:
			if strings.TrimSpace(cfg.TLS.CertFile) != "" || strings.TrimSpace(cfg.TLS.KeyFile) != "" {
				return fmt.Errorf("admin.tls.certificate_source acme cannot be combined with admin.tls.cert_file/key_file")
			}
			if len(cfg.TLS.ACME) == 0 {
				return fmt.Errorf("admin.tls.certificate_source acme requires an admin.tls.acme block")
			}
		default:
			return fmt.Errorf("admin.tls.certificate_source must be files or acme")
		}
		if len(cfg.TLS.TrustedProxyCIDRs) > 0 {
			return fmt.Errorf("admin.tls.trusted_proxy_cidrs require admin.tls.mode proxy")
		}
	case adminTLSModeProxy:
		if len(cfg.TLS.TrustedProxyCIDRs) == 0 {
			return fmt.Errorf("admin.tls.mode proxy requires admin.tls.trusted_proxy_cidrs")
		}
		if strings.TrimSpace(cfg.TLS.CertFile) != "" || strings.TrimSpace(cfg.TLS.KeyFile) != "" {
			return fmt.Errorf("admin.tls.cert_file/key_file cannot be used with admin.tls.mode proxy")
		}
		if cfg.TLS.hasACMEBlock() {
			return fmt.Errorf("admin.tls.acme requires admin.tls.mode router")
		}
	default:
		return fmt.Errorf("admin.tls.mode must be off, router or proxy")
	}
	for _, cidr := range cfg.TLS.TrustedProxyCIDRs {
		if err := validateAdminTrustedProxyCIDR(cidr); err != nil {
			return err
		}
	}
	switch strings.TrimSpace(cfg.TLS.MinVersion) {
	case adminTLSMinVersion12, adminTLSMinVersion13:
	default:
		return fmt.Errorf("admin.tls.min_version must be 1.2 or 1.3")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.TLS.ClientAuth)) {
	case adminClientAuthNone:
		if strings.TrimSpace(cfg.TLS.ClientCAFile) != "" {
			return fmt.Errorf("admin.tls.client_ca_file requires admin.tls.client_auth optional or require")
		}
	case adminClientAuthOptional, adminClientAuthRequire:
		if strings.EqualFold(strings.TrimSpace(cfg.TLS.Mode), adminTLSModeProxy) {
			return fmt.Errorf("admin.tls.client_auth cannot be used with admin.tls.mode proxy (a reverse proxy cannot prove a native TLS client identity)")
		}
		if strings.TrimSpace(cfg.TLS.ClientCAFile) == "" && len(cfg.Authentication.MTLS.CAFiles) == 0 {
			return fmt.Errorf("admin.tls.client_auth %s requires a client CA (admin.tls.client_ca_file or admin.authentication.mtls.ca_files)", cfg.TLS.ClientAuth)
		}
	default:
		return fmt.Errorf("admin.tls.client_auth must be none, optional or require")
	}
	if len(cfg.Authentication.MTLS.CAFiles) > 0 && strings.EqualFold(strings.TrimSpace(cfg.TLS.ClientAuth), adminClientAuthNone) {
		return fmt.Errorf("admin.authentication.mtls.ca_files requires admin.tls.client_auth optional or require")
	}
	if interval := strings.TrimSpace(cfg.Authentication.MTLS.ReloadInterval); interval != "" {
		parsed, err := time.ParseDuration(interval)
		if err != nil || parsed < time.Second {
			return fmt.Errorf("admin.authentication.mtls.reload_interval must be a duration of at least 1s")
		}
	}
	if strings.TrimSpace(cfg.ExternalURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(cfg.ExternalURL))
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("admin.external_url must be an absolute URL")
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			return fmt.Errorf("admin.external_url must use https")
		}
		if parsed.Scheme == "http" && !loopback {
			return fmt.Errorf("admin.external_url must use https unless admin.listen is loopback")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return fmt.Errorf("admin.external_url must not contain userinfo, a query or a fragment")
		}
	}
	if sunset := strings.TrimSpace(cfg.LegacySunset); sunset != "" {
		parsed, err := time.Parse(time.RFC3339, sunset)
		if err != nil {
			return fmt.Errorf("admin.legacy_sunset must be an RFC3339 timestamp")
		}
		floor, _ := time.Parse(time.RFC3339, adminLegacySunsetFloor)
		if parsed.Before(floor) {
			return fmt.Errorf("admin.legacy_sunset must be on or after %s (v4.8.0 plus the 90-day minimum before legacy removal)", adminLegacySunsetFloor)
		}
	}
	return validateAdminLimits(cfg.Limits)
}

func validateAdminLimits(limits AdminLimitsConfig) error {
	if limits.MaxBodyBytes < 0 || limits.MaxBodyBytes > 256<<20 {
		return fmt.Errorf("admin.limits.max_body_bytes must be between 1 and 268435456")
	}
	if limits.MaxHeaderBytes < 0 || limits.MaxHeaderBytes > 1<<20 {
		return fmt.Errorf("admin.limits.max_header_bytes must be between 1 and 1048576")
	}
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"read_header_timeout_ms", limits.ReadHeaderTimeoutMS},
		{"read_timeout_ms", limits.ReadTimeoutMS},
		{"write_timeout_ms", limits.WriteTimeoutMS},
		{"idle_timeout_ms", limits.IdleTimeoutMS},
		{"shutdown_timeout_ms", limits.ShutdownTimeoutMS},
	} {
		if limit.value < 0 {
			return fmt.Errorf("admin.limits.%s cannot be negative", limit.name)
		}
		if limit.value > 24*60*60*1000 {
			return fmt.Errorf("admin.limits.%s cannot exceed 24 hours", limit.name)
		}
	}
	return nil
}

// validateAdminTrustedProxyCIDR rejects a proxy allowlist entry that trusts too
// much: a default route or a prefix broader than a site allocation would let
// almost any peer claim to be the TLS-terminating proxy.
func validateAdminTrustedProxyCIDR(raw string) error {
	_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("admin.tls.trusted_proxy_cidrs contains an invalid CIDR")
	}
	ones, bits := network.Mask.Size()
	if strings.TrimSpace(raw) == "0.0.0.0/0" || strings.TrimSpace(raw) == "::/0" {
		return fmt.Errorf("admin.tls.trusted_proxy_cidrs must not trust every peer (0.0.0.0/0 or ::/0)")
	}
	if bits == 32 && ones < 8 {
		return fmt.Errorf("admin.tls.trusted_proxy_cidrs entry %q is wider than /8; list the proxy addresses or a narrower network", strings.TrimSpace(raw))
	}
	if bits == 128 && ones < 32 {
		return fmt.Errorf("admin.tls.trusted_proxy_cidrs entry %q is wider than /32; list the proxy addresses or a narrower network", strings.TrimSpace(raw))
	}
	return nil
}

// adminPlaneBlockLooksConfigured reports whether the operator set anything
// beyond enabled/legacy_routes_on_serving_port, so a disabled block is visibly
// a no-op instead of a silent surprise.
func adminPlaneBlockLooksConfigured(cfg *AdminPlaneConfig) bool {
	listen := strings.TrimSpace(cfg.Listen)
	return (listen != "" && listen != defaultAdminListenAddr) ||
		strings.TrimSpace(cfg.ExternalURL) != "" ||
		strings.TrimSpace(cfg.TLS.Mode) != "" ||
		strings.TrimSpace(cfg.TLS.CertFile) != "" ||
		strings.TrimSpace(cfg.TLS.KeyFile) != "" ||
		strings.TrimSpace(cfg.TLS.ClientCAFile) != "" ||
		strings.TrimSpace(cfg.TLS.ClientAuth) != "" ||
		len(cfg.TLS.TrustedProxyCIDRs) > 0 ||
		len(cfg.Authentication.MTLS.CAFiles) > 0 ||
		cfg.Metrics.OnServingPort != nil
}
