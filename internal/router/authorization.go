// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

const authzCasbinModel = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, "*") || r.sub == p.sub) && (r.dom == p.dom || p.dom == "*") && (r.obj == p.obj || p.obj == "*") && actionMatch(r.act, p.act)
`

const (
	authzObjectMetrics         = "metrics"
	authzObjectAdminReports    = "admin:reports"
	authzObjectSecurityReports = "admin:security_reports"
	authzObjectContentCapture  = "content:capture"
	authzActionRead            = "read"
	authzActionExport          = "export"
	authzActionDrilldown       = "drilldown"
	authzActionDelete          = "delete"
	authzActionPurge           = "purge"
	authzRoleMetricsAdmin      = "metrics_admin"
	authzRoleContentAdmin      = "content_admin"
)

type authorizationSubject struct {
	subject string
	domain  string
	source  string
}

// actionMatch anchors policy action patterns so an action like "read" cannot
// silently match a different action such as "xreadx" (casbin's regexMatch is
// unanchored). An invalid policy pattern never matches.
func actionMatch(args ...any) (any, error) {
	if len(args) != 2 {
		return false, fmt.Errorf("actionMatch expects 2 arguments, got %d", len(args))
	}
	action, ok := args[0].(string)
	if !ok {
		return false, fmt.Errorf("actionMatch action must be a string")
	}
	pattern, ok := args[1].(string)
	if !ok {
		return false, fmt.Errorf("actionMatch pattern must be a string")
	}
	matched, err := regexp.MatchString("^(?:"+pattern+")$", action)
	if err != nil {
		return false, nil
	}
	return matched, nil
}

// newAuthzEnforcer builds an enforcer with the router's custom matcher
// functions registered. Every enforcer using authzCasbinModel must be created
// through this helper.
func newAuthzEnforcer() (*casbin.Enforcer, error) {
	m, err := model.NewModelFromString(authzCasbinModel)
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, err
	}
	enforcer.AddFunction("actionMatch", actionMatch)
	return enforcer, nil
}

type authorizer struct {
	enforcer        *casbin.Enforcer
	activePolicySet string
}

func newAuthorizer(cfg AdminAuthorizationConfig, callers map[string]*callerRuntime, usage *usageStore) (*authorizer, error) {
	enforcer, err := newAuthzEnforcer()
	if err != nil {
		return nil, err
	}
	for _, caller := range callers {
		subject := authzSubjectForCaller(caller)
		if subject.subject == "" || subject.domain == "" {
			continue
		}
		if caller.cfg.MetricsAdmin {
			if _, err := enforcer.AddPolicy(authzRoleMetricsAdmin, subject.domain, authzObjectMetrics, authzActionRead); err != nil {
				return nil, err
			}
			if _, err := enforcer.AddGroupingPolicy(subject.subject, authzRoleMetricsAdmin, subject.domain); err != nil {
				return nil, err
			}
		}
		if caller.cfg.ContentAdmin {
			if _, err := enforcer.AddPolicy(authzRoleContentAdmin, subject.domain, authzObjectContentCapture, authzActionDelete+"|"+authzActionPurge); err != nil {
				return nil, err
			}
			if _, err := enforcer.AddGroupingPolicy(subject.subject, authzRoleContentAdmin, subject.domain); err != nil {
				return nil, err
			}
		}
	}
	activePolicySet := ""
	if cfg.Enabled {
		policyLines, policySetID, err := authzPolicyLines(cfg, usage)
		if err != nil {
			return nil, err
		}
		activePolicySet = policySetID
		for _, raw := range policyLines {
			fields, err := parseCasbinPolicyLine(raw)
			if err != nil {
				return nil, err
			}
			if err := validateCasbinPolicyFields(fields, "authorization policy"); err != nil {
				return nil, err
			}
			switch fields[0] {
			case "p":
				if len(fields) != 5 {
					return nil, fmt.Errorf("policy line %q must have 5 fields", fields[0])
				}
				if _, err := enforcer.AddPolicy(fields[1], fields[2], fields[3], fields[4]); err != nil {
					return nil, err
				}
			case "g":
				if len(fields) != 4 {
					return nil, fmt.Errorf("policy line %q must have 4 fields", fields[0])
				}
				if _, err := enforcer.AddGroupingPolicy(fields[1], fields[2], fields[3]); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unsupported policy line %q", fields[0])
			}
		}
	}
	return &authorizer{enforcer: enforcer, activePolicySet: activePolicySet}, nil
}

func authzPolicyLines(cfg AdminAuthorizationConfig, usage *usageStore) ([]string, string, error) {
	if strings.EqualFold(strings.TrimSpace(cfg.Source), "db") {
		if usage == nil {
			return nil, "", fmt.Errorf("authorization source db requires usage_db")
		}
		lines, policySetID, err := usage.LoadActiveAuthzPolicyLines()
		if err != nil {
			return nil, "", err
		}
		return lines, policySetID, nil
	}
	lines := append([]string(nil), cfg.Policy...)
	if strings.TrimSpace(cfg.PolicyFile) == "" {
		return lines, "", nil
	}
	raw, err := os.ReadFile(strings.TrimSpace(cfg.PolicyFile))
	if err != nil {
		return nil, "", fmt.Errorf("read authorization policy_file: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, "", nil
}

func parseCasbinPolicyLine(raw string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(raw))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return nil, err
	}
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields, nil
}

func validateCasbinPolicyFields(fields []string, label string) error {
	if len(fields) == 0 {
		return fmt.Errorf("%s is empty", label)
	}
	switch fields[0] {
	case "p":
		if len(fields) != 5 {
			return fmt.Errorf("%s p lines require subject, domain, object, action", label)
		}
		// The matcher compares objects by exact equality (or the single
		// wildcard "*"). Partial wildcards such as "admin:*" would never match
		// and would hide a policy authoring mistake, so reject them here.
		if strings.Contains(fields[3], "*") && fields[3] != "*" {
			return fmt.Errorf("%s object %q uses a partial wildcard; only exact object names or the single wildcard %q are supported", label, fields[3], "*")
		}
		if _, err := regexp.Compile("^(?:" + fields[4] + ")$"); err != nil {
			return fmt.Errorf("%s action %q is not a valid anchored action pattern: %w", label, fields[4], err)
		}
	case "g":
		if len(fields) != 4 {
			return fmt.Errorf("%s g lines require subject, role, domain", label)
		}
	default:
		return fmt.Errorf("%s must start with p or g", label)
	}
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("%s contains an empty field", label)
		}
		if strings.ContainsAny(field, "\r\n") {
			return fmt.Errorf("%s contains unsupported characters", label)
		}
		if authzPolicyFieldLooksSensitive(field) {
			return fmt.Errorf("%s contains unsafe field", label)
		}
	}
	return nil
}

func authzPolicyFieldLooksSensitive(field string) bool {
	lower := strings.ToLower(strings.TrimSpace(field))
	for _, marker := range []string{"authorization", "bearer ", "token_hash", "api_key", "provider_key", "password", "secret", "sha256", "sk-", "ghp_"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validateCasbinPolicyLines(lines []string) error {
	if len(lines) == 0 {
		return fmt.Errorf("authorization policy set has no policy rules or role links")
	}
	enforcer, err := newAuthzEnforcer()
	if err != nil {
		return err
	}
	for i, raw := range lines {
		fields, err := parseCasbinPolicyLine(raw)
		if err != nil {
			return fmt.Errorf("policy line %d is invalid: %w", i+1, err)
		}
		if err := validateCasbinPolicyFields(fields, fmt.Sprintf("policy line %d", i+1)); err != nil {
			return err
		}
		switch fields[0] {
		case "p":
			if _, err := enforcer.AddPolicy(fields[1], fields[2], fields[3], fields[4]); err != nil {
				return fmt.Errorf("policy line %d rejected by casbin: %w", i+1, err)
			}
		case "g":
			if _, err := enforcer.AddGroupingPolicy(fields[1], fields[2], fields[3]); err != nil {
				return fmt.Errorf("policy line %d rejected by casbin: %w", i+1, err)
			}
		}
	}
	return nil
}

func formatCasbinPolicyLine(fields ...string) (string, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write(fields); err != nil {
		return "", err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func (a *authorizer) enforce(subject authorizationSubject, object, action string) bool {
	if a == nil || a.enforcer == nil {
		return false
	}
	ok, err := a.enforcer.Enforce(subject.subject, subject.domain, object, action)
	return err == nil && ok
}

func (a *authorizer) enforceInDomain(subject authorizationSubject, domain, object, action string) bool {
	subject.domain = strings.TrimSpace(domain)
	return a.enforce(subject, object, action)
}

func authzSubjectForBasic(subject adminBasicRuntime) authorizationSubject {
	return authorizationSubject{subject: subject.subject, domain: subject.domain, source: "basic"}
}

func authzSubjectForAdmin(subject adminAuthSubject) authorizationSubject {
	return authorizationSubject{subject: subject.subject, domain: subject.domain, source: subject.source}
}

func authzSubjectForCaller(caller *callerRuntime) authorizationSubject {
	if caller == nil {
		return authorizationSubject{}
	}
	return authorizationSubject{
		subject: "caller:" + strings.TrimSpace(caller.cfg.ID),
		domain:  authzDomainForCaller(caller),
		source:  "caller_token",
	}
}

func authzDomainForCaller(caller *callerRuntime) string {
	project := strings.TrimSpace(caller.project)
	if project == "" {
		project = strings.TrimSpace(caller.cfg.Project)
	}
	return authzDomainForProjectEnvironment(project, caller.cfg.Environment)
}

func authzDomainForProjectEnvironment(project, environment string) string {
	project = strings.TrimSpace(project)
	env := strings.TrimSpace(environment)
	switch {
	case project != "" && env != "":
		return project + "/" + env
	case project != "":
		return project
	default:
		return "default"
	}
}
