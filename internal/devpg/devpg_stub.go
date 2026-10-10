// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !devpg

package devpg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/myriarch/myriarch/internal/config"
)

// ErrNotBuiltIn is returned by Start in builds without the devpg build tag.
var ErrNotBuiltIn = errors.New("embedded PostgreSQL is not built in")

// Start always fails in builds without the devpg build tag: the embedded
// PostgreSQL is not linked into the binary. The error wraps ErrNotBuiltIn.
func Start(_ context.Context, _ config.DevConfig, _ *slog.Logger) (dsn string, stop func() error, err error) {
	return "", func() error { return nil }, fmt.Errorf(
		"%w: this myriarchd was built without embedded PostgreSQL (build tag devpg): "+
			"start `myriarchd dev --dsn postgres://...` against an external PostgreSQL, "+
			"or rebuild with -tags devpg (make build / make dev do this)", ErrNotBuiltIn)
}
