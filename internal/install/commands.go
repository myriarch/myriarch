// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
)

// The install commands (ADR-0046, docs/design/opamp-server.md, "Installation from the server"): the text that a person
// copies to a host. It downloads the installer bundle of the server, checks the SHA-256 that the command pins, unpacks
// the bundle in a private directory and starts the installer from it.
//
// The server renders the command and never sees a token. The variant token_inline has the placeholder
// brand.EnrollmentTokenPlaceholder where the token goes; the web UI replaces it in the browser with the token that
// the person created or pasted. What the command does with the token:
//
//   - it reaches the host as text of the command and is read by a builtin (read, printf; Set-Content), never from an
//     argument vector, an environment variable or a URL; on Linux it comes through the standard input of the shell
//     that runs as root (a quoted here document), on Windows through a parameter of a script block;
//   - it is written to a file in a private directory (mode 0600 below the home directory of root on Linux; a directory
//     below %ProgramData% whose DACL admits SYSTEM and the administrators only, set before the first download), is
//     passed to the installer with --token-file (-TokenFile) and is deleted with the directory after the run;
//   - it leaves the host as a request header only (curl -K - reads the header from standard input; Invoke-WebRequest
//     -Headers).
//
// The text of the command is a constant: every value (server, bundle, hash, options) travels as an argument that is
// quoted on its own, so no value is ever pasted into code. Values are checked against allowlists first (printable
// ASCII subset, lengths), and a value with a quotation mark of any kind (the ASCII ones, the typographic ones of
// U+2018 to U+201E that PowerShell treats as quotes, the backtick), a dollar sign, a semicolon or a control character
// is refused, so the quoting needs no escape. The Windows command uses only cmdlets and native programs, because under
// App Control the typed command runs in Constrained Language Mode.

// Variants of the command.
const (
	// VariantTokenInline: the token is part of the command (the placeholder).
	VariantTokenInline = "token_inline"
	// VariantTokenFile: the token is in a file on the host, whose path the person gives.
	VariantTokenFile = "token_file"
	// VariantLab: like token_inline for a development server over plain http, with --insecure for the installer.
	// Only a development server renders it.
	VariantLab = "lab"
)

// Shells of the commands.
const (
	ShellBash       = "bash"
	ShellPowerShell = "powershell"
)

// CommandsRequest is the body of the request for install commands. It holds no secret.
type CommandsRequest struct {
	// Variant is VariantTokenInline (the default), VariantTokenFile or VariantLab.
	Variant string `json:"variant,omitempty"`
	// Platforms are the platforms to render a command for, as "os/arch". Without any, all of install.platforms.
	Platforms []string `json:"platforms,omitempty"`
	// Fleet is the fleet that the person expects; the server of the installation decides and refuses a host that it
	// assigns elsewhere (fleet_mismatch).
	Fleet string `json:"fleet,omitempty"`
	// Attributes are the site attributes of the host: site.type and site.id are required (the installers need them),
	// site.name, site.country, site.region, site.timezone and deployment.environment.name are optional.
	Attributes map[string]string `json:"attributes,omitempty"`
	// CollectorVersion is a version that the host asks for instead of the target of its fleet.
	CollectorVersion string `json:"collectorVersion,omitempty"`
	// TokenFiles are the paths of the token file on the host (VariantTokenFile only), by operating system.
	TokenFiles *TokenFiles `json:"tokenFiles,omitempty"`
	// CAFile is the path of the PEM file with the CA of the server on a Linux host. Windows uses the certificate store
	// of the machine.
	CAFile string `json:"caFile,omitempty"`
}

// TokenFiles are the paths of the token file on the host, one for each operating system.
type TokenFiles struct {
	Linux   string `json:"linux,omitempty"`
	Windows string `json:"windows,omitempty"`
}

