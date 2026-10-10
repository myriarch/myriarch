// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/configload"
	"github.com/myriarch/myriarch/internal/netguard"
)

// EnvironmentFile is the name of the generated environment page in docs/reference.
const EnvironmentFile = "environment-variables.md"

// EnvRead documents environment variables that a program reads outside of the configuration overrides. Every environment read
// in the production code must have an entry (TestEveryEnvironmentReadIsDocumented), so a new read cannot be forgotten.
type EnvRead struct {
	// Names are the variables, for example HTTP_PROXY and http_proxy.
	Names []string
	// ReadBy names the programs and modes that read the variables.
	ReadBy string
	// Effect describes what the variables change.
	Effect string
}

// envReads is the registry of the environment variables that the programs read directly. Variables whose name is a constant of
// internal/brand or a package variable use that constant, so a rename cannot leave the page behind.
var envReads = []EnvRead{
	{
		Names:  []string{brand.EnrollmentTokenEnv},
		ReadBy: "`" + brand.SimBinary + " run`",
		Effect: "The enrollment token of the simulated collectors when `--token` is not given. Prefer it to the flag: arguments are visible in process listings.",
	},
	{
		Names:  append(append([]string{}, netguard.ProxyEnvVars...), "NO_PROXY", "no_proxy"),
		ReadBy: "`" + brand.ServerBinary + "` (every outbound connection), `" + brand.UpdaterBinary + "` and `" + brand.SimBinary + "` (package downloads)",
		Effect: "Forward proxy for outbound HTTP and HTTPS connections, as the Go standard library reads it (`NO_PROXY` lists the hosts that bypass the proxy). `" + brand.ServerBinary + "` exempts the host and port of the proxy from the address check (`outbound.allowedCidrs`). While a proxy is in use, `" + brand.ServerBinary + "` still refuses a destination that is given as an address in a denied range, and `localhost`; other host names are left to the egress policy of the proxy. `ALL_PROXY` is not used.",
	},
	{
		Names:  []string{"SSL_CERT_FILE", "SSL_CERT_DIR"},
		ReadBy: "`" + brand.ServerBinary + "`, `" + brand.UpdaterBinary + "` and `" + brand.SimBinary + "` (Go standard library, on Linux and other Unix systems except macOS)",
		Effect: "The CA bundle file and the CA directories that replace the default locations of the system trust store. The binaries carry an embedded copy of the Mozilla roots that Go uses only when the system provides no roots at all. `outbound.caFile` adds CA certificates on top of whichever roots are in use.",
	},
	{
		Names:  []string{"PGTEST_CACHE_DIR"},
		ReadBy: "`" + brand.ServerBinary + " dev`, in builds with the `devpg` build tag (`make build`, `make dev`; the release binaries are built without it)",
		Effect: "The directory that caches the PostgreSQL binaries of the embedded database (default: `" + brand.Name + "/embedded-postgres` below the user cache directory of the operating system). The test helper shares it.",
	},
	{
		Names:  []string{"PATH"},
		ReadBy: "`" + brand.UpdaterBinary + "` on Unix systems, when it runs as root",
		Effect: "The only variable of its own environment that the updater passes to the configuration check that it runs under the account of the supervisor (with `HOME` and `USER` of that account).",
	},
}

