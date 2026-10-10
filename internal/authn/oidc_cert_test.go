// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// FX-8: Entra ID with app roles in the "roles" claim and client authentication
// with a certificate (private_key_jwt).

// entraAuth configures OIDC like the Entra ID setup of the test environment:
// app roles (claim "roles"), no group mapping, no default role.
func entraAuth(idp *fakeIdP, mut func(o *config.OIDCConfig)) func(c *config.AuthConfig) {
	return func(c *config.AuthConfig) {
		oidcAuth(idp)(c)
		c.OIDC.GroupsClaim = "roles"
		c.OIDC.RoleMapping = config.RoleMapping{
			Admin: []string{"myriarch.admin"}, Operator: []string{"myriarch.operator"}, Viewer: []string{"myriarch.viewer"},
		}
		if mut != nil {
			mut(&c.OIDC)
		}
	}
}

func TestOIDCAppRolesInTheRolesClaim(t *testing.T) {
	idp := newIdP(t)
	roles := []string{}
	sub := "u1"
	idp.claims = func(string) map[string]any { return map[string]any{"sub": sub, "roles": roles} }
	e := newEnv(t, envOpts{auth: entraAuth(idp, nil), httpCli: idp.srv.Client()})

	login := func() (result, *client) {
		c := e.newClient()
		cb, _ := startLogin(t, c, idp, "")
		return c.req("GET", callbackPath(t, cb), nil, false), c
	}
	role := func(c *client) domain.Role {
		var me dto.Me
		c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
		return me.Role
	}

	// a user without an app role (Entra: "assignment required" off, or a guest): denied
	r, c := login()
	if r.Header.Get("Location") != "/login?error=access_denied" || c.cookie(authn.SessionCookie) != "" {
		t.Fatalf("no role: %d %s", r.Code, r.Header.Get("Location"))
	}
	// roles of other applications do not count
	roles = []string{"Some.Other.Role"}
	if r, _ = login(); r.Header.Get("Location") != "/login?error=access_denied" {
		t.Fatalf("unrelated role: %s", r.Header.Get("Location"))
	}
	// several app roles: the highest wins
	roles = []string{"myriarch.viewer", "myriarch.operator"}
	sub = "u2"
	if _, c = login(); role(c) != domain.RoleOperator {
		t.Fatal("viewer + operator must give operator")
	}
	roles = []string{"myriarch.viewer", "myriarch.admin", "myriarch.operator"}
	sub = "u3"
	if _, c = login(); role(c) != domain.RoleAdmin {
		t.Fatal("the highest app role must win")
	}
	roles = []string{"myriarch.viewer"}
	sub = "u4"
	if _, c = login(); role(c) != domain.RoleViewer {
		t.Fatal("viewer")
	}
	// the claim is a string (a single role): the same
	idp.claims = func(string) map[string]any { return map[string]any{"sub": "u5", "roles": "myriarch.operator"} }
	if _, c = login(); role(c) != domain.RoleOperator {
		t.Fatal("single string role")
	}
	// group overage (more than 200 groups: Entra leaves the groups out) does not matter with app roles
	idp.claims = func(string) map[string]any {
		// what Entra sends for a user in more than 200 groups: no groups, a pointer to the Graph API instead
		return map[string]any{"sub": "u6", "roles": []string{"myriarch.viewer"},
			"_claim_names":   map[string]any{"groups": "src1"},
			"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.windows.net/tenant/users/u6/getMemberObjects"}}}
	}
	if _, c = login(); role(c) != domain.RoleViewer {
		t.Fatal("the user with group overage must get the role from the roles claim")
	}
}

// newKeyPair writes a certificate (valid for a day) with an RSA key, in the
// given PEM flavours, and returns the certificate.
func newKeyPair(t *testing.T, certFile, keyFile string, pkcs8 bool) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "myriarchd"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if pkcs8 {
		b, _ := x509.MarshalPKCS8PrivateKey(key)
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestOIDCClientCertificateLogin(t *testing.T) {
	for name, pkcs8 := range map[string]bool{"PKCS#1 key": false, "PKCS#8 key": true} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
			cert := newKeyPair(t, certFile, keyFile, pkcs8)
			idp := newIdP(t)
			idp.assertCerts = []*x509.Certificate{cert}
			idp.claims = func(string) map[string]any { return map[string]any{"roles": []string{"myriarch.admin"}} }
			e := newEnv(t, envOpts{httpCli: idp.srv.Client(), auth: entraAuth(idp, func(o *config.OIDCConfig) {
				o.ClientSecret = ""
				o.ClientCertificateFile, o.ClientKeyFile = certFile, keyFile
			})})

			login := func() (result, *client) {
				c := e.newClient()
				cb, _ := startLogin(t, c, idp, "")
				return c.req("GET", callbackPath(t, cb), nil, false), c
			}
			r, c := login()
			if r.Code != 302 || r.Header.Get("Location") != "/" {
				t.Fatalf("login with a certificate: %d %s %s", r.Code, r.Header.Get("Location"), r.Body)
			}
			var me dto.Me
			c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
			if me.Role != domain.RoleAdmin || me.AuthMethod != "oidc" {
				t.Fatalf("%+v", me)
			}
			// the IdP checked signature, thumbprints, audience, lifetime and the form fields
			if len(idp.assertions) != 1 {
				t.Fatalf("assertions seen: %d", len(idp.assertions))
			}
			seen := idp.assertions[0]
			if seen.form.Get("client_secret") != "" || seen.form.Get("client_id") != idp.clientID {
				t.Errorf("form: %v", seen.form)
			}
			// every login uses a fresh assertion (jti) - the IdP refuses a reused one
			if _, c2 := login(); role(t, c2) != domain.RoleAdmin {
				t.Error("second login")
			}
			if len(idp.assertions) != 2 {
				t.Errorf("assertions seen: %d", len(idp.assertions))
			}
		})
	}
}