// Commands are the commands for the platforms that were asked for.
type Commands struct {
	Variant string `json:"variant"`
	// Server is the address of the server in the commands (server.publicUrl).
	Server string `json:"server"`
	// Version is the version of the installer bundles.
	Version string `json:"version"`
	// Insecure tells that the commands lower the checks of the installer for a development server (VariantLab).
	Insecure bool `json:"insecure"`
	// TokenPlaceholder is the text in each command that stands for the enrollment token: the person (the web UI)
	// replaces it, in each command, with the token. The commands of VariantTokenFile have none.
	TokenPlaceholder string    `json:"tokenPlaceholder,omitempty"`
	Commands         []Command `json:"commands"`
}

// Command is the command for one platform.
type Command struct {
	// Platform is "os/arch".
	Platform string `json:"platform"`
	// Shell is ShellBash or ShellPowerShell: the shell that the command is for, run with the rights of root or of the
	// administrators.
	Shell   string `json:"shell"`
	Command string `json:"command"`
	// Bundle is the installer bundle whose SHA-256 the command pins.
	Bundle Bundle `json:"bundle"`
	// TokenFile is the file with the token that the command reads (VariantTokenFile only).
	TokenFile string `json:"tokenFile,omitempty"`
}

// BundleStore is what the commands and the delivery need from the installer bundles. *Bundles is the implementation.
type BundleStore interface {
	State() BundleList
	Find(p Platform) (Bundle, bool)
	Prepare()
	Open(filename string) (*os.File, Bundle, error)
}

// maxCommandPlatforms bounds the list of platforms of a request.
const maxCommandPlatforms = 8

// RenderCommands renders the install commands: the request is checked against allowlists (422), the bundles of the
// platforms have to be ready, and the server has to be reachable over https (the commands carry a token), unless the
// request is for the variant lab of a development server.
func (p *Planner) RenderCommands(req CommandsRequest) (*Commands, error) {
	in, err := p.checkCommands(&req)
	if err != nil {
		return nil, err
	}
	if p.d.Bundles == nil {
		return nil, refuse(domain.ErrPrecondition, CodeLocalBuild, "this server has no installer bundles")
	}
	// Asking for commands is what makes the server look at its bundles: it starts the preparation of a bundle that is
	// pending, or failed a minute ago, and a development server reads its directory again.
	p.d.Bundles.Prepare()
	st := p.d.Bundles.State()
	if st.Source == BundleSourceNone {
		return nil, refuse(domain.ErrPrecondition, CodeLocalBuild,
			"this server is a local build and not a release of the project, so it has no installer bundle to hand out: install the host on the manual path, or run a release (a development server can serve a directory of bundles with --install-bundle-dir)")
	}
	in.server, err = commandServer(p.d.PublicURL, in.variant != VariantLab)
	if err != nil {
		return nil, refuse(domain.ErrPrecondition, CodeServerSettings, "%v", err)
	}
	out := &Commands{Variant: in.variant, Server: in.server, Version: st.Version, Insecure: in.variant == VariantLab}
	if in.variant != VariantTokenFile {
		out.TokenPlaceholder = brand.EnrollmentTokenPlaceholder
	}
	var waiting, failed []Bundle
	bundles := make([]Bundle, 0, len(in.platforms))
	for _, plat := range in.platforms {
		b, ok := p.d.Bundles.Find(plat)
		switch {
		case !ok:
			failed = append(failed, Bundle{OS: plat.OS, Arch: plat.Arch, Error: "the server has no bundle for this platform"})
		case b.Status == BundleReady:
			bundles = append(bundles, b)
		case b.Status == BundleFailed:
			failed = append(failed, b)
		default:
			waiting = append(waiting, b)
		}
	}
	if len(failed) > 0 {
		return nil, bundleFailedRefusal(failed[0])
	}
	if len(waiting) > 0 {
		return nil, bundlePreparingRefusal()
	}
	for _, b := range bundles {
		cmd, err := in.render(b)
		if err != nil {
			return nil, err
		}
		out.Commands = append(out.Commands, cmd)
	}
	return out, nil
}