// templateVarMeaning explains the variables of the deployment templates. A variable that a template uses without an entry here
// fails TestDeployTemplateVariablesHaveMeaning.
var templateVarMeaning = map[string]string{
	brand.EnrollmentTokenEnv: "The enrollment token. The supervisor sends it in the `Authorization: Bearer` header to the OpAMP endpoint; in container mode `" + brand.UpdaterBinary + "` sends it for the updater itself.",
	brand.OpAMPEndpointEnv:   "The OpAMP endpoint of the server that the supervisor in the Kubernetes template connects to.",
	"SITE_TYPE":              "Site attribute `site.type` that the supervisor in the Kubernetes template reports.",
	"SITE_ID":                "Site attribute `site.id` that the supervisor in the Kubernetes template reports.",
	"SITE_NAME":              "Site attribute `site.name` that the supervisor in the Kubernetes template reports.",
	"SITE_COUNTRY":           "Site attribute `site.country` that the supervisor in the Kubernetes template reports.",
	"SITE_REGION":            "Site attribute `site.region` that the supervisor in the Kubernetes template reports.",
	"SITE_TIMEZONE":          "Site attribute `site.timezone` that the supervisor in the Kubernetes template reports.",
	"ENVIRONMENT":            "Attribute `deployment.environment.name` that the supervisor in the Kubernetes template reports.",
	"K8S_CLUSTER_NAME":       "Kubernetes attribute `k8s.cluster.name` that the supervisor in the Kubernetes template reports.",
	"K8S_NAMESPACE_NAME":     "Kubernetes attribute `k8s.namespace.name` that the supervisor in the Kubernetes template reports (from the Downward API).",
	"K8S_POD_NAME":           "Kubernetes attribute `k8s.pod.name` that the supervisor in the Kubernetes template reports (from the Downward API).",
	"K8S_NODE_NAME":          "Kubernetes attribute `k8s.node.name` that the supervisor in the Kubernetes template reports (from the Downward API).",
}

// TemplateVar is a variable that a deployment template references with ${env:NAME}.
type TemplateVar struct {
	Name string
	// Files are the template files that reference the variable, relative to the repository root, sorted.
	Files []string
}

// templateRefRE matches ${env:NAME} and ${env:NAME:-default}.
var templateRefRE = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)

