// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

const dbDSN = "postgres://myriarch:not-a-real-password@db.example.com:5432/myriarch?sslmode=verify-full"

func problemTexts(err error) string {
	var out []string
	for _, p := range Problems(err) {
		out = append(out, p.String())
	}
	return strings.Join(out, "\n")
}

// A file with the database section alone is enough: no public URL, no login path, no secrets of other features.
func TestLoadDatabaseNeedsOnlyTheDatabaseSection(t *testing.T) {
	p := writeFile(t, "db.yaml", "database:\n  dsn: "+dbDSN+"\n")
	db, warnings, err := loadDatabase(p, env(nil))
	require.NoError(t, err)
	assert.Equal(t, dbDSN, db.DSN)
	assert.Equal(t, []string{"database.dsn is an inline secret (a literal value in the configuration file); provide it through the matching *File setting or an environment variable"}, warnings,
		"a verified connection has no transport warning, but a DSN written into the file is an inline secret")
	// the defaults of the section apply
	assert.Equal(t, int32(20), db.MaxConns)
	assert.True(t, db.MigrateOnStart)
	assert.Equal(t, 15*time.Second, db.StatementTimeout)
}

// The DSN comes from the environment alone as well; the variable wins over the file.
func TestLoadDatabaseFromTheEnvironment(t *testing.T) {
	db, _, err := loadDatabase("", env(map[string]string{"MYRIARCH_DATABASE_DSN": dbDSN, "MYRIARCH_DATABASE_MAXCONNS": "7", "MYRIARCH_DATABASE_STATEMENTTIMEOUT": "5s"}))
	require.NoError(t, err)
	assert.Equal(t, dbDSN, db.DSN)
	assert.Equal(t, int32(7), db.MaxConns)
	assert.Equal(t, 5*time.Second, db.StatementTimeout)

	p := writeFile(t, "db.yaml", "database:\n  dsn: postgres://file@db.example.com/x?sslmode=verify-full\n")
	db, _, err = loadDatabase(p, env(map[string]string{"MYRIARCH_DATABASE_DSN": dbDSN}))
	require.NoError(t, err)
	assert.Equal(t, dbDSN, db.DSN, "the environment variable wins over the file")
}

func TestLoadDatabaseExpandsVariablesAndReadsTheDSNFile(t *testing.T) {
	p := writeFile(t, "db.yaml", "database:\n  dsn: postgres://${DB_USER}@db.example.com/${DB_NAME:-myriarch}?sslmode=verify-full\n")
	db, _, err := loadDatabase(p, env(map[string]string{"DB_USER": "migrator"}))
	require.NoError(t, err)
	assert.Equal(t, "postgres://migrator@db.example.com/myriarch?sslmode=verify-full", db.DSN)

	dsnFile := writeFile(t, "dsn", "  "+dbDSN+"\n")
	p = writeFile(t, "db.yaml", "database:\n  dsn: postgres://ignored@db.example.com/x?sslmode=verify-full\n  dsnFile: "+filepath.ToSlash(dsnFile)+"\n")
	db, _, err = loadDatabase(p, env(nil))
	require.NoError(t, err)
	assert.Equal(t, dbDSN, db.DSN, "the content of the DSN file (without white space) wins over the inline value")
}

func TestLoadDatabaseFileProblems(t *testing.T) {
	empty := writeFile(t, "empty", "\n")
	missing := filepath.Join(t.TempDir(), "missing")
	for name, tc := range map[string]struct {
		yaml string
		env  map[string]string
		want string
	}{
		"no DSN at all":                {"database: {}\n", nil, "database.dsn: is required (or database.dsnFile)"},
		"an empty database key":        {"database:\n", nil, "database.dsn: is required (or database.dsnFile)"},
		"no database section":          {"server:\n  listenAddr: :1\n", nil, "database.dsn: is required (or database.dsnFile)"},
		"an empty DSN file":            {"database:\n  dsnFile: " + filepath.ToSlash(empty) + "\n", nil, "database.dsnFile: file " + filepath.ToSlash(empty) + " is empty"},
		"a missing DSN file":           {"database:\n  dsnFile: " + filepath.ToSlash(missing) + "\n", nil, "database.dsnFile: cannot read " + filepath.ToSlash(missing)},
		"an invalid DSN":               {"database:\n  dsn: \"postgres://user:secretpw@host:badport/db\"\n", nil, "database.dsn: is not a valid PostgreSQL connection string"},
		"too few connections":          {"database:\n  dsn: " + dbDSN + "\n  maxConns: 3\n", nil, "database.maxConns: must be at least 5"},
		"a negative timeout":           {"database:\n  dsn: " + dbDSN + "\n  statementTimeout: -1s\n", nil, "database.statementTimeout: must not be negative"},
		"an unknown key":               {"database:\n  dsn: " + dbDSN + "\n  dsnFlie: /x\n", nil, "line 3: field dsnFlie not found in type config.DatabaseConfig"},
		"a plain number as a duration": {"database:\n  dsn: " + dbDSN + "\n  statementTimeout: 15\n", nil, "line 3: cannot unmarshal !!int `15` into time.Duration"},
		"a list instead of a mapping":  {"database: [1, 2]\n", nil, "line 1: cannot unmarshal !!seq into config.DatabaseConfig"},
		"invalid YAML":                 {"database: [\n", nil, "yaml"},
		"two database sections":        {"database:\n  dsn: " + dbDSN + "\ndatabase:\n  maxConns: 9\n", nil, `mapping key "database" already defined`},
		"a malformed override":         {"database:\n  dsn: " + dbDSN + "\n", map[string]string{"MYRIARCH_DATABASE_MAXCONNS": "many"}, "environment MYRIARCH_DATABASE_MAXCONNS: invalid integer"},
	} {
		t.Run(name, func(t *testing.T) {
			p := writeFile(t, "db.yaml", tc.yaml)
			_, _, err := loadDatabase(p, env(tc.env))
			require.Error(t, err)
			assert.Contains(t, err.Error()+"\n"+problemTexts(err), tc.want)
			assert.NotContains(t, err.Error(), "secretpw", "an error never repeats the connection string")
			assert.NotContains(t, problemTexts(err), "secretpw")
		})
	}

	t.Run("a file that cannot be read", func(t *testing.T) {
		_, _, err := loadDatabase(filepath.Join(t.TempDir(), "nope.yaml"), env(nil))
		var re *ReadError
		require.True(t, errors.As(err, &re), "%v", err)
	})
	t.Run("the problems of the section are listed together", func(t *testing.T) {
		p := writeFile(t, "db.yaml", "database:\n  maxConns: 1\n  statementTimeout: -1s\n")
		_, _, err := loadDatabase(p, env(nil))
		require.Error(t, err)
		var ve *domain.ValidationError
		require.True(t, errors.As(err, &ve))
		assert.Len(t, Problems(err), 3, problemTexts(err))
	})
}

