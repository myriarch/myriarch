// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package secretfile_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/secretfile"
)

// secretContent is written into every test file; no log line may contain it.
const secretContent = "s3cr3t-content-that-must-never-be-logged"

// The settings that List returns (with indexes and names replaced by [] and
// <name>), the files that authn checks itself, and the files that hold no
// secret. Every *File setting of config.Config belongs to exactly one list.
var (
	listedSettings = []string{
		"database.dsnFile",
		"server.tls.keyFile",
		"auth.downloadTokenSecretFile",
		"auth.oidc.clientSecretFile",
		"auth.oidc.clientKeyFile",
		"gitops.auth.tokenFile",
		"gitops.auth.sshKeyFile",
		"gitops.webhookSecretFile",
		"releases.tokenFile",
		"artifacts.sourceAuth.tokenFile",
		"artifacts.sourceAuth.passwordFile",
		"notifications.channels[].urlFile",
		"notifications.channels[].secretFile",
		"executors.secrets.<name>.file",
	}
	// listed in addition: files below executors.secretDir that are bound in executors.secretFileHosts
	// (a map of hosts, not a *File setting)
	listedFileHosts = []string{"executors.secretFileHosts.<name>"}
	checkedByAuthn  = []string{
		"auth.bootstrapAdmin.passwordHashFile",
		"auth.bootstrapAdmin.tokenFile",
		"opamp.enrollmentTokens[].tokenFile",
	}
	publicFiles = []string{
		"server.tls.certFile",
		"server.tls.clientCaFile",
		"auth.oidc.clientCertificateFile",
		"gitops.auth.knownHostsFile",
		"gitops.auth.caFile",
		"artifacts.verify.trustedRootFile",
		"outbound.caFile",
	}
)

var indexRE = regexp.MustCompile(`\[\d+\]`)

// normalize maps a setting of List to the form of listedSettings.
func normalize(setting string) string {
	setting = indexRE.ReplaceAllString(setting, "[]")
	if strings.HasPrefix(setting, "executors.secretFileHosts.") {
		return "executors.secretFileHosts.<name>"
	}
	if rest, ok := strings.CutPrefix(setting, "executors.secrets."); ok {
		return "executors.secrets.<name>." + rest[strings.LastIndex(rest, ".")+1:]
	}
	return setting
}

// fullConfig activates every feature and sets every secret file.
func fullConfig(dir string) *config.Config {
	p := func(name string) string { return filepath.Join(dir, name) }
	cfg := config.Default()
	cfg.Database.DSNFile = p("dsn")
	cfg.Server.TLS = config.TLSServerConfig{CertFile: p("tls.crt"), KeyFile: p("tls.key")}
	cfg.Auth.DownloadTokenSecretFile = p("download")
	cfg.Auth.OIDC = config.OIDCConfig{Enabled: true, ClientSecretFile: p("oidc-secret"), ClientKeyFile: p("oidc-key"), ClientCertificateFile: p("oidc.crt")}
	cfg.GitOps.Enabled = true
	cfg.GitOps.Auth = config.GitAuthConfig{TokenFile: p("git-token"), SSHKeyFile: p("git-key"), KnownHostsFile: p("known-hosts"), CAFile: p("git-ca")}
	cfg.GitOps.WebhookSecretFile = p("webhook")
	cfg.Releases.Enabled = true
	cfg.Releases.TokenFile = p("github-token")
	cfg.Artifacts.SourceAuth = config.ArtifactSourceAuth{TokenFile: p("mirror-token"), PasswordFile: p("mirror-password")}
	cfg.Notifications.Channels = []config.NotificationChannel{
		{Name: "a", URLFile: p("notify-a-url")},
		{Name: "b", URLFile: p("notify-b-url"), SecretFile: p("notify-b-secret")},
	}
	cfg.Executors.SecretDir = p("executors")
	cfg.Executors.Secrets = map[string]config.ExecutorSecret{
		"awx":     {File: "awx-token"},
		"webhook": {File: p("absolute-webhook")},
		"env":     {Env: "MYRIARCH_EXEC_X"},
	}
	cfg.Executors.SecretFileHosts = map[string][]string{"hook-secret": {"hooks.example.test"}, "awx-token": {"awx.example.test"}}
	return &cfg
}

