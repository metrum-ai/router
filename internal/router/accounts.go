// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"math"
	"strings"
)

const (
	accountStatusActive    = "active"
	accountStatusDisabled  = "disabled"
	accountStatusSuspended = "suspended"
	accountStatusRemoved   = "removed"
	accountStatusArchived  = "archived"
	keyStatusExpired       = "expired"
	keyStatusRotated       = "rotated"
)

type accountDirectory struct {
	users       map[string]UserConfig
	projects    map[string]ProjectConfig
	memberships map[string]ProjectMembershipConfig
}

func (c *Config) normalizeAccountsForValidation() accountDirectory {
	dir := accountDirectory{
		users:       map[string]UserConfig{},
		projects:    map[string]ProjectConfig{},
		memberships: map[string]ProjectMembershipConfig{},
	}
	for _, user := range c.Users {
		id := normalizeAccountID(user.ID)
		if id == "" {
			continue
		}
		user.ID = id
		user.Status = normalizeStatusDefault(user.Status)
		user.Type = normalizeUserTypeDefault(user.Type)
		dir.users[id] = user
	}
	for _, project := range c.Projects {
		id := normalizeAccountID(project.ID)
		if id == "" {
			continue
		}
		project.ID = id
		project.Status = normalizeStatusDefault(project.Status)
		dir.projects[id] = project
	}
	for _, membership := range c.ProjectMemberships {
		userID := normalizeAccountID(membership.UserID)
		projectID := normalizeAccountID(membership.Project)
		if userID == "" || projectID == "" {
			continue
		}
		membership.UserID = userID
		membership.Project = projectID
		membership.Status = normalizeStatusDefault(membership.Status)
		membership.Role = normalizeRoleDefault(membership.Role)
		dir.memberships[membershipKey(userID, projectID)] = membership
	}
	return dir
}

func (c *Config) validateAccounts() (accountDirectory, error) {
	dir := accountDirectory{
		users:       map[string]UserConfig{},
		projects:    map[string]ProjectConfig{},
		memberships: map[string]ProjectMembershipConfig{},
	}
	seenUserRaw := map[string]string{}
	for _, user := range c.Users {
		id := normalizeAccountID(user.ID)
		if id == "" {
			return dir, fmt.Errorf("user missing id")
		}
		if previous, ok := seenUserRaw[id]; ok {
			return dir, fmt.Errorf("duplicate user id %q conflicts with %q after normalization", user.ID, previous)
		}
		status := normalizeStatusDefault(user.Status)
		if !validAccountStatus(status) {
			return dir, fmt.Errorf("user %s has invalid status %q", id, user.Status)
		}
		userType := normalizeUserTypeDefault(user.Type)
		if userType != "human" && userType != "service_account" {
			return dir, fmt.Errorf("user %s has invalid type %q", id, user.Type)
		}
		user.ID = id
		user.Status = status
		user.Type = userType
		dir.users[id] = user
		seenUserRaw[id] = user.ID
	}

	seenProjectRaw := map[string]string{}
	for _, project := range c.Projects {
		id := normalizeAccountID(project.ID)
		if id == "" {
			return dir, fmt.Errorf("project missing id")
		}
		if previous, ok := seenProjectRaw[id]; ok {
			return dir, fmt.Errorf("duplicate project id %q conflicts with %q after normalization", project.ID, previous)
		}
		status := normalizeStatusDefault(project.Status)
		if !validAccountStatus(status) {
			return dir, fmt.Errorf("project %s has invalid status %q", id, project.Status)
		}
		project.ID = id
		project.Status = status
		budget := project.Budget
		if budget.DailyUSD < 0 || budget.MonthlyUSD < 0 || math.IsNaN(budget.DailyUSD) || math.IsInf(budget.DailyUSD, 0) || math.IsNaN(budget.MonthlyUSD) || math.IsInf(budget.MonthlyUSD, 0) {
			return dir, fmt.Errorf("project %s has invalid budget ceiling", id)
		}
		seenThreshold := map[int]bool{}
		for _, threshold := range budget.AlertThresholds {
			if threshold < 1 || threshold > 100 || seenThreshold[threshold] {
				return dir, fmt.Errorf("project %s has invalid or duplicate budget alert threshold %d", id, threshold)
			}
			seenThreshold[threshold] = true
		}
		seenEnvironment := map[string]bool{}
		for _, environment := range project.Environments {
			environmentID := normalizeAccountID(environment.ID)
			if environmentID == "" || seenEnvironment[environmentID] {
				return dir, fmt.Errorf("project %s has missing or duplicate environment id %q", id, environment.ID)
			}
			if !validAccountStatus(normalizeStatusDefault(environment.Status)) {
				return dir, fmt.Errorf("project %s environment %s has invalid status %q", id, environmentID, environment.Status)
			}
			seenEnvironment[environmentID] = true
		}
		dir.projects[id] = project
		seenProjectRaw[id] = project.ID
	}

	seenMembershipRaw := map[string]string{}
	for _, membership := range c.ProjectMemberships {
		userID := normalizeAccountID(membership.UserID)
		projectID := normalizeAccountID(membership.Project)
		if userID == "" || projectID == "" {
			return dir, fmt.Errorf("project membership missing user_id or project")
		}
		if _, ok := dir.users[userID]; !ok {
			return dir, fmt.Errorf("project membership references unknown user %s", userID)
		}
		if _, ok := dir.projects[projectID]; !ok {
			return dir, fmt.Errorf("project membership references unknown project %s", projectID)
		}
		status := normalizeStatusDefault(membership.Status)
		if !validAccountStatus(status) {
			return dir, fmt.Errorf("project membership %s/%s has invalid status %q", userID, projectID, membership.Status)
		}
		role := normalizeRoleDefault(membership.Role)
		environment := normalizeAccountID(membership.Environment)
		if environment != "" {
			project := dir.projects[projectID]
			if len(project.Environments) == 0 || !projectHasEnvironment(project, environment) {
				return dir, fmt.Errorf("project membership references unknown environment %s/%s", projectID, environment)
			}
		}
		key := membershipKey(userID, projectID, environment)
		raw := strings.TrimSpace(membership.UserID) + "/" + strings.TrimSpace(membership.Project) + "/" + strings.TrimSpace(membership.Environment)
		if previous, ok := seenMembershipRaw[key]; ok {
			return dir, fmt.Errorf("duplicate project membership %q conflicts with %q after normalization", raw, previous)
		}
		membership.UserID = userID
		membership.Project = projectID
		membership.Environment = environment
		membership.Status = status
		membership.Role = role
		dir.memberships[key] = membership
		seenMembershipRaw[key] = raw
	}

	hasExplicitDirectory := len(dir.users) > 0 || len(dir.projects) > 0 || len(dir.memberships) > 0
	for _, caller := range c.Callers {
		owner, err := callerOwnerUser(caller)
		if err != nil {
			return dir, err
		}
		project := normalizeAccountID(caller.Project)
		if owner == "" && project == "" {
			if hasExplicitDirectory {
				return dir, fmt.Errorf("caller %s missing owner_user and project", caller.ID)
			}
			continue
		}
		if owner == "" {
			return dir, fmt.Errorf("caller %s missing owner_user", caller.ID)
		}
		if project == "" {
			return dir, fmt.Errorf("caller %s missing project", caller.ID)
		}
		if !hasExplicitDirectory {
			addSyntheticAccount(dir, owner, project)
			continue
		}
		user, ok := dir.users[owner]
		if !ok {
			return dir, fmt.Errorf("caller %s references unknown owner_user %s", caller.ID, owner)
		}
		if user.Status != accountStatusActive {
			return dir, fmt.Errorf("caller %s references non-active owner_user %s", caller.ID, owner)
		}
		projectCfg, ok := dir.projects[project]
		if !ok {
			return dir, fmt.Errorf("caller %s references unknown project %s", caller.ID, project)
		}
		if projectCfg.Status != accountStatusActive {
			return dir, fmt.Errorf("caller %s references non-active project %s", caller.ID, project)
		}
		if len(projectCfg.Environments) > 0 && !projectHasEnvironment(projectCfg, normalizeAccountID(caller.Environment)) {
			return dir, fmt.Errorf("caller %s references unknown environment %s/%s", caller.ID, project, caller.Environment)
		}
		membership, ok := accountMembership(dir, owner, project, normalizeAccountID(caller.Environment))
		if !ok {
			return dir, fmt.Errorf("caller %s has no active membership for owner_user %s in project %s", caller.ID, owner, project)
		}
		if membership.Status != accountStatusActive {
			return dir, fmt.Errorf("caller %s has non-active membership for owner_user %s in project %s", caller.ID, owner, project)
		}
	}
	return dir, nil
}

