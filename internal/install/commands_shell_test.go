// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package install

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/testutil/linuxhost"
)

// These tests run the command for Linux in a real shell on a host that internal/testutil/linuxhost builds: stand-ins
// for sudo, id and mktemp (the tests are not root) and, unless a test says otherwise, for curl. The installer in the
// bundle writes down how it was called. They check what the command does and what it leaves behind, and that the token
// is in no argument vector, no environment and no file once the command has ended.

// shellCase is a host with a bundle, and the means to render the command for that bundle.
type shellCase struct {
	t      *testing.T
	host   *linuxhost.Host
	bundle linuxhost.Bundle
	name   string
}

func newShellCase(t *testing.T, shell string) *shellCase {
	t.Helper()
	linuxhost.SkipWithoutShell(t)
	b := linuxhost.NewBundle(t, "")
	return &shellCase{t: t, host: linuxhost.New(t, linuxhost.Options{Shell: shell, Bundle: b}), bundle: b, name: BundleFilename("0.2.0", Platform{"linux", "amd64"})}
}

// command renders the command for the bundle of the host and puts the token in.
func (c *shellCase) command(req CommandsRequest, token string, mut ...func(d *Deps, s *stubBundles)) string {
	c.t.Helper()
	e := newCommandsEnv(c.t, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "linux" && s.list.Items[i].Arch == "amd64" {
				s.list.Items[i].Filename, s.list.Items[i].SHA256 = c.name, c.bundle.SHA256
			}
		}
		for _, m := range mut {
			m(d, s)
		}
	})
	req.Platforms = []string{"linux/amd64"}
	cmds, err := e.render(req)
	require.NoError(c.t, err)
	require.Len(c.t, cmds.Commands, 1)
	return strings.ReplaceAll(cmds.Commands[0].Command, brand.EnrollmentTokenPlaceholder, token)
}

func withHash(hash string) func(d *Deps, s *stubBundles) {
	return func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "linux" && s.list.Items[i].Arch == "amd64" {
				s.list.Items[i].SHA256 = hash
			}
		}
	}
}

func TestLinuxCommandInstallsTheHost(t *testing.T) {
	for _, sh := range linuxhost.Shells() {
		t.Run(linuxhost.ShellName(sh), func(t *testing.T) {
			c := newShellCase(t, sh)
			h := c.host
			r := h.Run(c.command(fullRequest(VariantTokenInline), testToken))
			require.Equal(t, 0, r.Code, r.Out)

			// the installer ran from the unpacked bundle, with the token file of the private directory
			args := h.Argv("installer")
			require.GreaterOrEqual(t, len(args), 6, r.Out)
			assert.Equal(t, "--server", args[0])
			assert.Equal(t, "https://fleet.example.com", args[1])
			assert.Equal(t, "--token-file", args[2])
			assert.True(t, strings.HasPrefix(args[3], filepath.Join(h.Root, "root", brand.InstallSetupDir+".")), args[3])
			assert.True(t, strings.HasSuffix(args[3], "/enrollment-token"))
			assert.Equal(t, []string{"--ca-file", "/etc/ssl/myriarch-ca.pem", "--fleet", "linux-hosts", "--collector-version", "0.162.0", "--site-type", "edge", "--site-id", "042",
				"--site-name", "Edge site 42 (north)", "--site-country", "NL", "--site-region", "north", "--site-timezone", "Europe/Amsterdam", "--environment", "prod"}, args[4:])
			assert.Equal(t, testToken+"\n", h.Log("installer.token"), "the file holds the token")
			assert.Equal(t, "600\n", h.Log("installer.tokenmode"), "and only its owner can read it")
			assert.Contains(t, r.Out, "the installer ran")

			// curl: https only, no redirects, no globbing, a header from the standard input, the file in the private directory
			curl := h.Argv("curl")
			assert.Equal(t, []string{"-fsSg", "--retry", "3", "--proto", "=https", "--tlsv1.2", "--cacert", "/etc/ssl/myriarch-ca.pem", "-K", "-", "-o"}, curl[:11])
			assert.True(t, strings.HasSuffix(curl[11], "/"+c.name))
			assert.Equal(t, "https://fleet.example.com/api/v1/install/bundles/"+c.name, curl[12])
			assert.NotContains(t, curl, "-L")
			assert.Equal(t, "header = \"Authorization: Bearer "+testToken+"\"\n", h.Log("curl.stdin"))

			// sudo got the script and the values, not the token; mktemp was asked for a directory of root
			assert.Equal(t, "sh", h.Argv("sudo")[0])
			assert.Equal(t, []string{"-d", "/root/" + brand.InstallSetupDir + ".XXXXXX"}, h.Argv("mktemp"))

			// and nothing is left: not the directory, not the token
			assert.Empty(t, h.PrivateDirs())
			h.AssertTokenNowhere(testToken, r.Out)
		})
	}
}

