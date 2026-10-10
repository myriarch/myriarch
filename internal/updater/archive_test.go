// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeArchive(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.bin")
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func TestExtractBinaryTarGzOK(t *testing.T) {
	body := []byte("#!/bin/sh\necho hi\n")
	a := writeArchive(t, makeTarGz(t,
		tarEntry{name: "README.md", body: []byte("r"), mode: 0o644},
		tarEntry{name: "./" + testBinary, body: body},
	))
	dest := t.TempDir()
	res, err := extractBinary(a, dest, testBinary, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, int64(len(body)), res.Size)
	assert.Equal(t, sha(body), res.SHA256)
	got, err := os.ReadFile(filepath.Join(dest, testBinary))
	require.NoError(t, err)
	assert.Equal(t, body, got)
	if st, _ := os.Stat(res.Path); st != nil {
		assert.NotZero(t, st.Mode()&0o100, "binary must be executable")
	}
	entries, _ := os.ReadDir(dest)
	assert.Len(t, entries, 1, "README and partial files must not be written")
}

func TestExtractBinaryRejects(t *testing.T) {
	big := make([]byte, 5000)
	cases := map[string]struct {
		archive []byte
		wantErr string
	}{
		"path traversal entry": {
			makeTarGz(t, tarEntry{name: "../evil", body: []byte("x")}, tarEntry{name: testBinary, body: []byte("b")}),
			"escapes the archive root"},
		"traversal in the middle": {
			makeTarGz(t, tarEntry{name: "a/../../evil", body: []byte("x")}, tarEntry{name: testBinary, body: []byte("b")}),
			"escapes the archive root"},
		"absolute path": {
			makeTarGz(t, tarEntry{name: "/etc/passwd", body: []byte("x")}, tarEntry{name: testBinary, body: []byte("b")}),
			"absolute path"},
		"windows drive path": {
			makeTarGz(t, tarEntry{name: `C:\Windows\x`, body: []byte("x")}, tarEntry{name: testBinary, body: []byte("b")}),
			"absolute path"},
		"backslash traversal": {
			makeTarGz(t, tarEntry{name: `..\..\evil`, body: []byte("x")}, tarEntry{name: testBinary, body: []byte("b")}),
			"escapes the archive root"},
		"binary is a symlink": {
			makeTarGz(t, tarEntry{name: testBinary, typeflag: tar.TypeSymlink, linkname: "/bin/sh"}),
			"not a regular file"},
		"binary is a hardlink": {
			makeTarGz(t, tarEntry{name: testBinary, typeflag: tar.TypeLink, linkname: "/etc/passwd"}),
			"not a regular file"},
		"binary only in subdirectory": {
			makeTarGz(t, tarEntry{name: "bin/" + testBinary, body: []byte("b")}),
			"does not contain"},
		"binary twice": {
			makeTarGz(t, tarEntry{name: testBinary, body: []byte("a")}, tarEntry{name: testBinary, body: []byte("b")}),
			"more than once"},
		"missing binary": {
			makeTarGz(t, tarEntry{name: "README.md", body: []byte("r")}),
			"does not contain"},
		"binary over limit": {
			makeTarGz(t, tarEntry{name: testBinary, body: big}),
			"invalid size"},
		"empty binary": {
			makeTarGz(t, tarEntry{name: testBinary, body: nil}),
			"invalid size"},
		"not an archive": {[]byte("hello world, not an archive"), "unsupported archive format"},
		"truncated gzip": {makeTarGz(t, tarEntry{name: testBinary, body: big})[:30], ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			_, err := extractBinary(writeArchive(t, tc.archive), dest, testBinary, 4096)
			require.Error(t, err)
			if tc.wantErr != "" {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
			entries, _ := os.ReadDir(dest)
			assert.Empty(t, entries, "nothing may be written on failure")
			// nothing outside dest either
			parent := filepath.Dir(dest)
			_, statErr := os.Stat(filepath.Join(parent, "evil"))
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

func TestExtractBinaryDecompressionBomb(t *testing.T) {
	// A skipped (non-binary) entry that expands far beyond the cap must abort.
	huge := make([]byte, 40<<20) // compresses to a few KiB
	a := writeArchive(t, makeTarGz(t,
		tarEntry{name: "junk", body: huge},
		tarEntry{name: testBinary, body: []byte("b")},
	))
	_, err := extractBinary(a, t.TempDir(), testBinary, 1<<20) // cap = 2*1MiB+16MiB
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompressed size limit")
}

func TestExtractBinaryInvalidName(t *testing.T) {
	for _, n := range []string{"", "../x", "a/b", `a\b`} {
		_, err := extractBinary("x", t.TempDir(), n, 10)
		require.Error(t, err, n)
	}
}

func TestExtractBinaryZip(t *testing.T) {
	body := []byte("zip-binary")
	a := writeArchive(t, makeZip(t, map[string][]byte{"README.md": []byte("r"), testBinary: body}))
	dest := t.TempDir()
	res, err := extractBinary(a, dest, testBinary, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, sha(body), res.SHA256)

	for name, files := range map[string]map[string][]byte{
		"traversal": {"../evil": []byte("x"), testBinary: body},
		"nested":    {"sub/" + testBinary: body},
		"too big":   {testBinary: make([]byte, 5000)},
	} {
		t.Run("reject "+name, func(t *testing.T) {
			d := t.TempDir()
			_, err := extractBinary(writeArchive(t, makeZip(t, files)), d, testBinary, 4096)
			require.Error(t, err)
			entries, _ := os.ReadDir(d)
			assert.Empty(t, entries)
		})
	}
}

func TestCheckEntryName(t *testing.T) {
	ok := map[string]string{"a": "a", "./a": "a", "a/b": "a/b", "a//b": "a/b", "./": "", "": ""}
	for in, want := range ok {
		got, err := checkEntryName(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"..", "../a", "a/..", "/a", `\a`, "C:/a", "a\x00b"} {
		_, err := checkEntryName(in)
		assert.Error(t, err, in)
	}
}
