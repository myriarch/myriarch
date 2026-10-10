// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

type captured struct {
	header http.Header
	body   []byte
	path   string
}

type sink struct {
	mu      sync.Mutex
	got     []captured
	status  func(n int) int // response status by call number (1-based)
	calls   atomic.Int32
	srv     *httptest.Server
	delayed time.Duration
}

func newSink(t *testing.T) *sink {
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n := int(s.calls.Add(1))
		s.mu.Lock()
		s.got = append(s.got, captured{header: r.Header.Clone(), body: b, path: r.URL.Path})
		st := s.status
		d := s.delayed
		s.mu.Unlock()
		if d > 0 {
			time.Sleep(d)
		}
		code := 200
		if st != nil {
			code = st(n)
		}
		w.WriteHeader(code)
		if code >= 400 {
			_, _ = w.Write([]byte("go away\n  now"))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) bodies() []captured {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]captured(nil), s.got...)
}

func sample() notify.Notification {
	return notify.Notification{
		Event: notify.EventRolloutPaused, Severity: notify.SeverityWarning,
		Title: "Rollout paused: edge-nl", Summary: "Wave \"pilot\": 3 of 20 targets failed <&>",
		Link:   "/rollouts/0197-abc",
		Fields: []notify.Field{{Name: "Fleet", Value: "edge-nl"}, {Name: "Wave", Value: "pilot"}},
		Time:   time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), DedupKey: "rollout.paused/0197-abc",
	}
}

// testSecret is a valid webhook HMAC secret (at least 32 bytes).
const testSecret = "0123456789abcdef0123456789abcdef-notify"

func newDispatcher(t *testing.T, st *memstore.Store, chans ...config.NotificationChannel) *dispatcher {
	t.Helper()
	n, err := NewDispatcher(config.NotificationsConfig{Channels: chans}, Deps{
		KV: st.KV(), HTTPClient: http.DefaultClient, PublicURL: "https://fleet.example.com/",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	d := n.(*dispatcher)
	d.backoff = func(int) time.Duration { return time.Millisecond }
	return d
}

func TestGenericWebhookPayloadAndSignature(t *testing.T) {
	s := newSink(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(secretFile, []byte(testSecret+"\n"), 0o600))
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "ops", Type: "webhook", URL: s.srv.URL + "/hook", SecretFile: secretFile})
	require.NoError(t, d.Notify(context.Background(), sample()))

	got := s.bodies()
	require.Len(t, got, 1)
	c := got[0]
	assert.Equal(t, "/hook", c.path)
	assert.Equal(t, "application/json", c.header.Get("Content-Type"))
	assert.NotEmpty(t, c.header.Get("X-Myriarch-Delivery"))
	ts := c.header.Get("X-Myriarch-Timestamp")
	assert.NotEmpty(t, ts)
	m := hmac.New(sha256.New, []byte(testSecret))
	m.Write([]byte(ts + "."))
	m.Write(c.body)
	assert.Equal(t, "sha256="+hex.EncodeToString(m.Sum(nil)), c.header.Get("X-Myriarch-Signature"), "same scheme as the webhook executor")

	var n notify.Notification
	require.NoError(t, json.Unmarshal(c.body, &n))
	assert.Equal(t, notify.EventRolloutPaused, n.Event)
	assert.Equal(t, "https://fleet.example.com/rollouts/0197-abc", n.Link, "absolute link")
	assert.Equal(t, "Rollout paused: edge-nl", n.Title)
	assert.Len(t, n.Fields, 2)
	assert.Contains(t, string(c.body), "<&>", "no HTML escaping")
}

func TestGenericWebhookWithoutSecretHasNoSignature(t *testing.T) {
	s := newSink(t)
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "ops", Type: "webhook", URL: s.srv.URL})
	require.NoError(t, d.Notify(context.Background(), sample()))
	got := s.bodies()
	require.Len(t, got, 1)
	assert.Empty(t, got[0].header.Get("X-Myriarch-Signature"))
	assert.NotEmpty(t, got[0].header.Get("X-Myriarch-Timestamp"))
}

