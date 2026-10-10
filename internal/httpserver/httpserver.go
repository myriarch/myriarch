// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package httpserver assembles the HTTP server of myriarchd: REST modules under
// /api/v1, the OpAMP endpoint, the embedded UI, /healthz, /readyz, /metrics and
// the public license texts under /licenses, with the standard middleware chain.
//
// Middleware order for /api/v1 routes: request id (X-Request-Id) -> real IP
// (trusted proxies) -> access log (slog) -> metrics
// (myriarch_http_requests_total{route,method,code}, ..._duration_seconds) ->
// panic recovery (500 Problem) -> body limit (Route.MaxBodyBytes, default
// 1 MiB) -> authentication (Auth mode) -> CSRF (cookie sessions, mutating
// methods) -> permission (Route.Permission) -> handler.
//
// Every pattern registered on a mux has a declared access decision
// (Server.Routes): REST routes carry their permission or "public"/"custom",
// the infrastructure routes are classified as public, agent-enrollment,
// metrics (network restricted) or ui. A test enumerates them, so a route can
// not be added without a decision.
//
// The response writer wrappers pass http.Hijacker (WebSocket upgrade of the
// OpAMP endpoint) and http.Flusher (SSE) through and implement Unwrap for
// http.ResponseController. The http.Server has no global Read/WriteTimeout
// (long lived WebSocket and SSE connections); only ReadHeaderTimeout.
package httpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
	"github.com/myriarch/myriarch/internal/metrics"
)

// DefaultBodyLimit is the default request body limit of API routes.
const DefaultBodyLimit = 1 << 20

// Authenticator resolves principals and checks CSRF (implemented by
// *authn.Service).
type Authenticator interface {
	Authenticate(r *http.Request) (*authz.Principal, error)
	CheckCSRF(r *http.Request, p *authz.Principal) error
}

// Deps are the server dependencies.
type Deps struct {
	Auth    Authenticator
	Modules []api.Module
	// OpAMPPath (config.OpAMPConfig.Path) and handler.
	OpAMPPath   string
	OpAMP       http.Handler
	ConnContext func(ctx context.Context, c net.Conn) context.Context
	// UI serves the SPA for all remaining paths.
	UI http.Handler
	// Licenses serves /licenses and everything below /licenses/ (project license and
	// third-party notices). The routes are public by design and are registered
	// before the UI catch-all.
	Licenses http.Handler
	// Ready is used by /readyz (DB ping, migrations applied, bus connected).
	Ready func(ctx context.Context) error
	// Metrics is the /metrics handler (served on ListenAddr if
	// MetricsListenAddr is empty, else on its own listener).
	Metrics    http.Handler
	Logger     *slog.Logger
	Registerer prometheus.Registerer
}

// Server is the HTTP server.
type Server struct {
	cfg     config.ServerConfig
	deps    Deps
	log     *slog.Logger
	handler http.Handler
	mux     *http.ServeMux
	ip      *realip.Resolver

	reqTotal *prometheus.CounterVec
	reqDur   *prometheus.HistogramVec

	started chan struct{}
	addr    atomic.Pointer[net.Addr]
	mAddr   atomic.Pointer[net.Addr]

	// catchAll is the pattern registered for unmatched /api/v1/ paths.
	catchAll string
	methods  map[string]bool

	decisions   []RouteDecision
	publicURL   *url.URL
	readyMu     sync.Mutex
	readyAt     time.Time
	readyErr    error
	readyFlight singleflight.Group
	// metricsDecisions are the patterns of the separate metrics listener.
	metricsDecisions []RouteDecision
}

// Access decisions of RouteDecision.Kind.
const (
	// DecisionPermission: authentication plus Permission (REST routes).
	DecisionPermission = "permission"
	// DecisionPublic: reachable without credentials by design (login, probes).
	DecisionPublic = "public"
	// DecisionCustom: the handler authenticates itself (webhook secret,
	// signed download tokens).
	DecisionCustom = "custom"
	// DecisionAgent: OpAMP endpoint, enrollment token / client certificate.
	DecisionAgent = "agent-enrollment"
	// DecisionMetrics: /metrics, restricted by the network (NetworkPolicy,
	// separate listener).
	DecisionMetrics = "metrics"
	// DecisionUI: static UI files.
	DecisionUI = "ui"
)

// RouteDecision is the declared access decision of one mux pattern.
type RouteDecision struct {
	Pattern    string
	Kind       string
	Permission authz.Permission
}

// Routes returns the access decisions of all registered patterns.
func (s *Server) Routes() []RouteDecision { return append([]RouteDecision(nil), s.decisions...) }

