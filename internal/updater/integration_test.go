// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	stypes "github.com/open-telemetry/opamp-go/server/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opampTestServer is a minimal in-process OpAMP server that records messages
// and pushes package offers.
type opampTestServer struct {
	t     *testing.T
	srv   server.OpAMPServer
	mu    sync.Mutex
	conn  stypes.Connection
	msgs  []*protobufs.AgentToServer
	hdr   http.Header
	conns int
}

func newOpampTestServer(t *testing.T) *opampTestServer {
	s := &opampTestServer{t: t, srv: server.New(nil)}
	err := s.srv.Start(server.StartSettings{
		ListenEndpoint: "127.0.0.1:0",
		Settings: server.Settings{Callbacks: stypes.Callbacks{
			OnConnecting: func(r *http.Request) stypes.ConnectionResponse {
				s.mu.Lock()
				s.hdr = r.Header.Clone()
				s.mu.Unlock()
				return stypes.ConnectionResponse{Accept: true, ConnectionCallbacks: stypes.ConnectionCallbacks{
					OnConnected: func(ctx context.Context, c stypes.Connection) {
						s.mu.Lock()
						s.conn = c
						s.conns++
						s.mu.Unlock()
					},
					OnMessage: func(ctx context.Context, c stypes.Connection, m *protobufs.AgentToServer) *protobufs.ServerToAgent {
						s.mu.Lock()
						s.msgs = append(s.msgs, m)
						s.mu.Unlock()
						return &protobufs.ServerToAgent{
							InstanceUid: m.InstanceUid,
							Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus |
								protobufs.ServerCapabilities_ServerCapabilities_OffersPackages |
								protobufs.ServerCapabilities_ServerCapabilities_AcceptsPackagesStatus),
						}
					},
					OnConnectionClose: func(c stypes.Connection) {
						s.mu.Lock()
						if s.conn == c {
							s.conn = nil
						}
						s.mu.Unlock()
					},
				}}
			},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.srv.Stop(context.Background()) })
	return s
}

func (s *opampTestServer) url() string { return "ws://" + s.srv.Addr().String() + "/v1/opamp" }

func (s *opampTestServer) eventually(cond func() bool, msg string) {
	s.t.Helper()
	require.Eventually(s.t, cond, 20*time.Second, 10*time.Millisecond, msg)
}

func (s *opampTestServer) waitConnected() {
	s.t.Helper()
	s.eventually(func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.conn != nil && len(s.msgs) > 0
	}, "updater connects")
}

func (s *opampTestServer) first() *protobufs.AgentToServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) == 0 {
		return nil
	}
	return s.msgs[0]
}

// withDescription returns the latest message carrying an agent description.
func (s *opampTestServer) lastDescription() *protobufs.AgentDescription {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if d := s.msgs[i].AgentDescription; d != nil {
			return d
		}
	}
	return nil
}

func (s *opampTestServer) offerPackages(pkgs map[string]*protobufs.PackageAvailable, allHash []byte) {
	s.t.Helper()
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	require.NotNil(s.t, c, "no connection")
	require.NoError(s.t, c.Send(context.Background(), &protobufs.ServerToAgent{
		InstanceUid:       s.first().InstanceUid,
		PackagesAvailable: &protobufs.PackagesAvailable{Packages: pkgs, AllPackagesHash: allHash},
	}))
}

// status returns the latest reported status of the named package.
func (s *opampTestServer) status(name string) *protobufs.PackageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if ps := s.msgs[i].PackageStatuses; ps != nil {
			if st, ok := ps.Packages[name]; ok {
				return st
			}
		}
	}
	return nil
}

func (s *opampTestServer) seenStatuses(name string) map[protobufs.PackageStatusEnum]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[protobufs.PackageStatusEnum]bool{}
	for _, m := range s.msgs {
		if ps := m.PackageStatuses; ps != nil {
			if st, ok := ps.Packages[name]; ok {
				seen[st.Status] = true
			}
		}
	}
	return seen
}

func (s *opampTestServer) waitStatus(name string, want protobufs.PackageStatusEnum) *protobufs.PackageStatus {
	s.t.Helper()
	var got *protobufs.PackageStatus
	s.eventually(func() bool {
		got = s.status(name)
		return got != nil && got.Status == want
	}, "package status "+want.String())
	return got
}

