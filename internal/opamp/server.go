// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package opamp implements the OpAMP server of myriarchd on top of
// github.com/open-telemetry/opamp-go/server (Attach mode).
//
// Responsibilities (see docs/design/opamp-server.md, OpAMP server):
//   - connection lifecycle (OnConnecting auth + rate limiting, OnConnected,
//     OnConnectionClose), local connection registry, replica bookkeeping
//   - processing every AgentToServer field and persisting state
//   - reconciling desired state on every message and on bus events
//     (remote_config, packages_available, ReportFullState/ReportAvailable-
//     Components flags, restart command)
//   - offline detection (leader sweeper) and batched last_seen updates
//
// Design notes (verified against opamp-go v0.25.0, see
// docs/design/upstream-notes.md, OpAMP library behaviour):
//   - The ConnContext returned by Attach MUST be installed on the
//     http.Server (HTTP transport panics without it): Server.ConnContext.
//   - OnMessage runs with context.Background() on WebSocket connections, so
//     every database call gets its own timeout.
//   - Connection.Send ignores its context and gorilla/websocket resets a
//     deadline set on the underlying net.Conn before every frame write; a push
//     therefore runs in a goroutine and a send that exceeds cfg.SendTimeout
//     closes the connection (Disconnect unblocks the write).
//   - Heartbeats (empty messages) are answered from per-session cached state
//     without any database access.
package opamp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	opampserver "github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/leader"
	lru "github.com/myriarch/myriarch/internal/lru"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/store"
)

// AgentAuthenticator authenticates OpAMP handshakes (implemented by
// *authn.Service).
type AgentAuthenticator interface {
	AuthenticateAgent(r *http.Request) (*authn.AgentCredential, error)
}

// DownloadAuthorizer creates the credentials of package downloads
// (implemented by *artifact.Cache).
type DownloadAuthorizer interface {
	// DownloadHeaders returns the HTTP headers (Authorization with a signed
	// download token bound to artifact and agent) for a download URL.
	DownloadHeaders(downloadURL string, agent domain.AgentID) (map[string]string, error)
	// DownloadTokenTTL is the lifetime of these tokens (0 = unlimited).
	DownloadTokenTTL() time.Duration
}

// Deps are the dependencies of the server.
type Deps struct {
	Store    store.Store
	Bus      eventbus.Bus
	Assigner control.Assigner
	Auth     AgentAuthenticator
	// Downloads (optional) mints download tokens when offers are sent.
	Downloads DownloadAuthorizer
	// Audit (optional) records forced disconnects and identity conflicts.
	Audit      audit.Recorder
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	ReplicaID  string
	// Elector reports leadership for the replica heartbeat (IsLeader).
	Elector leader.Elector
	// HA provides the replica heartbeat interval.
	HA config.HAConfig
	// Hostname and Version are reported in the replica heartbeat.
	Hostname string
	Version  string
	// Now overrides the clock (tests).
	Now func() time.Time
}

const (
	messageTimeout     = 20 * time.Second
	assignTimeout      = 15 * time.Second
	closeTimeout       = 10 * time.Second
	viewRefreshAfter   = 5 * time.Minute
	reofferGuard       = 60 * time.Second
	eventQueueSize     = 8192
	publishQueueSize   = 8192
	pushQueueSize      = 16384
	pushWorkers        = 8
	publishWorkers     = 4
	httpSessionIdleTTL = 10 * time.Minute
	shutdownCloseRate  = 100 // connections per second
)

// Server is the OpAMP server.
type Server struct {
	cfg  config.OpAMPConfig
	deps Deps
	log  *slog.Logger
	now  func() time.Time

	handler opampserver.HTTPHandlerFunc
	connCtx opampserver.ConnContext
	limiter *rate.Limiter
	guard   *guard
	// connection timeouts (captured at construction, see guard.go)
	firstMsgTimeout time.Duration
	tokenRecheck    time.Duration

	shuttingDown atomic.Bool
	shutdownOnce sync.Once
	shutdownErr  error
	wsCount      atomic.Int64
	startedAt    time.Time

	mu       sync.RWMutex
	sessions map[domain.AgentID]*session

	httpMu       sync.Mutex
	httpSessions map[domain.AgentID]*httpEntry

	lastSeenMu sync.Mutex
	lastSeen   map[domain.AgentID]time.Time

	events  chan domain.AgentEvent
	pubq    chan pubItem
	pushq   chan *session
	bodies  *lru.Cache[string, []byte]
	knownCS *lru.Cache[string, struct{}]

	m serverMetrics
}

