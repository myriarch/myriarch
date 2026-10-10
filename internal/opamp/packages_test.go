// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
)

func updaterFirst(id domain.AgentID, collector domain.AgentID, pkgHash []byte) *protobufs.AgentToServer {
	d := description(id, descOpts{host: "host1", version: "0.1.0", service: domain.UpdaterServiceName,
		extra: []*protobufs.KeyValue{kv(domain.AttrUpdaterCollectorUID, collector.String())}})
	return &protobufs.AgentToServer{InstanceUid: id[:], AgentDescription: d, Health: healthy(),
		PackageStatuses: &protobufs.PackageStatuses{ServerProvidedAllPackagesHash: pkgHash, Packages: map[string]*protobufs.PackageStatus{
			"": {Name: "", AgentHasVersion: "v0.162.0", AgentHasHash: []byte("h162"), Status: protobufs.PackageStatusEnum_PackageStatusEnum_Installed}}}}
}

func TestUpdaterPackageOffers(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	collector := newAgentID()
	updater := newAgentID()
	u := r.connect("", updater, capsUpdater)
	u.send(updaterFirst(updater, collector, []byte{}))
	m := u.mustRecv()
	if m.PackagesAvailable != nil || m.RemoteConfig != nil || m.ErrorResponse != nil {
		t.Fatalf("a fresh updater without offers receives no packages_available (an empty set would delete local packages): %v", m)
	}
	ag := r.agent(updater)
	if ag.Kind != domain.AgentKindUpdater || ag.Updater == nil || ag.Updater.AgentID != collector {
		t.Fatalf("updater registration and link: %+v %+v", ag.Kind, ag.Updater)
	}
	if r.assigner.calls.Load() != 0 {
		t.Fatal("updaters are not assigned to fleets")
	}
	if ag.PackageStatus == nil || ag.PackageStatus.Packages[""].Status != domain.PackageInstalled || ag.PackageStatus.Packages[""].AgentHasVersion != "v0.162.0" {
		t.Fatalf("package statuses: %+v", ag.PackageStatus)
	}
	for i := 0; i < 3; i++ {
		u.send(&protobufs.AgentToServer{})
		if m := u.mustRecv(); m.PackagesAvailable != nil {
			t.Fatal("no offers: never send packages_available")
		}
	}

	// the executor sets an offer and calls DesiredStateChanged
	rid := newAgentID()
	offer := domain.PackageOffer{Name: domain.CollectorPackageName, Version: "v0.163.0", DownloadURL: "https://fleet.example.com/api/v1/artifacts/abc/content/otelcol-contrib_0.163.0_linux_amd64.tar.gz",
		ContentHash: domain.SHA256([]byte("archive")), Signature: []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`),
		Headers: map[string]string{"Authorization": "Bearer myrd_token", "X-Other": "1"}}
	_ = rid
	if err := r.db.Packages().SetOffers(ctx, updater, []domain.PackageOffer{offer}); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.DesiredStateChanged(ctx, []domain.AgentID{updater}); err != nil {
		t.Fatal(err)
	}
	m = u.mustRecv()
	pa := m.PackagesAvailable
	if pa == nil || len(pa.Packages) != 1 {
		t.Fatalf("expected packages_available: %v", m)
	}
	if !domain.Hash(pa.AllPackagesHash).Equal(domain.AllPackagesHash([]domain.PackageOffer{offer})) {
		t.Fatal("all_packages_hash must equal domain.AllPackagesHash(offers)")
	}
	p, ok := pa.Packages[""]
	if !ok {
		t.Fatalf("collector package must have the empty name: %v", pa.Packages)
	}
	if p.Type != protobufs.PackageType_PackageType_TopLevel || p.Version != "v0.163.0" || p.File.DownloadUrl != offer.DownloadURL ||
		string(p.File.ContentHash) != string(offer.ContentHash) || string(p.File.Signature) != string(offer.Signature) ||
		!domain.Hash(p.Hash).Equal(domain.PackageHash(offer)) {
		t.Fatalf("package: %+v", p)
	}
	hdrs := map[string]string{}
	for _, h := range p.File.Headers.GetHeaders() {
		hdrs[h.Key] = h.Value
	}
	if hdrs["Authorization"] != "Bearer myrd_token" || hdrs["X-Other"] != "1" || len(hdrs) != 2 {
		t.Fatalf("download headers: %v", hdrs)
	}
	r.waitEvent(updater, domain.AgentEventPackageOffered)

	// the updater starts the sync and reports the hash it received: no re-offer
	u.send(&protobufs.AgentToServer{PackageStatuses: &protobufs.PackageStatuses{ServerProvidedAllPackagesHash: pa.AllPackagesHash, Packages: map[string]*protobufs.PackageStatus{
		"": {Name: "", AgentHasVersion: "v0.162.0", ServerOfferedVersion: "v0.163.0", Status: protobufs.PackageStatusEnum_PackageStatusEnum_Downloading,
			DownloadDetails: &protobufs.PackageDownloadDetails{DownloadPercent: 42}}}}})
	if m := u.mustRecv(); m.PackagesAvailable != nil {
		t.Fatal("offer repeated although the updater reported the offered hash")
	}
	r.waitEvent(updater, domain.AgentEventPackageStatus)
	if got := r.agent(updater).PackageStatus.Packages[""]; got.Status != domain.PackageDownloading || got.DownloadPercent != 42 {
		t.Fatalf("download progress: %+v", got)
	}
	u.send(&protobufs.AgentToServer{PackageStatuses: &protobufs.PackageStatuses{ServerProvidedAllPackagesHash: pa.AllPackagesHash, Packages: map[string]*protobufs.PackageStatus{
		"": {Name: "", AgentHasVersion: "v0.162.0", ServerOfferedVersion: "v0.163.0", Status: protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed, ErrorMessage: "sha256 mismatch"}}}})
	u.mustRecv()
	eventually(t, "install failure recorded", 5*time.Second, func() bool {
		got := r.agent(updater).PackageStatus
		return got != nil && got.Packages[""].Status == domain.PackageInstallFailed && got.Packages[""].ErrorMessage == "sha256 mismatch"
	})
	eventually(t, "error event for install_failed", 5*time.Second, func() bool {
		evs, _ := r.db.Agents().ListEvents(ctx, updater, 50, 0)
		for _, e := range evs {
			if e.Type == domain.AgentEventPackageStatus && e.Severity == "error" {
				return true
			}
		}
		return false
	})

	// retry-failed: the engine stores the identical offer again (new download token
	// and creation time). The updater still reports the offered hash (opamp-go does
	// so as soon as a sync starts), but must get the offer again exactly once.
	offer.Headers = map[string]string{"Authorization": "Bearer myrd_token_2", "X-Other": "1"}
	if err := r.db.Packages().SetOffers(ctx, updater, []domain.PackageOffer{offer}); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.DesiredStateChanged(ctx, []domain.AgentID{updater}); err != nil {
		t.Fatal(err)
	}
	m = u.mustRecv()
	if m.PackagesAvailable == nil || m.PackagesAvailable.Packages[""].File.Headers.GetHeaders()[0].Value != "Bearer myrd_token_2" {
		t.Fatalf("a re-issued offer after INSTALL_FAILED must be sent again with the new token: %v", m)
	}
	// the retry fails the same way: no loop without another re-issued offer
	u.send(&protobufs.AgentToServer{PackageStatuses: &protobufs.PackageStatuses{ServerProvidedAllPackagesHash: pa.AllPackagesHash, Packages: map[string]*protobufs.PackageStatus{
		"": {Name: "", AgentHasVersion: "v0.162.0", ServerOfferedVersion: "v0.163.0", Status: protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed, ErrorMessage: "sha256 mismatch"}}}})
	if m := u.mustRecv(); m.PackagesAvailable != nil {
		t.Fatal("the same offer must not be repeated while the updater keeps failing")
	}

	// a reconnect of an updater that still reports the OLD hash gets the offer again
	u.close()
	eventually(t, "updater offline", 5*time.Second, func() bool { return !r.agent(updater).IsOnline() })
	u2 := r.connect("", updater, capsUpdater)
	u2.send(updaterFirst(updater, collector, []byte("old-hash")))
	if m := u2.mustRecv(); m.PackagesAvailable == nil {
		t.Fatal("offer must be sent again to an updater that reports another all_packages_hash")
	}

	// clearing the offers sends nothing (the syncer would delete local packages)
	if err := r.db.Packages().ClearOffers(ctx, updater); err != nil {
		t.Fatal(err)
	}
	_ = r.ctl.DesiredStateChanged(ctx, []domain.AgentID{updater})
	u2.expectSilence(500 * time.Millisecond)
	u2.send(&protobufs.AgentToServer{PackageStatuses: &protobufs.PackageStatuses{ServerProvidedAllPackagesHash: []byte("x")}})
	if m := u2.mustRecv(); m.PackagesAvailable != nil {
		t.Fatal("empty offer sets must never be sent")
	}
}

func TestUpdaterLinkChangesWithAttribute(t *testing.T) {
	r := newRig(t, rigOpts{})
	collector, other, updater := newAgentID(), newAgentID(), newAgentID()
	u := r.connect("", updater, capsUpdater)
	u.send(updaterFirst(updater, collector, []byte{}))
	u.mustRecv()
	// the collector registers: its updater is derived
	c := r.connect("", collector, capsCollector)
	c.send(firstMessage(collector, descOpts{host: "host1", version: "0.162.0"}))
	c.mustRecv()
	if ag := r.agent(collector); ag.Updater == nil || ag.Updater.AgentID != updater || !ag.Updater.Online {
		t.Fatalf("collector -> updater: %+v", ag.Updater)
	}
	// the supervisor got a new instance uid: the updater re-links
	u.send(&protobufs.AgentToServer{AgentDescription: description(updater, descOpts{host: "host1", version: "0.1.0", service: domain.UpdaterServiceName,
		extra: []*protobufs.KeyValue{kv(domain.AttrUpdaterCollectorUID, other.String())}})})
	u.mustRecv()
	if ag := r.agent(updater); ag.Updater == nil || ag.Updater.AgentID != other {
		t.Fatalf("relink: %+v", ag.Updater)
	}
	// an invalid uid is ignored
	u.send(&protobufs.AgentToServer{AgentDescription: description(updater, descOpts{host: "host1", version: "0.1.0", service: domain.UpdaterServiceName,
		extra: []*protobufs.KeyValue{kv(domain.AttrUpdaterCollectorUID, "not-a-uuid")}})})
	if m := u.mustRecv(); m.ErrorResponse != nil {
		t.Fatal("invalid link attribute must not break the connection")
	}
	if ag := r.agent(updater); ag.Updater == nil || ag.Updater.AgentID != other {
		t.Fatal("invalid link keeps the previous one")
	}
}