// artifactServer serves archives under /api/v1/artifacts/<id>/content/<file>
// and requires the bearer token from the offer headers.
type artifactServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  atomic.Int32
	delay time.Duration
}

func newArtifactServer(t *testing.T) *artifactServer {
	a := &artifactServer{files: map[string][]byte{}}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer myrd_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.mu.Lock()
		data, ok := a.files[r.URL.Path]
		a.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		time.Sleep(a.delay)
		w.Header().Set("Content-Length", itoa64(len(data)))
		_, _ = w.Write(data)
	}))
	t.Cleanup(a.Close)
	return a
}

func itoa64(n int) string { return string(appendInt(nil, n)) }

func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}

func (a *artifactServer) add(version string, archive []byte) (url string) {
	p := "/api/v1/artifacts/" + version + "/content/otelcol-contrib_" + version + "_linux_amd64.tar.gz"
	a.mu.Lock()
	a.files[p] = archive
	a.mu.Unlock()
	return a.URL + p
}

func (a *artifactServer) offer(url string, version string, archive []byte) *protobufs.PackageAvailable {
	return &protobufs.PackageAvailable{
		Type:    protobufs.PackageType_PackageType_TopLevel,
		Version: version,
		Hash:    sha(append([]byte(version), archive...)),
		File: &protobufs.DownloadableFile{
			DownloadUrl: url,
			ContentHash: sha(archive),
			Signature:   []byte(`{"mediaType":"x"}`),
			Headers:     &protobufs.Headers{Headers: []*protobufs.Header{{Key: "Authorization", Value: "Bearer myrd_test"}}},
		},
	}
}

// updaterHarness runs a real Updater against the test server.
type updaterHarness struct {
	*env
	srv    *opampTestServer
	art    *artifactServer
	u      *Updater
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T) *updaterHarness {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	h := &updaterHarness{env: e, srv: newOpampTestServer(t), art: newArtifactServer(t)}
	h.cfg.Server.Endpoint = h.srv.url()
	h.cfg.Server.Token = "myre_testtoken"
	h.cfg.Update.AllowedURLPrefixes = []string{h.art.URL + "/api/v1/artifacts/"}
	h.cfg.Update.HealthTimeout = Duration(2 * time.Second)
	h.rebuild()
	return h
}

func (h *updaterHarness) start() {
	h.t.Helper()
	u, err := New(h.cfg, testLogger(h.t), Deps{
		Services: h.svc, Verifier: h.verifier, Version: "9.9.9", CollectorUIDRefresh: 50 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	})
	require.NoError(h.t, err)
	h.u = u
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		_ = u.Run(ctx)
	}()
	h.t.Cleanup(h.stop)
}

func (h *updaterHarness) stop() {
	if h.cancel != nil {
		h.cancel()
		<-h.done
		h.cancel = nil
	}
}

func attrMap(kvs []*protobufs.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.GetStringValue()
	}
	return m
}

func TestUpdaterRegistersWithLinkAndCapabilities(t *testing.T) {
	h := newHarness(t)
	collectorUID := uuid.NewString()
	require.NoError(t, os.WriteFile(h.cfg.SupervisorStatePath(), []byte("instance_id: "+collectorUID+"\n"), 0o600))
	h.start()
	h.srv.waitConnected()

	first := h.srv.first()
	d := first.AgentDescription
	require.NotNil(t, d)
	ident := attrMap(d.IdentifyingAttributes)
	non := attrMap(d.NonIdentifyingAttributes)
	assert.Equal(t, "myriarch-updater", ident["service.name"])
	assert.Equal(t, "9.9.9", ident["service.version"])
	assert.Equal(t, h.u.InstanceUID().String(), ident["service.instance.id"])
	assert.Equal(t, collectorUID, non["dev.myriarch.collector.instance_uid"], "link to the supervisor instance")
	assert.Equal(t, "otelcol-supervisor", non["dev.myriarch.updater.supervisor_service"])
	assert.Equal(t, "binary", non["dev.myriarch.updater.install_method"])
	assert.NotEmpty(t, non["os.type"])

	// instance uid on the wire is the persisted updater uid (UUID bytes)
	selfUID := h.u.InstanceUID()
	assert.Equal(t, selfUID[:], first.InstanceUid)

	want := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat
	assert.Equal(t, uint64(want), first.Capabilities)
	require.NotNil(t, first.Health)
	assert.True(t, first.Health.Healthy)

	h.srv.mu.Lock()
	auth := h.srv.hdr.Get("Authorization")
	h.srv.mu.Unlock()
	assert.Equal(t, "Bearer myre_testtoken", auth)
}

