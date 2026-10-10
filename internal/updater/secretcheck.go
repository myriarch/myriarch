// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"log/slog"
	"os"
	"runtime"
)

// warnLooseSecrets reports files that hold the enrollment token and that other accounts can use:
// server.tokenFile writable by group or others, readable by others, or owned by neither root nor the
// account of the updater, and server.token set inline in a configuration file with such a mode. Only
// the setting and the path are logged, never the content. Group read access is accepted (Kubernetes
// mounts secrets with fsGroup and mode 0440). Nothing is checked on Windows.
func warnLooseSecrets(log *slog.Logger, cfg Config) {
	if runtime.GOOS == "windows" {
		return
	}
	if cfg.Server.TokenFile != "" {
		warnLooseFile(log, "server.tokenFile", cfg.Server.TokenFile, true)
	}
	if cfg.Server.Token != "" && cfg.Path != "" {
		warnLooseFile(log, "server.token", cfg.Path, false)
	}
}

func warnLooseFile(log *slog.Logger, setting, path string, checkOwner bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return // the component that reads the file reports the problem
	}
	if fi.Mode().Perm()&0o027 != 0 {
		log.Warn("a file with the enrollment token is accessible to other accounts; restrict it to root and the service account (mode 0600 or 0640)",
			"setting", setting, "file", path, "mode", fi.Mode().Perm().String())
		return
	}
	if checkOwner {
		if uid, ok := fileOwner(fi); ok && uid != 0 && uid != uint32(os.Geteuid()) {
			log.Warn("the enrollment token file is owned by an account other than root and the service account",
				"setting", setting, "file", path, "owner", uid)
		}
	}
}
