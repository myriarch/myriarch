// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package channels implements notify.Notifier: a Dispatcher routing
// notifications to MS Teams (Workflows/incoming webhook, Adaptive Card),
// Slack (incoming webhook, Block Kit) and generic webhooks (JSON +
// HMAC-SHA256 signature header X-Myriarch-Signature).
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor/webhookexec"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/version"
)

const (
	sendTimeout = 10 * time.Second
	maxAttempts = 3
	// deliveryTimeout is the overall deadline of one notification (all
	// attempts, all channels).
	deliveryTimeout = 30 * time.Second
	// dedupPrefix is the KV namespace of sent notifications.
	dedupPrefix = "notify/sent/"
	// minSecretLen is the shortest accepted HMAC secret of a webhook channel.
	minSecretLen = 32
	// DefaultQueueSize and DefaultWorkers configure asynchronous delivery.
	DefaultQueueSize = 256
	DefaultWorkers   = 4
)

// Deps are the dependencies of the dispatcher.
type Deps struct {
	KV         store.KVStore
	HTTPClient *http.Client
	// PublicURL (config server.publicUrl) turns Notification.Link paths into
	// absolute URLs.
	PublicURL  string
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// QueueSize > 0 switches to asynchronous delivery: Notify only enqueues
	// (after routing and de-duplication) and Workers goroutines deliver, so
	// callers (rollout tick, GitOps sync, release poll) never wait for a slow
	// receiver. A full queue drops the notification (counted as result
	// "dropped"). 0 delivers synchronously inside Notify (tests).
	QueueSize int
	Workers   int
	// RequireHTTPS refuses channel URLs (also those read from urlFile) that are
	// not https: webhook URLs of Teams/Slack are credentials. Set outside dev
	// mode.
	RequireHTTPS bool
	// Context stops the workers (default: they run until the process ends).
	Context context.Context
}

type channel struct {
	name   string
	typ    string
	url    string
	secret []byte
	events []string
	minSev notify.Severity
}

type dispatcher struct {
	channels []*channel
	deps     Deps
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time
	backoff  func(attempt int) time.Duration
	sent     *prometheus.CounterVec
	queue    chan job
}

// job is one queued notification with its routing result.
type job struct {
	n       notify.Notification
	targets []*channel
}

// NewDispatcher creates the dispatcher. Channels with unreadable secrets
// are reported as error at startup.
func NewDispatcher(cfg config.NotificationsConfig, deps Deps) (notify.Notifier, error) {
	d := &dispatcher{
		deps: deps, now: func() time.Time { return time.Now().UTC() },
		backoff: func(attempt int) time.Duration { return time.Duration(attempt) * 500 * time.Millisecond },
	}
	d.log = deps.Logger
	if d.log == nil {
		d.log = slog.Default()
	}
	d.log = d.log.With("component", "notify")
	base := deps.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	c.Timeout = 0 // per-attempt context deadline
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d.client = &c

	seen := map[string]bool{}
	for i, cc := range cfg.Channels {
		ch, err := buildChannel(cc, deps.RequireHTTPS)
		if err != nil {
			return nil, fmt.Errorf("notifications.channels[%d] (%q): %w", i, cc.Name, err)
		}
		if seen[ch.name] {
			return nil, fmt.Errorf("notifications.channels[%d]: duplicate channel name %q", i, ch.name)
		}
		seen[ch.name] = true
		d.channels = append(d.channels, ch)
	}

	d.sent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_notifications_total", Help: "Notification deliveries by channel and result.",
	}, []string{"channel", "result"})
	if deps.Registerer != nil {
		if err := deps.Registerer.Register(d.sent); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				if cv, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
					d.sent = cv
				}
			}
		}
	}
	if deps.QueueSize > 0 {
		d.queue = make(chan job, deps.QueueSize)
		workers := deps.Workers
		if workers <= 0 {
			workers = DefaultWorkers
		}
		ctx := deps.Context
		if ctx == nil {
			ctx = context.Background()
		}
		for i := 0; i < workers; i++ {
			go d.worker(ctx)
		}
	}
	return d, nil
}

func (d *dispatcher) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.queue:
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryTimeout)
			if err := d.deliver(dctx, j.n, j.targets); err != nil {
				d.log.Warn("notification could not be delivered", "event", j.n.Event, "error", err)
			}
			cancel()
		}
	}
}

var knownEvents = map[string]bool{}

func init() {
	for _, e := range []notify.EventType{
		notify.EventReleaseNew, notify.EventReleaseBreaking, notify.EventRolloutStarted, notify.EventRolloutWaitingApprove,
		notify.EventRolloutPaused, notify.EventRolloutFailed, notify.EventRolloutCompleted, notify.EventRolloutRolledBack,
		notify.EventGitOpsSyncFailed, notify.EventTest,
	} {
		knownEvents[string(e)] = true
	}
}

