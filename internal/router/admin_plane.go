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
	"os"
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
func (m *AdminMux) handler(cfg *AdminPlaneConfig) http.Handler {
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
	h = adminMachineOnlyGate(h)
	h = adminProxyGate(cfg)(h)
	h = adminHardening(cfg)(h)
	return h
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
				w = &adminLimitWriter{ResponseWriter: w, limiter: limiter}
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
		writeAdminPlaneProblem(w.ResponseWriter, nil, http.StatusRequestEntityTooLarge, "admin-payload-too-large")
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

// AdminCertificateSource is the router-native certificate seam #810 supplies to
// the listener. Its manager calls the listener's single handshake hook, so the
// listener asks the source whether it serves the SNI and then fetches the
// certificate before applying the #814 client-CA snapshot.
type AdminCertificateSource interface {
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

// AdminClientCASnapshot is the listener's built-in AdminClientCASource. It
// holds the immutable client-CA pool the admin TLS handshake verifies against;
// Snapshot returns a pool that is never mutated after publication, so the
// handshake always sees a consistent snapshot, and Store publishes a reload.
type AdminClientCASnapshot struct {
	pool  atomic.Pointer[x509.CertPool]
	mu    sync.Mutex
	hooks []func(*x509.CertPool)
}

// Snapshot returns the current immutable CA pool, or nil when no CA is
// configured.
func (s *AdminClientCASnapshot) Snapshot() *x509.CertPool {
	if s == nil {
		return nil
	}
	return s.pool.Load()
}

// OnReload registers a callback and immediately delivers the current snapshot.
func (s *AdminClientCASnapshot) OnReload(hook func(*x509.CertPool)) {
	if s == nil || hook == nil {
		return
	}
	s.mu.Lock()
	s.hooks = append(s.hooks, hook)
	current := s.pool.Load()
	s.mu.Unlock()
	hook(current)
}

// Store publishes a new immutable CA pool and notifies reload hooks.
func (s *AdminClientCASnapshot) Store(pool *x509.CertPool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.pool.Store(pool)
	hooks := append([]func(*x509.CertPool){}, s.hooks...)
	s.mu.Unlock()
	for _, hook := range hooks {
		hook(pool)
	}
}

// LoadPEMFiles builds a fresh pool from PEM bundles and publishes it. An
// unreadable or empty bundle is an error and leaves the previous snapshot in
// place (last known good), matching the #814 reload contract.
func (s *AdminClientCASnapshot) LoadPEMFiles(paths []string) error {
	if s == nil {
		return nil
	}
	pool, err := loadAdminCAPool(paths)
	if err != nil {
		return err
	}
	s.Store(pool)
	return nil
}

func loadAdminCAPool(paths []string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	added := 0
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read admin client CA bundle: %w", err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("admin client CA bundle %q contains no valid certificates", path)
		}
		added++
	}
	if added == 0 {
		return nil, fmt.Errorf("no admin client CA bundle configured")
	}
	return pool, nil
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
	cfg      AdminPlaneConfig
	handler  http.Handler
	clientCA *AdminClientCASnapshot
	certMu   sync.Mutex
	certSrc  AdminCertificateSource
	tlsCfg   *tls.Config

	mu  sync.Mutex
	srv *http.Server
	ln  net.Listener
	err chan error
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
		cfg:      local,
		handler:  handler,
		clientCA: &AdminClientCASnapshot{},
		err:      make(chan error, 1),
	}
	caPaths := make([]string, 0, 1+len(local.Authentication.MTLS.CAFiles))
	if path := strings.TrimSpace(local.TLS.ClientCAFile); path != "" {
		caPaths = append(caPaths, path)
	}
	caPaths = append(caPaths, local.Authentication.MTLS.CAFiles...)
	switch strings.ToLower(strings.TrimSpace(local.TLS.ClientAuth)) {
	case adminClientAuthOptional, adminClientAuthRequire:
		if err := p.clientCA.LoadPEMFiles(caPaths); err != nil {
			return nil, err
		}
	}
	if strings.EqualFold(strings.TrimSpace(local.TLS.Mode), adminTLSModeRouter) {
		tlsCfg, err := p.routerTLSConfig()
		if err != nil {
			return nil, err
		}
		p.tlsCfg = tlsCfg
	}
	if !local.AllowPlaintextNonLoopback && strings.EqualFold(strings.TrimSpace(local.TLS.Mode), adminTLSModeOff) {
		if loopback, err := adminListenIsLoopback(local.Listen); err == nil && !loopback {
			return nil, fmt.Errorf("admin.listen %q is not loopback and TLS is off", local.Listen)
		}
	}
	return p, nil
}

// ClientCASnapshot returns the shared CA snapshot so #814 can reload client
// trust without restarting the listener.
func (p *AdminPlane) ClientCASnapshot() *AdminClientCASnapshot {
	if p == nil {
		return nil
	}
	return p.clientCA
}

// SetCertificateSource installs #810's certificate manager. It must be called
// before Start; the manager's GetCertificate/CheckSNI run inside the listener's
// single GetConfigForClient, ahead of the client-CA snapshot and ClientAuth.
func (p *AdminPlane) SetCertificateSource(src AdminCertificateSource) error {
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
		p.clientCA.Store(pool)
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
		ClientCAs:  p.clientCA.Snapshot(),
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
		current.ClientCAs = p.clientCA.Snapshot()
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
	p.mu.Lock()
	srv := p.srv
	p.mu.Unlock()
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
	s.adminClientCA = &AdminClientCASnapshot{}
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
	return s.adminMux.handler(&s.cfg.Admin)
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

// AdminListenAddr returns the configured admin listen address, or "" when the
// plane is disabled.
func (s *Service) AdminListenAddr() string {
	if s == nil || !s.adminPlaneEnabled() {
		return ""
	}
	return strings.TrimSpace(s.cfg.Admin.Listen)
}

// AdminClientCASnapshot returns the shared client-CA snapshot #814 reloads.
func (s *Service) AdminClientCASnapshot() *AdminClientCASnapshot {
	if s == nil {
		return nil
	}
	return s.adminClientCA
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