// commandInput is a checked request.
type commandInput struct {
	variant   string
	server    string
	platforms []Platform
	// options are the options of the installer (without the leading dashes' spelling: they are rendered per shell)
	fleet, collectorVersion          string
	siteType, siteID                 string
	siteName, siteCountry            string
	siteRegion, siteTimezone, env    string
	linuxTokenFile, windowsTokenFile string
	caFile                           string
}

// Allowlists. Every value is printable ASCII; a value never starts with "-" (it could pass for an option) and never
// contains a quotation mark, a backtick, "$", ";" or a character of the shells that could end a quoted string.
var (
	reWord    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	reSiteID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,62}$`)
	reText    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._,:/@+()#%=~-]{0,99}$`)
	reCountry = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,15}$`)
	reZone    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._+/-]{0,63}$`)
	reLinux   = regexp.MustCompile(`^/[A-Za-z0-9 ._,:@+=%~#()/-]{1,239}$`)
	reWindows = regexp.MustCompile(`^[A-Za-z]:\\[A-Za-z0-9 ._,@+=%~#()\\-]{1,237}$`)
)

// commandAttributes are the attributes that a command can carry: the options of the installers for the site.
var commandAttributes = []string{
	domain.AttrSiteType, domain.AttrSiteID, domain.AttrSiteName, domain.AttrSiteCountry, domain.AttrSiteRegion, domain.AttrSiteTimezone, domain.AttrEnvironment,
}

