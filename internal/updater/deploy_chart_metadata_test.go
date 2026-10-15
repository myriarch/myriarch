// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/brand"
)

// TestChartHasArtifactHubMetadata keeps the metadata that Artifact Hub reads from Chart.yaml complete and consistent with the rest
// of the repository: the icon is a brand file that the website serves, the maintainers are the project address, the links point
// to the project, and the image of the chart is the one the values name.
func TestChartHasArtifactHubMetadata(t *testing.T) {
	var chart struct {
		Home        string   `yaml:"home"`
		Icon        string   `yaml:"icon"`
		Sources     []string `yaml:"sources"`
		Maintainers []struct {
			Name  string `yaml:"name"`
			Email string `yaml:"email"`
			URL   string `yaml:"url"`
		} `yaml:"maintainers"`
		Annotations map[string]string `yaml:"annotations"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/Chart.yaml"), &chart))

	// the icon is a file of assets/brand, which the website publishes below /brand/ (website/config/_default/module.yaml)
	iconURL, err := url.Parse(chart.Icon)
	require.NoError(t, err)
	assert.Equal(t, "https", iconURL.Scheme)
	assert.Equal(t, "myriarch.dev", iconURL.Host)
	assert.Equal(t, "/brand/", path.Dir(iconURL.Path)+"/")
	assert.True(t, strings.HasSuffix(iconURL.Path, ".svg"))
	assert.FileExists(t, filepath.Join(deployDir, "..", "assets", "brand", path.Base(iconURL.Path)))

	// the maintainers are the project, with its address; the names of people are in MAINTAINERS.md
	require.NotEmpty(t, chart.Maintainers)
	for _, m := range chart.Maintainers {
		assert.Equal(t, "The Myriarch Authors", m.Name)
		assert.Equal(t, "maintainers@myriarch.dev", m.Email)
		assert.Equal(t, chart.Home, m.URL)
	}
	assert.Equal(t, []string{brand.Repository}, chart.Sources)

	ann := chart.Annotations
	assert.Equal(t, "Apache-2.0", ann["artifacthub.io/license"])
	assert.Equal(t, "false", ann["artifacthub.io/operator"], "the chart is not an operator")
	assert.Contains(t, []string{"ai-machine-learning", "database", "integration-delivery", "monitoring-logging", "networking", "security", "storage", "skip-prediction"},
		ann["artifacthub.io/category"], "a category that Artifact Hub knows")

	var links []struct {
		Name string `yaml:"name"`
		URL  string `yaml:"url"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(ann["artifacthub.io/links"]), &links))
	require.GreaterOrEqual(t, len(links), 3)
	byName := map[string]string{}
	for _, l := range links {
		assert.NotEmpty(t, l.Name)
		assert.True(t, strings.HasPrefix(l.URL, "https://"), "%s: %s", l.Name, l.URL)
		byName[l.Name] = l.URL
	}
	assert.Equal(t, brand.Repository, byName["Source code"])
	assert.Equal(t, brand.Repository+"/issues", byName["Issues"])
	assert.Equal(t, chart.Home+"/docs/", byName["Documentation"])
	assert.Equal(t, brand.Repository+"/security/policy", byName["Security policy"])

	// the images: the entry is a placeholder for the release job (it adds the digest), but it names the image of the values
	var values struct {
		Image struct {
			Registry   string `yaml:"registry"`
			Repository string `yaml:"repository"`
		} `yaml:"image"`
	}
	require.NoError(t, yaml.Unmarshal(readDeploy(t, "helm/myriarch/values.yaml"), &values))
	var images []struct {
		Name      string   `yaml:"name"`
		Image     string   `yaml:"image"`
		Platforms []string `yaml:"platforms"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(ann["artifacthub.io/images"]), &images))
	require.Len(t, images, 1)
	assert.Equal(t, brand.Name, images[0].Name)
	assert.Equal(t, values.Image.Registry+"/"+values.Image.Repository, images[0].Image, "the repository of the image, without a tag")
	assert.Equal(t, []string{"linux/amd64", "linux/arm64"}, images[0].Platforms, "the platforms of the published index")
}
