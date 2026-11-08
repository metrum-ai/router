// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"strings"
	"time"
)

// Authorizer is the sole administrative decision seam. Authentication supplies
// Principal; callers supply the trusted target domain and declared operation.
// A nil error permits the operation; every error denies it.
type Authorizer interface {
	Authorize(context.Context, Principal, string, string, string) error
}

var (
	ErrAdminForbidden           = &AdminAuthorizationError{status: 403, code: "admin-forbidden"}
	ErrAuthorizationUnavailable = &AdminAuthorizationError{status: 503, code: "authorization-unavailable"}
)

const (
	internalAuthorizationSubjectPrefix = "router-internal:"
	emergencyAuthorizationSubject      = internalAuthorizationSubjectPrefix + "emergency"
	emergencyAuthorizationRole         = internalAuthorizationSubjectPrefix + "security-admin"
)

// authorizationDecisionAudit is a consumer hook for the audit owner. Install
// before serving requests; the callback must return only after the required
// durable acknowledgement. No credential material is present in Principal.
// Emergency operations fail closed until a sink is installed. Other legacy
// operations retain their existing handler audit path until outbox integration.
type authorizationDecisionAudit func(context.Context, Principal, string, string, string, error) error

type authorizationGuardKey struct{}
type authorizationRevisionKey struct{}
type pinnedAuthorizationRevision struct {
	owner    *Service
	snapshot *authorizer
}
type legacyAuthorizationKey struct{}
type legacyAuthorizationAdapter struct{ source, mode string }

// PinAuthorization pins one immutable revision for all checks in a request.
// A request beginning after a swap observes revocation; in-flight checks remain
// coherent. The pin cannot be consumed by a different Service instance.
func (s *Service) PinAuthorization(ctx context.Context) context.Context {
	if s == nil || ctx == nil {
		return ctx
	}
	if captured, ok := ctx.Value(adminMachineGenerationContextKey{}).(capturedAdminMachineGeneration); ok && captured.owner == s && captured.generation != nil {
		// Machine credentials and grants were captured together. An earlier
		// generic pin must not substitute the independently published serving
		// snapshot, and later mount/handler/guard pins cannot change it.
		if pin, ok := ctx.Value(authorizationRevisionKey{}).(pinnedAuthorizationRevision); ok && pin.owner == s && pin.snapshot == captured.generation.authorizer {
			return ctx
		}
		return context.WithValue(ctx, authorizationRevisionKey{}, pinnedAuthorizationRevision{s, captured.generation.authorizer})
	}
	if pin, ok := ctx.Value(authorizationRevisionKey{}).(pinnedAuthorizationRevision); ok && pin.owner == s {
		return ctx
	}
	return context.WithValue(ctx, authorizationRevisionKey{}, pinnedAuthorizationRevision{s, s.currentAuthorizer()})
}

func (s *Service) authorizeLegacy(subject adminAuthSubject, object, action, mode string) bool {
	ctx := subject.authContext
	if ctx == nil {
		ctx = context.Background()
	}
	if principal, ok := PrincipalFrom(ctx); ok {
		return s.Authorize(ctx, principal, subject.domain, object, action) == nil
	}
	ctx = context.WithValue(ctx, legacyAuthorizationKey{}, legacyAuthorizationAdapter{subject.source, mode})
	return s.Authorize(ctx, Principal{Subject: subject.subject, CredentialID: subject.credentialID, Issuer: subject.issuer}, subject.domain, object, action) == nil
}

