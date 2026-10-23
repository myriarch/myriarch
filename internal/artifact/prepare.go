// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"errors"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// How Prepare treats a record that is not ready, by its last change (the database sets updated_at on every write).
const (
	// A record that is downloading and changed less than this long ago belongs to a download that another replica
	// (or the prefetch of the leader) is running; older ones belong to a replica that is gone.
	prepareStaleAfter = 10 * time.Minute
	// A failed attempt is not repeated before this time has passed, so that a host that asks again every few seconds
	// cannot make the server request an upstream release in a loop.
	prepareRetryAfter = time.Minute
)

// Readiness is the state of an artifact for a caller that cannot wait for a download.
type Readiness int

const (
	// PrepareReady: the artifact is stored with a pinned SHA-256 and, where signatures are required, a verified bundle.
	PrepareReady Readiness = iota
	// PreparePending: a download is under way, on this replica or on another one, or was started by this call.
	PreparePending
	// PrepareFailed: the last attempt failed and the next one is not due yet.
	PrepareFailed
)

// Prepare returns the artifact for a platform when it is ready, and otherwise makes sure that it becomes ready
// without making the caller wait: it starts the download in the background (Ensure, with the checks of the
// cache: checksum, Sigstore bundle, outbound policy, size limit and at most three downloads at a time) and
// reports PreparePending. The install plan answers a host with 503 and Retry-After until the artifact is ready.
//
// A download that this replica runs is started once, however many callers ask. A record that another replica
// is downloading (status downloading, changed within ten minutes) is not started again; an older one belongs to a
// replica that is gone and is. A failed record is tried again after a minute; until then the state is
// PrepareFailed. The error is that of an invalid platform or version (domain.ErrInvalidArgument) or of the store.
func (c *Cache) Prepare(ctx context.Context, distribution, version, os, arch string) (*domain.Artifact, Readiness, error) {
	s, err := newSpec(distribution, version, os, arch)
	if err != nil {
		return nil, PrepareFailed, err
	}
	rec, err := c.deps.Store.Artifacts().Find(ctx, s.dist, s.version, s.os, s.arch, s.format)
	switch {
	case err == nil:
		if c.servable(rec) {
			return rec, PrepareReady, nil
		}
	case !errors.Is(err, domain.ErrNotFound):
		return nil, PrepareFailed, err
	default:
		rec = nil
	}

	key := s.key()
	c.prepMu.Lock()
	defer c.prepMu.Unlock()
	if _, running := c.preparing[key]; running {
		return nil, PreparePending, nil
	}
	if rec != nil {
		age := c.now().Sub(rec.UpdatedAt)
		switch rec.Status {
		case domain.ArtifactDownloading, domain.ArtifactPending:
			if age < prepareStaleAfter {
				return nil, PreparePending, nil
			}
		case domain.ArtifactFailed:
			if age < prepareRetryAfter {
				return nil, PrepareFailed, nil
			}
		}
	}
	if c.preparing == nil {
		c.preparing = map[string]struct{}{}
	}
	c.preparing[key] = struct{}{}
	c.prepWG.Add(1)
	go func() {
		defer c.prepWG.Done()
		defer func() {
			c.prepMu.Lock()
			delete(c.preparing, key)
			c.prepMu.Unlock()
		}()
		bctx, cancel := context.WithTimeout(context.Background(), ensureTimeout)
		defer cancel()
		// Ensure logs the failure and records it with the artifact
		_, _ = c.Ensure(bctx, s.dist, s.version, s.os, s.arch)
	}()
	return nil, PreparePending, nil
}

// WaitPrepared waits until the downloads that Prepare and StartPrefetch started have finished, or until ctx is done. Tests use it
// before they remove the directory of the cache.
func (c *Cache) WaitPrepared(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.prepWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