// CheckChannel reports why NewDispatcher would reject the channel (type, url,
// secret, events, severity). With skipFiles the files url/secret are not read
// (a url from urlFile counts as valid, the secret is not checked): for
// "myriarchd config validate --skip-files".
func CheckChannel(cc config.NotificationChannel, requireHTTPS, skipFiles bool) error {
	_, err := parseChannel(cc, requireHTTPS, skipFiles)
	return err
}

func buildChannel(cc config.NotificationChannel, requireHTTPS bool) (*channel, error) {
	return parseChannel(cc, requireHTTPS, false)
}

func parseChannel(cc config.NotificationChannel, requireHTTPS, skipFiles bool) (*channel, error) {
	ch := &channel{name: strings.TrimSpace(cc.Name), typ: strings.ToLower(strings.TrimSpace(cc.Type))}
	if ch.name == "" {
		return nil, errors.New("name is required")
	}
	switch ch.typ {
	case "webhook", "teams", "slack":
	default:
		return nil, fmt.Errorf("type %q is not supported (webhook, teams, slack)", cc.Type)
	}
	raw := cc.URL
	if cc.URLFile != "" && !skipFiles {
		b, err := os.ReadFile(cc.URLFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read urlFile: %w", stripPathError(err))
		}
		raw = strings.TrimSpace(string(b))
	}
	if !(skipFiles && cc.URLFile != "" && raw == "") { // the url may come from a file that is not read
		u, err := url.Parse(raw)
		if raw == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("a valid http(s) url (or urlFile) is required")
		}
		if requireHTTPS && u.Scheme != "https" {
			return nil, errors.New("the url (or urlFile) must be an https URL in production")
		}
	}
	ch.url = raw
	if cc.SecretFile != "" {
		if ch.typ != "webhook" {
			return nil, errors.New("secretFile is only supported for type webhook")
		}
		if !skipFiles {
			b, err := os.ReadFile(cc.SecretFile)
			if err != nil {
				return nil, fmt.Errorf("cannot read secretFile: %w", stripPathError(err))
			}
			ch.secret = bytes.TrimSpace(b)
			if len(ch.secret) == 0 {
				return nil, errors.New("secretFile is empty")
			}
			if len(ch.secret) < minSecretLen {
				return nil, fmt.Errorf("secretFile must contain a secret of at least %d bytes (HMAC-SHA256 key)", minSecretLen)
			}
		}
	}
	for _, e := range cc.Events {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		base := strings.TrimSuffix(e, "*")
		if e != "*" && base == e && !knownEvents[e] {
			return nil, fmt.Errorf("unknown event %q", e)
		}
		ch.events = append(ch.events, e)
	}
	switch s := notify.Severity(strings.ToLower(strings.TrimSpace(cc.MinSeverity))); s {
	case "":
		ch.minSev = notify.SeverityInfo
	case notify.SeverityInfo, notify.SeverityWarning, notify.SeverityCritical:
		ch.minSev = s
	default:
		return nil, fmt.Errorf("minSeverity %q is invalid (info, warning, critical)", cc.MinSeverity)
	}
	return ch, nil
}

func stripPathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func severityRank(s notify.Severity) int {
	switch s {
	case notify.SeverityCritical:
		return 3
	case notify.SeverityWarning:
		return 2
	}
	return 1
}

