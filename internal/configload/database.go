// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// LoadDatabase loads the database settings and nothing else, for "myriarchd migrate": a job that applies the migrations
// before the server is updated has no server configuration (public URL, login path, secrets of other features) and must not
// need one.
//
// It reads the same sources as Load for these settings, in the same order: the defaults of the database section, the
// section "database" of the YAML file at path (empty: no file), ${VAR} references in its string values, the
// MYRIARCH_DATABASE_* environment variables, and the content of database.dsnFile. Every other section of the file is
// ignored, whatever it holds, and so are the environment variables of other settings and the secret files of other features.
// Inside the database section an unknown key is an error, as everywhere in the configuration.
//
// The checks are the database checks of Validate (a DSN is required, it must parse, maxConns and statementTimeout must be in
// range). The result also holds the warnings that apply to the database settings: an inline DSN and a connection to a remote
// host that does not verify the server certificate. An unreadable file is a *ReadError; see Problems for the other errors.
func LoadDatabase(path string) (config.DatabaseConfig, []string, error) {
	return loadDatabase(path, os.LookupEnv)
}

func loadDatabase(path string, lookup func(string) (string, bool)) (config.DatabaseConfig, []string, error) {
	db := config.Default().Database
	dbValue := reflect.ValueOf(&db).Elem()
	var inline []string
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return db, nil, &ReadError{Err: err}
		}
		if err := decodeDatabaseSection(data, &db); err != nil {
			return db, nil, &fileError{file: path, err: err}
		}
		inline = inlineSecrets(dbValue, "database")
	}
	expandEnv(dbValue, lookup)
	if err := applyEnvOverridesAt(dbValue, []string{"database"}, lookup); err != nil {
		return db, nil, err
	}
	var errs []error
	if err := readSecretFile(&db.DSN, db.DSNFile, "database.dsnFile"); err != nil {
		errs = append(errs, err)
	}
	v := &validator{skipFiles: true}
	v.checkDatabase(db, false)
	if len(v.issues) > 0 {
		errs = append(errs, &domain.ValidationError{Issues: v.issues})
	}
	if len(errs) > 0 {
		return db, nil, errors.Join(errs...)
	}
	var warnings []string
	if w := databaseTransportWarning(db.DSN); w != "" {
		warnings = append(warnings, w)
	}
	for _, p := range inline {
		warnings = append(warnings, p+" is an inline secret (a literal value in the configuration file); provide it through the matching *File setting or an environment variable")
	}
	return db, warnings, nil
}

// decodeDatabaseSection decodes the section "database" of the YAML document in data on top of db. Other top-level keys are
// ignored. An unknown key inside the section is an error that names its line, in the wording of the yaml decoder.
func decodeDatabaseSection(data []byte, db *config.DatabaseConfig) error {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return err
	}
	node, ok := top["database"]
	if !ok {
		return nil
	}
	// a section that is not a mapping is reported in the wording of the full loader, not with the helper type below
	target := &node
	if target.Kind == yaml.AliasNode {
		target = target.Alias
	}
	if target.Kind != yaml.MappingNode && target.ShortTag() != "!!null" {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: cannot unmarshal %s into config.DatabaseConfig", target.Line, target.ShortTag())}}
	}
	section := struct {
		config.DatabaseConfig `yaml:",inline"`
		Unknown               map[string]yaml.Node `yaml:",inline"`
	}{DatabaseConfig: *db}
	if err := node.Decode(&section); err != nil {
		return err
	}
	if len(section.Unknown) > 0 {
		keys := make([]string, 0, len(section.Unknown))
		for k := range section.Unknown {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			li, lj := section.Unknown[keys[i]].Line, section.Unknown[keys[j]].Line
			if li != lj {
				return li < lj
			}
			return keys[i] < keys[j]
		})
		te := &yaml.TypeError{}
		for _, k := range keys {
			te.Errors = append(te.Errors, fmt.Sprintf("line %d: field %s not found in type config.DatabaseConfig", section.Unknown[k].Line, k))
		}
		return te
	}
	*db = section.DatabaseConfig
	return nil
}
