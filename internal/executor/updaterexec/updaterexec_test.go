// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updaterexec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

type fakeArtifacts struct {
	mu     sync.Mutex
	calls  map[string]int
	arts   map[string]*domain.Artifact
	errs   map[string]error
	tokens int
	// updaters records the updater agents URLs were requested for.
	updaters []domain.AgentID
	// requireSig makes the resolver report that signatures are mandatory
	requireSig bool
}

func (f *fakeArtifacts) Ensure(ctx context.Context, dist, version, os, arch string) (*domain.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := os + "/" + arch
	f.calls[key]++
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return f.arts[key], nil
}

// DownloadURLFor mimics artifacts.agentBaseUrls: updaters connected to
// ws://myriarch:8080 download from the internal service, all others from
// the public route.
func (f *fakeArtifacts) DownloadURLFor(a *domain.Artifact, updater *domain.Agent) (string, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens++
	f.updaters = append(f.updaters, updater.ID)
	base := "https://fleet.example.com"
	if updater.NonIdentifyingAttributes[domain.AttrUpdaterServerEndpoint] == "ws://myriarch:8080" {
		base = "http://myriarch:8080"
	}
	return base + "/api/v1/artifacts/" + a.ID.String() + "/content/" + a.Filename,
		map[string]string{"Authorization": fmt.Sprintf("Bearer myrd_token-%d", f.tokens)}, nil
}

type recControl struct {
	control.AgentControl
	mu   sync.Mutex
	last [][]domain.AgentID
}

func (r *recControl) DesiredStateChanged(_ context.Context, ids []domain.AgentID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = append(r.last, ids)
	return nil
}

type fx struct {
	st   *memstore.Store
	art  *fakeArtifacts
	ctl  *recControl
	exec executor.Executor
}

func newFx(t *testing.T) *fx {
	f := &fx{st: memstore.New(), ctl: &recControl{}}
	f.art = &fakeArtifacts{calls: map[string]int{}, errs: map[string]error{}, arts: map[string]*domain.Artifact{}}
	for _, p := range []string{"linux/amd64", "windows/amd64"} {
		os, arch := "linux", "amd64"
		if p == "windows/amd64" {
			os = "windows"
		}
		sum := domain.SHA256([]byte(p))
		f.art.arts[p] = &domain.Artifact{
			ID: uuid.Must(uuid.NewV7()), Distribution: "otelcol-contrib", Version: "0.162.0", OS: os, Arch: arch, Format: "tar.gz",
			Filename: fmt.Sprintf("otelcol-contrib_0.162.0_%s_%s.tar.gz", os, arch), SHA256: sum, SigstoreBundle: []byte(`{"bundle":"` + p + `"}`),
			Status: domain.ArtifactReady,
		}
	}
	f.exec = New(Deps{Store: f.st, Control: f.ctl, Artifacts: f.art, Now: func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }})
	return f
}

func (f *fx) addCollectorWithUpdater(t *testing.T, osType string, updaterOnline bool) executor.Target {
	collector := uuid.Must(uuid.NewV7())
	updater := uuid.Must(uuid.NewV7())
	status := domain.ConnectionOnline
	if !updaterOnline {
		status = domain.ConnectionOffline
	}
	f.st.PutAgent(domain.Agent{ID: updater, Kind: domain.AgentKindUpdater, Connection: domain.AgentConnection{Status: status}})
	f.st.PutAgent(domain.Agent{ID: collector, Kind: domain.AgentKindCollector, OSType: osType, Arch: "amd64", Updater: &domain.UpdaterLink{AgentID: updater}})
	return executor.Target{AgentID: collector, Hostname: "h", OSType: osType, Arch: "amd64", UpdaterAgentID: &updater, Attempt: 1}
}

