// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package clidoc

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toyTree returns a small command tree: toy (serve, config (check), version) plus a hidden and a deprecated command.
func toyTree() *cobra.Command {
	root := &cobra.Command{Use: "toy", Short: "Toy server <for tests>"}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the server",
		Long:  "Runs the server.\nOutput: \"OK: <file> is valid\".",
		Run:   func(*cobra.Command, []string) {},
	}
	serve.Flags().StringP("config", "c", "", "path to the configuration file")
	serve.Flags().Int("port", 8080, "listen port")
	cfg := &cobra.Command{Use: "config", Short: "Inspect a configuration"}
	cfg.AddCommand(&cobra.Command{Use: "check", Short: "Check it.", Run: func(*cobra.Command, []string) {}})
	hidden := &cobra.Command{Use: "secret", Short: "Hidden", Hidden: true, Run: func(*cobra.Command, []string) {}}
	old := &cobra.Command{Use: "old", Short: "Old", Deprecated: "use serve", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(serve, cfg, hidden, old, &cobra.Command{Use: "version", Short: "Print the version", Run: func(*cobra.Command, []string) {}})
	root.PersistentFlags().Bool("verbose", false, "verbose output")
	return root
}

func names(files map[string][]byte) []string {
	var out []string
	for n := range files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestRenderPagesAndNames(t *testing.T) {
	files, err := Render(toyTree())
	require.NoError(t, err)
	assert.Equal(t, []string{"_index.md", "toy.md", "toy_config.md", "toy_config_check.md", "toy_serve.md", "toy_version.md"}, names(files))
	for n, b := range files {
		s := string(b)
		assert.True(t, strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\n\n"), "%s ends with exactly one newline", n)
		assert.True(t, strings.HasPrefix(s, "---\ntitle: \""), "%s starts with front matter", n)
		assert.NotContains(t, s, "Auto generated", "%s has no generator footer", n)
	}
}

func TestRenderFrontMatterAndBody(t *testing.T) {
	files, err := Render(toyTree())
	require.NoError(t, err)

	root := string(files["toy.md"])
	assert.Contains(t, root, "---\ntitle: \"toy\"\nweight: 10\ndescription: \"Toy server <for tests>.\"\n---\n\n## toy\n")
	assert.Contains(t, root, "Toy server &lt;for tests>\n", "angle brackets of the body are escaped")
	assert.Contains(t, root, "- [toy config](toy_config.md) - Inspect a configuration", "links are bare file names, bullets use a dash and no tab")
	assert.NotContains(t, root, "secret")
	assert.NotContains(t, root, "toy old")

	// depth-first order: toy 10, config 20, config check 30, serve 40, version 50
	assert.Contains(t, string(files["toy_config.md"]), "title: \"toy config\"\nweight: 20\n")
	assert.Contains(t, string(files["toy_config_check.md"]), "title: \"toy config check\"\nweight: 30\ndescription: \"Check it.\"\n")
	assert.Contains(t, string(files["toy_serve.md"]), "weight: 40\n")
	assert.Contains(t, string(files["toy_version.md"]), "weight: 50\n")

	serve := string(files["toy_serve.md"])
	assert.Contains(t, serve, "### Synopsis")
	assert.Contains(t, serve, "Output: \"OK: &lt;file> is valid\".")
	assert.Contains(t, serve, "-c, --config string")
	assert.Contains(t, serve, "--port int")
	assert.Contains(t, serve, "### Options inherited from parent commands")
	assert.Contains(t, serve, "--verbose")
}

func TestRenderIndex(t *testing.T) {
	files, err := Render(toyTree(), &cobra.Command{Use: "other", Short: "Other | tool", Run: func(*cobra.Command, []string) {}})
	require.NoError(t, err)
	idx := string(files[IndexFile])
	assert.True(t, strings.HasPrefix(idx, "---\ntitle: \"CLI reference\"\nweight: 20\ndescription: \""))
	assert.Contains(t, idx, "| [toy](toy.md) | Toy server &lt;for tests> |")
	assert.Contains(t, idx, "| [other](other.md) | Other \\| tool |")
	assert.Contains(t, idx, "make docs-gen")
	assert.Contains(t, idx, "alias myr=myrctl")
	// the second root continues the depth-first numbering
	assert.Contains(t, string(files["other.md"]), "weight: 60\n")
}

func TestRenderDeterministic(t *testing.T) {
	a, err := Render(toyTree())
	require.NoError(t, err)
	b, err := Render(toyTree())
	require.NoError(t, err)
	assert.Equal(t, a, b)
	tree := toyTree()
	c, err := Render(tree)
	require.NoError(t, err)
	d, err := Render(tree) // the same tree twice: the help command that cobra adds must not change anything
	require.NoError(t, err)
	assert.Equal(t, c, d)
	assert.Equal(t, a, c)
}

func TestRenderRestoresTexts(t *testing.T) {
	tree := toyTree()
	_, err := Render(tree)
	require.NoError(t, err)
	assert.Equal(t, "Toy server <for tests>", tree.Short)
	serve, _, err := tree.Find([]string{"serve"})
	require.NoError(t, err)
	assert.Contains(t, serve.Long, "<file>")
	assert.True(t, serve.DisableAutoGenTag)
}

func TestRenderDuplicateFileName(t *testing.T) {
	a := &cobra.Command{Use: "same", Short: "A", Run: func(*cobra.Command, []string) {}}
	b := &cobra.Command{Use: "same", Short: "B", Run: func(*cobra.Command, []string) {}}
	_, err := Render(a, b)
	require.Error(t, err)
}

func TestLintClean(t *testing.T) {
	in := "intro\twith tab\n\n```\ntoy serve [flags]\n```\n\n### Options\n\n```\n  -h, --help\t help\n```\n\n```sh\necho hi\n```\n\n### SEE ALSO\n\n* [toy](toy.md)\t - Toy server\n* not a link\n"
	want := "intro with tab\n\n```text\ntoy serve [flags]\n```\n\n### Options\n\n```text\n  -h, --help\t help\n```\n\n```sh\necho hi\n```\n\n### SEE ALSO\n\n- [toy](toy.md) - Toy server\n- not a link\n"
	assert.Equal(t, want, lintClean(in), "fences without a language get text, tabs inside fences stay, bullets use a dash")
	assert.Equal(t, want, lintClean(want), "idempotent")
}

func TestRenderIsLintClean(t *testing.T) {
	files, err := Render(toyTree())
	require.NoError(t, err)
	for n, b := range files {
		s := string(b)
		open := false
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "```") {
				if !open {
					assert.NotEqual(t, "```", line, n+": an opening fence without a language")
				}
				open = !open
			}
		}
		assert.NotRegexp(t, `(?m)^\* `, s, n+": a star bullet")
	}
	serve := string(files["toy_serve.md"])
	assert.Contains(t, serve, "```text\ntoy serve [flags]\n```")
	assert.Contains(t, string(files["toy.md"]), "- [toy config](toy_config.md) - Inspect a configuration")
}