func TestUpdaterUsesTokenFileAndPicksUpRotation(t *testing.T) {
	h := newHarness(t)
	tokenFile := filepath.Join(h.root, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("myre_from_file\n"), 0o600))
	h.cfg.Server.Token = ""
	h.cfg.Server.TokenFile = tokenFile
	h.start()
	h.srv.waitConnected()
	h.srv.mu.Lock()
	assert.Equal(t, "Bearer myre_from_file", h.srv.hdr.Get("Authorization"))
	h.srv.mu.Unlock()
}

func TestUpdaterLinkAppearsLaterAndIsReported(t *testing.T) {
	h := newHarness(t)
	h.start() // no persistent_state.yaml yet (supervisor never started)
	h.srv.waitConnected()
	assert.NotContains(t, attrMap(h.srv.first().AgentDescription.NonIdentifyingAttributes), "dev.myriarch.collector.instance_uid")

	collectorUID := uuid.NewString()
	require.NoError(t, os.WriteFile(h.cfg.SupervisorStatePath(), []byte("instance_id: "+collectorUID+"\n"), 0o600))
	h.srv.eventually(func() bool {
		d := h.srv.lastDescription()
		return d != nil && attrMap(d.NonIdentifyingAttributes)["dev.myriarch.collector.instance_uid"] == collectorUID
	}, "description is re-sent with the collector link")

	// the supervisor re-keys itself (server assigned a new UID): link follows
	next := uuid.NewString()
	require.NoError(t, os.WriteFile(h.cfg.SupervisorStatePath(), []byte("instance_id: "+next+"\n"), 0o600))
	h.srv.eventually(func() bool {
		return attrMap(h.srv.lastDescription().NonIdentifyingAttributes)["dev.myriarch.collector.instance_uid"] == next
	}, "link follows instance id changes")
}

func TestUpdaterInstallsOfferedPackage(t *testing.T) {
	h := newHarness(t)
	h.art.delay = 150 * time.Millisecond
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()

	allHash := sha([]byte("offer-1"))
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, allHash)
	st := h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)

	assert.Equal(t, "v0.163.0", st.AgentHasVersion)
	assert.Equal(t, "v0.163.0", st.ServerOfferedVersion)
	assert.Equal(t, st.ServerOfferedHash, st.AgentHasHash)
	assert.Empty(t, st.ErrorMessage)
	seen := h.srv.seenStatuses("")
	assert.True(t, seen[protobufs.PackageStatusEnum_PackageStatusEnum_Installing], "INSTALLING reported")
	assert.True(t, seen[protobufs.PackageStatusEnum_PackageStatusEnum_Downloading], "DOWNLOADING reported")
	assert.False(t, seen[protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed])
	h.srv.mu.Lock()
	last := h.srv.msgs[len(h.srv.msgs)-1]
	h.srv.mu.Unlock()
	_ = last

	assert.Equal(t, "0.163.0", h.active())
	assert.Equal(t, []string{"stop:" + testSupervisorSvc, "start:" + testSupervisorSvc}, h.svc.Calls())
	assert.Equal(t, 1, h.verifier.calls)
	assert.JSONEq(t, `{"mediaType":"x"}`, string(h.verifier.lastSig), "signature from the offer reaches the verifier")
	h.noLeftovers()

	// reported statuses carry the server-provided all-packages hash
	h.srv.mu.Lock()
	var gotHash []byte
	for _, m := range h.srv.msgs {
		if m.PackageStatuses != nil {
			gotHash = m.PackageStatuses.ServerProvidedAllPackagesHash
		}
	}
	h.srv.mu.Unlock()
	assert.Equal(t, allHash, gotHash)

	// health returns to idle without errors
	h.srv.eventually(func() bool {
		h.srv.mu.Lock()
		defer h.srv.mu.Unlock()
		m := h.srv.msgs[len(h.srv.msgs)-1]
		return m.Health == nil || m.Health.LastError == ""
	}, "no lingering health error")
}