func role(t *testing.T, c *client) domain.Role {
	t.Helper()
	var me dto.Me
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	return me.Role
}

// The key is read again when its files change (rotation without a restart); an
// assertion signed by a key the IdP does not know fails the login.
func TestOIDCClientCertificateRotationAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	oldCert := newKeyPair(t, certFile, keyFile, true)
	idp := newIdP(t)
	idp.assertCerts = []*x509.Certificate{oldCert}
	idp.claims = func(string) map[string]any { return map[string]any{"roles": []string{"myriarch.viewer"}} }
	e := newEnv(t, envOpts{httpCli: idp.srv.Client(), auth: entraAuth(idp, func(o *config.OIDCConfig) {
		o.ClientSecret = ""
		o.ClientCertificateFile, o.ClientKeyFile = certFile, keyFile
	})})
	login := func() result {
		c := e.newClient()
		cb, _ := startLogin(t, c, idp, "")
		return c.req("GET", callbackPath(t, cb), nil, false)
	}
	if r := login(); r.Header.Get("Location") != "/" {
		t.Fatalf("before the rotation: %s", r.Header.Get("Location"))
	}

	// new key and certificate in the files, the IdP knows only the old one: the login fails
	newCert := newKeyPair(t, certFile, keyFile, true)
	if r := login(); r.Header.Get("Location") != "/login?error=oidc_error" {
		t.Fatalf("an unregistered certificate must fail: %s", r.Header.Get("Location"))
	}
	found := false
	for _, ev := range e.findAudit("auth.login") {
		found = found || (ev.Result == "failure" && ev.Details["reason"] == "token_exchange")
	}
	if !found {
		t.Fatalf("audit: %+v", e.findAudit("auth.login"))
	}
	// the new certificate is registered at the IdP (both are valid during a rotation)
	idp.mu.Lock()
	idp.assertCerts = append(idp.assertCerts, newCert)
	idp.mu.Unlock()
	if r := login(); r.Header.Get("Location") != "/" {
		t.Fatalf("after the rotation: %s", r.Header.Get("Location"))
	}
}

func TestOIDCClientAuthenticationConfigurationErrors(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	newKeyPair(t, certFile, keyFile, true)
	otherCert, otherKey := filepath.Join(dir, "other.crt"), filepath.Join(dir, "other.key")
	newKeyPair(t, otherCert, otherKey, true)
	junk := filepath.Join(dir, "junk")
	_ = os.WriteFile(junk, []byte("junk"), 0o600)
	ecKey := filepath.Join(dir, "ec.key")
	_ = os.WriteFile(ecKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1}}), 0o600)

	for name, tc := range map[string]struct {
		mut  func(o *config.OIDCConfig)
		want string
	}{
		"secret and certificate": {func(o *config.OIDCConfig) { o.ClientCertificateFile, o.ClientKeyFile = certFile, keyFile }, "either a client secret or a client certificate"},
		"certificate without key": {func(o *config.OIDCConfig) {
			o.ClientSecret = ""
			o.ClientCertificateFile = certFile
		}, "set together"},
		"key does not match": {func(o *config.OIDCConfig) {
			o.ClientSecret = ""
			o.ClientCertificateFile, o.ClientKeyFile = certFile, otherKey
		}, "no certificate matches the private key"},
		"no certificate in the file": {func(o *config.OIDCConfig) {
			o.ClientSecret = ""
			o.ClientCertificateFile, o.ClientKeyFile = junk, keyFile
		}, "contains no PEM certificate"},
		"no key in the file": {func(o *config.OIDCConfig) {
			o.ClientSecret = ""
			o.ClientCertificateFile, o.ClientKeyFile = certFile, junk
		}, "contains no PEM private key"},
		"EC key": {func(o *config.OIDCConfig) {
			o.ClientSecret = ""
			o.ClientCertificateFile, o.ClientKeyFile = certFile, ecKey
		}, "not an RSA key"},
		"missing file": {func(o *config.OIDCConfig) {
			o.ClientSecret = ""
			o.ClientCertificateFile, o.ClientKeyFile = filepath.Join(dir, "missing.crt"), keyFile
		}, "client certificate"},
	} {
		t.Run(name, func(t *testing.T) {
			idp := newIdP(t)
			_, err := tryNew(t, envOpts{httpCli: idp.srv.Client(), auth: entraAuth(idp, tc.mut)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("the error must not contain file paths: %v", err)
			}
		})
	}
}

// tryNew creates just the service (no HTTP server) and returns its error.
func tryNew(t *testing.T, o envOpts) (*authn.Service, error) {
	t.Helper()
	db := postgres.New(pgtest.NewPool(t), nil)
	authCfg := config.AuthConfig{SessionTTL: time.Hour, DevLogin: config.DevLoginConfig{Enabled: true, Users: devUsers()}}
	o.auth(&authCfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return authn.New(ctx, authCfg, config.ServerConfig{PublicURL: "https://fleet.example.com"}, config.Default().OpAMP, authn.Deps{
		Store: db, Audit: audit.NewRecorder(db.Audit(), quiet), Registerer: prometheus.NewRegistry(), Logger: quiet, HTTPClient: o.httpCli,
	})
}
