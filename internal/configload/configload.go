// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package configload loads and validates the myriarchd configuration
// (rules: see the package documentation of internal/config).
//
// *File fields are read at startup only while their feature is active: the
// OIDC client secret file while auth.oidc.enabled, the GitOps webhook secret
// file while gitops.enabled (see readSecretFiles).
package configload

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// EnvPrefix is the prefix of environment overrides (MYRIARCH_<PATH>).
const EnvPrefix = brand.EnvPrefix

// Options control how a configuration is loaded.
type Options struct {
	// Dev starts from config.DevDefaults() and relaxes the production checks
	// of Validate (myriarchd dev).
	Dev bool
	// SkipFiles does not read the files the configuration refers to (*File
	// fields, outbound.caFile, certificates, ...): their paths count as
	// present. For checks where the secrets are not mounted (CI, helm
	// template).
	SkipFiles bool
	// CheckFiles additionally reads and checks the files that only the
	// components read at startup (CA files, certificates and keys, token
	// files): "myriarchd config validate" without --skip-files. "myriarchd serve"
	// leaves it off, the components report such files themselves. Has no
	// effect together with SkipFiles.
	CheckFiles bool
}

// checksFiles reports whether the validation reads the referenced files.
func (o Options) checksFiles() bool { return o.CheckFiles && !o.SkipFiles }

// ReadError is returned by LoadWithOptions when the configuration file itself
// cannot be read (as opposed to being invalid).
type ReadError struct{ Err error }

func (e *ReadError) Error() string { return "read config file: " + e.Err.Error() }
func (e *ReadError) Unwrap() error { return e.Err }

// Load reads the YAML file at path (may be empty: defaults + env only) on
// top of config.Default() (or config.DevDefaults() if dev is true), expands
// ${VAR} references, applies MYRIARCH_* environment overrides, reads *File
// secrets and validates the result.
func Load(path string, dev bool) (config.Config, error) {
	cfg, _, err := LoadWithOptions(path, Options{Dev: dev})
	return cfg, err
}

// LoadWithOptions is Load with options. It also returns the warnings of
// Warnings (settings that weaken a control but do not prevent startup). An
// unreadable configuration file is a *ReadError; invalid content (YAML syntax,
// unknown keys, validation) is any other error, see Problems.
func LoadWithOptions(path string, opts Options) (config.Config, []string, error) {
	return loadWith(path, opts, os.LookupEnv)
}

func load(path string, dev bool, lookup func(string) (string, bool)) (config.Config, error) {
	cfg, _, err := loadWith(path, Options{Dev: dev}, lookup)
	return cfg, err
}

func loadWith(path string, opts Options, lookup func(string) (string, bool)) (config.Config, []string, error) {
	cfg := config.Default()
	if opts.Dev {
		cfg = config.DevDefaults()
	}
	var inline []string
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, nil, &ReadError{Err: err}
		}
		if err := decodeYAML(data, &cfg); err != nil {
			return cfg, nil, &fileError{file: path, err: err}
		}
		if !opts.Dev {
			inline = inlineSecrets(reflect.ValueOf(&cfg).Elem(), "")
		}
	}
	expandEnv(reflect.ValueOf(&cfg).Elem(), lookup)
	if err := applyEnvOverrides(&cfg, lookup); err != nil {
		return cfg, nil, err
	}
	// All problems are reported together: unreadable secret files first, then
	// what the validation finds (it does not read these files again).
	var errs []error
	if !opts.SkipFiles {
		if err := readSecretFiles(&cfg); err != nil {
			errs = append(errs, err)
		}
	}
	if err := ValidateOptions(&cfg, opts); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return cfg, nil, errors.Join(errs...)
	}
	warnings := Warnings(&cfg, opts.Dev)
	for _, p := range inline {
		warnings = append(warnings, p+" is an inline secret (a literal value in the configuration file); provide it through the matching *File setting or an environment variable")
	}
	return cfg, warnings, nil
}

// fileError is a problem inside the configuration file (syntax, unknown key).
type fileError struct {
	file string
	err  error
}

func (e *fileError) Error() string { return fmt.Sprintf("parse config file %s: %v", e.file, e.err) }
func (e *fileError) Unwrap() error { return e.err }