func TestLinuxCommandStopsWhenTheHashIsNotTheOneInTheCommand(t *testing.T) {
	c := newShellCase(t, "")
	r := c.host.Run(c.command(site(), testToken, withHash(strings.Repeat("0", 64))))
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Out, "[myriarch-install] ERROR [checksum_mismatch]")
	assert.Contains(t, r.Out, "nothing was unpacked")
	assert.Empty(t, c.host.Log("installer.argv"), "the installer never ran")
	assert.Empty(t, c.host.PrivateDirs(), "nothing is left")
	c.host.AssertTokenNowhere(testToken, r.Out)
}

func TestLinuxCommandStopsWithoutAUsableToken(t *testing.T) {
	cases := map[string]struct{ token, code string }{
		"nothing":                {"", "option_missing"},
		"the placeholder":        {brand.EnrollmentTokenPlaceholder, "option_missing"},
		"a space inside":         {"myre_abc def", "option_invalid"},
		"a quote":                {"myre_abc'def", "option_invalid"},
		"a dollar":               {"myre_$(id)", "option_invalid"},
		"a semicolon":            {"myre_abc;id", "option_invalid"},
		"a backtick":             {"myre_`id`", "option_invalid"},
		"a carriage return":      {"myre_abc\r", "option_invalid"},
		"a double quote":         {"\"myre_abc\"", "option_invalid"},
		"a backslash":            {`myre_abc\n`, "option_invalid"},
		"an asterisk":            {"myre_*", "option_invalid"},
		"the placeholder inside": {"myre_@@abc", "option_missing"},
		"a unicode quote":        {"myre_’abc", "option_invalid"},
		"a tab":                  {"myre_abc\tdef", "option_invalid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newShellCase(t, "")
			r := c.host.Run(c.command(site(), tc.token))
			assert.Equal(t, 1, r.Code, r.Out)
			assert.Contains(t, r.Out, "[myriarch-install] ERROR ["+tc.code+"]")
			assert.Empty(t, c.host.Argv("curl"), "nothing is downloaded with it")
			assert.Empty(t, c.host.Log("installer.argv"))
			assert.Empty(t, c.host.PrivateDirs())
			if tc.token != "" && tc.token != brand.EnrollmentTokenPlaceholder {
				assert.NotContains(t, r.Out, tc.token, "the message does not repeat it")
			}
		})
	}
}

func TestLinuxCommandUsesTheFirstLineOfTheHereDocumentOnly(t *testing.T) {
	c := newShellCase(t, "")
	marker := filepath.Join(t.TempDir(), "pwned")
	r := c.host.Run(c.command(site(), testToken+"\nid >"+marker+"\nmyre_other"))
	require.Equal(t, 0, r.Code, r.Out)
	assert.Equal(t, testToken+"\n", c.host.Log("installer.token"))
	assert.NoFileExists(t, marker)
}

func TestLinuxCommandNeedsRoot(t *testing.T) {
	c := newShellCase(t, "")
	r := c.host.Run(c.command(site(), testToken), "FAKE_UID=1000")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Out, "[myriarch-install] ERROR [privilege_required]")
	assert.Empty(t, c.host.Argv("mktemp"), "it does nothing before it knows that it is root")
	assert.Empty(t, c.host.Argv("curl"))
}

func TestLinuxCommandReportsADownloadThatFails(t *testing.T) {
	c := newShellCase(t, "")
	r := c.host.Run(c.command(site(), testToken), "CURL_FAIL=1")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Out, "[myriarch-install] ERROR [download_failed]")
	assert.Contains(t, r.Out, "https://fleet.example.com")
	assert.Empty(t, c.host.Log("installer.argv"))
	assert.Empty(t, c.host.PrivateDirs())
	c.host.AssertTokenNowhere(testToken, r.Out)
}

func TestLinuxCommandKeepsTheExitCodeOfTheInstallerAndCleansUp(t *testing.T) {
	c := newShellCase(t, "")
	r := c.host.Run(c.command(site(), testToken), "INSTALLER_EXIT=3")
	assert.Equal(t, 3, r.Code, "the status of the installer is the status of the command")
	assert.Empty(t, c.host.PrivateDirs(), "the private directory goes with the token file, whatever the installer did")
	c.host.AssertTokenNowhere(testToken, r.Out)
}

