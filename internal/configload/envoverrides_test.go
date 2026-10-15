// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
)

// leafValues flattens a configuration into the values of all its leaves, keyed by the dotted YAML path.
func leafValues(cfg *config.Config) map[string]any {
	out := map[string]any{}
	walkLeaves(reflect.ValueOf(cfg).Elem(), nil, func(path []string, f reflect.Value) {
		out[strings.Join(path, ".")] = f.Interface()
	})
	return out
}

// sampleFor returns an environment value for a leaf of type t and the value that the loader must store, different from the
// development default cur.
func sampleFor(t *testing.T, o EnvOverride, cur reflect.Value) (string, any) {
	t.Helper()
	switch {
	case o.Type == durationType:
		return "7m", 7 * time.Minute
	case o.Type.Kind() == reflect.String:
		return "sample-value", reflect.ValueOf("sample-value").Convert(o.Type).Interface()
	case o.Type.Kind() == reflect.Bool:
		return map[bool]string{true: "false", false: "true"}[cur.Bool()], !cur.Bool()
	case o.Type.Kind() >= reflect.Int && o.Type.Kind() <= reflect.Int64:
		return "123", reflect.ValueOf(int64(123)).Convert(o.Type).Interface()
	case o.Type.Kind() >= reflect.Uint && o.Type.Kind() <= reflect.Uint64:
		return "123", reflect.ValueOf(uint64(123)).Convert(o.Type).Interface()
	case o.Type.Kind() == reflect.Float32 || o.Type.Kind() == reflect.Float64:
		return "12.5", reflect.ValueOf(12.5).Convert(o.Type).Interface()
	case o.Type.Kind() == reflect.Slice && o.Type.Elem().Kind() == reflect.String:
		return "one, two", []string{"one", "two"}
	}
	t.Fatalf("no sample for %s (%s)", o.Path, o.Type)
	return "", nil
}

// derivedDefaults lists the settings whose default follows another setting while they are left out: an override of the
// key changes them too, to the value that their default derives from it (the sample of a duration is 7m).
var derivedDefaults = map[string]map[string]any{
	// install.grantTtl is the shorter of one hour and artifacts.downloadTokenTtl (defaultInstallGrantTTL)
	"artifacts.downloadTokenTtl": {"install.grantTtl": 7 * time.Minute},
}

func TestEnvOverridesListEveryOverridableSettingAndSetExactlyThatField(t *testing.T) {
	overrides := EnvOverrides()
	require.Greater(t, len(overrides), 100, "the list looks incomplete")

	base, _, err := loadWith("", Options{Dev: true, SkipFiles: true}, env(nil))
	require.NoError(t, err)
	baseValues := leafValues(&base)

	seen := map[string]bool{}
	for _, o := range overrides {
		require.False(t, seen[o.Name], "duplicate variable %s", o.Name)
		seen[o.Name] = true
		assert.Equal(t, envName(strings.Split(o.Path, ".")), o.Name)

		raw, want := sampleFor(t, o, reflect.ValueOf(baseValues[o.Path]))
		// The result may carry validation errors (a sample value is not a valid setting of every field); the override
		// itself must not fail.
		got, _, err := loadWith("", Options{Dev: true, SkipFiles: true}, env(map[string]string{o.Name: raw}))
		if err != nil {
			assert.NotContains(t, err.Error(), "environment "+o.Name, o.Name)
		}
		gotValues := leafValues(&got)
		var changed []string
		for path, v := range gotValues {
			if !reflect.DeepEqual(v, baseValues[path]) {
				changed = append(changed, path)
			}
		}
		wantChanged := []string{o.Path}
		for p := range derivedDefaults[o.Path] {
			wantChanged = append(wantChanged, p)
		}
		sort.Strings(changed)
		sort.Strings(wantChanged)
		assert.Equal(t, wantChanged, changed, "%s changes its own field and the settings whose default follows it, nothing else", o.Name)
		assert.Equal(t, want, gotValues[o.Path], o.Name)
		for p, v := range derivedDefaults[o.Path] {
			assert.Equal(t, v, gotValues[p], "%s: the default of %s", o.Name, p)
		}
	}
}

func TestEnvOverridesLeaveOutWhatTheLoaderCannotSet(t *testing.T) {
	paths := map[string]bool{}
	for _, o := range EnvOverrides() {
		paths[o.Path] = true
		assert.True(t, overridable(o.Type), o.Path)
	}
	for _, p := range []string{
		"opamp.enrollmentTokens", "notifications.channels", "auth.devLogin.users",
		"executors.secrets", "executors.secretEnvHosts", "executors.secretFileHosts", "executors.requireSecretHosts",
	} {
		assert.False(t, paths[p], "%s is not settable through the environment", p)
	}
	for _, p := range []string{"server.publicUrl", "database.dsn", "auth.oidc.scopes", "server.shutdownTimeout", "database.maxConns"} {
		assert.True(t, paths[p], "%s is settable through the environment", p)
	}
}

func TestOverridable(t *testing.T) {
	for _, v := range []any{time.Second, "", false, int(0), int32(0), int64(0), uint32(0), uint64(0), float64(0), []string{}} {
		assert.True(t, overridable(reflect.TypeOf(v)), "%T", v)
	}
	for _, v := range []any{map[string]string{}, map[string][]string{}, []config.DevUser{}, []int{}, struct{}{}} {
		assert.False(t, overridable(reflect.TypeOf(v)), "%T", v)
	}
}

func TestYAMLKeyAndIsSecretKey(t *testing.T) {
	st := reflect.TypeOf(config.ExecutorsConfig{})
	f, ok := st.FieldByName("RequireSecretHosts")
	require.True(t, ok)
	assert.Equal(t, "", YAMLKey(f), "yaml:\"-\" is not part of the file format")
	f, _ = st.FieldByName("SecretEnvPrefix")
	assert.Equal(t, "secretEnvPrefix", YAMLKey(f))

	for _, k := range []string{"dsn", "clientSecret", "webhookSecret", "url", "password", "seedEnrollmentToken"} {
		assert.True(t, IsSecretKey(k), k)
	}
	for _, k := range []string{"dsnFile", "username", "tokenFile"} {
		assert.False(t, IsSecretKey(k), k)
	}
}
