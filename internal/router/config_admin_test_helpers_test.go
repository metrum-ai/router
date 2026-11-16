// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func sha256HexForTest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func yamlUnmarshalForTest(raw string, out any) error {
	return yaml.Unmarshal([]byte(raw), out)
}

// configAdminTestTokenHash returns a deterministic salted verifier for the
// harness caller token.
func configAdminTestTokenHash() string {
	verifier, err := HashCallerToken(configAdminTestToken, bytes.NewReader(make([]byte, callerTokenHashSaltBytes)), callerTokenHashMinIterations)
	if err != nil {
		panic(err)
	}
	return verifier
}

func mustCallerTokenHashForTest(t testing.TB, token string) string {
	t.Helper()
	verifier, err := HashCallerToken(token, nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func mustCallerTokenIDAndHashForTest(t testing.TB, token string) (string, string) {
	t.Helper()
	separator := strings.LastIndexByte(token, '_')
	if separator <= 0 || separator == len(token)-1 {
		t.Fatalf("test caller token %q must have a token-id prefix and secret suffix", token)
	}
	return token[:separator], mustCallerTokenHashForTest(t, token)
}