func settingsOf(files []secretfile.File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Setting
	}
	return out
}

func TestListNamesEverySecretFileOfAnActiveConfiguration(t *testing.T) {
	dir := t.TempDir()
	got := secretfile.List(fullConfig(dir))
	want := []string{
		"database.dsnFile", "server.tls.keyFile", "auth.downloadTokenSecretFile",
		"auth.oidc.clientSecretFile", "auth.oidc.clientKeyFile",
		"gitops.auth.tokenFile", "gitops.auth.sshKeyFile", "gitops.webhookSecretFile",
		"releases.tokenFile",
		"artifacts.sourceAuth.tokenFile", "artifacts.sourceAuth.passwordFile",
		"notifications.channels[0].urlFile", "notifications.channels[1].urlFile", "notifications.channels[1].secretFile",
		"executors.secrets.awx.file", "executors.secrets.webhook.file",
		// "awx-token" is already listed through executors.secrets.awx.file
		"executors.secretFileHosts.hook-secret",
	}
	if !slices.Equal(settingsOf(got), want) {
		t.Fatalf("settings:\n got %v\nwant %v", settingsOf(got), want)
	}
	paths := map[string]string{}
	for _, f := range got {
		paths[f.Setting] = f.Path
	}
	for setting, want := range map[string]string{
		"database.dsnFile":                  filepath.Join(dir, "dsn"),
		"server.tls.keyFile":                filepath.Join(dir, "tls.key"),
		"notifications.channels[1].urlFile": filepath.Join(dir, "notify-b-url"),
		// a relative name lies below executors.secretDir, an absolute path is used as it is
		"executors.secrets.awx.file":     filepath.Join(dir, "executors", "awx-token"),
		"executors.secrets.webhook.file": filepath.Join(dir, "absolute-webhook"),
		// a file bound in executors.secretFileHosts lies below executors.secretDir
		"executors.secretFileHosts.hook-secret": filepath.Join(dir, "executors", "hook-secret"),
	} {
		if paths[setting] != want {
			t.Errorf("%s = %q, want %q", setting, paths[setting], want)
		}
	}
	// the list is what the classification test expects
	norm := map[string]bool{}
	for _, s := range settingsOf(got) {
		norm[normalize(s)] = true
	}
	for _, s := range listedSettings {
		if !norm[s] {
			t.Errorf("listedSettings names %s, but List does not return it", s)
		}
	}
	for _, s := range listedFileHosts {
		if !norm[s] {
			t.Errorf("listedFileHosts names %s, but List does not return it", s)
		}
	}
	if len(norm) != len(listedSettings)+len(listedFileHosts) {
		t.Errorf("List returns %d kinds of settings, the tests name %d", len(norm), len(listedSettings)+len(listedFileHosts))
	}
}

func TestListSkipsFeaturesThatAreOff(t *testing.T) {
	cfg := fullConfig(t.TempDir())
	cfg.Auth.OIDC.Enabled = false
	cfg.GitOps.Enabled = false
	cfg.Releases.Enabled = false
	cfg.Executors.SecretDir = "" // a relative file name is refused without it
	for _, s := range settingsOf(secretfile.List(cfg)) {
		for _, off := range []string{"auth.oidc.", "gitops.", "releases.", "executors.secrets.awx", "executors.secretFileHosts."} {
			if strings.HasPrefix(s, off) {
				t.Errorf("%s is listed although its feature is off", s)
			}
		}
	}
	if got := secretfile.List(&config.Config{}); len(got) != 0 {
		t.Errorf("an empty configuration lists %v", got)
	}
}