type httpEntry struct {
	sess     *session
	lastUsed time.Time
}

type pubItem struct {
	typ     eventbus.Type
	payload any
}

type serverMetrics struct {
	connections *prometheus.GaugeVec
	messages    *prometheus.CounterVec
	rejected    *prometheus.CounterVec
	pushErrors  prometheus.Counter
	duration    prometheus.Histogram
	agents      *prometheus.GaugeVec
	configSync  *prometheus.GaugeVec
	dropped     *prometheus.CounterVec
}

// New creates the server (does not start goroutines).
func New(cfg config.OpAMPConfig, deps Deps) (*Server, error) {
	if deps.Store == nil || deps.Bus == nil || deps.Auth == nil {
		return nil, errors.New("opamp: Store, Bus and Auth are required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 10 * time.Second
	}
	if cfg.LastSeenFlushInterval <= 0 {
		cfg.LastSeenFlushInterval = 15 * time.Second
	}
	if cfg.OfflineAfter <= 0 {
		cfg.OfflineAfter = 90 * time.Second
	}
	if cfg.MaxConnectionsPerReplica <= 0 {
		cfg.MaxConnectionsPerReplica = 10000
	}
	if cfg.ConnectRateLimit <= 0 {
		cfg.ConnectRateLimit = 200
	}
	if cfg.ConnectRatePerIP <= 0 {
		cfg.ConnectRatePerIP = 50
	}
	if cfg.MaxEffectiveConfigBytes <= 0 {
		cfg.MaxEffectiveConfigBytes = 1 << 20
	}
	if deps.HA.HeartbeatInterval <= 0 {
		deps.HA.HeartbeatInterval = 10 * time.Second
	}
	s := &Server{
		cfg: cfg, deps: deps, log: logger.With("component", "opamp"), now: deps.Now, startedAt: time.Now().UTC(),
		sessions: map[domain.AgentID]*session{}, httpSessions: map[domain.AgentID]*httpEntry{}, lastSeen: map[domain.AgentID]time.Time{},
		events: make(chan domain.AgentEvent, eventQueueSize), pubq: make(chan pubItem, publishQueueSize), pushq: make(chan *session, pushQueueSize),
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	burst := int(2 * cfg.ConnectRateLimit)
	if burst < 1 {
		burst = 1
	}
	s.limiter = rate.NewLimiter(rate.Limit(cfg.ConnectRateLimit), burst)
	s.guard = newGuard(cfg.ConnectRatePerIP)
	s.firstMsgTimeout, s.tokenRecheck = firstMessageTimeout, tokenRecheckInterval
	s.bodies, _ = lru.New[string, []byte](128)
	s.knownCS, _ = lru.New[string, struct{}](4096)
	s.registerMetrics(deps.Registerer)

	h, cc, err := opampserver.New(&slogAdapter{log: s.log}).Attach(opampserver.Settings{
		Callbacks:         types.Callbacks{OnConnecting: s.onConnecting},
		EnableCompression: cfg.EnableCompression,
		MaxMessageSize:    cfg.MaxMessageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("opamp: attach: %w", err)
	}
	s.handler, s.connCtx = h, cc
	return s, nil
}

func (s *Server) registerMetrics(reg prometheus.Registerer) {
	s.m.connections = metrics.Register(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "myriarch_opamp_connections", Help: "Live OpAMP connections on this replica by transport.",
	}, []string{"transport"}))
	s.m.messages = metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_opamp_messages_total", Help: "OpAMP messages by direction (in: from agents, out: replies and pushes).",
	}, []string{"direction"}))
	s.m.rejected = metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_opamp_connect_rejected_total", Help: "Rejected OpAMP connection attempts by reason.",
	}, []string{"reason"}))
	s.m.pushErrors = metrics.Register(reg, prometheus.NewCounter(prometheus.CounterOpts{
		Name: "myriarch_opamp_push_errors_total", Help: "Failed or timed out pushes to agents.",
	}))
	s.m.duration = metrics.Register(reg, prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "myriarch_opamp_message_duration_seconds", Help: "Processing time of one AgentToServer message.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}))
	s.m.agents = metrics.Register(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "myriarch_agents", Help: "Agents by status and kind (refreshed by the leader).",
	}, []string{"status", "kind"}))
	s.m.configSync = metrics.Register(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "myriarch_config_sync", Help: "Agents by config sync state (refreshed by the leader).",
	}, []string{"state"}))
	s.m.dropped = metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_opamp_dropped_total", Help: "Internal queue overflows (events, bus publications, pushes).",
	}, []string{"queue"}))
}

