// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/testutil/clitest"
)

func TestMain(m *testing.M) {
	clitest.RunAsMain(main)
	os.Exit(m.Run())
}

// TestOutputContract runs the real main with the real os.Stdout and os.Stderr: the version and help go to stdout,
// errors to stderr (docs/development.md, "CLI output").
func TestOutputContract(t *testing.T) { clitest.CheckOutputContract(t, brand.CLIBinary) }
