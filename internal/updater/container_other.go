// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package updater

import "errors"

// runContainerCommand is not available on Windows: the updater runs there as
// a service next to the supervisor service (myriarch-updater run).
func runContainerCommand(string) error {
	return errors.New("container mode is only supported on Linux and macOS")
}
