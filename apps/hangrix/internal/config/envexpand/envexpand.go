// Package envexpand provides runtime expansion of ${env:NAME} references in
// config values. It is applied by config.NewConfig after viper has merged YAML,
// defaults, and env overrides but before Unmarshal — so string values anywhere
// in the config tree (including env-overridden values) can reference other
// environment variables.
package envexpand

import (
	"os"
	"regexp"

	"github.com/spf13/viper"
)

// Pattern is the regex pattern that matches a single ${env:NAME} reference.
// NAME must start with a letter or underscore followed by zero or more
// alphanumeric or underscore characters.
const Pattern = `\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`

var patternRe = regexp.MustCompile(Pattern)

// LookupFunc resolves an environment variable name to its value.
// Return (value, true) when the variable exists; ("", false) otherwise.
type LookupFunc func(name string) (value string, ok bool)

// ExpandString replaces every ${env:NAME} reference in s with the value
// returned by lookup. If lookup is nil, os.LookupEnv is used as the default.
// Missing variables expand to the empty string. Malformed patterns (anything
// that does not match Pattern) are left unchanged.
func ExpandString(s string, lookup LookupFunc) string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return patternRe.ReplaceAllStringFunc(s, func(match string) string {
		// match is guaranteed to have the form "${env:NAME}" with a valid
		// NAME because ReplaceAllStringFunc only calls us for regex matches.
		name := match[len("${env:") : len(match)-1]
		val, ok := lookup(name)
		if !ok {
			return ""
		}
		return val
	})
}

// ApplyToViper walks every string leaf in v's merged settings and expands
// ${env:NAME} references in place. If lookup is nil, os.LookupEnv is used.
// Call this after ReadInConfig / AutomaticEnv but before Unmarshal so the
// expanded values are what the Config struct receives.
func ApplyToViper(v *viper.Viper, lookup LookupFunc) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, key := range v.AllKeys() {
		val := v.Get(key)
		s, ok := val.(string)
		if !ok {
			continue
		}
		expanded := ExpandString(s, lookup)
		if expanded != s {
			v.Set(key, expanded)
		}
	}
}
