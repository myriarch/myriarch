// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/myriarch/myriarch/internal/config"
)

// devBannerInterval repeats the dev mode warning while the server runs.
const devBannerInterval = 30 * time.Minute

// isLoopbackHost reports whether host names the local machine only. An empty
// host (":8080") listens on all interfaces and is therefore not loopback.
func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	if strings.HasPrefix(host, "/") { // unix socket directory
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// devNonLocalReasons lists the reasons why `myriarchd dev` must not run with
// these settings unless --allow-nonlocal is given: the dev mode ships well-known
// logins and a well-known enrollment token and writes them into the database,
// so it must stay on the local machine. cfg is the configuration as loaded
// from the optional -c file (before the dev flags are applied).
func devNonLocalReasons(cfg config.Config, o devOptions) []string {
	var reasons []string
	if cfg.Auth.OIDC.Enabled {
		reasons = append(reasons, "auth.oidc.enabled is set (a real identity provider belongs to `myriarchd serve`)")
	}
	if cfg.Server.TLS.CertFile != "" {
		reasons = append(reasons, "server.tls.certFile is set (a TLS deployment belongs to `myriarchd serve`)")
	}
	if cfg.Server.PublicURL != "" {
		if u, err := url.Parse(cfg.Server.PublicURL); err == nil {
			if u.Scheme == "https" {
				reasons = append(reasons, "server.publicUrl is an https URL")
			} else if !isLoopbackHost(u.Hostname()) {
				reasons = append(reasons, "server.publicUrl is not a local address ("+u.Hostname()+")")
			}
		}
	}
	if cfg.Server.MetricsListenAddr != "" {
		if host, _, err := net.SplitHostPort(cfg.Server.MetricsListenAddr); err != nil || !isLoopbackHost(host) {
			reasons = append(reasons, fmt.Sprintf("server.metricsListenAddr %q is not a loopback address (the metrics listener is unauthenticated)", cfg.Server.MetricsListenAddr))
		}
	}
	if host, _, err := net.SplitHostPort(o.listen); err != nil || !isLoopbackHost(host) {
		reasons = append(reasons, fmt.Sprintf("--listen %q is not a loopback address (use 127.0.0.1:<port>)", o.listen))
	}
	if o.dsn != "" {
		if pc, err := pgconn.ParseConfig(o.dsn); err != nil {
			reasons = append(reasons, "--dsn cannot be parsed")
		} else {
			hosts := []string{pc.Host}
			for _, f := range pc.Fallbacks {
				hosts = append(hosts, f.Host)
			}
			for _, h := range hosts {
				if !isLoopbackHost(h) {
					reasons = append(reasons, fmt.Sprintf("--dsn points to a database on a non-local host (%s)", h))
					break
				}
			}
		}
	}
	return reasons
}

// logDevBanner writes the dev mode warning now and repeats it periodically.
// The credentials and the DSN are only logged here, marked DEV ONLY.
func logDevBanner(ctx context.Context, logger *slog.Logger, attrs ...any) {
	emit := func() {
		logger.Warn("DEV MODE: well-known logins and enrollment token are active - NEVER expose this server; use `myriarchd serve` in production", attrs...)
	}
	emit()
	go func() {
		t := time.NewTicker(devBannerInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				emit()
			}
		}
	}()
}

// maskDSN hides the password of a DSN for logs.
func maskDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}
