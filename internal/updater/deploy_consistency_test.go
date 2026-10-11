// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/config"
)

// Helm default values must be a valid myriarchd configuration (unknown keys are errors in myriarchd).
func TestHelmValuesConfigIsValidMyriarchdConfig(t *testing.T) {
	var values struct {
		Config yaml.Node `yaml:"config"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/values.yaml"), &values))
	raw, err := yaml.Marshal(&values.Config)
	require.NoError(t, err)
	cfg := config.Default()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	require.NoError(t, dec.Decode(&cfg), "values.yaml config: contains keys myriarchd does not know")
	assert.Equal(t, "/etc/myriarchd/secrets/database-dsn", cfg.Database.DSNFile)
	assert.True(t, cfg.OpAMP.RequireEnrollmentToken)
	assert.Equal(t, ":9090", cfg.Server.MetricsListenAddr)
	assert.Equal(t, "/v1/opamp", cfg.OpAMP.Path)
}

func TestHelmChartHasRequiredTemplatesAndSettings(t *testing.T) {
	for _, f := range []string{
		"deployment.yaml", "service.yaml", "ingress.yaml", "httproute.yaml", "route.yaml", "pdb.yaml", "configmap.yaml",
		"networkpolicy.yaml", "secret-bootstrap.yaml", "secret-enrollment.yaml", "servicemonitor.yaml", "serviceaccount.yaml",
	} {
		assert.FileExists(t, filepath.Join(deployDir, "helm/myriarch/templates", f))
	}
	vals := string(readDeploy(t, "helm/myriarch/values.yaml"))
	for _, want := range []string{
		"replicaCount: 2",
		"openshift:",
		"bootstrapAdmin:",
		"enrollmentTokens: []",
		"secretChecksum:",
		"ephemeral-storage: 5Gi",
		"allowSameNamespace: true",
		"preStopSleepSeconds: 10",
		"terminationGracePeriodSeconds: 60",
		"readOnlyRootFilesystem: true",
		"runAsNonRoot: true",
		"existingSecret:",
		"PgBouncer",
		"path: /healthz",
		"path: /readyz",
	} {
		assert.Contains(t, vals, want)
	}
	dep := string(readDeploy(t, "helm/myriarch/templates/deployment.yaml"))
	for _, want := range []string{"probes.readiness.path", "probes.liveness.path", "preStop", "topologySpreadConstraints", "checksum/config", "checksum/secret"} {
		assert.Contains(t, dep, want)
	}
	// the chart never starts the dev mode and never pins a user in the pod template (the OpenShift profile relies on it)
	assert.Contains(t, dep, `args: ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"]`)
	assert.NotContains(t, dep, "runAsUser")
	assert.NotContains(t, dep, "allow-nonlocal")
	// WebSocket timeouts of the ingress variants live in the examples (no controller is the default)
	nginx := string(readDeploy(t, "helm/myriarch/examples/ingress-nginx.yaml"))
	for _, want := range []string{
		`nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"`,
		`nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"`,
		`nginx.ingress.kubernetes.io/proxy-body-size: "16m"`,
	} {
		assert.Contains(t, nginx, want)
	}
	for _, f := range []string{"openshift.yaml", "quickstart-dex.yaml", "dex/dex.yaml"} {
		assert.FileExists(t, filepath.Join(deployDir, "helm/myriarch/examples", f))
	}
	assert.NoFileExists(t, filepath.Join(deployDir, "helm/myriarch/examples/ingress-openshift.yaml"))
}

// The ConfigMap in deploy/k8s/collector embeds the same files as deploy/supervisor/k8s.
func TestK8sConfigMapMatchesSupervisorTemplates(t *testing.T) {
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "k8s/collector/supervisor-configmap.yaml"), &cm))
	for _, name := range []string{"supervisor.yaml", "fallback.yaml"} {
		want := string(readDeploy(t, "supervisor/k8s/"+name))
		assert.Equal(t, want, cm.Data[name], "%s in supervisor-configmap.yaml is out of sync with deploy/supervisor/k8s/%s", name, name)
	}
}