func TestTeamsAdaptiveCard(t *testing.T) {
	s := newSink(t)
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "teams", Type: "teams", URL: s.srv.URL})
	require.NoError(t, d.Notify(context.Background(), sample()))
	var msg map[string]any
	require.NoError(t, json.Unmarshal(s.bodies()[0].body, &msg))
	assert.Equal(t, "message", msg["type"])
	atts := msg["attachments"].([]any)
	require.Len(t, atts, 1)
	att := atts[0].(map[string]any)
	assert.Equal(t, "application/vnd.microsoft.card.adaptive", att["contentType"])
	card := att["content"].(map[string]any)
	assert.Equal(t, "http://adaptivecards.io/schemas/adaptive-card.json", card["$schema"])
	assert.Equal(t, "AdaptiveCard", card["type"])
	assert.Equal(t, "1.4", card["version"])
	body := card["body"].([]any)
	require.Len(t, body, 3)
	title := body[0].(map[string]any)
	assert.Equal(t, "TextBlock", title["type"])
	assert.Equal(t, "Rollout paused: edge-nl", title["text"])
	assert.Equal(t, "Warning", title["color"])
	summary := body[1].(map[string]any)
	assert.Equal(t, `Wave "pilot": 3 of 20 targets failed <&>`, summary["text"])
	facts := body[2].(map[string]any)
	assert.Equal(t, "FactSet", facts["type"])
	var fm = map[string]string{}
	for _, f := range facts["facts"].([]any) {
		fm[f.(map[string]any)["title"].(string)] = f.(map[string]any)["value"].(string)
	}
	assert.Equal(t, "edge-nl", fm["Fleet"])
	assert.Equal(t, "pilot", fm["Wave"])
	assert.Equal(t, "warning", fm["Severity"])
	assert.Equal(t, "rollout.paused", fm["Event"])
	acts := card["actions"].([]any)
	require.Len(t, acts, 1)
	a := acts[0].(map[string]any)
	assert.Equal(t, "Action.OpenUrl", a["type"])
	assert.Equal(t, "Open in myriarch", a["title"])
	assert.Equal(t, "https://fleet.example.com/rollouts/0197-abc", a["url"])
}

func TestTeamsCardWithoutLinkHasNoActions(t *testing.T) {
	n := sample()
	n.Link = ""
	card := teamsCard(n)["attachments"].([]map[string]any)[0]["content"].(map[string]any)
	assert.NotContains(t, card, "actions")
	n.Severity = notify.SeverityCritical
	assert.Equal(t, "Attention", teamsCard(n)["attachments"].([]map[string]any)[0]["content"].(map[string]any)["body"].([]map[string]any)[0]["color"])
}

func TestSlackBlocks(t *testing.T) {
	s := newSink(t)
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "slack", Type: "slack", URL: s.srv.URL})
	require.NoError(t, d.Notify(context.Background(), sample()))
	var msg map[string]any
	require.NoError(t, json.Unmarshal(s.bodies()[0].body, &msg))
	assert.Equal(t, "[WARNING] Rollout paused: edge-nl", msg["text"], "fallback text for notifications")
	blocks := msg["blocks"].([]any)
	require.Len(t, blocks, 4)
	header := blocks[0].(map[string]any)
	assert.Equal(t, "header", header["type"])
	assert.Equal(t, "plain_text", header["text"].(map[string]any)["type"])
	section := blocks[1].(map[string]any)
	assert.Equal(t, "section", section["type"])
	assert.Equal(t, "mrkdwn", section["text"].(map[string]any)["type"])
	assert.Equal(t, `Wave "pilot": 3 of 20 targets failed &lt;&amp;&gt;`, section["text"].(map[string]any)["text"], "Slack control characters are escaped")
	fields := blocks[2].(map[string]any)["fields"].([]any)
	require.Len(t, fields, 2)
	assert.Equal(t, "*Fleet*\nedge-nl", fields[0].(map[string]any)["text"])
	actions := blocks[3].(map[string]any)
	assert.Equal(t, "actions", actions["type"])
	btn := actions["elements"].([]any)[0].(map[string]any)
	assert.Equal(t, "button", btn["type"])
	assert.Equal(t, "https://fleet.example.com/rollouts/0197-abc", btn["url"])
}

