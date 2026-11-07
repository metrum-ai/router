// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AdminMux is the route registry for the admin listener (ACP-001). Other lanes
// mount their routes here: #811 mounts the /admin/v1 router, #814 installs its
// authenticator chain with Use and #816 installs audit the same way, so no lane
// has to touch the server lifecycle.
//
// Patterns are Go 1.22 mux patterns ("GET /admin/v1/clients"), so method and
// path matching behave exactly as on the serving mux. Routes registered here
// are served only on the admin listener.
type AdminMux struct {
	mux        *http.ServeMux
	mu         sync.Mutex
	middleware []func(http.Handler) http.Handler
}

func newAdminMux() *AdminMux {
	return &AdminMux{mux: http.NewServeMux()}
}

// Handle registers an admin-only route.
func (m *AdminMux) Handle(pattern string, h http.Handler) {
	if m == nil || h == nil {
		return
	}
	m.mux.Handle(pattern, h)
}

// HandleFunc registers an admin-only route.
func (m *AdminMux) HandleFunc(pattern string, f http.HandlerFunc) {
	if m == nil || f == nil {
		return
	}
	m.Handle(pattern, f)
}

// Use appends middleware around the registered routes. Middleware appended
// first runs closest to the mux; the listener's hardening (security headers,
// CORS, body limit, credential-shape gate) always runs before it.
func (m *AdminMux) Use(mw func(http.Handler) http.Handler) {
	if m == nil || mw == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.middleware = append(m.middleware, mw)
}

// handler builds the admin listener handler: hardening → machine-only gate →
// Use middleware → routes. Every response is JSON; the fallback is a JSON 404
// so the plane never serves HTML (ACP-N06).
func (m *AdminMux) handler(cfg *AdminPlaneConfig, authenticate Authenticator) http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	m.mu.Lock()
	middleware := append([]func(http.Handler) http.Handler(nil), m.middleware...)
	m.mu.Unlock()
	var h http.Handler = m.mux
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	h = adminAuthentication(authenticate)(h)
	h = adminMachineOnlyGate(h)
	h = adminProxyGate(cfg)(h)
	h = adminHardening(cfg)(h)
	return h
}