func (p *Planner) checkCommands(req *CommandsRequest) (*commandInput, error) {
	var issues domain.Issues
	add := func(path, format string, args ...any) {
		issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: CodeInvalidValue, Path: path, Message: fmt.Sprintf(format, args...)})
	}
	in := &commandInput{variant: req.Variant}

	switch in.variant {
	case "":
		in.variant = VariantTokenInline
	case VariantTokenInline, VariantTokenFile:
	case VariantLab:
		if !p.d.DevMode {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: CodeVariantNotAvailable, Path: "variant",
				Message: "the variant lab is only available on a development server: it lowers the checks of the installer"})
		}
	default:
		add("variant", "variant must be %s, %s or %s", VariantTokenInline, VariantTokenFile, VariantLab)
	}

	// platforms: those of the request, each of install.platforms; without any, all of them
	switch {
	case len(req.Platforms) > maxCommandPlatforms:
		add("platforms", "at most %d platforms are accepted", maxCommandPlatforms)
	case len(req.Platforms) == 0:
		in.platforms = append(in.platforms, p.platforms...)
	default:
		seen := map[Platform]bool{}
		for i, raw := range req.Platforms {
			path := fmt.Sprintf("platforms[%d]", i)
			os, arch, ok := strings.Cut(raw, "/")
			if !ok || len(raw) > 32 {
				add(path, "a platform is written os/arch, for example linux/amd64")
				continue
			}
			plat := NormalizePlatform(os, arch)
			found := false
			for _, s := range p.platforms {
				found = found || s == plat
			}
			if !found {
				issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: CodePlatformNotSupported, Path: path,
					Message: "hosts on this platform cannot be installed from this server (install.platforms)"})
				continue
			}
			if !seen[plat] {
				seen[plat] = true
				in.platforms = append(in.platforms, plat)
			}
		}
	}
	needs := map[string]bool{}
	for _, plat := range in.platforms {
		needs[plat.OS] = true
	}

	if req.Fleet != "" {
		if !domain.ValidDNSLabel(req.Fleet) {
			add("fleet", "fleet must be the name of a fleet (lower case letters, digits and \"-\", at most 63 characters)")
		}
		in.fleet = req.Fleet
	}
	if v := req.CollectorVersion; v != "" {
		v = domain.NormalizeVersion(v)
		if _, err := semver.StrictNewVersion(v); err != nil || len(v) > 64 || !versionValue.MatchString(v) {
			add("collectorVersion", "collectorVersion must be a version such as 0.162.0")
		} else {
			in.collectorVersion = v
		}
	}

	// attributes
	for k := range req.Attributes {
		known := false
		for _, a := range commandAttributes {
			known = known || a == k
		}
		if !known {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: CodeUnknownAttribute, Path: "attributes." + safeKey(k),
				Message: fmt.Sprintf("the attribute %q is not an option of the installers (accepted: %s)", safeKey(k), strings.Join(commandAttributes, ", "))})
		}
	}
	attr := func(key string, re *regexp.Regexp, hint string, required bool, into *string) {
		v, ok := req.Attributes[key]
		if !ok {
			if required {
				add("attributes."+key, "%s is required: the installers stop with option_missing without it", key)
			}
			return
		}
		if why := checkText(v, re, hint); why != "" {
			add("attributes."+key, "%s %s", key, why)
			return
		}
		*into = v
	}
	attr(domain.AttrSiteType, reWord, hintWord, true, &in.siteType)
	attr(domain.AttrSiteID, reSiteID, hintWord+" and \":\"", true, &in.siteID)
	attr(domain.AttrSiteName, reText, hintText, false, &in.siteName)
	attr(domain.AttrSiteCountry, reCountry, hintWord+", at most 16 characters", false, &in.siteCountry)
	attr(domain.AttrSiteRegion, reText, hintText, false, &in.siteRegion)
	attr(domain.AttrSiteTimezone, reZone, "a time zone such as Europe/Amsterdam: letters, digits and \"._+/-\"", false, &in.siteTimezone)
	attr(domain.AttrEnvironment, reWord, hintWord, false, &in.env)

	// files on the host
	if in.variant == VariantTokenFile {
		tf := req.TokenFiles
		if tf == nil {
			tf = &TokenFiles{}
		}
		for _, f := range []struct {
			os, path string
			re       *regexp.Regexp
			into     *string
			sep      string
		}{
			{"linux", tf.Linux, reLinux, &in.linuxTokenFile, "/"},
			{"windows", tf.Windows, reWindows, &in.windowsTokenFile, `\`},
		} {
			path := "tokenFiles." + f.os
			switch {
			case f.path == "" && needs[f.os]:
				add(path, "the path of the token file on the host is required for the variant %s", VariantTokenFile)
			case f.path == "":
			default:
				if why := checkPath(f.path, f.re, f.sep); why != "" {
					add(path, "%s", why)
				} else {
					*f.into = f.path
				}
			}
		}
	} else if req.TokenFiles != nil {
		add("tokenFiles", "tokenFiles applies to the variant %s only", VariantTokenFile)
	}
	if req.CAFile != "" {
		if why := checkPath(req.CAFile, reLinux, "/"); why != "" {
			add("caFile", "%s", why)
		} else {
			in.caFile = req.CAFile
		}
	}

	if len(issues) > 0 {
		return nil, domain.NewValidationError(issues...)
	}
	return in, nil
}

// checkText returns why a value is refused, or "" when the allowlist accepts it. A value that carries a quotation
// mark, a backtick, a dollar sign, a semicolon, a control character or a character outside printable ASCII gets the
// reason for it, so that the person knows what to change; it never repeats the value.
func checkText(v string, re *regexp.Regexp, hint string) string {
	if v == "" {
		return "must not be empty"
	}
	if why := forbidden(v); why != "" {
		return why
	}
	if strings.HasSuffix(v, " ") || !re.MatchString(v) {
		return "has a character or a length that is not accepted: " + hint
	}
	return ""
}

// What the allowlists accept, for the messages.
const (
	hintWord = "it starts with a letter or a digit and has letters, digits and \"._-\", at most 63 characters"
	hintText = "it starts with a letter or a digit and has letters, digits, spaces (not at the end) and \".,:/@+()#%=~-\", at most 100 characters"
)

func checkPath(v string, re *regexp.Regexp, sep string) string {
	if why := forbidden(v); why != "" {
		return "the path " + why
	}
	if !re.MatchString(v) || strings.HasSuffix(v, sep) || strings.HasSuffix(v, " ") {
		return "the path must be an absolute path to a file in the form of the host (/etc/... or C:\\...), without quotation marks or other special characters"
	}
	for _, seg := range strings.Split(v, sep) {
		if seg == ".." || seg == "." {
			return "the path must not contain \".\" or \"..\" segments"
		}
	}
	return ""
}

// forbidden names why a value must not appear in a command, or returns "". The characters are the ones that end or
// change a quoted string in bash or PowerShell, and everything that is not printable ASCII (which includes the
// typographic quotation marks U+2018 to U+201E that PowerShell treats as quotation marks, and control characters).
func forbidden(v string) string {
	for _, r := range v {
		switch {
		case r == '\'' || r == '"' || r == '`' || (r >= 0x2018 && r <= 0x201E) || r == 0x00AB || r == 0x00BB:
			return "must not contain a quotation mark or a backtick"
		case r == '$' || r == ';':
			return "must not contain \"$\" or \";\""
		case unicode.IsControl(r):
			return "must not contain a control character"
		case r > unicode.MaxASCII:
			return "must consist of printable ASCII characters"
		}
	}
	if strings.Contains(v, "@@") {
		return "must not contain \"@@\" (it marks the placeholder of the enrollment token)"
	}
	for _, prefix := range []string{brand.EnrollmentTokenPrefix, brand.APITokenPrefix, brand.DownloadTokenPrefix} {
		if strings.Contains(v, prefix) {
			return "must not look like a token: a token is never part of a request"
		}
	}
	return ""
}