func TestK8sManifestsParseAndUseIdentityPersistence(t *testing.T) {
	type doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec yaml.Node `yaml:"spec"`
	}
	read := func(file string) []doc {
		dec := yaml.NewDecoder(bytes.NewReader(readDeploy(t, file)))
		var out []doc
		for {
			var d doc
			if err := dec.Decode(&d); err != nil {
				break
			}
			if d.Kind != "" {
				out = append(out, d)
			}
		}
		return out
	}
	gw := read("k8s/collector/gateway.yaml")
	var kinds []string
	for _, d := range gw {
		kinds = append(kinds, d.Kind)
	}
	assert.Equal(t, []string{"Service", "Service", "StatefulSet"}, kinds)
	raw := string(readDeploy(t, "k8s/collector/gateway.yaml"))
	assert.Contains(t, raw, "volumeClaimTemplates", "gateway: PVC for a stable supervisor identity")
	assert.Contains(t, raw, "K8S_POD_NAME")
	assert.Contains(t, raw, "fieldPath: spec.nodeName")
	assert.Contains(t, raw, "mountPath: /var/lib/otelcol/supervisor")
	ds := string(readDeploy(t, "k8s/collector/daemonset.yaml"))
	assert.Contains(t, ds, "hostPath", "daemonset: hostPath storage")
	assert.Contains(t, ds, "kind: DaemonSet")
	for _, f := range []string{"namespace.yaml", "site-configmap.yaml", "secret.example.yaml", "kustomization.yaml", "Dockerfile"} {
		assert.FileExists(t, filepath.Join(deployDir, "k8s/collector", f))
	}
	df := string(readDeploy(t, "k8s/collector/Dockerfile"))
	assert.Contains(t, df, "otel/opentelemetry-collector-opampsupervisor")
	assert.Contains(t, df, "otel/opentelemetry-collector-contrib")
	assert.Contains(t, df, "/usr/local/bin/otelcol-contrib", "matches agent.executable in the k8s supervisor config")
}