// Problem is one thing that is wrong with a configuration.
type Problem struct {
	// Path is the configuration key ("auth.oidc.issuerUrl"), or the file for
	// syntax errors; empty if the problem has no location.
	Path string
	// Message is the description without the path.
	Message string
}

// String formats the problem as "<path>: <message>".
func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// Problems breaks an error of Load/LoadWithOptions into its single problems
// (one per unknown key, one per validation issue).
func Problems(err error) []Problem {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []Problem
		for _, e := range j.Unwrap() {
			out = append(out, Problems(e)...)
		}
		return out
	}
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		var out []Problem
		for _, is := range ve.Issues {
			if is.Severity != domain.SeverityError {
				continue
			}
			out = append(out, Problem{Path: is.Path, Message: strings.TrimPrefix(is.Message, is.Path+": ")})
		}
		if len(out) > 0 {
			return out
		}
	}
	var sf *secretFileError
	if errors.As(err, &sf) {
		return []Problem{{Path: sf.setting, Message: sf.msg}}
	}
	var fe *fileError
	if errors.As(err, &fe) {
		var te *yaml.TypeError
		if errors.As(fe.err, &te) && len(te.Errors) > 0 {
			out := make([]Problem, 0, len(te.Errors))
			for _, m := range te.Errors {
				out = append(out, Problem{Path: fe.file, Message: m})
			}
			return out
		}
		return []Problem{{Path: fe.file, Message: strings.TrimPrefix(fe.err.Error(), "yaml: ")}}
	}
	return []Problem{{Message: err.Error()}}
}

// decodeYAML decodes data on top of cfg. Unknown keys are an error. An empty
// document leaves cfg unchanged. time.Duration fields accept Go duration
// strings ("30s"); plain integers are rejected (unit ambiguity), which is the
// behavior of the yaml decoder for durations.
func decodeYAML(data []byte, cfg *config.Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ------------------------------------------------------------ reflection ---

var durationType = reflect.TypeOf(time.Duration(0))

// yamlName returns the yaml key of a struct field ("" = skip).
func yamlName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "-" {
		return ""
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name
}

// walkLeaves calls fn for every settable non-struct field with its yaml path.
func walkLeaves(v reflect.Value, path []string, fn func(path []string, f reflect.Value)) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name := yamlName(sf)
		if name == "" {
			continue
		}
		fv := v.Field(i)
		p := append(append([]string{}, path...), name)
		if fv.Kind() == reflect.Struct && fv.Type() != reflect.TypeOf(time.Time{}) {
			walkLeaves(fv, p, fn)
			continue
		}
		fn(p, fv)
	}
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandString replaces ${VAR} and ${VAR:-default}. An unset variable without
// default expands to the empty string.
func expandString(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRef.FindStringSubmatch(m)
		if v, ok := lookup(sub[1]); ok && v != "" {
			return v
		}
		return sub[2]
	})
}

// expandEnv expands ${VAR} references in all string values (recursively, also
// inside slices and slices of structs). Expansion happens per value after
// decoding, so a value can never inject YAML structure.
func expandEnv(v reflect.Value, lookup func(string) (string, bool)) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(expandString(v.String(), lookup))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				expandEnv(v.Field(i), lookup)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			expandEnv(v.Index(i), lookup)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			expandEnv(e, lookup)
			v.SetMapIndex(k, e)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			expandEnv(v.Elem(), lookup)
		}
	}
}

// envName returns MYRIARCH_<PATH> for a yaml path.
func envName(path []string) string {
	return EnvPrefix + strings.ToUpper(strings.Join(path, "_"))
}

// overridable reports whether an environment variable can set a leaf of this type: durations, strings, booleans, signed and
// unsigned integers, floats and lists of strings (comma separated). Maps and lists of structs (dev users, notification
// channels, enrollment tokens) are not overridable.
func overridable(t reflect.Type) bool {
	if t == durationType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.String
	}
	return false
}

// EnvOverride describes one setting that a MYRIARCH_<PATH> environment variable can override.
type EnvOverride struct {
	// Name is the environment variable, for example MYRIARCH_SERVER_PUBLICURL.
	Name string
	// Path is the dotted YAML path of the setting, for example server.publicUrl.
	Path string
	// Type is the Go type of the setting (time.Duration, string, bool, a number type or a list of strings).
	Type reflect.Type
}

