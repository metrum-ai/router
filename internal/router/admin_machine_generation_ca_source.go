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

// CA polling never mutates published trust. It reconstructs the full captured
// configuration and compatible authorizer through the same factory/publisher
// as config reload. Requests do not participate in producer serialization.
func (src *adminMachineGenerationCASource) Reload() error {
	if src == nil || src.owner == nil {
		return errors.New("machine CA owner unavailable")
	}
	s := src.owner
	return s.replaceAdminMachineGeneration(func() (*adminMachineGeneration, error) {
		current := s.machineGeneration.Load()
		if current == nil || current.config == nil {
			return nil, errors.New("machine CA generation configuration unavailable")
		}
		cfg := current.config
		var callers map[string]*callerRuntime
		if s.quota != nil {
			// Only the scalar caller authorization inputs are needed. Never
			// retain a mutable runtime directory or hold its lock across DB IO.
			s.quota.mu.Lock()
			callers = make(map[string]*callerRuntime, len(s.quota.callers))
			for id, caller := range s.quota.callers {
				if caller == nil {
					continue
				}
				callers[id] = &callerRuntime{project: caller.project, cfg: CallerConfig{ID: caller.cfg.ID, Project: caller.cfg.Project, Environment: caller.cfg.Environment, MetricsAdmin: caller.cfg.MetricsAdmin, ContentAdmin: caller.cfg.ContentAdmin}}
			}
			s.quota.mu.Unlock()
		}
		compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, callers, s.usage, cfg.Server.AdminAuth)
		if err != nil {
			return nil, err
		}
		return s.prepareAdminMachineConfigGeneration(cfg, compatible)
	})
}