// ScanTemplateVars finds the environment variables that the YAML templates below deployDir/supervisor and deployDir/updater
// reference. Comment lines are ignored. The file paths in the result start with the name of deployDir ("deploy/...").
func ScanTemplateVars(deployDir string) ([]TemplateVar, error) {
	files := map[string]map[string]bool{}
	base := filepath.Base(filepath.Clean(deployDir))
	for _, sub := range []string{"supervisor", "updater"} {
		err := filepath.WalkDir(filepath.Join(deployDir, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".yaml") {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(deployDir, p)
			if err != nil {
				return err
			}
			name := path.Join(base, filepath.ToSlash(rel))
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, m := range templateRefRE.FindAllStringSubmatch(line, -1) {
					if files[m[1]] == nil {
						files[m[1]] = map[string]bool{}
					}
					files[m[1]][name] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var out []TemplateVar
	for name, set := range files {
		v := TemplateVar{Name: name}
		for f := range set {
			v.Files = append(v.Files, f)
		}
		sort.Strings(v.Files)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// documentedEnvNames returns every variable name that the page documents outside of the configuration overrides.
func documentedEnvNames() map[string]bool {
	names := map[string]bool{}
	for _, r := range envReads {
		for _, n := range r.Names {
			names[n] = true
		}
	}
	for n := range templateVarMeaning {
		names[n] = true
	}
	return names
}

// RenderEnvironment renders docs/reference/environment-variables.md.
func RenderEnvironment(overrides []configload.EnvOverride, templateVars []TemplateVar) ([]byte, error) {
	var b strings.Builder
	b.WriteString(frontMatter("Environment variables", 60, "The environment variables that the server, the updater and the tools read."))
	b.WriteString("This page is generated from the configuration loader, the environment reads of the programs and the deployment templates by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.\n\n")

	// ---- configuration overrides
	sorted := append([]configload.EnvOverride(nil), overrides...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	b.WriteString("## Configuration overrides\n\n")
	b.WriteString("Every scalar setting of the [server configuration](configuration.md) can be overridden with an environment variable. The name is `" + brand.EnvPrefix + "` followed by the YAML path in upper case, joined with `_`. A camelCase key is not split: `server.publicUrl` becomes `" + brand.EnvPrefix + "SERVER_PUBLICURL`.\n\n")
	b.WriteString("- A list is given as comma separated values, for example `" + brand.EnvPrefix + "AUTH_OIDC_SCOPES=openid,profile,email`.\n")
	b.WriteString("- A duration uses Go syntax such as `30s` or `5m`. A plain integer is rejected.\n")
	b.WriteString("- An empty value is ignored for a boolean, a number and a duration. For a string it sets the empty string.\n")
	b.WriteString("- The overrides apply to `" + brand.ServerBinary + " serve`, `" + brand.ServerBinary + " dev`, `" + brand.ServerBinary + " migrate`, `" + brand.ServerBinary + " config validate` and `" + brand.ServerBinary + " config print`.\n")
	b.WriteString("- An override wins over the configuration file.\n")
	b.WriteString("- Lists of objects and maps (`opamp.enrollmentTokens`, `notifications.channels`, `auth.devLogin.users` and the `executors` maps) cannot be set through the environment.\n\n")
	var rows [][]string
	for _, o := range sorted {
		typ, err := typeCell(o.Type)
		if err != nil {
			return nil, fmt.Errorf("refdoc: %s: %w", o.Path, err)
		}
		top, _, _ := strings.Cut(o.Path, ".")
		rows = append(rows, []string{code(o.Name), "[" + code(o.Path) + "](configuration.md#" + top + ")", typ})
	}
	b.WriteString(table([]string{"Variable", "Key", "Type"}, rows) + "\n")

	// ---- references inside configuration files
	b.WriteString("## References inside configuration files\n\n")
	b.WriteString("Two configuration files expand references to environment variables in their string values:\n\n")
	b.WriteString("- `" + brand.ServerBinary + ".yaml` accepts `${VAR}` and `${VAR:-default}`. A variable that is unset or empty gives the default, or the empty string when there is none.\n")
	b.WriteString("- The configuration of `" + brand.UpdaterBinary + "` accepts `${env:VAR}` and `${env:VAR:-default}`. A variable that is unset and has no default stops the updater with an error.\n\n")

	// ---- executor secrets
	def := config.Default().Executors.SecretEnvPrefix
	b.WriteString("## Executor secrets\n\n")
	b.WriteString("An `Executor` object in Git can name an environment variable of the server for a secret (`secretEnv`, `tokenEnv`), see the [Executor](gitops-schema.md#executor) reference. The name must start with `executors.secretEnvPrefix` (default `" + def + "`), so Git cannot read arbitrary variables. Each such variable needs a destination binding in `executors.secretEnvHosts` or an entry in `executors.secrets`; `" + brand.ServerBinary + " serve` refuses an executor secret without one.\n\n")

	// ---- variables read by the programs
	b.WriteString("## Variables that the programs read\n\n")
	b.WriteString("These variables are not part of the configuration overrides.\n\n")
	rows = nil
	for _, r := range envReads {
		names := make([]string, len(r.Names))
		for i, n := range r.Names {
			names[i] = code(n)
		}
		rows = append(rows, []string{strings.Join(names, ", "), r.ReadBy, r.Effect})
	}
	b.WriteString(table([]string{"Variable", "Read by", "Effect"}, rows) + "\n")

	// ---- deployment templates
	b.WriteString("## Variables in the deployment templates\n\n")
	b.WriteString("The templates under `deploy/supervisor` and `deploy/updater` reference these variables as `${env:NAME}`. They are resolved by the OpAMP Supervisor or by `" + brand.UpdaterBinary + "` when they load their configuration, not by `" + brand.ServerBinary + "`.\n\n")
	rows = nil
	for _, v := range templateVars {
		meaning, ok := templateVarMeaning[v.Name]
		if !ok {
			return nil, fmt.Errorf("refdoc: the deployment templates use ${env:%s}, which has no entry in templateVarMeaning (internal/refdoc/env.go)", v.Name)
		}
		files := make([]string, len(v.Files))
		for i, f := range v.Files {
			files[i] = code(f)
		}
		rows = append(rows, []string{code(v.Name), strings.Join(files, ", "), meaning})
	}
	b.WriteString(table([]string{"Variable", "Templates", "Meaning"}, rows) + "\n")
	return finish(b.String())
}
