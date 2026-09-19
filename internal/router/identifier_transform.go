// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	siv "github.com/secure-io/siv-go"
	"gopkg.in/yaml.v3"
)

type IdentifierTransform interface {
	Encode(upstreamID string) string
	Decode(callerID string) (upstreamID string, err error)
	KeyIDs() (current string, previous string)
}

type IdentifierConfig struct {
	Mode      string                    `yaml:"mode"`
	Transform IdentifierTransformConfig `yaml:"transform"`
}
type IdentifierTransformConfig struct {
	Current  IdentifierKeyConfig  `yaml:"current"`
	Previous *IdentifierKeyConfig `yaml:"previous"`
}

// Reject extra epochs instead of silently ignoring rotation configuration.
func (c *IdentifierTransformConfig) UnmarshalYAML(n *yaml.Node) error {
	type plain IdentifierTransformConfig
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "current" && k != "previous" {
			return fmt.Errorf("identifiers.transform: unsupported epoch %q", k)
		}
	}
	return n.Decode((*plain)(c))
}

type IdentifierKeyConfig struct {
	KeyID      string    `yaml:"key_id"`
	Key        string    `yaml:"key" json:"-"`
	ValidUntil time.Time `yaml:"valid_until"`
}
type identifierTransform struct {
	current, previous     cipher.AEAD
	currentID, previousID string
	validUntil            time.Time
}

func (c IdentifierConfig) passthrough() bool { return c.Mode == "passthrough" }
func newIdentifierTransform(c IdentifierConfig) (IdentifierTransform, error) {
	if c.passthrough() {
		return nil, nil
	}
	if c.Mode != "" && c.Mode != "rewrite" {
		return nil, errors.New("identifiers.mode must be rewrite or passthrough")
	}
	parse := func(k IdentifierKeyConfig) (cipher.AEAD, error) {
		if len(k.KeyID) != 1 || !strings.Contains("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", k.KeyID) {
			return nil, errors.New("identifiers key_id must be one base62 epoch character")
		}
		raw := strings.TrimSpace(k.Key)
		b, e := hex.DecodeString(raw)
		if e != nil || len(b) != 64 {
			b, e = base64.StdEncoding.DecodeString(raw)
		}
		if e != nil || len(b) != 64 {
			return nil, errors.New("identifiers key is required and must be 64-byte hex or base64")
		}
		return siv.NewCMAC(b)
	}
	a, e := parse(c.Transform.Current)
	if e != nil {
		return nil, e
	}
	t := &identifierTransform{current: a, currentID: c.Transform.Current.KeyID}
	if p := c.Transform.Previous; p != nil {
		if p.KeyID == t.currentID {
			return nil, errors.New("identifiers epoch collision")
		}
		now := time.Now()
		if !p.ValidUntil.After(now) || p.ValidUntil.After(now.Add(30*24*time.Hour)) {
			return nil, errors.New("identifiers previous valid_until must be future and at most 30 days away")
		}
		t.previous, e = parse(*p)
		if e != nil {
			return nil, e
		}
		t.previousID = p.KeyID
		t.validUntil = p.ValidUntil
	}
	const probe = "identifier-transform-self-test"
	if decoded, e := t.Decode(t.Encode(probe)); e != nil || decoded != probe {
		return nil, errors.New("identifier transform self-test failed")
	}
	return t, nil
}
func (t *identifierTransform) KeyIDs() (string, string) { return t.currentID, t.previousID }
func (t *identifierTransform) Encode(id string) string {
	prefix := "mr_" + t.currentID
	return prefix + base64.RawURLEncoding.EncodeToString(t.current.Seal(nil, nil, []byte(id), []byte(prefix)))
}
func (t *identifierTransform) Decode(id string) (string, error) {
	if len(id) < 5 || !strings.HasPrefix(id, "mr_") || !strings.Contains("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", id[3:4]) {
		return "", errors.New("id-decode-malformed")
	}
	a := t.current
	if id[3:4] != t.currentID {
		if id[3:4] != t.previousID || t.previous == nil || !time.Now().Before(t.validUntil) {
			return "", errors.New("id-decode-unknown-epoch")
		}
		a = t.previous
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(id[4:])
	if e != nil || len(b) < a.Overhead() {
		return "", errors.New("id-decode-malformed")
	}
	p, e := a.Open(nil, nil, b, []byte(id[:4]))
	if e != nil {
		return "", errors.New("id-decode-auth-failed")
	}
	return string(p), nil
}
