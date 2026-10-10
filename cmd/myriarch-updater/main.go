// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Command myriarch-updater is the myriarch host agent that installs collector
// binary updates offered via OpAMP (Linux systemd, Windows service).
// See internal/updater for the implementation.
package main

import (
	"fmt"
	"os"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/updater"
)

func main() {
	if err := updater.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, brand.UpdaterBinary+":", err)
		os.Exit(1)
	}
}
