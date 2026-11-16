// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/metrum-ai/router/internal/router"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// UsageBackupResult is a secret-free summary of a SQLite usage backup.
type UsageBackupResult struct {
	Schema      string `json:"schema"`
	Driver      string `json:"driver"`
	SourceBase  string `json:"source_base"`
	DestBase    string `json:"dest_base"`
	FilesCopied int    `json:"files_copied"`
	IntegrityOK bool   `json:"integrity_ok"`
}

// UsageRestoreResult is a secret-free summary of a SQLite usage restore.
type UsageRestoreResult struct {
	Schema             string `json:"schema"`
	Driver             string `json:"driver"`
	SourceBase         string `json:"source_base"`
	DestBase           string `json:"dest_base"`
	FilesRestored      int    `json:"files_restored"`
	IntegrityOK        bool   `json:"integrity_ok"`
	NextStep           string `json:"next_step"`
	ConfirmOfflineUsed bool   `json:"confirm_offline_used"`
}

// BackupSQLiteUsage copies the SQLite DB and WAL/SHM sidecars after an exclusive offline confirm.
func BackupSQLiteUsage(cfg *router.Config, destPath string, confirmOffline bool) (*UsageBackupResult, error) {
	if !confirmOffline {
		return nil, fmt.Errorf("refusing backup without --confirm-offline; stop the router and confirm exclusive SQLite access")
	}
	driver, path, err := sqliteUsagePath(cfg)
	if err != nil {
		return nil, err
	}
	lock, err := router.AcquireUsageRuntimeLock(path, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := checkSQLiteOfflineOperationCapacity(path); err != nil {
		return nil, err
	}
	if err := copySQLiteFamily(path, destPath); err != nil {
		return nil, err
	}
	ok, err := integrityCheck(destPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("backup integrity_check failed")
	}
	if err := recordSQLiteOfflineOperation(path, "usage backup"); err != nil {
		return nil, err
	}
	return &UsageBackupResult{
		Schema:      "metrum.ai/smartrouter-usage-backup/v1",
		Driver:      driver,
		SourceBase:  filepath.Base(path),
		DestBase:    filepath.Base(destPath),
		FilesCopied: countSQLiteFamily(destPath),
		IntegrityOK: ok,
	}, nil
}

// RestoreSQLiteUsage restores a SQLite usage DB family while the router is offline.
func RestoreSQLiteUsage(cfg *router.Config, sourcePath string, confirmOffline bool) (*UsageRestoreResult, error) {
	if !confirmOffline {
		return nil, fmt.Errorf("refusing restore without --confirm-offline; stop the router and confirm exclusive SQLite access")
	}
	driver, path, err := sqliteUsagePath(cfg)
	if err != nil {
		return nil, err
	}
	lock, err := router.AcquireUsageRuntimeLock(path, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if _, err := os.Stat(sourcePath); err != nil {
		return nil, fmt.Errorf("backup source: %w", err)
	}
	ok, err := integrityCheck(sourcePath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("backup integrity_check failed")
	}
	if err := checkSQLiteOfflineOperationCapacity(sourcePath); err != nil {
		return nil, fmt.Errorf("backup provenance capacity: %w", err)
	}
	if err := copySQLiteFamily(sourcePath, path); err != nil {
		return nil, err
	}
	ok, err = integrityCheck(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("restored database integrity_check failed")
	}
	if err := recordSQLiteOfflineOperation(path, "usage restore"); err != nil {
		return nil, err
	}
	return &UsageRestoreResult{
		Schema:             "metrum.ai/smartrouter-usage-restore/v1",
		Driver:             driver,
		SourceBase:         filepath.Base(sourcePath),
		DestBase:           filepath.Base(path),
		FilesRestored:      countSQLiteFamily(path),
		IntegrityOK:        ok,
		ConfirmOfflineUsed: true,
		NextStep:           "Run router-migrate --action=verify-serving --driver=sqlite --db=<usage path> before serving.",
	}, nil
}

func recordSQLiteOfflineOperation(dbPath, command string) error {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return router.RecordOfflineUsageMaintenance(db, command, "operator")
}

func checkSQLiteOfflineOperationCapacity(dbPath string) error {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return router.CheckOfflineUsageMarkerCapacity(db)
}

func sqliteUsagePath(cfg *router.Config) (driver, path string, err error) {
	if cfg == nil {
		return "", "", fmt.Errorf("config is required")
	}
	if cfg.Server.UsageDB.Enable != nil && !*cfg.Server.UsageDB.Enable {
		return "", "", fmt.Errorf("usage database is disabled")
	}
	driver = strings.ToLower(strings.TrimSpace(cfg.Server.UsageDB.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "sqlite" {
		return driver, "", fmt.Errorf("usage backup/restore supports sqlite only; for postgres see docs/DATA_MIGRATIONS.md")
	}
	path = strings.TrimSpace(cfg.Server.UsageDB.Path)
	if path == "" {
		return driver, "", fmt.Errorf("server.usage_db.path is required for sqlite")
	}
	return driver, path, nil
}

func copySQLiteFamily(src, dst string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return fmt.Errorf("source and destination paths are required")
	}
	if filepath.Clean(src) == filepath.Clean(dst) {
		return fmt.Errorf("source and destination must differ")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Remove destination sidecars so a restore cannot leave a stale WAL attached.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(dst + suffix)
	}
	copied := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		from := src + suffix
		to := dst + suffix
		if _, err := os.Stat(from); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
		copied++
	}
	if copied == 0 {
		return fmt.Errorf("no sqlite files found at %s", src)
	}
	return nil
}

func countSQLiteFamily(base string) int {
	n := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(base + suffix); err == nil {
			n++
		}
	}
	return n
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func integrityCheck(path string) (bool, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return false, fmt.Errorf("open sqlite for integrity: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return false, err
	}
	defer sqlDB.Close()
	var result string
	if err := sqlDB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return false, err
	}
	return result == "ok", nil
}

// OpenEmptySQLite creates an empty sqlite file for tests.
func OpenEmptySQLite(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	gdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("create sqlite: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	_, err = sqlDB.Exec("CREATE TABLE IF NOT EXISTS smartrouterctl_probe (id INTEGER PRIMARY KEY)")
	return err
}

// TimestampedBackupName builds a destination basename for usage backups.
func TimestampedBackupName(prefix string) string {
	if prefix == "" {
		prefix = "usage"
	}
	return fmt.Sprintf("%s-%s.sqlite", prefix, time.Now().UTC().Format("20060102T150405Z"))
}
