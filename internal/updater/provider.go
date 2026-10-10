// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// offer is what the server announced for one download URL.
type offer struct {
	name    string
	version string
}

// offerBook remembers the offered package versions: opamp-go passes only the
// download URL (not the version) to UpdateContent.
type offerBook struct {
	mu    sync.Mutex
	byURL map[string]offer
}

func (b *offerBook) record(pa *protobufs.PackagesAvailable) {
	m := map[string]offer{}
	for name, p := range pa.GetPackages() {
		if f := p.GetFile(); f != nil {
			m[f.GetDownloadUrl()] = offer{name: name, version: p.GetVersion()}
		}
	}
	b.mu.Lock()
	b.byURL = m
	b.mu.Unlock()
}

func (b *offerBook) lookup(url string) (offer, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.byURL[url]
	return o, ok
}

// installEvents are callbacks for the OpAMP layer (health reporting).
type installEvents struct {
	started  func(version string)
	finished func(version string, err error)
}

// packagesProvider implements types.PackagesStateProvider for myriarch-updater.
// Only the collector package (name "") is supported.
type packagesProvider struct {
	st      *stateStore
	layout  *Layout
	inst    *Installer
	offers  *offerBook
	log     *slog.Logger
	now     func() time.Time
	events  installEvents
	backoff time.Duration
}

var _ types.PackagesStateProvider = (*packagesProvider)(nil)

func (p *packagesProvider) AllPackagesHash() ([]byte, error) {
	var h []byte
	p.st.read(func(st *stateFile) { h = append([]byte(nil), st.AllPackagesHash...) })
	return h, nil
}

func (p *packagesProvider) SetAllPackagesHash(hash []byte) error {
	return p.st.update(func(st *stateFile) { st.AllPackagesHash = append([]byte(nil), hash...) })
}

func (p *packagesProvider) Packages() ([]string, error) {
	var names []string
	p.st.read(func(st *stateFile) {
		for n := range st.Packages {
			names = append(names, n)
		}
	})
	return names, nil
}

func (p *packagesProvider) PackageState(name string) (types.PackageState, error) {
	var ps types.PackageState
	p.st.read(func(st *stateFile) {
		if r, ok := st.Packages[name]; ok {
			ps = types.PackageState{Exists: true, Type: protobufs.PackageType(r.Type), Hash: append([]byte(nil), r.Hash...), Version: r.Version}
		}
	})
	return ps, nil
}

func (p *packagesProvider) SetPackageState(name string, s types.PackageState) error {
	return p.st.update(func(st *stateFile) {
		st.Packages[name] = packageRecord{Type: int32(s.Type), Hash: append([]byte(nil), s.Hash...), Version: s.Version}
	})
}

// CreatePackage rejects everything except the collector package so that
// unsupported offers fail fast with INSTALL_FAILED, before any download.
func (p *packagesProvider) CreatePackage(name string, typ protobufs.PackageType) error {
	if name != "" {
		return fmt.Errorf("unsupported package %q: myriarch-updater installs only the collector package (empty name)", name)
	}
	if typ != protobufs.PackageType_PackageType_TopLevel {
		return fmt.Errorf("unsupported package type %s for the collector package", typ)
	}
	return p.st.update(func(st *stateFile) {
		if _, ok := st.Packages[name]; !ok {
			st.Packages[name] = packageRecord{Type: int32(typ)}
		}
	})
}

// FileContentHash returns the SHA-256 of the verified archive of the active
// version. If the offer carries the same content hash the syncer skips the
// download and reports INSTALLED: the content is already installed and active.
func (p *packagesProvider) FileContentHash(name string) ([]byte, error) {
	if name != "" {
		return nil, nil
	}
	return p.layout.ActiveContentHash(), nil
}

func (p *packagesProvider) UpdateContent(ctx context.Context, name, downloadURL string, data io.Reader, contentHash, signature []byte) error {
	if name != "" {
		return fmt.Errorf("unsupported package %q", name)
	}
	o, ok := p.offers.lookup(downloadURL)
	if !ok || o.version == "" {
		return fmt.Errorf("no package offer with a version is known for %s", downloadURL)
	}
	if p.events.started != nil {
		p.events.started(o.version)
	}
	err := p.inst.Install(ctx, InstallRequest{
		Version:     o.version,
		DownloadURL: downloadURL,
		Data:        data,
		ContentHash: contentHash,
		Signature:   signature,
	})
	if p.events.finished != nil {
		p.events.finished(o.version, err)
	}
	if err != nil {
		p.log.Error("package install failed", "version", o.version, "err", err)
		if rerr := p.st.recordFailure(contentHash, o.version, err.Error(), p.now()); rerr != nil {
			p.log.Warn("persist failure record", "err", rerr)
		}
		return err
	}
	if cerr := p.st.clearFailure(contentHash); cerr != nil {
		p.log.Warn("clear failure record", "err", cerr)
	}
	return nil
}

// DeletePackage forgets the package state. It deliberately does NOT remove
// anything from disk: offers are complete sets, and a withdrawn offer must
// never uninstall the running collector.
func (p *packagesProvider) DeletePackage(name string) error {
	return p.st.update(func(st *stateFile) { delete(st.Packages, name) })
}

func (p *packagesProvider) LastReportedStatuses() (*protobufs.PackageStatuses, error) {
	return p.st.lastStatuses()
}

func (p *packagesProvider) SetLastReportedStatuses(s *protobufs.PackageStatuses) error {
	return p.st.setLastStatuses(s)
}

// downloadGuard returns an error if an install of this content failed
// recently: reconnecting updaters would otherwise stop and restart the
// supervisor on every re-offer of a broken package.
func (p *packagesProvider) downloadGuard(contentHash []byte) error {
	f, ok := p.st.failure(contentHash)
	if !ok || p.backoff <= 0 {
		return nil
	}
	if wait := p.backoff - p.now().Sub(f.At); wait > 0 {
		return fmt.Errorf("install of v%s failed %s ago (%s); not retrying for another %s",
			f.Version, p.now().Sub(f.At).Round(time.Second), f.Error, wait.Round(time.Second))
	}
	return nil
}

// markInterruptedStatuses turns statuses of installs that were cut off by a
// restart into INSTALL_FAILED so the server does not see them stuck forever.
func (p *packagesProvider) markInterruptedStatuses() {
	ps, _ := p.st.lastStatuses()
	if ps == nil {
		return
	}
	changed := false
	for _, s := range ps.Packages {
		switch s.Status {
		case protobufs.PackageStatusEnum_PackageStatusEnum_Installing,
			protobufs.PackageStatusEnum_PackageStatusEnum_Downloading,
			protobufs.PackageStatusEnum_PackageStatusEnum_InstallPending:
			s.Status = protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed
			s.ErrorMessage = "myriarch-updater restarted while the package was being installed"
			s.DownloadDetails = nil
			changed = true
		}
	}
	if changed {
		// Force a re-sync of the offer on the next PackagesAvailable.
		_ = p.st.update(func(st *stateFile) { st.AllPackagesHash = nil })
		_ = p.st.setLastStatuses(ps)
	}
}
