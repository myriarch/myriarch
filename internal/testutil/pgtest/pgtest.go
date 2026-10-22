// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package pgtest runs one embedded PostgreSQL server per test binary (i.e. per
// Go package under `go test ./...`) and hands out isolated databases cloned
// from a migrated template.
//
// Verified on darwin/arm64 with embedded-postgres v1.34.0
// (docs/design/upstream-notes.md, PostgreSQL facts). Usage in a package that
// needs a database:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }
//
//	func TestSomething(t *testing.T) {
//		pool := pgtest.NewPool(t) // isolated database with the full schema
//		...
//	}
//
// Design (all of these address real pitfalls of fergusstrange/embedded-postgres):
//   - random free TCP port per process (default 5432 collides with a local PG and
//     with other test packages running in parallel),
//   - per-process RuntimePath/DataPath under a fresh temp dir (Start() does
//     os.RemoveAll(RuntimePath)!),
//   - shared, version-keyed BinariesPath guarded by a cross-process flock and a
//     ".complete" marker (the library's own mutex is process-local, so parallel
//     test binaries could see a half-extracted tree),
//   - Unix sockets disabled (TCP only): avoids /tmp/.s.PGSQL.<port> clashes and the
//     103-byte sun_path limit on macOS when temp dirs are deep,
//   - Locale C / UTF8 so the server does not inherit e.g. de_DE.UTF-8 from the shell,
//   - a detached watchdog (/bin/sh) that stops the server and deletes the temp dir if
//     the test binary dies without calling Stop (test panic, -timeout, SIGINT/SIGKILL).
//
// External server (CI, no embedded PostgreSQL): with PGTEST_DSN set, e.g.
//
//	PGTEST_DSN='postgres://postgres:secret@127.0.0.1:5432/postgres?sslmode=disable'
//
// no embedded server is started. The test binary uses that server instead
// (the role needs CREATEDB; a superuser is recommended): it creates a template
// database "myriarch_tmpl_<pid>_<random>" (unique per test binary, since
// `go test ./...` runs many binaries against the same server), migrates it and
// clones it per test like the embedded variant. The DSN of the test databases
// is PGTEST_DSN with the database name replaced (user, password and parameters
// stay). At the end the template and all test databases of the binary are
// dropped. A binary that is killed leaves its databases behind (names
// myriarch_tmpl_* and t_*): drop them or throw the server away. Tests of
// internal/devpg, which start an embedded server themselves, skip.
// PGTEST_SKIP=1 still skips all database tests.
package pgtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myriarch/myriarch/internal/brand"
)

// ExternalDSNEnv is the environment variable that selects an external server.
const ExternalDSNEnv = "PGTEST_DSN"

// ExternalDSN returns the DSN of the external server ("" = embedded server).
func ExternalDSN() string { return strings.TrimSpace(os.Getenv(ExternalDSNEnv)) }

// Version is the PostgreSQL version used for tests. The library constants lag
// behind Maven (V17 = 17.5.0), but any published version string works.
var Version = embeddedpostgres.PostgresVersion(envOr("PGTEST_PG_VERSION", string(embeddedpostgres.V17)))

const (
	user     = brand.Name
	password = brand.Name
	adminDB  = "postgres"
)

// Server is one running embedded PostgreSQL instance.
type Server struct {
	Port    uint32
	BaseDir string // per-process temp dir (runtime + data); removed by Stop
	BinDir  string // shared extracted binaries
	Timings map[string]time.Duration

	ep       *embeddedpostgres.EmbeddedPostgres
	log      *bytes.Buffer
	template string
	seq      atomic.Int64

	// external server (PGTEST_DSN): the DSN with the admin database, a random
	// part for the names of this binary's databases and the databases to drop.
	external string
	adminCfg *pgx.ConnConfig
	suffix   string
	tlsCert  string // certificate of the embedded server with PGTEST_TLS (trust anchor of the tests)
	mu       sync.Mutex
	created  []string
}

