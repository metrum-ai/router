// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Production wiring for the /admin/v1 surface (metrum-ai/router#811/#816).
//
// MountAdminV1 is the one call the admin listener makes. It builds the mount
// dependencies from the running service and takes the two cross-lane seams as
// parameters, so this lane never has to wrap or reimplement authentication or
// authorization:
//
//	s.RegisterAdminRoutes(func(m *AdminMux) {
//	    if err := s.MountAdminV1(m, authenticator, authorizer.Authorize); err != nil {
//	        return err
//	    }
//	    return nil
//	})
//
// The durable sinks installed here are what make "no unaudited admin write" true at
// runtime: a control-plane mutation writes its change-event row in the change's
// transaction, a usage-database mutation writes its audit row in the change's
// transaction, refusals go to the security access log, and credential outcomes go to
// the credential-event table.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

// MountAdminV1 mounts the versioned admin contract on r.
func (s *Service) MountAdminV1(r adminRouter, authenticate Authenticator, authorize func(ctx context.Context, principal Principal, domain, object, action string) error) error {
	if s == nil {
		return errors.New("service is nil")
	}
	if s.cfg == nil {
		return errors.New("service configuration is not loaded")
	}
	if authenticate == nil || authorize == nil {
		return errors.New("admin v1 mount requires the authentication and authorization seams")
	}
	s.adminAuthorizeFn = authorize
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		return err
	}
	idempotency, idempotencyOn := s.adminIdempotencyStore()
	settings := s.adminAPISettings()
	return RegisterAdminV1(r, AdminV1Deps{
		Registry:     registry,
		Authenticate: authenticate,
		Authorize:    s.Authorize,
		Handlers:     s.adminV1HandlerSet(registry),
		ResolveDomain: func(principal Principal) string {
			return s.adminCredentialDomain(principal)
		},
		ResolveCallerDomain: func(r *http.Request) (string, error) {
			return AdminV1CallerResourceDomain(s.configControlPlane, s.cfg.Server.ConfigSource.RuntimeScope, r)
		},
		ResolveCapturedDomains: func(r *http.Request) ([]string, error) {
			if s.usage == nil {
				return nil, errAdminV1NoUsageStore
			}
			return AdminV1CapturedRowDomains(s.contentCaptureTargetDomains, r)
		},
		CurrentETag:      s.adminV1CurrentETag,
		CursorCodec:      NewAdminCursorCodec(nil),
		Pagination:       settings.pagination,
		Idempotency:      idempotency,
		IdempotencyOn:    idempotencyOn,
		Limiter:          NewAdminRateLimiter(settings.rateLimit),
		Throttle:         NewAdminAuthThrottle(settings.authFailureThrottle),
		Denials:          s.adminDenialSink(),
		CredentialEvents: s.adminCredentialEventSink(),
		Observe:          s.adminObserveHookFor(),
		Audit:            s.adminAuditSink,
		Clock:            s.adminNow,
	})
}

// adminV1HandlerSet binds every operation to the handler that serves its legacy
// route, including the reports adapter over the existing reports handler.
func (s *Service) adminV1HandlerSet(registry *AdminV1Registry) map[string]http.HandlerFunc {
	principalOf := func(r *http.Request) (Principal, string, bool) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			return Principal{}, "", false
		}
		return principal, s.adminCredentialDomain(principal), true
	}
	authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
		if s == nil {
			return ErrAuthorizationUnavailable
		}
		return s.Authorize(ctx, principal, domain, object, action)
	}
	extra := map[string]http.HandlerFunc{
		"training.export":         s.handleTrainingExport,
		"training.outcomes.label": s.handleTrainingOutcome,
		"metrics.scrape":          s.handleAdminPlaneMetrics,
		"content.captures.delete": s.handleContentCaptureDelete,
		"content.captures.purge":  s.handleContentCapturePurgeExpired,
	}
	return AdminV1ServiceHandlers(
		registry,
		AdminV1ConfigAdminDeps(ConfigAdminDeps{
			DB:                      s.configControlPlane,
			RuntimeScope:            s.cfg.Server.ConfigSource.RuntimeScope,
			FileMode:                s.configControlPlane == nil,
			RequireValidationStatus: s.cfg.Server.ConfigSource.RequireValidationStatus,
			InstanceID:              s.configInstanceID,
			CurrentConfig:           func() *Config { return s.configSnapshot.Load() },
			LoadedRevision:          s.loadedConfigRevision,
			Reload:                  s.reloadActiveConfigFromDB,
			ReloadShadows:           s.loadShadowRunsFromDB,
			UsageDB:                 s.usageDBHandle,
			RecordAccess:            s.recordAdminSecurityAccess,
		}, principalOf, authorize),
		AdminV1CallerAdminDeps(CallerAdminDeps{
			DB:           s.configControlPlane,
			RuntimeScope: s.cfg.Server.ConfigSource.RuntimeScope,
			AfterMutate: func(r *http.Request, callerID string) error {
				_ = callerID
				return s.reloadActiveConfigFromDB()
			},
		}, principalOf, authorize),
		s.handleAdminReports,
		extra,
	)
}

// adminV1Authorize answers a handler's secondary check through the single
// authorization seam the listener supplied.
func (s *Service) adminV1Authorize(subject adminAuthSubject, permission string) bool {
	if s == nil || subject.principal == nil {
		return false
	}
	object, action, ok := adminPermissionObjectAction(permission)
	if !ok {
		return false
	}
	// The mount already recorded the decisions it took; a new decision needs the
	// listener's authorizer, which is installed on the request context's principal
	// path only. Without it the check fails closed.
	if s.adminAuthorizeFn == nil {
		return false
	}
	return s.adminAuthorizeFn(context.Background(), *subject.principal, subject.domain, object, action) == nil
}

