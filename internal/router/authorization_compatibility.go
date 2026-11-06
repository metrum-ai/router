// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/casbin/casbin/v2"
)

const compatibilitySubjectPrefix = "router-internal:compat:"

// grantMatch preserves the two distinct v4.7 grant languages: configured
// policy uses domain/object whole wildcards and anchored actions, whereas
// compatibility permission maps use literal strings, including '*' and regex
// punctuation. Internal identities cannot be authored in a policy set.
func grantMatch(args ...any) (any, error) {
	if len(args) != 7 {
		return false, fmt.Errorf("grantMatch expects 7 arguments")
	}
	var fields [7]string
	for i, arg := range args {
		value, ok := arg.(string)
		if !ok {
			return false, fmt.Errorf("grantMatch arguments must be strings")
		}
		fields[i] = value
	}
	if strings.HasPrefix(fields[6], compatibilitySubjectPrefix) {
		return fields[0] == fields[1] && fields[2] == fields[3] && fields[4] == fields[5], nil
	}
	if (fields[0] != fields[1] && fields[1] != "*") || (fields[2] != fields[3] && fields[3] != "*") {
		return false, nil
	}
	return actionMatch(fields[4], fields[5])
}

// Separate identities preserve credential-local grants even when two
// credentials intentionally share a public policy subject. The direct-only
// identity preserves the legacy Basic /admin/auth behavior.
func compatibilityIdentity(subject adminAuthSubject, mode string) string {
	if subject.credentialID == "" || (subject.source != "admin_key" && subject.source != "basic") {
		return subject.subject
	}
	encode := base64.RawURLEncoding.EncodeToString
	return compatibilitySubjectPrefix + mode + ":" + encode([]byte(subject.source)) + ":" + encode([]byte(subject.credentialID)) + ":" + encode([]byte(subject.subject))
}

// Resolve the trusted public identity inside the SAME Casbin decision, rather
// than adding a role edge (which would consume one level of the existing role
// hierarchy budget). Direct-only adapters deliberately have no policy fallback.
func compatibilityPolicySubject(args ...any) (any, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("policySubject expects one argument")
	}
	subject, ok := args[0].(string)
	if !ok {
		return "", fmt.Errorf("policySubject expects a string")
	}
	if !strings.HasPrefix(subject, compatibilitySubjectPrefix) {
		return subject, nil
	}
	parts := strings.Split(strings.TrimPrefix(subject, compatibilitySubjectPrefix), ":")
	if len(parts) != 4 || (parts[0] != "permission" && parts[0] != "object") {
		return subject, nil
	}
	public, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return subject, nil
	}
	if strings.HasPrefix(string(public), compatibilitySubjectPrefix) {
		return subject, nil
	}
	return string(public), nil
}

func addCompatibilityGrants(e *casbin.SyncedEnforcer, cfg AdminAuthConfig) error {
	add := func(subject adminAuthSubject, permissions []string) error {
		if strings.HasPrefix(subject.subject, compatibilitySubjectPrefix) {
			return fmt.Errorf("admin subject uses a reserved identity")
		}
		combined := compatibilityIdentity(subject, "permission")
		objectIdentity := compatibilityIdentity(subject, "object")
		direct := compatibilityIdentity(subject, "direct")
		for _, permission := range permissions {
			object, action, ok := adminPermissionObjectAction(permission)
			if !ok {
				continue
			}
			idx := strings.LastIndex(permission, ":")
			if idx > 0 {
				if _, err := e.AddPolicy(objectIdentity, subject.domain, permission[:idx], permission[idx+1:]); err != nil {
					return err
				}
			}
			for _, identity := range []string{combined, direct} {
				if _, err := e.AddPolicy(identity, subject.domain, object, action); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, key := range cfg.APIKeys {
		if strings.TrimSpace(key.Status) == "disabled" {
			continue
		}
		subject := strings.TrimSpace(key.Subject)
		if subject == "" {
			subject = "adminkey:" + strings.TrimSpace(key.KeyID)
		}
		if err := add(adminAuthSubject{subject: subject, domain: strings.TrimSpace(key.Domain), source: "admin_key", credentialID: strings.TrimSpace(key.KeyID)}, key.Permissions); err != nil {
			return err
		}
	}
	if cfg.Basic.Enabled {
		for _, user := range cfg.Basic.Users {
			subject := strings.TrimSpace(user.Subject)
			if subject == "" {
				subject = "basic:" + strings.TrimSpace(user.Username)
			}
			if err := add(adminAuthSubject{subject: subject, domain: strings.TrimSpace(user.Domain), source: "basic", credentialID: strings.TrimSpace(user.Username)}, user.Permissions); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) authorizeCompatibility(subject adminAuthSubject, object, action, mode string) bool {
	if strings.HasPrefix(subject.subject, compatibilitySubjectPrefix) {
		return false
	}
	identity := authzSubjectForAdmin(subject)
	identity.subject = compatibilityIdentity(subject, mode)
	return s.currentAuthorizer().enforce(identity, object, action)
}
