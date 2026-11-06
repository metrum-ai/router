// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/casbin/casbin/v2"
)

var (
	ErrPrivilegeEscalation = errors.New("privilege-escalation")
	ErrLastPolicyAdmin     = errors.New("last-policy-admin")
	ErrInvalidPolicy       = errors.New("invalid-policy")
)

const securityAdministratorRole = "security-admin"
const securityAdministratorCapability = internalAuthorizationSubjectPrefix + "security-admin-capability"

// Protected queries may only use immutable exact capability rows, not ordinary
// wildcard data grants or compatibility strings. Ordinary legacy queries retain
// their old literal/public-policy semantics, even for namespace-shaped strings.
func managementRoleMatch(args ...any) (any, error) {
	if len(args) != 3 {
		return false, fmt.Errorf("managementRoleMatch expects three strings")
	}
	subject, okSubject := args[0].(string)
	role, okRole := args[1].(string)
	object, okObject := args[2].(string)
	if !okSubject || !okRole || !okObject {
		return false, fmt.Errorf("managementRoleMatch expects three strings")
	}
	if !strings.HasPrefix(subject, managementGuardSubjectPrefix) {
		return true, nil
	}
	return object == securityAdministratorCapability && (role == securityAdministratorRole || role == emergencyAuthorizationRole), nil
}

// The caller must supply authoritative current/candidate snapshots built under
// the store mutation lock, with the SAME compatibility configuration, and an
// authoritative post-mutation enabled-credential inventory. Every authored
// candidate record passes online validation, including unchanged/duplicate
// records; synthesized rows are checked separately for effective scope changes.
// It does not persist anything or substitute for a transactional store guard.
func guardPolicyMutation(ctx context.Context, actor Principal, current, candidate *Service, enabled []Principal, operation string) error {
	if ctx == nil || current == nil || candidate == nil {
		return ErrAuthorizationUnavailable
	}
	before, after := current.currentAuthorizer(), candidate.currentAuthorizer()
	if before == nil || after == nil || before.enforcer == nil || after.enforcer == nil {
		return ErrAuthorizationUnavailable
	}
	if operation != "activate" && operation != "rollback" {
		return ErrInvalidPolicy
	}
	// Freeze both inputs, including metadata/role reads, against asynchronous
	// runtime publication. Candidate checks are simulations, not real requests.
	current = &Service{authorizer: before, authorizationAudit: current.authorizationAudit}
	candidate = &Service{authorizer: after}
	// A request pin cannot authorize a write against a different locked revision.
	ctx = context.WithValue(ctx, authorizationRevisionKey{}, pinnedAuthorizationRevision{current, before})
	ctx = context.WithValue(ctx, authorizationGuardKey{}, true)
	if err := current.Authorize(ctx, actor, AdminGlobalDomain, "admin:policy", operation); err != nil {
		return err
	}
	if err := current.Authorize(ctx, actor, AdminGlobalDomain, securityAdministratorCapability, "administer"); err != nil {
		return err
	}
	for _, fields := range after.authoredPolicy {
		if err := validateCasbinPolicyMutationFields(fields, "candidate policy"); err != nil {
			return ErrInvalidPolicy
		}
	}
	oldP, err := before.enforcer.GetPolicy()
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	newP, err := after.enforcer.GetPolicy()
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	oldG, err := before.enforcer.GetGroupingPolicy()
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	newG, err := after.enforcer.GetGroupingPolicy()
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	oldPolicyRows, oldGroupRows := guardRowSet(oldP), guardRowSet(oldG)
	for _, row := range newP {
		if oldPolicyRows[guardRowKey(row)] {
			continue
		}
		if len(row) != 4 {
			return ErrInvalidPolicy
		}
		if strings.HasPrefix(row[0], compatibilitySubjectPrefix+"global:") {
			// These rows can change when public scoped policy is projected for legacy
			// credentials. They are literal global capabilities, not universal grants.
			if err := guardCeilingDecision(current.Authorize(ctx, actor, row[1], row[2], row[3])); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(row[0], internalAuthorizationSubjectPrefix) {
			return ErrAuthorizationUnavailable
		}
		if err := validateCasbinPolicyMutationFields(append([]string{"p"}, row...), "candidate policy"); err != nil {
			return ErrInvalidPolicy
		}
		if err := guardCeilingCheck(ctx, actor, current, oldP, row[1], row[2], row[3]); err != nil {
			return err
		}
	}
	for _, row := range newG {
		if oldGroupRows[guardRowKey(row)] {
			continue
		}
		if err := validateCasbinPolicyMutationFields(append([]string{"g"}, row...), "candidate role"); err != nil {
			return ErrInvalidPolicy
		}
		if err := guardRoleBundle(ctx, actor, current, after, oldP, newP, newG, row); err != nil {
			return err
		}
	}
	return guardLastNormalPolicyAdministrator(candidate, enabled)
}

// Compare opaque policy identities byte-for-byte. JSON normalizes invalid
// UTF-8 to replacement runes and can collapse distinct legacy CSV role names.
// Length prefixes also preserve embedded delimiters and field partitions.
func guardRowKey(row []string) string {
	var key strings.Builder
	for _, field := range row {
		key.WriteString(strconv.Itoa(len(field)))
		key.WriteByte(':')
		key.WriteString(field)
	}
	return key.String()
}

func guardRowSet(rows [][]string) map[string]bool {
	set := make(map[string]bool, len(rows))
	for _, row := range rows {
		set[guardRowKey(row)] = true
	}
	return set
}

func guardLiteralActions(pattern string) ([]string, bool) {
	actions := strings.Split(pattern, "|")
	for _, action := range actions {
		if action == "" || regexp.QuoteMeta(action) != action {
			return nil, false
		}
	}
	return actions, true
}

// Literal requests can reuse Authorize. Wildcard selectors require a structural
// universal-scope proof: a private '*' target is NOT a public wildcard domain.
// General regex languages are accepted only with exact existing-pattern proof.
func guardCeilingDecision(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrAuthorizationUnavailable) {
		return ErrAuthorizationUnavailable
	}
	return ErrPrivilegeEscalation
}

