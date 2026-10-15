// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/brand"
)

// The published images (ghcr.io/myriarch/myriarch and ghcr.io/myriarch/myriarch-updater) are built from the linux archives of
// the release build by scripts/build-image-crane.sh (IMAGE_SOURCE=dist, helper scripts/lib/oci_image.py). These tests keep
// the sources of the contract in step: the user, the program paths, the archive names, the repositories and the labels.
// The built image itself (config, one layer, no shell, determinism) is checked by ci/tests/image-crane-test.sh.

// imageUID is the user and the group of every image of the project and of the pod security context of the Helm chart. The
// collector image is the one documented exception (10001, the user of the upstream supervisor image).
const imageUID = "65532"

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, "..", rel))
	require.NoError(t, err, rel)
	return string(b)
}

func TestImagesRunAsOneUID(t *testing.T) {
	// the crane script passes IMAGE_UID to the release image builder (user and group of the config and of /etc/passwd)
	m := regexp.MustCompile(`(?m)^IMAGE_UID="(\d+)"`).FindStringSubmatch(readRepoFile(t, "scripts/build-image-crane.sh"))
	require.NotNil(t, m, "scripts/build-image-crane.sh sets IMAGE_UID")
	assert.Equal(t, imageUID, m[1])
	crane := readRepoFile(t, "scripts/build-image-crane.sh")
	assert.Contains(t, crane, "myriarch:x:"+imageUID+":"+imageUID+":", "/etc/passwd of the build from source")
	assert.Contains(t, crane, "myriarch:x:"+imageUID+":\\n", "/etc/group of the build from source")
	// the release image builder takes the number from its caller: no second place to keep it
	oci := readRepoFile(t, "scripts/lib/oci_image.py")
	assert.False(t, strings.Contains(oci, imageUID), "scripts/lib/oci_image.py gets the user as --uid")
	assert.True(t, strings.Contains(oci, "--uid"), "scripts/lib/oci_image.py takes --uid")

	// the local Dockerfiles
	files, err := filepath.Glob(filepath.Join(deployDir, "docker", "*.Dockerfile"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		assert.Regexpf(t, `(?m)^USER `+imageUID+`:`+imageUID+`$`, readRepoFile(t, "deploy/docker/"+filepath.Base(f)), "%s", filepath.Base(f))
	}

	// the Helm chart (standard profile)
	var values struct {
		PodSecurityContext struct {
			RunAsUser  int `yaml:"runAsUser"`
			RunAsGroup int `yaml:"runAsGroup"`
			FSGroup    int `yaml:"fsGroup"`
		} `yaml:"podSecurityContext"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/values.yaml"), &values))
	assert.Equal(t, 65532, values.PodSecurityContext.RunAsUser)
	assert.Equal(t, 65532, values.PodSecurityContext.RunAsGroup)
	assert.Equal(t, 65532, values.PodSecurityContext.FSGroup)

	// the documented exception
	assert.Regexp(t, `(?m)^USER 10001:10001$`, string(readDeploy(t, "k8s/collector/Dockerfile")), "collector image: the user of the upstream supervisor image")
	assert.Contains(t, readRepoFile(t, "scripts/build-collector-image-crane.sh"), "10001")
}

func TestReleaseImageKindsMatchTheReleaseArchives(t *testing.T) {
	oci := readRepoFile(t, "scripts/lib/oci_image.py")
	kind := func(name string) (binary, entrypoint string) {
		re := regexp.MustCompile(`(?s)"` + name + `": \{\s*"binary": "([^"]+)",\s*"entrypoint": "([^"]+)"`)
		m := re.FindStringSubmatch(oci)
		require.NotNilf(t, m, "scripts/lib/oci_image.py: kind %s", name)
		return m[1], m[2]
	}
	serverBin, serverEntry := kind("server")
	updaterBin, updaterEntry := kind("updater")
	assert.Equal(t, brand.ServerBinary, serverBin)
	assert.Equal(t, brand.UpdaterBinary, updaterBin)
	assert.Equal(t, "/usr/local/bin/"+brand.ServerBinary, serverEntry)
	assert.Equal(t, "/usr/local/bin/"+brand.UpdaterBinary, updaterEntry)

	// the server image is started by the Helm chart with args only: the entrypoint is the server and the command is serve
	assert.Contains(t, oci, `"cmd": ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"]`)
	assert.Contains(t, string(readDeploy(t, "helm/myriarch/templates/deployment.yaml")), `args: ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"]`)
	assert.Contains(t, readRepoFile(t, "scripts/build-image-crane.sh"), `IMAGE_CMD="serve,--config,/etc/myriarchd/myriarchd.yaml"`)

	// the collector Dockerfile chooses the stage "updater" as updater-${UPDATER_SOURCE}: every documented value must name a stage
	// that writes /out/myriarch-updater (the next stage copies that path), and the stage for the published image reads the
	// updater at the path of its entrypoint from a stage that is FROM ${UPDATER_IMAGE}
	collectorFile := string(readDeploy(t, "k8s/collector/Dockerfile"))
	stages := dockerStages(collectorFile)
	assert.Contains(t, collectorFile, "ARG UPDATER_SOURCE=build", "the default source is the build context")
	selector, ok := stages["updater"]
	if assert.True(t, ok, "collector Dockerfile: stage updater") {
		assert.Equal(t, "updater-${UPDATER_SOURCE}", selector.from)
	}
	assert.Contains(t, collectorFile, "COPY --from=updater --chmod=0755 /out/"+brand.UpdaterBinary, "the collector image copies the updater stage")
	for _, source := range []string{"build", "image"} {
		st, ok := stages["updater-"+source]
		if !assert.Truef(t, ok, "collector Dockerfile: UPDATER_SOURCE=%s names the stage updater-%s", source, source) {
			continue
		}
		assert.Containsf(t, collectorFile, "UPDATER_SOURCE="+source, "the header documents UPDATER_SOURCE=%s", source)
		assert.Containsf(t, st.body, "/out/"+brand.UpdaterBinary, "stage updater-%s writes /out/%s", source, brand.UpdaterBinary)
	}
	if st, ok := stages["updater-image"]; ok {
		copies := dockerCopies(st.body)
		if assert.Len(t, copies, 1, "stage updater-image: one COPY") {
			assert.Equal(t, []string{updaterEntry}, copies[0].srcs)
			assert.Equal(t, "/out/"+brand.UpdaterBinary, copies[0].dst)
			src, ok := stages[copies[0].from]
			if assert.Truef(t, ok, "stage updater-image copies from the stage %q", copies[0].from) {
				assert.Equal(t, "${UPDATER_IMAGE}", src.from, "the published updater image is the source")
			}
		}
	}
	assert.Contains(t, string(readDeploy(t, "k8s/collector/Dockerfile")), "ARG UPDATER_IMAGE=scratch", "no default image: the digest of a release is passed on purpose")

	// the archives that the builder reads: <binary>_<version>_<os>_<arch>.tar.gz of the goreleaser archives
	assert.Contains(t, oci, `"%s_%s_%s_%s.tar.gz" % (program, args.version, os_name, arch)`)
	var g struct {
		Archives []struct {
			ID           string   `yaml:"id"`
			Formats      []string `yaml:"formats"`
			NameTemplate string   `yaml:"name_template"`
		} `yaml:"archives"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	found := map[string]bool{}
	for _, a := range g.Archives {
		if a.ID == brand.ServerBinary || a.ID == brand.UpdaterBinary {
			found[a.ID] = true
			assert.Equal(t, []string{"tar.gz"}, a.Formats, a.ID)
			assert.Equal(t, a.ID+"_{{ .Version }}_{{ .Os }}_{{ .Arch }}", a.NameTemplate, a.ID)
		}
	}
	assert.Len(t, found, 2, "goreleaser archives of the two published binaries")
}

// The server image holds myriarchd, myrctl and myriarch-sim, the example repositories and the data directory of the server. The
// example files come from the release archives (everything in the image is then covered by checksums.txt), so the archives must
// carry them.
func TestServerImageContentComesFromTheReleaseArchives(t *testing.T) {
	oci := readRepoFile(t, "scripts/lib/oci_image.py")
	server := regexp.MustCompile(`(?s)"server": \{.*?\n    \},`).FindString(oci)
	require.NotEmpty(t, server, "scripts/lib/oci_image.py: kind server")
	assert.Contains(t, server, `"companions": ["`+brand.CLIBinary+`", "`+brand.SimBinary+`"]`)
	assert.Contains(t, server, `("`+brand.ServerBinary+`", "examples/quickstart", "opt/myriarch/examples/quickstart")`)
	assert.Contains(t, server, `("`+brand.SimBinary+`", "examples/sim", "opt/myriarch/examples/sim")`)
	assert.Contains(t, server, `"dirs": [("var/lib/myriarchd", 0o750)]`, "the data directory of the server")
	updater := regexp.MustCompile(`(?s)"updater": \{.*?\n    \},`).FindString(oci)
	require.NotEmpty(t, updater)
	assert.Contains(t, updater, `"companions": []`)
	assert.Contains(t, updater, `"trees": []`)
	assert.Contains(t, updater, `"dirs": []`, "the updater image needs no state directory: container mode keeps it on the volume of the collector image")

	var g struct {
		Archives []struct {
			ID           string   `yaml:"id"`
			Formats      []string `yaml:"formats"`
			NameTemplate string   `yaml:"name_template"`
			Files        []struct {
				Src string `yaml:"src"`
			} `yaml:"files"`
		} `yaml:"archives"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "release/goreleaser.yaml"), &g))
	srcs := map[string][]string{}
	for _, a := range g.Archives {
		for _, f := range a.Files {
			srcs[a.ID] = append(srcs[a.ID], f.Src)
		}
		if a.ID == brand.CLIBinary || a.ID == brand.SimBinary {
			require.NotEmpty(t, a.Formats, "%s: formats", a.ID)
			assert.Equal(t, []string{"tar.gz"}, a.Formats[:1], "%s: the image reads the tar.gz archive", a.ID)
			assert.Equal(t, a.ID+"_{{ .Version }}_{{ .Os }}_{{ .Arch }}", a.NameTemplate, a.ID)
		}
	}
	assert.Contains(t, srcs[brand.ServerBinary], "examples/quickstart", "the myriarchd archive carries the quickstart repository for the image")
	assert.Contains(t, srcs[brand.SimBinary], "examples/sim/*", "the myriarch-sim archive carries the simulator profiles for the image")
	for _, id := range []string{brand.ServerBinary, brand.CLIBinary, brand.SimBinary} {
		assert.Contains(t, srcs[id], "LICENSE", id)
		assert.Contains(t, srcs[id], "THIRD_PARTY_NOTICES", id)
	}

	// the build script describes what the image holds
	crane := readRepoFile(t, "scripts/build-image-crane.sh")
	assert.Contains(t, crane, "myriarchd + myrctl + myriarch-sim and the example repositories")
	assert.Contains(t, crane, "an empty data directory /var/lib/myriarchd for the user 65532")
}

// The data directory of the server: the image makes it (empty, 0750, owned by the user of the image), and every way of running
// the server mounts a volume exactly there and keeps its data below it.
func TestServerDataDirectory(t *testing.T) {
	const dir = "/var/lib/myriarchd"
	// the build from source (kind probe, tests): same directory, same owner and mode
	crane := readRepoFile(t, "scripts/build-image-crane.sh")
	assert.Contains(t, crane, `add_dir("var/lib/myriarchd", 0o750, `+imageUID+`)`, "the image built from source")

	// the local Dockerfile: made in the build stage (the distroless base has no shell), copied with its owner
	df := string(readDeploy(t, "docker/myriarchd.Dockerfile"))
	stages := dockerStages(df)
	if st, ok := stages["build"]; assert.True(t, ok, "build stage") {
		assert.Contains(t, st.body, "mkdir -p /out/data && chmod 0750 /out/data")
	}
	c, found := copyTo(dockerCopies(df), dir)
	if assert.True(t, found, "myriarchd.Dockerfile creates "+dir) {
		assert.Equal(t, "build", c.from)
		assert.Equal(t, []string{"/out/data"}, c.srcs)
		assert.Equal(t, imageUID+":"+imageUID, c.chown)
	}

	// Compose: a named volume at the data directory (it takes over owner and mode from the image), the server keeps its data there
	var compose struct {
		Services map[string]struct {
			Command []string `yaml:"command"`
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "compose/docker-compose.yaml"), &compose))
	srv := compose.Services["myriarchd"]
	assert.Contains(t, srv.Command, "--data-dir="+dir)
	var mounted bool
	for _, v := range srv.Volumes {
		if name, rest, ok := strings.Cut(v, ":"); ok && strings.HasPrefix(rest, dir) {
			mounted = true
			assert.Equal(t, dir, rest, "no mode suffix: the volume must be writable")
			_, declared := compose.Volumes[name]
			assert.Truef(t, declared, "%q is a named volume declared at the top level, not a path", name)
			assert.NotContains(t, name, "/", "a named volume, not a bind mount")
		}
	}
	assert.True(t, mounted, "compose: the server mounts a volume at "+dir)

	// Helm: the data volume at the data directory, GitOps clone and artifact cache below it, the pod user is the image user
	var values struct {
		Config struct {
			GitOps struct {
				CloneDir string `yaml:"cloneDir"`
			} `yaml:"gitops"`
			Artifacts struct {
				Dir string `yaml:"dir"`
			} `yaml:"artifacts"`
		} `yaml:"config"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/values.yaml"), &values))
	assert.True(t, strings.HasPrefix(values.Config.GitOps.CloneDir, dir+"/"), values.Config.GitOps.CloneDir)
	assert.True(t, strings.HasPrefix(values.Config.Artifacts.Dir, dir+"/"), values.Config.Artifacts.Dir)
	assert.Regexp(t, `(?s)- name: data\s+mountPath: `+regexp.QuoteMeta(dir)+`\n`, string(readDeploy(t, "helm/myriarch/templates/deployment.yaml")))
}

func TestPublishedRepositoriesAndLabels(t *testing.T) {
	crane := readRepoFile(t, "scripts/build-image-crane.sh")
	assert.Contains(t, crane, `IMAGE_REPO_NAME="ghcr.io/myriarch/myriarch"`)
	assert.Contains(t, crane, `IMAGE_REPO_NAME="ghcr.io/myriarch/myriarch-updater"`)
	var values struct {
		Image struct {
			Registry   string `yaml:"registry"`
			Repository string `yaml:"repository"`
		} `yaml:"image"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/values.yaml"), &values))
	assert.Equal(t, "ghcr.io/myriarch/myriarch", values.Image.Registry+"/"+values.Image.Repository, "the chart pulls the published server image")

	// the labels the release images carry come from ci/image-labels.env; the source links the package to the repository
	labels := readRepoFile(t, "ci/image-labels.env")
	value := func(key string) string {
		m := regexp.MustCompile(`(?m)^` + key + `=(.*)$`).FindStringSubmatch(labels)
		require.NotNilf(t, m, "ci/image-labels.env sets %s", key)
		return strings.TrimSpace(m[1])
	}
	assert.Equal(t, brand.Repository, value("LABEL_SOURCE"))
	assert.Equal(t, "https://"+brand.ModulePath, value("LABEL_SOURCE"))
	assert.Equal(t, brand.Name, value("LABEL_TITLE"))
	assert.Equal(t, brand.UpdaterBinary, value("LABEL_TITLE_UPDATER"))
	assert.Equal(t, "Apache-2.0", value("LABEL_LICENSES"))
}

// The Dockerfiles of the release images and of the CI tool image are gone: the published images are built from the signed
// release archives, and the project publishes no myrctl image (the binary is in the release archives).
func TestRemovedReleaseDockerfilesStayRemoved(t *testing.T) {
	assert.NoFileExists(t, filepath.Join(deployDir, "docker", "myriarchd-release.Dockerfile"))
	assert.NoFileExists(t, filepath.Join(deployDir, "docker", "myrctl.Dockerfile"))
}

// dockerStage is one build stage of a Dockerfile: the base of its FROM instruction and the instructions that follow it.
type dockerStage struct {
	from, body string
}

// dockerStages parses the named stages (FROM <base> AS <name>); line continuations are joined and comments skipped.
func dockerStages(dockerfile string) map[string]dockerStage {
	out := map[string]dockerStage{}
	name := ""
	for _, line := range strings.Split(strings.ReplaceAll(dockerfile, "\\\n", " "), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if strings.EqualFold(fields[0], "FROM") {
			name = ""
			args := fields[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:]
			}
			if len(args) == 3 && strings.EqualFold(args[1], "AS") {
				name = args[2]
				out[name] = dockerStage{from: args[0]}
			}
			continue
		}
		if name != "" {
			st := out[name]
			st.body += line + "\n"
			out[name] = st
		}
	}
	return out
}
