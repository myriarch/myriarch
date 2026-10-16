// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/store"
)

// RunPrefetch (leader only) prefetches artifacts for all fleet target
// versions x cfg.PrefetchPlatforms and evicts by cfg.MaxCacheBytes.
func (c *Cache) RunPrefetch(ctx context.Context) error {
	// A moment after startup so that the replica is serving before it downloads.
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(15 * time.Second):
	}
	for {
		if err := c.PrefetchOnce(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("artifact prefetch finished with errors", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(prefetchInterval):
		}
	}
}

// platform is an (os, arch) pair.
type platform struct{ os, arch string }

func parsePlatforms(list []string) ([]platform, error) {
	var out []platform
	for _, p := range list {
		if err := CheckPlatform(p); err != nil {
			return nil, err
		}
		o, a, _ := strings.Cut(strings.TrimSpace(p), "/")
		out = append(out, platform{NormalizeOS(o), NormalizeArch(a)})
	}
	return out, nil
}

// CheckPlatform validates one entry of artifacts.prefetchPlatforms: "os/arch"
// with an operating system and an architecture that release assets exist for
// (the rules of newSpec).
func CheckPlatform(p string) error {
	o, a, ok := strings.Cut(strings.TrimSpace(p), "/")
	if !ok || o == "" || a == "" {
		return fmt.Errorf("%w: platform %q must be \"os/arch\"", domain.ErrInvalidArgument, p)
	}
	if !validOS[NormalizeOS(o)] {
		return fmt.Errorf("%w: platform %q: unsupported operating system %q (linux, windows, darwin, aix)", domain.ErrInvalidArgument, p, o)
	}
	if !archRE.MatchString(NormalizeArch(a)) {
		return fmt.Errorf("%w: platform %q: invalid architecture %q", domain.ErrInvalidArgument, p, a)
	}
	return nil
}

// target is a (distribution, version) a fleet wants.
type target struct{ dist, version string }