func guardCeilingCheck(ctx context.Context, actor Principal, current *Service, policies [][]string, domain, object, pattern string) error {
	actions, literal := guardLiteralActions(pattern)
	if literal && domain != "*" && object != "*" {
		for _, action := range actions {
			if err := guardCeilingDecision(current.Authorize(ctx, actor, domain, object, action)); err != nil {
				return err
			}
		}
		return nil
	}
	if !literal {
		if !guardPatternCovered(actor, current.currentAuthorizer(), policies, domain, object, pattern, true) {
			return ErrPrivilegeEscalation
		}
		return nil
	}
	for _, action := range actions {
		if !guardPatternCovered(actor, current.currentAuthorizer(), policies, domain, object, action, false) {
			return ErrPrivilegeEscalation
		}
	}
	return nil
}

func guardPatternCovered(actor Principal, a *authorizer, policies [][]string, domain, object, action string, exactPattern bool) bool {
	subject := actor.Subject
	if actor.Kind == "emergency" {
		subject = emergencyAuthorizationSubject
	}
	for _, row := range policies {
		if len(row) != 4 {
			continue
		}
		// Original explicit '*' caller-row map scope is truly universal in
		// the object adapter. A synthesized global identity is NOT: it remains
		// a literal '*' target. Match the credential and raw key exactly; never
		// interpret map punctuation as an action language or share across kinds.
		if actor.Kind == "adminkey" && actor.CredentialID != "" && !exactPattern && object == authzObjectAdminCallers && row[1] == AdminGlobalDomain && row[3] == "" {
			identity := compatibilityIdentity(adminAuthSubject{subject: actor.Subject, source: "admin_key", credentialID: actor.CredentialID}, "object")
			if row[0] == identity && row[2] == object+":"+action {
				return true
			}
		}
		if strings.HasPrefix(row[0], internalAuthorizationSubjectPrefix) && row[0] != emergencyAuthorizationRole {
			continue
		}
		if (row[1] != domain && row[1] != "*") || (row[2] != object && row[2] != "*") {
			continue
		}
		if domain == "*" && row[1] != "*" || object == "*" && row[2] != "*" {
			continue
		}
		if !guardRoleApplies(a.enforcer, subject, row[0], domain) {
			continue
		}
		if exactPattern {
			if row[3] == action {
				return true
			}
			continue
		}
		match, err := actionMatch(action, row[3])
		if err == nil && match == true {
			return true
		}
	}
	return false
}