// adminDenialSink records refusals in the existing security access log, synchronously
// so a refusal that cannot be recorded fails closed.
func (s *Service) adminDenialSink() AdminDenialSink {
	return adminDenialSinkFunc(func(ctx context.Context, record AdminDenialRecord) error {
		if s == nil || s.usage == nil || s.usage.db == nil {
			return errors.New("security access log is unavailable")
		}
		if err := s.usage.db.Create(&securityAccessEventRecord{
			TS: formatUsageTime(record.At), RequestID: record.RequestID, EventType: "admin_access",
			Surface: "admin", HTTPMethod: defaultString(record.Method, http.MethodGet), PathTemplate: record.Path,
			StatusCode: record.Status, Outcome: securityOutcome(record.Status),
			ReasonCode:     adminAuditFieldValue(record.ReasonCode),
			AuthSubject:    adminAuditFieldValue(record.PrincipalKind + ":" + record.PrincipalSubject),
			AuthSource:     adminAuditFieldValue(record.PrincipalKind),
			AdminSubject:   adminAuditFieldValue(record.PrincipalSubject),
			AdminDomain:    adminAuditFieldValue(record.Domain),
			CredentialKind: adminAuditFieldValue(record.PrincipalKind),
			CredentialID:   adminAuditFieldValue(record.CredentialID),
			Object:         adminAuditFieldValue(record.Object),
			Action:         adminAuditFieldValue(record.Action),
			OperationID:    adminAuditFieldValue(record.OperationID),
			ClientIPHash:   adminAuditFieldValue(record.ClientIPHash),
		}).Error; err != nil {
			return fmt.Errorf("record admin denial: %w", err)
		}
		return nil
	})
}

type adminDenialSinkFunc func(context.Context, AdminDenialRecord) error

func (f adminDenialSinkFunc) RecordDenial(ctx context.Context, record AdminDenialRecord) error {
	return f(ctx, record)
}

// adminCredentialEventSink records authentication outcomes in the control-plane
// credential-event table, falling back to the usage-side audit table when the
// deployment runs the control plane from a file.
func (s *Service) adminCredentialEventSink() AdminCredentialEventSink {
	return adminCredentialEventSinkFunc(func(ctx context.Context, event AdminCredentialEvent) error {
		if s == nil {
			return errors.New("service is nil")
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		if s.configControlPlane != nil {
			if err := s.configControlPlane.Transaction(func(tx *gorm.DB) error {
				return insertAdminCredentialEventTx(tx, event)
			}); err != nil {
				return err
			}
			return nil
		}
		if s.usage == nil || s.usage.db == nil {
			return errors.New("no database is available for credential events")
		}
		return s.usage.db.Transaction(func(tx *gorm.DB) error {
			return insertAdminAuditUsageTx(tx, AdminAuditRecord{
				At: event.At, PrincipalSubject: event.Subject, PrincipalKind: event.Kind,
				CredentialID: event.CredentialID, Issuer: event.Issuer, RequestID: event.RequestID,
				Outcome: event.Outcome, ReasonCode: event.ReasonCode, OperationID: "authentication",
				Action: event.Lifecycle,
			})
		})
	})
}

type adminCredentialEventSinkFunc func(context.Context, AdminCredentialEvent) error

func (f adminCredentialEventSinkFunc) RecordCredentialEvent(ctx context.Context, event AdminCredentialEvent) error {
	return f(ctx, event)
}

// adminV1CurrentETag is the strong validator of the configuration revision.
func (s *Service) adminV1CurrentETag(r *http.Request) (string, int64, error) {
	_ = r
	if s == nil || s.configControlPlane == nil {
		revision, _, _ := s.loadedConfigRevision()
		return AdminETagForRevision(revision), revision, nil
	}
	revision, err := currentRevisionForScope(s.configControlPlane, strings.TrimSpace(s.cfg.Server.ConfigSource.RuntimeScope))
	if err != nil {
		return "", 0, err
	}
	return AdminETagForRevision(revision), revision, nil
}

// adminAPISettings resolves the admin.api block. The block itself is wired into the
// configuration when the admin listener's `admin:` section lands (#809); until then
// the documented defaults apply.
func (s *Service) adminAPISettings() adminAPISettings {
	if s == nil || s.cfg == nil {
		return AdminAPIConfig{}.resolve()
	}
	return s.cfg.Server.AdminAPI.resolve()
}

// adminIdempotencyStore returns the Idempotency-Key store and whether store-mode
// operations are replay-protected. The configuration control-plane database persists
// records across restarts; a file-mode deployment keeps them for the process
// lifetime (documented in docs/ADMIN_API_V1.md).
func (s *Service) adminIdempotencyStore() (AdminIdempotencyKeyStore, bool) {
	if s == nil {
		return nil, false
	}
	settings := s.adminAPISettings()
	if store, ok := s.adminIdempotency.Load().(AdminIdempotencyKeyStore); ok && store != nil {
		return store, true
	}
	if s.configControlPlane != nil {
		if store, err := NewSQLAdminIdempotencyStore(s.configControlPlane, settings.idempotency); err == nil {
			s.adminIdempotency.Store(store)
			return store, true
		}
	}
	store := NewMemoryAdminIdempotencyStore(settings.idempotency, settings.rateLimit.MaxIdentities)
	s.adminIdempotency.Store(store)
	return store, true
}

// adminNow is the mount's clock.
func (s *Service) adminNow() time.Time { return time.Now().UTC() }