// YAML anchors work like in the full loader: the section may be an alias of a mapping defined elsewhere in the file.
func TestLoadDatabaseAcceptsAnAliasAsTheSection(t *testing.T) {
	p := writeFile(t, "db.yaml", "defaults: &db\n  dsn: "+dbDSN+"\n  maxConns: 6\ndatabase: *db\n")
	db, _, err := loadDatabase(p, env(nil))
	require.NoError(t, err)
	assert.Equal(t, dbDSN, db.DSN)
	assert.Equal(t, int32(6), db.MaxConns)
}

// Everything except the database settings is ignored: sections that the server would reject, values of the wrong type,
// secret files that are not mounted, and malformed overrides of other settings.
func TestLoadDatabaseIgnoresEverythingElse(t *testing.T) {
	p := writeFile(t, "myriarchd.yaml", `
server:
  publicUrl: http://not-https.example.com
  listenAddr: ""
  somethingRemovedInThisVersion: true
opamp:
  maxConnectionsPerReplica: -5
  enrollmentTokens:
    - name: one
      tokenFile: /does/not/exist
auth: [not, a, mapping]
gitops:
  enabled: true
  repoUrl: dir:/x
notifications:
  channels:
    - name: n
      urlFile: /does/not/exist
unknownSection:
  a: b
database:
  dsn: `+dbDSN+`
  maxConns: 9
`)
	db, _, err := loadDatabase(p, env(map[string]string{
		"MYRIARCH_SERVER_SHUTDOWNTIMEOUT": "forever",
		"MYRIARCH_OPAMP_OFFLINEAFTER":     "soon",
		"MYRIARCH_AUTH_OIDC_ENABLED":      "maybe",
	}))
	require.NoError(t, err)
	assert.Equal(t, dbDSN, db.DSN)
	assert.Equal(t, int32(9), db.MaxConns)

	// the full loader rejects the same file: the difference is the point
	_, err = load(p, false, env(nil))
	require.Error(t, err)
}

func TestLoadDatabaseWarnings(t *testing.T) {
	p := writeFile(t, "db.yaml", "database:\n  dsn: postgres://myriarch:pw@db.example.com/myriarch?sslmode=disable\n")
	_, warnings, err := loadDatabase(p, env(nil))
	require.NoError(t, err)
	require.Len(t, warnings, 2, strings.Join(warnings, "\n"))
	assert.Contains(t, warnings[0], "database.dsn: the connection to a remote database uses sslmode=disable")
	assert.Equal(t, "database.dsn is an inline secret (a literal value in the configuration file); provide it through the matching *File setting or an environment variable", warnings[1])
	for _, w := range warnings {
		assert.NotContains(t, w, "myriarch:pw", "a warning names the setting, never the value")
	}

	// a reference to the environment is not an inline secret, and a local database has no transport warning
	p = writeFile(t, "db.yaml", "database:\n  dsn: ${MIGRATE_DSN}\n")
	_, warnings, err = loadDatabase(p, env(map[string]string{"MIGRATE_DSN": "postgres://myriarch:pw@localhost/myriarch?sslmode=disable"}))
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

// The decoding of the section is the decoding of the full loader: the same value in the same file gives the same result.
func TestLoadDatabaseAgreesWithTheFullLoader(t *testing.T) {
	content := strings.Replace(prodMinimal, "dsn: postgres://u:p@db/myriarch?sslmode=verify-full",
		"dsn: postgres://u:p@db/myriarch?sslmode=verify-full\n  maxConns: 11\n  migrateOnStart: false\n  statementTimeout: 2m", 1)
	p := writeFile(t, "myriarchd.yaml", content)
	full, err := load(p, false, env(nil))
	require.NoError(t, err)
	db, _, err := loadDatabase(p, env(nil))
	require.NoError(t, err)
	assert.Equal(t, full.Database, db)
}