func addSyntheticAccount(dir accountDirectory, owner, project string) {
	if _, ok := dir.users[owner]; !ok {
		dir.users[owner] = UserConfig{ID: owner, Type: "human", Status: accountStatusActive}
	}
	if _, ok := dir.projects[project]; !ok {
		dir.projects[project] = ProjectConfig{ID: project, Status: accountStatusActive}
	}
	key := membershipKey(owner, project, "")
	if _, ok := dir.memberships[key]; !ok {
		dir.memberships[key] = ProjectMembershipConfig{UserID: owner, Project: project, Role: "member", Status: accountStatusActive}
	}
}

func callerOwnerUser(c CallerConfig) (string, error) {
	owner := normalizeAccountID(c.OwnerUser)
	legacy := normalizeAccountID(c.User)
	if owner != "" && legacy != "" && owner != legacy {
		return "", fmt.Errorf("caller %s has conflicting owner_user %s and legacy user %s", c.ID, owner, legacy)
	}
	if owner != "" {
		return owner, nil
	}
	return legacy, nil
}

func normalizeAccountID(v string) string {
	return slugify(v)
}

func normalizeStatusDefault(v string) string {
	v = slugify(v)
	if v == "" {
		return accountStatusActive
	}
	return v
}

func validAccountStatus(v string) bool {
	switch v {
	case accountStatusActive, accountStatusDisabled, accountStatusSuspended, accountStatusRemoved, accountStatusArchived:
		return true
	default:
		return false
	}
}

func validCallerKeyStatus(v string) bool {
	if validAccountStatus(v) {
		return true
	}
	switch v {
	case keyStatusExpired, keyStatusRotated:
		return true
	default:
		return false
	}
}

func normalizeUserTypeDefault(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	if v == "" {
		return "human"
	}
	return v
}

func normalizeRoleDefault(v string) string {
	v = slugify(v)
	if v == "" {
		return "member"
	}
	return v
}

func membershipKey(userID, project string, environment ...string) string {
	env := ""
	if len(environment) > 0 {
		env = environment[0]
	}
	return userID + "\x00" + project + "\x00" + env
}

func projectHasEnvironment(project ProjectConfig, environment string) bool {
	for _, candidate := range project.Environments {
		if normalizeAccountID(candidate.ID) == environment && normalizeStatusDefault(candidate.Status) == accountStatusActive {
			return true
		}
	}
	return false
}

func accountMembership(dir accountDirectory, userID, project, environment string) (ProjectMembershipConfig, bool) {
	if membership, ok := dir.memberships[membershipKey(userID, project, environment)]; ok {
		return membership, true
	}
	membership, ok := dir.memberships[membershipKey(userID, project, "")]
	return membership, ok
}
