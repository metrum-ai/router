// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Test-only publication helpers. Production code publishes only through
// publishMachinePlane, which prepares under the lock; these helpers exist so a
// test can hand over an already prepared generation and prove that the
// publisher still refuses one pinned to superseded authority.

import "errors"

var errTestPreparationRequired = errors.New("machine generation preparation required")

func (s *Service) publishAdminMachineGeneration(next *adminMachineGeneration) error {
	return s.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		// The generation is the authority being published here: the test helper
		// never claims a different snapshot than the bundle it installs.
		var published *authorizer
		if next != nil {
			published = next.authorizer
		}
		return &machinePlanePublication{Generation: next, Authorizer: published}, nil
	})
}

func (s *Service) replaceAdminMachineGeneration(prepare func() (*adminMachineGeneration, error)) error {
	if prepare == nil {
		return errTestPreparationRequired
	}
	return s.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		next, err := prepare()
		if err != nil {
			return nil, err
		}
		var published *authorizer
		if next != nil {
			published = next.authorizer
		}
		return &machinePlanePublication{Generation: next, Authorizer: published}, nil
	})
}