func TestComposeFile(t *testing.T) {
	var c struct {
		Services map[string]struct {
			Image     string   `yaml:"image"`
			Profiles  []string `yaml:"profiles"`
			DependsOn any      `yaml:"depends_on"`
			Build     struct {
				Context    string `yaml:"context"`
				Dockerfile string `yaml:"dockerfile"`
			} `yaml:"build"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "compose/docker-compose.yaml"), &c))
	assert.Regexp(t, `^postgres:17@sha256:[0-9a-f]{64}$`, c.Services["postgres"].Image, "images are pinned by digest")
	assert.Equal(t, "deploy/docker/myriarchd.Dockerfile", c.Services["myriarchd"].Build.Dockerfile)
	assert.Equal(t, []string{"sim"}, c.Services["myriarch-sim"].Profiles, "myriarch-sim is optional")
	for name, s := range c.Services {
		if s.Build.Dockerfile != "" {
			assert.FileExists(t, filepath.Join(deployDir, "compose", s.Build.Context, s.Build.Dockerfile), name)
		}
	}
}

func TestDockerfiles(t *testing.T) {
	myriarchd := string(readDeploy(t, "docker/myriarchd.Dockerfile"))
	for _, want := range []string{"node:24", "pnpm", "-tags embedui", "gcr.io/distroless/static:nonroot", "internal/webui/dist", "/myriarchd"} {
		assert.Contains(t, myriarchd, want)
	}
	sim := string(readDeploy(t, "docker/myriarch-sim.Dockerfile"))
	for _, want := range []string{"./cmd/myriarch-sim", "gcr.io/distroless/static:nonroot", "examples/sim"} {
		assert.Contains(t, sim, want)
	}

	// every image built from this repository carries the project license and the third-party notices
	files, err := filepath.Glob(filepath.Join(deployDir, "docker", "*.Dockerfile"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	dockerfiles := map[string]bool{"k8s/collector/Dockerfile": true}
	for _, f := range files {
		dockerfiles["docker/"+filepath.Base(f)] = true
	}
	for rel := range dockerfiles {
		copies := dockerCopies(string(readDeploy(t, rel)))
		for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES"} {
			c, found := copyTo(copies, "/usr/share/licenses/myriarch/"+name)
			if assert.Truef(t, found, "%s copies %s to /usr/share/licenses/myriarch/", rel, name) {
				assert.Equalf(t, []string{name}, c.srcs, "%s: the source of %s is the file of the build context", rel, name)
				assert.Emptyf(t, c.from, "%s: %s comes from the build context, not from another stage", rel, name)
				assert.Equalf(t, "0644", c.chmod, "%s: mode of %s", rel, name)
			}
		}
	}
	// the Dockerfiles of docker/ declare the license of ci/image-labels.env (LABEL_LICENSES); the collector image declares none
	// because its alpine base brings packages under other licenses
	labels, err := os.ReadFile(filepath.Join(deployDir, "..", "ci", "image-labels.env"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^LABEL_LICENSES=(.+)$`).FindSubmatch(labels)
	require.NotNil(t, m, "ci/image-labels.env sets LABEL_LICENSES")
	for _, f := range files {
		assert.Containsf(t, string(readDeploy(t, "docker/"+filepath.Base(f))), `org.opencontainers.image.licenses="`+string(m[1])+`"`,
			"%s sets the licenses label to LABEL_LICENSES of ci/image-labels.env", filepath.Base(f))
	}
	assert.NotContains(t, string(readDeploy(t, "k8s/collector/Dockerfile")), "org.opencontainers.image.licenses")
	// the collector image also carries the Apache-2.0 text and the README of the OpenTelemetry software
	collector := string(readDeploy(t, "k8s/collector/Dockerfile"))
	c, found := copyTo(dockerCopies(collector), "/usr/share/licenses/opentelemetry-collector/LICENSE")
	if assert.True(t, found, "collector image: LICENSE of the OpenTelemetry software") {
		assert.Equal(t, []string{"LICENSE"}, c.srcs)
		assert.Equal(t, "0644", c.chmod)
	}
	assert.Contains(t, collector, "/usr/share/licenses/opentelemetry-collector/README")
	assert.Contains(t, collector, "Copyright The OpenTelemetry Authors")
}

// dockerCopy is one COPY instruction of a Dockerfile.
type dockerCopy struct {
	from, chmod, chown string
	srcs               []string
	dst                string
}

// dockerCopies parses the COPY instructions (line continuations joined, comments skipped, flags --from, --chmod and --chown kept).
func dockerCopies(dockerfile string) []dockerCopy {
	var out []dockerCopy
	for _, line := range strings.Split(strings.ReplaceAll(dockerfile, "\\\n", " "), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.EqualFold(fields[0], "COPY") {
			continue
		}
		var c dockerCopy
		args := fields[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			switch k, v, _ := strings.Cut(args[0], "="); k {
			case "--from":
				c.from = v
			case "--chmod":
				c.chmod = v
			case "--chown":
				c.chown = v
			}
			args = args[1:]
		}
		if len(args) < 2 {
			continue
		}
		c.srcs, c.dst = args[:len(args)-1], args[len(args)-1]
		out = append(out, c)
	}
	return out
}

func copyTo(copies []dockerCopy, dst string) (dockerCopy, bool) {
	for _, c := range copies {
		if c.dst == dst {
			return c, true
		}
	}
	return dockerCopy{}, false
}

// No image carries the release fixtures (test data of `myriarchd dev --releases-fixtures`, present only in the dev variant
// of the crane image). The published images are not built from Dockerfiles but by scripts/build-image-crane.sh from the
// release archives (deploy_image_contract_test.go), so deploy/docker holds only the local build Dockerfiles.
func TestNoImageCarriesReleaseFixtures(t *testing.T) {
	var all []string
	files, err := filepath.Glob(filepath.Join(deployDir, "docker", "*.Dockerfile"))
	require.NoError(t, err)
	for _, f := range files {
		all = append(all, "docker/"+filepath.Base(f))
	}
	all = append(all, "k8s/collector/Dockerfile")
	for _, rel := range all {
		assert.NotContainsf(t, strings.ToLower(string(readDeploy(t, rel))), "fixtures", "%s must not mention release fixtures", rel)
	}
}

// Every tar.gz and zip archive holds LICENSE and THIRD_PARTY_NOTICES at its root and lists its files explicitly; the bare
// binaries cannot hold files and stay as they are.
func TestGoreleaserArchivesShipLicenseFiles(t *testing.T) {
	var g struct {
		Archives []struct {
			ID      string   `yaml:"id"`
			Formats []string `yaml:"formats"`
			Files   []struct {
				Src  string `yaml:"src"`
				Dst  string `yaml:"dst"`
				Info struct {
					Mtime string `yaml:"mtime"`
				} `yaml:"info"`
			} `yaml:"files"`
		} `yaml:"archives"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	var ids []string
	for _, a := range g.Archives {
		if len(a.Formats) == 1 && a.Formats[0] == "binary" {
			assert.Empty(t, a.Files, "%s: the binary format has no files", a.ID)
			continue
		}
		ids = append(ids, a.ID)
		assert.NotEmpty(t, a.Files, "%s: an explicit files list", a.ID)
		for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES"} {
			n := 0
			for _, f := range a.Files {
				if f.Src == name {
					n++
					assert.Empty(t, f.Dst, "%s: %s sits at the archive root", a.ID, name)
					assert.Equal(t, "{{ .CommitDate }}", f.Info.Mtime, "%s: %s has a reproducible mtime", a.ID, name)
				}
			}
			assert.Equal(t, 1, n, "%s holds %s exactly once", a.ID, name)
		}
	}
	sort.Strings(ids)
	assert.Equal(t, []string{"myrctl", "myriarch-sim", "myriarch-updater", "myriarchd"}, ids)
}

// The release matrix of deploy/release/goreleaser.yaml: platforms and tags per binary.
func TestGoreleaserMatrix(t *testing.T) {
	var g struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Main   string   `yaml:"main"`
			Tags   []string `yaml:"tags"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
			Ignore []struct {
				Goos   string `yaml:"goos"`
				Goarch string `yaml:"goarch"`
			} `yaml:"ignore"`
		} `yaml:"builds"`
		Checksum struct {
			Name string `yaml:"name_template"`
		} `yaml:"checksum"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	got := map[string][]string{}
	for _, b := range g.Builds {
		skip := map[string]bool{}
		for _, ig := range b.Ignore {
			skip[ig.Goos+"/"+ig.Goarch] = true
		}
		for _, os := range b.Goos {
			for _, arch := range b.Goarch {
				if !skip[os+"/"+arch] {
					got[b.ID] = append(got[b.ID], os+"/"+arch)
				}
			}
		}
		sort.Strings(got[b.ID])
		if b.ID == "myriarchd" {
			assert.Equal(t, []string{"embedui"}, b.Tags)
		}
	}
	assert.Equal(t, []string{"darwin/arm64", "linux/amd64", "linux/arm64"}, got["myriarchd"])
	assert.Equal(t, []string{"linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}, got["myriarch-updater"])
	assert.Equal(t, []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}, got["myrctl"])
	assert.Equal(t, []string{"darwin/arm64", "linux/amd64", "linux/arm64"}, got["myriarch-sim"])
	assert.Equal(t, "checksums.txt", g.Checksum.Name)
}

var (
	versionDefMakefile = regexp.MustCompile(`(?m)^VERSION_PKG\s*:=\s*(\S+)\s*$`)
	versionDefShell    = regexp.MustCompile(`(?m)^VERSION_PKG=(\S+)\s*$`)
	versionDefDocker   = regexp.MustCompile(`(?m)^ARG VERSION_PKG=(\S+)\s*$`)
	versionLdflag      = regexp.MustCompile(`-X\s+(\S+)\.(Version|Commit|BuildDate)=`)
	// any -X that targets the version package, spelled out or through a VERSION_PKG variable
	versionLdflagAny = regexp.MustCompile(`-X\s+(\S*internal/version|\$[({]VERSION_PKG[)}])`)
)

// The package that receives the build metadata (-X) is defined once per build system and always derived from the module
// path in go.mod: a module rename changes the definitions, never the -X lines, and a forgotten definition fails here
// instead of silently producing binaries that report 0.0.0-dev.
func TestVersionLdflagsUseOnePackage(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`).FindSubmatch(gomod)
	require.NotNil(t, m, "go.mod has no module line")
	want := string(m[1]) + "/internal/version"

	root := filepath.Join(deployDir, "..")
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, err, rel)
		return string(b)
	}
	// definition returns the package a file defines, or "" when it does not define one
	definition := func(rel string, re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(read(rel)); m != nil {
			return m[1]
		}
		return ""
	}

	// (a) the definitions
	assert.Equal(t, want, definition("Makefile", versionDefMakefile), "Makefile: VERSION_PKG")
	assert.Equal(t, want, definition("scripts/lib/image-common.sh", versionDefShell), "scripts/lib/image-common.sh: VERSION_PKG")
	dockerfiles := []string{"deploy/docker/myriarchd.Dockerfile", "deploy/docker/myriarch-sim.Dockerfile", "deploy/k8s/collector/Dockerfile"}
	for _, f := range dockerfiles {
		assert.Equal(t, want, definition(f, versionDefDocker), "%s: ARG VERSION_PKG", f)
	}

	// (b) and (c): every use names the package through the definition of its build system (or spells out the same
	// literal), and each file sets all three variables
	uses := map[string]string{ // file -> variable reference its definition provides ("" = literal only)
		"Makefile":                               "$(VERSION_PKG)",
		"deploy/release/goreleaser.yaml":         "",
		"scripts/build-image-crane.sh":           "${VERSION_PKG}",
		"scripts/build-collector-image-crane.sh": "${VERSION_PKG}",
	}
	for _, f := range dockerfiles {
		uses[f] = "${VERSION_PKG}"
	}
	for f, ref := range uses {
		raw := read(f)
		if strings.HasSuffix(f, "-crane.sh") {
			assert.Contains(t, raw, `. "${ROOT}/scripts/lib/image-common.sh"`, "%s must source the file that defines VERSION_PKG", f)
		}
		seen := map[string]bool{}
		for _, u := range versionLdflag.FindAllStringSubmatch(raw, -1) {
			assert.Truef(t, u[1] == want || (ref != "" && u[1] == ref), "%s: -X %s.%s must use %s or %s", f, u[1], u[2], want, ref)
			seen[u[2]] = true
		}
		for _, v := range []string{"Version", "Commit", "BuildDate"} {
			assert.Truef(t, seen[v], "%s sets no -X ...%s", f, v)
		}
	}

	// (d) no other build file sets the package
	for _, dir := range []string{"deploy", "scripts", "ci"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, path)
			require.NoError(t, err)
			rel = filepath.ToSlash(rel)
			if _, known := uses[rel]; known {
				return nil
			}
			if info, err := d.Info(); err != nil || info.Size() > 1<<20 {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			assert.Falsef(t, versionLdflagAny.Match(b), "%s sets internal/version through -X; add it to the central definitions and to this test", rel)
			return nil
		}))
	}
}

