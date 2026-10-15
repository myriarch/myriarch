// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// These tests put the real authentication service (internal/authn) in front of the real handler, so that the
// failure cases of the route are checked as a whole: what the host sees when its token is missing, wrong, revoked or
// expired, and when too many lookups happen.

// realAuthEnv mounts the install routes with authn.Service.AuthenticateInstall.
func realAuthEnv(t *testing.T, opamp func(c *config.OpAMPConfig)) *apiEnv {
	t.Helper()
	e := newAPIEnv(t)
	opampCfg := config.Default().OpAMP
	if opamp != nil {
		opamp(&opampCfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc, err := authn.New(ctx, config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, opampCfg,
		authn.Deps{Store: e.tokens, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	e.remount(func(d *Deps) { d.InstallAuth = svc })
	return e
}

// enrollment creates an enrollment token in the store and returns its secret and ID.
func (e *apiEnv) enrollment(mut ...func(t *domain.EnrollmentToken)) (string, uuid.UUID) {
	e.t.Helper()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	secret := domain.EnrollmentTokenPrefix + hex.EncodeToString(b)
	tok := domain.EnrollmentToken{Name: "t-" + secret[5:9], Hash: domain.SHA256([]byte(secret)), CreatedAt: time.Now()}
	for _, m := range mut {
		m(&tok)
	}
	return secret, e.tokens.Create(tok).ID
}

func TestInstallPlanWithTheRealAuthentication(t *testing.T) {
	for name, opamp := range map[string]func(c *config.OpAMPConfig){
		"the OpAMP endpoint requires a token":      nil,
		"the OpAMP endpoint accepts hosts without": func(c *config.OpAMPConfig) { c.RequireEnrollmentToken = false },
	} {
		t.Run(name, func(t *testing.T) {
			e := realAuthEnv(t, opamp)
			valid, id := e.enrollment()
			revoked, revokedID := e.enrollment()
			e.tokens.Revoke(revokedID, time.Now())
			past := time.Now().Add(-time.Minute)
			expired, _ := e.enrollment(func(t *domain.EnrollmentToken) { t.ExpiresAt = &past })

			// the token that is valid gets its plan
			r := e.readyPlan(valid, datacenterHost())
			require.Equal(t, http.StatusOK, r.Code, r.Body.String())
			assert.Contains(t, r.Body.String(), "fleet=datacenter\n")

			// everything else gets the same 401, whether the token exists or not
			attempts := map[string][]string{
				"no header":      nil,
				"unknown":        {"Authorization", "Bearer myre_" + strings.Repeat("c", 32)},
				"revoked":        {"Authorization", "Bearer " + revoked},
				"expired":        {"Authorization", "Bearer " + expired},
				"malformed":      {"Authorization", "Bearer myre_short"},
				"an API token":   {"Authorization", "Bearer myra_" + strings.Repeat("c", 32)},
				"a grant":        {"Authorization", "Bearer myrd_abc.def"},
				"another scheme": {"Authorization", "Basic " + valid},
			}
			var want string
			for name, hdr := range attempts {
				r := e.planRequest("", datacenterHost(), hdr...)
				require.Equal(t, http.StatusUnauthorized, r.Code, name)
				assert.Contains(t, r.Header().Get("WWW-Authenticate"), "Bearer", name)
				for _, secret := range []string{valid, revoked, expired} {
					assert.NotContains(t, r.Body.String(), secret, name)
				}
				assert.NotContains(t, r.Body.String(), revokedID.String(), name)
				assert.NotContains(t, r.Body.String(), id.String(), name)
				e.expect(r, "getInstallPlan", http.StatusUnauthorized)
				detail := problemOf(t, r).Detail
				if want == "" {
					want = detail
				}
				assert.Equal(t, want, detail, "%s: the detail tells nothing about the token", name)
			}
		})
	}
}

// Failed authentications from one address are limited before any lookup; another address is not affected and the
// agents' lookups stay possible.
func TestInstallPlanLimitsFailedAuthenticationsPerAddress(t *testing.T) {
	e := realAuthEnv(t, nil)
	valid, _ := e.enrollment()

	limited := 0
	for i := 0; i < 40; i++ {
		unknown := "myre_" + strings.Repeat(string(rune('a'+i%26)), 31) + string(rune('A'+i/26))
		r := e.planRequest(unknown, datacenterHost())
		switch r.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			limited++
			assert.NotEmpty(t, r.Header().Get("Retry-After"))
			assert.Equal(t, "too_many_requests", problemOf(t, r).Code)
			e.expect(r, "getInstallPlan", http.StatusTooManyRequests)
		default:
			t.Fatalf("attempt %d: %d %s", i, r.Code, r.Body.String())
		}
	}
	assert.GreaterOrEqual(t, limited, 8, "after the 30 free failures the address is refused")
	assert.Equal(t, float64(limited), e.counter("rate_limited"))
	// refused, even with a valid token, until the budget refills
	r := e.planRequest(valid, datacenterHost())
	assert.Equal(t, http.StatusTooManyRequests, r.Code)
}

// A host that does not use the OpAMP token requirement still needs a token for the plan.
func TestInstallPlanNeedsATokenWhereOpAMPDoesNot(t *testing.T) {
	e := realAuthEnv(t, func(c *config.OpAMPConfig) { c.RequireEnrollmentToken = false })
	r := e.planRequest("", datacenterHost())
	require.Equal(t, http.StatusUnauthorized, r.Code)
	assert.Equal(t, "unauthorized", problemOf(t, r).Code)
	assert.Equal(t, float64(1), e.counter("unauthorized"))
}