// matches reports whether the channel routes the event at this severity.
func (c *channel) matches(n notify.Notification) bool {
	if severityRank(n.Severity) < severityRank(c.minSev) {
		return false
	}
	if len(c.events) == 0 {
		return true
	}
	for _, p := range c.events {
		if p == "*" || p == string(n.Event) {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(string(n.Event), strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- Notifier ----

// Notify routes the notification to all matching channels. Duplicates (same
// DedupKey, cluster-wide through the KV store) are dropped. It returns an
// error only if all matching channels failed.
func (d *dispatcher) Notify(ctx context.Context, n notify.Notification) error {
	if n.Time.IsZero() {
		n.Time = d.now()
	}
	if n.Severity == "" {
		n.Severity = notify.SeverityInfo
	}
	var targets []*channel
	for _, c := range d.channels {
		if c.matches(n) {
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	if n.DedupKey != "" && d.deps.KV != nil {
		first, err := d.deps.KV.PutIfAbsent(ctx, dedupPrefix+n.DedupKey, []byte(n.Time.Format(time.RFC3339)))
		if err != nil {
			// Better a possible duplicate than a lost notification.
			d.log.Warn("notification de-duplication failed, sending anyway", "dedup_key", n.DedupKey, "error", err)
		} else if !first {
			return nil
		}
	}
	n.Link = d.absoluteLink(n.Link)

	if d.queue != nil {
		select {
		case d.queue <- job{n: n, targets: targets}:
			return nil
		default:
			for _, c := range targets {
				d.sent.WithLabelValues(c.name, "dropped").Inc()
			}
			if n.DedupKey != "" && d.deps.KV != nil {
				_ = d.deps.KV.Delete(context.WithoutCancel(ctx), dedupPrefix+n.DedupKey) // a later attempt may succeed
			}
			d.log.Warn("notification queue is full, dropping the notification", "event", n.Event)
			return nil
		}
	}
	dctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	return d.deliver(dctx, n, targets)
}

// deliver sends n to all targets (in parallel) and reports an error only if
// every one of them failed.
func (d *dispatcher) deliver(ctx context.Context, n notify.Notification, targets []*channel) error {
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, c := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.send(ctx, c, n)
		}()
	}
	wg.Wait()
	var failed []error
	ok := 0
	for i, err := range errs {
		if err != nil {
			failed = append(failed, fmt.Errorf("channel %s: %w", targets[i].name, err))
		} else {
			ok++
		}
	}
	if ok == 0 {
		if n.DedupKey != "" && d.deps.KV != nil {
			// Nothing was delivered: allow a later attempt to send it again.
			_ = d.deps.KV.Delete(context.WithoutCancel(ctx), dedupPrefix+n.DedupKey)
		}
		return errors.Join(failed...)
	}
	return nil
}

// Test sends a test notification to one channel (ignores routing and
// de-duplication).
func (d *dispatcher) Test(ctx context.Context, name string) error {
	for _, c := range d.channels {
		if c.name == name {
			n := notify.Notification{
				Event: notify.EventTest, Severity: notify.SeverityInfo, Time: d.now(),
				Title:   "Myriarch test notification",
				Summary: "This is a test message from myriarchd. If you can read it, the channel works.",
				Link:    d.absoluteLink("/"),
				Fields:  []notify.Field{{Name: "Channel", Value: c.name}, {Name: "Type", Value: c.typ}},
			}
			if err := d.send(ctx, c, n); err != nil {
				return fmt.Errorf("channel %s: %w", name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: notification channel %q", domain.ErrNotFound, name)
}

// Channels lists configured channels with redacted targets.
func (d *dispatcher) Channels() []notify.ChannelInfo {
	out := make([]notify.ChannelInfo, 0, len(d.channels))
	for _, c := range d.channels {
		events := append([]string{}, c.events...)
		if len(events) == 0 {
			events = []string{"*"}
		}
		out = append(out, notify.ChannelInfo{Name: c.name, Type: c.typ, Events: events, MinSeverity: string(c.minSev), Target: redactTarget(c.url)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func redactTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

func (d *dispatcher) absoluteLink(link string) string {
	if link == "" || strings.Contains(link, "://") {
		return link
	}
	return strings.TrimRight(d.deps.PublicURL, "/") + "/" + strings.TrimLeft(link, "/")
}

// ---------------------------------------------------------------- delivery ----

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
	}
	return fmt.Sprintf("HTTP %d", e.status)
}

func retryable(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.status >= 500 || he.status == http.StatusTooManyRequests || he.status == http.StatusRequestTimeout
	}
	return !errors.Is(err, context.Canceled)
}

// send delivers with timeout and retries (3 attempts, backoff).
func (d *dispatcher) send(ctx context.Context, c *channel, n notify.Notification) error {
	body, err := render(c, n)
	if err != nil {
		d.sent.WithLabelValues(c.name, "error").Inc()
		return fmt.Errorf("render notification: %w", err)
	}
	delivery := uuid.New()
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = d.post(ctx, c, body, delivery)
		if last == nil {
			d.sent.WithLabelValues(c.name, "ok").Inc()
			return nil
		}
		if !retryable(last) || attempt == maxAttempts || ctx.Err() != nil {
			break
		}
		d.log.Warn("notification delivery failed, retrying", "channel", c.name, "attempt", attempt, "error", last)
		select {
		case <-ctx.Done():
			attempt = maxAttempts
		case <-time.After(d.backoff(attempt)):
		}
	}
	d.sent.WithLabelValues(c.name, "error").Inc()
	d.log.Error("notification delivery failed", "channel", c.name, "event", n.Event, "error", last)
	return last
}

func (d *dispatcher) post(ctx context.Context, c *channel, body []byte, delivery uuid.UUID) error {
	rctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid request: %w", stripURL(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent(brand.ServerBinary))
	if c.typ == "webhook" {
		ts := d.now().Unix()
		req.Header.Set(brand.DeliveryHeader, delivery.String())
		req.Header.Set(brand.TimestampHeader, fmt.Sprint(ts))
		if len(c.secret) > 0 {
			req.Header.Set(brand.SignatureHeader, webhookexec.Signature(c.secret, ts, body))
		}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return stripURL(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpError{status: resp.StatusCode, body: shorten(string(b))}
	}
	return nil
}

// stripURL removes the (secret) webhook URL from transport errors.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.DeadlineExceeded) {
			return errors.New("timeout")
		}
		return ue.Err
	}
	return err
}

func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
