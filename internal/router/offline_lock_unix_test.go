// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package router

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSharedRuntimeLockTimesOutDuringExclusiveMaintenance(t *testing.T) {
	path := t.TempDir() + "/db.sqlite"
	lock, err := AcquireUsageRuntimeLock(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	probe, err := os.OpenFile(path+".router-online.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	started := time.Now()
	err = acquireUsageFileLockTimeout(probe, false, 120*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "holds the runtime lock") {
		t.Fatalf("shared lock error=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("bounded lock took %s", time.Since(started))
	}
}