func TestDeployReadmeListsEverything(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(deployDir, "README.md"))
	require.NoError(t, err)
	s := string(data)
	for _, dir := range []string{"helm/myriarch", "k8s/collector", "compose", "docker", "supervisor", "scripts", "systemd", "windows", "release", "updater"} {
		assert.True(t, strings.Contains(s, dir), "deploy/README.md should mention %s", dir)
	}
	for _, want := range []string{"## License files", "THIRD_PARTY_NOTICES", "/usr/share/licenses/myriarch/", "IMAGE_VARIANT", "/usr/share/licenses/opentelemetry-collector/", "GPL-2.0"} {
		assert.Contains(t, s, want, "deploy/README.md, license files")
	}
}

// --- supply chain and network exposure (security review) -------------------

func TestDevComposePublishesOnlyOnLoopback(t *testing.T) {
	for _, file := range []string{"compose/docker-compose.yaml", "compose/quickstart.yaml"} {
		var c struct {
			Services map[string]struct {
				Ports []string `yaml:"ports"`
			} `yaml:"services"`
		}
		require.NoError(t, yaml.Unmarshal(readDeploy(t, file), &c), file)
		for name, s := range c.Services {
			for _, p := range s.Ports {
				assert.True(t, strings.HasPrefix(p, "127.0.0.1:"), "%s: %s publishes %q on all interfaces (dev mode has fixed logins)", file, name, p)
			}
		}
	}
}