// MuxPatterns returns every pattern registered on the main mux (the
// enumeration test compares it with Routes).
func (s *Server) MuxPatterns() []string {
	out := make([]string, 0, len(s.decisions))
	for _, d := range s.decisions {
		out = append(out, d.Pattern)
	}
	sort.Strings(out)
	return out
}

var apiMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// New validates all module routes (duplicate operationIds and conflicting
// patterns are errors) and builds the handler.
func New(cfg config.ServerConfig, deps Deps) (*Server, error) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	nets, err := realip.ParseTrusted(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("httpserver: server.trustedProxies: %w", err)
	}
	s := &Server{
		cfg: cfg, deps: deps, log: logger.With("component", "http"), mux: http.NewServeMux(), ip: realip.New(nets),
		started: make(chan struct{}), catchAll: api.BasePath + "/",
	}
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Host != "" {
		s.publicURL = u
	}
	s.reqTotal = metrics.Register(deps.Registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_http_requests_total", Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"}))
	s.reqDur = metrics.Register(deps.Registerer, prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "myriarch_http_request_duration_seconds", Help: "HTTP request duration by route and method.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"route", "method"}))

	if err := s.registerModules(); err != nil {
		return nil, err
	}
	if err := s.registerInfra(); err != nil {
		return nil, err
	}
	s.handler = withDeadlines(s.mux)
	return s, nil
}

// register adds a handler to the mux and converts ServeMux panics (pattern
// conflicts) into errors. The access decision is recorded with it.
func (s *Server) register(pattern string, h http.Handler, d RouteDecision) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("httpserver: cannot register %q: %v", pattern, r)
		}
	}()
	s.mux.Handle(pattern, h)
	d.Pattern = pattern
	s.decisions = append(s.decisions, d)
	return nil
}

