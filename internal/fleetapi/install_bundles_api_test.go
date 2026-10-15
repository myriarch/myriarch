// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/install"
)

// ------------------------------------------------------------------- fixtures ----

var bundlePlatforms = []install.Platform{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}, {OS: "windows", Arch: "amd64"}}

// apiBundles are installer bundles of the release 0.2.0 whose source is a local server. The archives are small; the
// verifier accepts the signature (the check of the real signature is in internal/install).
type apiBundles struct {
	e        *apiEnv
	b        *install.Bundles
	cacheDir string
	src      *httptest.Server
	archives map[string][]byte
	verifier *okVerifier
	gate     chan struct{} // closed: the source answers; open: it waits
	mu       sync.Mutex
	hits     int
}

type okVerifier struct {
	mu  sync.Mutex
	err error
}

func (v *okVerifier) Verify(_, _ []byte, id artifact.Identity) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return "", v.err
	}
	return "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/" + id.Tag, nil
}

func (v *okVerifier) fail(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

// withBundles gives the API of the test installer bundles. With hold, the source waits until release is called.
func (e *apiEnv) withBundles(t *testing.T, version string, hold bool) *apiBundles {
	t.Helper()
	ab := &apiBundles{e: e, archives: map[string][]byte{}, verifier: &okVerifier{}, gate: make(chan struct{})}
	if !hold {
		close(ab.gate)
	}
	var sums []string
	for _, p := range bundlePlatforms {
		name := install.BundleFilename("0.2.0", p)
		ab.archives[name] = bytes.Repeat([]byte(name+"|"), 100)
		sum := sha256.Sum256(ab.archives[name])
		sums = append(sums, hex.EncodeToString(sum[:])+"  "+name)
	}
	checksums := []byte(strings.Join(sums, "\n") + "\n")
	ab.src = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-ab.gate
		ab.mu.Lock()
		ab.hits++
		ab.mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/v0.2.0/")
		switch {
		case name == "checksums.txt":
			_, _ = w.Write(checksums)
		case name == "checksums.txt.sigstore.json":
			_, _ = w.Write([]byte(`{"fake":"bundle"}`))
		case ab.archives[name] != nil:
			_, _ = w.Write(ab.archives[name])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ab.src.Close)
	ab.cacheDir = t.TempDir()
	b, err := install.NewBundles(install.BundleDeps{Version: version, Dir: ab.cacheDir, BaseURL: ab.src.URL, Platforms: bundlePlatforms,
		Verifier: ab.verifier, HTTPClient: ab.src.Client(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	ab.b = b
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ab.release()
		_ = b.Wait(ctx)
	})
	e.useBundles(t, b, false)
	return ab
}

// useBundles remounts the routes on a planner that has the bundles.
func (e *apiEnv) useBundles(t *testing.T, bundles install.BundleStore, dev bool, mut ...func(d *install.Deps)) {
	t.Helper()
	d := install.Deps{Store: e.st, Assigner: e.assigner, Artifacts: e.cache, Config: e.cfg.Install, PublicURL: e.cfg.Server.PublicURL, OpAMPPath: e.cfg.OpAMP.Path,
		Logger: slog.New(slog.NewTextHandler(e.logs, nil)), Bundles: bundles, DevMode: dev}
	for _, m := range mut {
		m(&d)
	}
	p, err := install.NewPlanner(d)
	require.NoError(t, err)
	e.planner = p
	e.remount(func(deps *Deps) { deps.Install = p })
}

func (ab *apiBundles) release() {
	select {
	case <-ab.gate:
	default:
		close(ab.gate)
	}
}

func (ab *apiBundles) ready(t *testing.T) {
	t.Helper()
	ab.release()
	ab.b.Prepare()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, ab.b.Wait(ctx))
	for _, it := range ab.b.State().Items {
		require.Equal(t, install.BundleReady, it.Status, it.Error)
	}
}

func (e *apiEnv) bundleCounter(result string) float64 {
	e.t.Helper()
	mfs, err := e.reg.Gather()
	require.NoError(e.t, err)
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_install_bundle_downloads_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func (e *apiEnv) listBundles(role string) resp {
	return e.do(http.MethodGet, "/install/bundles", role, nil)
}

func (e *apiEnv) renderCommands(role string, body any) resp {
	return e.do(http.MethodPost, "/install/commands", role, body)
}

func (e *apiEnv) downloadBundle(token, name string, hdr ...string) resp {
	h := append([]string{}, hdr...)
	if token != "" {
		h = append(h, "Authorization", "Bearer "+token)
	}
	return e.do(http.MethodGet, "/install/bundles/"+name, "", nil, h...)
}

func linuxBundleName() string {
	return install.BundleFilename("0.2.0", install.Platform{OS: "linux", Arch: "amd64"})
}

func edgeSite(mut ...func(b map[string]any)) map[string]any {
	b := map[string]any{"attributes": map[string]string{"site.type": "edge", "site.id": "042"}}
	for _, m := range mut {
		m(b)
	}
	return b
}

// ------------------------------------------------------------------------ list ----

func TestListInstallBundles(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", true)

	v := asMap(t, e.expect(e.listBundles("viewer"), "listInstallBundles", http.StatusOK))
	assert.Equal(t, "release", v["source"])
	assert.Equal(t, "0.2.0", v["version"])
	assert.NotContains(t, v, "reason")
	items := asList(t, v)
	require.Len(t, items, 3)
	for _, it := range items {
		m := asMap(t, it)
		assert.Equal(t, "pending", m["status"], "nothing is fetched before somebody asks")
		assert.NotContains(t, m, "sha256")
	}

	// reading the list starts nothing
	ab.mu.Lock()
	assert.Zero(t, ab.hits)
	ab.mu.Unlock()

	ab.ready(t)
	v = asMap(t, e.expect(e.listBundles("viewer"), "listInstallBundles", http.StatusOK))
	for i, it := range asList(t, v) {
		m := asMap(t, it)
		name := install.BundleFilename("0.2.0", bundlePlatforms[i])
		assert.Equal(t, "ready", m["status"])
		assert.Equal(t, name, m["filename"])
		assert.Equal(t, bundlePlatforms[i].OS, m["os"])
		assert.Equal(t, bundlePlatforms[i].Arch, m["arch"])
		assert.Equal(t, sumOf(ab.archives[name]), m["sha256"])
		assert.EqualValues(t, len(ab.archives[name]), m["size"])
		assert.NotContains(t, m, "error")
	}
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestListInstallBundlesShowsAFailure(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.verifier.fail(errors.New("sigstore verification failed: no matching CertificateIdentity"))
	ab.b.Prepare()
	require.NoError(t, ab.b.Wait(context.Background()))
	v := asMap(t, e.expect(e.listBundles("viewer"), "listInstallBundles", http.StatusOK))
	for _, it := range asList(t, v) {
		m := asMap(t, it)
		assert.Equal(t, "failed", m["status"])
		assert.Contains(t, m["error"], "not signed by the release workflow of this project")
		assert.NotContains(t, m, "sha256")
	}
}

func TestListInstallBundlesOfALocalBuild(t *testing.T) {
	e := newAPIEnv(t)
	e.withBundles(t, "0.0.0-dev", false)
	v := asMap(t, e.expect(e.listBundles("viewer"), "listInstallBundles", http.StatusOK))
	assert.Equal(t, "none", v["source"])
	assert.Equal(t, "local_build", v["reason"])
	assert.NotContains(t, v, "version")
	assert.Empty(t, asList(t, v))
}

func TestListInstallBundlesNeedsAPrincipalAndTheFeature(t *testing.T) {
	e := newAPIEnv(t)
	e.withBundles(t, "0.2.0", false)
	e.expect(e.listBundles(""), "listInstallBundles", http.StatusUnauthorized)
	for _, role := range []string{"viewer", "operator", "admin"} {
		e.expect(e.listBundles(role), "listInstallBundles", http.StatusOK)
	}

	off := e.cfg.Install
	off.Enabled = false
	for name, mut := range map[string]func(d *install.Deps){
		"install.enabled off": func(d *install.Deps) { d.Config = off },
		"no bundles":          func(d *install.Deps) { d.Bundles = nil },
	} {
		t.Run(name, func(t *testing.T) {
			e.useBundles(t, nil, false, func(d *install.Deps) { d.Bundles = e.planner.Bundles(); mut(d) })
			r := e.listBundles("viewer")
			e.expect(r, "listInstallBundles", http.StatusNotFound)
			assert.Equal(t, "not_found", problemOf(t, r).Code)
		})
	}
}

// -------------------------------------------------------------------- commands ----

func TestRenderInstallCommands(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)

	r := e.renderCommands("viewer", edgeSite(func(b map[string]any) { b["fleet"] = "linux-hosts" }))
	v := asMap(t, e.expect(r, "renderInstallCommands", http.StatusOK))
	assert.Equal(t, "no-store", r.Header().Get("Cache-Control"))
	assert.Equal(t, "token_inline", v["variant"])
	assert.Equal(t, "https://fleet.example.com", v["server"])
	assert.Equal(t, "0.2.0", v["version"])
	assert.Equal(t, false, v["insecure"])
	assert.Equal(t, brand.EnrollmentTokenPlaceholder, v["tokenPlaceholder"])
	cmds := v["commands"].([]any)
	require.Len(t, cmds, 3)
	for i, c := range cmds {
		m := asMap(t, c)
		name := install.BundleFilename("0.2.0", bundlePlatforms[i])
		assert.Equal(t, bundlePlatforms[i].OS+"/"+bundlePlatforms[i].Arch, m["platform"])
		cmd := m["command"].(string)
		assert.Equal(t, 1, strings.Count(cmd, brand.EnrollmentTokenPlaceholder))
		assert.Contains(t, cmd, "'"+sumOf(ab.archives[name])+"'", "the command pins the hash of the bundle")
		assert.Contains(t, cmd, "'"+name+"'")
		assert.Equal(t, "ready", asMap(t, m["bundle"])["status"])
		for _, prefix := range []string{"myre_", "myra_", "myrd_"} {
			assert.NotContains(t, cmd, prefix)
		}
	}
	assert.Equal(t, "bash", asMap(t, cmds[0])["shell"])
	assert.Equal(t, "powershell", asMap(t, cmds[2])["shell"])
	// the log names who rendered it, and holds no command
	assert.Contains(t, e.logs.String(), "install commands rendered")
	assert.NotContains(t, e.logs.String(), "sudo sh -c")

	// a viewer may render; nobody without a session
	e.expect(e.renderCommands("", edgeSite()), "renderInstallCommands", http.StatusUnauthorized)
	for _, role := range []string{"viewer", "operator", "admin"} {
		e.expect(e.renderCommands(role, edgeSite()), "renderInstallCommands", http.StatusOK)
	}
}

func TestRenderInstallCommandsTokenFileVariant(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	r := e.renderCommands("admin", edgeSite(func(b map[string]any) {
		b["variant"] = "token_file"
		b["platforms"] = []string{"linux/amd64", "windows/amd64"}
		b["tokenFiles"] = map[string]string{"linux": "/etc/myriarch/enrollment-token", "windows": `C:\Secrets\token.txt`}
		b["caFile"] = "/etc/ssl/myriarch-ca.pem"
	}))
	v := asMap(t, e.expect(r, "renderInstallCommands", http.StatusOK))
	assert.NotContains(t, v, "tokenPlaceholder")
	cmds := v["commands"].([]any)
	require.Len(t, cmds, 2)
	assert.Equal(t, "/etc/myriarch/enrollment-token", asMap(t, cmds[0])["tokenFile"])
	assert.Equal(t, `C:\Secrets\token.txt`, asMap(t, cmds[1])["tokenFile"])
	assert.NotContains(t, r.Body.String(), brand.EnrollmentTokenPlaceholder)
}

func TestRenderInstallCommandsLabIsForDevelopmentServersOnly(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	body := edgeSite(func(b map[string]any) { b["variant"] = "lab" })
	r := e.renderCommands("admin", body)
	doc := asMap(t, e.expect(r, "renderInstallCommands", http.StatusUnprocessableEntity))
	assert.Equal(t, "validation_failed", doc["code"])
	assert.Equal(t, "variant_not_available", asMap(t, doc["issues"].([]any)[0])["code"])

	// a development server renders it, over http
	e.cfg.Server.PublicURL = "http://localhost:8080"
	e.useBundles(t, ab.b, true, func(d *install.Deps) { d.PublicURL = "http://localhost:8080" })
	r = e.renderCommands("admin", body)
	v := asMap(t, e.expect(r, "renderInstallCommands", http.StatusOK))
	assert.Equal(t, true, v["insecure"])
	assert.Equal(t, "http://localhost:8080", v["server"])
}

func TestRenderInstallCommandsRefusesWhatItShould(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)

	// 400: not a request
	for name, body := range map[string]any{
		"not json": "{", "empty": "", "unknown field": `{"attributes":{"site.type":"edge","site.id":"1"},"token":"x"}`,
		"a number": `{"variant":1}`, "too large": `{"fleet":"` + strings.Repeat("a", 20<<10) + `"}`,
	} {
		r := e.renderCommands("admin", body)
		require.Equal(t, http.StatusBadRequest, r.Code, name+": "+r.Body.String())
		e.expect(r, "renderInstallCommands", http.StatusBadRequest)
	}

	// 422: with the issues, and no command at all
	r := e.renderCommands("admin", map[string]any{
		"variant": "x", "platforms": []string{"plan9/mips"}, "fleet": "Bad Fleet",
		"attributes": map[string]string{"site.type": "a'b", "site.id": "$(id)", "nope": "x"},
	})
	doc := asMap(t, e.expect(r, "renderInstallCommands", http.StatusUnprocessableEntity))
	assert.Equal(t, "validation_failed", doc["code"])
	paths := map[string]string{}
	for _, is := range doc["issues"].([]any) {
		m := asMap(t, is)
		paths[m["path"].(string)] = m["code"].(string)
	}
	assert.Equal(t, map[string]string{
		"variant": "invalid_value", "platforms[0]": "platform_not_supported", "fleet": "invalid_value", "attributes.site.type": "invalid_value",
		"attributes.site.id": "invalid_value", "attributes.nope": "unknown_attribute",
	}, paths)
	assert.NotContains(t, r.Body.String(), "$(id)", "the answer does not repeat what was refused")
	assert.NotContains(t, r.Body.String(), "sudo sh -c")

	// a token in the request is refused wherever it is
	r = e.renderCommands("admin", edgeSite(func(b map[string]any) {
		b["attributes"] = map[string]string{"site.type": "edge", "site.id": "1", "site.name": "myre_" + strings.Repeat("a", 32)}
	}))
	e.expect(r, "renderInstallCommands", http.StatusUnprocessableEntity)
	assert.NotContains(t, r.Body.String(), "myre_")
	assert.NotContains(t, e.logs.String(), "myre_")
}

func TestRenderInstallCommandsOfALocalBuild(t *testing.T) {
	e := newAPIEnv(t)
	e.withBundles(t, "0.0.0-dev", false)
	r := e.renderCommands("admin", edgeSite())
	doc := asMap(t, e.expect(r, "renderInstallCommands", http.StatusPreconditionFailed))
	assert.Equal(t, "precondition_failed", doc["code"])
	assert.Equal(t, "local_build", asMap(t, doc["issues"].([]any)[0])["code"])
}

func TestRenderInstallCommandsNeedsAnHTTPSServer(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	e.useBundles(t, ab.b, false, func(d *install.Deps) { d.PublicURL = "http://fleet.example.com" })
	r := e.renderCommands("admin", edgeSite())
	doc := asMap(t, e.expect(r, "renderInstallCommands", http.StatusPreconditionFailed))
	assert.Equal(t, "server_settings", asMap(t, doc["issues"].([]any)[0])["code"])
}

func TestRenderInstallCommandsWaitsForTheBundles(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", true)

	r := e.renderCommands("admin", edgeSite())
	doc := asMap(t, e.expect(r, "renderInstallCommands", http.StatusServiceUnavailable))
	assert.Equal(t, "unavailable", doc["code"])
	assert.Equal(t, "bundle_preparing", asMap(t, doc["issues"].([]any)[0])["code"])
	assert.Equal(t, "5", r.Header().Get("Retry-After"))

	ab.ready(t)
	e.expect(e.renderCommands("admin", edgeSite()), "renderInstallCommands", http.StatusOK)

	// a bundle that failed says so, with the cause, and the server tries again later
	e2 := newAPIEnv(t)
	ab2 := e2.withBundles(t, "0.2.0", false)
	ab2.verifier.fail(errors.New("no good"))
	r = e2.renderCommands("admin", edgeSite())
	require.Equal(t, http.StatusServiceUnavailable, r.Code)
	require.NoError(t, ab2.b.Wait(context.Background()))
	r = e2.renderCommands("admin", edgeSite())
	doc = asMap(t, e2.expect(r, "renderInstallCommands", http.StatusServiceUnavailable))
	assert.Equal(t, "bundle_failed", asMap(t, doc["issues"].([]any)[0])["code"])
	assert.Equal(t, "30", r.Header().Get("Retry-After"))
	assert.Contains(t, doc["detail"], "no good")
}

func TestRenderInstallCommandsIsOffWhenTheInstallationIsOff(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	off := e.cfg.Install
	off.Enabled = false
	e.useBundles(t, ab.b, false, func(d *install.Deps) { d.Config = off })
	r := e.renderCommands("admin", edgeSite())
	e.expect(r, "renderInstallCommands", http.StatusNotFound)
}

// -------------------------------------------------------------------- download ----

func TestDownloadInstallBundle(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, id := e.auth.newToken(nil)

	for i, p := range bundlePlatforms {
		name := install.BundleFilename("0.2.0", p)
		r := e.downloadBundle(secret, name)
		require.Equal(t, http.StatusOK, r.Code, r.Body.String())
		assert.Equal(t, ab.archives[name], r.Body.Bytes(), "the bytes of the release")
		wantType := "application/gzip"
		if p.OS == "windows" {
			wantType = "application/zip"
		}
		assert.Equal(t, wantType, r.Header().Get("Content-Type"), i)
		assert.Equal(t, `attachment; filename="`+name+`"`, r.Header().Get("Content-Disposition"))
		assert.Equal(t, `"sha256-`+sumOf(ab.archives[name])+`"`, r.Header().Get("ETag"))
		assert.Equal(t, "private, no-store", r.Header().Get("Cache-Control"))
		assert.Equal(t, "nosniff", r.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "bytes", r.Header().Get("Accept-Ranges"))
		assert.Equal(t, fmt.Sprint(len(ab.archives[name])), r.Header().Get("Content-Length"))
		assert.Empty(t, r.Header().Get("Last-Modified"), "no time that a client could use for a conditional request")
	}
	assert.Equal(t, 3.0, e.bundleCounter("ok"))
	assert.Contains(t, e.logs.String(), "installer bundle served")
	assert.Contains(t, e.logs.String(), id.String(), "the log names the token by its ID")
	assert.NotContains(t, e.logs.String(), secret)
}

func TestDownloadInstallBundleSupportsRanges(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	name := linuxBundleName()
	r := e.downloadBundle(secret, name, "Range", "bytes=10-19")
	require.Equal(t, http.StatusPartialContent, r.Code)
	assert.Equal(t, ab.archives[name][10:20], r.Body.Bytes())
	assert.Equal(t, fmt.Sprintf("bytes 10-19/%d", len(ab.archives[name])), r.Header().Get("Content-Range"))
	// a range is not logged as a delivery of its own
	assert.NotContains(t, e.logs.String(), "installer bundle served", "only full requests are logged")
}

func TestDownloadInstallBundleNeedsAnEnrollmentToken(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	revoked, revokedID := e.auth.newToken(nil)
	e.tokens.Revoke(revokedID, time.Now())
	past := time.Now().Add(-time.Hour)
	expired, _ := e.auth.newToken(nil, expiresAt(past))
	name := linuxBundleName()

	attempts := map[string][]string{
		"no header":      nil,
		"unknown token":  {"Authorization", "Bearer myre_" + strings.Repeat("a", 32)},
		"revoked token":  {"Authorization", "Bearer " + revoked},
		"expired token":  {"Authorization", "Bearer " + expired},
		"another scheme": {"Authorization", "Basic abc"},
		"the dev token":  {"Authorization", "Bearer " + e.cfg.Dev.SeedEnrollmentToken},
	}
	var details []string
	for label, hdr := range attempts {
		r := e.downloadBundle("", name, hdr...)
		require.Equal(t, http.StatusUnauthorized, r.Code, label)
		assert.Contains(t, r.Header().Get("WWW-Authenticate"), "Bearer", label)
		assert.NotContains(t, r.Body.String(), leakMarker, label)
		assert.NotContains(t, r.Body.Bytes(), ab.archives[name][:20], "no byte of the archive")
		p := problemOf(t, r)
		assert.Equal(t, "unauthorized", p.Code)
		details = append(details, p.Detail)
	}
	for _, d := range details[1:] {
		assert.Equal(t, details[0], d, "the same answer whether the token exists or not")
	}
	assert.Equal(t, float64(len(attempts)), e.bundleCounter("unauthorized"))
}

// The token is read from the header and nowhere else: not from the URL, and a token in the URL is not used.
func TestDownloadInstallBundleDoesNotTakeTheTokenFromTheURL(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	name := linuxBundleName()
	for _, path := range []string{name + "?token=" + secret, name + "?access_token=" + secret, name + "?Authorization=Bearer%20" + secret, name + ";token=" + secret} {
		r := e.do(http.MethodGet, "/install/bundles/"+path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, r.Code, path)
	}
	// and a token in the URL path is no file name
	r := e.do(http.MethodGet, "/install/bundles/"+secret, "", nil, "Authorization", "Bearer "+secret)
	assert.Equal(t, http.StatusNotFound, r.Code)
	assert.NotContains(t, r.Body.String(), secret, "the answer does not repeat a path that is not a bundle")
}

func expiresAt(t time.Time) func(tok *domain.EnrollmentToken) {
	return func(tok *domain.EnrollmentToken) { tok.ExpiresAt = &t }
}

func TestDownloadInstallBundleRateLimitAndDatabaseFailure(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	name := linuxBundleName()

	e.auth.setFail(&domain.RateLimitError{Msg: "too many failed authentications from this client address, retry in 5s", After: 5 * time.Second})
	r := e.downloadBundle(secret, name)
	require.Equal(t, http.StatusTooManyRequests, r.Code)
	assert.Equal(t, "5", r.Header().Get("Retry-After"))
	assert.Equal(t, "too_many_requests", problemOf(t, r).Code)

	e.auth.setFail(fmt.Errorf("%w: enrollment token lookup: connection refused (password=hunter2)", domain.ErrUnavailable))
	r = e.downloadBundle(secret, name)
	require.Equal(t, http.StatusServiceUnavailable, r.Code)
	assert.NotContains(t, r.Body.String(), "hunter2")
	assert.Equal(t, 1.0, e.bundleCounter("rate_limited"))
	assert.Equal(t, 1.0, e.bundleCounter("unavailable"))
	e.auth.setFail(nil)
	assert.Equal(t, http.StatusOK, e.downloadBundle(secret, name).Code)
}

func TestDownloadInstallBundleNotFound(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	for _, name := range []string{
		"checksums.txt", "myriarch-updater_0.1.0_linux_amd64.tar.gz", "myriarch-updater_0.2.0_darwin_arm64.tar.gz", "myriarch-updater_0.2.0_linux_amd64",
		"myriarch-updater_0.2.0_linux_amd64.zip", "myriarch-updater_0.2.0_linux_amd64.tar.gz.sha256", "..%2f..%2fetc%2fpasswd", "%2e%2e", "x",
		"myriarch-updater_0.2.0_linux_amd64.tar.gz%00", "myriarch-updater_0.2.0_linux_amd64.tar.gz%0a", "MYRIARCH-UPDATER_0.2.0_LINUX_AMD64.TAR.GZ",
	} {
		r := e.downloadBundle(secret, name)
		assert.Contains(t, []int{http.StatusNotFound, http.StatusBadRequest, http.StatusMovedPermanently}, r.Code, name)
		assert.NotEqual(t, http.StatusOK, r.Code, name)
	}
	r := e.downloadBundle(secret, "myriarch-updater_0.1.0_linux_amd64.tar.gz")
	require.Equal(t, http.StatusNotFound, r.Code)
	assert.Equal(t, "not_found", problemOf(t, r).Code)
	assert.GreaterOrEqual(t, e.bundleCounter("not_found"), 1.0)
}

func TestDownloadInstallBundleBeforeItIsReady(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", true)
	secret, _ := e.auth.newToken(nil)
	name := linuxBundleName()

	r := e.downloadBundle(secret, name)
	require.Equal(t, http.StatusServiceUnavailable, r.Code, r.Body.String())
	assert.Equal(t, "5", r.Header().Get("Retry-After"))
	doc := asMap(t, e.expect(r, "downloadInstallBundle", http.StatusServiceUnavailable))
	assert.Equal(t, "unavailable", doc["code"])
	assert.Equal(t, "bundle_preparing", asMap(t, doc["issues"].([]any)[0])["code"])
	assert.Equal(t, 1.0, e.bundleCounter("preparing"))

	ab.ready(t)
	assert.Equal(t, http.StatusOK, e.downloadBundle(secret, name).Code, "the request started the preparation")

	// failed: the cause is in the answer, nothing is delivered
	e2 := newAPIEnv(t)
	ab2 := e2.withBundles(t, "0.2.0", false)
	ab2.verifier.fail(errors.New("no good"))
	secret2, _ := e2.auth.newToken(nil)
	require.Equal(t, http.StatusServiceUnavailable, e2.downloadBundle(secret2, name).Code)
	require.NoError(t, ab2.b.Wait(context.Background()))
	r = e2.downloadBundle(secret2, name)
	doc = asMap(t, e2.expect(r, "downloadInstallBundle", http.StatusServiceUnavailable))
	assert.Equal(t, "bundle_failed", asMap(t, doc["issues"].([]any)[0])["code"])
	assert.Equal(t, "30", r.Header().Get("Retry-After"))
	assert.Equal(t, 1.0, e2.bundleCounter("failed"))
}

// An archive that was changed on the disk is not delivered.
func TestDownloadInstallBundleRefusesAChangedArchive(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	name := linuxBundleName()
	b, ok := ab.b.Find(install.Platform{OS: "linux", Arch: "amd64"})
	require.True(t, ok)
	stored := filepath.Join(ab.cacheDir, artifact.InstallDir, "sha256", b.SHA256)
	require.FileExists(t, stored)
	require.NoError(t, os.WriteFile(stored, []byte("evil"), 0o640))
	r := e.downloadBundle(secret, name)
	require.Equal(t, http.StatusServiceUnavailable, r.Code, r.Body.String())
	assert.NotContains(t, r.Body.String(), "evil")
	assert.Equal(t, "5", r.Header().Get("Retry-After"))
	require.NoError(t, ab.b.Wait(context.Background()))
	r = e.downloadBundle(secret, name)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Equal(t, ab.archives[name], r.Body.Bytes())
}

func TestDownloadInstallBundleIsOffWhenTheInstallationIsOff(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	off := e.cfg.Install
	off.Enabled = false
	e.useBundles(t, ab.b, false, func(d *install.Deps) { d.Config = off })
	before := e.auth.callCount()
	for _, tok := range []string{secret, "", "myre_" + strings.Repeat("x", 32)} {
		r := e.downloadBundle(tok, linuxBundleName())
		require.Equal(t, http.StatusNotFound, r.Code)
		assert.Equal(t, "not_found", problemOf(t, r).Code)
	}
	assert.Equal(t, before, e.auth.callCount(), "no token was looked at")
	assert.Equal(t, 3.0, e.bundleCounter("disabled"))
}

// The token is written nowhere: not into the log, not into the metrics, not into an answer.
func TestInstallBundleRoutesNeverRecordTheToken(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	revoked, revokedID := e.auth.newToken(nil)
	e.tokens.Revoke(revokedID, time.Now())
	name := linuxBundleName()

	var seen []string
	collect := func(r resp) {
		seen = append(seen, r.Body.String())
		for _, vs := range r.Header() {
			seen = append(seen, strings.Join(vs, ","))
		}
	}
	collect(e.downloadBundle(secret, name))
	collect(e.downloadBundle(secret, name, "Range", "bytes=0-9"))
	collect(e.downloadBundle(revoked, name))
	collect(e.downloadBundle(secret, "nope"))
	collect(e.renderCommands("admin", edgeSite()))
	collect(e.listBundles("viewer"))

	var metrics bytes.Buffer
	mfs, err := e.reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		_, err := expfmt.MetricFamilyToText(&metrics, mf)
		require.NoError(t, err)
	}
	seen = append(seen, e.logs.String(), metrics.String())
	for _, s := range seen {
		for _, value := range []string{secret, revoked} {
			assert.NotContains(t, s, value)
		}
	}
	// the downloads leave a trace by token ID only, and nothing is audited
	assert.Contains(t, metrics.String(), `myriarch_install_bundle_downloads_total{result="ok"} 2`)
}

// Many hosts install at once: every request ends with the bytes, and the archive is read from the same file.
func TestDownloadInstallBundleConcurrently(t *testing.T) {
	e := newAPIEnv(t)
	ab := e.withBundles(t, "0.2.0", false)
	ab.ready(t)
	secret, _ := e.auth.newToken(nil)
	name := linuxBundleName()
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.downloadBundle(secret, name)
			assert.Equal(t, http.StatusOK, r.Code)
			assert.Equal(t, ab.archives[name], r.Body.Bytes())
		}()
	}
	wg.Wait()
	assert.Equal(t, 30.0, e.bundleCounter("ok"))
}