func (f *fx) req(ts ...executor.Target) executor.UpdateRequest {
	return executor.UpdateRequest{
		RolloutID: uuid.MustParse("01a11121-e597-7b69-9d25-a80cc7b9c707"), Wave: 0, Fleet: "edge-nl",
		Distribution: "otelcol-contrib", ToVersion: "0.162.0", Targets: ts, Spec: domain.UpdateSpec{Executor: domain.ExecutorUpdater},
	}
}

func TestType(t *testing.T) {
	f := newFx(t)
	assert.Equal(t, domain.ExecutorUpdater, f.exec.Type())
	assert.Empty(t, f.exec.Validate(domain.UpdateSpec{}, nil))
}

func TestStartBuildsPackageOffers(t *testing.T) {
	f := newFx(t)
	lin := f.addCollectorWithUpdater(t, "linux", true)
	win := f.addCollectorWithUpdater(t, "windows", true)
	lin2 := f.addCollectorWithUpdater(t, "linux", true)

	res, err := f.exec.Start(context.Background(), f.req(lin, win, lin2))
	require.NoError(t, err)
	require.Len(t, res, 3)
	for i, tg := range []executor.Target{lin, win, lin2} {
		assert.Equal(t, tg.AgentID, res[i].AgentID)
		assert.True(t, res[i].Started)
		assert.Empty(t, res[i].Error)
	}
	assert.Equal(t, 1, f.art.calls["linux/amd64"], "one Ensure per platform")
	assert.Equal(t, 1, f.art.calls["windows/amd64"])

	offers, err := f.st.Packages().GetOffers(context.Background(), *win.UpdaterAgentID)
	require.NoError(t, err)
	require.Len(t, offers, 1, "exactly the collector package")
	o := offers[0]
	art := f.art.arts["windows/amd64"]
	assert.Equal(t, *win.UpdaterAgentID, o.AgentID, "the offer goes to the UPDATER agent")
	assert.Equal(t, domain.CollectorPackageName, o.Name, "empty package name = collector")
	assert.Equal(t, "v0.162.0", o.Version, "with leading v (upstream convention)")
	assert.Equal(t, "https://fleet.example.com/api/v1/artifacts/"+art.ID.String()+"/content/otelcol-contrib_0.162.0_windows_amd64.tar.gz", o.DownloadURL, "tar.gz for windows, too")
	assert.Equal(t, art.SHA256, o.ContentHash)
	assert.Equal(t, art.SigstoreBundle, o.Signature, "the sigstore bundle is the signature")
	assert.Empty(t, o.Headers, "the download token is minted when the offer is sent, never stored")
	require.NotNil(t, o.RolloutID)
	assert.Equal(t, f.req().RolloutID, *o.RolloutID)
	assert.False(t, o.CreatedAt.IsZero())

	f.ctl.mu.Lock()
	defer f.ctl.mu.Unlock()
	require.Len(t, f.ctl.last, 1)
	assert.ElementsMatch(t, []domain.AgentID{*lin.UpdaterAgentID, *win.UpdaterAgentID, *lin2.UpdaterAgentID}, f.ctl.last[0], "DesiredStateChanged for the updaters")
}

func TestStartIsIdempotentForThePackageHash(t *testing.T) {
	f := newFx(t)
	lin := f.addCollectorWithUpdater(t, "linux", true)
	_, err := f.exec.Start(context.Background(), f.req(lin))
	require.NoError(t, err)
	first, _ := f.st.Packages().GetOffers(context.Background(), *lin.UpdaterAgentID)
	_, err = f.exec.Start(context.Background(), f.req(lin))
	require.NoError(t, err)
	second, _ := f.st.Packages().GetOffers(context.Background(), *lin.UpdaterAgentID)
	assert.Len(t, second, 1, "SetOffers replaces: never more than one offer")
	assert.Empty(t, second[0].Headers, "no token is stored in the offer")
	assert.Equal(t, domain.PackageHash(first[0]), domain.PackageHash(second[0]), "...but the OpAMP package hash stays the same: no re-download")
	assert.Equal(t, domain.AllPackagesHash(first), domain.AllPackagesHash(second))
}

