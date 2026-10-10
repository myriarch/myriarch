// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
)

// problemMap returns the problems of err by path.
func problemMap(err error) map[string]string {
	out := map[string]string{}
	for _, p := range Problems(err) {
		out[p.Path] = p.Message
	}
	return out
}

// randomHex returns n random bytes as hex.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// bcryptFile writes a bcrypt hash of a random password with the given cost.
func bcryptFile(t *testing.T, cost int) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(randomHex(t, 12)), cost)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "password-hash", string(h)+"\n")
}

// bootstrapConfig is a production configuration whose only login path is the
// bootstrap admin with a one-time token (the file holds a random value).
func bootstrapConfig(t *testing.T) config.Config {
	t.Helper()
	c := prodConfig()
	c.Auth.OIDC = config.Default().Auth.OIDC
	c.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin", TokenFile: writeFile(t, "token", randomHex(t, 24)+"\n")}
	return c
}

func hasWarning(ws []string, part string) bool {
	for _, w := range ws {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

func TestServeRequiresALoginPath(t *testing.T) {
	c := prodConfig()
	c.Auth.OIDC = config.Default().Auth.OIDC
	err := Validate(&c, false)
	if err == nil {
		t.Fatal("serve mode without any login path must be invalid")
	}
	want := `no login path is configured for serve mode: enable auth.oidc (single sign-on) or auth.bootstrapAdmin with passwordHashFile or tokenFile (break-glass administrator); the dev login exists only in "` + brand.ServerBinary + ` dev"`
	if got := problemMap(err)["auth"]; got != want {
		t.Errorf("auth problem:\n got %q\nwant %q", got, want)
	}
	if len(Problems(err)) != 1 {
		t.Errorf("exactly one problem expected: %v", Problems(err))
	}

	// the same check through the loader (what serve and migrate run)
	p := writeFile(t, "c.yaml", prodMinimal[:strings.Index(prodMinimal, "auth:")])
	_, loadErr := load(p, false, env(nil))
	if loadErr == nil || problemMap(loadErr)["auth"] != want {
		t.Errorf("the loader (serve, migrate) must report the same problem: %v", loadErr)
	}

	// dev mode has its own login
	d := config.DevDefaults()
	if err := Validate(&d, true); err != nil {
		t.Errorf("dev mode: %v", err)
	}
	// OIDC alone is a login path and produces no warning about the bootstrap admin
	ok := prodConfig()
	if err := Validate(&ok, false); err != nil {
		t.Fatalf("OIDC only: %v", err)
	}
	ok.Server.TrustedProxies = []string{"10.0.0.0/8"}
	if w := Warnings(&ok, false); len(w) != 0 {
		t.Errorf("OIDC only must be warning free: %v", w)
	}
}

func TestBootstrapAdminAloneIsValidAndWarns(t *testing.T) {
	c := bootstrapConfig(t)
	if err := ValidateOptions(&c, Options{CheckFiles: true}); err != nil {
		t.Fatalf("bootstrap admin with a token file: %v", err)
	}
	ws := Warnings(&c, false)
	if !hasWarning(ws, "auth.bootstrapAdmin is the only login path: configure single sign-on (auth.oidc) and disable the bootstrap admin after the initial setup") {
		t.Errorf("W-BOOTSTRAP-ONLY missing: %v", ws)
	}
	if hasWarning(ws, "keepWithOidc") {
		t.Errorf("the keepWithOidc warning belongs to OIDC setups: %v", ws)
	}
	// dev mode warns about nothing
	if w := Warnings(&c, true); len(w) != 0 {
		t.Errorf("dev: %v", w)
	}
}

func TestBootstrapAdminNextToOIDC(t *testing.T) {
	c := bootstrapConfig(t)
	c.Auth.OIDC = prodConfig().Auth.OIDC
	c.Auth.BootstrapAdmin.KeepWithOIDC = true
	if err := ValidateOptions(&c, Options{CheckFiles: true}); err != nil {
		t.Fatal(err)
	}
	ws := Warnings(&c, false)
	want := "auth.bootstrapAdmin.keepWithOidc keeps the break-glass bootstrap admin active next to OIDC: every sign-in is audited as auth.bootstrap.login; monitor these events and keep the credential in a vault"
	if !hasWarning(ws, want) || hasWarning(ws, "is the only login path") {
		t.Errorf("W-BOOTSTRAP-KEEP expected: %v", ws)
	}
}

// Enabled but replaced by single sign-on: no warning, and its files are not read.
func TestBootstrapAdminInactiveBehindOIDC(t *testing.T) {
	c := prodConfig()
	c.Server.TrustedProxies = []string{"10.0.0.0/8"}
	c.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin",
		PasswordHashFile: "/does/not/exist/hash", TokenFile: "/does/not/exist/token"}
	if c.Auth.BootstrapActive() {
		t.Fatal("OIDC without keepWithOidc replaces the bootstrap admin")
	}
	if err := ValidateOptions(&c, Options{CheckFiles: true}); err != nil {
		t.Errorf("files of an inactive bootstrap admin must not be read: %v", err)
	}
	if w := Warnings(&c, false); len(w) != 0 {
		t.Errorf("an inactive bootstrap admin is no warning: %v", w)
	}
	// with keepWithOidc it is active, and its files are checked
	c.Auth.BootstrapAdmin.KeepWithOIDC = true
	err := ValidateOptions(&c, Options{CheckFiles: true})
	pm := problemMap(err)
	for _, path := range []string{"auth.bootstrapAdmin.passwordHashFile", "auth.bootstrapAdmin.tokenFile"} {
		if !strings.HasPrefix(pm[path], "cannot read /does/not/exist/") {
			t.Errorf("%s: %q", path, pm[path])
		}
	}
	// --skip-files / serve do not read them
	if err := ValidateOptions(&c, Options{}); err != nil {
		t.Errorf("serve does not read the files here: %v", err)
	}
}

func TestBootstrapAdminSettings(t *testing.T) {
	abs := writeFile(t, "x", "x")
	cases := []struct {
		name string
		b    config.BootstrapAdminConfig
		path string
		msg  string
	}{
		{"no files", config.BootstrapAdminConfig{Enabled: true, Username: "admin"}, "auth.bootstrapAdmin", "needs passwordHashFile or tokenFile when enabled"},
		{"empty username", config.BootstrapAdminConfig{Enabled: true, TokenFile: abs}, "auth.bootstrapAdmin.username", `must be 1 to 64 characters: letters, digits, ".", "_", "@" or "-"`},
		{"space in username", config.BootstrapAdminConfig{Enabled: true, Username: "a b", TokenFile: abs}, "auth.bootstrapAdmin.username", ""},
		{"slash in username", config.BootstrapAdminConfig{Enabled: true, Username: "a/b", TokenFile: abs}, "auth.bootstrapAdmin.username", ""},
		{"long username", config.BootstrapAdminConfig{Enabled: true, Username: strings.Repeat("a", 65), TokenFile: abs}, "auth.bootstrapAdmin.username", ""},
		{"relative hash path", config.BootstrapAdminConfig{Enabled: true, Username: "admin", PasswordHashFile: "hash"}, "auth.bootstrapAdmin.passwordHashFile", "must be an absolute path"},
		{"relative token path", config.BootstrapAdminConfig{Enabled: true, Username: "admin", TokenFile: "token"}, "auth.bootstrapAdmin.tokenFile", "must be an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := prodConfig()
			c.Auth.OIDC = config.Default().Auth.OIDC
			c.Auth.BootstrapAdmin = tc.b
			err := Validate(&c, false)
			if err == nil {
				t.Fatal("expected an error")
			}
			got, ok := problemMap(err)[tc.path]
			if !ok || (tc.msg != "" && got != tc.msg) {
				t.Errorf("%s: %q (%v)", tc.path, got, Problems(err))
			}
		})
	}
	// valid user names
	for _, u := range []string{"admin", "a", "Ops.Admin_1", "ops@example.test", "break-glass", strings.Repeat("a", 64)} {
		c := bootstrapConfig(t)
		c.Auth.BootstrapAdmin.Username = u
		if err := Validate(&c, false); err != nil {
			t.Errorf("username %q: %v", u, err)
		}
	}
	// a disabled bootstrap admin is not validated further
	c := prodConfig()
	c.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Username: "not valid", TokenFile: "relative"}
	if err := Validate(&c, false); err != nil {
		t.Errorf("disabled bootstrap admin: %v", err)
	}
}

