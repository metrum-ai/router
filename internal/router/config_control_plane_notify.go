// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"log"
	"strings"

	"gorm.io/gorm"
)

// revisionNotificationObserverKey permits SQLite tests to observe the
// transaction-scoped notification request without pretending SQLite supports
// PostgreSQL LISTEN/NOTIFY. The private context key is not a public API.
type revisionNotificationObserverKey struct{}

// queueConfigRevisionNotification runs inside the mutation transaction.
// PostgreSQL delivers pg_notify only on commit, and discards it on rollback.
// A savepoint keeps notification errors from aborting a successful config
// change; the revision poller remains the fallback. SQLite is a no-op.
func queueConfigRevisionNotification(tx *gorm.DB, scope string) {
	if observer, ok := tx.Statement.Context.Value(revisionNotificationObserverKey{}).(func(*gorm.DB, string)); ok {
		observer(tx, scope)
	}
	if tx.Dialector.Name() != "postgres" {
		return
	}
	// PostgreSQL payloads cannot contain NUL and must be shorter than 8000
	// bytes. Do not let an invalid payload prevent revision updates.
	if len(scope) >= 8000 || strings.ContainsRune(scope, '\x00') {
		log.Print("config revision notification skipped: invalid scope payload; poller remains active")
		return
	}
	if err := tx.Exec("SAVEPOINT router_config_notify").Error; err != nil {
		log.Print("config revision notification savepoint failed; poller remains active")
		return
	}
	if err := tx.Exec("SELECT pg_notify(?, ?)", configControlPlaneNotifyChannel, scope).Error; err != nil {
		if rollbackErr := tx.Exec("ROLLBACK TO SAVEPOINT router_config_notify").Error; rollbackErr != nil {
			log.Print("config revision notification savepoint rollback failed")
		}
		log.Print("config revision notification failed; poller remains active")
	}
	if err := tx.Exec("RELEASE SAVEPOINT router_config_notify").Error; err != nil {
		log.Print("config revision notification savepoint release failed")
	}
}
