// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/config"
)

// FX-10: the configuration files of the deployment examples
// (deploy/examples/<name>/) match the configuration contract: the same check
// that "myriarchd config validate --skip-files" makes in CI, and the config:
// section of the Helm values decodes without unknown keys.
//
// The tests fail when no example file is found, so a moved or deleted example
// cannot silently disable them.

const examplesGlob = "../../deploy/examples/*"

func TestEnvironmentConfigFilesAreValid(t *testing.T) {
	files, err := filepath.Glob(examplesGlob + "/myriarchd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no deploy/examples/*/myriarchd.yaml found")
	}
	for _, f := range files {
		t.Run(filepath.Base(filepath.Dir(f)), func(t *testing.T) {
			_, warnings, err := LoadWithOptions(f, Options{SkipFiles: true})
			if err != nil {
				for _, p := range Problems(err) {
					t.Errorf("%s: %s", f, p)
				}
				return
			}
			for _, w := range warnings {
				t.Logf("warning: %s", w)
			}
		})
	}
}

func TestEnvironmentHelmValuesDecode(t *testing.T) {
	files, err := filepath.Glob(examplesGlob + "/*values*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob(examplesGlob + "/*/*values*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, more...)
	if len(files) == 0 {
		t.Fatal("no deploy/examples/**/*values*.yaml found")
	}
	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			Config yaml.Node `yaml:"config"`
		}
		if err := yaml.Unmarshal(raw, &values); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if values.Config.Kind == 0 { // no config: section (for example an overlay file)
			continue
		}
		checked++
		cfgYAML, err := yaml.Marshal(&values.Config)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		// the chart's defaults are not part of these files, so only the decoding
		// (known keys, types) is checked, not the whole validation
		cfg := config.Default()
		if err := decodeYAML(cfgYAML, &cfg); err != nil {
			t.Errorf("%s: the config: section does not decode: %v", f, err)
		}
	}
	if checked == 0 {
		t.Fatal("none of the values files has a config: section")
	}
}
