// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
)

// minimalConfig is enough for a write-and-reparse round trip.
func minimalConfig() *router.Config {
	return &router.Config{
		StatePath: "/app/state/router-state.json",
		Provider:  map[string]router.ProviderConfig{},
		Models:    map[string]router.ModelGroup{},
	}
}

// A configuration that is bind-mounted as a single read-only file has no writable
// sibling. Adding a caller must still succeed, with the backup going where the
// operator pointed it. Found on Strix Halo hardware: `callers generate --write`
// failed with `open /app/config/config.yaml.bak.<ts>: permission denied`.
func TestWriteConfigAtomicBackupDirOverride(t *testing.T) {
	configDir := t.TempDir()
	backupDir := t.TempDir()
	path := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(path, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_CONFIG_BACKUP_DIR", backupDir)

	backupPath, err := WriteConfigAtomic(path, minimalConfig())
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	if filepath.Dir(backupPath) != backupDir {
		t.Fatalf("backup went to %q, want a file inside %q", backupPath, backupDir)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

// With no writable backup location the write still completes and says so: a backup
// is a convenience, and a configuration copy must never land in a shared temp
// directory as a silent substitute.
func TestWriteConfigAtomicWarnsWithoutBackup(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(path, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// /dev/null is a file, so no directory can be created beneath it.
	t.Setenv("ROUTER_CONFIG_BACKUP_DIR", "/dev/null/not-a-directory")

	backupPath, err := WriteConfigAtomic(path, minimalConfig())
	if err != nil {
		t.Fatalf("write config must survive an unusable backup dir: %v", err)
	}
	// A missing backup is acceptable; a backup anywhere other than the config's own
	// directory (for example a shared temp directory) is not.
	if backupPath != "" && filepath.Dir(backupPath) != configDir {
		t.Fatalf("backup landed at %q; only the config directory is acceptable without ROUTER_CONFIG_BACKUP_DIR", backupPath)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config was not written: %v", err)
	}
}

// An unwritable configuration directory must produce an error that names the cause
// and the remedy, not a bare permission denied.
func TestWriteConfigAtomicErrorNamesTheRemedy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	configDir := t.TempDir()
	path := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(path, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	if _, err := WriteConfigAtomic(path, minimalConfig()); err == nil {
		t.Fatal("expected an error for an unwritable config directory")
	} else {
		for _, want := range []string{"not writable", "mounted read-write", "ROUTER_CONFIG_MOUNT_MODE=rw"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not mention %q", err, want)
			}
		}
	}
}