func TestSlackLimits(t *testing.T) {
	n := sample()
	n.Title = strings.Repeat("x", 300)
	n.Summary = strings.Repeat("y", 5000)
	n.Fields = nil
	for i := 0; i < 15; i++ {
		n.Fields = append(n.Fields, notify.Field{Name: "f", Value: strings.Repeat("v", 3000)})
	}
	msg := slackMessage(n)
	blocks := msg["blocks"].([]map[string]any)
	assert.LessOrEqual(t, len([]rune(blocks[0]["text"].(map[string]any)["text"].(string))), 150)
	assert.LessOrEqual(t, len([]rune(blocks[1]["text"].(map[string]any)["text"].(string))), 3000)
	fields := blocks[2]["fields"].([]map[string]any)
	assert.Len(t, fields, 10)
	assert.LessOrEqual(t, len([]rune(fields[0]["text"].(string))), 2000)
}

func TestRoutingByEventsAndSeverity(t *testing.T) {
	all, rollouts, releases, crit := newSink(t), newSink(t), newSink(t), newSink(t)
	d := newDispatcher(t, memstore.New(),
		config.NotificationChannel{Name: "all", Type: "webhook", URL: all.srv.URL},
		config.NotificationChannel{Name: "rollouts", Type: "slack", URL: rollouts.srv.URL, Events: []string{"rollout.*"}},
		config.NotificationChannel{Name: "releases", Type: "teams", URL: releases.srv.URL, Events: []string{"release.new", "release.breaking"}},
		config.NotificationChannel{Name: "critical", Type: "webhook", URL: crit.srv.URL, MinSeverity: "critical"},
	)
	send := func(ev notify.EventType, sev notify.Severity) {
		require.NoError(t, d.Notify(context.Background(), notify.Notification{Event: ev, Severity: sev, Title: "t", Summary: "s"}))
	}
	send(notify.EventRolloutStarted, notify.SeverityInfo)
	send(notify.EventRolloutFailed, notify.SeverityCritical)
	send(notify.EventReleaseNew, notify.SeverityInfo)
	send(notify.EventGitOpsSyncFailed, notify.SeverityWarning)
	assert.Len(t, all.bodies(), 4)
	assert.Len(t, rollouts.bodies(), 2, "rollout.* wildcard")
	assert.Len(t, releases.bodies(), 1)
	assert.Len(t, crit.bodies(), 1, "minSeverity critical")

	// no matching channel: not an error, nothing sent
	d2 := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "r", Type: "webhook", URL: all.srv.URL, Events: []string{"release.new"}})
	before := len(all.bodies())
	require.NoError(t, d2.Notify(context.Background(), notify.Notification{Event: notify.EventRolloutPaused, Severity: notify.SeverityInfo}))
	assert.Len(t, all.bodies(), before)
}

func TestDeduplicationIsClusterWide(t *testing.T) {
	s := newSink(t)
	st := memstore.New() // the KV store is shared by all replicas
	d1 := newDispatcher(t, st, config.NotificationChannel{Name: "c", Type: "webhook", URL: s.srv.URL})
	d2 := newDispatcher(t, st, config.NotificationChannel{Name: "c", Type: "webhook", URL: s.srv.URL})
	n := sample()
	require.NoError(t, d1.Notify(context.Background(), n))
	require.NoError(t, d2.Notify(context.Background(), n))
	require.NoError(t, d1.Notify(context.Background(), n))
	assert.Len(t, s.bodies(), 1, "same dedupKey is sent once")
	v, err := st.KV().Get(context.Background(), "notify/sent/rollout.paused/0197-abc")
	require.NoError(t, err)
	assert.NotEmpty(t, v)

	// another key / no key
	n.DedupKey = "other"
	require.NoError(t, d1.Notify(context.Background(), n))
	n.DedupKey = ""
	require.NoError(t, d1.Notify(context.Background(), n))
	require.NoError(t, d1.Notify(context.Background(), n))
	assert.Len(t, s.bodies(), 4)

	// concurrent replicas: exactly one wins
	s2 := newSink(t)
	dd1 := newDispatcher(t, st, config.NotificationChannel{Name: "c", Type: "webhook", URL: s2.srv.URL})
	dd2 := newDispatcher(t, st, config.NotificationChannel{Name: "c", Type: "webhook", URL: s2.srv.URL})
	n.DedupKey = "race"
	var wg sync.WaitGroup
	for _, d := range []*dispatcher{dd1, dd2, dd1, dd2} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Notify(context.Background(), n) }()
	}
	wg.Wait()
	assert.Len(t, s2.bodies(), 1)
}

