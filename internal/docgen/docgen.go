// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package docgen holds the file handling that the generators of the reference documentation share: writing generated Markdown
// pages and comparing them with the committed files. The generators are internal/clidoc (command line), internal/apidoc (REST
// interface) and internal/refdoc (configuration, environment and metrics). Every one is guarded against drift by a test that
// calls Diff or DiffFile. Write and Diff own a whole directory of generated pages; WriteFile and DiffFile handle single pages of
// a directory that also holds hand-written files.
package docgen

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
)

// Write writes the files into dir (created when missing) and removes every other *.md file of dir, so renamed or
// removed pages leave no stale page behind. Files with identical content are not touched.
func Write(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
			continue
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		if _, ok := files[e.Name()]; !ok {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Diff compares the files with the *.md files in dir and returns one line per difference, sorted: "missing: <name>"
// (generated but not on disk), "changed: <name>" and "extra: <name>" (on disk but not generated). An empty result
// means that dir is up to date. A missing dir counts as empty.
func Diff(dir string, files map[string][]byte) []string {
	var out []string
	for name, content := range files {
		old, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case err != nil:
			out = append(out, "missing: "+name)
		case !bytes.Equal(old, content):
			out = append(out, "changed: "+name)
		}
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			if _, ok := files[e.Name()]; !ok {
				out = append(out, "extra: "+e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

// WriteFile writes one generated file (the parent directory is created when missing). A file with identical content is not
// touched. Unlike Write it never removes other files, so it is safe for a directory with hand-written pages.
func WriteFile(path string, content []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// DiffFile compares content with the file at path and returns "" when they are identical, "missing: <base name>" when the
// file does not exist (or cannot be read) and "changed: <base name>" when the content differs.
func DiffFile(path string, content []byte) string {
	old, err := os.ReadFile(path)
	switch {
	case err != nil:
		return "missing: " + filepath.Base(path)
	case !bytes.Equal(old, content):
		return "changed: " + filepath.Base(path)
	}
	return ""
}
