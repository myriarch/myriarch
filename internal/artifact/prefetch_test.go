// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

func prefetchFleet(name, dist string) domain.Fleet {
	return domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: name}, FleetSpec: domain.FleetSpec{
		Collector: domain.CollectorSpec{Distribution: dist}, Update: domain.UpdateSpec{Executor: domain.ExecutorUpdater}}}
}

func (f *fixture) putPrefetchAgent(fleet string, kind domain.AgentKind, osType, arch string, status domain.ConnectionStatus) {
	f.st.PutAgent(domain.Agent{
		ID: uuid.Must(uuid.NewV7()), Kind: kind, OSType: osType, Arch: arch,
		Connection:               domain.AgentConnection{Status: status},
		Assignment:               domain.AgentAssignment{FleetName: fleet},
		IdentifyingAttributes:    map[string]string{},
		NonIdentifyingAttributes: map[string]string{},
		Labels:                   map[string]string{},
	})
}

func prefetchedPlatforms(list []domain.Artifact) []string {
	var out []string
	for _, a := range list {
		out = append(out, a.OS+"/"+a.Arch)
	}
	return out
}

// Without any configuration nothing is prefetched by default.
func TestPrefetchPlatformsDefaultIsEmpty(t *testing.T) {
	assert.Empty(t, config.Default().Artifacts.PrefetchPlatforms)
}

func TestStartPrefetchUsesConfiguredPlatforms(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.PrefetchPlatforms = []string{"linux/amd64"} })
	ctx := context.Background()
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(200), []byte("b"))
	_, err := f.st.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "c", Fleets: []domain.Fleet{prefetchFleet("edge", "")}}, domain.GitOpsSync{})
	require.NoError(t, err)
	// connected collectors on another platform must not matter when platforms are configured
	f.putPrefetchAgent("edge", domain.AgentKindCollector, "windows", "amd64", domain.ConnectionOnline)

	list, err := f.cache.StartPrefetch(ctx, domain.DistributionContrib, "0.162.0", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"linux/amd64"}, prefetchedPlatforms(list))
}

func TestStartPrefetchDerivesPlatformsFromConnectedCollectors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i, p := range [][2]string{{"linux", "arm64"}, {"windows", "amd64"}, {"linux", "amd64"}, {"darwin", "arm64"}} {
		b := fakeArchive(200)
		b[0] = byte(i + 1)
		f.up.release("0.162.0", p[0], p[1], b, []byte("b"))
	}
	_, err := f.st.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "c", Fleets: []domain.Fleet{
		prefetchFleet("default-dist", ""),
		prefetchFleet("contrib", domain.DistributionContrib),
		prefetchFleet("supervisor", domain.DistributionSupervisor),
	}}, domain.GitOpsSync{})
	require.NoError(t, err)
	on, off := domain.ConnectionOnline, domain.ConnectionOffline
	f.putPrefetchAgent("default-dist", domain.AgentKindCollector, "linux", "arm64", on)
	f.putPrefetchAgent("default-dist", domain.AgentKindCollector, "linux", "arm64", on) // duplicate platform
	f.putPrefetchAgent("contrib", domain.AgentKindCollector, "windows", "amd64", on)
	f.putPrefetchAgent("contrib", domain.AgentKindCollector, "linux", "amd64", off)    // not connected
	f.putPrefetchAgent("contrib", domain.AgentKindUpdater, "linux", "amd64", on)       // not a collector
	f.putPrefetchAgent("supervisor", domain.AgentKindCollector, "darwin", "arm64", on) // other distribution

	list, plan, err := f.cache.StartPrefetchResolved(ctx, domain.DistributionContrib, "0.162.0", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"linux/arm64", "windows/amd64"}, prefetchedPlatforms(list))
	assert.Equal(t, PrefetchPlan{Platforms: []string{"linux/arm64", "windows/amd64"}, Source: PlatformsFromConnected}, plan)
	require.Eventually(t, func() bool {
		l, _, _ := f.st.Artifacts().List(ctx, store.ArtifactFilter{Status: []domain.ArtifactStatus{domain.ArtifactReady}})
		return len(l) == 2
	}, 5*time.Second, 10*time.Millisecond)

	// explicit platforms in the request win over the derivation
	explicit, err := f.cache.StartPrefetch(ctx, domain.DistributionContrib, "0.162.0", []string{"linux/amd64"})
	require.NoError(t, err)
	assert.Equal(t, []string{"linux/amd64"}, prefetchedPlatforms(explicit))
	// wait for the background download so it does not outlive the test's temp directory
	require.Eventually(t, func() bool {
		l, _, _ := f.st.Artifacts().List(ctx, store.ArtifactFilter{Status: []domain.ArtifactStatus{domain.ArtifactReady}})
		return len(l) == 3
	}, 5*time.Second, 10*time.Millisecond)
}

func TestStartPrefetchWithoutAnyPlatformFails(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "c", Fleets: []domain.Fleet{
		prefetchFleet("edge", ""), prefetchFleet("other", domain.DistributionSupervisor),
	}}, domain.GitOpsSync{})
	require.NoError(t, err)
	// only an offline collector and a collector of another distribution
	f.putPrefetchAgent("edge", domain.AgentKindCollector, "linux", "amd64", domain.ConnectionOffline)
	f.putPrefetchAgent("other", domain.AgentKindCollector, "linux", "amd64", domain.ConnectionOnline)

	_, err = f.cache.StartPrefetch(ctx, domain.DistributionContrib, "0.162.0", nil)
	require.ErrorIs(t, err, domain.ErrInvalidArgument)
	assert.Contains(t, err.Error(), "no platforms given, none configured (artifacts.prefetchPlatforms) and no connected collectors use this distribution")
}

// WaitPrepared must also wait for the background downloads of StartPrefetch, otherwise they outlive the temp directory.
func TestWaitPreparedWaitsForPrefetchDownloads(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.PrefetchPlatforms = []string{"linux/amd64"} })
	ctx := context.Background()
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(200), []byte("b"))
	release := f.up.block()
	t.Cleanup(release)

	_, err := f.cache.StartPrefetch(ctx, domain.DistributionContrib, "0.162.0", nil)
	require.NoError(t, err)

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, f.cache.WaitPrepared(short), context.DeadlineExceeded, "the download is still blocked")

	release()
	long, cancelLong := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLong()
	require.NoError(t, f.cache.WaitPrepared(long))
	l, _, err := f.st.Artifacts().List(ctx, store.ArtifactFilter{Status: []domain.ArtifactStatus{domain.ArtifactReady}})
	require.NoError(t, err)
	assert.Len(t, l, 1)
}