var (
	reServerHost = regexp.MustCompile(`^(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)
	reServerPath = regexp.MustCompile(`^(?:/[A-Za-z0-9._~-]+)*$`)
)

// commandServer returns the address of the server for the commands: server.publicUrl without a trailing slash, as
// scheme://host[:port][/path]. A command that carries a token needs https; plain http is for the lab variant only.
func commandServer(publicURL string, needHTTPS bool) (string, error) {
	u, err := url.Parse(publicURL)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
		return "", errors.New("server.publicUrl is not set to an absolute http(s) URL: the commands cannot name the server")
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(publicURL, "#"):
		return "", errors.New("server.publicUrl has credentials, a query or a fragment: the commands cannot use it")
	case needHTTPS && u.Scheme != "https":
		return "", errors.New("server.publicUrl is not an https URL: a command carries the enrollment token and must not send it in clear text (only a development server renders the variant lab)")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if !reServerHost.MatchString(u.Host) || !reServerPath.MatchString(path) {
		return "", errors.New("server.publicUrl has characters that a command cannot carry")
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// ------------------------------------------------------------------------ rendering ----

// render builds the command for a bundle.
func (in *commandInput) render(b Bundle) (Command, error) {
	if !ValidBundleFilename(b.Filename) || !isHexDigest(b.SHA256) || strings.ToLower(b.SHA256) != b.SHA256 {
		return Command{}, fmt.Errorf("install: the bundle %q has no usable name or hash", b.Filename)
	}
	cmd := Command{Platform: b.OS + "/" + b.Arch, Bundle: b}
	var err error
	switch b.OS {
	case "linux":
		cmd.Shell = ShellBash
		cmd.TokenFile = in.linuxTokenFile
		cmd.Command, err = in.renderLinux(b)
	case "windows":
		cmd.Shell = ShellPowerShell
		cmd.TokenFile = in.windowsTokenFile
		cmd.Command, err = in.renderWindows(b)
	default:
		err = fmt.Errorf("install: no command for %s", b.OS)
	}
	return cmd, err
}

// quote writes a value as a single-quoted string for bash and PowerShell. The checks have refused every value with a
// quotation mark; this is the second line of defense, and the command is not rendered if it is needed.
func quote(v string) (string, error) {
	for _, r := range v {
		if r == '\'' || r < 0x20 || r == 0x7f || r > unicode.MaxASCII {
			return "", fmt.Errorf("install: a value for a command has a character that cannot be quoted")
		}
	}
	return "'" + v + "'", nil
}

func quoteAll(vs ...string) ([]string, error) {
	out := make([]string, len(vs))
	for i, v := range vs {
		q, err := quote(v)
		if err != nil {
			return nil, err
		}
		out[i] = q
	}
	return out, nil
}

// installerOptions are the options of the installer, in the order in which they appear: name and value pairs, in the
// spelling of each installer.
func (in *commandInput) installerOptions(windows bool) []string {
	pair := func(linux, win, v string) []string {
		if v == "" {
			return nil
		}
		if windows {
			return []string{win, v}
		}
		return []string{linux, v}
	}
	var o []string
	o = append(o, pair("--fleet", "-Fleet", in.fleet)...)
	o = append(o, pair("--collector-version", "-CollectorVersion", in.collectorVersion)...)
	o = append(o, pair("--site-type", "-SiteType", in.siteType)...)
	o = append(o, pair("--site-id", "-SiteId", in.siteID)...)
	o = append(o, pair("--site-name", "-SiteName", in.siteName)...)
	o = append(o, pair("--site-country", "-SiteCountry", in.siteCountry)...)
	o = append(o, pair("--site-region", "-SiteRegion", in.siteRegion)...)
	o = append(o, pair("--site-timezone", "-SiteTimezone", in.siteTimezone)...)
	o = append(o, pair("--environment", "-Environment", in.env)...)
	if in.variant == VariantLab {
		if windows {
			o = append(o, "-Insecure")
		} else {
			o = append(o, "--insecure")
		}
	}
	return o
}

// The setup script of the Linux command. It is the same text for every variant and every platform: the values reach it
// as the positional parameters of `sh -c`, and the token through the standard input. The text has no single quote (it
// sits inside a single-quoted word) and no backslash that a shell could read differently.
//
//	$1 server  $2 curl --proto value  $3 CA file or ""  $4 token file or ""  $5 archive name  $6 SHA-256
//	"$@" after that: options of the installer
const linuxScript = `
set -eu
umask 077
die() { printf "[{{LOG}}] ERROR [%s] %s\n" "$1" "$2" >&2; exit 1; }
[ "$(id -u)" -eq 0 ] || die privilege_required "this command has to run as root (sudo)"
srv=$1 proto=$2 ca=$3 tf=$4 file=$5 sum=$6
shift 6
for t in curl tar gzip sha256sum mktemp; do
  command -v "$t" >/dev/null 2>&1 || die tool_missing "$t is required and was not found"
done
d=$(mktemp -d /root/{{SETUP_DIR}}.XXXXXX) || die directory_unsafe "cannot create a private working directory below /root"
cleanup() { rm -rf "$d"; }
trap cleanup EXIT
trap "exit 1" HUP INT TERM
if [ -n "$tf" ]; then
  [ -f "$tf" ] && [ -r "$tf" ] || die file_unreadable "the token file $tf is not a readable file"
  tok=$(tr -d "\r\n[:space:]" <"$tf")
  own=
else
  IFS= read -r tok || :
  own=1
fi
case $tok in
  "") die option_missing "the enrollment token is empty" ;;
  *@@*) die option_missing "the placeholder of the enrollment token was not replaced" ;;
  *[!A-Za-z0-9_.~+/=-]*) die option_invalid "the enrollment token contains characters that a token cannot have" ;;
