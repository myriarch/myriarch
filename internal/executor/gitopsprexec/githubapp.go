// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// renewBefore is how long before expiry an installation token is replaced.
const renewBefore = 5 * time.Minute

// appState holds the installation token of one GitHub App login. It lives in
// memory only and is never logged.
type appState struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

func (e *Executor) appState(key string) *appState {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.apps[key]
	if st == nil {
		st = &appState{}
		e.apps[key] = st
	}
	return st
}

// appSource returns the token source of a GitHub App: it signs a JWT with the
// private key, finds the installation of the repository and requests an
// installation token limited to that repository.
func (e *Executor) appSource(s *domain.GitOpsPRExecutorSpec, u *url.URL) tokenSource {
	st := e.appState(s.EffectiveAPIURL() + "|" + s.AppID + "|" + s.Repository)
	return func(ctx context.Context) (string, error) {
		st.mu.Lock()
		defer st.mu.Unlock()
		now := e.now()
		if st.token != "" && now.Add(renewBefore).Before(st.expires) {
			return st.token, nil
		}
		keyPEM, err := e.access.Secret("", s.PrivateKeyFile, "gitops-pr app key", u)
		if err != nil {
			return "", err
		}
		key, err := parseRSAKey(keyPEM)
		if err != nil {
			return "", errors.New("gitops-pr app key: the private key file is not a valid RSA key in PEM format")
		}
		jwt, err := signJWT(key, s.AppID, now)
		if err != nil {
			return "", errors.New("gitops-pr app key: signing failed")
		}
		api := newGitHubAPI(e.client, s.EffectiveAPIURL(), e.timeout, func(context.Context) (string, error) { return jwt, nil }, e.now)
		gh := newGitHub(api, s.Repository, e.log)
		var inst struct {
			ID int64 `json:"id"`
		}
		if _, err := api.do(ctx, call{method: "GET", path: gh.p("/installation"), out: &inst, ok: []int{200}}); err != nil {
			return "", err
		}
		var tok struct {
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		in := map[string]any{
			"repositories": []string{gh.repo},
			"permissions":  map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"},
		}
		if _, err := api.do(ctx, call{method: "POST", path: "/app/installations/" + strconv.FormatInt(inst.ID, 10) + "/access_tokens", in: in, out: &tok, ok: []int{201}}); err != nil {
			return "", err
		}
		if tok.Token == "" {
			return "", errors.New("gitops-pr app: the installation token answer is empty")
		}
		st.token, st.expires = tok.Token, tok.ExpiresAt
		return st.token, nil
	}
}

func parseRSAKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// signJWT returns a JWT (RS256) for the App: iat a minute in the past to
// allow clock drift, valid for 9 minutes.
func signJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return strings.Join([]string{signing, enc.EncodeToString(sig)}, "."), nil
}
