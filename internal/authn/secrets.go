// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

const (
	kvDownloadSecret = "auth/download-secret"
	kvCookieSecret   = "auth/cookie-secret"
	secretSize       = 32
)

// loadOrCreateSecret returns the secret stored under key, creating 32 random
// bytes on first use. PutIfAbsent makes concurrent replicas converge on one value.
func loadOrCreateSecret(ctx context.Context, kv store.KVStore, key string) ([]byte, error) {
	v, err := kv.Get(ctx, key)
	if err == nil && len(v) >= secretSize {
		return v, nil
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	b := make([]byte, secretSize)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	if _, err := kv.PutIfAbsent(ctx, key, b); err != nil {
		return nil, err
	}
	v, err = kv.Get(ctx, key) // the winner's value
	if err != nil {
		return nil, err
	}
	if len(v) < secretSize {
		return nil, fmt.Errorf("stored secret %q is too short (%d bytes)", key, len(v))
	}
	return v, nil
}

// LoadOrCreateDownloadSecret returns the HMAC key for artifact download
// tokens: from cfg.DownloadTokenSecretFile if set, otherwise from the KV store
// key "auth/download-secret" (created with 32 random bytes on first use, so
// all replicas share it).
func LoadOrCreateDownloadSecret(ctx context.Context, cfg config.AuthConfig, kv store.KVStore) ([]byte, error) {
	if cfg.DownloadTokenSecretFile != "" {
		b, err := os.ReadFile(cfg.DownloadTokenSecretFile)
		if err != nil {
			return nil, fmt.Errorf("read download token secret file: %w", err)
		}
		// keep the content as is except a trailing newline written by editors/secret tools
		for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
			b = b[:len(b)-1]
		}
		if len(b) < secretSize {
			return nil, fmt.Errorf("download token secret file %s must contain at least %d bytes", cfg.DownloadTokenSecretFile, secretSize)
		}
		return b, nil
	}
	return loadOrCreateSecret(ctx, kv, kvDownloadSecret)
}
