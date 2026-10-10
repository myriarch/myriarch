// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Command myrctl validates and renders myriarch GitOps repositories (CI).
// See internal/myrctl for the implementation.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	// embed the Mozilla root CA set as the fallback trust store (container
	// image FROM scratch without CA bundle); used only when the host has no
	// roots of its own, see cmd/myriarchd/main.go. (The IANA time zone database
	// comes with internal/blueprint.)
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/myriarch/myriarch/internal/myrctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := myrctl.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