// A new *File setting in config.Config must be classified: listed in List,
// checked by authn, or public (no secret).
func TestEveryFileSettingIsClassified(t *testing.T) {
	var found []string
	collectFileSettings(reflect.TypeOf(config.Config{}), "", &found)
	if len(found) < 20 {
		t.Fatalf("only %d file settings found: %v", len(found), found)
	}
	classes := map[string]string{}
	for class, list := range map[string][]string{"listed": listedSettings, "checked by authn": checkedByAuthn, "public": publicFiles} {
		for _, s := range list {
			if prev, dup := classes[s]; dup {
				t.Errorf("%s is both %s and %s", s, prev, class)
			}
			classes[s] = class
		}
	}
	for _, s := range found {
		if classes[s] == "" {
			t.Errorf("file setting %s is not classified: add it to secretfile.List if it holds a secret, otherwise to publicFiles in this test", s)
		}
		delete(classes, s)
	}
	for s, class := range classes {
		t.Errorf("%s is listed as %s, but config.Config has no such file setting", s, class)
	}
}

// collectFileSettings appends the configuration key of every string field
// whose key ends in "File" (or is "file"): slices appear as [], maps as .<name>.
func collectFileSettings(t reflect.Type, path string, out *[]string) {
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if key == "" || key == "-" {
				continue
			}
			p := key
			if path != "" {
				p = path + "." + key
			}
			if f.Type.Kind() == reflect.String && (strings.HasSuffix(key, "File") || key == "file") {
				*out = append(*out, p)
				continue
			}
			collectFileSettings(f.Type, p, out)
		}
	case reflect.Slice:
		collectFileSettings(t.Elem(), path+"[]", out)
	case reflect.Map:
		collectFileSettings(t.Elem(), path+".<name>", out)
	}
}

// logLine is one decoded warning.
type logLine struct {
	Msg     string `json:"msg"`
	Level   string `json:"level"`
	Setting string `json:"setting"`
	File    string `json:"file"`
	Mode    string `json:"mode"`
}

func decodeLog(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()
	var out []logLine
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var l logLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, l)
	}
	return out
}

// write creates a file with the secret content and exactly the given mode.
func write(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path) // an earlier call may have left a read-only file
	if err := os.WriteFile(path, []byte(secretContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // the umask does not apply to chmod
		t.Fatal(err)
	}
	return path
}

func TestWarnLooseModesNamesTheSettingAndNeverTheContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows: nothing is checked there")
	}
	dir := t.TempDir()
	cfg := fullConfig(dir)
	// every secret file exists with a tight mode ...
	for _, f := range secretfile.List(cfg) {
		write(t, f.Path, 0o440)
	}
	if err := os.Chmod(cfg.Executors.SecretDir, 0o750); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	secretfile.WarnLooseModes(log, cfg)
	if buf.Len() != 0 {
		t.Fatalf("0440 files must not warn: %s", buf.String())
	}

	// ... and a few are open to others (read, write, or both), one only to the group
	loose := map[string]os.FileMode{
		"database.dsnFile":                  0o644,
		"server.tls.keyFile":                0o604,
		"auth.oidc.clientKeyFile":           0o666,
		"gitops.auth.sshKeyFile":            0o602,
		"notifications.channels[1].urlFile": 0o644,
		"executors.secrets.awx.file":        0o644,
	}
	for _, f := range secretfile.List(cfg) {
		if mode, ok := loose[f.Setting]; ok {
			write(t, f.Path, mode)
		}
	}
	write(t, filepath.Join(dir, "download"), 0o640) // group access is not "others"
	secretfile.WarnLooseModes(log, cfg)

	got := map[string]logLine{}
	for _, l := range decodeLog(t, &buf) {
		if l.Level != "WARN" || !strings.Contains(l.Msg, "accessible to other users") {
			t.Errorf("unexpected log line: %+v", l)
		}
		got[l.Setting] = l
	}
	if len(got) != len(loose) {
		t.Errorf("%d settings warned, want %d: %v", len(got), len(loose), got)
	}
	paths := map[string]string{}
	for _, f := range secretfile.List(cfg) {
		paths[f.Setting] = f.Path
	}
	for setting, mode := range loose {
		l, ok := got[setting]
		if !ok {
			t.Errorf("no warning for %s", setting)
			continue
		}
		if l.File != paths[setting] || l.Mode != fmt.Sprintf("%04o", mode) {
			t.Errorf("%s: file=%q mode=%q, want file=%q mode=%04o", setting, l.File, l.Mode, paths[setting], mode)
		}
	}
	if strings.Contains(buf.String(), secretContent) {
		t.Fatal("the log contains file content")
	}
}

