// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"path/filepath"
)

// writeVersionedStateFile is the shared durable-file contract for signed
// quota state: owner-only temp write, atomic replacement, and one
// owner-only last-known-good backup. Callers validate integrity on read; a
// backup is never used to mask a present but invalid primary file.
func writeVersionedStateFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if previous, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", previous, 0o600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// readVersionedStateFile recovers only a missing primary from its last known
// backup. A corrupt or tampered primary stays visible and fails closed.
func readVersionedStateFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	backup, backupErr := os.ReadFile(path + ".bak")
	if backupErr == nil {
		return backup, nil
	}
	if os.IsNotExist(backupErr) {
		return nil, err
	}
	return nil, backupErr
}
