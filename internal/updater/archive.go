// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractResult describes the extracted binary.
type ExtractResult struct {
	Path   string
	Size   int64
	SHA256 []byte
}

const maxArchiveEntries = 10000

// extractBinary extracts exactly one regular file named binaryName from the
// archive (.tar.gz or .zip, detected by magic bytes) into destDir. Everything
// else in the archive is ignored and never written. Hardening:
//   - only the entry whose cleaned name equals binaryName (archive root) is
//     written; the destination path is built from binaryName, never from the
//     archive, so path traversal / zip-slip is impossible by construction;
//   - entries with absolute paths, ".." elements or NUL bytes make the archive
//     invalid (fail closed);
//   - the binary entry must be a regular file (no symlink/hardlink/device);
//   - sizes are enforced while copying, not just from headers (decompression
//     bombs), and the total decompressed stream is capped.
func extractBinary(archivePath, destDir, binaryName string, maxBinaryBytes int64) (res ExtractResult, err error) {
	defer func() {
		if err != nil && binaryName != "" && binaryName == filepath.Base(binaryName) {
			// never leave a partially trusted file behind
			_ = os.Remove(filepath.Join(destDir, binaryName))
			_ = os.Remove(filepath.Join(destDir, "."+binaryName+".partial"))
		}
	}()
	if binaryName == "" || binaryName != filepath.Base(binaryName) || strings.ContainsAny(binaryName, `/\`) {
		return ExtractResult{}, fmt.Errorf("invalid binary name %q", binaryName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return ExtractResult{}, err
	}
	defer func() { _ = f.Close() }()
	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ExtractResult{}, err
	}
	switch {
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return extractFromTarGz(f, destDir, binaryName, maxBinaryBytes)
	case n >= 4 && bytes.Equal(magic[:], []byte("PK\x03\x04")):
		st, err := f.Stat()
		if err != nil {
			return ExtractResult{}, err
		}
		return extractFromZip(f, st.Size(), destDir, binaryName, maxBinaryBytes)
	default:
		return ExtractResult{}, errors.New("unsupported archive format (expected .tar.gz or .zip)")
	}
}

// checkEntryName rejects unsafe archive entry names and returns the cleaned
// name relative to the archive root ("" for the root itself).
func checkEntryName(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("archive entry %q contains NUL", name)
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || (len(name) >= 2 && name[1] == ':') {
		return "", fmt.Errorf("archive entry %q has an absolute path", name)
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	var clean []string
	for _, p := range parts {
		switch p {
		case "..":
			return "", fmt.Errorf("archive entry %q escapes the archive root", name)
		case ".":
		default:
			clean = append(clean, p)
		}
	}
	return strings.Join(clean, "/"), nil
}

type capReader struct {
	r   io.Reader
	n   int64 // remaining
	err error
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, c.err
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	n, err := c.r.Read(p)
	c.n -= int64(n)
	return n, err
}

func extractFromTarGz(r io.Reader, destDir, binaryName string, maxBytes int64) (ExtractResult, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	total := &capReader{r: gz, n: 2*maxBytes + (16 << 20), err: errors.New("archive exceeds the decompressed size limit")}
	tr := tar.NewReader(total)

	var res ExtractResult
	found := false
	for i := 0; ; i++ {
		if i > maxArchiveEntries {
			return ExtractResult{}, errors.New("archive has too many entries")
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ExtractResult{}, fmt.Errorf("read tar: %w", err)
		}
		name, err := checkEntryName(hdr.Name)
		if err != nil {
			return ExtractResult{}, err
		}
		if name != binaryName {
			continue // never written
		}
		if found {
			return ExtractResult{}, fmt.Errorf("archive contains %q more than once", binaryName)
		}
		if hdr.Typeflag != tar.TypeReg { //nolint:staticcheck // TypeRegA is normalized by archive/tar
			return ExtractResult{}, fmt.Errorf("archive entry %q is not a regular file (type %q)", hdr.Name, hdr.Typeflag)
		}
		if hdr.Size <= 0 || hdr.Size > maxBytes {
			return ExtractResult{}, fmt.Errorf("archive entry %q has invalid size %d (limit %d)", hdr.Name, hdr.Size, maxBytes)
		}
		res, err = writeBinary(tr, hdr.Size, destDir, binaryName)
		if err != nil {
			return ExtractResult{}, err
		}
		found = true
	}
	if !found {
		return ExtractResult{}, fmt.Errorf("archive does not contain %q at its root", binaryName)
	}
	return res, nil
}

func extractFromZip(r io.ReaderAt, size int64, destDir, binaryName string, maxBytes int64) (ExtractResult, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("open zip: %w", err)
	}
	if len(zr.File) > maxArchiveEntries {
		return ExtractResult{}, errors.New("archive has too many entries")
	}
	var res ExtractResult
	found := false
	for _, zf := range zr.File {
		name, err := checkEntryName(zf.Name)
		if err != nil {
			return ExtractResult{}, err
		}
		if name != binaryName {
			continue
		}
		if found {
			return ExtractResult{}, fmt.Errorf("archive contains %q more than once", binaryName)
		}
		if !zf.Mode().IsRegular() {
			return ExtractResult{}, fmt.Errorf("archive entry %q is not a regular file", zf.Name)
		}
		if zf.UncompressedSize64 == 0 || zf.UncompressedSize64 > uint64(maxBytes) {
			return ExtractResult{}, fmt.Errorf("archive entry %q has invalid size %d (limit %d)", zf.Name, zf.UncompressedSize64, maxBytes)
		}
		rc, err := zf.Open()
		if err != nil {
			return ExtractResult{}, fmt.Errorf("open zip entry: %w", err)
		}
		res, err = writeBinary(rc, int64(zf.UncompressedSize64), destDir, binaryName)
		_ = rc.Close()
		if err != nil {
			return ExtractResult{}, err
		}
		found = true
	}
	if !found {
		return ExtractResult{}, fmt.Errorf("archive does not contain %q at its root", binaryName)
	}
	return res, nil
}

// writeBinary copies exactly size bytes from r to destDir/binaryName (mode
// 0755), failing if r yields fewer or more bytes, and returns its SHA-256.
func writeBinary(r io.Reader, size int64, destDir, binaryName string) (ExtractResult, error) {
	final := filepath.Join(destDir, binaryName)
	tmp := filepath.Join(destDir, "."+binaryName+".partial")
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return ExtractResult{}, err
	}
	fail := func(err error) (ExtractResult, error) {
		_ = out.Close()
		_ = os.Remove(tmp)
		return ExtractResult{}, err
	}
	h := sha256.New()
	// Read one byte more than announced to detect size lies.
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, size+1))
	if err != nil {
		return fail(fmt.Errorf("extract %s: %w", binaryName, err))
	}
	if n != size {
		return fail(fmt.Errorf("extract %s: size mismatch (header %d, actual %d)", binaryName, size, n))
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return ExtractResult{}, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return ExtractResult{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return ExtractResult{}, err
	}
	return ExtractResult{Path: final, Size: n, SHA256: h.Sum(nil)}, nil
}
