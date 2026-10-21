// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Secret redaction for configuration and startup errors (Metrum AI Router).
//
// Configuration loading expands ${NAME} references and env.json values into
// memory, and startup resolves *_env fields such as password_env, api_key_env
// and dsn_env. Errors raised on those paths (YAML type errors, validation,
// backend connection failures) can quote the expanded text. The helpers here
// scrub every resolved secret value from error text before the error leaves
// the package, so callers that log or print an error name the field and the
// environment variable and never the value.

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// minRedactedSecretLen skips trivially short values (for example "1" or
// "on") whose replacement would mangle unrelated error text while protecting
// nothing.
const minRedactedSecretLen = 4

// envReferencePattern matches the $NAME and ${NAME} forms that os.ExpandEnv
// substitutes in configuration text.
var envReferencePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// literalSecretYAMLKeys are configuration keys whose value is itself a
// credential (as opposed to the name of an environment variable holding one).
var literalSecretYAMLKeys = map[string]bool{
	"api_key":       true,
	"password":      true,
	"secret":        true,
	"client_secret": true,
	"token":         true,
	"dsn":           true,
}

// callerVerifierYAMLKeys hold caller token verifiers. A verifier is not a
// credential, but config errors still never print one, so a stray quote of
// it (for example a YAML type error) is scrubbed like a secret.
var callerVerifierYAMLKeys = map[string]bool{
	"token_hash":   true,
	"token_sha256": true,
}

// redactedConfigError carries scrubbed error text only. It deliberately does
// not unwrap to the original error, because the original message is the one
// that may quote a secret value.
type redactedConfigError struct {
	msg string
}

func (e *redactedConfigError) Error() string { return e.msg }

// secretRedactor maps secret values to the label that replaces them.
type secretRedactor struct {
	labels map[string]string
}

func newSecretRedactor() *secretRedactor {
	return &secretRedactor{labels: map[string]string{}}
}

// addValue registers value for redaction under label. Labels name the env
// var or config field so redacted errors stay actionable.
func (r *secretRedactor) addValue(value, label string) {
	if r == nil {
		return
	}
	for _, candidate := range []string{value, strings.TrimSpace(value)} {
		if len(candidate) < minRedactedSecretLen {
			continue
		}
		r.addLabel(candidate, label)
		// yaml.v3 type errors quote long scalars as their first seven bytes
		// plus "..."; scrub that truncated form too so no prefix leaks.
		if len(candidate) > yamlErrorScalarMax {
			r.addLabel(candidate[:yamlErrorScalarPrefix]+"...", label)
		}
	}
}

// yaml.v3 shortens scalars longer than yamlErrorScalarMax bytes in unmarshal
// errors to their first yamlErrorScalarPrefix bytes plus "...".
const (
	yamlErrorScalarMax    = 10
	yamlErrorScalarPrefix = 7
)

func (r *secretRedactor) addLabel(value, label string) {
	if _, exists := r.labels[value]; !exists {
		r.labels[value] = label
	}
}

// addEnv registers the current value of environment variable name.
func (r *secretRedactor) addEnv(name string) {
	name = strings.TrimSpace(name)
	if name == "" || !validEnvName(name) {
		return
	}
	if value, ok := os.LookupEnv(name); ok {
		r.addValue(value, "[redacted: value of $"+name+"]")
	}
}

// envReferenceNames lists the environment variables referenced from raw
// configuration text, matching what os.ExpandEnv substitutes.
func envReferenceNames(raw []byte) []string {
	var names []string
	for _, match := range envReferencePattern.FindAllSubmatch(raw, -1) {
		name := string(match[1])
		if name == "" {
			name = string(match[2])
		}
		names = append(names, name)
	}
	return names
}

// addConfig registers secrets reachable from a parsed configuration: the
// resolved value of every *_env field, literal credential fields, caller
// token verifiers, and credential-bearing header values.
func (r *secretRedactor) addConfig(cfg *Config) {
	if r == nil || cfg == nil {
		return
	}
	for _, name := range cfg.secretEnvRefs {
		r.addEnv(name)
	}
	r.walk(reflect.ValueOf(cfg).Elem(), "")
}

func (r *secretRedactor) walk(v reflect.Value, yamlKey string) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			r.walk(v.Elem(), yamlKey)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			key, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
			if key == "-" {
				continue
			}
			r.walk(v.Field(i), key)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			r.walk(v.Index(i), yamlKey)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if yamlKey == "headers" && iter.Key().Kind() == reflect.String && iter.Value().Kind() == reflect.String {
				if credentialHeaderName(iter.Key().String()) {
					r.addValue(iter.Value().String(), "[redacted: header "+iter.Key().String()+"]")
				}
				continue
			}
			r.walk(iter.Value(), yamlKey)
		}
	case reflect.String:
		switch {
		case strings.HasSuffix(yamlKey, "_env"):
			name := strings.TrimSpace(v.String())
			if name != "" && !validEnvName(name) {
				// Not an env var name, so possibly a credential pasted into
				// the wrong field; never echo it.
				r.addValue(v.String(), "[redacted: invalid "+yamlKey+" value]")
				return
			}
			r.addEnv(name)
		case literalSecretYAMLKeys[yamlKey], callerVerifierYAMLKeys[yamlKey]:
			r.addValue(v.String(), "[redacted: "+yamlKey+"]")
		}
	}
}

func credentialHeaderName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, marker := range []string{"authorization", "api-key", "apikey", "token", "secret", "cookie", "password"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// redact returns text with every registered secret value replaced by its
// label. Longer values are replaced first so a secret that contains another
// secret is removed whole.
func (r *secretRedactor) redact(text string) string {
	if r == nil || len(r.labels) == 0 {
		return text
	}
	values := make([]string, 0, len(r.labels))
	for value := range r.labels {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	for _, value := range values {
		text = strings.ReplaceAll(text, value, r.labels[value])
	}
	return text
}

// RedactConfigError scrubs secrets in a partially decoded configuration from an
// error, including truncated YAML scalar quotes. The returned error does not
// unwrap to the original, potentially secret-bearing error.
func RedactConfigError(err error, cfg *Config) error {
	r := newSecretRedactor()
	r.addConfig(cfg)
	return redactConfigError(err, r)
}

// redactConfigError scrubs registered secret values from err. A nil error
// stays nil; any other error becomes a redactedConfigError so the original,
// possibly secret-bearing message cannot be recovered with errors.Unwrap.
func redactConfigError(err error, r *secretRedactor) error {
	if err == nil {
		return nil
	}
	return &redactedConfigError{msg: r.redact(err.Error())}
}
