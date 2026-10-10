// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package clientassert

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePair(t *testing.T, certFile, keyFile string, pkcs8 bool) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "myriarchd"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	keyBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if pkcs8 {
		b, _ := x509.MarshalPKCS8PrivateKey(key)
		keyBlock = &pem.Block{Type: "PRIVATE KEY", Bytes: b}
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	return key, cert
}

func decode(t *testing.T, seg string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestAssertion(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_, cert := writePair(t, certFile, keyFile, false)
	s, err := New(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	jwt, err := s.Assertion("client-1", "https://login.microsoftonline.com/t/oauth2/v2.0/token", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %s", jwt)
	}
	var hdr map[string]string
	var claims map[string]any
	decode(t, parts[0], &hdr)
	decode(t, parts[1], &claims)

	s1, s256 := sha1.Sum(cert.Raw), sha256.Sum256(cert.Raw)
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" || hdr["x5t"] != base64.RawURLEncoding.EncodeToString(s1[:]) || hdr["x5t#S256"] != base64.RawURLEncoding.EncodeToString(s256[:]) {
		t.Errorf("header: %v", hdr)
	}
	if claims["aud"] != "https://login.microsoftonline.com/t/oauth2/v2.0/token" || claims["iss"] != "client-1" || claims["sub"] != "client-1" {
		t.Errorf("claims: %v", claims)
	}
	if int64(claims["iat"].(float64)) != now.Unix() || int64(claims["nbf"].(float64)) != now.Unix() || int64(claims["exp"].(float64)) != now.Add(5*time.Minute).Unix() {
		t.Errorf("times: %v", claims)
	}
	if jti, _ := claims["jti"].(string); len(jti) < 20 {
		t.Errorf("jti: %v", claims["jti"])
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature: %v", err)
	}
	// every assertion is unique
	jwt2, _ := s.Assertion("client-1", "aud", now)
	var c2 map[string]any
	decode(t, strings.Split(jwt2, ".")[1], &c2)
	if c2["jti"] == claims["jti"] {
		t.Error("jti must be random")
	}

	if th, err := s.Thumbprint(); err != nil || th != fmt.Sprintf("%X", s1) {
		t.Errorf("thumbprint %q %v", th, err)
	}
	if na, err := s.NotAfter(); err != nil || !na.Equal(cert.NotAfter) {
		t.Errorf("not after %v %v", na, err)
	}
	if _, err := s.Assertion("", "aud", now); err == nil {
		t.Error("a client id is required")
	}
}

func TestRotationKeepsWorkingWithBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_, oldCert := writePair(t, certFile, keyFile, true)
	s, err := New(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	thumb := func() string {
		jwt, err := s.Assertion("c", "a", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var h map[string]string
		decode(t, strings.Split(jwt, ".")[0], &h)
		return h["x5t#S256"]
	}
	first := thumb()
	// a half written rotation (new certificate, key not yet replaced) keeps the old material
	_, newCert := writePair(t, filepath.Join(dir, "new.pem"), filepath.Join(dir, "newkey.pem"), true)
	_ = os.Rename(filepath.Join(dir, "new.pem"), certFile)
	if got := thumb(); got != first {
		t.Fatal("a mismatching pair must not replace the working material")
	}
	// the matching key arrives: the new certificate is used
	_ = os.Rename(filepath.Join(dir, "newkey.pem"), keyFile)
	s256 := sha256.Sum256(newCert.Raw)
	if got := thumb(); got != base64.RawURLEncoding.EncodeToString(s256[:]) || got == first {
		t.Fatal("the new material must be used")
	}
	_ = oldCert
}

func TestNewErrors(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writePair(t, certFile, keyFile, false)
	for name, tc := range map[string]struct{ cert, key, want string }{
		"no files":    {"", "", "must be set together"},
		"no key":      {certFile, "", "must be set together"},
		"no cert":     {"", keyFile, "must be set together"},
		"missing":     {filepath.Join(dir, "x"), keyFile, "client certificate"},
		"cert as key": {certFile, certFile, "contains no PEM private key"},
		"key as cert": {keyFile, keyFile, "contains no PEM certificate"},
	} {
		if _, err := New(tc.cert, tc.key); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the cert file may hold a chain, in any order: the one that matches the key is used
	_, other := writePair(t, filepath.Join(dir, "o.pem"), filepath.Join(dir, "ok.pem"), false)
	chain, _ := os.ReadFile(certFile)
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Raw})
	_ = os.WriteFile(filepath.Join(dir, "chain.pem"), append(otherPEM, chain...), 0o600)
	if err := Check(filepath.Join(dir, "chain.pem"), keyFile); err != nil {
		t.Errorf("chain: %v", err)
	}
}
