// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"bytes"
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

// There are two kinds of download credentials, both "myrd_<b64url(json)>.<b64url(HMAC-SHA256(secret, json))>"
// with the signature over the JSON as sent:
//
//   - the token of a package offer, bound to one artifact and one updater agent ({"a","g","t"}, no kind);
//     ServeContent accepts it only while that agent has an offer for the artifact;
//   - the install grant, bound to one artifact and one enrollment token ({"a","t","k":"i","e"}), which a host
//     gets from its install plan before it is enrolled; ServeContent accepts it only while that enrollment
//     token is valid.
//
// The kind is part of the signed payload, so neither credential can be turned into the other. An unknown kind
// is refused.
const grantKindInstall = "i"

// updaterClaims is the payload of the token of a package offer.
type updaterClaims struct {
	Artifact uuid.UUID      `json:"a"`
	Agent    domain.AgentID `json:"g"`
	Issued   int64          `json:"t"`
}

// installClaims is the payload of an install grant.
type installClaims struct {
	Artifact uuid.UUID `json:"a"`
	Issued   int64     `json:"t"`
	Kind     string    `json:"k"`
	Token    uuid.UUID `json:"e"`
}

// tokenClaims is the payload of either kind, as parsed.
type tokenClaims struct {
	Artifact uuid.UUID      `json:"a"`
	Agent    domain.AgentID `json:"g"`
	Issued   int64          `json:"t"`
	// Kind is "" for the token of a package offer and grantKindInstall for an install grant.
	Kind string `json:"k"`
	// Token is the ID of the enrollment token of an install grant.
	Token uuid.UUID `json:"e"`
}

func (c *Cache) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, c.deps.DownloadSecret)
	m.Write(payload)
	return m.Sum(nil)
}