func TestRetriesAndFailureHandling(t *testing.T) {
	// transient 503 twice, then ok: delivered with 3 attempts
	s := newSink(t)
	s.status = func(n int) int {
		if n < 3 {
			return 503
		}
		return 200
	}
	st := memstore.New()
	d := newDispatcher(t, st, config.NotificationChannel{Name: "c", Type: "webhook", URL: s.srv.URL})
	require.NoError(t, d.Notify(context.Background(), sample()))
	assert.Equal(t, int32(3), s.calls.Load())

	// permanent client error: no retry, error returned, dedup key released
	s2 := newSink(t)
	s2.status = func(int) int { return 400 }
	d2 := newDispatcher(t, st, config.NotificationChannel{Name: "bad", Type: "teams", URL: s2.srv.URL})
	n := sample()
	n.DedupKey = "will-fail"
	err := d2.Notify(context.Background(), n)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel bad")
	assert.Contains(t, err.Error(), "HTTP 400: go away now")
	assert.Equal(t, int32(1), s2.calls.Load(), "4xx is not retried")
	_, kerr := st.KV().Get(context.Background(), "notify/sent/will-fail")
	assert.ErrorIs(t, kerr, domain.ErrNotFound, "nothing was delivered: a later attempt may send it")

	// 429 and 5xx exhaust the attempts
	s3 := newSink(t)
	s3.status = func(int) int { return 429 }
	d3 := newDispatcher(t, st, config.NotificationChannel{Name: "limited", Type: "slack", URL: s3.srv.URL})
	assert.Error(t, d3.Notify(context.Background(), notify.Notification{Event: notify.EventTest, Severity: notify.SeverityInfo, Title: "x"}))
	assert.Equal(t, int32(3), s3.calls.Load())
}

func TestPartialFailureIsNotAnErrorAndKeepsTheDedupKey(t *testing.T) {
	good, bad := newSink(t), newSink(t)
	bad.status = func(int) int { return 400 }
	st := memstore.New()
	d := newDispatcher(t, st, config.NotificationChannel{Name: "good", Type: "webhook", URL: good.srv.URL}, config.NotificationChannel{Name: "bad", Type: "webhook", URL: bad.srv.URL})
	require.NoError(t, d.Notify(context.Background(), sample()), "an error only if ALL channels failed")
	_, err := st.KV().Get(context.Background(), "notify/sent/rollout.paused/0197-abc")
	assert.NoError(t, err)
	assert.Len(t, good.bodies(), 1)
}

func TestDeliveryIsParallelAndTimeBounded(t *testing.T) {
	slow1, slow2 := newSink(t), newSink(t)
	slow1.delayed, slow2.delayed = 200*time.Millisecond, 200*time.Millisecond
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "a", Type: "webhook", URL: slow1.srv.URL}, config.NotificationChannel{Name: "b", Type: "webhook", URL: slow2.srv.URL})
	start := time.Now()
	require.NoError(t, d.Notify(context.Background(), sample()))
	assert.Less(t, time.Since(start), 380*time.Millisecond, "channels are served in parallel")

	// the caller's context stops deliveries
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	n := sample()
	n.DedupKey = ""
	start = time.Now()
	err := d.Notify(ctx, n)
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 190*time.Millisecond)
}