// Start boots a server (or connects to the external one of PGTEST_DSN). Call
// Stop when done (typically deferred in TestMain).
func Start(ctx context.Context) (*Server, error) {
	if dsn := ExternalDSN(); dsn != "" {
		return startExternal(ctx, dsn)
	}
	cacheDir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	binDir := filepath.Join(cacheDir, fmt.Sprintf("pg-%s-%s-%s", Version, runtime.GOOS, runtime.GOARCH))

	tmpRoot := os.Getenv("PGTEST_TMPDIR") // optional; default os.TempDir()
	base, err := os.MkdirTemp(tmpRoot, "pgtest-")
	if err != nil {
		return nil, err
	}

	s := &Server{BaseDir: base, BinDir: binDir, Timings: map[string]time.Duration{}}

	// Retry a few times: picking a free port and binding it later is racy.
	attempts, fixed := 3, uint32(0)
	if v := os.Getenv("PGTEST_FIXED_PORT"); v != "" { // only to demonstrate collisions
		p, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return nil, err
		}
		attempts, fixed = 1, uint32(p)
	}
	startParams := map[string]string{
		"unix_socket_directories": "", // TCP only
		"fsync":                   "off",
		"synchronous_commit":      "off",
		"full_page_writes":        "off",
		"max_connections":         "100",
		"timezone":                "UTC", // initdb otherwise bakes in the host TZ (e.g. Europe/Berlin)
		"log_timezone":            "UTC",
	}
	if os.Getenv(TLSEnv) != "" {
		tf, err := writeTLSFiles(base)
		if err != nil {
			_ = os.RemoveAll(base)
			return nil, fmt.Errorf("tls certificate: %w", err)
		}
		startParams["ssl"], startParams["ssl_cert_file"], startParams["ssl_key_file"] = "on", tf.cert, tf.key
		s.tlsCert = tf.cert
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		if fixed != 0 {
			port = fixed
		}
		s.Port = port
		s.log = &bytes.Buffer{}
		cfg := embeddedpostgres.DefaultConfig().
			Version(Version).
			Port(port).
			Username(user).
			Password(password).
			Database(adminDB).
			CachePath(cacheDir).
			RuntimePath(filepath.Join(base, "run")).
			DataPath(filepath.Join(base, "run", "data")).
			BinariesPath(binDir).
			StartTimeout(60 * time.Second).
			Logger(s.log).
			StartParameters(startParams)
		if os.Getenv("PGTEST_INHERIT_LOCALE") == "" {
			// Without this initdb inherits LANG/LC_* of the developer shell (e.g. de_DE.UTF-8).
			cfg = cfg.Locale("C").Encoding("UTF8")
		}
		s.ep = embeddedpostgres.NewDatabase(cfg)

		t0 := time.Now()
		lastErr = withBinariesLock(binDir, func() error { return s.ep.Start() })
		s.Timings["ep.Start"] = time.Since(t0)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		_ = os.RemoveAll(base)
		return nil, fmt.Errorf("embedded postgres: %w\n--- postgres log ---\n%s", lastErr, s.log.String())
	}

	if err := s.spawnWatchdog(); err != nil {
		_ = s.Stop()
		return nil, err
	}
	return s, nil
}

// startExternal connects to the server of PGTEST_DSN.
func startExternal(ctx context.Context, dsn string) (*Server, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid connection string: %w", ExternalDSNEnv, errors.New(redactParseError(err)))
	}
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	s := &Server{
		Timings: map[string]time.Duration{}, external: dsn, adminCfg: cfg, Port: uint32(cfg.Port),
		suffix: fmt.Sprintf("%d_%s", os.Getpid(), hex.EncodeToString(rnd)),
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.adminExec(cctx, "SELECT 1"); err != nil {
		return nil, fmt.Errorf("%s: cannot connect to %s:%d: %w", ExternalDSNEnv, cfg.Host, cfg.Port, err)
	}
	return s, nil
}

// redactParseError removes the connection string (it holds the password) from a
// parse error.
func redactParseError(err error) string {
	var pe interface{ Unwrap() error }
	if errors.As(err, &pe) && pe.Unwrap() != nil {
		return pe.Unwrap().Error()
	}
	return "invalid format"
}

// IsExternal reports whether the server is the external one of PGTEST_DSN.
func (s *Server) IsExternal() bool { return s.external != "" }

var dbnameKV = regexp.MustCompile(`(^|\s)(dbname|database)\s*=\s*('([^'\\]|\\.)*'|\S+)`)

// DSN returns a connection string for the given database. With an external
// server it is PGTEST_DSN with the database replaced.
func (s *Server) DSN(db string) string {
	if s.external == "" {
		return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?%s", user, password, s.Port, db, s.sslQuery())
	}
	if u, err := url.Parse(s.external); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path, u.RawPath = "/"+db, ""
		return u.String()
	}
	// keyword/value form
	repl := "${1}dbname=" + strings.ReplaceAll(quoteKV(db), "$", "$$")
	if dbnameKV.MatchString(s.external) {
		return dbnameKV.ReplaceAllString(s.external, repl)
	}
	return s.external + " dbname=" + quoteKV(db)
}

func quoteKV(v string) string {
	if v != "" && !strings.ContainsAny(v, " '\\") {
		return v
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// databaseName returns the name of the database that a template or a test
// database gets: unique per test binary on an external server.
func (s *Server) databaseName(prefix string) string {
	if s.external != "" {
		return fmt.Sprintf("%s_%s_%d", prefix, s.suffix, s.seq.Add(1))
	}
	return fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), s.seq.Add(1))
}

// PrepareTemplate creates database `name`, runs migrate against it and marks it as
// a template so NewDatabase can clone it cheaply (CREATE DATABASE ... TEMPLATE).
func (s *Server) PrepareTemplate(ctx context.Context, name string, migrate func(context.Context, *pgxpool.Pool) error) error {
	t0 := time.Now()
	if s.external != "" {
		name = fmt.Sprintf("%s_%s", name, s.suffix) // unique per test binary
	}
	coll, err := collationClause()
	if err != nil {
		return err
	}
	if err := s.createDatabase(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" "+coll, name); err != nil {
		if coll != "" {
			return fmt.Errorf("%w (%s=%q)", err, CollationEnv, os.Getenv(CollationEnv))
		}
		return err
	}
	pool, err := pgxpool.New(ctx, s.DSN(name))
	if err != nil {
		return err
	}
	err = migrate(ctx, pool)
	pool.Close() // template must have no open connections
	if err != nil {
		return err
	}
	if err := s.adminExec(ctx, "ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH IS_TEMPLATE true"); err != nil {
		return err
	}
	s.template = name
	s.Timings["template+migrate"] = time.Since(t0)
	return nil
}

