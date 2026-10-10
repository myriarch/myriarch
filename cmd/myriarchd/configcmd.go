// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/myriarch/myriarch/internal/configload"
)

// Exit codes of "myriarchd config validate".
const (
	exitInvalid = 1 // the configuration is invalid
	exitUsage   = 2 // wrong usage: no --config, the file cannot be read
)

// newConfigCommand returns "myriarchd config" with its subcommands.
func newConfigCommand() *cobra.Command {
	cfg := &cobra.Command{
		Use:   "config",
		Short: "Check and inspect a myriarchd configuration without starting myriarchd",
	}
	cfg.AddCommand(newConfigValidateCommand(), newConfigPrintCommand())
	return cfg
}

type configFlags struct {
	path      string
	skipFiles bool
}

func (f *configFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.path, "config", "c", "", "path to myriarchd.yaml (environment MYRIARCH_* overrides apply)")
	cmd.Flags().BoolVar(&f.skipFiles, "skip-files", false, "do not read the files the configuration refers to (secrets, CA files, certificates): their paths count as present")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &exitError{code: exitUsage, msg: err.Error()} })
}

func newConfigValidateCommand() *cobra.Command {
	var f configFlags
	cmd := &cobra.Command{
		Use:   "validate --config FILE [--skip-files]",
		Short: "Check a configuration exactly like `myriarchd serve` loads it, without starting anything",
		Long: `Loads the configuration like "myriarchd serve" (defaults, YAML with unknown keys rejected,
${VAR} expansion, MYRIARCH_* overrides) and runs every check that serve makes before it touches the
database or the network. Nothing is started and no connection is made.

Without --skip-files the files that the configuration refers to are read and checked as well
(secrets not empty, CA file with a certificate, key pair matching, trusted root parsable).

Output: "OK: <file> is valid" and one "WARNING: <text>" line per warning on stdout; one
"ERROR: <path>: <message>" line per problem on stderr.
Exit codes: 0 valid (warnings allowed), 1 invalid, 2 wrong usage (no --config, file not readable).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigValidate(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	f.add(cmd)
	return cmd
}

func runConfigValidate(stdout, stderr io.Writer, f configFlags) error {
	if f.path == "" {
		fmt.Fprintln(stderr, "ERROR: --config: the path of the configuration file is required")
		return &exitError{code: exitUsage}
	}
	_, warnings, err := configload.LoadWithOptions(f.path, configload.Options{SkipFiles: f.skipFiles, CheckFiles: !f.skipFiles})
	if err != nil {
		var re *configload.ReadError
		if errors.As(err, &re) {
			fmt.Fprintf(stderr, "ERROR: %s: cannot read the configuration file: %s\n", f.path, osError(re.Err))
			return &exitError{code: exitUsage}
		}
		for _, p := range configload.Problems(err) {
			fmt.Fprintf(stderr, "ERROR: %s\n", oneLine(p.String()))
		}
		return &exitError{code: exitInvalid}
	}
	fmt.Fprintf(stdout, "OK: %s is valid\n", f.path)
	for _, w := range warnings {
		fmt.Fprintf(stdout, "WARNING: %s\n", oneLine(w))
	}
	return nil
}

// osError returns the system message of a file error without the path.
func osError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// oneLine makes a message fit on one output line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func newConfigPrintCommand() *cobra.Command {
	var f configFlags
	cmd := &cobra.Command{
		Use:   "print --config FILE [--skip-files]",
		Short: "Print the effective configuration (defaults, file, environment) as YAML, secrets redacted",
		Long: `Prints the configuration that "myriarchd serve" would run with: the defaults, the file and the
MYRIARCH_* environment overrides applied. Secrets (database.dsn, auth.oidc.clientSecret,
gitops.webhookSecret, the URLs of notification channels, dev passwords) are replaced by
"<redacted>". The configuration is validated first; exit codes as for "config validate".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigPrint(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	f.add(cmd)
	return cmd
}

func runConfigPrint(stdout, stderr io.Writer, f configFlags) error {
	if f.path == "" {
		fmt.Fprintln(stderr, "ERROR: --config: the path of the configuration file is required")
		return &exitError{code: exitUsage}
	}
	cfg, _, err := configload.LoadWithOptions(f.path, configload.Options{SkipFiles: f.skipFiles, CheckFiles: !f.skipFiles})
	if err != nil {
		var re *configload.ReadError
		if errors.As(err, &re) {
			fmt.Fprintf(stderr, "ERROR: %s: cannot read the configuration file: %s\n", f.path, osError(re.Err))
			return &exitError{code: exitUsage}
		}
		for _, p := range configload.Problems(err) {
			fmt.Fprintf(stderr, "ERROR: %s\n", oneLine(p.String()))
		}
		return &exitError{code: exitInvalid}
	}
	out, err := configload.RedactedYAML(&cfg)
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}