func (s *Server) registerModules() error {
	opIDs := map[string]string{}
	for _, m := range s.deps.Modules {
		if m == nil {
			continue
		}
		for _, rt := range m.Routes() {
			where := fmt.Sprintf("module %q route %s %s", m.Name(), rt.Method, rt.Pattern)
			switch {
			case rt.Method == "" || strings.ContainsAny(rt.Method, " \t"):
				return fmt.Errorf("httpserver: %s: invalid method", where)
			case !strings.HasPrefix(rt.Pattern, "/"):
				return fmt.Errorf("httpserver: %s: pattern must start with \"/\"", where)
			case rt.Handler == nil:
				return fmt.Errorf("httpserver: %s: handler is nil", where)
			case rt.OperationID == "":
				return fmt.Errorf("httpserver: %s: operationId is required", where)
			}
			if prev, dup := opIDs[rt.OperationID]; dup {
				return fmt.Errorf("httpserver: %s: duplicate operationId %q (also %s)", where, rt.OperationID, prev)
			}
			opIDs[rt.OperationID] = where
			if rt.Auth == api.AuthRequired {
				if rt.Permission == "" {
					return fmt.Errorf("httpserver: %s: authenticated route without permission", where)
				}
				if s.deps.Auth == nil {
					return fmt.Errorf("httpserver: %s: authenticated route but no Authenticator configured", where)
				}
			}
			dec := RouteDecision{Kind: DecisionPermission, Permission: rt.Permission}
			switch rt.Auth {
			case api.AuthPublic:
				dec = RouteDecision{Kind: DecisionPublic}
			case api.AuthCustom:
				dec = RouteDecision{Kind: DecisionCustom}
			}
			if err := s.register(rt.Method+" "+api.BasePath+rt.Pattern, s.apiHandler(rt), dec); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
	}
	// unmatched /api/v1/... paths: JSON problem instead of the SPA
	return s.register(s.catchAll, s.base("api_not_found", slog.LevelInfo, http.HandlerFunc(s.apiNotFound)), RouteDecision{Kind: DecisionPublic})
}

func (s *Server) registerInfra() error {
	if s.deps.OpAMP != nil && s.deps.OpAMPPath != "" {
		if err := s.register(s.deps.OpAMPPath, s.base("opamp", slog.LevelDebug, s.deps.OpAMP), RouteDecision{Kind: DecisionAgent}); err != nil {
			return err
		}
	}
	if err := s.register("GET /healthz", s.base("healthz", slog.LevelDebug, http.HandlerFunc(s.healthz)), RouteDecision{Kind: DecisionPublic}); err != nil {
		return err
	}
	if err := s.register("GET /readyz", s.base("readyz", slog.LevelDebug, http.HandlerFunc(s.readyz)), RouteDecision{Kind: DecisionPublic}); err != nil {
		return err
	}
	if s.deps.Metrics != nil && s.cfg.MetricsListenAddr == "" {
		if err := s.register("GET /metrics", s.base("metrics", slog.LevelDebug, s.deps.Metrics), RouteDecision{Kind: DecisionMetrics}); err != nil {
			return err
		}
	}
	if s.deps.Licenses != nil {
		for _, pattern := range []string{"GET /licenses", "GET /licenses/"} {
			if err := s.register(pattern, s.base("licenses", slog.LevelDebug, s.deps.Licenses), RouteDecision{Kind: DecisionPublic}); err != nil {
				return err
			}
		}
	}
	if s.deps.UI != nil {
		return s.register("/", s.base("ui", slog.LevelDebug, s.deps.UI), RouteDecision{Kind: DecisionUI})
	}
	return nil
}

// Handler returns the root handler (for tests).
func (s *Server) Handler() http.Handler { return s.handler }

// Addr returns the listener address once Run is listening (nil before).
func (s *Server) Addr() net.Addr {
	if a := s.addr.Load(); a != nil {
		return *a
	}
	return nil
}

// MetricsAddr returns the address of the separate metrics listener (nil if
// none).
func (s *Server) MetricsAddr() net.Addr {
	if a := s.mAddr.Load(); a != nil {
		return *a
	}
	return nil
}

// Started is closed when the main listener accepts connections.
func (s *Server) Started() <-chan struct{} { return s.started }

// ------------------------------------------------------------------ run ---

func (s *Server) tlsConfig() (*tls.Config, error) {
	t := s.cfg.TLS
	if t.CertFile == "" {
		return nil, nil
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if t.ClientCAFile != "" {
		pem, err := os.ReadFile(t.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("httpserver: read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("httpserver: client CA file %s contains no certificate", t.ClientCAFile)
		}
		c.ClientCAs = pool
		// optional: the OpAMP handler enforces opamp.requireClientCert, REST
		// and UI clients (browsers) connect without a certificate
		c.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return c, nil
}

// Run listens and serves until ctx is done, then shuts down gracefully
// (cfg.ShutdownTimeout). WebSocket and SSE connections are hijacked/long
// lived: the OpAMP server is shut down separately before Run returns.
func (s *Server) Run(ctx context.Context) error {
	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("httpserver: listen %s: %w", s.cfg.ListenAddr, err)
	}
	a := ln.Addr()
	s.addr.Store(&a)

	main := &http.Server{
		Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: maxHeaderBytes,
		ConnContext: s.deps.ConnContext, TLSConfig: tlsCfg,
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	servers := []*http.Server{main}
	errCh := make(chan error, 2)
	go func() {
		var err error
		if tlsCfg != nil {
			err = main.ServeTLS(ln, s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
		} else {
			err = main.Serve(ln)
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("httpserver: serve: %w", err)
		}
	}()

	if s.deps.Metrics != nil && s.cfg.MetricsListenAddr != "" {
		mln, err := net.Listen("tcp", s.cfg.MetricsListenAddr)
		if err != nil {
			_ = main.Close()
			return fmt.Errorf("httpserver: listen metrics %s: %w", s.cfg.MetricsListenAddr, err)
		}
		ma := mln.Addr()
		s.mAddr.Store(&ma)
		mmux := http.NewServeMux()
		mmux.Handle("GET /metrics", s.deps.Metrics)
		mmux.HandleFunc("GET /healthz", s.healthz)
		s.metricsDecisions = []RouteDecision{{Pattern: "GET /metrics", Kind: DecisionMetrics}, {Pattern: "GET /healthz", Kind: DecisionPublic}}
		msrv := &http.Server{Handler: withDeadlines(mmux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: maxHeaderBytes, ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug)}
		servers = append(servers, msrv)
		go func() {
			if err := msrv.Serve(mln); !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("httpserver: serve metrics: %w", err)
			}
		}()
	}
	s.log.Info("http server listening", "addr", ln.Addr().String(), "tls", tlsCfg != nil)
	close(s.started)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}
	timeout := s.cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	sctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, srv := range servers {
			if err := srv.Shutdown(sctx); err != nil {
				s.log.Warn("http server shutdown timed out, closing connections", "error", err)
				_ = srv.Close()
			}
		}
	}()
	<-done
	return runErr
}

// ------------------------------------------------------------ handlers ---

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.readiness(); err != nil {
		// the cause is logged; unauthenticated callers only learn the state
		s.log.Warn("readiness check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	_, _ = w.Write([]byte("ready\n"))
}

// readiness returns the result of Deps.Ready. /readyz is public: the result is cached for
// readyCacheTTL and concurrent checks are coalesced, so that at most one database round trip per
// period and replica happens.
func (s *Server) readiness() error {
	if s.deps.Ready == nil {
		return nil
	}
	s.readyMu.Lock()
	if !s.readyAt.IsZero() && time.Since(s.readyAt) < readyCacheTTL {
		err := s.readyErr
		s.readyMu.Unlock()
		return err
	}
	s.readyMu.Unlock()
	v, _, _ := s.readyFlight.Do("ready", func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := s.deps.Ready(ctx)
		s.readyMu.Lock()
		s.readyErr, s.readyAt = err, time.Now()
		s.readyMu.Unlock()
		return err, nil
	})
	err, _ := v.(error)
	return err
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	// a registered pattern with another method -> 405
	var allow []string
	for _, m := range apiMethods {
		if m == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != s.catchAll {
			allow = append(allow, m)
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		api.WriteProblem(w, r, api.Problem{Status: http.StatusMethodNotAllowed, Code: api.ErrorCodeBadRequest,
			Detail: fmt.Sprintf("method %s is not allowed for %s", r.Method, r.URL.Path)})
		return
	}
	api.WriteError(w, r, fmt.Errorf("%w: no such endpoint %s", domain.ErrNotFound, r.URL.Path))
}

// apiHandler builds the chain of one REST route.
func (s *Server) apiHandler(rt api.Route) http.Handler {
	limit := rt.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultBodyLimit
	}
	var inner http.Handler = rt.Handler
	if rt.Auth == api.AuthRequired {
		inner = s.authenticated(rt, inner)
	}
	publicMutation := rt.Auth == api.AuthPublic && isMutating(rt.Method)
	bodyLimited := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API responses carry operational data (configs, audit log): never cache them
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		if publicMutation {
			// unauthenticated state changes (login) are not accepted from other sites
			if !s.sameOrigin(r) {
				api.WriteProblem(w, r, api.Problem{Status: http.StatusForbidden, Code: api.ErrorCodeCSRF, Detail: "cross-site request refused"})
				return
			}
			// browsers send cross-site "simple" requests only with form or text types (or none)
			if !isJSONContentType(r.Header.Get("Content-Type")) {
				api.WriteProblem(w, r, api.Problem{Status: http.StatusUnsupportedMediaType, Code: api.ErrorCodeBadRequest, Detail: "Content-Type must be application/json"})
				return
			}
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		if rt.Streaming && rt.Auth != api.AuthRequired {
			// SSE streams keep their own write deadlines (authenticated streams: see authenticated)
			deadlinesFrom(r.Context()).disableStream()
		}
		if !rt.Streaming && !rt.LongTransfer {
			// bounded handler time: database work is cancelled with the deadline
			ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		inner.ServeHTTP(w, r)
	}))
	return s.base(rt.Pattern, slog.LevelInfo, bodyLimited)
}

// isJSONContentType reports whether a Content-Type header names a JSON media type.
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

// RequestTimeout is the deadline of non-streaming API handlers.
const RequestTimeout = 30 * time.Second

// sameOrigin reports whether a request is not a cross-site request: the
// browser-set Sec-Fetch-Site header, if present, must not be cross-site, and an
// Origin header must name this server (request host or public URL).
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return s.publicURL != nil && strings.EqualFold(u.Host, s.publicURL.Host)
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// authenticated enforces authentication, CSRF and permission.
func (s *Server) authenticated(rt api.Route, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.deps.Auth.Authenticate(r)
		if err != nil {
			if errors.Is(err, domain.ErrUnauthorized) {
				w.Header().Add("WWW-Authenticate", `Bearer realm="`+brand.AuthRealm+`"`)
			}
			api.WriteError(w, r, err)
			return
		}
		if p == nil {
			api.WriteError(w, r, domain.ErrUnauthorized)
			return
		}
		r = r.WithContext(authz.WithPrincipal(r.Context(), p))
		if info := infoFrom(r.Context()); info != nil {
			info.principal = p.String()
		}
		if isMutating(r.Method) {
			if err := s.deps.Auth.CheckCSRF(r, p); err != nil {
				api.WriteProblem(w, r, api.Problem{Status: http.StatusForbidden, Code: api.ErrorCodeCSRF, Detail: err.Error()})
				return
			}
		}
		if !p.Can(rt.Permission) {
			api.WriteProblem(w, r, api.Problem{Status: http.StatusForbidden, Code: api.ErrorCodeForbidden,
				Detail: fmt.Sprintf("permission %q is required (role %q)", rt.Permission, p.Role)})
			return
		}
		if rt.Streaming {
			// only an accepted request gets an unbounded stream: a rejected one keeps its budgets
			deadlinesFrom(r.Context()).disableStream()
		}
		next.ServeHTTP(w, r)
	})
}
