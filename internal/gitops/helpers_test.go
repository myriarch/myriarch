// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func osDirFS(t testing.TB, dir string) fs.FS {
	t.Helper()
	return os.DirFS(dir)
}

// fileURL returns the file:// URL of a local directory: file:///tmp/work on
// Unix, file:///C:/Users/me/work on Windows (the volume follows the empty host).
func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// The Windows form is checked on every platform. "file://" in front of a Windows
// path does not work with go-git wherever it runs: with backslashes the URL does
// not parse ("invalid port"), with slashes the drive letter becomes the host. In
// "file:///C:/Users/me/work" the host is empty, and go-git takes the leading
// slash off before it hands the path to git-upload-pack on Windows.
func TestFileURLIsAcceptedByGoGit(t *testing.T) {
	for dir, want := range map[string]string{
		"/tmp/work":            "file:///tmp/work",
		"C:/Users/me/work":     "file:///C:/Users/me/work",
		"C:/Users/me/my repo":  "file:///C:/Users/me/my%20repo",
		"/tmp/a b/100%/#frag?": "file:///tmp/a%20b/100%25/%23frag%3F",
	} {
		got := fileURL(dir)
		assert.Equal(t, want, got)
		ep, err := transport.NewEndpoint(got)
		require.NoError(t, err, got)
		assert.Equal(t, "file", ep.Protocol, got)
		assert.Empty(t, ep.Host, got)
		assert.Equal(t, "/"+strings.TrimPrefix(dir, "/"), ep.Path, "the percent escapes are decoded: %s", got)
	}
}
