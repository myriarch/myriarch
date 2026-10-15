// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import "time"

// ChartMini is the configuration of the golden test of the chart schema: the structures of MiniConfig and the shapes that it lacks.
type ChartMini struct {
	Server MiniServer  `yaml:"server"`
	Notify MiniNotify  `yaml:"notify"`
	Dev    MiniDev     `yaml:"dev"`
	Shapes ChartShapes `yaml:"shapes"`
}

// ChartShapes holds the field types that MiniConfig does not have.
type ChartShapes struct {
	// Port is an unsigned port number.
	Port uint16 `yaml:"port"`
	// Retries is a signed number that may be negative.
	Retries int32 `yaml:"retries"`
	// Names is a list of strings.
	Names []string `yaml:"names"`
	// Budget is a duration; the loader accepts only Go syntax.
	Budget time.Duration `yaml:"budget"`
}

// The structures below are what the generator refuses. Each is the root of one case of TestChartSchemaRefusesWhatItCannotRepresent.

// ChartBadSlice has a list of numbers.
type ChartBadSlice struct {
	// Numbers is a list of integers, which the configuration reference does not support either.
	Numbers []int `yaml:"numbers"`
}

// ChartBadMap has a map of numbers.
type ChartBadMap struct {
	// Counts maps names to numbers.
	Counts map[string]int `yaml:"counts"`
}

// ChartBadTime has a time stamp.
type ChartBadTime struct {
	// When is a point in time.
	When time.Time `yaml:"when"`
}

// ChartBadNoComment has a field without a comment.
type ChartBadNoComment struct {
	Name string `yaml:"name"`
}

// ChartBadBacktick has a comment with an unpaired backtick.
type ChartBadBacktick struct {
	// Name is the `name of the thing.
	Name string `yaml:"name"`
}

// ChartBadNested has a nested structure with a field that the generator refuses.
type ChartBadNested struct {
	// Inner is a structure.
	Inner ChartBadSlice `yaml:"inner"`
}

// ChartLeaky has a setting of every shape that can hold text, each named like a secret, and the same inside a structure, a list of
// structures and a map of structures. TestChartSchemaSecretCheckSeesEveryShape expects the check of TestChartSchemaOffersNoSecret
// to report every one of them.
type ChartLeaky struct {
	// APIKey is a string.
	APIKey string `yaml:"apiKey"`
	// Tokens is a list of strings.
	Tokens []string `yaml:"tokens"`
	// Passwords maps names to strings.
	Passwords map[string]string `yaml:"passwords"`
	// Credentials maps names to lists of strings.
	Credentials map[string][]string `yaml:"credentials"`
	// URL is a string with a key that the loader redacts.
	URL string `yaml:"url"`
	// Nested is a structure.
	Nested ChartLeakySecrets `yaml:"nested"`
	// Items is a list of structures.
	Items []ChartLeakySecrets `yaml:"items"`
	// ByName maps names to structures.
	ByName map[string]ChartLeakySecrets `yaml:"byName"`
}

// ChartLeakySecrets has two strings that are named like secrets.
type ChartLeakySecrets struct {
	// Passphrase is a string.
	Passphrase string `yaml:"passphrase"`
	// Bearer is a string.
	Bearer string `yaml:"bearer"`
}

// ChartHarmless has settings that the check of the secrets must not report: names that mention a secret but hold a path, a
// directory, a prefix or a time span, and settings that are not text.
type ChartHarmless struct {
	// TokenFile is the path of a file.
	TokenFile string `yaml:"tokenFile"`
	// KeyDir is a directory.
	KeyDir string `yaml:"keyDir"`
	// SecretPrefix is a prefix.
	SecretPrefix string `yaml:"secretPrefix"`
	// TokenTTL is a time span.
	TokenTTL time.Duration `yaml:"tokenTtl"`
	// LockKey is a number.
	LockKey int `yaml:"lockKey"`
	// RequireToken is a switch.
	RequireToken bool `yaml:"requireToken"`
	// Hosts is a list of strings with an unsuspicious name.
	Hosts []string `yaml:"hosts"`
	// Labels maps names to strings with an unsuspicious name.
	Labels map[string]string `yaml:"labels"`
	// Secrets maps names to structures whose strings are unsuspicious.
	Secrets map[string]ChartHarmlessEntry `yaml:"secrets"`
}

// ChartHarmlessEntry is an entry of the map of ChartHarmless.
type ChartHarmlessEntry struct {
	// Env is a string with an unsuspicious name.
	Env string `yaml:"env"`
	// AllowedHosts is a list of strings with an unsuspicious name.
	AllowedHosts []string `yaml:"allowedHosts"`
}