// EnvOverrides lists every setting that an environment variable can override, in the order of the configuration structure.
// It is the same set that applyEnvOverrides applies, so the environment reference cannot drift from the loader.
func EnvOverrides() []EnvOverride {
	cfg := config.Default()
	var out []EnvOverride
	walkLeaves(reflect.ValueOf(&cfg).Elem(), nil, func(path []string, f reflect.Value) {
		if !overridable(f.Type()) {
			return
		}
		out = append(out, EnvOverride{Name: envName(path), Path: strings.Join(path, "."), Type: f.Type()})
	})
	return out
}

// YAMLKey returns the YAML key of a configuration field, or "" when the field is not part of the file format (`yaml:"-"`).
func YAMLKey(f reflect.StructField) string { return yamlName(f) }

// IsSecretKey reports whether the value of a YAML key is a secret that "myriarchd config print" redacts.
func IsSecretKey(key string) bool { return redactedKeys[key] }

// applyEnvOverrides applies MYRIARCH_<PATH> overrides to the overridable leaves (see overridable). Slices of structs (dev
// users, notification channels) and maps are not overridable by environment.
func applyEnvOverrides(cfg *config.Config, lookup func(string) (string, bool)) error {
	return applyEnvOverridesAt(reflect.ValueOf(cfg).Elem(), nil, lookup)
}

// applyEnvOverridesAt applies the overrides to the leaves of the struct v, whose YAML path is path ("database" for the
// database settings, nil for the whole configuration).
func applyEnvOverridesAt(v reflect.Value, path []string, lookup func(string) (string, bool)) error {
	var errs []error
	walkLeaves(v, path, func(path []string, f reflect.Value) {
		if !overridable(f.Type()) {
			return
		}
		name := envName(path)
		raw, ok := lookup(name)
		if !ok {
			return
		}
		if err := setFromString(f, raw); err != nil {
			errs = append(errs, fmt.Errorf("environment %s: %w", name, err))
		}
	})
	return errors.Join(errs...)
}

func setFromString(f reflect.Value, raw string) error {
	if f.Type() == durationType {
		if raw == "" {
			return nil
		}
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid duration %q (use Go syntax like 30s, 5m)", raw)
		}
		f.SetInt(int64(d))
		return nil
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw)
	case reflect.Bool:
		if raw == "" {
			return nil
		}
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid boolean %q", raw)
		}
		f.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if raw == "" {
			return nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, f.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid integer %q", raw)
		}
		f.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if raw == "" {
			return nil
		}
		n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, f.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid unsigned integer %q", raw)
		}
		f.SetUint(n)
	case reflect.Float32, reflect.Float64:
		if raw == "" {
			return nil
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), f.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid number %q", raw)
		}
		f.SetFloat(n)
	case reflect.Slice:
		if f.Type().Elem().Kind() != reflect.String {
			return nil // slices of structs are not overridable
		}
		parts := []string{}
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		sl := reflect.MakeSlice(f.Type(), len(parts), len(parts))
		for i, p := range parts {
			sl.Index(i).SetString(p)
		}
		f.Set(sl)
	}
	return nil
}

// readSecretFiles loads the *File variants that have an inline counterpart.
// The file content (trimmed) takes precedence over the inline value. An empty
// file is an error: the service would start without the secret.
//
// A file is read only while the feature it belongs to is active:
// database.dsnFile and the notification urlFile entries always,
// auth.oidc.clientSecretFile only while auth.oidc.enabled and
// gitops.webhookSecretFile only while gitops.enabled. The path of an inactive
// feature may point to a file that is not mounted.
func readSecretFiles(cfg *config.Config) error {
	var errs []error
	read := func(dst *string, file, setting string) {
		if err := readSecretFile(dst, file, setting); err != nil {
			errs = append(errs, err)
		}
	}
	read(&cfg.Database.DSN, cfg.Database.DSNFile, "database.dsnFile")
	if cfg.Auth.OIDC.Enabled {
		read(&cfg.Auth.OIDC.ClientSecret, cfg.Auth.OIDC.ClientSecretFile, "auth.oidc.clientSecretFile")
	}
	if cfg.GitOps.Enabled {
		read(&cfg.GitOps.WebhookSecret, cfg.GitOps.WebhookSecretFile, "gitops.webhookSecretFile")
	}
	for i := range cfg.Notifications.Channels {
		ch := &cfg.Notifications.Channels[i]
		read(&ch.URL, ch.URLFile, fmt.Sprintf("notifications.channels[%d].urlFile", i))
	}
	return errors.Join(errs...)
}

