// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package netguard

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"go/parser"
	"go/token"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	// The same import as in cmd/myriarchd, cmd/myriarch-sim and cmd/myrctl: the test
	// binary then carries the embedded Mozilla roots exactly like the binaries
	// of the container image (which has no CA bundle at all).
	_ "golang.org/x/crypto/x509roots/fallback"
)

// The container image is built FROM scratch: no /etc/ssl/certs, no OS packages.
// The binaries carry the Mozilla root set instead (x509.SetFallbackRoots via
// golang.org/x/crypto/x509roots/fallback). Go uses it only when the system has
// no roots of its own; x509.SystemCertPool then returns (a copy of) it, and
// every pool built here (AppendRoots, RootPool: outbound.caFile, gitops CA,
// AWX CA, ...) starts from SystemCertPool. These tests pin that contract.

const fallbackHelperEnv = "NETGUARD_FALLBACK_HELPER"

// TestFallbackRootsPlusConfiguredCA runs the checks below in a child process
// whose system trust store is empty: the fallback can only be installed once
// per process and the system pool is fixed at first use, so this cannot be
// emulated in the (shared) test process itself. SSL_CERT_FILE/SSL_CERT_DIR point
// to a missing file and an empty directory, which is exactly what a scratch
// image looks like to Go on Linux. macOS and Windows have no on-disk system
// store (platform verifier; the variables only count with Go 1.27 and a
// go.mod of 1.27 or newer), there the fallback is forced with the documented
// GODEBUG=x509usefallbackroots=1; the pool semantics checked afterwards are the same.
func TestFallbackRootsPlusConfiguredCA(t *testing.T) {
	if os.Getenv(fallbackHelperEnv) == "1" {
		t.Skip("running as the helper process")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFallbackRootsHelper$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(),
		fallbackHelperEnv+"=1",
		"SSL_CERT_FILE="+filepath.Join(t.TempDir(), "no-such-ca-bundle.pem"),
		"SSL_CERT_DIR="+t.TempDir(),
	)
	switch runtime.GOOS {
	case "windows", "darwin", "ios":
		cmd.Env = append(cmd.Env, "GODEBUG=x509usefallbackroots=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("the system trust store cannot be emptied on this platform/Go version:\n%s", out)
	}
	if !strings.Contains(string(out), "--- PASS: TestFallbackRootsHelper") {
		t.Fatalf("helper process did not run the test:\n%s", out)
	}
}

// TestFallbackRootsHelper is the body of TestFallbackRootsPlusConfiguredCA; it
// only runs in the child process with the emptied system trust store.
func TestFallbackRootsHelper(t *testing.T) {
	if os.Getenv(fallbackHelperEnv) != "1" {
		t.Skip("helper process of TestFallbackRootsPlusConfiguredCA")
	}

	// Precondition: with no system roots, SystemCertPool is the embedded Mozilla set.
	sys, err := x509.SystemCertPool()
	if err != nil || sys == nil {
		t.Fatalf("SystemCertPool: %v %v", sys, err)
	}
	fallbackCount := len(sys.Subjects()) //nolint:staticcheck // exact for pools that are not the platform pool
	if fallbackCount < 100 {
		t.Skipf("SystemCertPool has %d certificates, the Mozilla fallback set (about 140) is not active", fallbackCount)
	}

	caPEM, caCert, caKey := newTestCA(t)
	caFile := filepath.Join(t.TempDir(), "internal-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	want := sys.Clone()
	if !want.AppendCertsFromPEM(caPEM) {
		t.Fatal("test CA not appended")
	}

	// outbound.caFile (RootPool) and every other CA file (AppendRoots with a nil base):
	// exactly the fallback roots plus the configured CA, nothing replaced, nothing lost.
	fromFile, err := RootPool(caFile)
	if err != nil {
		t.Fatal(err)
	}
	fromPEM, err := AppendRoots(nil, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*x509.CertPool{"RootPool": fromFile, "AppendRoots(nil)": fromPEM} {
		if n := len(got.Subjects()); n != fallbackCount+1 { //nolint:staticcheck // see above
			t.Errorf("%s: %d certificates, want %d fallback roots + 1 configured CA", name, n, fallbackCount)
		}
		if !got.Equal(want) {
			t.Errorf("%s: pool differs from fallback roots + configured CA", name)
		}
	}

	// A second CA file (gitops.auth.caFile, AWX caFile) is added on top of the pool of
	// outbound.caFile, again without dropping anything.
	secondPEM, _, _ := newTestCA(t)
	both, err := AppendRoots(fromFile, secondPEM)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(both.Subjects()); n != fallbackCount+2 { //nolint:staticcheck // see above
		t.Errorf("two CA files: %d certificates, want %d", n, fallbackCount+2)
	}
	if len(fromFile.Subjects()) != fallbackCount+1 { //nolint:staticcheck // see above
		t.Error("AppendRoots must not modify the base pool")
	}

	// The configured CA really verifies certificates it signed ...
	leaf := newTestLeaf(t, caCert, caKey, "service.internal.example")
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: fromFile, DNSName: "service.internal.example"}); err != nil {
		t.Errorf("a certificate of the configured CA must verify: %v", err)
	}
	// ... and the embedded roots do not trust it by themselves.
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: sys, DNSName: "service.internal.example"}); err == nil {
		t.Error("the fallback roots alone must not trust the internal CA")
	}

	// End to end through the HTTP client of the policy (outbound.caFile).
	srv, serverCA := tlsServer(t)
	p, err := New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := get(p.Client(5*time.Second), srv.URL); err == nil {
		t.Error("an unknown server CA must be refused with only the fallback roots")
	}
	pool, err := RootPool(serverCA)
	if err != nil {
		t.Fatal(err)
	}
	p.SetRootCAs(pool)
	if err := get(p.Client(5*time.Second), srv.URL); err != nil {
		t.Errorf("the server CA of outbound.caFile must be trusted on top of the fallback roots: %v", err)
	}
}

// TestBinariesEmbedFallbackRoots keeps the import in the main packages of the
// three binaries that are in the image. Without it the image (no CA bundle)
// could not verify any public TLS certificate.
func TestBinariesEmbedFallbackRoots(t *testing.T) {
	const pkg = "golang.org/x/crypto/x509roots/fallback"
	for _, bin := range []string{"myriarchd", "myriarch-sim", "myrctl"} {
		file := filepath.Join("..", "..", "cmd", bin, "main.go")
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", bin, err)
		}
		found := false
		for _, imp := range f.Imports {
			if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == pkg {
				found = imp.Name != nil && imp.Name.Name == "_"
			}
		}
		if !found {
			t.Errorf("cmd/%s/main.go must import _ %q (container image without CA bundle)", bin, pkg)
		}
	}
}

func newTestCA(t *testing.T) (pemBytes []byte, cert *x509.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "myriarch test internal CA " + serial.String()},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key
}

func newTestLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, dnsName string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}
