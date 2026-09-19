// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	siv "github.com/secure-io/siv-go"
	"gopkg.in/yaml.v3"
)

const identifierEpochAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

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
	current, previous         cipher.AEAD
	currentID, previousID     string
	currentEpoch, previousEpoch string
	validUntil                time.Time
}

func (c IdentifierConfig) passthrough() bool { return c.Mode == "passthrough" }

// epochFromKeyID maps a loggable key_id onto the single base62 wire epoch char.
func epochFromKeyID(keyID string) (string, error) {
	id := strings.TrimSpace(keyID)
	if id == "" {
		return "", errors.New("identifiers key_id is required")
	}
	for _, r := range id {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return "", errors.New("identifiers key_id must be a printable non-empty string safe to log")
		}
	}
	sum := sha256.Sum256([]byte(id))
	return string(identifierEpochAlphabet[int(sum[0])%len(identifierEpochAlphabet)]), nil
}

func newIdentifierTransform(c IdentifierConfig) (IdentifierTransform, error) {
	if c.passthrough() {
		return nil, nil
	}
	if c.Mode != "" && c.Mode != "rewrite" {
		return nil, errors.New("identifiers.mode must be rewrite or passthrough")
	}
	parse := func(k IdentifierKeyConfig) (cipher.AEAD, string, string, error) {
		epoch, e := epochFromKeyID(k.KeyID)
		if e != nil {
			return nil, "", "", e
		}
		raw := strings.TrimSpace(k.Key)
		b, e := hex.DecodeString(raw)
		if e != nil || len(b) != 64 {
			b, e = base64.StdEncoding.DecodeString(raw)
		}
		if e != nil || len(b) != 64 {
			return nil, "", "", errors.New("identifiers key is required and must be 64-byte hex or base64")
		}
		aead, e := siv.NewCMAC(b)
		if e != nil {
			return nil, "", "", e
		}
		return aead, strings.TrimSpace(k.KeyID), epoch, nil
	}
	a, keyID, epoch, e := parse(c.Transform.Current)
	if e != nil {
		return nil, e
	}
	t := &identifierTransform{current: a, currentID: keyID, currentEpoch: epoch}
	if p := c.Transform.Previous; p != nil {
		now := time.Now()
		if !p.ValidUntil.After(now) || p.ValidUntil.After(now.Add(30*24*time.Hour)) {
			return nil, errors.New("identifiers previous valid_until must be future and at most 30 days away")
		}
		prevAEAD, prevID, prevEpoch, e := parse(*p)
		if e != nil {
			return nil, e
		}
		if prevID == t.currentID || prevEpoch == t.currentEpoch {
			return nil, errors.New("identifiers epoch collision")
		}
		t.previous = prevAEAD
		t.previousID = prevID
		t.previousEpoch = prevEpoch
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
	prefix := "mr_" + t.currentEpoch
	return prefix + base64.RawURLEncoding.EncodeToString(t.current.Seal(nil, nil, []byte(id), []byte(prefix)))
}
func (t *identifierTransform) Decode(id string) (string, error) {
	if len(id) < 5 || !strings.HasPrefix(id, "mr_") || !strings.Contains(identifierEpochAlphabet, id[3:4]) {
		return "", errors.New("id-decode-malformed")
	}
	a := t.current
	if id[3:4] != t.currentEpoch {
		if id[3:4] != t.previousEpoch || t.previous == nil || !time.Now().Before(t.validUntil) {
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
