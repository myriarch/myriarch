// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
)

// ------------------------------------------------------------------ download ----

// download streams a URL into a temporary file and returns its SHA-256.
func (c *Cache) download(ctx context.Context, rawURL string) (tmp string, sum []byte, size int64, err error) {
	// a bounded number of downloads per replica
	select {
	case c.dlSem <- struct{}{}:
		defer func() { <-c.dlSem }()
	case <-ctx.Done():
		return "", nil, 0, ctx.Err()
	}
	// idle deadline: a download that stops delivering data is aborted
	ctx, cancelIdle := context.WithCancel(ctx)
	defer cancelIdle()
	idleFor := downloadIdleTimeout
	if idleTimeoutOverride > 0 {
		idleFor = idleTimeoutOverride
	}
	idle := time.AfterFunc(idleFor, cancelIdle)
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, 0, fmt.Errorf("request %s: %w", redactURL(rawURL), err)
	}
	resp, err := c.large.Do(req)
	if err != nil {
		return "", nil, 0, fmt.Errorf("GET %s: %w", redactURL(rawURL), cleanURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", nil, 0, c.statusError(rawURL, resp.StatusCode)
	}
	f, err := os.CreateTemp(filepath.Join(c.dir, ".tmp"), "dl-*")
	if err != nil {
		return "", nil, 0, fmt.Errorf("create temporary file: %w", err)
	}
	cleanup := func() { f.Close(); os.Remove(f.Name()) }
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(&idleReader{r: resp.Body, timer: idle, d: idleFor}, maxArchiveBytes+1))
	if err != nil {
		cleanup()
		return "", nil, 0, fmt.Errorf("download %s: %w", redactURL(rawURL), err)
	}
	if n > maxArchiveBytes {
		cleanup()
		return "", nil, 0, fmt.Errorf("%s is larger than %d bytes", redactURL(rawURL), int64(maxArchiveBytes))
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		cleanup()
		return "", nil, 0, fmt.Errorf("download %s: truncated (%d of %d bytes)", redactURL(rawURL), n, resp.ContentLength)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", nil, 0, fmt.Errorf("sync temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, 0, fmt.Errorf("close temporary file: %w", err)
	}
	return f.Name(), h.Sum(nil), n, nil
}

// idleReader restarts the idle timer whenever data arrives.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(i.d)
	}
	return n, err
}

// ------------------------------------------------------------------- tokens ----

type tokenClaims struct {
	Artifact uuid.UUID      `json:"a"`
	Agent    domain.AgentID `json:"g"`
	Issued   int64          `json:"t"`
}

func (c *Cache) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, c.deps.DownloadSecret)
	m.Write(payload)
	return m.Sum(nil)
}

