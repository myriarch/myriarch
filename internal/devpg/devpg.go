// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build devpg

package devpg

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
)

// Start starts (or reuses) the embedded server and returns its DSN and a
// stop function.
func Start(ctx context.Context, cfg config.DevConfig, logger *slog.Logger) (dsn string, stop func() error, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	port := cfg.PostgresPort
	if port == 0 {
		port = 54329
	}
	dsn = DSN(port)
	noop := func() error { return nil }

	if pingDSN(ctx, dsn) == nil {
		logger.Info("reusing running embedded postgres", "port", port)
		return dsn, noop, nil
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = ".dev"
	}
	base, err := filepath.Abs(filepath.Join(dataDir, "postgres"))
	if err != nil {
		return "", noop, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", noop, fmt.Errorf("devpg: create data dir: %w", err)
	}
	cacheDir, err := cacheDir()
	if err != nil {
		return "", noop, err
	}
	version := embeddedpostgres.V17
	binDir := filepath.Join(cacheDir, fmt.Sprintf("pg-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH))

	var logBuf lockedBuffer
	ep := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(version).
		Port(port).
		Username(user).
		Password(password).
		Database(database).
		CachePath(cacheDir).
		// RuntimePath is wiped by Start(); the data directory lives next to it
		RuntimePath(filepath.Join(base, "run")).
		DataPath(filepath.Join(base, "data")).
		BinariesPath(binDir).
		Locale("C").Encoding("UTF8").
		StartTimeout(90 * time.Second).
		Logger(&logBuf).
		StartParameters(map[string]string{
			"unix_socket_directories": "", // TCP only
			"max_connections":         "100",
			"timezone":                "UTC",
			"log_timezone":            "UTC",
		}))

	t0 := time.Now()
	if err := withBinariesLock(binDir, ep.Start); err != nil {
		// a concurrent starter may have won the port: reuse its server
		if pingDSN(ctx, dsn) == nil {
			logger.Info("reusing embedded postgres started concurrently", "port", port)
			return dsn, noop, nil
		}
		return "", noop, fmt.Errorf("devpg: start embedded postgres: %w\n--- postgres log ---\n%s", err, tail(logBuf.String(), 4000))
	}
	logger.Info("embedded postgres started", "port", port, "data", filepath.Join(base, "data"), "took", time.Since(t0).Round(time.Millisecond))
	var once sync.Once
	stop = func() error {
		var err error
		once.Do(func() { err = ep.Stop() })
		return err
	}
	return dsn, stop, nil
}

func pingDSN(ctx context.Context, dsn string) error {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(pctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return conn.Ping(pctx)
}

func cacheDir() (string, error) {
	if d := os.Getenv("PGTEST_CACHE_DIR"); d != "" { // shared with the test helper
		return d, os.MkdirAll(d, 0o755)
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("devpg: cache dir: %w", err)
	}
	d = filepath.Join(d, brand.Name, "embedded-postgres")
	return d, os.MkdirAll(d, 0o755)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// lockedBuffer is a bytes.Buffer safe for the concurrent writers of the library.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