func TestWarnLooseModesOnlyForTheFilesTheServerReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows")
	}
	dir := t.TempDir()
	cfg := fullConfig(dir)
	for _, f := range secretfile.List(cfg) {
		write(t, f.Path, 0o644)
	}
	// files of features that are off are not read, and files that do not exist are the concern of their reader
	cfg.Auth.OIDC.Enabled = false
	cfg.GitOps.Enabled = false
	cfg.Releases.Enabled = false
	if err := os.Remove(cfg.Database.DSNFile); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.DownloadTokenSecretFile = filepath.Join(dir, "does-not-exist")

	var buf bytes.Buffer
	secretfile.WarnLooseModes(slog.New(slog.NewJSONHandler(&buf, nil)), cfg)
	for _, l := range decodeLog(t, &buf) {
		for _, off := range []string{"auth.oidc.", "gitops.", "releases.", "database.", "auth.downloadTokenSecretFile"} {
			if strings.HasPrefix(l.Setting, off) {
				t.Errorf("warning for %s although the file is not read or not there", l.Setting)
			}
		}
	}
	if !strings.Contains(buf.String(), `"setting":"server.tls.keyFile"`) {
		t.Errorf("the files that are read still warn: %s", buf.String())
	}
}

// The helper that authn uses for the bootstrap and enrollment token files has
// the same message format.
func TestWarnLooseModeHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows")
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	secretfile.WarnLooseMode(log, "x.tokenFile", "")
	secretfile.WarnLooseMode(log, "x.tokenFile", filepath.Join(t.TempDir(), "missing"))
	secretfile.WarnLooseMode(log, "x.tokenFile", write(t, filepath.Join(t.TempDir(), "tight"), 0o400))
	if buf.Len() != 0 {
		t.Fatalf("no warning expected: %s", buf.String())
	}
	file := write(t, filepath.Join(t.TempDir(), "open"), 0o644)
	secretfile.WarnLooseMode(log, "x.tokenFile", file)
	lines := decodeLog(t, &buf)
	if len(lines) != 1 || lines[0].Setting != "x.tokenFile" || lines[0].File != file || lines[0].Mode != "0644" {
		t.Fatalf("log: %+v", lines)
	}
}

// executors.secretDir itself must not be writable by group or others or readable by others.
func TestWarnLooseModesChecksTheSecretDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows: nothing is checked there")
	}
	for name, tc := range map[string]struct {
		mode os.FileMode
		warn bool
	}{
		"private":                {0o750, false},
		"private read only":      {0o550, false},
		"group writable":         {0o770, true},
		"world readable":         {0o755, true},
		"world writable":         {0o777, true},
		"world writable sticky":  {0o777 | os.ModeSticky, false}, // a tmpfs secret mount
		"owner only":             {0o700, false},
		"group readable":         {0o750, false},
		"others can only search": {0o751, false},
	} {
		dir := filepath.Join(t.TempDir(), "secrets")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, tc.mode); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{}
		cfg.Executors.SecretDir = dir
		var buf bytes.Buffer
		secretfile.WarnLooseModes(slog.New(slog.NewJSONHandler(&buf, nil)), cfg)
		lines := decodeLog(t, &buf)
		if tc.warn != (len(lines) == 1) {
			t.Errorf("%s (%v): warnings %v, want warn=%v", name, tc.mode, lines, tc.warn)
			continue
		}
		if tc.warn && (lines[0].Setting != "executors.secretDir" || lines[0].File != dir) {
			t.Errorf("%s: %+v", name, lines[0])
		}
	}
}
