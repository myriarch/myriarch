// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/spf13/cobra"
)

type verifyBundleFlags struct {
	bundle, artifact, sha256, version, trustedRoot, identity, issuer, tufCache string
}

func runVerifyBundle(cmd *cobra.Command, f verifyBundleFlags) error {
	if f.bundle == "" || f.version == "" || (f.artifact == "" && f.sha256 == "") {
		return errors.New("--bundle, --version and one of --artifact/--sha256 are required")
	}
	if !anchoredRegexp(f.identity) {
		return errors.New("--identity-regexp must be anchored: start with ^ and end with $")
	}
	sig, err := os.ReadFile(f.bundle)
	if err != nil {
		return err
	}
	var digest []byte
	if f.sha256 != "" {
		digest, err = hex.DecodeString(strings.TrimSpace(f.sha256))
		if err != nil {
			return fmt.Errorf("--sha256: %w", err)
		}
	} else if digest, err = fileSHA256(f.artifact); err != nil {
		return err
	}
	v := &SigstoreVerifier{
		TrustedRootFile:           f.trustedRoot,
		CertificateIdentityRegexp: f.identity,
		CertificateOIDCIssuer:     f.issuer,
		RequireSignature:          true,
		TUFCacheDir:               f.tufCache,
	}
	if err := v.Verify(sig, digest, f.version); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "OK: bundle authenticates sha256:%x as a release artifact of %s\n", digest, f.version)
	return nil
}

// runFetchTrustedRoot fetches the Sigstore trusted root via TUF (online) and
// writes it as trusted_root.json, the file offline verification (and an
// in-cluster mirror without egress) needs.
func runFetchTrustedRoot(cmd *cobra.Command, out, tufCache string) error {
	if out == "" {
		return errors.New("--out is required")
	}
	opts := tuf.DefaultOptions()
	if tufCache != "" {
		opts = opts.WithCachePath(tufCache)
	}
	tr, err := root.FetchTrustedRootWithOptions(opts)
	if err != nil {
		return fmt.Errorf("fetch sigstore trusted root via TUF: %w", err)
	}
	data, err := tr.MarshalJSON()
	if err != nil {
		return fmt.Errorf("encode trusted root: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(out, data, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	fmt.Fprintf(cmd.OutOrStdout(), "trusted root written to %s (%d bytes, sha256:%x)\n", out, len(data), sum)
	return nil
}

// printHostStatus prints the installed collector versions and the supervisor
// link for `myriarch-updater status --config`.
func printHostStatus(cmd *cobra.Command, cfgPath string) error {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	l := &Layout{InstallDir: cfg.Collector.InstallDir, BinaryName: cfg.Collector.BinaryName, Activation: cfg.Collector.Activation}
	cur, err := l.CurrentVersion()
	if err != nil {
		return err
	}
	if cur == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "active collector version: none")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "active collector version: v%s\n", cur)
	}
	if vs, err := l.ListVersions(); err == nil {
		for _, v := range vs {
			fmt.Fprintf(cmd.OutOrStdout(), "  installed: v%s\n", v.Version)
		}
	}
	id, err := readSupervisorInstanceID(cfg.SupervisorStatePath())
	switch {
	case err != nil:
		fmt.Fprintf(cmd.OutOrStdout(), "supervisor instance uid: unreadable (%v)\n", err)
	case id == "":
		fmt.Fprintln(cmd.OutOrStdout(), "supervisor instance uid: not yet created")
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "supervisor instance uid: %s\n", id)
	}
	if j, err := readJournal(cfg.StateDir); err == nil && j != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "UNFINISHED UPDATE: %s -> %s (phase %s); it is repaired when the updater service starts\n", j.From, j.To, j.Phase)
	}
	return nil
}