// HasLink uses the same Casbin role manager and hierarchy limit as the matcher.
// This is a subset-proof input, not another HTTP authorization decision point.
func guardRoleApplies(e *casbin.SyncedEnforcer, subject, role, domain string) bool {
	if subject == role {
		return true
	}
	local, err := e.GetRoleManager().HasLink(subject, role, domain)
	if err != nil {
		return false
	}
	global, err := e.GetRoleManager().HasLink(subject, role, "*")
	return err == nil && (local || global)
}

func guardRoleBundle(ctx context.Context, actor Principal, current *Service, candidate *authorizer, oldP, policies, groups [][]string, link []string) error {
	if len(link) != 3 {
		return ErrInvalidPolicy
	}
	// A fresh subject inheriting ONLY this role avoids accidentally counting the
	// recipient's unrelated existing grants, while preserving cycles and the
	// model's separate global/scoped graph semantics. Never mutate the candidate.
	probe := ""
	for i := 0; ; i++ {
		probe = fmt.Sprintf("policy-guard-probe:%d", i)
		collision := false
		for _, row := range policies {
			if row[0] == probe {
				collision = true
			}
		}
		for _, row := range groups {
			if row[0] == probe || row[1] == probe {
				collision = true
			}
		}
		if !collision {
			break
		}
	}
	clone, err := newAuthzEnforcer()
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	for _, row := range policies {
		if _, err := clone.AddPolicy(row); err != nil {
			return ErrAuthorizationUnavailable
		}
	}
	for _, row := range groups {
		if _, err := clone.AddGroupingPolicy(row); err != nil {
			return ErrAuthorizationUnavailable
		}
	}
	if _, err := clone.AddGroupingPolicy(probe, link[1], link[2]); err != nil {
		return ErrAuthorizationUnavailable
	}
	domains := map[string]bool{link[2]: true}
	if link[2] == "*" {
		for _, row := range policies {
			domains[row[1]] = true
		}
		for _, row := range groups {
			domains[row[2]] = true
		}
	}
	for domain := range domains {
		for _, row := range policies {
			if strings.HasPrefix(row[0], internalAuthorizationSubjectPrefix) || strings.HasPrefix(row[2], internalAuthorizationSubjectPrefix) {
				continue
			}
			if row[1] != domain && row[1] != "*" {
				continue
			}
			if !guardRoleApplies(clone, probe, row[0], domain) {
				continue
			}
			if err := guardCeilingCheck(ctx, actor, current, oldP, domain, row[2], row[3]); err != nil {
				return err
			}
		}
	}
	return nil
}

// The inventory describes ENABLED normal credentials after this transaction,
// not arbitrary p/g subjects or the emergency principal. Simulation deliberately
// does not inherit the requesting actor's PrincipalFrom or request revision pin.
func guardLastNormalPolicyAdministrator(candidate *Service, enabled []Principal) error {
	if candidate == nil {
		return ErrAuthorizationUnavailable
	}
	snapshot := candidate.currentAuthorizer()
	if snapshot == nil || snapshot.enforcer == nil {
		return ErrAuthorizationUnavailable
	}
	candidate = &Service{authorizer: snapshot} // no decision audit for simulated principals
	for _, principal := range enabled {
		if principal.Kind == "emergency" || !validMachinePrincipal(principal) {
			continue
		}
		ctx := context.WithValue(context.Background(), authorizationGuardKey{}, true)
		ctx = candidate.PinAuthorization(ctx)
		if candidate.Authorize(ctx, principal, AdminGlobalDomain, securityAdministratorCapability, "administer") == nil &&
			candidate.Authorize(ctx, principal, AdminGlobalDomain, "admin:policy", "write") == nil &&
			candidate.Authorize(ctx, principal, AdminGlobalDomain, "admin:policy", "activate") == nil {
			return nil
		}
	}
	return ErrLastPolicyAdmin
}
