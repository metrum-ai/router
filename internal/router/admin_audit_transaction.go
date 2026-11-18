// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// SQLite retries inside auditTransaction so the outer call never multiplies its budget.
func retryAuditedTxConflict(db *gorm.DB, write func() error) error {
	if db.Dialector.Name() == "postgres" {
		return retryTransientTx(write)
	}
	return write()
}

// auditTransaction owns the mutation and its audit append. SQLite permits a
// single writer per database, including across separate processes. Reserve
// that slot before reading the state used by the mutation or its audit chain.
func auditTransaction(db *gorm.DB, mutate func(*gorm.DB) error) error {
	if db.Dialector.Name() != "sqlite" {
		return db.Transaction(mutate)
	}
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		// Only the owner of the outer transaction can discard its snapshot.
		return db.Transaction(mutate)
	}
	wait, cancel := context.WithTimeout(db.Statement.Context, 6*time.Second)
	defer cancel()
	// SQLite's busy handler can continue sleeping after context cancellation.
	// Use short waits only on this leased connection, restoring the pool's
	// original setting before returning it to other users.
	return db.WithContext(wait).Connection(func(pinned *gorm.DB) error {
		conn := pinned.Statement.ConnPool.(*sql.Conn)
		var previous int
		if err := conn.QueryRowContext(wait, "PRAGMA busy_timeout").Scan(&previous); err != nil {
			return err
		}
		deadline, _ := wait.Deadline()
		attemptMillis := max(int64(1), min(int64(1200), time.Until(deadline).Milliseconds()))
		if _, err := conn.ExecContext(wait, "PRAGMA busy_timeout="+strconv.FormatInt(attemptMillis, 10)); err != nil {
			return err
		}
		defer func() {
			if _, err := conn.ExecContext(context.WithoutCancel(wait), "PRAGMA busy_timeout="+strconv.Itoa(previous)); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
		return retrySQLiteAuditWrite(pinned.WithContext(db.Statement.Context), wait, mutate)
	})
}

func retrySQLiteAuditWrite(db *gorm.DB, wait context.Context, mutate func(*gorm.DB) error) error {
	for attempt := 0; ; attempt++ {
		callbackSucceeded := false
		err := db.Transaction(func(tx *gorm.DB) error {
			// This zero-row write is the equivalent of BEGIN IMMEDIATE for a
			// GORM transaction. The connection's busy_timeout applies, and
			// read-only transactions on the same pool stay deferred.
			writer := tx.WithContext(wait)
			const reserve = "UPDATE router_admin_audit_chain_anchors SET head_hash=head_hash WHERE 0"
			err := execWithAuditAnchors(writer, reserve)
			if err != nil {
				return err
			}
			err = mutate(tx)
			callbackSucceeded = err == nil
			return err
		})
		// Never replay an ambiguous commit. Callback failures have already
		// rolled back before the next attempt obtains a fresh transaction.
		var driverError interface{ Code() int }
		if err == nil || callbackSucceeded || attempt == 4 || !errors.As(err, &driverError) {
			return err
		}
		switch driverError.Code() & 0xff {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED (including extended codes)
		default:
			return err
		}
		backoff := (10 * time.Millisecond) << attempt
		select {
		case <-wait.Done():
			return err
		case <-time.After(backoff + rand.N(backoff)):
		}
	}
}

// The first statement against an existing anchor table must be a write. A
// schema probe first would open a deferred read snapshot that cannot always
// upgrade to a writer while another connection commits.
func execWithAuditAnchors(tx *gorm.DB, query string, args ...any) error {
	err := tx.Exec(query, args...).Error
	if err == nil || !strings.Contains(err.Error(), "no such table: "+adminAuditChainAnchorsTable) {
		return err
	}
	if err := ensureAdminAuditChainAnchors(tx); err != nil {
		return err
	}
	return tx.Exec(query, args...).Error
}