// sign builds "myrd_<b64url(json)>.<b64url(HMAC-SHA256(secret, json))>".
func (c *Cache) sign(claims any) (string, error) {
	if len(c.deps.DownloadSecret) == 0 {
		return "", errors.New("artifact download secret is not configured")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return domain.DownloadTokenPrefix + enc.EncodeToString(payload) + "." + enc.EncodeToString(c.mac(payload)), nil
}

// signToken builds the token of a package offer.
func (c *Cache) signToken(artifact uuid.UUID, agent domain.AgentID, at time.Time) (string, error) {
	return c.sign(updaterClaims{Artifact: artifact, Agent: agent, Issued: at.Unix()})
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
	u, err := contentURL(a, base)
	if err != nil {
		return "", nil, err
	}
	tok, err := c.signToken(a.ID, agentID, c.now())
	if err != nil {
		return "", nil, err
	}
	return u, map[string]string{"Authorization": "Bearer " + tok}, nil
}

// contentURL is <base>/api/v1/artifacts/<id>/content/<filename>.
func contentURL(a *domain.Artifact, base string) (string, error) {
	if base == "" {
		return "", errors.New("server.publicUrl is not configured: cannot build download URLs")
	}
	return strings.TrimRight(base, "/") + api.BasePath + "/artifacts/" + a.ID.String() + "/content/" + url.PathEscape(a.Filename), nil
}

// InstallURL returns the absolute URL <publicUrl>/api/v1/artifacts/<id>/content/<filename> of an artifact, for
// a host that downloads with an install grant (InstallGrant). The checksum and the Sigstore bundle of the
// artifact are served under the same URL with the suffixes ".sha256" and ".sigstore.json".
func (c *Cache) InstallURL(a *domain.Artifact) (string, error) {
	return contentURL(a, c.deps.PublicURL)
}

// InstallGrant mints the credential with which a host that is not enrolled yet downloads an artifact
// (ADR-0046): a "myrd_" token bound to the artifact and to the ID of the enrollment token that the host
// presented to get its plan. ServeContent accepts it for the artifact, its ".sha256" and its ".sigstore.json"
// until it is older than Deps.InstallGrantTTL, and only while the enrollment token is neither revoked nor
// expired and Deps.InstallEnabled is on. It is never accepted as the token of a package offer. A grant is issued
// only for an artifact that is ready, has a pinned SHA-256 and, where signatures are required, a verified bundle.
func (c *Cache) InstallGrant(a *domain.Artifact, enrollmentToken uuid.UUID) (string, error) {
	if !c.deps.InstallEnabled {
		return "", errors.New("install grant: the installation from the server is turned off (install.enabled)")
	}
	if a == nil || !c.servable(a) {
		return "", errors.New("install grant: the artifact is not ready and verified")
	}
	if enrollmentToken == uuid.Nil {
		return "", errors.New("install grant: no enrollment token")
	}
	return c.sign(installClaims{Artifact: a.ID, Issued: c.now().Unix(), Kind: grantKindInstall, Token: enrollmentToken})
}

// servable reports whether an artifact may be served: ready, with a pinned SHA-256 and, when signatures are
// required, a verified bundle.
func (c *Cache) servable(a *domain.Artifact) bool {
	return a.Status == domain.ArtifactReady && len(a.SHA256) == sha256.Size && (!c.cfg.Verify.RequireSignature || a.SignatureVerified)
}

// ------------------------------------------------------------------- serving ----

func (c *Cache) result(res string) { c.downloads.WithLabelValues(res).Inc() }

// ServeContent serves the artifact bytes (operation downloadArtifact,
// api.AuthCustom): verifies the download credential, fetches the file into the
// local cache if this replica does not have it yet, supports Range.
//
// Two credentials work (see the comment on grantKindInstall). The token of a package offer is accepted only
// while its agent has an offer for the artifact and serves the artifact itself. An install grant is accepted
// only while the installation from the server is on (Deps.InstallEnabled) and its enrollment token is valid, and
// serves the artifact and, from the record, its checksum (<filename>.sha256) and its Sigstore bundle
// (<filename>.sigstore.json), the files that the installer checks the artifact with. Everything that is served is
// verified: ready, with a pinned SHA-256 and, where signatures are required, a verified bundle.
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
	var install bool
	switch cl.Kind {
	case "": // the token of a package offer
		if msg, err := c.authorizeOffer(ctx, cl, id); err != nil {
			c.result("error")
			api.WriteError(w, r, err)
			return
		} else if msg != "" {
			unauthorized(msg)
			return
		}
	case grantKindInstall:
		install = true
		if !c.deps.InstallEnabled {
			// the feature that issues the credential is off: a grant minted before is no credential any more, and its
			// lifetime and its enrollment token do not matter (install.grantTtl is not checked while the feature is off)
			unauthorized("invalid install grant")
			return
		}
		if msg, err := c.authorizeInstall(ctx, cl); err != nil {
			c.result("error")
			api.WriteError(w, r, err)
			return
		} else if msg != "" {
			unauthorized(msg)
			return
		}
	default:
		unauthorized("invalid download token")
		return
	}

	rec, err := c.deps.Store.Artifacts().Get(ctx, id)
	if err != nil || rec.Status != domain.ArtifactReady {
		c.result("not_found")
		api.WriteError(w, r, fmt.Errorf("%w: artifact not available", domain.ErrNotFound))
		return
	}
	// Only verified artifacts are served: a pinned SHA-256 and, when
	// signatures are required, a verified sigstore bundle.
	if !c.servable(rec) {
		c.result("not_found")
		c.log.Warn("artifact is not verified, refusing to serve it", "artifact_id", id)
		api.WriteError(w, r, fmt.Errorf("%w: artifact not available", domain.ErrNotFound))
		return
	}
	switch name := r.PathValue("filename"); {
	case name == rec.Filename:
	case install && name == rec.Filename+checksumSuffix:
		c.serveRecordFile(w, r, rec, name, "text/plain; charset=utf-8", []byte(rec.SHA256.String()+"  "+rec.Filename+"\n"))
		return
	case install && name == rec.Filename+bundleSuffix && len(rec.SigstoreBundle) > 0:
		c.serveRecordFile(w, r, rec, name, "application/json", rec.SigstoreBundle)
		return
	default:
		c.result("not_found")
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
	if install && r.Header.Get("Range") == "" {
		// only the ID of the enrollment token is logged, never a credential
		c.log.Info("artifact served with an install grant", "artifact_id", id, "token_id", cl.Token)
	}
	http.ServeContent(w, r, rec.Filename, st.ModTime(), f)
}

// Suffixes of the files that an install grant gets next to the artifact.
const (
	checksumSuffix = ".sha256"
	bundleSuffix   = ".sigstore.json"
)

// authorizeOffer checks the token of a package offer: it is valid only while the agent has an offer for the
// artifact and, if artifacts.downloadTokenTtl is set, while it is younger than that. It returns the reason of a
// refusal (the answer is 401) or an error of the store.
func (c *Cache) authorizeOffer(ctx context.Context, cl tokenClaims, id uuid.UUID) (string, error) {
	if ttl := c.cfg.DownloadTokenTTL; ttl > 0 && c.now().Sub(time.Unix(cl.Issued, 0)) > ttl {
		return "download token expired", nil
	}
	// The token is only valid while the agent has a current offer for this artifact.
	offers, err := c.deps.Store.Packages().GetOffers(ctx, cl.Agent)
	if err != nil {
		return "", err
	}
	marker := "/artifacts/" + id.String() + "/content/"
	for _, o := range offers {
		if strings.Contains(o.DownloadURL, marker) {
			return "", nil
		}
	}
	return "the agent has no package offer for this artifact", nil
}

// authorizeInstall checks an install grant: it is valid while it is younger than Deps.InstallGrantTTL (0: no
// expiry) and only while the enrollment token it is bound to exists and is neither revoked nor expired. The
// token is read from the store for every request, so a revocation takes effect at once on every replica. The
// reasons are the same for a token that is gone and for one that is revoked or expired.
func (c *Cache) authorizeInstall(ctx context.Context, cl tokenClaims) (string, error) {
	if cl.Token == uuid.Nil {
		return "invalid download token", nil
	}
	now := c.now()
	if ttl := c.deps.InstallGrantTTL; ttl > 0 && now.Sub(time.Unix(cl.Issued, 0)) > ttl {
		return "install grant expired", nil
	}
	tok, err := c.deps.Store.Identity().GetEnrollmentToken(ctx, cl.Token)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "invalid install grant", nil
	case err != nil:
		return "", fmt.Errorf("%w: enrollment token lookup: %v", domain.ErrUnavailable, err)
	case tok.RevokedAt != nil || (tok.ExpiresAt != nil && !now.Before(*tok.ExpiresAt)):
		return "invalid install grant", nil
	}
	return "", nil
}

// serveRecordFile serves a small file that comes from the record of the artifact (checksum, Sigstore bundle).
func (c *Cache) serveRecordFile(w http.ResponseWriter, r *http.Request, rec *domain.Artifact, name, contentType string, content []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	c.result("ok")
	http.ServeContent(w, r, name, rec.UpdatedAt, bytes.NewReader(content))
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