func TestStartPerTargetErrors(t *testing.T) {
	f := newFx(t)
	good := f.addCollectorWithUpdater(t, "linux", true)
	offline := f.addCollectorWithUpdater(t, "linux", false)
	noUpdater := executor.Target{AgentID: uuid.Must(uuid.NewV7()), OSType: "linux", Arch: "amd64"}
	unknown := uuid.Must(uuid.NewV7())
	ghost := executor.Target{AgentID: uuid.Must(uuid.NewV7()), OSType: "linux", Arch: "amd64", UpdaterAgentID: &unknown}
	notUpdater := f.addCollectorWithUpdater(t, "linux", true)
	f.st.UpdateAgent(*notUpdater.UpdaterAgentID, func(a *domain.Agent) { a.Kind = domain.AgentKindCollector })
	f.art.errs["windows/amd64"] = errors.New("checksum mismatch")
	winBroken := f.addCollectorWithUpdater(t, "windows", true)

	res, err := f.exec.Start(context.Background(), f.req(good, offline, noUpdater, ghost, notUpdater, winBroken))
	require.NoError(t, err, "per-target problems are results, not a call error")
	assert.True(t, res[0].Started)
	assert.Equal(t, "myriarch-updater offline", res[1].Error)
	assert.Equal(t, "no myriarch-updater linked to this collector", res[2].Error)
	assert.Contains(t, res[3].Error, "is unknown")
	assert.Contains(t, res[4].Error, "is not a myriarch-updater")
	assert.Contains(t, res[5].Error, "artifact otelcol-contrib 0.162.0 for windows/amd64 is not available: checksum mismatch")
	for _, i := range []int{1, 2, 3, 4, 5} {
		assert.False(t, res[i].Started)
	}
	// only the good one got an offer
	o, _ := f.st.Packages().GetOffers(context.Background(), *offline.UpdaterAgentID)
	assert.Empty(t, o)
	o, _ = f.st.Packages().GetOffers(context.Background(), *good.UpdaterAgentID)
	assert.Len(t, o, 1)
	assert.Equal(t, 0, f.art.calls["windows/amd64"]-1, "the artifact of a platform is requested once")
}

func TestStartKeepsTargetsPendingWhileTheArtifactIsDownloading(t *testing.T) {
	f := newFx(t)
	lin := f.addCollectorWithUpdater(t, "linux", true)
	win := f.addCollectorWithUpdater(t, "windows", true)
	f.art.errs["windows/amd64"] = context.DeadlineExceeded // still downloading in the background
	res, err := f.exec.Start(context.Background(), f.req(lin, win))
	require.NoError(t, err)
	assert.True(t, res[0].Started)
	assert.False(t, res[1].Started)
	assert.Empty(t, res[1].Error, "not a failure")
	assert.True(t, res[1].RetryLater, "understood by the rollout engine")
	o, _ := f.st.Packages().GetOffers(context.Background(), *win.UpdaterAgentID)
	assert.Empty(t, o)

	// next call: the artifact is ready
	delete(f.art.errs, "windows/amd64")
	res, err = f.exec.Start(context.Background(), f.req(win))
	require.NoError(t, err)
	assert.True(t, res[0].Started)
}

