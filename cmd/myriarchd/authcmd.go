// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
)

// Limits of "myriarchd auth hash-password".
const (
	minPasswordBytes = 12
	// maxPasswordBytes is the longest input bcrypt takes into account.
	maxPasswordBytes = 72
	defaultHashCost  = 12
	maxHashCost      = 14
)

// newAuthCommand returns "myriarchd auth" with its subcommands.
func newAuthCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Helpers for the credentials of the bootstrap admin",
	}
	c.AddCommand(newHashPasswordCommand())
	return c
}

func newHashPasswordCommand() *cobra.Command {
	var cost int
	cmd := &cobra.Command{
		Use:   "hash-password [--cost N]",
		Short: "Print the bcrypt hash of a password read from standard input",
		Long: `Reads a password from standard input (the first line; the line break is removed), and prints its
bcrypt hash and a newline on standard output. The password is never echoed or printed. Use the output
as the file auth.bootstrapAdmin.passwordHashFile.

The password must have 12 to 72 bytes (bcrypt ignores everything after 72 bytes). The cost is 12 by
default; it must be between ` + fmt.Sprint(domain.MinBcryptCost) + ` and ` + fmt.Sprint(maxHashCost) + `.

Example:
  printf '%s' "$PASSWORD" | ` + brand.ServerBinary + ` auth hash-password > password-hash

Exit codes: 0 hash printed, 1 invalid input, 2 wrong usage.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &exitError{code: exitUsage, msg: "hash-password takes no arguments; the password is read from standard input"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHashPassword(cmd.InOrStdin(), cmd.OutOrStdout(), cost)
		},
	}
	cmd.Flags().IntVar(&cost, "cost", defaultHashCost, fmt.Sprintf("bcrypt cost (%d to %d)", domain.MinBcryptCost, maxHashCost))
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &exitError{code: exitUsage, msg: err.Error()} })
	return cmd
}

func runHashPassword(in io.Reader, out io.Writer, cost int) error {
	if cost < domain.MinBcryptCost || cost > maxHashCost {
		return &exitError{code: 1, msg: fmt.Sprintf("--cost must be between %d and %d", domain.MinBcryptCost, maxHashCost)}
	}
	if f, ok := in.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			return &exitError{code: 1, msg: "refusing to read a password from a terminal, where it would be echoed; pipe it in: " +
				`printf '%s' "$PASSWORD" | ` + brand.ServerBinary + " auth hash-password"}
		}
	}
	pw, err := readFirstLine(in)
	if err != nil {
		return &exitError{code: 1, msg: "cannot read the password from standard input"}
	}
	if len(pw) < minPasswordBytes || len(pw) > maxPasswordBytes {
		return &exitError{code: 1, msg: fmt.Sprintf("the password must have %d to %d bytes (it has %d)", minPasswordBytes, maxPasswordBytes, len(pw))}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), cost)
	if err != nil {
		return &exitError{code: 1, msg: "cannot hash the password: " + err.Error()}
	}
	_, err = fmt.Fprintf(out, "%s\n", hash)
	return err
}

// readFirstLine returns the first line of r without the line break. It reads
// at most a few hundred bytes more than a usable password has.
func readFirstLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 4*maxPasswordBytes)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
