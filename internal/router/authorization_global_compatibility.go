// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"strings"
)

// Global projection is action-specific. These legacy handlers operated on
// plane-global resources but looked up a credential's configured grant domain.
// Keep the legacy grants unchanged and add a separate private identity for the
// one AdminGlobalDomain target. Internal grants treat '*' as a literal domain,
// never as permission in every project/environment.
func addGlobalCompatibilityGrants(snapshot *authorizer, callers map[string]*callerRuntime, configs ...AdminAuthConfig) error {
	evaluator := &Service{authorizer: snapshot}
	add := func(subject adminAuthSubject, object, action string) error {
		identity := compatibilityIdentity(subject, "global")
		if !strings.HasPrefix(identity, compatibilitySubjectPrefix+"global:") {
			return fmt.Errorf("global compatibility requires a credential identity")
		}
		_, err := snapshot.enforcer.AddPolicy(identity, AdminGlobalDomain, object, action)
		return err
	}
	for _, cfg := range configs {
		for _, key := range cfg.APIKeys {
			if strings.TrimSpace(key.Status) == "disabled" {
				continue
			}
			id := strings.TrimSpace(key.KeyID)
			public := strings.TrimSpace(key.Subject)
			if public == "" {
				public = "adminkey:" + id
			}
			subject := adminAuthSubject{subject: public, domain: strings.TrimSpace(key.Domain), source: "admin_key", credentialID: id}
			for _, permission := range []string{adminPermissionConfigRead, adminPermissionConfigWrite, adminPermissionConfigActivate, adminPermissionConfigForce, adminPermissionShadowWrite, adminPermissionAuthRead, "admin:callers:list", "metrics:read"} {
				object, action, _ := adminPermissionObjectAction(permission)
				mode := "permission"
				if object == "admin:callers" {
					mode = "object"
				}
				// Use the same sole decision point as a legacy request, not a second
				// implementation of policy inheritance, regex or direct-map semantics.
				if evaluator.authorizeCompatibility(subject, object, action, mode) {
					if err := add(subject, object, action); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, caller := range callers {
		if caller == nil || strings.TrimSpace(caller.cfg.ID) == "" {
			continue
		}
		if evaluator.authorizeCaller(caller, authzObjectMetrics, authzActionRead) {
			subject := authzSubjectForCaller(caller)
			if err := add(adminAuthSubject{subject: subject.subject, domain: subject.domain, source: "caller_token", credentialID: strings.TrimSpace(caller.cfg.ID)}, authzObjectMetrics, authzActionRead); err != nil {
				return err
			}
		}
	}
	return nil
}