// Handler returns the http.Handler to mount at cfg.Path (WebSocket and plain
// HTTP transport), created with opamp-go server.Attach.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.handler) }

// ConnContext must be installed as http.Server.ConnContext (required by
// opamp-go for the plain HTTP transport).
func (s *Server) ConnContext(ctx context.Context, c net.Conn) context.Context {
	return s.connCtx(ctx, c)
}

// Connections returns the number of live WebSocket connections on this replica.
func (s *Server) Connections() int { return int(s.wsCount.Load()) }

// slogAdapter adapts slog to the opamp-go logger interface.
type slogAdapter struct{ log *slog.Logger }

func (a *slogAdapter) Debugf(ctx context.Context, format string, v ...any) {
	if a.log.Enabled(ctx, slog.LevelDebug) {
		a.log.Debug(fmt.Sprintf(format, v...), "source", "opamp-go")
	}
}

func (a *slogAdapter) Errorf(ctx context.Context, format string, v ...any) {
	a.log.Warn(fmt.Sprintf(format, v...), "source", "opamp-go")
}

// ----------------------------------------------------------- connecting ---

func reject(code int, retryAfter int) types.ConnectionResponse {
	r := types.ConnectionResponse{Accept: false, HTTPStatusCode: code, HTTPResponseHeader: map[string]string{}}
	if retryAfter > 0 {
		r.HTTPResponseHeader["Retry-After"] = fmt.Sprint(retryAfter)
	}
	return r
}

// onConnecting runs for every WebSocket handshake and every plain HTTP poll.
func (s *Server) onConnecting(r *http.Request) types.ConnectionResponse {
	if s.shuttingDown.Load() {
		s.m.rejected.WithLabelValues("shutdown").Inc()
		return reject(http.StatusServiceUnavailable, 10)
	}
	// Cheap checks first, before any database access: the per-client-IP
	// bucket protects the authentication from a single noisy client.
	if !s.guard.allowIP(r.RemoteAddr) {
		s.m.rejected.WithLabelValues("rate_limited_ip").Inc()
		return reject(http.StatusTooManyRequests, 5)
	}
	transport := "http"
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		transport = "websocket"
	}
	if transport == "websocket" && int(s.wsCount.Load()) >= s.cfg.MaxConnectionsPerReplica {
		s.m.rejected.WithLabelValues("max_connections").Inc()
		return reject(http.StatusServiceUnavailable, 30)
	}
	cred, err := s.deps.Auth.AuthenticateAgent(r)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			s.m.rejected.WithLabelValues("unauthorized").Inc()
			resp := reject(http.StatusUnauthorized, 0)
			resp.HTTPResponseHeader["WWW-Authenticate"] = "Bearer"
			return resp
		}
		if errors.Is(err, domain.ErrTooManyRequests) {
			// the replica-wide bound for lookups of unknown enrollment tokens
			s.m.rejected.WithLabelValues("auth_rate_limited").Inc()
			return reject(http.StatusTooManyRequests, 5)
		}
		s.log.Warn("agent authentication unavailable", "error", err)
		s.m.rejected.WithLabelValues("auth_unavailable").Inc()
		return reject(http.StatusServiceUnavailable, 10)
	}
	// The replica-wide bucket is charged only for authenticated connections:
	// requests with bad credentials cannot exhaust it for the real agents.
	if !s.limiter.Allow() {
		s.m.rejected.WithLabelValues("rate_limited").Inc()
		return reject(http.StatusTooManyRequests, 5)
	}
	if transport == "websocket" && s.cfg.MaxConnectionsPerToken > 0 && cred.EnrollmentTokenID != nil &&
		s.guard.tokenCount(*cred.EnrollmentTokenID) >= s.cfg.MaxConnectionsPerToken {
		s.m.rejected.WithLabelValues("token_connections").Inc()
		return reject(http.StatusTooManyRequests, 30)
	}
	ci := &connInfo{
		srv: s, transport: transport, remoteAddr: r.RemoteAddr, cred: cred, connectedAt: s.now(),
	}
	return types.ConnectionResponse{Accept: true, ConnectionCallbacks: types.ConnectionCallbacks{
		OnConnected:            ci.onConnected,
		OnMessage:              ci.onMessage,
		OnConnectionClose:      ci.onClose,
		OnReadMessageError:     ci.onReadError,
		OnMessageResponseError: ci.onResponseError,
	}}
}