func TestLinuxCommandStopsWhenTheArchiveCannotBeUnpacked(t *testing.T) {
	c := newShellCase(t, "")
	bad := linuxhost.Bundle{Archive: []byte("this is not a gzip file")}
	bad.SHA256 = linuxhost.SHA256Hex(bad.Archive)
	c.host.SetBundle(bad)
	r := c.host.Run(c.command(site(), testToken, withHash(bad.SHA256)))
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Out, "[myriarch-install] ERROR [archive_invalid]")
	assert.Empty(t, c.host.PrivateDirs())
}

func TestLinuxCommandCleansUpWhenInterrupted(t *testing.T) {
	linuxhost.SkipWithoutShell(t)
	// the installer waits; the test interrupts the process group, as Ctrl-C does
	slow := linuxhost.NewBundle(t, "#!/usr/bin/env bash\necho started > \"$LOGDIR/installer.started\"\nsleep 30\n")
	c := &shellCase{t: t, host: linuxhost.New(t, linuxhost.Options{Bundle: slow}), bundle: slow, name: BundleFilename("0.2.0", Platform{"linux", "amd64"})}
	cmd, out := c.host.Start(c.command(site(), testToken))
	require.Eventually(t, func() bool { return c.host.Log("installer.started") != "" }, 20*time.Second, 20*time.Millisecond, out.String())
	require.NotEmpty(t, c.host.PrivateDirs())
	require.NoError(t, linuxhost.Interrupt(cmd))
	_ = cmd.Wait()
	require.Eventually(t, func() bool { return len(c.host.PrivateDirs()) == 0 }, 20*time.Second, 20*time.Millisecond, "the private directory is removed after an interrupt")
	c.host.AssertTokenNowhere(testToken, out.String())
}

func TestLinuxCommandReportsAMissingTool(t *testing.T) {
	c := newShellCase(t, "")
	// a PATH with the stand-ins and a few tools, but no gzip
	only := t.TempDir()
	for _, tool := range []string{"bash", "sh", "tr", "rm", "cat", "tar", "sha256sum", "env", "pwd", "dirname", "stat", "cp", "mkdir", "uname"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(only, tool))
		}
	}
	for _, stub := range []string{"sudo", "id", "mktemp", "curl", "sha256sum"} {
		if _, err := os.Stat(filepath.Join(c.host.Bin(), stub)); err == nil {
			_ = os.Remove(filepath.Join(only, stub))
			require.NoError(t, os.Symlink(filepath.Join(c.host.Bin(), stub), filepath.Join(only, stub)))
		}
	}
	c.host.WithPath(only)
	r := c.host.Run(c.command(site(), testToken))
	assert.Equal(t, 1, r.Code, r.Out)
	assert.Contains(t, r.Out, "[myriarch-install] ERROR [tool_missing] gzip is required")
	assert.Empty(t, c.host.Argv("mktemp"))
}

func TestLinuxCommandReadsTheTokenFromAFile(t *testing.T) {
	for name, content := range map[string]string{"plain": testToken, "newline": testToken + "\n", "crlf": testToken + "\r\n", "spaces": "  " + testToken + "  \n\n"} {
		t.Run(name, func(t *testing.T) {
			c := newShellCase(t, "")
			h := c.host
			tokenFile := filepath.Join(h.Root, "etc-token")
			require.NoError(t, os.WriteFile(tokenFile, []byte(content), 0o600))
			req := fullRequest(VariantTokenFile)
			req.TokenFiles = &TokenFiles{Linux: tokenFile}
			r := h.Run(c.command(req, ""))
			require.Equal(t, 0, r.Code, r.Out)
			args := h.Argv("installer")
			assert.Equal(t, []string{"--server", "https://fleet.example.com", "--token-file", tokenFile}, args[:4], "the installer reads the file of the operator")
			assert.Equal(t, "header = \"Authorization: Bearer "+testToken+"\"\n", h.Log("curl.stdin"))
			assert.Equal(t, content, h.Log("installer.token"), "the file is as the operator left it")
			assert.FileExists(t, tokenFile, "and it is still there")
			assert.Empty(t, h.PrivateDirs())
			for _, a := range h.Argv("curl") {
				assert.NotContains(t, a, testToken)
			}
		})
	}
}

