// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/config"
)

// run() logs the configuration warnings through logConfigWarnings: next to the
// warnings about weak settings it names every secret file that other users of
// the host can read, with the setting and the path and never the content.
func TestStartupWarnsAboutOpenSecretFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows: nothing is checked there")
	}
	const content = "postgres://myriarch:not-a-real-password@db.example.com:5432/myriarch"
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := config.Default()
	cfg.Database.DSNFile = write("dsn", 0o644)
	cfg.Server.TLS.KeyFile = write("tls.key", 0o440)
	cfg.Auth.DownloadTokenSecretFile = write("download", 0o666)

	var buf bytes.Buffer
	logConfigWarnings(slog.New(slog.NewTextHandler(&buf, nil)), &cfg, false)
	out := buf.String()
	for _, want := range []string{
		"a credential file is accessible to other users",
		"setting=database.dsnFile file=" + cfg.Database.DSNFile + " mode=0644",
		"setting=auth.downloadTokenSecretFile file=" + cfg.Auth.DownloadTokenSecretFile + " mode=0666",
		"server.trustedProxies is empty", // the other configuration warnings are still logged
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "server.tls.keyFile") {
		t.Errorf("a 0440 file must not warn:\n%s", out)
	}
	if strings.Contains(out, "not-a-real-password") {
		t.Errorf("the log contains file content:\n%s", out)
	}
}
