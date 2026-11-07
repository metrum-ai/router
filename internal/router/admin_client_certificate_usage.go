// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "crypto/x509"

// adminLeafExplicitClientAuth is stricter than x509.Verify's usage rules:
// absence of EKU and anyExtendedKeyUsage are not enrollment for admin access.
// Both mTLS identity and RFC 8705 certificate possession require an explicitly
// clientAuth-enabled leaf, without anyExtendedKeyUsage, after chain verification.
func adminLeafExplicitClientAuth(leaf *x509.Certificate) bool {
	if leaf == nil {
		return false
	}
	client := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageAny {
			return false
		}
		if usage == x509.ExtKeyUsageClientAuth {
			client = true
		}
	}
	return client
}