// readSecretFile reads the secret file of a setting into dst (nothing happens for an empty path). The content, without
// surrounding white space, wins over the inline value in dst. A file that cannot be read or is empty is a
// *secretFileError, whose message names the setting and the path, never the content.
func readSecretFile(dst *string, file, setting string) error {
	if file == "" {
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &secretFileError{setting: setting, msg: fmt.Sprintf("cannot read %s: %v", file, err)}
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return &secretFileError{setting: setting, msg: fmt.Sprintf("file %s is empty", file)}
	}
	*dst = v
	return nil
}

// secretFileError is a secret file that cannot be used.
type secretFileError struct{ setting, msg string }

func (e *secretFileError) Error() string { return e.setting + ": " + e.msg }

// roleValid is domain.Role.Valid for config values.
func roleValid(r domain.Role) bool { return r.Valid() }

// redactedKeys are the YAML keys whose values are secrets.
var redactedKeys = map[string]bool{
	"dsn": true, "clientSecret": true, "webhookSecret": true, "url": true, "password": true, "seedEnrollmentToken": true,
}

// RedactedYAML returns the configuration as YAML with every secret value (the
// database DSN, client and webhook secrets, the URLs of notification channels,
// dev passwords and the dev enrollment token) replaced by "<redacted>"; empty
// values stay empty so a missing secret is visible.
func RedactedYAML(c *config.Config) ([]byte, error) {
	cp := *c
	cp.Notifications.Channels = append([]config.NotificationChannel(nil), c.Notifications.Channels...)
	cp.Auth.DevLogin.Users = append([]config.DevUser(nil), c.Auth.DevLogin.Users...)
	redact(reflect.ValueOf(&cp).Elem())
	return yaml.Marshal(&cp)
}

func redact(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			fv := v.Field(i)
			if fv.Kind() == reflect.String && redactedKeys[yamlName(sf)] && fv.String() != "" {
				fv.SetString("<redacted>")
				continue
			}
			if fv.Kind() == reflect.String && urlKeyRE.MatchString(yamlName(sf)) {
				fv.SetString(redactURLUserInfo(fv.String()))
				continue
			}
			redact(fv)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			redact(v.Index(i))
		}
	}
}

// urlKeyRE matches the YAML keys of URL-valued settings ("proxyUrl", "repoUrl", "sourceBaseUrl", ...).
var urlKeyRE = regexp.MustCompile(`(Url|URL)$`)

// redactURLUserInfo hides the password of a URL's user information (the user name stays visible).
func redactURLUserInfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); !has {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), "<redacted>")
	return u.String()
}

// inlineSecretKeys are the YAML keys whose values are secrets that belong in a file or the environment.
var inlineSecretKeys = map[string]bool{"dsn": true, "clientSecret": true, "webhookSecret": true, "url": true, "password": true}

// inlineSecrets lists the settings that hold a secret as a literal value of the configuration file
// (not a ${VAR} reference). The values are never part of the result. dev settings are not listed.
func inlineSecrets(v reflect.Value, path string) []string {
	var out []string
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			name := yamlName(sf)
			if !sf.IsExported() || name == "dev" || name == "-" {
				continue
			}
			p := name
			if path != "" {
				p = path + "." + name
			}
			fv := v.Field(i)
			if fv.Kind() == reflect.String && inlineSecretKeys[name] && fv.String() != "" && !strings.Contains(fv.String(), "${") {
				out = append(out, p)
				continue
			}
			out = append(out, inlineSecrets(fv, p)...)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			out = append(out, inlineSecrets(v.Index(i), fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return out
}
