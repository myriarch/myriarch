// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/myriarch/myriarch"
)

// newLicensesCommand returns "licenses": it prints the license texts that are
// compiled into the binary. The bare myriarch-updater binary is a release
// asset and is placed on hosts without any other file, so the texts must
// travel inside it (Apache-2.0 section 4(a), and the notice requirements of
// the BSD, MIT and ISC components).
func newLicensesCommand() *cobra.Command {
	var thirdParty bool
	c := &cobra.Command{
		Use:   "licenses",
		Short: "Print the license of myriarch, or with --third-party the notices of the included software",
		Long: "Prints the Apache-2.0 license text of myriarch. With --third-party it prints THIRD_PARTY_NOTICES instead: the " +
			"license texts and NOTICE files of the third-party software that the myriarch binaries include.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			text := myriarch.License()
			if thirdParty {
				text = myriarch.ThirdPartyNotices()
			}
			_, err := io.WriteString(cmd.OutOrStdout(), text)
			return err
		},
	}
	c.Flags().BoolVar(&thirdParty, "third-party", false, "print the third-party notices instead of the project license")
	return c
}
