// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Exact concrete types intentionally, not a marker interface that could be
// promoted by embedding into a wrapper with additional security obligations.
// Only pure SAN identity mapping is redundant after verified token possession
// or declared certificate-emergency proof plus durable Use acknowledgement.
// Emergency/auditing/other wrappers must still run and acknowledge their state.
func adminPureCertificateIdentity(mechanism Authenticator) bool {
	switch a := mechanism.(type) {
	case *adminMTLSAuthenticator:
		return a != nil
	case *adminMTLSIssuerDispatcher:
		return a != nil
	case *adminMTLSTrustAuthenticator:
		// Pure grouped identity selection only. The successful possession
		// mechanism verified the same current union; no SAN identity is needed.
		return a != nil
	default:
		return false
	}
}
