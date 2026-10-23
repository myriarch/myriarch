// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/myriarch/myriarch/internal/brand"
)

// The two file commands exist for init containers. The upstream collector image is built FROM scratch and has no shell
// and no cp, so a pod that has to put files on a shared volume runs this static binary instead. They never touch the
// network.

// installSelf copies the running binary to <dir>/myriarch-updater (myriarch-updater.exe on Windows) with mode 0755.
func installSelf(dir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	return copyRegularFile(exe, filepath.Join(dir, brand.UpdaterBinary+exeSuffix()), true, true)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// copyRegularFile copies the regular file src to dst and returns the path it wrote. dst may be an existing directory,
// then the file keeps the base name of src. With dstIsFile (install-self) dst is never treated as a directory. Both paths
// must be absolute. A destination that exists must be a regular file: a symlink, a directory or a device is refused, so
// the copy can neither follow a link out of the target directory nor replace something that is not a file. The source
// may be a symlink (a mounted ConfigMap is one) but must resolve to a regular file. The mode is 0755 when the source
// has an execute bit (or with forceExec) and 0644 otherwise. The write goes to a temporary file in the destination
// directory that is renamed into place, so a reader never sees a half-written file and a failure leaves nothing behind.
func copyRegularFile(src, dst string, dstIsFile, forceExec bool) (string, error) {
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("source %q must be an absolute path", src)
	}
	if !filepath.IsAbs(dst) {
		return "", fmt.Errorf("destination %q must be an absolute path", dst)
	}
	// Refuse anything that is not a regular file before opening it: opening a named pipe without a writer blocks
	// forever and an init container would hang. The check on the opened file below stays the authoritative one.
	if pre, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("open source: %w", err)
	} else if !pre.Mode().IsRegular() {
		return "", fmt.Errorf("source %q is not a regular file (%s)", src, kindOf(pre.Mode()))
	}
	in, err := os.OpenFile(src, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }()
	srcInfo, err := in.Stat()
	if err != nil {
		return "", fmt.Errorf("stat source: %w", err)
	}
	if !srcInfo.Mode().IsRegular() {
		return "", fmt.Errorf("source %q is not a regular file (%s)", src, kindOf(srcInfo.Mode()))
	}

	target := filepath.Clean(dst)
	if !dstIsFile {
		if li, err := os.Lstat(target); err == nil && li.IsDir() {
			target = filepath.Join(target, filepath.Base(src))
		}
	}
	li, err := os.Lstat(target)
	switch {
	case err == nil:
		if !li.Mode().IsRegular() {
			return "", fmt.Errorf("destination %q exists and is not a regular file (%s)", target, kindOf(li.Mode()))
		}
		if si, serr := os.Stat(target); serr == nil && os.SameFile(srcInfo, si) {
			return "", fmt.Errorf("source and destination %q are the same file", target)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return "", fmt.Errorf("stat destination: %w", err)
	}

	mode := os.FileMode(0o644)
	if forceExec || srcInfo.Mode()&0o111 != 0 {
		mode = 0o755
	}
	if err := writeAtomic(target, in, mode); err != nil {
		return "", err
	}
	return target, nil
}

// writeAtomic streams r into a new temporary file next to target, syncs it and renames it over target. The directory of
// target must exist and must not be a symlink; it is not created. It is opened once and the temporary file, the rename
// and the clean-up all go through that handle, so a swap of the directory entry afterwards changes nothing.
func writeAtomic(target string, r io.Reader, mode os.FileMode) (err error) {
	dir := filepath.Dir(target)
	li, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("destination directory: %w", err)
	}
	if !li.IsDir() {
		return fmt.Errorf("destination directory %q is not a directory (%s)", dir, kindOf(li.Mode()))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open destination directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if ri, rerr := root.Stat("."); rerr != nil || !os.SameFile(li, ri) {
		return fmt.Errorf("destination directory %q changed while it was opened", dir)
	}

	name := filepath.Base(target)
	tmp := "." + name + ".tmp-" + randSuffix()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary file in %q: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	if _, err = io.Copy(f, r); err != nil {
		return fmt.Errorf("write %q: %w", target, err)
	}
	if cerr := f.Chmod(mode); cerr != nil && runtime.GOOS != "windows" {
		err = fmt.Errorf("set mode: %w", cerr)
		return err
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", target, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", target, err)
	}
	if err = root.Rename(tmp, name); err != nil {
		return fmt.Errorf("rename into place %q: %w", target, err)
	}
	if d, derr := root.Open("."); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func kindOf(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "directory"
	case m&os.ModeDevice != 0:
		return "device"
	case m&os.ModeNamedPipe != 0:
		return "named pipe"
	case m&os.ModeSocket != 0:
		return "socket"
	default:
		return m.Type().String()
	}
}