// signToken builds "myrd_<b64url(json)>.<b64url(HMAC-SHA256(secret, json))>".
func (c *Cache) signToken(artifact uuid.UUID, agent domain.AgentID, at time.Time) (string, error) {
	if len(c.deps.DownloadSecret) == 0 {
		return "", errors.New("artifact download secret is not configured")
	}
	payload, err := json.Marshal(tokenClaims{Artifact: artifact, Agent: agent, Issued: at.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return domain.DownloadTokenPrefix + enc.EncodeToString(payload) + "." + enc.EncodeToString(c.mac(payload)), nil
}

var errBadToken = errors.New("invalid download token")

func (c *Cache) parseToken(tok string) (tokenClaims, error) {
	var cl tokenClaims
	if len(c.deps.DownloadSecret) == 0 || !strings.HasPrefix(tok, domain.DownloadTokenPrefix) {
		return cl, errBadToken
	}
	body := strings.TrimPrefix(tok, domain.DownloadTokenPrefix)
	p, m, ok := strings.Cut(body, ".")
	if !ok {
		return cl, errBadToken
	}
	enc := base64.RawURLEncoding
	payload, err1 := enc.DecodeString(p)
	mac, err2 := enc.DecodeString(m)
	if err1 != nil || err2 != nil || !hmac.Equal(mac, c.mac(payload)) {
		return cl, errBadToken
	}
	if err := json.Unmarshal(payload, &cl); err != nil {
		return cl, errBadToken
	}
	return cl, nil
}

// DownloadHeaders mints the Authorization header for a download URL built by
// DownloadURL (artifact id taken from the URL). Package offers store only the
// URL; the token is created when the offer is sent, so it is always fresh.
func (c *Cache) DownloadHeaders(downloadURL string, agentID domain.AgentID) (map[string]string, error) {
	const marker = "/artifacts/"
	i := strings.Index(downloadURL, marker)
	if i < 0 {
		return nil, errors.New("not an artifact download URL")
	}
	idStr, _, _ := strings.Cut(downloadURL[i+len(marker):], "/")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, errors.New("not an artifact download URL")
	}
	tok, err := c.signToken(id, agentID, c.now())
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

// DownloadURL returns the absolute URL
// <publicUrl>/api/v1/artifacts/<id>/content/<filename> and the
// "Authorization: Bearer myrd_..." header for agentID. The token binds
// artifact id + agent id (HMAC); ServeContent accepts it only while the agent
// has a package offer referencing the artifact (and, if
// DownloadTokenTTL > 0, while it is younger than the TTL).
//
// DownloadURL always uses server.publicUrl; DownloadURLFor picks the base per
// updater (artifacts.agentBaseUrls).
func (c *Cache) DownloadURL(a *domain.Artifact, agentID domain.AgentID) (string, map[string]string, error) {
	return c.downloadURL(a, agentID, c.deps.PublicURL)
}

// DownloadURLFor is DownloadURL for an updater agent: the base URL is the
// artifacts.agentBaseUrls entry whose host:port equals the one of the
// updater's reported dev.myriarch.updater.server_endpoint attribute (the OpAMP
// endpoint it is connected to), else server.publicUrl. The token is minted
// for updater.ID.
func (c *Cache) DownloadURLFor(a *domain.Artifact, updater *domain.Agent) (string, map[string]string, error) {
	if updater == nil {
		return "", nil, errors.New("download URL: no updater given")
	}
	return c.downloadURL(a, updater.ID, c.baseFor(updater))
}

func (c *Cache) downloadURL(a *domain.Artifact, agentID domain.AgentID, base string) (string, map[string]string, error) {
	if base == "" {
		return "", nil, errors.New("server.publicUrl is not configured: cannot build download URLs")
	}
	tok, err := c.signToken(a.ID, agentID, c.now())
	if err != nil {
		return "", nil, err
	}
	u := strings.TrimRight(base, "/") + api.BasePath + "/artifacts/" + a.ID.String() + "/content/" + url.PathEscape(a.Filename)
	return u, map[string]string{"Authorization": "Bearer " + tok}, nil
}

// ------------------------------------------------------------------- serving ----

func (c *Cache) result(res string) { c.downloads.WithLabelValues(res).Inc() }

// ServeContent serves the artifact bytes (operation downloadArtifact,
// api.AuthCustom): verifies the download token, fetches the file into the
// local cache if this replica does not have it yet, supports Range.
func (c *Cache) ServeContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		c.result("not_found")
		api.WriteError(w, r, fmt.Errorf("%w: unknown artifact", domain.ErrNotFound))
		return
	}
	unauthorized := func(msg string) {
		c.result("unauthorized")
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+brand.ArtifactsAuthRealm+`"`)
		api.WriteError(w, r, fmt.Errorf("%w: %s", domain.ErrUnauthorized, msg))
	}

	hdr := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(hdr, "Bearer ")
	if !ok {
		unauthorized("a download token is required")
		return
	}
	cl, err := c.parseToken(strings.TrimSpace(tok))
	if err != nil || cl.Artifact != id {
		unauthorized("invalid download token")
		return
	}
	if ttl := c.cfg.DownloadTokenTTL; ttl > 0 && c.now().Sub(time.Unix(cl.Issued, 0)) > ttl {
		unauthorized("download token expired")
		return
	}
	// The token is only valid while the agent has a current offer for this artifact.
	offers, err := c.deps.Store.Packages().GetOffers(ctx, cl.Agent)
	if err != nil {
		c.result("error")
		api.WriteError(w, r, err)
		return
	}
	marker := "/artifacts/" + id.String() + "/content/"
	offered := false
	for _, o := range offers {
		if strings.Contains(o.DownloadURL, marker) {
			offered = true
			break
		}
	}
	if !offered {
		unauthorized("the agent has no package offer for this artifact")
		return
	}

	rec, err := c.deps.Store.Artifacts().Get(ctx, id)
	if err != nil || rec.Status != domain.ArtifactReady || r.PathValue("filename") != rec.Filename {
		c.result("not_found")
		api.WriteError(w, r, fmt.Errorf("%w: artifact not available", domain.ErrNotFound))
		return
	}
	// Only verified artifacts are served: a pinned SHA-256 and, when
	// signatures are required, a verified sigstore bundle.
	if len(rec.SHA256) != 32 || (c.cfg.Verify.RequireSignature && !rec.SignatureVerified) {
		c.result("not_found")
		c.log.Warn("artifact is not verified, refusing to serve it", "artifact_id", id)
		api.WriteError(w, r, fmt.Errorf("%w: artifact not available", domain.ErrNotFound))
		return
	}
	path := c.path(rec)
	if err := c.ensureLocal(ctx, rec, path); err != nil {
		c.result("error")
		c.log.Error("artifact not available locally", "artifact_id", id, "error", err)
		api.WriteError(w, r, fmt.Errorf("%w: artifact could not be fetched from the source: %v", domain.ErrUnavailable, err))
		return
	}
	f, err := openNoFollow(path)
	if err != nil {
		c.result("error")
		api.WriteError(w, r, fmt.Errorf("%w: open artifact: %v", domain.ErrUnavailable, err))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		c.result("error")
		api.WriteError(w, r, fmt.Errorf("%w: stat artifact: %v", domain.ErrUnavailable, err))
		return
	}
	c.touch(path)

	ct := "application/octet-stream"
	if rec.Format == "tar.gz" {
		ct = "application/gzip"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+rec.Filename+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"sha256-`+rec.SHA256.String()+`"`)
	c.result("ok")
	http.ServeContent(w, r, rec.Filename, st.ModTime(), f)
}

// fileStamp identifies the state of a file whose content hash was verified.
type fileStamp struct {
	size  int64
	mtime int64
	sum   string
}

// verifyLocal checks that the file at path has the pinned SHA-256. The result
// is remembered per process for the same (size, mtime), so a file is hashed
// once and again whenever it changes on disk.
func (c *Cache) verifyLocal(path string, rec *domain.Artifact) error {
	f, err := openNoFollow(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != rec.Size {
		return fmt.Errorf("%s is not the expected file", filepath.Base(path))
	}
	stamp := fileStamp{size: st.Size(), mtime: st.ModTime().UnixNano(), sum: rec.SHA256.String()}
	c.verifiedMu.Lock()
	known := c.verified[path] == stamp
	c.verifiedMu.Unlock()
	if known {
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !equalBytes(h.Sum(nil), rec.SHA256) {
		return fmt.Errorf("cached file %s does not match the pinned SHA-256", filepath.Base(path))
	}
	c.verifiedMu.Lock()
	if len(c.verified) > 1024 {
		c.verified = map[string]fileStamp{}
	}
	c.verified[path] = stamp
	c.verifiedMu.Unlock()
	return nil
}

// ensureLocal makes sure this replica has the file: it fetches it from the
// source if missing or modified and verifies it against the pinned SHA-256
// (upstream artifacts are mutable: the stored hash wins).
func (c *Cache) ensureLocal(ctx context.Context, rec *domain.Artifact, path string) error {
	if err := c.verifyLocal(path, rec); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		c.log.Warn("cached artifact file failed verification, fetching it again", "artifact_id", rec.ID, "error", err)
		_ = os.Remove(path)
	}
	ch := c.fetchGroup.DoChan(rec.ID.String(), func() (any, error) {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ensureTimeout)
		defer cancel()
		// Re-check inside the singleflight.
		if err := c.verifyLocal(path, rec); err == nil {
			return nil, nil
		}
		// The URL is derived from the CURRENT source configuration, not from
		// the stored record: a changed or tampered record cannot redirect fetches.
		src, err := newSpec(rec.Distribution, rec.Version, rec.OS, rec.Arch)
		if err != nil {
			return nil, err
		}
		tmp, sum, size, err := c.download(wctx, c.sourceURL(src, ""))
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp)
		if !equalBytes(sum, rec.SHA256) || size != rec.Size {
			c.log.Error("upstream artifact changed: the pinned SHA-256 is kept and the file is rejected",
				"artifact_id", rec.ID, "pinned", rec.SHA256.String(), "upstream", fmt.Sprintf("%x", sum), "source", redactURL(rec.SourceURL))
			return nil, fmt.Errorf("upstream artifact changed (SHA-256 %x, pinned %s)", sum, rec.SHA256.Short())
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		if err := os.Chmod(tmp, 0o640); err != nil {
			return nil, err
		}
		return nil, os.Rename(tmp, path)
	})
	select {
	case r := <-ch:
		return r.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// touch refreshes the modification time (LRU eviction order) at most every
// few minutes per file.
func (c *Cache) touch(path string) {
	now := time.Now()
	c.touchMu.Lock()
	last := c.lastTouch[path]
	if now.Sub(last) < touchInterval {
		c.touchMu.Unlock()
		return
	}
	c.lastTouch[path] = now
	c.touchMu.Unlock()
	_ = os.Chtimes(path, now, now)
}
