// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/csv"
	"fmt"
	"regexp"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

// Frozen from v4.7.0 (2a95b18f). Do not use the production model/compiler:
// changing its matcher or compatibility synthesis must not change the oracle.
const v47FixtureModel = `
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

func v47FixtureAuthorizer(cfg AdminAuthorizationConfig, callers map[string]*callerRuntime) (*authorizer, error) {
	m, err := model.NewModelFromString(v47FixtureModel)
	if err != nil {
		return nil, err
	}
	e, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		return nil, err
	}
	e.AddFunction("actionMatch", func(args ...any) (any, error) {
		if len(args) != 2 {
			return false, nil
		}
		action, ok := args[0].(string)
		if !ok {
			return false, nil
		}
		pattern, ok := args[1].(string)
		if !ok {
			return false, nil
		}
		matched, err := regexp.MatchString("^(?:"+pattern+")$", action)
		if err != nil {
			return false, nil
		}
		return matched, nil
	})
	for _, caller := range callers {
		domain := authzDomainForCaller(caller)
		subject := "caller:" + strings.TrimSpace(caller.cfg.ID)
		for _, grant := range []struct {
			enabled              bool
			role, object, action string
		}{
			{caller.cfg.MetricsAdmin, "metrics_admin", "metrics", "read"},
			{caller.cfg.ContentAdmin, "content_admin", "content:capture", "delete|purge"},
		} {
			if !grant.enabled {
				continue
			}
			if _, err := e.AddPolicy(grant.role, domain, grant.object, grant.action); err != nil {
				return nil, err
			}
			if _, err := e.AddGroupingPolicy(subject, grant.role, domain); err != nil {
				return nil, err
			}
		}
	}
	if cfg.Enabled {
		if cfg.Source == "db" || cfg.PolicyFile != "" {
			return nil, fmt.Errorf("fixture requires explicit file/DB policy oracle input")
		}
		for _, line := range cfg.Policy {
			reader := csv.NewReader(strings.NewReader(line))
			reader.TrimLeadingSpace = true
			reader.FieldsPerRecord = -1
			fields, err := reader.Read()
			if err != nil {
				return nil, err
			}
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			if len(fields) == 5 && fields[0] == "p" {
				if _, err := e.AddPolicy(fields[1], fields[2], fields[3], fields[4]); err != nil {
					return nil, err
				}
			} else if len(fields) == 4 && fields[0] == "g" {
				if _, err := e.AddGroupingPolicy(fields[1], fields[2], fields[3]); err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("invalid fixture policy")
			}
		}
	}
	return &authorizer{enforcer: e}, nil
}
