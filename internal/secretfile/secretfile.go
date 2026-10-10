// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package secretfile checks the permissions of the files that hold secrets:
// the credentials, keys and tokens that the server reads from paths in its
// configuration. At startup a file that other users of the host can read or
// write is reported with the setting and the path, never with its content.
//
// The package lists the secret files of the configuration (List); authn uses
// WarnLooseMode for the files it reads itself (the bootstrap admin and the
// declarative enrollment tokens), WarnLooseModes covers all the others.
package secretfile

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/myriarch/myriarch/internal/config"
)

// File is a configuration setting that points to a file with a secret.
type File struct {
	// Setting is the configuration key, for example "auth.oidc.clientSecretFile".
	Setting string
	// Path is the configured path.
	Path string
}

// WarnLooseMode logs a warning when a secret file can be read or written by
// "others" (mode bits 0007). The mounts of the Helm chart use 0440; hosts
// outside Kubernetes should use 0400 or 0440. Only the setting and the path
// are logged. File modes carry no meaning on Windows, where nothing is checked.
// A file that cannot be examined is skipped: the component that reads it
// reports the problem.
func WarnLooseMode(log *slog.Logger, setting, file string) {
	if runtime.GOOS == "windows" || file == "" {
		return
	}
	if fi, err := os.Stat(file); err == nil && fi.Mode().Perm()&0o007 != 0 {
		log.Warn("a credential file is accessible to other users; restrict it to the server account (mode 0400 or 0440)",
			"setting", setting, "file", file, "mode", fmt.Sprintf("%04o", fi.Mode().Perm()))
	}
}

// WarnLooseDir logs a warning when a directory that holds secret files can be written by group or
// others or read by others (mode bits 0022 and 0004). A directory with the sticky bit is not
// reported: Kubernetes mounts Secrets as such a directory on a tmpfs and the files in it are
// protected by their own mode. Only the setting and the path are logged. Nothing is checked on Windows.
func WarnLooseDir(log *slog.Logger, setting, dir string) {
	if runtime.GOOS == "windows" || dir == "" {
		return
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSticky != 0 {
		return
	}
	if fi.Mode().Perm()&0o026 != 0 || fi.Mode().Perm()&0o004 != 0 {
		log.Warn("a directory with secret files is writable by other users or readable by others; restrict it to the server account (mode 0750 or 0700)",
			"setting", setting, "file", dir, "mode", fmt.Sprintf("%04o", fi.Mode().Perm()))
	}
}

// WarnLooseModes calls WarnLooseMode for every file of List(cfg) and WarnLooseDir for
// executors.secretDir.
func WarnLooseModes(log *slog.Logger, cfg *config.Config) {
	for _, f := range List(cfg) {
		WarnLooseMode(log, f.Setting, f.Path)
	}
	WarnLooseDir(log, "executors.secretDir", cfg.Executors.SecretDir)
}

// List returns the secret files that the server reads with cfg: the *File
// settings that hold credentials, keys or tokens, for the features that are
// active (the rule that decides whether a file is read at all, see the package
// documentation of internal/config). Certificates, CA bundles and known-hosts
// files hold no secret and are not listed. The files of the bootstrap admin
// (auth.bootstrapAdmin.passwordHashFile and tokenFile) and of the declarative
// enrollment tokens (opamp.enrollmentTokens[].tokenFile) are not listed
// either: authn checks them when it reads them.
//
// A new *File setting must be added here or declared public in the test of
// this package (TestEveryFileSettingIsClassified), which reads the settings
// from the Config type.
func List(cfg *config.Config) []File {
	var out []File
	add := func(setting, path string) {
		if path != "" {
			out = append(out, File{Setting: setting, Path: path})
		}
	}
	add("database.dsnFile", cfg.Database.DSNFile)
	add("server.tls.keyFile", cfg.Server.TLS.KeyFile)
	add("auth.downloadTokenSecretFile", cfg.Auth.DownloadTokenSecretFile)
	if cfg.Auth.OIDC.Enabled {
		add("auth.oidc.clientSecretFile", cfg.Auth.OIDC.ClientSecretFile)
		add("auth.oidc.clientKeyFile", cfg.Auth.OIDC.ClientKeyFile)
	}
	if cfg.GitOps.Enabled {
		add("gitops.auth.tokenFile", cfg.GitOps.Auth.TokenFile)
		add("gitops.auth.sshKeyFile", cfg.GitOps.Auth.SSHKeyFile)
		add("gitops.webhookSecretFile", cfg.GitOps.WebhookSecretFile)
	}
	if cfg.Releases.Enabled {
		add("releases.tokenFile", cfg.Releases.TokenFile)
	}
	add("artifacts.sourceAuth.tokenFile", cfg.Artifacts.SourceAuth.TokenFile)
	add("artifacts.sourceAuth.passwordFile", cfg.Artifacts.SourceAuth.PasswordFile)
	for i, ch := range cfg.Notifications.Channels {
		add(fmt.Sprintf("notifications.channels[%d].urlFile", i), ch.URLFile)
		add(fmt.Sprintf("notifications.channels[%d].secretFile", i), ch.SecretFile)
	}
	names := make([]string, 0, len(cfg.Executors.Secrets))
	for name := range cfg.Executors.Secrets {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		path := cfg.Executors.Secrets[name].File
		if path != "" && !filepath.IsAbs(path) {
			// a relative name lies below executors.secretDir; without it the
			// reference is refused and nothing is read
			if cfg.Executors.SecretDir == "" {
				continue
			}
			path = filepath.Join(cfg.Executors.SecretDir, path)
		}
		add("executors.secrets."+name+".file", path)
	}
	// files below executors.secretDir that Git may reference (bound in executors.secretFileHosts)
	if cfg.Executors.SecretDir != "" {
		listed := map[string]bool{}
		for _, f := range out {
			listed[f.Path] = true
		}
		hostFiles := make([]string, 0, len(cfg.Executors.SecretFileHosts))
		for name := range cfg.Executors.SecretFileHosts {
			hostFiles = append(hostFiles, name)
		}
		slices.Sort(hostFiles)
		for _, name := range hostFiles {
			if filepath.IsAbs(name) {
				continue // refused by the executor: names must be relative
			}
			path := filepath.Join(cfg.Executors.SecretDir, name)
			if !listed[path] {
				add("executors.secretFileHosts."+name, path)
			}
		}
	}
	return out
}
