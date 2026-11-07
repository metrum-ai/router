// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// Shared strict decoder. Callers also enforce aggregate file/byte/certificate
// limits across the complete publication, not just each individual bundle.
func decodeAdminClientCAs(data []byte) ([]*x509.Certificate, error) {
	if len(data) > maxAdminCABundleBytes {
		return nil, errors.New("admin client CA bundle oversized")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("admin client CA bundle empty")
	}
	var certs []*x509.Certificate
	for len(data) > 0 {
		const begin = "-----BEGIN CERTIFICATE-----"
		const end = "-----END CERTIFICATE-----"
		if !bytes.HasPrefix(data, []byte(begin)) {
			return nil, errors.New("admin client CA bundle malformed")
		}
		endIndex := bytes.Index(data, []byte(end))
		if endIndex < 0 {
			return nil, errors.New("admin client CA PEM malformed")
		}
		blockEnd := endIndex + len(end)
		firstBlock := data[:blockEnd]
		// pem.Decode searches past malformed blocks: never allow it to skip input.
		if bytes.Contains(firstBlock[len(begin):], []byte("-----BEGIN")) {
			return nil, errors.New("admin client CA PEM nested block")
		}
		block, trailing := pem.Decode(firstBlock)
		if block == nil || len(bytes.TrimSpace(trailing)) != 0 || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("admin client CA PEM malformed")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.BasicConstraintsValid || !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, errors.New("admin client CA certificate invalid")
		}
		certs = append(certs, cert)
		if len(certs) > maxAdminCACertificates {
			return nil, errors.New("admin client CA certificate limit exceeded")
		}
		data = bytes.TrimSpace(data[blockEnd:])
	}
	return certs, nil
}
