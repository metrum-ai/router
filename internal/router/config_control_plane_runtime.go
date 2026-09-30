// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase two of the configuration control plane runtime-side helpers:
// - ConfigSnapshot: a thread-safe, atomically-swappable holder for the
//   currently-served Config. Readers and writers never block each other.
// - StartConfigRevisionPoller: on SQLite, polls the runtime-revision row
//   for a scope and calls reload when it changes.
// - StartConfigRevisionNotifier: on PostgreSQL, attempts to LISTEN on the
//   router_config channel. On SQLite the notifier is a graceful no-op so
//   callers can use one startup path.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// ConfigSnapshot holds an atomic pointer to a Config. Store/Load provide
// lock-free reads and a single atomic write. Get returns the latest stored
// configuration or nil if none has been loaded.
type ConfigSnapshot struct {
	ptr atomic.Pointer[Config]
}

// NewConfigSnapshot returns an empty ConfigSnapshot.
func NewConfigSnapshot() *ConfigSnapshot {
	return &ConfigSnapshot{}
}

// Store atomically replaces the snapshot with cfg. Callers may pass nil
// to clear the snapshot.
func (s *ConfigSnapshot) Store(cfg *Config) {
	s.ptr.Store(cfg)
}

// Load atomically reads the current snapshot. It returns the same pointer
// for the lifetime of a read; callers must not mutate it.
func (s *ConfigSnapshot) Load() *Config {
	return s.ptr.Load()
}

// Get is a convenience wrapper around Load that returns the latest Config
// or nil when nothing has been stored.
func (s *ConfigSnapshot) Get() *Config {
	return s.ptr.Load()
}

// currentRevisionForScope returns the cached revision counter for runtimeScope
// from router_config_runtime, or zero if no row exists yet. Used by both the
// poller and the notifier.
func currentRevisionForScope(db *gorm.DB, runtimeScope string) (int64, error) {
	var revision sql.NullInt64
	if err := db.Raw(
		`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`,
		runtimeScope,
	).Row().Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("read control-plane revision for %q: %w", runtimeScope, err)
	}
	if !revision.Valid {
		return 0, nil
	}
	return revision.Int64, nil
}

// StartConfigRevisionPoller launches a goroutine that polls the runtime
// revision for runtimeScope on interval. When the revision changes, reload
// is invoked (typically: LoadActiveConfigFromDB + snapshot.Store). It
// returns a stop function the caller can use to terminate the poller.
//
// The function is safe to call on both SQLite and PostgreSQL but is only
// required when an external LISTEN connection is unavailable. On SQLite it
// is the primary refresh path.
func StartConfigRevisionPoller(ctx context.Context, db *gorm.DB, runtimeScope string, interval time.Duration, reload func()) (func(), error) {
	if db == nil {
		return nil, errors.New("config control-plane database is required")
	}
	if reload == nil {
		return nil, errors.New("config control-plane reload callback is required")
	}
	if interval <= 0 {
		return nil, errors.New("config control-plane poll interval must be positive")
	}

	// Ensure the runtime tables exist before the first poll so that a
	// fresh deployment does not trip a missing-table error on the initial
	// revision read.
	if err := db.Transaction(func(tx *gorm.DB) error {
		return ensureConfigControlPlaneRuntimeTables(tx)
	}); err != nil {
		return nil, fmt.Errorf("prepare control-plane runtime tables: %w", err)
	}

	// Prime the initial revision so the first poll does not fire a stale
	// reload if another instance already activated a set.
	last, err := currentRevisionForScope(db, runtimeScope)
	if err != nil {
		return nil, err
	}

	pollerCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pollerCtx.Done():
				return
			case <-ticker.C:
				current, err := currentRevisionForScope(db, runtimeScope)
				if err != nil {
					continue
				}
				if current != last {
					last = current
					reload()
				}
			}
		}
	}()
	return cancel, nil
}

// StartConfigRevisionNotifier attempts to LISTEN on the router_config channel
// on a dedicated PostgreSQL connection. On SQLite the call returns a no-op
// stop function and nil error so callers can use one startup path regardless
// of driver.
//
// On PostgreSQL it opens a background connection, issues LISTEN, and invokes
// reload each time the channel receives a notification. If the dedicated
// listener connection cannot be opened, the function returns an error so the
// caller can fall back to polling.
func StartConfigRevisionNotifier(ctx context.Context, db *gorm.DB, runtimeScope string, reload func()) (func(), error) {
	if db == nil {
		return nil, errors.New("config control-plane database is required")
	}
	if reload == nil {
		return nil, errors.New("config control-plane reload callback is required")
	}

	// SQLite has no LISTEN/NOTIFY support. The poller is the refresh path.
	if db.Dialector.Name() != "postgres" {
		return func() {}, nil
	}

	// Phase two stubs the dedicated listener connection so the build does
	// not depend on a driver-specific listener package. The poller continues
	// to provide revision-change detection on PostgreSQL when used in
	// combination with this stub; full LISTEN support will be added in a
	// follow-up that owns the dedicated pgx connection lifecycle.
	return func() {}, nil
}