// The Compose quickstart runs the release image (FROM scratch, user 65532, read-only root file system) in dev mode against
// a PostgreSQL container. The data directory /var/lib/myriarchd, which the image creates for that user, is on a named
// volume. The default image tag follows the chart, the postgres image is the one of the dev compose file, and the paths it
// uses below /opt/myriarch/examples map to repository paths that the release image copies there (examples/quickstart and
// examples/sim in scripts/build-image-crane.sh).
func TestQuickstartCompose(t *testing.T) {
	type service struct {
		Image       string   `yaml:"image"`
		Entrypoint  []string `yaml:"entrypoint"`
		Command     []string `yaml:"command"`
		ReadOnly    bool     `yaml:"read_only"`
		Tmpfs       []string `yaml:"tmpfs"`
		CapDrop     []string `yaml:"cap_drop"`
		SecurityOpt []string `yaml:"security_opt"`
		Ports       []string `yaml:"ports"`
		Volumes     []string `yaml:"volumes"`
	}
	var q struct {
		Name     string             `yaml:"name"`
		Services map[string]service `yaml:"services"`
		Volumes  map[string]any     `yaml:"volumes"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "compose/quickstart.yaml"), &q))
	var dev struct {
		Services map[string]service `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "compose/docker-compose.yaml"), &dev))
	var chart struct {
		AppVersion string `yaml:"appVersion"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/Chart.yaml"), &chart))
	require.NotEmpty(t, chart.AppVersion)

	assert.Equal(t, "myriarch-quickstart", q.Name)
	pg := q.Services["postgres"]
	assert.Equal(t, dev.Services["postgres"].Image, pg.Image, "the quickstart uses the PostgreSQL image of the dev compose file")
	assert.Empty(t, pg.Ports, "PostgreSQL is not published")
	assert.Contains(t, q.Volumes, "pgdata")

	wantImage := "${QUICKSTART_IMAGE:-ghcr.io/myriarch/myriarch:" + chart.AppVersion + "}"
	for _, name := range []string{"myriarchd", "myriarch-sim"} {
		s := q.Services[name]
		assert.Equal(t, wantImage, s.Image, "%s: the default image tag is the appVersion of the chart", name)
		assert.True(t, s.ReadOnly, "%s: read_only", name)
		assert.Empty(t, s.Tmpfs, "%s: nothing writes to the root file system, and the data directory is a volume", name)
		assert.Equal(t, []string{"ALL"}, s.CapDrop, "%s: cap_drop", name)
		assert.Equal(t, []string{"no-new-privileges:true"}, s.SecurityOpt, "%s: security_opt", name)
	}

	d := q.Services["myriarchd"]
	for _, want := range []string{"dev", "--allow-nonlocal", "--data-dir=/var/lib/myriarchd"} {
		assert.Contains(t, d.Command, want, "myriarchd command")
	}
	assert.Equal(t, []string{"myriarchd-data:/var/lib/myriarchd"}, d.Volumes, "the data directory is a named volume")
	assert.Contains(t, q.Volumes, "myriarchd-data")
	assert.Empty(t, q.Services["myriarch-sim"].Volumes, "the simulator needs no data directory")
	var gitops string
	for _, a := range d.Command {
		if v, ok := strings.CutPrefix(a, "--gitops-dir="); ok {
			gitops = v
		}
	}
	require.NotEmpty(t, gitops, "myriarchd command: --gitops-dir")
	assert.Equal(t, "${QUICKSTART_GITOPS_DIR:-/opt/myriarch/examples/quickstart}", gitops)
	assert.Equal(t, []string{"127.0.0.1:8080:8080"}, d.Ports)
	assert.DirExists(t, filepath.Join(deployDir, "..", "examples", "quickstart"), "/opt/myriarch/examples/quickstart in the image")

	sim := q.Services["myriarch-sim"]
	assert.Equal(t, []string{"/usr/local/bin/myriarch-sim"}, sim.Entrypoint)
	assert.Empty(t, sim.Ports, "the simulator publishes nothing")
	assert.Contains(t, sim.Command, "--profile=/opt/myriarch/examples/sim/mixed-fleet.yaml")
	assert.FileExists(t, filepath.Join(deployDir, "..", "examples", "sim", "mixed-fleet.yaml"), "/opt/myriarch/examples/sim in the image")
}

func TestContainerImagesArePinnedByDigest(t *testing.T) {
	re := regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
	for _, f := range []string{"docker/myriarchd.Dockerfile", "docker/myriarch-sim.Dockerfile"} {
		for _, line := range strings.Split(string(readDeploy(t, f)), "\n") {
			if strings.HasPrefix(line, "FROM ") {
				assert.Regexp(t, re, line, "%s: %s", f, line)
			}
		}
	}
	assert.Regexp(t, re, string(readDeploy(t, "k8s/collector/daemonset.yaml")), "the root init container image is pinned")
	assert.Contains(t, string(readDeploy(t, "k8s/collector/Dockerfile")), "COLLECTOR_IMAGE", "upstream images can be pinned by digest at build time")
}

func TestHelmChartShipsNetworkPolicyAndRequiresIngressTLS(t *testing.T) {
	np := string(readDeploy(t, "helm/myriarch/templates/networkpolicy.yaml"))
	for _, want := range []string{"kind: NetworkPolicy", "port: http", "port: metrics", "policyTypes", "networkPolicy.ingress.from", "allowSameNamespace", "openshift.routerNamespaceSelector", "exceptCidrs"} {
		assert.Contains(t, np, want)
	}
	vals := string(readDeploy(t, "helm/myriarch/values.yaml"))
	assert.Regexp(t, `(?m)^networkPolicy:\n  enabled: true`, vals, "enabled by default")
	assert.Contains(t, vals, "trustedProxies")
	assert.Contains(t, vals, "ONLY together with the NetworkPolicy")
	ing := string(readDeploy(t, "helm/myriarch/templates/ingress.yaml"))
	assert.Contains(t, ing, "allowWithoutTls")
	assert.Contains(t, ing, "fail ")
}

func TestK8sCollectorNetworkPolicy(t *testing.T) {
	dec := yaml.NewDecoder(bytes.NewReader(readDeploy(t, "k8s/collector/networkpolicy.yaml")))
	var names []string
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := dec.Decode(&d); err != nil {
			break
		}
		assert.Equal(t, "NetworkPolicy", d.Kind)
		names = append(names, d.Metadata.Name)
	}
	assert.Equal(t, []string{"otel-gateway", "otel-agent"}, names)
	assert.Contains(t, string(readDeploy(t, "k8s/collector/kustomization.yaml")), "networkpolicy.yaml")
}

func TestUpdaterUnitIsSandboxed(t *testing.T) {
	unit := string(readDeploy(t, "systemd/myriarch-updater.service"))
	for _, want := range []string{
		"ProtectSystem=strict", "ReadWritePaths=", "StateDirectory=myriarch-updater", "ProtectKernelTunables=true",
		"ProtectKernelModules=true", "ProtectKernelLogs=true", "ProtectControlGroups=true", "PrivateDevices=true",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6", "RestrictSUIDSGID=true", "LockPersonality=true",
		"SystemCallArchitectures=native", "CapabilityBoundingSet=",
	} {
		assert.Contains(t, unit, want)
	}
	// the preflight drops to the supervisor's account: that needs setuid/setgid
	assert.Contains(t, unit, "CAP_SETUID")
	assert.Contains(t, unit, "CAP_SETGID")
	// ... and ending that child at the timeout needs CAP_KILL (it runs under another uid)
	assert.Contains(t, unit, "CAP_KILL")
	assert.NotContains(t, unit, "CAP_SYS_ADMIN")
}

// The release build holds no signing key: goreleaser signs nothing (releases are signed keyless by the release workflow, and the
// Windows files get Authenticode through the hook of deploy/release/windows-signed.sh), and the key-based tooling is gone.
func TestGoreleaserReleaseBuildHasNoSigningKey(t *testing.T) {
	raw := readDeploy(t, "release/goreleaser.yaml")
	var top map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &top))
	assert.NotContains(t, top, "signs", "goreleaser.yaml has no signs: block")
	for _, banned := range []string{"cosign", "sign-blob", "authenticode.sh", "COSIGN_", "AUTHENTICODE_", "pnpm", "npm ", "corepack", "--enrich"} {
		assert.NotContains(t, string(raw), banned, "goreleaser.yaml must not mention %q", banned)
	}
	var g struct {
		Release struct {
			Disable bool `yaml:"disable"`
		} `yaml:"release"`
		Changelog struct {
			Disable string `yaml:"disable"`
		} `yaml:"changelog"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &g))
	assert.True(t, g.Release.Disable, "release publishing stays disabled")
	assert.Equal(t, "true", g.Changelog.Disable, "the release notes come from the CHANGELOG")
	assert.NoFileExists(t, filepath.Join(deployDir, "release/authenticode.sh"))
	assert.NoFileExists(t, filepath.Join(deployDir, "release/cosign.pub"))
}