// adminAuthentication runs the #814 machine authentication chain and publishes
// the authenticated principal with WithPrincipal, the only identity source the
// admin port has. The health probes stay unauthenticated because they carry
// state only (ACP-006); every other admin route requires an identity.
//
// No installed chain means no authenticated identity, so every non-probe route
// fails closed with 401. A route mounted by another lane can therefore never be
// reachable without authentication.
func adminAuthentication(authenticate Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
				next.ServeHTTP(w, r)
				return
			}
			if authenticate == nil {
				writeAdminAuthenticationProblem(w, r, ErrNoCredential)
				return
			}
			principal, err := authenticate.Authenticate(r.Context(), r)
			if err != nil {
				writeAdminAuthenticationProblem(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// writeAdminAuthenticationProblem renders #814's typed failures with their
// stable problem codes and a generic challenge. No credential detail is echoed.
func writeAdminAuthenticationProblem(w http.ResponseWriter, r *http.Request, err error) {
	code := "invalid_credential"
	switch {
	case errors.Is(err, ErrNoCredential):
		code = "authentication_required"
	case errors.Is(err, ErrUnsupportedAuthentication):
		code = "unsupported_authentication"
	case errors.Is(err, ErrInvalidCredential):
		code = "invalid_credential"
	}
	if errors.Is(err, ErrNoCredential) {
		// RFC 6750: error="invalid_token" is only for a presented credential.
		w.Header().Set("WWW-Authenticate", `Bearer realm="router-admin"`)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer realm="router-admin", error="invalid_token"`)
	}
	writeAdminPlaneProblem(w, r, http.StatusUnauthorized, code)
}

// writeAdminPlaneProblem renders a listener-level problem document. Codes are
// fixed strings; no handler, config or peer detail is ever echoed.
func writeAdminPlaneProblem(w http.ResponseWriter, r *http.Request, status int, code string) {
	WriteAdminProblem(w, r, AdminProblemFor(r, status, code))
}

// adminErrorWriter rewrites plain-text 404/405 responses (the ones the Go mux
// produces for unmatched paths and methods) into problem documents, so every
// admin-port response has one shape.
type adminErrorWriter struct {
	http.ResponseWriter
	r           *http.Request
	wroteHeader bool
	substituted bool
}

func (w *adminErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *adminErrorWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		contentType := w.Header().Get("Content-Type")
		if contentType == "" || strings.HasPrefix(contentType, "text/plain") {
			code := "admin-not-found"
			if status == http.StatusMethodNotAllowed {
				code = "admin-method-not-allowed"
			}
			w.substituted = true
			WriteAdminProblem(w.ResponseWriter, w.r, AdminProblemFor(w.r, status, code))
			return
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *adminErrorWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.substituted {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// setAdminPlaneSecurityHeaders applies the CORS-off, no-store, no-HTML security
// header set to every admin response (ACP-N02).
func setAdminPlaneSecurityHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Referrer-Policy", "no-referrer")
	// The plane never emits Access-Control-Allow-*; strip anything a handler
	// tried to add so "CORS off" is enforced centrally.
	header.Del("Access-Control-Allow-Origin")
	header.Del("Access-Control-Allow-Credentials")
	header.Del("Access-Control-Allow-Headers")
	header.Del("Access-Control-Allow-Methods")
}

// adminHardening applies the listener-wide security headers, rejects CORS
// preflights, enforces the independent body limit (ACP-N02) and normalises
// unmatched-path bodies to problem+json.
//
// Two statuses do not come from this middleware: net/http itself answers 431
// when a request header set exceeds AdminPlane's MaxHeaderBytes, and a malformed
// request line or oversized header block can produce a 400, both with a plain
// text body written before any handler runs. They are transport-level refusals,
// documented as such in the admin-listener reference.
func adminHardening(cfg *AdminPlaneConfig) func(http.Handler) http.Handler {
	maxBody := int64(defaultAdminMaxBodyBytes)
	if cfg != nil && cfg.Limits.MaxBodyBytes > 0 {
		maxBody = int64(cfg.Limits.MaxBodyBytes)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setAdminPlaneSecurityHeaders(w)
			// One server-generated correlation ID per admin request; a
			// client-supplied X-Request-Id is never trusted or echoed.
			r = r.WithContext(WithAdminRequestID(r.Context(), AdminRequestID(r)))
			w.Header().Set(adminRequestIDHeader, AdminRequestID(r))
			w = &adminErrorWriter{ResponseWriter: w, r: r}
			if r.Method == http.MethodOptions {
				if r.Header.Get("Origin") != "" && r.Header.Get("Access-Control-Request-Method") != "" {
					writeAdminPlaneProblem(w, r, http.StatusMethodNotAllowed, "admin-cors-rejected")
					return
				}
				writeAdminPlaneProblem(w, r, http.StatusMethodNotAllowed, "admin-method-not-allowed")
				return
			}
			defer recoverAdminPlane(w, r)
			if r.Body != nil && r.Body != http.NoBody {
				if r.ContentLength > maxBody {
					writeAdminPlaneProblem(w, r, http.StatusRequestEntityTooLarge, "admin-payload-too-large")
					return
				}
				limiter := &adminBodyLimiter{body: http.MaxBytesReader(w, r.Body, maxBody)}
				r.Body = limiter
				w = &adminLimitWriter{ResponseWriter: w, r: r, limiter: limiter}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// recoverAdminPlane turns a handler panic into a problem document without
// leaking the panic value. http.ErrAbortHandler keeps its documented meaning.
func recoverAdminPlane(w http.ResponseWriter, r *http.Request) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(recovered)
	}
	log.Printf("admin plane handler panic recovered")
	writeAdminPlaneProblem(w, r, http.StatusInternalServerError, "admin-internal-error")
}

// adminBodyLimiter records whether the request body crossed the limit so the
// response can be forced to 413 even when the handler would answer 400.
type adminBodyLimiter struct {
	body     io.ReadCloser
	oversize atomic.Bool
}

func (l *adminBodyLimiter) Read(p []byte) (int, error) {
	n, err := l.body.Read(p)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			l.oversize.Store(true)
		}
	}
	return n, err
}

func (l *adminBodyLimiter) Close() error { return l.body.Close() }

// adminLimitWriter substitutes a 413 for any response produced after the body
// limit was crossed.
type adminLimitWriter struct {
	http.ResponseWriter
	r           *http.Request
	limiter     *adminBodyLimiter
	wroteHeader bool
	substituted bool
}

func (w *adminLimitWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *adminLimitWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if w.limiter.oversize.Load() && status < http.StatusBadRequest {
		w.substituted = true
		writeAdminPlaneProblem(w.ResponseWriter, w.r, http.StatusRequestEntityTooLarge, "admin-payload-too-large")
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *adminLimitWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.substituted {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// adminProxyGate enforces the reverse-proxy contract for mode proxy
// (ACP-002): non-loopback peers must be trusted proxies and must report an
// HTTPS hop. Loopback peers stay usable for port-forward and local tooling but
// still need machine credentials.
func adminProxyGate(cfg *AdminPlaneConfig) func(http.Handler) http.Handler {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.TLS.Mode), adminTLSModeProxy) {
		return func(next http.Handler) http.Handler { return next }
	}
	trusted := make([]*net.IPNet, 0, len(cfg.TLS.TrustedProxyCIDRs))
	for _, cidr := range cfg.TLS.TrustedProxyCIDRs {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
			trusted = append(trusted, network)
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := adminRemoteIP(r)
			if ip == nil || !ip.IsLoopback() {
				trustedPeer := false
				for _, network := range trusted {
					if ip != nil && network.Contains(ip) {
						trustedPeer = true
						break
					}
				}
				if !trustedPeer {
					writeAdminPlaneProblem(w, r, http.StatusForbidden, "admin-proxy-untrusted")
					return
				}
				if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
					writeAdminPlaneProblem(w, r, http.StatusForbidden, "admin-tls-required")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func adminRemoteIP(r *http.Request) net.IP {
	if r == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	return net.ParseIP(host)
}

// adminMachineOnlyGate rejects browser credential shapes before any handler or
// authenticator runs (ACP-N06): cookies, HTTP Basic and non-Bearer schemes.
// #814's authenticator chain stays authoritative for the rest of the envelope
// rules and uses the same error vocabulary.
func adminMachineOnlyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Cookie")) > 0 {
			writeAdminPlaneProblem(w, r, http.StatusUnauthorized, "unsupported_authentication")
			return
		}
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if header != "" {
			scheme, _, _ := strings.Cut(header, " ")
			switch strings.ToLower(strings.TrimSpace(scheme)) {
			case "bearer", "dpop":
			default:
				writeAdminPlaneProblem(w, r, http.StatusUnauthorized, "unsupported_authentication")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// AdminCertificateManager is the router-native certificate seam #810 supplies to
// the listener. The TLS lane adapts its ACME manager to it (the adapter struct
// is named AdminCertificateSource there). The listener asks the manager whether
// it serves the SNI and then fetches the certificate, all inside its single
// handshake hook, before applying the #814 client-CA snapshot.
type AdminCertificateManager interface {
	// GetCertificate returns the certificate for the hello, or an error when the
	// router cannot serve it.
	GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
	// CheckSNI reports whether hello.ServerName is a name this source serves.
	CheckSNI(string) bool
}

// AdminClientCASource is the client-CA snapshot seam #814 supplies to the
// listener: the handshake verifies client certificates against the current
// immutable snapshot, and every reload is delivered through OnReload. #814's
// AdminClientCAStore satisfies it.
type AdminClientCASource interface {
	// Snapshot returns the current immutable client trust pool.
	Snapshot() *x509.CertPool
	// OnReload registers a callback and immediately delivers the current
	// snapshot.
	OnReload(func(*x509.CertPool))
}

func adminCASnapshot(source AdminClientCASource) *x509.CertPool {
	if source == nil {
		return nil
	}
	return source.Snapshot()
}

func (p *AdminPlane) clientCASnapshot() *x509.CertPool {
	if p == nil {
		return nil
	}
	return adminCASnapshot(p.ClientCASource())
}

// adminFixedCASource is the handshake-side view of the installed source: it
// holds the pool the source last published.
// adminCASourceHolder wraps the interface so the atomic pointer has one
// concrete type for every store (atomic.Value rejects varying types).
type adminCASourceHolder struct{ source AdminClientCASource }

type adminFixedCASource struct{ pool *x509.CertPool }

func (s *adminFixedCASource) Snapshot() *x509.CertPool { return s.pool }

func (s *adminFixedCASource) OnReload(hook func(*x509.CertPool)) {
	if hook != nil {
		hook(s.pool)
	}
}

// AdminPeerCertificates returns the client certificate chain the admin TLS
// handshake verified. It is nil for plaintext, reverse-proxy-terminated and
// non-TLS requests, and it never consults forwarded certificate headers: the
// #814 SAN/SPIFFE mapping therefore only ever sees a chain verified by native
// TLS.
func AdminPeerCertificates(r *http.Request) []*x509.Certificate {
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return nil
	}
	return r.TLS.VerifiedChains[0]
}

// AdminPlane is the second http.Server lifecycle (ACP-001/ACP-002). It binds,
// serves and drains independently of the serving server; an accept-loop failure
// is reported on Err and never stops inference.
type AdminPlane struct {
	cfg     AdminPlaneConfig
	handler http.Handler
	// clientCAS holds the AdminClientCASource under an atomic so the reload
	// callback and every handshake see one consistent source (no data race).
	clientCAS atomic.Pointer[adminCASourceHolder]
	certMu    sync.Mutex
	certSrc   AdminCertificateManager
	tlsCfg    *tls.Config

	mu       sync.Mutex
	srv      *http.Server
	ln       net.Listener
	err      chan error
	reloadMu sync.Mutex
	reload   context.CancelFunc
	reloadWG sync.WaitGroup
}

// adminCAReloader is the optional reload capability of an AdminClientCASource
// (#828's client CA store has it). When admin.authentication.mtls.reload_interval
// is set, the plane re-reads the bundles on that interval so a rotated bundle
// applies without a restart.
type adminCAReloader interface {
	Reload() error
}

// NewAdminPlane validates the listener policy and builds the server. TLS files
// are loaded here, so a router-native misconfiguration fails before serving
// starts; the plaintext/loopback policy is already enforced by Validate.
func NewAdminPlane(cfg *AdminPlaneConfig, handler http.Handler) (*AdminPlane, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, fmt.Errorf("admin plane is not enabled")
	}
	if handler == nil {
		return nil, fmt.Errorf("admin plane requires a handler")
	}
	local := AdminPlaneConfig(*cfg)
	p := &AdminPlane{
		cfg:     local,
		handler: handler,
		err:     make(chan error, 1),
	}
	// A fixed capacity avoids deriving an allocation size from configuration.
	caPaths := make([]string, 0, 2)
	if path := strings.TrimSpace(local.TLS.ClientCAFile); path != "" {
		caPaths = append(caPaths, path)
	}
	caPaths = append(caPaths, local.Authentication.MTLS.CAFiles...)
	switch strings.ToLower(strings.TrimSpace(local.TLS.ClientAuth)) {
	case adminClientAuthOptional, adminClientAuthRequire:
		// #828's store owns the trust anchors and reloads them; an unreadable
		// or empty bundle fails startup instead of trusting nothing silently.
		store, err := NewAdminClientCAStore(caPaths)
		if err != nil {
			return nil, err
		}
		p.clientCAS.Store(&adminCASourceHolder{source: store})
	}
	if strings.EqualFold(strings.TrimSpace(local.TLS.Mode), adminTLSModeRouter) {
		tlsCfg, err := p.routerTLSConfig()
		if err != nil {
			return nil, err
		}
		p.tlsCfg = tlsCfg
	}
	if strings.EqualFold(strings.TrimSpace(local.TLS.Mode), adminTLSModeOff) {
		loopback, err := adminListenIsLoopback(local.Listen)
		if err != nil {
			return nil, err
		}
		if !loopback {
			if !local.AllowPlaintextNonLoopback {
				return nil, fmt.Errorf("admin.listen %q is not loopback and TLS is off", local.Listen)
			}
			// The override is deliberate, so say so once at startup.
			log.Printf("WARNING: admin.allow_plaintext_non_loopback is set; the admin listener on %s carries plaintext credentials across the network. Terminate TLS in front of it and remove the override.", local.Listen)
		}
	}
	return p, nil
}

// ClientCASource returns the #828 client-CA source the handshake verifies
// against, so #814 can reload client trust without restarting the listener.
func (p *AdminPlane) ClientCASource() AdminClientCASource {
	if p == nil {
		return nil
	}
	holder := p.clientCAS.Load()
	if holder == nil {
		return nil
	}
	return holder.source
}

// SetCertificateSource installs #810's certificate manager. It must be called
// before Start; the manager's GetCertificate/CheckSNI run inside the listener's
// single GetConfigForClient, ahead of the client-CA snapshot and ClientAuth.
func (p *AdminPlane) SetCertificateSource(src AdminCertificateManager) error {
	if p == nil || src == nil {
		return fmt.Errorf("admin certificate source is required")
	}
	if p.tlsCfg == nil || !strings.EqualFold(strings.TrimSpace(p.cfg.TLS.CertificateSource), adminCertificateSourceACME) {
		return fmt.Errorf("an admin certificate source requires admin.tls.mode router with certificate_source acme")
	}
	p.certMu.Lock()
	p.certSrc = src
	p.certMu.Unlock()
	return nil
}

// SetClientCASource installs #814's client-CA source. It must be called before
// Start; every source reload is folded into the same snapshot the handshake
// reads, so handshake and per-request revalidation can never disagree.
func (p *AdminPlane) SetClientCASource(src AdminClientCASource) {
	if p == nil || src == nil {
		return
	}
	src.OnReload(func(pool *x509.CertPool) {
		p.clientCAS.Store(&adminCASourceHolder{source: &adminFixedCASource{pool: pool}})
	})
}

func (p *AdminPlane) routerTLSConfig() (*tls.Config, error) {
	useACME := strings.EqualFold(strings.TrimSpace(p.cfg.TLS.CertificateSource), adminCertificateSourceACME)
	var cert tls.Certificate
	if !useACME {
		loaded, err := tls.LoadX509KeyPair(p.cfg.TLS.CertFile, p.cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load admin TLS certificate: %w", err)
		}
		cert = loaded
	}
	minVersion := uint16(tls.VersionTLS12)
	if strings.TrimSpace(p.cfg.TLS.MinVersion) == adminTLSMinVersion13 {
		minVersion = tls.VersionTLS13
	}
	clientAuth := tls.NoClientCert
	switch strings.ToLower(strings.TrimSpace(p.cfg.TLS.ClientAuth)) {
	case adminClientAuthOptional:
		clientAuth = tls.VerifyClientCertIfGiven
	case adminClientAuthRequire:
		clientAuth = tls.RequireAndVerifyClientCert
	}
	cfg := &tls.Config{
		MinVersion: minVersion,
		ClientAuth: clientAuth,
		ClientCAs:  p.clientCASnapshot(),
		NextProtos: []string{"http/1.1"},
	}
	if !useACME {
		cfg.Certificates = []tls.Certificate{cert}
	}
	// One handshake hook for the listener: ask the certificate source (#810)
	// first, then apply the current client-CA snapshot (#814) so a reload
	// applies to new connections without restarting the listener.
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		current := cfg.Clone()
		current.GetConfigForClient = nil
		p.certMu.Lock()
		source := p.certSrc
		p.certMu.Unlock()
		if source != nil {
			if !source.CheckSNI(hello.ServerName) {
				return nil, fmt.Errorf("admin listener does not serve the requested SNI")
			}
			if _, err := source.GetCertificate(hello); err != nil {
				return nil, err
			}
			current.Certificates = nil
			current.GetCertificate = source.GetCertificate
		}
		current.ClientCAs = p.clientCASnapshot()
		return current, nil
	}
	return cfg, nil
}

// Start binds the admin listener and serves in the background. The returned
// error is a bind failure; accept-loop failures after a successful bind are
// reported on Err.
func (p *AdminPlane) Start() error {
	if p == nil {
		return fmt.Errorf("admin plane is not enabled")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		return fmt.Errorf("admin plane already started")
	}
	if p.tlsCfg != nil && strings.EqualFold(strings.TrimSpace(p.cfg.TLS.CertificateSource), adminCertificateSourceACME) {
		p.certMu.Lock()
		source := p.certSrc
		p.certMu.Unlock()
		if source == nil {
			return fmt.Errorf("admin.tls.certificate_source acme requires an installed certificate source before the listener starts")
		}
	}
	ln, err := net.Listen("tcp", p.cfg.Listen)
	if err != nil {
		return fmt.Errorf("admin listener: %w", err)
	}
	if p.tlsCfg != nil {
		ln = tls.NewListener(ln, p.tlsCfg)
	}
	srv := &http.Server{
		Handler:           p.handler,
		ReadHeaderTimeout: adminLimitDuration(p.cfg.Limits.ReadHeaderTimeoutMS, defaultAdminReadHeaderTimeoutMS),
		ReadTimeout:       adminLimitDuration(p.cfg.Limits.ReadTimeoutMS, defaultAdminReadTimeoutMS),
		WriteTimeout:      adminLimitDuration(p.cfg.Limits.WriteTimeoutMS, defaultAdminWriteTimeoutMS),
		IdleTimeout:       adminLimitDuration(p.cfg.Limits.IdleTimeoutMS, defaultAdminIdleTimeoutMS),
		MaxHeaderBytes:    p.cfg.Limits.MaxHeaderBytes,
	}
	p.srv = srv
	p.ln = ln
	p.startClientCAReload()
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case p.err <- serveErr:
			default:
			}
		}
	}()
	return nil
}

func adminLimitDuration(ms, fallback int) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return time.Duration(fallback) * time.Millisecond
}

// startClientCAReload schedules the client-CA bundle reload when the operator
// configured an interval and the source can reload. A reload failure keeps the
// last known good trust (the #828 store's contract) and logs without secrets.
func (p *AdminPlane) startClientCAReload() {
	interval := adminReloadInterval(p.cfg.Authentication.MTLS.ReloadInterval)
	if interval <= 0 {
		return
	}
	source, _ := p.ClientCASource().(adminCAReloader)
	if source == nil {
		log.Printf("admin client CA reload_interval is set but the installed source cannot reload; rotation needs a restart")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.reloadMu.Lock()
	p.reload = cancel
	p.reloadMu.Unlock()
	p.reloadWG.Add(1)
	go func() {
		defer p.reloadWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := source.Reload(); err != nil {
					// Never log the bundle path or the underlying parse error
					// text; trust stays on the last known good snapshot.
					log.Printf("admin client CA reload failed; keeping the last known good trust")
				}
			}
		}
	}()
}

func adminReloadInterval(raw string) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || parsed < time.Second {
		return 0
	}
	return parsed
}

// Addr returns the resolved listen address after Start (useful with ":0").
func (p *AdminPlane) Addr() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// Err reports accept-loop failures after a successful bind.
func (p *AdminPlane) Err() <-chan error {
	if p == nil {
		return nil
	}
	return p.err
}

// Shutdown drains the admin listener with its own budget. Serving shutdown is
// a separate call with its own deadline.
func (p *AdminPlane) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.reloadMu.Lock()
	cancelReload := p.reload
	p.reload = nil
	p.reloadMu.Unlock()
	p.mu.Lock()
	srv := p.srv
	p.mu.Unlock()
	if cancelReload != nil {
		cancelReload()
		p.reloadWG.Wait()
	}
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// ShutdownTimeout is the admin drain budget from config.
func (p *AdminPlane) ShutdownTimeout() time.Duration {
	if p == nil {
		return time.Duration(defaultAdminShutdownTimeoutMS) * time.Millisecond
	}
	return adminLimitDuration(p.cfg.Limits.ShutdownTimeoutMS, defaultAdminShutdownTimeoutMS)
}

// initAdminPlane builds the admin route registry and handler for this service.
// It is a no-op unless admin.enabled is true, which keeps v4.7 configurations
// byte-identical.
func (s *Service) initAdminPlane() {
	if s == nil || s.cfg == nil || !s.cfg.Admin.Enabled {
		return
	}
	s.adminMux = newAdminMux()
	s.adminMux.HandleFunc("GET /healthz", s.handleAdminPlaneHealth)
	s.adminMux.HandleFunc("GET /readyz", s.handleAdminPlaneHealth)
	// Nothing else is registered here yet: /admin/v1 mounts through
	// RegisterAdminRoutes (#811) and /metrics lands with its slice. The
	// catch-all keeps every other path a problem+json 404 so the plane never
	// serves HTML or another lane's routes (ACP-N06).
	s.adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeAdminPlaneProblem(w, r, http.StatusNotFound, "admin-not-found")
	})
}

// handleAdminPlaneHealth reports admin-plane state only (ACP-006): no version,
// no commit, no config detail. Serving readiness never consults it.
func (s *Service) handleAdminPlaneHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// AdminHandler returns the hardened admin-listener handler, or nil when the
// admin plane is disabled.
func (s *Service) AdminHandler() http.Handler {
	if s == nil || !s.adminPlaneEnabled() {
		return nil
	}
	return s.adminMux.handler(&s.cfg.Admin, s.AdminAuthenticator())
}

// RegisterAdminRoutes mounts routes on the admin listener. It is the hook
// #811/#813/#815 register through and the only supported way for another lane
// to add an admin-port route.
func (s *Service) RegisterAdminRoutes(register func(*AdminMux)) {
	if s == nil || register == nil || !s.adminPlaneEnabled() || s.adminMux == nil {
		return
	}
	register(s.adminMux)
}

// SetAdminAuthenticator installs #814's machine authentication chain. The
// listener runs it before dispatch and publishes the resulting principal with
// WithPrincipal; no other identity source exists on the admin port. Install it
// before the plane builds its handler (the binary does this at startup).
func (s *Service) SetAdminAuthenticator(authenticate Authenticator) {
	if s == nil || authenticate == nil {
		return
	}
	s.adminAuthMu.Lock()
	s.adminAuthenticate = authenticate
	s.adminAuthMu.Unlock()
}

// AdminAuthenticator returns the installed chain, or nil.
func (s *Service) AdminAuthenticator() Authenticator {
	if s == nil {
		return nil
	}
	s.adminAuthMu.Lock()
	defer s.adminAuthMu.Unlock()
	return s.adminAuthenticate
}

// AdminListenAddr returns the configured admin listen address, or "" when the
// plane is disabled.
func (s *Service) AdminListenAddr() string {
	if s == nil || !s.adminPlaneEnabled() {
		return ""
	}
	return strings.TrimSpace(s.cfg.Admin.Listen)
}

// SetAdminClientCASource installs #814's client-CA source for the admin
// listener. The binary applies it when it starts the plane.
func (s *Service) SetAdminClientCASource(src AdminClientCASource) {
	if s == nil || src == nil {
		return
	}
	s.adminCASourceMu.Lock()
	s.adminCASource = src
	s.adminCASourceMu.Unlock()
}

// AdminClientCASource returns the installed client-CA source, or nil.
func (s *Service) AdminClientCASource() AdminClientCASource {
	if s == nil {
		return nil
	}
	s.adminCASourceMu.Lock()
	defer s.adminCASourceMu.Unlock()
	return s.adminCASource
}

func (s *Service) adminPlaneEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Admin.Enabled
}

// adminListenerSettingsDiffer reports whether a config reload changed a
// setting that the already-bound admin listener cannot apply. Route-level
// behaviour reloads as on the serving port; the listener address, TLS material
// and deadlines need a restart, and the operator sees one notice instead of a
// half-applied plane.
func adminListenerSettingsDiffer(current, next AdminPlaneConfig) bool {
	if current.Enabled != next.Enabled {
		return true
	}
	if strings.TrimSpace(current.Listen) != strings.TrimSpace(next.Listen) {
		return true
	}
	if current.AllowPlaintextNonLoopback != next.AllowPlaintextNonLoopback {
		return true
	}
	if strings.TrimSpace(current.TLS.Mode) != strings.TrimSpace(next.TLS.Mode) ||
		strings.TrimSpace(current.TLS.CertFile) != strings.TrimSpace(next.TLS.CertFile) ||
		strings.TrimSpace(current.TLS.KeyFile) != strings.TrimSpace(next.TLS.KeyFile) ||
		strings.TrimSpace(current.TLS.MinVersion) != strings.TrimSpace(next.TLS.MinVersion) ||
		strings.TrimSpace(current.TLS.ClientCAFile) != strings.TrimSpace(next.TLS.ClientCAFile) ||
		strings.TrimSpace(current.TLS.ClientAuth) != strings.TrimSpace(next.TLS.ClientAuth) {
		return true
	}
	if len(current.TLS.TrustedProxyCIDRs) != len(next.TLS.TrustedProxyCIDRs) {
		return true
	}
	for i := range current.TLS.TrustedProxyCIDRs {
		if strings.TrimSpace(current.TLS.TrustedProxyCIDRs[i]) != strings.TrimSpace(next.TLS.TrustedProxyCIDRs[i]) {
			return true
		}
	}
	if len(current.Authentication.MTLS.CAFiles) != len(next.Authentication.MTLS.CAFiles) {
		return true
	}
	for i := range current.Authentication.MTLS.CAFiles {
		if strings.TrimSpace(current.Authentication.MTLS.CAFiles[i]) != strings.TrimSpace(next.Authentication.MTLS.CAFiles[i]) {
			return true
		}
	}
	return current.Limits != next.Limits
}