// Authorize evaluates even emergency authority inside Casbin. Emergency
// activation/expiry belongs exclusively to the authenticator; no request field
// or editable policy can select the internal identity in place of Kind.
func (s *Service) Authorize(ctx context.Context, principal Principal, domain, object, action string) (decision error) {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrAuthorizationUnavailable
	}
	legacy, isLegacy := ctx.Value(legacyAuthorizationKey{}).(legacyAuthorizationAdapter)
	authenticated, present := PrincipalFrom(ctx)
	if (present && authenticated != principal) || (!isLegacy && !present) {
		return ErrAdminForbidden
	}
	defer func() {
		if isLegacy {
			return
		} // existing serving handlers retain their audit path
		if s.authorizationAudit != nil {
			if err := s.authorizationAudit(ctx, principal, domain, object, action, decision); err != nil {
				decision = ErrAuthorizationUnavailable
			}
		} else if principal.Kind == "emergency" {
			decision = ErrAuthorizationUnavailable
		}
	}()
	if strings.HasPrefix(principal.Subject, internalAuthorizationSubjectPrefix) || strings.TrimSpace(principal.Subject) == "" || principal.Subject == securityAdministratorRole {
		return ErrAdminForbidden
	}
	internalObject := strings.HasPrefix(object, internalAuthorizationSubjectPrefix)
	guardQuery := internalObject && ctx.Value(authorizationGuardKey{}) == true
	if guardQuery && object != securityAdministratorCapability {
		return ErrAdminForbidden
	}
	if internalObject && !isLegacy && !guardQuery {
		return ErrAdminForbidden
	}
	if isLegacy {
		if principal.Kind != "" {
			return ErrAdminForbidden
		}
		switch legacy.mode {
		case "public", "permission", "object", "direct", "permission-raw":
		case "global":
			if legacy.source != "caller_token" || domain != AdminGlobalDomain || object != authzObjectMetrics {
				return ErrAdminForbidden
			}
		default:
			return ErrAdminForbidden
		}
	} else if !validMachinePrincipal(principal) || strings.TrimSpace(domain) == "" || strings.TrimSpace(object) == "" || strings.TrimSpace(action) == "" {
		return ErrAdminForbidden
	}
	snapshot := s.currentAuthorizer()
	if pin, ok := ctx.Value(authorizationRevisionKey{}).(pinnedAuthorizationRevision); ok && pin.owner == s {
		snapshot = pin.snapshot
	}
	if snapshot == nil || snapshot.enforcer == nil {
		return ErrAuthorizationUnavailable
	}
	subject := authorizationSubject{subject: principal.Subject, domain: domain, source: principal.Kind}
	if isLegacy {
		// Protected management queries check role membership, never a map.
		// Caller-token projection uses the trusted adapter kind and ID.
		if legacy.mode != "public" && !guardQuery {
			subject.subject = compatibilityIdentity(adminAuthSubject{subject: principal.Subject, source: legacy.source, credentialID: principal.CredentialID}, legacy.mode)
		}
	} else {
		switch principal.Kind {
		case "emergency":
			subject.subject = emergencyAuthorizationSubject
		case "adminkey":
			// Only these legacy route families consulted credential permission maps.
			// Reports, training and maintenance remain public-policy-only. New policy
			// and key management objects have no implicit compatibility permissions.
			mode := ""
			switch object {
			case "admin:config", "admin:shadow", "admin:auth":
				mode = "permission"
				if domain == AdminGlobalDomain {
					mode = "global"
				}
			case "admin:callers":
				mode = "object"
				if domain == AdminGlobalDomain && action == "list" {
					mode = "global"
				}
			case "metrics":
				if domain == AdminGlobalDomain {
					mode = "global"
				}
			}
			if mode != "" && principal.CredentialID != "" {
				subject.subject = compatibilityIdentity(adminAuthSubject{subject: principal.Subject, source: "admin_key", credentialID: principal.CredentialID}, mode)
			}
		}
	}
	if guardQuery {
		subject.subject = managementGuardIdentity(subject.subject)
	}
	if !snapshot.enforce(subject, object, action) {
		return ErrAdminForbidden
	}
	if !isLegacy && ctx.Value(adminPolicyMutationKey{}) == true {
		return s.checkAdminPolicyFreshness(time.Now())
	}
	return nil
}

func addEmergencyAuthorizationGrants(a interface {
	AddPolicy(...interface{}) (bool, error)
	AddGroupingPolicy(...interface{}) (bool, error)
}) error {
	if _, err := a.AddPolicy(emergencyAuthorizationRole, "*", "*", ".*"); err != nil {
		return err
	}
	for _, role := range []string{securityAdministratorRole, emergencyAuthorizationRole} {
		if _, err := a.AddPolicy(role, AdminGlobalDomain, securityAdministratorCapability, "administer"); err != nil {
			return err
		}
	}
	_, err := a.AddGroupingPolicy(emergencyAuthorizationSubject, emergencyAuthorizationRole, "*")
	return err
}