// PrefetchOnce downloads the artifacts of all fleets that update through the
// updater executor (platforms of their members plus cfg.PrefetchPlatforms)
// and evicts old files afterwards.
func (c *Cache) PrefetchOnce(ctx context.Context) error {
	defer metrics.CycleTimer(c.deps.Registerer)("artifact_prefetch")()
	fleets, err := c.deps.Store.GitOps().ListFleets(ctx)
	if err != nil {
		return fmt.Errorf("list fleets: %w", err)
	}
	configured, err := parsePlatforms(c.cfg.PrefetchPlatforms)
	if err != nil {
		return err
	}
	selections, err := c.deps.Store.FleetVersions().List(ctx)
	if err != nil {
		return fmt.Errorf("list version selections: %w", err)
	}
	selByFleet := make(map[string]*domain.FleetVersionSelection, len(selections))
	for i := range selections {
		selByFleet[selections[i].Fleet] = &selections[i]
	}
	protected := map[string]bool{}
	var errs []error
	for _, f := range fleets {
		dist := f.Collector.Distribution
		if dist == "" {
			dist = domain.DistributionContrib
		}
		if git := domain.NormalizeVersion(f.Collector.Version); git != "" {
			protected[dist+"/"+git] = true // the version a cleared selection falls back to
		}
		// the effective target: an active UI selection, else the Git target
		ver := domain.ResolveVersionTarget(&f, selByFleet[f.Name]).Version
		if ver == "" {
			continue
		}
		protected[dist+"/"+ver] = true
		if f.Update.Executor != domain.ExecutorUpdater {
			continue
		}
		plats := map[platform]bool{}
		for _, p := range configured {
			plats[p] = true
		}
		members, err := c.deps.Store.Agents().ListAll(ctx, store.AgentFilter{Fleets: []string{f.Name}, Kinds: []domain.AgentKind{domain.AgentKindCollector}})
		if err != nil {
			errs = append(errs, fmt.Errorf("list members of %s: %w", f.Name, err))
			continue
		}
		for _, m := range members {
			if m.OSType != "" && m.Arch != "" {
				plats[platform{NormalizeOS(m.OSType), NormalizeArch(m.Arch)}] = true
			}
		}
		for p := range plats {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := c.Ensure(ctx, dist, ver, p.os, p.arch); err != nil {
				errs = append(errs, fmt.Errorf("%s %s %s/%s: %w", dist, ver, p.os, p.arch, err))
			}
		}
	}
	if err := c.Evict(ctx, protected); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// StartPrefetch begins downloading (distribution, version) for the platforms
// in the background and returns the current artifact states immediately
// (POST /artifacts/actions/prefetch). Without platforms in the request the
// configured artifacts.prefetchPlatforms apply; when none are configured
// either, the distinct os/arch of the connected collectors of the fleets that
// use this distribution are fetched.
func (c *Cache) StartPrefetch(ctx context.Context, dist, version string, platforms []string) ([]domain.Artifact, error) {
	list, _, err := c.StartPrefetchResolved(ctx, dist, version, platforms)
	return list, err
}

// Where the platforms of a prefetch came from (PrefetchPlan.Source).
const (
	PlatformsFromRequest   = "request"
	PlatformsFromConfig    = "config"
	PlatformsFromConnected = "connected"
)

// PrefetchPlan reports which platforms a prefetch resolved to ("os/arch",
// in resolved order) and where they came from, for the audit trail.
type PrefetchPlan struct {
	Platforms []string
	Source    string
}

// StartPrefetchResolved behaves like StartPrefetch and additionally returns
// the platforms that were actually fetched and their source. The plan is
// returned (possibly with an empty platform list) even when the call fails
// after the platforms were resolved.
func (c *Cache) StartPrefetchResolved(ctx context.Context, dist, version string, platforms []string) ([]domain.Artifact, PrefetchPlan, error) {
	var plats []platform
	var err error
	plan := PrefetchPlan{Source: PlatformsFromRequest}
	if len(platforms) == 0 {
		platforms = c.cfg.PrefetchPlatforms
		plan.Source = PlatformsFromConfig
	}
	if len(platforms) > 0 {
		if plats, err = parsePlatforms(platforms); err != nil {
			return nil, plan, err
		}
	} else {
		plan.Source = PlatformsFromConnected
		if plats, err = c.connectedPlatforms(ctx, dist); err != nil {
			return nil, plan, err
		}
	}
	for _, p := range plats {
		plan.Platforms = append(plan.Platforms, p.os+"/"+p.arch)
	}
	if len(plats) == 0 {
		return nil, plan, fmt.Errorf("%w: no platforms given, none configured (artifacts.prefetchPlatforms) and no connected collectors use this distribution", domain.ErrInvalidArgument)
	}
	specs := make([]spec, 0, len(plats))
	for _, p := range plats {
		s, err := newSpec(dist, version, p.os, p.arch)
		if err != nil {
			return nil, plan, err
		}
		specs = append(specs, s)
	}
	out := make([]domain.Artifact, 0, len(specs))
	for _, s := range specs {
		rec, err := c.deps.Store.Artifacts().Find(ctx, s.dist, s.version, s.os, s.arch, s.format)
		if err != nil || rec.Status != domain.ArtifactReady {
			// Show the pending download right away.
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return nil, plan, err
			}
			pending := domain.Artifact{
				ID: uuid.Must(uuid.NewV7()), Distribution: s.dist, Version: s.version, OS: s.os, Arch: s.arch, Format: s.format,
				Filename: s.filename, SourceURL: c.sourceURL(s, ""), Status: domain.ArtifactDownloading, CreatedAt: c.now(),
			}
			rec, err = c.deps.Store.Artifacts().Upsert(ctx, pending)
			if err != nil {
				return nil, plan, fmt.Errorf("record artifact: %w", err)
			}
			go func() {
				bctx, cancel := context.WithTimeout(context.Background(), ensureTimeout)
				defer cancel()
				if _, err := c.Ensure(bctx, s.dist, s.version, s.os, s.arch); err != nil {
					c.log.Warn("prefetch failed", "distribution", s.dist, "version", s.version, "os", s.os, "arch", s.arch, "error", err)
				}
			}()
		}
		out = append(out, *rec)
	}
	return out, plan, nil
}

