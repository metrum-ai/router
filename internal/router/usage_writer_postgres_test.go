// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// The DSN must name a disposable database; this test intentionally inserts usage rows.
func TestUsageWriterPostgres(t *testing.T) {
	dsn := os.Getenv("OPS09_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OPS09_POSTGRES_TEST_DSN to a disposable PostgreSQL database")
	}
	db, err := openUsageDB(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	s := &usageStore{db: db}
	defer s.Close()
	// Writer integration uses an explicit disposable physical schema; production
	// migration admission is covered by the migration framework's own tests.
	if err = db.AutoMigrate(usageRelationalModels()...); err != nil {
		t.Fatal(err)
	}
	if err = ensureUsageRelationalSchema(db); err != nil {
		t.Fatal(err)
	}
	before := usageCount(t, s)
	s.writer = newUsageWriter(s)
	prefix := fmt.Sprint(time.Now().UnixNano())
	for i := 0; i < 257; i++ {
		s.Emit(logRecord{RequestID: fmt.Sprintf("%s-%d", prefix, i), TS: formatUsageTime(time.Now().UTC())})
	}
	if err = s.writer.close(); err != nil {
		t.Fatal(err)
	}
	if got := usageCount(t, s) - before; got != 257 {
		t.Fatalf("inserted %d rows want 257", got)
	}
}