func TestStartReturnsCallerContextError(t *testing.T) {
	f := newFx(t)
	lin := f.addCollectorWithUpdater(t, "linux", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.art.errs["linux/amd64"] = context.Canceled
	_, err := f.exec.Start(ctx, f.req(lin))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStartEmpty(t *testing.T) {
	f := newFx(t)
	res, err := f.exec.Start(context.Background(), f.req())
	require.NoError(t, err)
	assert.Empty(t, res)
}

func TestCancelClearsOnlyOffersOfTheRollout(t *testing.T) {
	f := newFx(t)
	a := f.addCollectorWithUpdater(t, "linux", true)
	b := f.addCollectorWithUpdater(t, "linux", true)
	c := f.addCollectorWithUpdater(t, "linux", true)
	_, err := f.exec.Start(context.Background(), f.req(a, b))
	require.NoError(t, err)
	// c's updater already holds an offer of a SUCCESSOR rollout
	other := uuid.New()
	require.NoError(t, f.st.Packages().SetOffers(context.Background(), *c.UpdaterAgentID, []domain.PackageOffer{{AgentID: *c.UpdaterAgentID, Version: "v0.163.0", RolloutID: &other}}))
	f.ctl.mu.Lock()
	f.ctl.last = nil
	f.ctl.mu.Unlock()

	err = f.exec.Cancel(context.Background(), executor.CancelRequest{RolloutID: f.req().RolloutID, AgentIDs: []domain.AgentID{a.AgentID, b.AgentID, c.AgentID, uuid.New()}})
	require.NoError(t, err)
	for _, x := range []executor.Target{a, b} {
		o, _ := f.st.Packages().GetOffers(context.Background(), *x.UpdaterAgentID)
		assert.Empty(t, o)
	}
	o, _ := f.st.Packages().GetOffers(context.Background(), *c.UpdaterAgentID)
	assert.Len(t, o, 1, "the successor's offer stays")
	f.ctl.mu.Lock()
	defer f.ctl.mu.Unlock()
	require.Len(t, f.ctl.last, 1)
	assert.ElementsMatch(t, []domain.AgentID{*a.UpdaterAgentID, *b.UpdaterAgentID}, f.ctl.last[0], "DesiredStateChanged so that the OpAMP layer refreshes its connection cache")

	// nothing to cancel is fine
	assert.NoError(t, f.exec.Cancel(context.Background(), executor.CancelRequest{}))
	assert.NoError(t, f.exec.Cancel(context.Background(), executor.CancelRequest{RolloutID: f.req().RolloutID, AgentIDs: []domain.AgentID{a.AgentID}}))
}

func (f *fakeArtifacts) SignatureRequired() bool { return f.requireSig }

// AC-06: several online updaters claiming one collector are an error, never "most recent wins".
func TestStartRefusesAmbiguousUpdaterLinks(t *testing.T) {
	f := newFx(t)
	tg := f.addCollectorWithUpdater(t, "linux", true)
	f.st.UpdateAgent(tg.AgentID, func(a *domain.Agent) { a.Updater.Ambiguous = true })
	res, err := f.exec.Start(context.Background(), f.req(tg))
	require.NoError(t, err)
	assert.False(t, res[0].Started)
	assert.Contains(t, res[0].Error, "ambiguous updater link")
	offers, _ := f.st.Packages().GetOffers(context.Background(), *tg.UpdaterAgentID)
	assert.Empty(t, offers, "no offer for an ambiguous link")
}

// AC-06: the credential binding between updater and collector is checked again right before the offer.
func TestStartRefusesUpdatersOfAnotherCredentialOrHost(t *testing.T) {
	f := newFx(t)
	tokA, tokB := uuid.New(), uuid.New()
	same := f.addCollectorWithUpdater(t, "linux", true)
	otherToken := f.addCollectorWithUpdater(t, "linux", true)
	otherHost := f.addCollectorWithUpdater(t, "linux", true)
	for _, tg := range []executor.Target{same, otherToken, otherHost} {
		f.st.UpdateAgent(tg.AgentID, func(a *domain.Agent) { a.EnrollmentTokenID, a.Hostname = &tokA, "host-1" })
		f.st.UpdateAgent(*tg.UpdaterAgentID, func(a *domain.Agent) { a.EnrollmentTokenID, a.Hostname = &tokA, "host-1" })
	}
	f.st.UpdateAgent(*otherToken.UpdaterAgentID, func(a *domain.Agent) { a.EnrollmentTokenID = &tokB })
	f.st.UpdateAgent(*otherHost.UpdaterAgentID, func(a *domain.Agent) { a.Hostname = "host-2" })

	res, err := f.exec.Start(context.Background(), f.req(same, otherToken, otherHost))
	require.NoError(t, err)
	assert.True(t, res[0].Started, res[0].Error)
	assert.False(t, res[1].Started)
	assert.Contains(t, res[1].Error, "different enrollment tokens")
	assert.False(t, res[2].Started)
	assert.Contains(t, res[2].Error, "different host names")
	for _, tg := range []executor.Target{otherToken, otherHost} {
		offers, _ := f.st.Packages().GetOffers(context.Background(), *tg.UpdaterAgentID)
		assert.Empty(t, offers)
	}
}

// AC-19: only verified artifacts are offered, with the bundle the updater checks again.
func TestStartOffersOnlyVerifiedArtifacts(t *testing.T) {
	f := newFx(t)
	tg := f.addCollectorWithUpdater(t, "linux", true)
	art := f.art.arts["linux/amd64"]

	// signatures required: unverified or bundle-less artifacts are refused
	f.art.requireSig = true
	art.SignatureVerified = false
	res, _ := f.exec.Start(context.Background(), f.req(tg))
	assert.False(t, res[0].Started)
	assert.Contains(t, res[0].Error, "no verified sigstore bundle")
	art.SignatureVerified, art.SigstoreBundle = true, nil
	res, _ = f.exec.Start(context.Background(), f.req(tg))
	assert.False(t, res[0].Started, "an empty bundle cannot be verified by the updater")
	art.SigstoreBundle = []byte(`{"bundle":true}`)

	// not ready / hash of the wrong size
	art.Status = domain.ArtifactDownloading
	res, _ = f.exec.Start(context.Background(), f.req(tg))
	assert.False(t, res[0].Started)
	art.Status, art.SHA256 = domain.ArtifactReady, domain.Hash("short")
	res, _ = f.exec.Start(context.Background(), f.req(tg))
	assert.False(t, res[0].Started)
	assert.Contains(t, res[0].Error, "no SHA-256")

	// everything in order
	art.SHA256 = domain.SHA256([]byte("ok"))
	res, _ = f.exec.Start(context.Background(), f.req(tg))
	assert.True(t, res[0].Started, res[0].Error)
	offers, _ := f.st.Packages().GetOffers(context.Background(), *tg.UpdaterAgentID)
	require.Len(t, offers, 1)
	assert.Equal(t, art.SigstoreBundle, offers[0].Signature)
	assert.Empty(t, offers[0].Headers, "the download token is minted when the offer is sent")
}

// TestStartUsesTheDownloadBaseOfTheUpdater: the URL of an offer is built for
// the updater agent (its reported server endpoint selects the base URL).
func TestStartUsesTheDownloadBaseOfTheUpdater(t *testing.T) {
	f := newFx(t)
	internal := f.addCollectorWithUpdater(t, "linux", true)
	f.st.UpdateAgent(*internal.UpdaterAgentID, func(a *domain.Agent) {
		a.NonIdentifyingAttributes = map[string]string{domain.AttrUpdaterServerEndpoint: "ws://myriarch:8080"}
	})
	external := f.addCollectorWithUpdater(t, "linux", true)

	res, err := f.exec.Start(context.Background(), f.req(internal, external))
	require.NoError(t, err)
	require.True(t, res[0].Started && res[1].Started)
	assert.ElementsMatch(t, []domain.AgentID{*internal.UpdaterAgentID, *external.UpdaterAgentID}, f.art.updaters, "URLs are requested for the updater agents, not the collectors")

	art := f.art.arts["linux/amd64"]
	o1, _ := f.st.Packages().GetOffers(context.Background(), *internal.UpdaterAgentID)
	o2, _ := f.st.Packages().GetOffers(context.Background(), *external.UpdaterAgentID)
	assert.Equal(t, "http://myriarch:8080/api/v1/artifacts/"+art.ID.String()+"/content/"+art.Filename, o1[0].DownloadURL)
	assert.Equal(t, "https://fleet.example.com/api/v1/artifacts/"+art.ID.String()+"/content/"+art.Filename, o2[0].DownloadURL)
}
