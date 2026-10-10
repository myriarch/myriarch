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

	// the collector Dockerfile copies the updater from the published image at the path of its entrypoint
	collector := dockerCopies(string(readDeploy(t, "k8s/collector/Dockerfile")))
	var fromImage *dockerCopy
	for i := range collector {
		if collector[i].from == "updater-image" {
			fromImage = &collector[i]
		}
	}
	if assert.NotNil(t, fromImage, "collector Dockerfile: COPY --from=updater-image") {
		assert.Equal(t, []string{updaterEntry}, fromImage.srcs)
	}
	assert.Contains(t, string(readDeploy(t, "k8s/collector/Dockerfile")), "ARG UPDATER_IMAGE=scratch", "no default image: the digest of a release is passed on purpose")

	// the archives that the builder reads: <binary>_<version>_<os>_<arch>.tar.gz of the goreleaser archives
	assert.Contains(t, oci, `"%s_%s_%s_%s.tar.gz" % (binary, args.version, os_name, arch)`)
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

func TestPublishedRepositoriesAndLabels(t *testing.T) {
	crane := readRepoFile(t, "scripts/build-image-crane.sh")
	assert.Contains(t, crane, `IMAGE_REPO_NAME="ghcr.io/myriarch/myriarch"`)
	assert.Contains(t, crane, `IMAGE_REPO_NAME="ghcr.io/myriarch/myriarch-updater"`)
	var values struct {
		Image struct {
			Repository string `yaml:"repository"`
		} `yaml:"image"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/values.yaml"), &values))
	assert.Equal(t, "ghcr.io/myriarch/myriarch", values.Image.Repository, "the chart pulls the published server image")

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
