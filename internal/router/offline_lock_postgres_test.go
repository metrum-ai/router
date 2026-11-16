// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"testing"
)

func TestPostgresConfigRuntimeAdvisoryLocks(t *testing.T) {
	if os.Getenv("ROUTER_TEST_PG_DSN") == "" {
		t.Skip("set ROUTER_TEST_PG_DSN to a disposable PostgreSQL database")
	}
	db, closeDB, _ := configAdminPostgresTestDB(t)
	defer closeDB()
	shared1, err := AcquirePostgresConfigRuntimeLock(db, true)
	if err != nil {
		t.Fatal(err)
	}
	defer shared1()
	shared2, err := AcquirePostgresConfigRuntimeLock(db, true)
	if err != nil {
		t.Fatal(err)
	}
	defer shared2()
	if unlock, err := AcquirePostgresConfigRuntimeLock(db, false); err == nil {
		_ = unlock()
		t.Fatal("exclusive offline lock acquired while router shared locks were held")
	}
}
