// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// SecretRef names an environment variable or a file that holds a credential.
// A configuration field is either <name>_env or <name>_file; a literal value in
// YAML is rejected at validation time so a credential never lives in a config
// file, a backup or a log.
type SecretRef struct {
	Env  string
	File string
}

// set reports whether the reference is configured at all.
func (r SecretRef) set() bool {
	return strings.TrimSpace(r.Env) != "" || strings.TrimSpace(r.File) != ""
}

// validate checks that at most one of Env and File is set and that the
// environment variable name is usable.
func (r SecretRef) validate(field string) error {
	env := strings.TrimSpace(r.Env)
	file := strings.TrimSpace(r.File)
	if env != "" && file != "" {
		return fmt.Errorf("%s: set exactly one of %s_env or %s_file", field, fieldName(field), fieldName(field))
	}
	if env != "" {
		for i, ch := range env {
			ok := ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (i > 0 && ch >= '0' && ch <= '9')
			if !ok {
				return fmt.Errorf("%s: %q is not a valid environment variable name", field, env)
			}
		}
	}
	return nil
}

// fieldName strips the last dot-separated component of a field path so the
// error can name <field>_env / <field>_file.
func fieldName(field string) string {
	if i := strings.LastIndex(field, "."); i >= 0 {
		return field[i+1:]
	}
	return field
}

// decodeEABKey accepts the base64url (unpadded or padded) and standard base64
// encodings CAs use for external account binding keys.
func decodeEABKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("empty key")
	}
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if raw, err := encoding.DecodeString(encoded); err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("unsupported encoding")
}

// resolve reads the secret. Errors name the field and the environment variable
// or path but never the value.
func (r SecretRef) resolve(field string) (string, error) {
	if env := strings.TrimSpace(r.Env); env != "" {
		value := os.Getenv(env)
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s: environment variable %s is empty or unset", field, env)
		}
		return value, nil
	}
	if path := strings.TrimSpace(r.File); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: read secret file %s: %w", field, path, err)
		}
		value := strings.TrimRight(string(raw), "\r\n")
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s: secret file %s is empty", field, path)
		}
		return value, nil
	}
	return "", nil
}

// secretSet holds the credentials resolved for one manager and scrubs them from
// every error or log line that leaves the package.
type secretSet struct {
	values []secretValue
}

type secretValue struct {
	label string
	value string
}

// resolve reads ref under label and remembers the value for redaction.
func (s *secretSet) resolve(ref SecretRef, label string) (string, error) {
	value, err := ref.resolve(label)
	if err != nil {
		return "", err
	}
	if value != "" {
		s.values = append(s.values, secretValue{label: label, value: value})
	}
	return value, nil
}

// redact replaces every resolved credential value in text with its label.
// Short values are still redacted: unlike the config redactor there is no
// unrelated text here that a two-character token could mangle.
func (s *secretSet) redact(text string) string {
	if s == nil || text == "" {
		return text
	}
	for _, secret := range s.values {
		if secret.value == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret.value, "<redacted:"+secret.label+">")
	}
	return text
}

// empty reports whether nothing was resolved.
func (s *secretSet) empty() bool {
	return s == nil || len(s.values) == 0
}
