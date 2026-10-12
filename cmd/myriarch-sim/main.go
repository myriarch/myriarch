// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Command myriarch-sim simulates many OpAMP supervisors and myriarch-updaters
// against myriarchd (load and integration testing).
// See internal/sim for the implementation.
package main

import (
	"os"

	// embed the Mozilla root CA set as the fallback trust store (container
	// image FROM scratch without CA bundle); used only when the host has no
	// roots of its own, see cmd/myriarchd/main.go. (The simulator needs no time
	// zone database: it only reports zone names as strings.)
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/myriarch/myriarch/internal/sim"
)

func main() {
	os.Exit(sim.Run(os.Args[1:], os.Stdout, os.Stderr))
}