func TestUpdaterRepeatedOfferOfInstalledContentIsSkipped(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()

	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	hits := h.art.hits.Load()
	calls := len(h.svc.Calls())

	// same content under a new all-hash (e.g. the server restarted and re-sent)
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("2")))
	h.srv.eventually(func() bool {
		h.srv.mu.Lock()
		defer h.srv.mu.Unlock()
		for i := len(h.srv.msgs) - 1; i >= 0; i-- {
			if ps := h.srv.msgs[i].PackageStatuses; ps != nil {
				return string(ps.ServerProvidedAllPackagesHash) == string(sha([]byte("2")))
			}
		}
		return false
	}, "status for the second offer")
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	assert.Equal(t, hits, h.art.hits.Load(), "no second download")
	assert.Len(t, h.svc.Calls(), calls, "no second restart")
}

func TestUpdaterWithdrawnOfferDoesNotUninstall(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	calls := len(h.svc.Calls())

	// the offer set is now empty (rollout finished): local package must be "deleted" - but not from disk
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{}, sha([]byte("empty")))
	h.srv.eventually(func() bool {
		got := false
		h.u.st.read(func(st *stateFile) {
			_, has := st.Packages[""]
			got = !has && string(st.AllPackagesHash) == string(sha([]byte("empty")))
		})
		return got
	}, "state forgotten")
	assert.Equal(t, "0.163.0", h.active())
	assert.FileExists(t, h.layout.BinaryPath("0.163.0"))
	assert.Len(t, h.svc.Calls(), calls, "no restart, nothing uninstalled")
}

func TestUpdaterWrongHashReportsInstallFailed(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	p := h.art.offer(url, "v0.163.0", archive)
	p.File.ContentHash = sha([]byte("different"))
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": p}, sha([]byte("1")))
	st := h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	assert.Contains(t, st.ErrorMessage, "SHA-256 mismatch")
	assert.Equal(t, "0.162.0", h.active())
	assert.Empty(t, h.svc.Calls(), "supervisor untouched")
}

func TestUpdaterRejectsURLOutsideAllowlist(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.163.0")
	evil := newArtifactServer(t)
	url := evil.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	st := h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	assert.Contains(t, st.ErrorMessage, "allowed URL prefix")
	assert.Zero(t, evil.hits.Load(), "no request may reach a non-allowlisted host")
	assert.Empty(t, h.svc.Calls())
}

