// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Templates contain schema (and, for configured stores, the real migration
// ledger), never request data. Each fixture gets a private byte copy, not a
// shared connection, hard link, transaction or database. Migration tests keep
// using the uncached constructors so actual DDL/adoption remains under test.
var usageFixtureTemplates [2]struct {
	once sync.Once
	path string
	err  error
}

func usageFixtureTemplate(managed bool) (string, error) {
	index := 0
	if managed {
		index = 1
	}
	entry := &usageFixtureTemplates[index]
	entry.once.Do(func() {
		dir, err := os.MkdirTemp("", "router-usage-template-")
		if err != nil {
			entry.err = err
			return
		}
		entry.path = filepath.Join(dir, "usage.sqlite")
		var store *usageStore
		if managed {
			store, err = OpenUsageStore(uncachedSQLiteUsageDBConfigForTest(entry.path))
		} else {
			store, err = OpenUsageStorePath(entry.path)
		}
		if err != nil {
			entry.err = err
			return
		}
		// No writer is running. Flush every WAL page before closing/copying only
		// the main file, including the migration ledger and its indexes.
		err = store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error
		closeErr := store.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Chmod(entry.path, 0400)
		}
		entry.err = err
	})
	return entry.path, entry.err
}

func cloneUsageFixture(path string, managed bool) error {
	if path == "" || strings.ContainsAny(path, "?:") {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	source, err := usageFixtureTemplate(managed)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func cleanupUsageFixtureTemplates() {
	for i := range usageFixtureTemplates {
		if path := usageFixtureTemplates[i].path; path != "" {
			_ = os.RemoveAll(filepath.Dir(path))
		}
	}
}

func openTestUsageStorePath(path string) (*usageStore, error) {
	// Existing/URI stores still take the real compatibility path, including
	// upgrade and corruption checks. Only a genuinely absent file is cloned.
	if _, err := os.Stat(path); err == nil || path == "" || strings.ContainsAny(path, "?:") {
		return OpenUsageStorePath(path)
	}
	if err := cloneUsageFixture(path, false); err != nil {
		return nil, err
	}
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		return nil, err
	}
	return &usageStore{db: db}, nil
}

// Compare the same store initialization before and after template cloning.
// Template construction is intentionally outside the amortized fixture timing.
func BenchmarkUsageFixtureSetup(b *testing.B) {
	for _, managed := range []bool{false, true} {
		if _, err := usageFixtureTemplate(managed); err != nil {
			b.Fatal(err)
		}
		for _, cached := range []bool{false, true} {
			b.Run(fmt.Sprintf("managed=%v/cached=%v", managed, cached), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					path := filepath.Join(b.TempDir(), "usage.sqlite")
					var store *usageStore
					var err error
					if cached && managed {
						err = cloneUsageFixture(path, managed)
						if err != nil {
							b.Fatal(err)
						}
					}
					if managed {
						store, err = OpenUsageStore(uncachedSQLiteUsageDBConfigForTest(path))
					} else if cached {
						store, err = openTestUsageStorePath(path)
					} else {
						store, err = OpenUsageStorePath(path)
					}
					if err != nil {
						b.Fatal(err)
					}
					if err := store.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestUsageFixtureClonesAreIndependent(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			a, b := filepath.Join(t.TempDir(), "a.sqlite"), filepath.Join(t.TempDir(), "b.sqlite")
			for _, path := range []string{a, b} {
				if err := cloneUsageFixture(path, managed); err != nil {
					t.Fatal(err)
				}
			}
			ai, _ := os.Stat(a)
			bi, _ := os.Stat(b)
			if os.SameFile(ai, bi) || ai.Mode().Perm() != 0600 {
				t.Fatal("fixtures must be private independent files")
			}
			first, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: a})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db, _ := first.DB(); _ = db.Close() }()
			second, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: b})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db, _ := second.DB(); _ = db.Close() }()
			if err := first.Create(&usageRecord{RequestID: "only-a"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := first.Exec("ALTER TABLE request_usage ADD COLUMN only_a INTEGER").Error; err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := second.Model(&usageRecord{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("request data leaked: count=%d err=%v", count, err)
			}
			if second.Migrator().HasColumn(&usageRecord{}, "only_a") {
				t.Fatal("schema mutation leaked")
			}
			if managed {
				runner, err := newUsageMigrationRunner(second)
				if err != nil {
					t.Fatal(err)
				}
				status, err := runner.Verify()
				if err != nil || !UsageMigrationServingCompatible(status) {
					t.Fatalf("cloned ledger incompatible: %+v %v", status, err)
				}
			}
		})
	}
}