type goreleaserHook struct {
	Cmd string `yaml:"cmd"`
}

type goreleaserHooks struct {
	Pre  []goreleaserHook `yaml:"pre"`
	Post []goreleaserHook `yaml:"post"`
}

// The hooks that tie the build to deploy/release/windows-signed.sh: version resources and stage marker before, the UI check as a
// pre hook of myriarchd only (a build of the Windows programs alone needs no UI), the stage hook after the Windows builds.
func TestGoreleaserHooks(t *testing.T) {
	var g struct {
		Before struct {
			Hooks []string `yaml:"hooks"`
		} `yaml:"before"`
		Builds []struct {
			ID    string          `yaml:"id"`
			Hooks goreleaserHooks `yaml:"hooks"`
		} `yaml:"builds"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	assert.Equal(t, []string{`bash deploy/release/windows-signed.sh prepare "{{ .Version }}" "{{ .IsSnapshot }}"`}, g.Before.Hooks)
	seen := map[string]bool{}
	for _, b := range g.Builds {
		seen[b.ID] = true
		switch b.ID {
		case "myriarchd":
			require.Len(t, b.Hooks.Pre, 1, "myriarchd checks the prebuilt UI")
			assert.Contains(t, b.Hooks.Pre[0].Cmd, "internal/webui/dist/index.html")
			assert.Empty(t, b.Hooks.Post)
		case "myriarch-updater", "myrctl":
			assert.Empty(t, b.Hooks.Pre, b.ID)
			require.Len(t, b.Hooks.Post, 1, b.ID)
			assert.Equal(t, `bash deploy/release/windows-signed.sh build `+b.ID+` "{{ .Path }}" "{{ .Os }}" "{{ .Arch }}" "{{ .IsSnapshot }}" "{{ .Version }}"`,
				b.Hooks.Post[0].Cmd, b.ID)
		case "myriarch-sim":
			assert.Empty(t, b.Hooks.Pre, b.ID)
			assert.Empty(t, b.Hooks.Post, b.ID)
		default:
			t.Errorf("unexpected build %q", b.ID)
		}
	}
	assert.Len(t, seen, 4)
	for _, rel := range []string{"release/windows-signed.sh", "release/sbom.sh"} {
		info, err := os.Stat(filepath.Join(deployDir, rel))
		require.NoError(t, err, rel)
		if runtime.GOOS != "windows" {
			assert.NotZero(t, info.Mode()&0o111, "%s is executable", rel)
		}
	}
}

// One SPDX JSON SBOM per archive and one for the web UI, both made by deploy/release/sbom.sh (pinned syft, no network, no enrichment).
func TestGoreleaserSBOMs(t *testing.T) {
	var g struct {
		SBOMs []struct {
			ID        string   `yaml:"id"`
			Artifacts string   `yaml:"artifacts"`
			Documents []string `yaml:"documents"`
			Cmd       string   `yaml:"cmd"`
			Args      []string `yaml:"args"`
		} `yaml:"sboms"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	require.Len(t, g.SBOMs, 2)
	archives, webui := g.SBOMs[0], g.SBOMs[1]
	assert.Equal(t, "archives", archives.ID)
	assert.Equal(t, "archive", archives.Artifacts)
	assert.Equal(t, []string{"{{ .ArtifactName }}.sbom.json"}, archives.Documents)
	assert.Equal(t, "bash", archives.Cmd)
	assert.Equal(t, []string{"../deploy/release/sbom.sh", "archive", "$artifact", "$document"}, archives.Args)
	assert.Equal(t, "webui", webui.ID)
	assert.Equal(t, "any", webui.Artifacts)
	assert.Equal(t, []string{"myriarch-webui_{{ .Version }}.sbom.json"}, webui.Documents)
	assert.Equal(t, "bash", webui.Cmd)
	assert.Equal(t, []string{"../deploy/release/sbom.sh", "webui", "{{ .Version }}", "$document"}, webui.Args)
	assert.FileExists(t, filepath.Join(deployDir, "release/syft.yaml"))
}

// LICENSE and THIRD_PARTY_NOTICES are release assets of their own and are listed in checksums.txt.
func TestGoreleaserChecksumExtraFiles(t *testing.T) {
	var g struct {
		Checksum struct {
			ExtraFiles []struct {
				Glob string `yaml:"glob"`
			} `yaml:"extra_files"`
		} `yaml:"checksum"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	var globs []string
	for _, f := range g.Checksum.ExtraFiles {
		globs = append(globs, f.Glob)
	}
	assert.Equal(t, []string{"./LICENSE", "./THIRD_PARTY_NOTICES"}, globs)
}

// The Windows scripts ship only in the Windows updater archives (ADR-0034): the tracked scripts are not archived by a glob, and the
// two staged entries match nothing for other systems.
func TestGoreleaserWindowsScriptsOnlyInWindowsArchives(t *testing.T) {
	var g struct {
		Archives []struct {
			ID    string `yaml:"id"`
			Files []struct {
				Src         string `yaml:"src"`
				Dst         string `yaml:"dst"`
				StripParent bool   `yaml:"strip_parent"`
			} `yaml:"files"`
		} `yaml:"archives"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	found := false
	for _, a := range g.Archives {
		if a.ID != "myriarch-updater" {
			continue
		}
		found = true
		staged := map[string]string{}
		for _, f := range a.Files {
			assert.False(t, strings.HasSuffix(f.Src, ".ps1"), "%s is a Windows script", f.Src)
			assert.NotEqual(t, "deploy/scripts/*", f.Src)
			assert.NotEqual(t, "deploy/windows/*", f.Src)
			if strings.HasPrefix(f.Src, ".cache/release/windows/archive/") {
				assert.True(t, f.StripParent, f.Src)
				staged[f.Src] = f.Dst
			}
		}
		assert.Equal(t, map[string]string{
			".cache/release/windows/archive/{{ .Os }}/scripts/*": "deploy/scripts",
			".cache/release/windows/archive/{{ .Os }}/windows/*": "deploy/windows",
		}, staged)
	}
	assert.True(t, found, "the myriarch-updater archive")
}

// The version resources that the release build generates are never committed, and ignoring them keeps the build reproducible (Go
// stamps vcs.modified=false).
func TestReleaseGitignoresWindowsResources(t *testing.T) {
	data, err := os.ReadFile("../../.gitignore")
	require.NoError(t, err)
	lines := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"/.cache/", "/cmd/myriarch-updater/rsrc_windows_*.syso", "/cmd/myrctl/rsrc_windows_*.syso"} {
		assert.True(t, lines[want], ".gitignore has %s", want)
	}
}

// The collector pods run configuration from Git: no Kubernetes API credential is mounted into them, and the
// storage preparation does not follow links below a directory that the collector writes.
func TestCollectorPodsMountNoServiceAccountTokenAndDoNotChownRecursively(t *testing.T) {
	type podSpec struct {
		AutomountServiceAccountToken *bool `yaml:"automountServiceAccountToken"`
		InitContainers               []struct {
			Name    string   `yaml:"name"`
			Command []string `yaml:"command"`
		} `yaml:"initContainers"`
	}
	type doc struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Template struct {
				Spec *podSpec `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	pods := 0
	for _, dir := range []string{"k8s/collector", "examples/production/collector"} {
		var files []string
		require.NoError(t, filepath.WalkDir(filepath.Join(deployDir, dir), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".yaml") {
				files = append(files, p)
			}
			return err
		}))
		for _, f := range files {
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			dec := yaml.NewDecoder(bytes.NewReader(raw))
			for {
				var d doc
				if err := dec.Decode(&d); err != nil {
					break
				}
				if d.Spec.Template.Spec == nil {
					continue
				}
				pods++
				spec := d.Spec.Template.Spec
				name := filepath.Base(f) + " " + d.Kind
				if d.Kind == "DaemonSet" && strings.Contains(f, "delete-node-agent") {
					continue // a delete patch
				}
				require.NotNil(t, spec.AutomountServiceAccountToken, "%s: automountServiceAccountToken must be set", name)
				assert.False(t, *spec.AutomountServiceAccountToken, "%s: automountServiceAccountToken must be false", name)
				for _, ic := range spec.InitContainers {
					assert.NotContains(t, strings.Join(ic.Command, " "), "chown -R", "%s: init container %s", name, ic.Name)
				}
			}
		}
	}
	assert.GreaterOrEqual(t, pods, 2, "the DaemonSet and the StatefulSet are checked")
}