func TestBootstrapAdminFileChecks(t *testing.T) {
	check := func(t *testing.T, mutate func(b *config.BootstrapAdminConfig)) map[string]string {
		t.Helper()
		c := bootstrapConfig(t)
		mutate(&c.Auth.BootstrapAdmin)
		err := ValidateOptions(&c, Options{CheckFiles: true})
		if err == nil {
			return nil
		}
		return problemMap(err)
	}
	const hashPath, tokenPath = "auth.bootstrapAdmin.passwordHashFile", "auth.bootstrapAdmin.tokenFile"
	hashMsg := func(file string) string {
		return "file " + file + " does not contain a bcrypt hash ($2a$, $2b$ or $2y$) with cost 10 or higher"
	}
	tokenMsg := func(file string) string {
		return "file " + file + " must contain a token of 32 to 512 characters without whitespace"
	}

	t.Run("missing", func(t *testing.T) {
		pm := check(t, func(b *config.BootstrapAdminConfig) {
			b.PasswordHashFile, b.TokenFile = filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "nope")
		})
		for _, p := range []string{hashPath, tokenPath} {
			if !strings.HasPrefix(pm[p], "cannot read ") || !strings.Contains(pm[p], "no such file") {
				t.Errorf("%s: %q", p, pm[p])
			}
		}
	})
	t.Run("empty", func(t *testing.T) {
		empty := writeFile(t, "empty", "\n")
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.PasswordHashFile, b.TokenFile = empty, empty })
		if pm[hashPath] != hashMsg(empty) || pm[tokenPath] != tokenMsg(empty) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("hash without bcrypt prefix", func(t *testing.T) {
		f := writeFile(t, "h", "plain-text-password-123\n")
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.PasswordHashFile = f })
		if pm[hashPath] != hashMsg(f) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("hash with cost 4", func(t *testing.T) {
		f := bcryptFile(t, bcrypt.MinCost)
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.PasswordHashFile = f })
		if pm[hashPath] != hashMsg(f) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("hash with cost 10", func(t *testing.T) {
		f := bcryptFile(t, 10)
		if pm := check(t, func(b *config.BootstrapAdminConfig) { b.PasswordHashFile = f }); pm != nil {
			t.Errorf("a cost 10 hash must be accepted: %v", pm)
		}
	})
	t.Run("short token", func(t *testing.T) {
		f := writeFile(t, "t", strings.Repeat("a", 31)+"\n")
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.TokenFile = f })
		if pm[tokenPath] != tokenMsg(f) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("token of 32 and 512 characters", func(t *testing.T) {
		for _, n := range []int{32, 512} {
			f := writeFile(t, "t", strings.Repeat("a", n)+"\n")
			if pm := check(t, func(b *config.BootstrapAdminConfig) { b.TokenFile = f }); pm != nil {
				t.Errorf("%d characters: %v", n, pm)
			}
		}
	})
	t.Run("long token", func(t *testing.T) {
		f := writeFile(t, "t", strings.Repeat("a", 513))
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.TokenFile = f })
		if pm[tokenPath] != tokenMsg(f) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("token with whitespace", func(t *testing.T) {
		f := writeFile(t, "t", strings.Repeat("a", 20)+" "+strings.Repeat("b", 20)+"\n")
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.TokenFile = f })
		if pm[tokenPath] != tokenMsg(f) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("token with a control character", func(t *testing.T) {
		f := writeFile(t, "t", strings.Repeat("a", 20)+"\x00"+strings.Repeat("b", 20))
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.TokenFile = f })
		if pm[tokenPath] != tokenMsg(f) {
			t.Errorf("%v", pm)
		}
	})
	t.Run("messages never contain the content", func(t *testing.T) {
		secret := "short-" + randomHex(t, 4)
		f := writeFile(t, "t", secret)
		pm := check(t, func(b *config.BootstrapAdminConfig) { b.TokenFile = f })
		for _, m := range pm {
			if strings.Contains(m, secret) {
				t.Errorf("the message leaks the file content: %q", m)
			}
		}
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOIDCClaimsAndUserMappings(t *testing.T) {
	mod := func(f func(o *config.OIDCConfig)) error {
		c := prodConfig()
		f(&c.Auth.OIDC)
		return Validate(&c, false)
	}
	if pm := problemMap(mod(func(o *config.OIDCConfig) { o.NameClaim = "" })); pm["auth.oidc.nameClaim"] != "must not be empty" {
		t.Errorf("nameClaim: %v", pm)
	}
	if pm := problemMap(mod(func(o *config.OIDCConfig) { o.EmailClaim = "" })); pm["auth.oidc.emailClaim"] != "must not be empty" {
		t.Errorf("emailClaim: %v", pm)
	}
	if err := mod(func(o *config.OIDCConfig) { o.NameClaim, o.EmailClaim = "display_name", "mail" }); err != nil {
		t.Errorf("custom claims: %v", err)
	}
	// the defaults of an unset file are the standard claims
	d := config.Default().Auth.OIDC
	if d.NameClaim != "name" || d.EmailClaim != "email" {
		t.Errorf("claim defaults: %q %q", d.NameClaim, d.EmailClaim)
	}

	bad := map[string]string{"admin": "root", "operator": "email:nobody", "viewer": "email:@example.test"}
	for role, entry := range bad {
		err := mod(func(o *config.OIDCConfig) {
			switch role {
			case "admin":
				o.RoleMapping.Users.Admin = []string{"sub:ok", entry}
			case "operator":
				o.RoleMapping.Users.Operator = []string{entry}
			case "viewer":
				o.RoleMapping.Users.Viewer = []string{entry}
			}
		})
		idx := 0
		if role == "admin" {
			idx = 1
		}
		path := "auth.oidc.roleMapping.users." + role + "[" + string(rune('0'+idx)) + "]"
		if got := problemMap(err)[path]; got != `must be "sub:<subject>" or "email:<address>"` {
			t.Errorf("%s: %q (%v)", path, got, Problems(err))
		}
	}
	// email_verified covers the standard claim only: "email:" entries need it
	emailPath := "auth.oidc.roleMapping.users.admin[0]"
	err := mod(func(o *config.OIDCConfig) {
		o.EmailClaim = "profile.mail"
		o.RoleMapping.Users.Admin = []string{"email:una@example.test"}
	})
	if got := problemMap(err)[emailPath]; !strings.Contains(got, `need auth.oidc.emailClaim "email"`) {
		t.Errorf("email entry with another emailClaim: %q (%v)", got, Problems(err))
	}
	if err := mod(func(o *config.OIDCConfig) {
		o.EmailClaim = "profile.mail"
		o.RoleMapping.Users.Admin = []string{"sub:abc-123"}
	}); err != nil {
		t.Errorf("sub entries work with any emailClaim: %v", err)
	}
	for _, entry := range []string{"sub:abc-123", "sub:00u1a2b3", "email:una@example.test", "email:first.last+tag@sub.example.test"} {
		if err := mod(func(o *config.OIDCConfig) { o.RoleMapping.Users.Viewer = []string{entry} }); err != nil {
			t.Errorf("%q: %v", entry, err)
		}
	}
	for _, entry := range []string{"sub:", "sub: ", "email:", "email:a@", "sub", "Sub:abc"} {
		if err := mod(func(o *config.OIDCConfig) { o.RoleMapping.Users.Viewer = []string{entry} }); err == nil {
			t.Errorf("%q must be refused", entry)
		}
	}

	// user entries count as a way to log in
	nobody := func(o *config.OIDCConfig) { o.RoleMapping = config.RoleMapping{} }
	if pm := problemMap(mod(nobody)); pm["auth.oidc.roleMapping"] == "" {
		t.Errorf("an OIDC login nobody can use must fail: %v", pm)
	}
	if err := mod(func(o *config.OIDCConfig) {
		nobody(o)
		o.RoleMapping.Users.Admin = []string{"email:admin@example.test"}
	}); err != nil {
		t.Errorf("a users entry is enough: %v", err)
	}
}

// The new settings can be overridden by environment variables (lists with commas).
func TestEnvOverridesForTheLoginSettings(t *testing.T) {
	p := writeFile(t, "c.yaml", prodMinimal)
	cfg, err := load(p, false, env(map[string]string{
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_ENABLED":           "true",
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_USERNAME":          "ops",
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_PASSWORDHASHFILE":  "/etc/myriarchd/bootstrap/password-hash",
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_TOKENFILE":         "/etc/myriarchd/bootstrap/token",
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_KEEPWITHOIDC":      "true",
		"MYRIARCH_AUTH_OIDC_NAMECLAIM":                   "display_name",
		"MYRIARCH_AUTH_OIDC_EMAILCLAIM":                  "mail",
		"MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_ADMIN":     "sub:a1, sub:abc",
		"MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_OPERATOR":  "sub:b1",
		"MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_VIEWER":    "sub:v1",
		"MYRIARCH_OPAMP_ENROLLMENTTOKENS":                "ignored: a slice of structs is not overridable",
		"MYRIARCH_AUTH_OIDC_ROLEMAPPING_ADMIN":           "admins",
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_THISDOESNOTEXIST":  "x",
		"MYRIARCH_AUTH_BOOTSTRAPADMIN_USERNAME_IGNORED_": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Auth.BootstrapAdmin
	if !b.Enabled || b.Username != "ops" || b.PasswordHashFile != "/etc/myriarchd/bootstrap/password-hash" || b.TokenFile != "/etc/myriarchd/bootstrap/token" || !b.KeepWithOIDC {
		t.Errorf("bootstrap admin: %+v", b)
	}
	o := cfg.Auth.OIDC
	if o.NameClaim != "display_name" || o.EmailClaim != "mail" {
		t.Errorf("claims: %q %q", o.NameClaim, o.EmailClaim)
	}
	if strings.Join(o.RoleMapping.Users.Admin, "|") != "sub:a1|sub:abc" || strings.Join(o.RoleMapping.Users.Operator, "|") != "sub:b1" || strings.Join(o.RoleMapping.Users.Viewer, "|") != "sub:v1" {
		t.Errorf("users mapping: %+v", o.RoleMapping.Users)
	}
	if len(cfg.OpAMP.EnrollmentTokens) != 0 {
		t.Errorf("enrollment tokens are not overridable by environment: %+v", cfg.OpAMP.EnrollmentTokens)
	}
}