// NewDatabase clones the template into a fresh database and returns a pool for it.
// The cleanup func closes the pool and drops the database.
func (s *Server) NewDatabase(ctx context.Context) (*pgxpool.Pool, func(), error) {
	if s.template == "" {
		return nil, nil, errors.New("PrepareTemplate not called")
	}
	name := s.databaseName("t")
	if err := s.createDatabase(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s",
		pgx.Identifier{name}.Sanitize(), pgx.Identifier{s.template}.Sanitize()), name); err != nil {
		return nil, nil, err
	}
	pool, err := pgxpool.New(ctx, s.DSN(name))
	if err != nil {
		return nil, nil, err
	}
	return pool, func() {
		pool.Close()
		s.dropDatabase(context.Background(), name)
	}, nil
}

// NewEmptyDatabase creates a database without any schema (with the collation of
// PGTEST_COLLATION) and returns a pool for it. The cleanup func closes the pool
// and drops the database.
func (s *Server) NewEmptyDatabase(ctx context.Context) (*pgxpool.Pool, func(), error) {
	coll, err := collationClause()
	if err != nil {
		return nil, nil, err
	}
	name := s.databaseName("e")
	if err := s.createDatabase(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" "+coll, name); err != nil {
		return nil, nil, err
	}
	pool, err := pgxpool.New(ctx, s.DSN(name))
	if err != nil {
		s.dropDatabase(context.Background(), name)
		return nil, nil, err
	}
	return pool, func() {
		pool.Close()
		s.dropDatabase(context.Background(), name)
	}, nil
}

// createDatabase runs a CREATE DATABASE statement and remembers the database
// (external server: dropped at Stop). While another session works on the
// template (SQLSTATE 55006, object in use: several test binaries or parallel
// tests clone at the same moment) it is repeated with a growing delay.
func (s *Server) createDatabase(ctx context.Context, stmt, name string) error {
	delay := 20 * time.Millisecond
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := s.adminExec(ctx, stmt)
		var pgErr *pgconn.PgError
		if err == nil {
			s.mu.Lock()
			s.created = append(s.created, name)
			s.mu.Unlock()
			return nil
		}
		if !errors.As(err, &pgErr) || pgErr.Code != "55006" || time.Now().After(deadline) {
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		if delay < time.Second {
			delay *= 2
		}
	}
}

// dropDatabase removes a database (also a template) and forgets it.
func (s *Server) dropDatabase(ctx context.Context, name string) {
	id := pgx.Identifier{name}.Sanitize()
	// a template cannot be dropped: ignore the error of the ALTER for ordinary databases
	_ = s.adminExec(ctx, "ALTER DATABASE "+id+" WITH IS_TEMPLATE false")
	_ = s.adminExec(ctx, "DROP DATABASE IF EXISTS "+id+" WITH (FORCE)")
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.created {
		if n == name {
			s.created = append(s.created[:i], s.created[i+1:]...)
			break
		}
	}
}

func (s *Server) adminExec(ctx context.Context, sql string) error {
	var conn *pgx.Conn
	var err error
	if s.external != "" {
		conn, err = pgx.ConnectConfig(ctx, s.adminCfg.Copy())
	} else {
		conn, err = pgx.Connect(ctx, s.DSN(adminDB))
	}
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

// Stop stops the server and removes the per-process directory. With an
// external server it drops the template and the test databases of this binary
// that are still there and leaves the server running.
func (s *Server) Stop() error {
	if s.external != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.mu.Lock()
		left := append([]string(nil), s.created...)
		s.mu.Unlock()
		for _, name := range left {
			s.dropDatabase(ctx, name)
		}
		return nil
	}
	var err error
	if s.ep != nil {
		t0 := time.Now()
		err = s.ep.Stop()
		s.Timings["ep.Stop"] = time.Since(t0)
	}
	if rmErr := os.RemoveAll(s.BaseDir); err == nil {
		err = rmErr
	}
	return err
}

// Log returns everything postgres/initdb/pg_ctl wrote so far.
func (s *Server) Log() string {
	if s.log == nil {
		return ""
	}
	return s.log.String()
}

func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}

func cacheDir() (string, error) {
	if d := os.Getenv("PGTEST_CACHE_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o755)
	}
	d, err := os.UserCacheDir() // ~/Library/Caches on macOS, ~/.cache on Linux
	if err != nil {
		return "", err
	}
	d = filepath.Join(d, brand.Name, "embedded-postgres")
	return d, os.MkdirAll(d, 0o755)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
