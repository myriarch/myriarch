// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/authn/clientassert"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// ---------------------------------------------------------------- files ---

// fileContent reads a file that the configuration refers to. The error does
// not name the path (the caller names the setting) and never the content.
func fileContent(file string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, fmt.Errorf("cannot read %s: %w", file, err)
	}
	return b, nil
}

// fileNotEmpty checks that the file at path exists and has content (a secret
// file). With skipFiles nothing is read.
func (v *validator) fileNotEmpty(setting, file string) {
	if v.skipFiles || file == "" {
		return
	}
	b, err := fileContent(file)
	if err != nil {
		v.add(setting, err.Error())
		return
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		v.add(setting, fmt.Sprintf("file %s is empty", file))
	}
}

// secretMinBytes is the size authn requires of the download token secret.
const secretMinBytes = 32

// downloadSecretFile checks auth.downloadTokenSecretFile like authn reads it: at
// least 32 bytes without trailing line breaks.
func (v *validator) downloadSecretFile(file string) {
	if v.skipFiles || file == "" {
		return
	}
	b, err := fileContent(file)
	if err != nil {
		v.add("auth.downloadTokenSecretFile", err.Error())
		return
	}
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	if len(b) < secretMinBytes {
		v.add("auth.downloadTokenSecretFile", fmt.Sprintf("file %s must contain at least %d bytes (random key of the HMAC)", file, secretMinBytes))
	}
}

// pemCertificates checks that the file holds at least one PEM certificate.
func (v *validator) pemCertificates(setting, file string) {
	if v.skipFiles || file == "" {
		return
	}
	b, err := fileContent(file)
	if err != nil {
		v.add(setting, err.Error())
		return
	}
	if !x509.NewCertPool().AppendCertsFromPEM(b) {
		v.add(setting, fmt.Sprintf("file %s contains no PEM certificate", file))
	}
}

// checkServerFiles checks the TLS files of the listener.
func (v *validator) checkServerFiles(c *config.Config) {
	t := c.Server.TLS
	if !v.skipFiles && t.CertFile != "" && t.KeyFile != "" {
		if _, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile); err != nil {
			v.add("server.tls", "certFile and keyFile cannot be loaded as a key pair: "+pathless(err))
		}
	}
	v.pemCertificates("server.tls.clientCaFile", t.ClientCAFile)
}

