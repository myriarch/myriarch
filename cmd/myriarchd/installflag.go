// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// installBundleDirFlag is the flag of `myriarchd dev` that names a directory of installer bundles. The archives in it carry
// no signature, so a server in production never takes them: the flag exists in development mode only.
const installBundleDirFlag = "install-bundle-dir"

// addInstallBundleDirFlag adds the flag to the command of the development server.
func addInstallBundleDirFlag(dev *cobra.Command, into *string) {
	dev.Flags().StringVar(into, installBundleDirFlag, "", "serve the installer bundles from this directory (myriarch-updater_<version>_<os>_<arch>.tar.gz or .zip, as in the release) instead of the bundles of a release; they are not signed, so only a development server takes them")
}

// refuseInstallBundleDirFlag makes `myriarchd serve` refuse the flag at start with a message that says why. The flag is
// hidden: it is not a feature of serve and is in neither its help nor the reference.
func refuseInstallBundleDirFlag(serve *cobra.Command) {
	serve.Flags().String(installBundleDirFlag, "", "")
	_ = serve.Flags().MarkHidden(installBundleDirFlag)
	next := serve.RunE
	serve.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed(installBundleDirFlag) {
			return fmt.Errorf("--%s exists only in development mode (myriarchd dev): a server in production serves the installer bundles of its own release", installBundleDirFlag)
		}
		return next(cmd, args)
	}
}