esac
if [ -n "$own" ]; then
  tf=$d/enrollment-token
  printf "%s\n" "$tok" >"$tf"
fi
printf "[{{LOG}}] downloading the installer bundle %s from %s\n" "$file" "$srv"
printf "header = \042Authorization: Bearer %s\042\n" "$tok" |
  curl -fsSg --retry 3 --proto "$proto" --tlsv1.2 ${ca:+--cacert "$ca"} -K - -o "$d/$file" "$srv{{BUNDLES_PATH}}$file" ||
  die download_failed "the installer bundle could not be downloaded from $srv (check the address, the certificate of the server and the enrollment token)"
printf "%s  %s\n" "$sum" "$file" >"$d/SHA256SUMS"
(cd "$d" && sha256sum -c SHA256SUMS >/dev/null 2>&1) ||
  die checksum_mismatch "the installer bundle does not have the SHA-256 given in the command; nothing was unpacked"
printf "[{{LOG}}] the SHA-256 of the installer bundle is the one in the command; unpacking it\n"
tar -xzf "$d/$file" -C "$d" || die archive_invalid "the installer bundle could not be unpacked"
bash "$d/deploy/scripts/install-linux.sh" --server "$srv" --token-file "$tf" ${ca:+--ca-file "$ca"} "$@" </dev/null
`

// heredocDelimiter ends the here document that carries the token. A token never contains it.
var heredocDelimiter = strings.ToUpper(brand.Name) + "_TOKEN_END"

// The setup script of the Windows command: a script block with one parameter, a hashtable with the values, so that the
// text of the block is a constant and the values come after it. It uses cmdlets and native programs only (no .NET
// method call, no type accelerator beyond the parameter type), which Constrained Language Mode permits.
const windowsScript = `
param([hashtable]$P)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$Server = $P.Server; $File = $P.File; $Sha256 = $P.Sha256
$Token = $P.Token; $TokenFile = $P.TokenFile; $InstallerArgs = $P.InstallerArgs
if (-not (whoami.exe /groups | Select-String -SimpleMatch 'S-1-16-12288' -Quiet)) {
  throw '[{{LOG}}] ERROR [privilege_required] this command has to run in an elevated Windows PowerShell (Run as administrator)'
}
$blocked = @(Get-ExecutionPolicy -List | Where-Object { ($_.Scope -eq 'MachinePolicy' -or $_.Scope -eq 'UserPolicy') -and ($_.ExecutionPolicy -eq 'AllSigned' -or $_.ExecutionPolicy -eq 'Restricted') })
if ($blocked.Count -gt 0) {
  throw '[{{LOG}}] ERROR [execution_policy_blocked] Group Policy sets the execution policy to AllSigned or Restricted. It wins over -ExecutionPolicy, so the unsigned installer cannot run on this host'
}
if ($TokenFile) {
  if (-not (Test-Path -LiteralPath $TokenFile -PathType Leaf)) {
    throw "[{{LOG}}] ERROR [file_unreadable] the token file $TokenFile is not a readable file"
  }
  $Token = (Get-Content -LiteralPath $TokenFile -Raw) -replace '\s', ''
}
if (-not $Token) { throw '[{{LOG}}] ERROR [option_missing] the enrollment token is empty' }
if ($Token -match '@@') { throw '[{{LOG}}] ERROR [option_missing] the placeholder of the enrollment token was not replaced' }
if ($Token -notmatch '^[A-Za-z0-9_.~+/=-]+\z') { throw '[{{LOG}}] ERROR [option_invalid] the enrollment token contains characters that a token cannot have' }
$d = Join-Path $env:ProgramData ('{{SETUP_DIR}}-' + (New-Guid).Guid)
New-Item -ItemType Directory -Path $d | Out-Null
try {
  icacls.exe $d /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
  if ($LASTEXITCODE -ne 0) { throw '[{{LOG}}] ERROR [acl_failed] icacls could not restrict the working directory to SYSTEM and the administrators' }
  if (@(Get-ChildItem -LiteralPath $d -Force).Count -ne 0) { throw '[{{LOG}}] ERROR [directory_unsafe] the new working directory is not empty' }
  if (-not $TokenFile) {
    $TokenFile = Join-Path $d 'enrollment-token.txt'
    Set-Content -LiteralPath $TokenFile -Value $Token -NoNewline
  }
  $zip = Join-Path $d $File
  Write-Host "[{{LOG}}] downloading the installer bundle $File from $Server"
  try {
    Invoke-WebRequest -UseBasicParsing -Uri "$Server{{BUNDLES_PATH}}$File" -Headers @{ Authorization = "Bearer $Token" } -OutFile $zip
  } catch {
    throw "[{{LOG}}] ERROR [download_failed] the installer bundle could not be downloaded from $Server ($($_.Exception.Message))"
  }
  if ((Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash -ne $Sha256) {
    throw '[{{LOG}}] ERROR [checksum_mismatch] the installer bundle does not have the SHA-256 given in the command; nothing was unpacked'
  }
  Write-Host '[{{LOG}}] the SHA-256 of the installer bundle is the one in the command; unpacking it'
  Expand-Archive -LiteralPath $zip -DestinationPath (Join-Path $d 'package')
  $installer = Join-Path $d 'package\deploy\scripts\install-windows.ps1'
  Unblock-File -LiteralPath $installer
  $run = @('-NoProfile', '-ExecutionPolicy', 'RemoteSigned', '-File', $installer, '-Server', $Server, '-TokenFile', $TokenFile) + $InstallerArgs
  & powershell.exe @run
  if ($LASTEXITCODE -ne 0) { throw "[{{LOG}}] ERROR [installer_failed] the installer ended with exit code $LASTEXITCODE; its message is above" }
} finally {
  Remove-Item -LiteralPath $d -Recurse -Force -ErrorAction SilentlyContinue
  if (Test-Path -LiteralPath $d) { Write-Warning "[{{LOG}}] the working directory $d could not be removed: delete it by hand, it holds the installer bundle and may hold the enrollment token" }
}
`

var scriptMarkers = strings.NewReplacer(
	"{{LOG}}", brand.Name+"-install",
	"{{SETUP_DIR}}", brand.InstallSetupDir,
	"{{BUNDLES_PATH}}", api.BasePath+"/install/bundles/",
)

func (in *commandInput) renderLinux(b Bundle) (string, error) {
	proto := "=https"
	if in.variant == VariantLab {
		proto = "=http,https"
	}
	fixed, err := quoteAll(in.server, proto, in.caFile, in.linuxTokenFileIfFile(), b.Filename, b.SHA256)
	if err != nil {
		return "", err
	}
	opts, err := quoteAll(in.installerOptions(false)...)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("sudo sh -c '")
	sb.WriteString(scriptMarkers.Replace(linuxScript))
	sb.WriteString("' sh ")
	sb.WriteString(strings.Join(append(fixed, opts...), " "))
	if in.variant != VariantTokenFile {
		sb.WriteString(" <<'" + heredocDelimiter + "'\n")
		sb.WriteString(brand.EnrollmentTokenPlaceholder + "\n")
		sb.WriteString(heredocDelimiter + "\n")
	} else {
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (in *commandInput) linuxTokenFileIfFile() string {
	if in.variant == VariantTokenFile {
		return in.linuxTokenFile
	}
	return ""
}

func (in *commandInput) renderWindows(b Bundle) (string, error) {
	tokenFile := ""
	token := brand.EnrollmentTokenPlaceholder
	if in.variant == VariantTokenFile {
		tokenFile, token = in.windowsTokenFile, ""
	}
	vals, err := quoteAll(in.server, b.Filename, b.SHA256, token, tokenFile)
	if err != nil {
		return "", err
	}
	opts, err := quoteAll(in.installerOptions(true)...)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("& {")
	sb.WriteString(indent(scriptMarkers.Replace(windowsScript), "  "))
	sb.WriteString("} @{\n")
	for i, name := range []string{"Server", "File", "Sha256", "Token", "TokenFile"} {
		fmt.Fprintf(&sb, "  %s = %s\n", name, vals[i])
	}
	sb.WriteString("  InstallerArgs = @(" + strings.Join(opts, ", ") + ")\n")
	sb.WriteString("}\n")
	return sb.String(), nil
}

// indent puts a prefix before every line that is not empty.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