// pathless returns the message of err without file paths of an *os.PathError.
func pathless(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// ------------------------------------------------------------- database ---

// checkDatabase validates the database settings: the part of the validation that "myriarchd migrate" shares with the
// server. A DSN (or a DSN file) is required outside dev mode.
func (v *validator) checkDatabase(d config.DatabaseConfig, dev bool) {
	if !dev && strings.TrimSpace(d.DSN) == "" && d.DSNFile == "" {
		v.add("database.dsn", "is required (or database.dsnFile)")
	}
	v.checkDSN(d.DSN)
	if d.StatementTimeout < 0 {
		v.add("database.statementTimeout", "must not be negative")
	}
	if d.MaxConns < 5 {
		v.add("database.maxConns", "must be at least 5 (event bus and leader election hold dedicated connections)")
	}
}

// checkDSN parses the connection string (no connection is made). The message
// does not repeat it: it holds the password.
func (v *validator) checkDSN(dsn string) {
	if strings.TrimSpace(dsn) == "" {
		return
	}
	if _, err := pgxpool.ParseConfig(dsn); err != nil {
		v.add("database.dsn", "is not a valid PostgreSQL connection string (postgres://user:password@host:5432/database?sslmode=verify-full)")
	}
}

// ----------------------------------------------------------------- OIDC ---

// checkOIDCClientAuth validates how myriarchd authenticates at the token
// endpoint: exactly one of a secret and a certificate.
func (v *validator) checkOIDCClientAuth(o config.OIDCConfig) {
	secret := o.ClientSecret != "" || o.ClientSecretFile != ""
	cert := o.ClientCertificateFile != "" || o.ClientKeyFile != ""
	switch {
	case secret && cert:
		v.add("auth.oidc.clientCertificateFile", "use either a client secret (clientSecret/clientSecretFile) or a client certificate (clientCertificateFile + clientKeyFile), not both")
		return
	case !secret && !cert:
		v.add("auth.oidc.clientSecret", "is required when OIDC is enabled (or clientSecretFile, or clientCertificateFile + clientKeyFile)")
		return
	case cert && (o.ClientCertificateFile == "" || o.ClientKeyFile == ""):
		v.add("auth.oidc.clientCertificateFile", "clientCertificateFile and clientKeyFile must be set together")
		return
	}
	if cert && !v.skipFiles {
		if err := clientassert.Check(o.ClientCertificateFile, o.ClientKeyFile); err != nil {
			v.add("auth.oidc.clientCertificateFile", err.Error())
		}
	}
}

// ------------------------------------------------------ bootstrap admin ---

// checkBootstrapAdmin validates auth.bootstrapAdmin. The files are read only
// while the bootstrap admin is active (it is enabled and not replaced by
// single sign-on) and only when the validation reads files.
func (v *validator) checkBootstrapAdmin(a config.AuthConfig) {
	b := a.BootstrapAdmin
	if !b.Enabled {
		return
	}
	if b.PasswordHashFile == "" && b.TokenFile == "" {
		v.add("auth.bootstrapAdmin", "needs passwordHashFile or tokenFile when enabled")
	}
	if !domain.ValidBootstrapUsername(b.Username) {
		v.add("auth.bootstrapAdmin.username", `must be 1 to 64 characters: letters, digits, ".", "_", "@" or "-"`)
	}
	if b.PasswordHashFile != "" && !filepath.IsAbs(b.PasswordHashFile) {
		v.add("auth.bootstrapAdmin.passwordHashFile", "must be an absolute path")
	}
	if b.TokenFile != "" && !filepath.IsAbs(b.TokenFile) {
		v.add("auth.bootstrapAdmin.tokenFile", "must be an absolute path")
	}
	if v.skipFiles || !a.BootstrapActive() {
		return
	}
	if b.PasswordHashFile != "" && filepath.IsAbs(b.PasswordHashFile) {
		v.credentialFile("auth.bootstrapAdmin.passwordHashFile", b.PasswordHashFile, domain.BootstrapHashProblem)
	}
	if b.TokenFile != "" && filepath.IsAbs(b.TokenFile) {
		v.credentialFile("auth.bootstrapAdmin.tokenFile", b.TokenFile, domain.BootstrapTokenProblem)
	}
}

// credentialFile reads a credential file, trims it and applies the format
// check. The messages name the file and never its content.
func (v *validator) credentialFile(setting, file string, problem func(string) string) (content string, ok bool) {
	b, err := fileContent(file)
	if err != nil {
		v.add(setting, err.Error())
		return "", false
	}
	content = strings.TrimSpace(string(b))
	if msg := problem(content); msg != "" {
		v.add(setting, fmt.Sprintf("file %s %s", file, msg))
		return "", false
	}
	return content, true
}

// userMappingEmail is the form of an "email:" entry of auth.oidc.roleMapping.users.
var userMappingEmail = regexp.MustCompile(`^email:[^@\s]+@[^@\s]+$`)

// validUserMapping reports whether e is "sub:<subject>" or "email:<address>".
func validUserMapping(e string) bool {
	if rest, ok := strings.CutPrefix(e, "sub:"); ok {
		return strings.TrimSpace(rest) != ""
	}
	return userMappingEmail.MatchString(e)
}

// ------------------------------------------------- enrollment tokens ---

// rfc3339Example is the hint of a malformed expiresAt.
const rfc3339Example = "2027-01-01T00:00:00Z"

// checkEnrollmentTokens validates opamp.enrollmentTokens.
func (v *validator) checkEnrollmentTokens(c *config.Config) {
	names := map[string]bool{}
	values := map[string]int{} // token value -> first entry index
	devTokens := map[string]bool{config.Default().Dev.SeedEnrollmentToken: true}
	if c.Dev.SeedEnrollmentToken != "" {
		devTokens[c.Dev.SeedEnrollmentToken] = true
	}
	for i, t := range c.OpAMP.EnrollmentTokens {
		p := fmt.Sprintf("opamp.enrollmentTokens[%d]", i)
		if !domain.ValidDNSLabel(t.Name) {
			v.add(p+".name", `must be a DNS label: lower case letters, digits and "-", 1 to 63 characters`)
		} else if names[t.Name] {
			v.add(p+".name", fmt.Sprintf("duplicate name %q", t.Name))
		}
		names[t.Name] = true
		pathOK := false
		switch {
		case t.TokenFile == "":
			v.add(p+".tokenFile", "is required")
		case !filepath.IsAbs(t.TokenFile):
			v.add(p+".tokenFile", "must be an absolute path")
		default:
			pathOK = true
		}
		if domain.CheckLabels(t.Labels) != nil {
			v.add(p+".labels", domain.LabelsRule)
		}
		if t.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339, t.ExpiresAt); err != nil {
				v.add(p+".expiresAt", "must be an RFC 3339 timestamp such as "+rfc3339Example)
			}
		}
		if !pathOK || v.skipFiles {
			continue
		}
		tok, ok := v.credentialFile(p+".tokenFile", t.TokenFile, domain.EnrollmentTokenProblem)
		if !ok {
			continue
		}
		if devTokens[tok] {
			v.add(p+".tokenFile", fmt.Sprintf("file %s contains the well-known development enrollment token", t.TokenFile))
			continue
		}
		if j, dup := values[tok]; dup {
			v.add(p+".tokenFile", fmt.Sprintf("file %s contains the same token as opamp.enrollmentTokens[%d]", t.TokenFile, j))
			continue
		}
		values[tok] = i
	}
}

