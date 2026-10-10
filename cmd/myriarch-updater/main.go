// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Command myriarch-updater is the myriarch host agent that installs collector
// binary updates offered via OpAMP (Linux systemd, Windows service).
// See internal/updater for the implementation.
package main

import (
	"fmt"
	"os"

	// embed the Mozilla root CA set as the fallback trust store: the updater
	// image is built FROM scratch (no OS packages, no CA bundle) and downloads
	// collector releases over TLS. The fallback is used only when the host has
	// no root certificates of its own (x509.SetFallbackRoots), see
	// cmd/myriarchd/main.go.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/updater"
)

func main() {
	if err := updater.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, brand.UpdaterBinary+":", err)
		os.Exit(1)
	}
}
