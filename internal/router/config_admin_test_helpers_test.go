// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"

	"gopkg.in/yaml.v3"
)

func yamlUnmarshalForTest(raw string, out any) error {
	return yaml.Unmarshal([]byte(raw), out)
}

func sha256HexForTest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// configAdminTestTokenSHA256 returns the hex SHA-256 of the harness caller
// token so no credential-shaped literal appears in the source tree.
func configAdminTestTokenSHA256() string {
	sum := sha256.Sum256([]byte(configAdminTestToken))
	return hex.EncodeToString(sum[:])
}