// ------------------------------------------------------------ artifacts ---

// checkSourceAuth validates the mirror credentials.
func (v *validator) checkSourceAuth(a config.ArtifactsConfig) {
	s := a.SourceAuth
	if !s.Configured() {
		return
	}
	switch {
	case s.TokenFile != "" && (s.Username != "" || s.PasswordFile != ""):
		v.add("artifacts.sourceAuth", "use either tokenFile (bearer token) or username + passwordFile (basic), not both")
		return
	case s.TokenFile == "" && (s.Username == "" || s.PasswordFile == ""):
		v.add("artifacts.sourceAuth", "needs tokenFile (bearer token) or username together with passwordFile (basic)")
		return
	}
	v.fileNotEmpty("artifacts.sourceAuth.tokenFile", s.TokenFile)
	v.fileNotEmpty("artifacts.sourceAuth.passwordFile", s.PasswordFile)
}

// checkVerify validates the verification settings that the artifact cache
// would refuse at startup.
func (v *validator) checkVerify(c config.ArtifactVerifyConfig, dev bool) {
	if c.CertificateIdentityRegexp != "" {
		if _, err := regexp.Compile(c.CertificateIdentityRegexp); err != nil {
			v.add("artifacts.verify.certificateIdentityRegexp", err.Error())
		} else if !AnchoredRegexp(c.CertificateIdentityRegexp) {
			v.add("artifacts.verify.certificateIdentityRegexp", "must be anchored: start with ^ and end with $ (an unanchored pattern also accepts identities that merely contain the expected text)")
		}
	}
	if c.TUFRepositoryURL != "" {
		u, err := url.Parse(c.TUFRepositoryURL)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			v.add("artifacts.verify.tufRepositoryUrl", "must be an absolute http(s) URL")
		case !dev && u.Scheme != "https":
			v.add("artifacts.verify.tufRepositoryUrl", "must be an https URL in production")
		}
	}
	if c.TrustedRootFile != "" && !v.skipFiles {
		if _, err := artifact.LoadTrustedRootFile(c.TrustedRootFile); err != nil {
			v.add("artifacts.verify.trustedRootFile", pathless(err))
		}
	}
}

// AnchoredRegexp reports whether the pattern matches only whole strings: its top level is a
// concatenation that begins with ^ and ends with $ (so "^a|b$" and an escaped final "\$" do not count).
func AnchoredRegexp(pattern string) bool {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil || re.Op != syntax.OpConcat || len(re.Sub) < 2 {
		return false
	}
	return re.Sub[0].Op == syntax.OpBeginText && re.Sub[len(re.Sub)-1].Op == syntax.OpEndText
}