func TestErrorsDoNotLeakWebhookURLs(t *testing.T) {
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "c", Type: "teams", URL: "http://127.0.0.1:1/workflows/SECRET-TOKEN/trigger?sig=SUPERSECRET"})
	err := d.Notify(context.Background(), notify.Notification{Event: notify.EventTest, Severity: notify.SeverityInfo, Title: "x"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET")
	err = d.Test(context.Background(), "c")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET")
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	target := newSink(t)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.srv.URL, http.StatusFound)
	}))
	defer redir.Close()
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "c", Type: "webhook", URL: redir.URL})
	err := d.Notify(context.Background(), notify.Notification{Event: notify.EventTest, Severity: notify.SeverityInfo, Title: "x"})
	assert.Error(t, err)
	assert.Empty(t, target.bodies())
}

func TestTestNotification(t *testing.T) {
	s := newSink(t)
	st := memstore.New()
	d := newDispatcher(t, st,
		config.NotificationChannel{Name: "teams", Type: "teams", URL: s.srv.URL, Events: []string{"release.new"}, MinSeverity: "critical"})
	require.NoError(t, d.Test(context.Background(), "teams"), "test messages ignore routing")
	require.NoError(t, d.Test(context.Background(), "teams"), "and de-duplication")
	assert.Len(t, s.bodies(), 2)
	assert.Contains(t, string(s.bodies()[0].body), "Myriarch test notification")
	assert.Contains(t, string(s.bodies()[0].body), "https://fleet.example.com/")

	err := d.Test(context.Background(), "nope")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	s.status = func(int) int { return 500 }
	assert.Error(t, d.Test(context.Background(), "teams"))
}

func TestChannelsListsRedactedTargets(t *testing.T) {
	d := newDispatcher(t, memstore.New(),
		config.NotificationChannel{Name: "b", Type: "slack", URL: "https://hooks.slack.com/services/T000/B000/XXXX"},
		config.NotificationChannel{Name: "a", Type: "teams", URL: "https://prod-1.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?sig=SECRET", Events: []string{"rollout.*"}, MinSeverity: "warning"},
	)
	list := d.Channels()
	require.Len(t, list, 2)
	assert.Equal(t, "a", list[0].Name)
	assert.Equal(t, "teams", list[0].Type)
	assert.Equal(t, "https://prod-1.westeurope.logic.azure.com:443/…", list[0].Target)
	assert.Equal(t, []string{"rollout.*"}, list[0].Events)
	assert.Equal(t, "warning", list[0].MinSeverity)
	assert.Equal(t, "https://hooks.slack.com/…", list[1].Target)
	assert.Equal(t, []string{"*"}, list[1].Events, "no events = all")
	assert.Equal(t, "info", list[1].MinSeverity)
	raw, _ := json.Marshal(list)
	assert.NotContains(t, string(raw), "SECRET")
	assert.NotContains(t, string(raw), "XXXX")
}

func TestConfigurationErrors(t *testing.T) {
	tmp := t.TempDir()
	empty := filepath.Join(tmp, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	urlFile := filepath.Join(tmp, "url")
	require.NoError(t, os.WriteFile(urlFile, []byte("https://hooks.example.com/x\n"), 0o600))
	good := config.NotificationChannel{Name: "c", Type: "webhook", URL: "https://x.example.com/h"}
	cases := map[string]config.NotificationChannel{
		"no name":            {Type: "webhook", URL: "https://x.example.com"},
		"bad type":           {Name: "c", Type: "email", URL: "https://x.example.com"},
		"no url":             {Name: "c", Type: "teams"},
		"ftp":                {Name: "c", Type: "teams", URL: "ftp://x.example.com"},
		"missing urlFile":    {Name: "c", Type: "teams", URLFile: filepath.Join(tmp, "missing")},
		"missing secretFile": {Name: "c", Type: "webhook", URL: "https://x.example.com", SecretFile: filepath.Join(tmp, "missing")},
		"empty secretFile":   {Name: "c", Type: "webhook", URL: "https://x.example.com", SecretFile: empty},
		"secret on slack":    {Name: "c", Type: "slack", URL: "https://x.example.com", SecretFile: urlFile},
		"unknown event":      {Name: "c", Type: "webhook", URL: "https://x.example.com", Events: []string{"rollout.explodes"}},
		"bad severity":       {Name: "c", Type: "webhook", URL: "https://x.example.com", MinSeverity: "fatal"},
	}
	for name, cc := range cases {
		_, err := NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{cc}}, Deps{})
		assert.Error(t, err, name)
	}
	_, err := NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{good, good}}, Deps{})
	assert.Error(t, err, "duplicate names")
	// urlFile works and wins over url
	n, err := NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{{Name: "f", Type: "teams", URL: "https://ignored.example.com", URLFile: urlFile, Events: []string{"*", "test", "rollout.*"}}}}, Deps{})
	require.NoError(t, err)
	assert.Equal(t, "https://hooks.example.com/…", n.Channels()[0].Target)
	// empty config: a working no-op notifier
	n, err = NewDispatcher(config.NotificationsConfig{}, Deps{})
	require.NoError(t, err)
	assert.NoError(t, n.Notify(context.Background(), sample()))
	assert.Empty(t, n.Channels())
	assert.ErrorIs(t, n.Test(context.Background(), "x"), domain.ErrNotFound)
}

