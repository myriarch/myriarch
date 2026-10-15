// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/myriarch/myriarch/internal/store/postgres"
)

// The design document (wrapped text is compared as one line) quotes the messages that an operator sees when the schema does not fit, and the exit codes of migrate. A
// change of the code has to change the document in the same pull request (docs/design is normative).
func TestSchemaMessagesAndExitCodesAreDocumented(t *testing.T) {
	doc := oneLine(section(t, readDoc(t, "docs/design/opamp-server.md"), "Start, migrations and schema compatibility"))

	// distinct numbers stand in for the placeholders of the document
	const v, c, s = 901, 902, 903
	templated := func(msg string) string {
		return strings.NewReplacer(fmt.Sprint(v), "<V>", fmt.Sprint(c), "<C>", fmt.Sprint(s), "<S>").Replace(msg)
	}
	status := postgres.SchemaStatus{Version: v, Level: c, Supported: s}
	for name, msg := range map[string]string{
		"the message of an incompatible schema":                 (&postgres.IncompatibleSchemaError{Status: status}).Error(),
		"the message of an older schema without migrateOnStart": (&postgres.OutdatedSchemaError{Status: status}).Error(),
		"the warning for a newer compatible schema":             postgres.NewerSchemaWarning,
	} {
		assert.Contains(t, doc, templated(msg), "docs/design/opamp-server.md does not quote %s", name)
	}

	for _, want := range []string{
		fmt.Sprintf("%d `--check` found pending migrations", exitMigrationsPending),
		fmt.Sprintf("%d the schema is incompatible with this binary", exitSchemaIncompatible),
		"schema version before: <V>", "schema version after: <V>", "compatibility level: <C>",
		"`status` (`current`, `pending`, `newer` or `incompatible`)",
	} {
		assert.Contains(t, doc, want)
	}

	// the other places that name the messages quote the same stems
	troubleshooting := oneLine(readDoc(t, "docs/operations/troubleshooting.md"))
	for _, want := range []string{
		"the database schema is incompatible with this myriarchd",
		"the database schema is at version N but this myriarchd needs version M and database.migrateOnStart is false",
		"the database schema is newer than this myriarchd but compatible with it",
	} {
		assert.Contains(t, troubleshooting, want)
	}
	dev := oneLine(readDoc(t, "docs/development.md"))
	assert.Contains(t, dev, fmt.Sprintf("returns %d for a schema that is incompatible with the binary, and with `--check` %d for pending migrations", exitSchemaIncompatible, exitMigrationsPending))
}