func TestUpdaterRejectsUnsupportedPackageName(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"opampsupervisor": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	st := h.srv.waitStatus("opampsupervisor", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	assert.Contains(t, st.ErrorMessage, "unsupported package")
	assert.Zero(t, h.art.hits.Load(), "unsupported packages are not even downloaded")
	assert.Empty(t, h.svc.Calls())
}

func TestUpdaterRejectsSignatureFailure(t *testing.T) {
	h := newHarness(t)
	h.verifier.err = assert.AnError
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	st := h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	assert.Contains(t, st.ErrorMessage, "signature verification")
	assert.Empty(t, h.svc.Calls())
	assert.Equal(t, "0.162.0", h.active())
}

func TestUpdaterRollbackAndNoRetryLoop(t *testing.T) {
	h := newHarness(t)
	h.healthy = func(active string) bool { return active != "0.163.0" }
	h.cfg.Update.HealthTimeout = Duration(300 * time.Millisecond)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()

	offer := h.art.offer(url, "v0.163.0", archive)
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": offer}, sha([]byte("1")))
	st := h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	assert.Contains(t, st.ErrorMessage, "rolled back to v0.162.0")
	assert.Equal(t, "0.162.0", h.active())
	assert.Len(t, h.svc.Calls(), 4, "swap + rollback")
	hits := h.art.hits.Load()

	// The server re-sends the same offer (reconnect, new all-hash): no new download, no restart.
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": offer}, sha([]byte("2")))
	h.srv.eventually(func() bool {
		s := h.srv.status("")
		return s != nil && s.Status == protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed &&
			bytes.Contains([]byte(s.ErrorMessage), []byte("not retrying"))
	}, "retry suppressed")
	assert.Equal(t, hits, h.art.hits.Load())
	assert.Len(t, h.svc.Calls(), 4, "the supervisor is not restarted again")
}

func TestUpdaterAcceptsNewInstanceUIDAndPersistsIt(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.srv.waitConnected()
	newUID := uuid.New()
	h.srv.mu.Lock()
	c := h.srv.conn
	first := h.srv.msgs[0]
	h.srv.mu.Unlock()
	require.NoError(t, c.Send(context.Background(), &protobufs.ServerToAgent{
		InstanceUid:         first.InstanceUid,
		AgentIdentification: &protobufs.AgentIdentification{NewInstanceUid: newUID[:]},
	}))
	h.srv.eventually(func() bool { return h.u.InstanceUID() == newUID }, "uid adopted")
	data, err := os.ReadFile(filepath.Join(h.cfg.StateDir, "instance_uid"))
	require.NoError(t, err)
	assert.Contains(t, string(data), newUID.String())
}

func TestUpdaterRestartKeepsStateAndDoesNotReinstall(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	allHash := sha([]byte("1"))
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, allHash)
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	uid := h.u.InstanceUID()
	hits := h.art.hits.Load()
	h.stop()

	// restart the updater process: same uid, remembered all-packages hash
	h.srv.mu.Lock()
	h.srv.msgs = nil
	h.srv.mu.Unlock()
	h.start()
	h.srv.waitConnected()
	assert.Equal(t, uid, h.u.InstanceUID())
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, allHash)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, hits, h.art.hits.Load())
	st := h.srv.status("")
	require.NotNil(t, st, "persisted statuses are reported after restart")
	assert.Equal(t, protobufs.PackageStatusEnum_PackageStatusEnum_Installed, st.Status)
}

func TestProviderMarksInterruptedInstallsFailed(t *testing.T) {
	dir := t.TempDir()
	st, err := openStateStore(dir)
	require.NoError(t, err)
	p := &packagesProvider{st: st}
	require.NoError(t, st.setLastStatuses(&protobufs.PackageStatuses{
		ServerProvidedAllPackagesHash: []byte{1},
		Packages: map[string]*protobufs.PackageStatus{"": {Name: "", Status: protobufs.PackageStatusEnum_PackageStatusEnum_Downloading,
			DownloadDetails: &protobufs.PackageDownloadDetails{DownloadPercent: 40}}},
	}))
	require.NoError(t, st.update(func(s *stateFile) { s.AllPackagesHash = []byte{1} }))
	p.markInterruptedStatuses()
	got, _ := st.lastStatuses()
	require.NotNil(t, got)
	assert.Equal(t, protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed, got.Packages[""].Status)
	assert.Contains(t, got.Packages[""].ErrorMessage, "restarted")
	assert.Nil(t, got.Packages[""].DownloadDetails)
	hash, _ := p.AllPackagesHash()
	assert.Empty(t, hash, "forces a re-sync of the next offer")
}

func TestProviderCreatePackageValidation(t *testing.T) {
	st, _ := openStateStore(t.TempDir())
	p := &packagesProvider{st: st}
	require.NoError(t, p.CreatePackage("", protobufs.PackageType_PackageType_TopLevel))
	require.Error(t, p.CreatePackage("x", protobufs.PackageType_PackageType_TopLevel))
	require.Error(t, p.CreatePackage("", protobufs.PackageType_PackageType_Addon))
	ps, _ := p.PackageState("")
	assert.True(t, ps.Exists)
	require.NoError(t, p.DeletePackage(""))
	ps, _ = p.PackageState("")
	assert.False(t, ps.Exists)
}

func TestStateStoreCorruptFileRecovers(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "packages.json"), []byte("{broken"), 0o600))
	st, err := openStateStore(dir)
	require.NoError(t, err)
	require.NoError(t, st.update(func(s *stateFile) { s.AllPackagesHash = []byte{7} }))
	st2, err := openStateStore(dir)
	require.NoError(t, err)
	var h []byte
	st2.read(func(s *stateFile) { h = s.AllPackagesHash })
	assert.Equal(t, []byte{7}, h)
}