func TestDefaultsForMissingFields(t *testing.T) {
	s := newSink(t)
	d := newDispatcher(t, memstore.New(), config.NotificationChannel{Name: "c", Type: "webhook", URL: s.srv.URL})
	require.NoError(t, d.Notify(context.Background(), notify.Notification{Event: notify.EventTest, Title: "x"}))
	var n notify.Notification
	require.NoError(t, json.Unmarshal(s.bodies()[0].body, &n))
	assert.Equal(t, notify.SeverityInfo, n.Severity)
	assert.False(t, n.Time.IsZero())
	assert.Empty(t, n.Link)
}

func TestAsyncDeliveryDoesNotBlockAndDropsOnOverflow(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // a receiver that never answers promptly
	}))
	defer srv.Close()
	defer close(release)

	reg := prometheus.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{{Name: "slow", Type: "slack", URL: srv.URL}}}, Deps{
		KV: memstore.New().KV(), HTTPClient: srv.Client(), Registerer: reg, QueueSize: 2, Workers: 1, Context: ctx,
	})
	require.NoError(t, err)

	start := time.Now()
	for i := 0; i < 20; i++ {
		require.NoError(t, d.Notify(context.Background(), notify.Notification{Event: notify.EventTest, Severity: notify.SeverityInfo, Title: "t", Summary: "s"}))
	}
	assert.Less(t, time.Since(start), 2*time.Second, "Notify only enqueues")

	mfs, err := reg.Gather()
	require.NoError(t, err)
	dropped := 0.0
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_notifications_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == "dropped" {
					dropped += m.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Greater(t, dropped, 10.0, "overflow is dropped and counted")
}

func TestWebhookSecretMustBeLongEnough(t *testing.T) {
	f := filepath.Join(t.TempDir(), "s")
	require.NoError(t, os.WriteFile(f, []byte("short"), 0o600))
	_, err := NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{{Name: "c", Type: "webhook", URL: "https://x.example.com", SecretFile: f}}}, Deps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 32 bytes")
}

// OUT-6: in production a channel URL is https, wherever it is configured (inline or urlFile).
func TestProductionRequiresHTTPSForChannelURLs(t *testing.T) {
	tmp := t.TempDir()
	httpFile := filepath.Join(tmp, "http-url")
	require.NoError(t, os.WriteFile(httpFile, []byte("http://hooks.example.com/x\n"), 0o600))
	httpsFile := filepath.Join(tmp, "https-url")
	require.NoError(t, os.WriteFile(httpsFile, []byte("https://hooks.example.com/x\n"), 0o600))
	build := func(cc config.NotificationChannel, requireHTTPS bool) error {
		_, err := NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{cc}}, Deps{RequireHTTPS: requireHTTPS})
		return err
	}
	plainFile := config.NotificationChannel{Name: "c", Type: "teams", URLFile: httpFile}
	err := build(plainFile, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https")
	assert.Error(t, build(config.NotificationChannel{Name: "c", Type: "slack", URL: "http://hooks.example.com/x"}, true))
	assert.NoError(t, build(config.NotificationChannel{Name: "c", Type: "teams", URLFile: httpsFile}, true))
	// dev mode keeps plain http for local mock receivers
	assert.NoError(t, build(plainFile, false))
}
