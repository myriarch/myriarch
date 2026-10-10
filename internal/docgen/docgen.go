// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package docgen holds the file handling that the generators of the reference documentation share (internal/clidoc for the
// command line reference, internal/apidoc for the REST API reference): writing a directory of generated Markdown pages and
// comparing it with the committed files. Both generators are guarded against drift by a test that calls Diff.
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