func TestLinuxCommandWithAFileNeedsAUsableFile(t *testing.T) {
	t.Run("a file that is not there", func(t *testing.T) {
		c := newShellCase(t, "")
		req := fullRequest(VariantTokenFile)
		req.TokenFiles = &TokenFiles{Linux: filepath.Join(c.host.Root, "missing")}
		r := c.host.Run(c.command(req, ""))
		assert.Equal(t, 1, r.Code)
		assert.Contains(t, r.Out, "[myriarch-install] ERROR [file_unreadable]")
		assert.Empty(t, c.host.Argv("curl"))
	})
	for name, content := range map[string]string{
		"empty": "", "a semicolon": "myre_abc;id", "a quote": "\"myre_abc\"", "the placeholder": brand.EnrollmentTokenPlaceholder, "a command": "$(id)",
	} {
		t.Run(name, func(t *testing.T) {
			c := newShellCase(t, "")
			tokenFile := filepath.Join(c.host.Root, "token")
			require.NoError(t, os.WriteFile(tokenFile, []byte(content), 0o600))
			req := fullRequest(VariantTokenFile)
			req.TokenFiles = &TokenFiles{Linux: tokenFile}
			r := c.host.Run(c.command(req, ""))
			assert.Equal(t, 1, r.Code, r.Out)
			assert.Contains(t, r.Out, "[myriarch-install] ERROR [option_")
			assert.Empty(t, c.host.Argv("curl"), "nothing is downloaded with it")
			assert.Empty(t, c.host.Log("installer.argv"))
			assert.FileExists(t, tokenFile, "the file of the operator is never touched")
			if content != "" {
				assert.NotContains(t, r.Out, content, "the message does not repeat it")
			}
		})
	}
	t.Run("a directory is not a file", func(t *testing.T) {
		c := newShellCase(t, "")
		req := fullRequest(VariantTokenFile)
		req.TokenFiles = &TokenFiles{Linux: c.host.Root}
		r := c.host.Run(c.command(req, ""))
		assert.Contains(t, r.Out, "[myriarch-install] ERROR [file_unreadable]")
	})
}

func TestLinuxCommandOfTheLabVariant(t *testing.T) {
	c := newShellCase(t, "")
	req := site(func(r *CommandsRequest) { r.Variant = VariantLab })
	r := c.host.Run(c.command(req, testToken, func(d *Deps, s *stubBundles) { d.DevMode, d.PublicURL = true, "http://localhost:8080" }))
	require.Equal(t, 0, r.Code, r.Out)
	curl := c.host.Argv("curl")
	assert.Equal(t, []string{"--proto", "=http,https"}, curl[3:5])
	assert.Equal(t, "http://localhost:8080/api/v1/install/bundles/"+c.name, curl[len(curl)-1])
	args := c.host.Argv("installer")
	assert.Equal(t, "--insecure", args[len(args)-1])
	assert.Equal(t, "http://localhost:8080", args[1])
	c.host.AssertTokenNowhere(testToken, r.Out)
}

// shellcheck has to be content with the script, as it is for the scripts of the repository.
func TestLinuxScriptPassesShellcheck(t *testing.T) {
	path, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not installed")
	}
	script := "#!/bin/sh\n" + strings.TrimPrefix(scriptMarkers.Replace(linuxScript), "\n")
	file := filepath.Join(t.TempDir(), "setup.sh")
	require.NoError(t, os.WriteFile(file, []byte(script), 0o644))
	out, err := exec.Command(path, "-s", "sh", file).CombinedOutput()
	assert.NoError(t, err, string(out))
}

// The real curl, against a TLS server with its own CA, with the real command: the configuration that carries the
// header, the options and the certificate check work as the command expects.
func TestLinuxCommandWithTheRealCurl(t *testing.T) {
	c := newShellCase(t, "")
	c.host.UseRealCurl()
	var mu sync.Mutex
	var gotAuth, gotPath, gotUA string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth, gotPath, gotUA = r.Header.Get("Authorization"), r.URL.Path, r.Header.Get("User-Agent")
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(c.bundle.Archive)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile := filepath.Join(c.host.Root, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644))

	req := site(func(r *CommandsRequest) { r.CAFile = caFile })
	r := c.host.Run(c.command(req, testToken, func(d *Deps, s *stubBundles) { d.PublicURL = srv.URL }))
	if r.Code != 0 && strings.Contains(r.Out, "certificate") && runtime.GOOS == "darwin" {
		t.Skipf("the curl of this system does not use --cacert: %s", r.Out)
	}
	require.Equal(t, 0, r.Code, r.Out)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "Bearer "+testToken, gotAuth, "the header came through the configuration on the standard input")
	assert.Equal(t, "/api/v1/install/bundles/"+c.name, gotPath)
	assert.NotEmpty(t, gotUA)
	assert.Equal(t, testToken+"\n", c.host.Log("installer.token"))
	assert.Empty(t, c.host.PrivateDirs())

	// without the CA the certificate check refuses the server, and nothing is installed
	c2 := newShellCase(t, "")
	c2.host.UseRealCurl()
	r = c2.host.Run(c2.command(site(), testToken, func(d *Deps, s *stubBundles) { d.PublicURL = srv.URL }))
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Out, "[myriarch-install] ERROR [download_failed]")
	assert.Empty(t, c2.host.Log("installer.argv"))
}
