// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/x509"
	"errors"
)

type adminMachineCAHook func(*x509.CertPool)
type adminMachineGenerationCASource struct{ owner *Service }

func adminMachineGenerationPool(g *adminMachineGeneration) *x509.CertPool {
	if g == nil || g.trust == nil {
		return nil
	}
	return g.trust.Snapshot()
}
func (s *Service) machineClientCASource() *adminMachineGenerationCASource {
	return &adminMachineGenerationCASource{owner: s}
}
func (src *adminMachineGenerationCASource) Snapshot() *x509.CertPool {
	if src == nil || src.owner == nil {
		return nil
	}
	return adminMachineGenerationPool(src.owner.machineGeneration.Load())
}

// Like the underlying CA store, callbacks run synchronously and must not
// register hooks or initiate reload/publication recursively. Registration and
// publication share producer ordering so an old pool cannot follow a new one.
func (src *adminMachineGenerationCASource) OnReload(hook func(*x509.CertPool)) {
	if src == nil || src.owner == nil || hook == nil {
		return
	}
	s := src.owner
	s.machinePublicationMu.Lock()
	defer s.machinePublicationMu.Unlock()
	s.machineCAHooks = append(s.machineCAHooks, hook)
	hook(adminMachineGenerationPool(s.machineGeneration.Load()))
}

// CA polling never mutates published trust. It reconstructs the complete
// generation from the CURRENT configuration and the CURRENT synchronized
// authorizer through the one publisher, under the one publication lock. It
// therefore cannot publish a bundle built from a superseded configuration, so a
// credential or root that a configuration reload removed cannot come back, and
// it can never publish grants that the synchronized authority does not hold.
func (src *adminMachineGenerationCASource) Reload() error {
	if src == nil || src.owner == nil {
		return errors.New("machine CA owner unavailable")
	}
	s := src.owner
	return s.publishMachinePlane(func(current machinePlaneSnapshot) (*machinePlanePublication, error) {
		if current.Generation == nil || current.Config == nil || current.Authorizer == nil {
			return nil, errors.New("machine CA generation configuration unavailable")
		}
		next, err := s.prepareAdminMachineConfigGeneration(current.Config, current.Authorizer)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, errors.New("machine CA generation factory unavailable")
		}
		return &machinePlanePublication{Generation: next}, nil
	})
}