// connectedPlatforms returns the distinct os/arch pairs (sorted) of the
// connected collector agents that belong to a fleet whose collector
// distribution equals dist (an empty fleet distribution means the default
// contrib distribution).
func (c *Cache) connectedPlatforms(ctx context.Context, dist string) ([]platform, error) {
	fleets, err := c.deps.Store.GitOps().ListFleets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list fleets: %w", err)
	}
	var names []string
	for _, f := range fleets {
		d := f.Collector.Distribution
		if d == "" {
			d = domain.DistributionContrib
		}
		if d == dist {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	agents, err := c.deps.Store.Agents().ListAll(ctx, store.AgentFilter{
		Fleets: names, Kinds: []domain.AgentKind{domain.AgentKindCollector},
		Status: []domain.ConnectionStatus{domain.ConnectionOnline},
	})
	if err != nil {
		return nil, fmt.Errorf("list connected collectors: %w", err)
	}
	seen := map[platform]bool{}
	var out []platform
	for _, a := range agents {
		if a.OSType == "" || a.Arch == "" {
			continue
		}
		p := platform{NormalizeOS(a.OSType), NormalizeArch(a.Arch)}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].os != out[j].os {
			return out[i].os < out[j].os
		}
		return out[i].arch < out[j].arch
	})
	return out, nil
}

// Delete removes an artifact from the database and from the disk of every
// replica (this replica directly, the others through the bus event).
func (c *Cache) Delete(ctx context.Context, id uuid.UUID) error {
	rec, err := c.deps.Store.Artifacts().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := c.deps.Store.Artifacts().Delete(ctx, id); err != nil {
		return err
	}
	c.removeLocal(rec)
	if c.deps.Bus != nil {
		p := eventbus.ArtifactChangedPayload{ArtifactID: id, Status: domain.ArtifactDeleted, SHA256: rec.SHA256.String()}
		if err := c.deps.Bus.Publish(ctx, eventbus.ArtifactChanged, p); err != nil {
			c.log.Warn("publish artifact deletion failed (other replicas keep their copy until evicted)", "artifact_id", id, "error", err)
		}
	}
	return nil
}

func (c *Cache) removeLocal(rec *domain.Artifact) {
	if err := os.Remove(c.path(rec)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Warn("remove artifact file failed", "artifact_id", rec.ID, "error", err)
	}
}

// Run (every replica) applies artifact deletions made on other replicas:
// the content addressed file is removed from the local disk.
func (c *Cache) Run(ctx context.Context) error {
	if c.deps.Bus == nil {
		<-ctx.Done()
		return nil
	}
	ch, cancel := c.deps.Bus.Subscribe(eventbus.ArtifactChanged)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			var p eventbus.ArtifactChangedPayload
			if err := ev.Decode(&p); err != nil || p.Status != domain.ArtifactDeleted || len(p.SHA256) != 64 {
				continue
			}
			sum, err := hex.DecodeString(p.SHA256)
			if err != nil {
				continue
			}
			if err := os.Remove(c.hashPath(sum)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				c.log.Warn("remove deleted artifact file failed", "artifact_id", p.ArtifactID, "error", err)
			}
		}
	}
}

// Evict removes least recently used files until the cache fits into
// cfg.MaxCacheBytes. Files of protected "<distribution>/<version>" pairs
// (target versions of fleets) are never removed. Database records stay: a
// missing file is fetched again on demand.
func (c *Cache) Evict(ctx context.Context, protected map[string]bool) error {
	type file struct {
		path  string
		size  int64
		mtime time.Time
		key   string
	}
	var files []file
	var total int64
	// content addressed files belong to "<distribution>/<version>" of their record
	owner := map[string]string{}
	if recs, _, err := c.deps.Store.Artifacts().List(ctx, store.ArtifactFilter{Limit: 100000}); err == nil {
		for _, r := range recs {
			if len(r.SHA256) == 32 {
				owner[c.hashPath(r.SHA256)] = r.Distribution + "/" + r.Version
			}
		}
	}
	err := filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// the TUF cache and the installer bundles (internal/install keeps and tidies them itself) are not artifacts
			if d.Name() == ".tmp" || d.Name() == "tuf" || p == filepath.Join(c.dir, InstallDir) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(c.dir, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		key := ""
		if len(parts) >= 3 {
			key = parts[0] + "/" + parts[1]
		}
		if k, ok := owner[p]; ok {
			key = k
		}
		files = append(files, file{p, info.Size(), info.ModTime(), key})
		total += info.Size()
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan artifact cache: %w", err)
	}
	if total <= c.cfg.MaxCacheBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if total <= c.cfg.MaxCacheBytes {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if protected[f.key] {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		total -= f.size
		c.log.Info("artifact evicted", "file", filepath.Base(f.path), "size", f.size)
	}
	if total > c.cfg.MaxCacheBytes {
		c.log.Warn("artifact cache exceeds its limit: only protected target versions remain", "bytes", total, "limit", c.cfg.MaxCacheBytes)
	}
	return nil
}
