// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/version"
)

// Limits of the downloads of the release source. checksums.txt and its bundle are a few kilobytes and an archive of
// the updater is about ten megabytes; the limits leave room and stop a source that sends more.
const (
	maxChecksumsBytes = 1 << 20
	maxSigstoreBytes  = 1 << 20
	maxBundleBytes    = 256 << 20
	// fetchSmallTimeout bounds a request for a small document; an archive has no overall timeout but the idle one.
	fetchSmallTimeout = 2 * time.Minute
	// fetchIdleTimeout aborts a download that delivers no data for this long.
	fetchIdleTimeout = 60 * time.Second
)

// fetcher downloads from the release source of the installer bundles (install.bundleSourceBaseUrl) under the rules
// of every outbound connection of the server: the destination policy of the HTTP client (every resolved address is
// checked), the proxy of artifacts.proxyUrl, redirects to https and to the allowed hosts only, a size limit and an idle
// timeout. One download runs at a time. The source gets no credential: it is a public release, or a mirror that is
// readable without one.
type fetcher struct {
	small, large *http.Client
	tmpDir       string
	sem          chan struct{}
	userAgent    string
}

type fetchDeps struct {
	// HTTPClient is the client with the destination policy (nil: the default client).
	HTTPClient *http.Client
	// Policy carries the destination policy for a proxy (nil: none).
	Policy *netguard.Policy
	// ProxyURL is artifacts.proxyUrl.
	ProxyURL string
	// AllowedRedirectHosts is artifacts.allowedRedirectHosts.
	AllowedRedirectHosts []string
	// BaseURL is install.bundleSourceBaseUrl.
	BaseURL string
	// TmpDir receives the downloads before they are checked.
	TmpDir string
}

func newFetcher(d fetchDeps) (*fetcher, error) {
	base := d.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	small, large := *base, *base
	small.Timeout = fetchSmallTimeout
	large.Timeout = 0
	if d.ProxyURL != "" {
		pu, err := url.Parse(d.ProxyURL)
		if err != nil || pu.Host == "" {
			return nil, fmt.Errorf("artifacts.proxyUrl %q is invalid", redact(d.ProxyURL))
		}
		var tr *http.Transport
		if d.Policy != nil {
			tr = d.Policy.TransportWithProxy(pu)
		} else {
			tr = http.DefaultTransport.(*http.Transport).Clone()
			tr.Proxy = http.ProxyURL(pu)
		}
		small.Transport, large.Transport = tr, tr
	}
	redirect := artifact.RedirectPolicy(d.AllowedRedirectHosts, d.BaseURL)
	small.CheckRedirect, large.CheckRedirect = redirect, redirect
	return &fetcher{small: &small, large: &large, tmpDir: d.TmpDir, sem: make(chan struct{}, 1), userAgent: version.UserAgent(brand.ServerBinary)}, nil
}

// get downloads a small document of at most max bytes.
func (f *fetcher) get(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchSmallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", redact(rawURL), err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	resp, err := f.small.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", redact(rawURL), unwrapURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GET %s: HTTP status %d", redact(rawURL), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", redact(rawURL), err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", redact(rawURL), max)
	}
	return body, nil
}

// download streams a file of at most max bytes into a temporary file and returns its path, SHA-256 and size. The
// caller removes the file.
func (f *fetcher) download(ctx context.Context, rawURL string, max int64) (path string, sum [sha256.Size]byte, size int64, err error) {
	select {
	case f.sem <- struct{}{}:
		defer func() { <-f.sem }()
	case <-ctx.Done():
		return "", sum, 0, ctx.Err()
	}
	// idle deadline: a download that stops delivering data is aborted
	ctx, cancelIdle := context.WithCancel(ctx)
	defer cancelIdle()
	idle := time.AfterFunc(fetchIdleTimeout, cancelIdle)
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", sum, 0, fmt.Errorf("request %s: %w", redact(rawURL), err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	resp, err := f.large.Do(req)
	if err != nil {
		return "", sum, 0, fmt.Errorf("GET %s: %w", redact(rawURL), unwrapURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", sum, 0, fmt.Errorf("GET %s: HTTP status %d", redact(rawURL), resp.StatusCode)
	}
	if resp.ContentLength > max {
		return "", sum, 0, fmt.Errorf("%s is larger than %d bytes", redact(rawURL), max)
	}
	if err := os.MkdirAll(f.tmpDir, 0o750); err != nil {
		return "", sum, 0, fmt.Errorf("create the download directory: %w", err)
	}
	file, err := os.CreateTemp(f.tmpDir, "dl-*")
	if err != nil {
		return "", sum, 0, fmt.Errorf("create a temporary file: %w", err)
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, h), io.LimitReader(&idleReader{r: resp.Body, timer: idle}, max+1))
	if err != nil {
		cleanup()
		return "", sum, 0, fmt.Errorf("download %s: %w", redact(rawURL), err)
	}
	if n > max {
		cleanup()
		return "", sum, 0, fmt.Errorf("%s is larger than %d bytes", redact(rawURL), max)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		cleanup()
		return "", sum, 0, fmt.Errorf("download %s: truncated (%d of %d bytes)", redact(rawURL), n, resp.ContentLength)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return "", sum, 0, fmt.Errorf("sync the temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return "", sum, 0, fmt.Errorf("close the temporary file: %w", err)
	}
	copy(sum[:], h.Sum(nil))
	return filepath.Clean(file.Name()), sum, n, nil
}

// idleReader restarts the idle timer whenever data arrives.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(fetchIdleTimeout)
	}
	return n, err
}

// redact shortens a URL to scheme, host and path, so that credentials or a query never reach a message.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid URL>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// unwrapURLError drops the URL that net/http puts into its errors (it may carry credentials in the user info).
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
