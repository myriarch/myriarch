// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package devpg runs an embedded PostgreSQL for "myriarchd dev"
// (github.com/fergusstrange/embedded-postgres; no container runtime needed).
//
// The embedded server is only part of builds with the "devpg" build tag
// (make build, make dev, the dev/sim container image). Builds without the tag,
// such as the release binaries, contain a stub whose Start returns
// ErrNotBuiltIn; "myriarchd dev --dsn postgres://..." keeps working there
// against an external PostgreSQL.
//
// The PostgreSQL binaries are not part of the myriarchd binary. The first start
// downloads them at runtime through embedded-postgres (from Maven Central) and
// caches them under the user cache directory (myriarch/embedded-postgres, or
// PGTEST_CACHE_DIR); that first start therefore needs network access, later
// starts work offline.
//
// The tagged implementation reuses the hardening of internal/testutil/pgtest
// (locale C, UTC, TCP only, shared binaries dir guarded by a cross-process
// lock) but with a PERSISTENT data dir (<dev.dataDir>/postgres/data) and the
// fixed port dev.postgresPort, so data survives restarts of `myriarchd dev`.
// If a server of a previous (killed) myriarchd dev process still answers on the
// port with the dev credentials, it is reused instead of starting a second one.
package devpg

import (
	"fmt"

	"github.com/myriarch/myriarch/internal/brand"
)

const (
	user     = brand.Name
	password = brand.Name
	database = brand.Name
)

// DSN returns the connection string of the dev database on port.
func DSN(port uint32) string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", user, password, port, database)
}
