// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Production routerctl entrypoint for the offline emergency controller. It is a
// local command surface: enrollment and database access are trusted local
// dependencies, never an admin HTTP route, and no command accepts a URL or token.

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"gorm.io/gorm"
)

// RouterctlEmergencyCommand returns a runner bound to the control-plane database
// and the trusted offline enrollment in cfg. Activation/deactivation events use
// #869's transaction-bound credential-event write, so a failure rolls the
// lifecycle mutation back.
func RouterctlEmergencyCommand(db *gorm.DB, cfg *Config, replicas int) (func(context.Context, []string, io.Writer) error, error) {
	if db == nil || cfg == nil || replicas < 1 {
		return nil, errors.New("emergency command requires the control-plane database and configuration")
	}
	store, err := newAdminEmergencyActivationStore(db, replicas, time.Now, func(reason string) string {
		return adminRedactThenTrim(reason, 512)
	})
	if err != nil {
		return nil, err
	}
	credentials := map[string]adminEmergencyCredential{}
	for _, descriptor := range cfg.Admin.Authentication.Emergency.Credentials {
		issuedAt, err := time.Parse(time.RFC3339Nano, descriptor.IssuedAt)
		if err != nil {
			return nil, errAdminEmergencyInvalid
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, descriptor.ExpiresAt)
		if err != nil {
			return nil, errAdminEmergencyInvalid
		}
		expiresAt = expiresAt.UTC()
		if !expiresAt.After(time.Now()) {
			// Expired enrollment is never activatable; skip it rather than
			// widening the accepted set.
			continue
		}
		var binding string
		switch descriptor.Kind {
		case "api_key":
			value, ok := os.LookupEnv(descriptor.KeyHashEnv)
			if !ok || value == "" {
				return nil, errors.New("emergency verifier reference unavailable")
			}
			verifier, err := parseCallerTokenHash(value)
			if err != nil {
				return nil, errAdminEmergencyInvalid
			}
			binding = verifier.String()
		case "certificate":
			binding = descriptor.CertificateSHA256
		default:
			return nil, errAdminEmergencyInvalid
		}
		digest, err := adminEmergencyCredentialBinding(descriptor, binding, issuedAt, expiresAt)
		if err != nil {
			return nil, err
		}
		credentials[descriptor.ID] = adminEmergencyCredential{
			ID: descriptor.ID, Subject: descriptor.Subject, Issuer: descriptor.Issuer,
			Fingerprint: digest, ExpiresAt: expiresAt,
		}
	}
	activate := func(tx *gorm.DB, activation adminEmergencyActivation) error {
		return insertAdminCredentialEventTx(tx, AdminCredentialEvent{
			At: time.Now().UTC(), Kind: "emergency", Lifecycle: "activate",
			CredentialID: activation.CredentialID, Subject: adminRedactThenTrim(activation.Subject, 1024),
			Issuer: adminRedactThenTrim(activation.Issuer, 2048), Outcome: "success",
		})
	}
	deactivate := func(tx *gorm.DB, deactivation adminEmergencyDeactivation) error {
		return insertAdminCredentialEventTx(tx, AdminCredentialEvent{
			At: time.Now().UTC(), Kind: "emergency", Lifecycle: "deactivate",
			CredentialID: deactivation.Activation.CredentialID, Outcome: "success",
		})
	}
	command, err := newAdminEmergencyOfflineCommand(store, func(id string) (adminEmergencyCredential, bool) {
		credential, ok := credentials[id]
		return credential, ok
	}, activate, deactivate)
	if err != nil {
		return nil, err
	}
	return command.Run, nil
}

// adminEmergencyCredentialBinding re-derives the protected identity/pin/time
// binding so the offline command only ever activates an unchanged enrollment.
func adminEmergencyCredentialBinding(descriptor AdminMachineEmergencyCredentialConfig, binding string, issuedAt, expiresAt time.Time) (string, error) {
	switch descriptor.Kind {
	case "api_key":
		verifier, err := parseCallerTokenHash(binding)
		if err != nil {
			return "", errAdminEmergencyInvalid
		}
		verifierInstance, err := newAdminEmergencyKeyVerifier(adminEmergencyKeyVerifierConfig{
			id: descriptor.ID, subject: descriptor.Subject, issuer: descriptor.Issuer,
			tokenHash: binding, issuedAt: issuedAt, expiresAt: expiresAt,
		}, time.Now)
		if err != nil {
			return "", err
		}
		_ = verifier
		return verifierInstance.bindingDigest, nil
	case "certificate":
		catalog, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{{
			id: descriptor.ID, subject: descriptor.Subject, issuer: descriptor.Issuer,
			thumbprint: descriptor.CertificateSHA256, issuedAt: issuedAt, expiresAt: expiresAt,
		}}, nil, time.Now)
		if err != nil {
			return "", err
		}
		enrollment := catalog.byID[descriptor.ID]
		if enrollment == nil {
			return "", errAdminEmergencyInvalid
		}
		return enrollment.credential.Fingerprint, nil
	}
	return "", errAdminEmergencyInvalid
}
