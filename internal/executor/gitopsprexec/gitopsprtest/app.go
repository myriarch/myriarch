// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprtest

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// EnableApp makes the double serve the GitHub App login: it checks the RS256
// signature and the claims of the App JWT with pub and issues installation
// tokens that are valid for ttl (counted by now). The API calls then accept
// the issued tokens.
func (g *GitHub) EnableApp(pub *rsa.PublicKey, appID string, now func() time.Time, ttl time.Duration) {
	g.SetAppHandler(func(w http.ResponseWriter, r *http.Request, allow func(string)) bool {
		p := r.URL.Path
		isInstall := r.Method == "GET" && strings.HasSuffix(p, "/installation")
		isToken := r.Method == "POST" && strings.HasPrefix(p, "/app/installations/") && strings.HasSuffix(p, "/access_tokens")
		if !isInstall && !isToken {
			return false
		}
		if !verifyJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), pub, appID, now()) {
			g.fail(w, r, http.StatusUnauthorized, "bad app jwt")
			return true
		}
		if isInstall {
			writeJSON(w, 200, map[string]int{"id": 99})
			return true
		}
		var in struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		decode(r, &in)
		if len(in.Repositories) != 1 || in.Permissions["contents"] != "write" || in.Permissions["pull_requests"] != "write" || in.Permissions["metadata"] != "read" {
			g.fail(w, r, http.StatusUnprocessableEntity, "unexpected scope")
			return true
		}
		g.appTokens++
		tok := fmt.Sprintf("ghs_install_token_%d", g.appTokens)
		allow(tok)
		writeJSON(w, 201, map[string]string{"token": tok, "expires_at": now().Add(ttl).UTC().Format(time.RFC3339)})
		return true
	})
}

// AppTokenRequests counts the installation tokens issued so far.
func (g *GitHub) AppTokenRequests() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.appTokens
}

func verifyJWT(token string, pub *rsa.PublicKey, appID string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
		Iat int64  `json:"iat"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return false
	}
	return c.Iss == appID && c.Exp > now.Unix() && c.Iat <= now.Unix() && c.Exp-c.Iat <= 600
}
