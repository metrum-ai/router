// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testOpener(t *testing.T) ConfigDBOpener {
	t.Helper()
	return func(opts ConfigDBOptions) (*gorm.DB, func() error, error) {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			return nil, nil, err
		}
		closer := func() error {
			sqlDB, err := db.DB()
			if err != nil {
				return err
			}
			return sqlDB.Close()
		}
		return db, closer, nil
	}
}

func validOpts() ConfigDBOptions {
	return ConfigDBOptions{
		Driver:       "sqlite",
		Path:         "/tmp/test.db",
		RuntimeScope: "test",
		Actor:        "tester",
	}
}
func TestConfigDBOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    ConfigDBOptions
		wantErr string
	}{
		{name: "valid sqlite", opts: validOpts(), wantErr: ""},
		{name: "valid postgres", opts: ConfigDBOptions{Driver: "postgres", DSN: "postgres://localhost/test", RuntimeScope: "test", Actor: "tester"}, wantErr: ""},
		{name: "sqlite missing path", opts: ConfigDBOptions{Driver: "sqlite", RuntimeScope: "test", Actor: "tester"}, wantErr: "path is required"},
		{name: "postgres missing dsn", opts: ConfigDBOptions{Driver: "postgres", RuntimeScope: "test", Actor: "tester"}, wantErr: "dsn is required"},
		{name: "unsupported driver", opts: ConfigDBOptions{Driver: "mysql", Path: "/tmp/test.db", RuntimeScope: "test", Actor: "tester"}, wantErr: "unsupported config db driver"},
		{name: "missing runtime scope", opts: ConfigDBOptions{Driver: "sqlite", Path: "/tmp/test.db", Actor: "tester"}, wantErr: "runtime scope is required"},
		{name: "missing actor", opts: ConfigDBOptions{Driver: "sqlite", Path: "/tmp/test.db", RuntimeScope: "test"}, wantErr: "actor is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestConfigDBOptionsUsageDBConfig(t *testing.T) {
	o := validOpts()
	got := o.UsageDBConfig()
	if got.Driver != "sqlite" || got.Path != "/tmp/test.db" || got.DSN != "" {
		t.Errorf("UsageDBConfig() = %+v, want driver/path from opts", got)
	}
}

func TestImportConfigYAML(t *testing.T) {
	called := false
	importFn := func(db *gorm.DB, scope string, yamlBytes []byte, actor string) (*ConfigSetImportResult, error) {
		called = true
		if db == nil {
			t.Error("importFn received nil db")
		}
		if scope != "test" {
			t.Errorf("importFn scope = %q, want %q", scope, "test")
		}
		if actor != "tester" {
			t.Errorf("importFn actor = %q, want %q", actor, "tester")
		}
		if string(yamlBytes) != "server: {}" {
			t.Errorf("importFn yaml = %q, want %q", string(yamlBytes), "server: {}")
		}
		return &ConfigSetImportResult{SetID: "set-1", RuntimeScope: scope, CreatedBy: actor}, nil
	}

	result, err := ImportConfigYAML(validOpts(), []byte("server: {}"), testOpener(t), importFn)
	if err != nil {
		t.Fatalf("ImportConfigYAML() error = %v", err)
	}
	if !called {
		t.Fatal("ImportConfigYAML() did not call importFn")
	}
	if result.SetID != "set-1" {
		t.Errorf("ImportConfigYAML() SetID = %q, want %q", result.SetID, "set-1")
	}
}

func TestImportConfigYAMLValidation(t *testing.T) {
	open := testOpener(t)
	importFn := func(db *gorm.DB, scope string, yamlBytes []byte, actor string) (*ConfigSetImportResult, error) {
		return &ConfigSetImportResult{}, nil
	}
	if _, err := ImportConfigYAML(validOpts(), nil, open, importFn); err == nil {
		t.Fatal("ImportConfigYAML() expected error for empty yaml")
	}
	if _, err := ImportConfigYAML(validOpts(), []byte("x"), nil, importFn); err == nil {
		t.Fatal("ImportConfigYAML() expected error for nil opener")
	}
	if _, err := ImportConfigYAML(validOpts(), []byte("x"), open, nil); err == nil {
		t.Fatal("ImportConfigYAML() expected error for nil importFn")
	}
	badOpts := validOpts()
	badOpts.RuntimeScope = ""
	if _, err := ImportConfigYAML(badOpts, []byte("x"), open, importFn); err == nil {
		t.Fatal("ImportConfigYAML() expected error for invalid opts")
	}
}

func TestImportConfigYAMLPropagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	importFn := func(db *gorm.DB, scope string, yamlBytes []byte, actor string) (*ConfigSetImportResult, error) {
		return nil, sentinel
	}
	_, err := ImportConfigYAML(validOpts(), []byte("server: {}"), testOpener(t), importFn)
	if err == nil {
		t.Fatal("ImportConfigYAML() expected error from importFn")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("ImportConfigYAML() error = %v, want wraps %v", err, sentinel)
	}
}

func TestExportConfigYAML(t *testing.T) {
	called := false
	exportFn := func(db *gorm.DB, scope string, setID string) ([]byte, error) {
		called = true
		if setID != "set-1" {
			t.Errorf("exportFn setID = %q, want %q", setID, "set-1")
		}
		return []byte("server: {}"), nil
	}
	body, err := ExportConfigYAML(validOpts(), "set-1", testOpener(t), exportFn)
	if err != nil {
		t.Fatalf("ExportConfigYAML() error = %v", err)
	}
	if !called {
		t.Fatal("ExportConfigYAML() did not call exportFn")
	}
	if string(body) != "server: {}" {
		t.Errorf("ExportConfigYAML() body = %q, want %q", string(body), "server: {}")
	}
}

func TestExportConfigYAMLValidation(t *testing.T) {
	open := testOpener(t)
	exportFn := func(db *gorm.DB, scope string, setID string) ([]byte, error) {
		return []byte("x"), nil
	}
	if _, err := ExportConfigYAML(validOpts(), "", open, exportFn); err == nil {
		t.Fatal("ExportConfigYAML() expected error for empty set id")
	}
	if _, err := ExportConfigYAML(validOpts(), "set-1", nil, exportFn); err == nil {
		t.Fatal("ExportConfigYAML() expected error for nil opener")
	}
	if _, err := ExportConfigYAML(validOpts(), "set-1", open, nil); err == nil {
		t.Fatal("ExportConfigYAML() expected error for nil exportFn")
	}
}

func TestActivateConfigSet(t *testing.T) {
	called := false
	activateFn := func(db *gorm.DB, scope string, setID string, actor string) (*ConfigSetActivationResult, error) {
		called = true
		return &ConfigSetActivationResult{SetID: setID, RuntimeScope: scope, Status: "active", ActivatedBy: actor}, nil
	}
	result, err := ActivateConfigSet(validOpts(), "set-1", testOpener(t), activateFn)
	if err != nil {
		t.Fatalf("ActivateConfigSet() error = %v", err)
	}
	if !called {
		t.Fatal("ActivateConfigSet() did not call activateFn")
	}
	if result.Status != "active" {
		t.Errorf("ActivateConfigSet() Status = %q, want %q", result.Status, "active")
	}
}

func TestActivateConfigSetValidation(t *testing.T) {
	open := testOpener(t)
	activateFn := func(db *gorm.DB, scope string, setID string, actor string) (*ConfigSetActivationResult, error) {
		return &ConfigSetActivationResult{}, nil
	}
	if _, err := ActivateConfigSet(validOpts(), "", open, activateFn); err == nil {
		t.Fatal("ActivateConfigSet() expected error for empty set id")
	}
	if _, err := ActivateConfigSet(validOpts(), "set-1", nil, activateFn); err == nil {
		t.Fatal("ActivateConfigSet() expected error for nil opener")
	}
	if _, err := ActivateConfigSet(validOpts(), "set-1", open, nil); err == nil {
		t.Fatal("ActivateConfigSet() expected error for nil activateFn")
	}
}

func TestRollbackConfigSet(t *testing.T) {
	called := false
	rollbackFn := func(db *gorm.DB, scope string, actor string) (*ConfigSetRollbackResult, error) {
		called = true
		return &ConfigSetRollbackResult{PreviousSetID: "set-1", ActiveSetID: "set-0", RuntimeScope: scope, RolledBackBy: actor}, nil
	}
	result, err := RollbackConfigSet(validOpts(), testOpener(t), rollbackFn)
	if err != nil {
		t.Fatalf("RollbackConfigSet() error = %v", err)
	}
	if !called {
		t.Fatal("RollbackConfigSet() did not call rollbackFn")
	}
	if result.ActiveSetID != "set-0" {
		t.Errorf("RollbackConfigSet() ActiveSetID = %q, want %q", result.ActiveSetID, "set-0")
	}
}

func TestRollbackConfigSetValidation(t *testing.T) {
	open := testOpener(t)
	rollbackFn := func(db *gorm.DB, scope string, actor string) (*ConfigSetRollbackResult, error) {
		return &ConfigSetRollbackResult{}, nil
	}
	if _, err := RollbackConfigSet(validOpts(), nil, rollbackFn); err == nil {
		t.Fatal("RollbackConfigSet() expected error for nil opener")
	}
	if _, err := RollbackConfigSet(validOpts(), open, nil); err == nil {
		t.Fatal("RollbackConfigSet() expected error for nil rollbackFn")
	}
}

func TestCallersIssueDB(t *testing.T) {
	called := false
	issueFn := func(db *gorm.DB, scope string, opts CallerIssueOptions, actor string) (*CallerIssueResult, error) {
		called = true
		if opts.OwnerUser != "alice" {
			t.Errorf("issueFn OwnerUser = %q, want %q", opts.OwnerUser, "alice")
		}
		return &CallerIssueResult{
			CallerID:  "alice.proj.dev",
			TokenID:   "tok-1",
			Token:     "rtr_metrum_secret",
			OwnerUser: opts.OwnerUser,
			Project:   opts.Project,
		}, nil
	}
	issueOpts := CallerIssueOptions{OwnerUser: "alice", Project: "proj", Environment: "dev", Allow: []string{"default"}}
	result, err := CallersIssueDB(validOpts(), issueOpts, testOpener(t), issueFn)
	if err != nil {
		t.Fatalf("CallersIssueDB() error = %v", err)
	}
	if !called {
		t.Fatal("CallersIssueDB() did not call issueFn")
	}
	if result.CallerID != "alice.proj.dev" {
		t.Errorf("CallersIssueDB() CallerID = %q, want %q", result.CallerID, "alice.proj.dev")
	}
	if result.Token == "" {
		t.Error("CallersIssueDB() Token is empty; CLI needs it for one-time display")
	}
}

func TestCallersIssueDBValidation(t *testing.T) {
	open := testOpener(t)
	issueFn := func(db *gorm.DB, scope string, opts CallerIssueOptions, actor string) (*CallerIssueResult, error) {
		return &CallerIssueResult{}, nil
	}
	validIssue := CallerIssueOptions{OwnerUser: "alice", Project: "proj", Environment: "dev"}

	if _, err := CallersIssueDB(validOpts(), validIssue, nil, issueFn); err == nil {
		t.Fatal("CallersIssueDB() expected error for nil opener")
	}
	if _, err := CallersIssueDB(validOpts(), validIssue, open, nil); err == nil {
		t.Fatal("CallersIssueDB() expected error for nil issueFn")
	}
	missingOwner := validIssue
	missingOwner.OwnerUser = ""
	if _, err := CallersIssueDB(validOpts(), missingOwner, open, issueFn); err == nil {
		t.Fatal("CallersIssueDB() expected error for missing owner user")
	}
	missingProject := validIssue
	missingProject.Project = ""
	if _, err := CallersIssueDB(validOpts(), missingProject, open, issueFn); err == nil {
		t.Fatal("CallersIssueDB() expected error for missing project")
	}
	missingEnv := validIssue
	missingEnv.Environment = ""
	if _, err := CallersIssueDB(validOpts(), missingEnv, open, issueFn); err == nil {
		t.Fatal("CallersIssueDB() expected error for missing environment")
	}
}

func TestCallersRevokeDB(t *testing.T) {
	called := false
	revokeFn := func(db *gorm.DB, scope string, callerID string, actor string) (*CallerRevokeResult, error) {
		called = true
		return &CallerRevokeResult{CallerID: callerID, Status: "disabled", RevokedBy: actor}, nil
	}
	result, err := CallersRevokeDB(validOpts(), "alice.proj.dev", testOpener(t), revokeFn)
	if err != nil {
		t.Fatalf("CallersRevokeDB() error = %v", err)
	}
	if !called {
		t.Fatal("CallersRevokeDB() did not call revokeFn")
	}
	if result.Status != "disabled" {
		t.Errorf("CallersRevokeDB() Status = %q, want %q", result.Status, "disabled")
	}
}

func TestCallersRevokeDBValidation(t *testing.T) {
	open := testOpener(t)
	revokeFn := func(db *gorm.DB, scope string, callerID string, actor string) (*CallerRevokeResult, error) {
		return &CallerRevokeResult{}, nil
	}
	if _, err := CallersRevokeDB(validOpts(), "", open, revokeFn); err == nil {
		t.Fatal("CallersRevokeDB() expected error for empty caller id")
	}
	if _, err := CallersRevokeDB(validOpts(), "alice.proj.dev", nil, revokeFn); err == nil {
		t.Fatal("CallersRevokeDB() expected error for nil opener")
	}
	if _, err := CallersRevokeDB(validOpts(), "alice.proj.dev", open, nil); err == nil {
		t.Fatal("CallersRevokeDB() expected error for nil revokeFn")
	}
}

func TestCallersRotateDB(t *testing.T) {
	called := false
	rotateFn := func(db *gorm.DB, scope string, callerID string, actor string) (*CallerRotateResult, error) {
		called = true
		return &CallerRotateResult{
			CallerID:  callerID,
			TokenID:   "tok-2",
			Token:     "rtr_metrum_newsecret",
			RotatedBy: actor,
		}, nil
	}
	result, err := CallersRotateDB(validOpts(), "alice.proj.dev", testOpener(t), rotateFn)
	if err != nil {
		t.Fatalf("CallersRotateDB() error = %v", err)
	}
	if !called {
		t.Fatal("CallersRotateDB() did not call rotateFn")
	}
	if result.TokenID != "tok-2" {
		t.Errorf("CallersRotateDB() TokenID = %q, want %q", result.TokenID, "tok-2")
	}
	if result.Token == "" {
		t.Error("CallersRotateDB() Token is empty; CLI needs it for one-time display")
	}
}

func TestCallersRotateDBValidation(t *testing.T) {
	open := testOpener(t)
	rotateFn := func(db *gorm.DB, scope string, callerID string, actor string) (*CallerRotateResult, error) {
		return &CallerRotateResult{}, nil
	}
	if _, err := CallersRotateDB(validOpts(), "", open, rotateFn); err == nil {
		t.Fatal("CallersRotateDB() expected error for empty caller id")
	}
	if _, err := CallersRotateDB(validOpts(), "alice.proj.dev", nil, rotateFn); err == nil {
		t.Fatal("CallersRotateDB() expected error for nil opener")
	}
	if _, err := CallersRotateDB(validOpts(), "alice.proj.dev", open, nil); err == nil {
		t.Fatal("CallersRotateDB() expected error for nil rotateFn")
	}
}

func TestCallersListDB(t *testing.T) {
	called := false
	listFn := func(db *gorm.DB, scope string) ([]CallerListEntry, error) {
		called = true
		return []CallerListEntry{
			{CallerID: "alice.proj.dev", OwnerUser: "alice", Project: "proj", Environment: "dev", Status: "active", TokenID: "tok-1"},
			{CallerID: "bob.proj.dev", OwnerUser: "bob", Project: "proj", Environment: "dev", Status: "active", TokenID: "tok-2"},
		}, nil
	}
	entries, err := CallersListDB(validOpts(), testOpener(t), listFn)
	if err != nil {
		t.Fatalf("CallersListDB() error = %v", err)
	}
	if !called {
		t.Fatal("CallersListDB() did not call listFn")
	}
	if len(entries) != 2 {
		t.Fatalf("CallersListDB() len = %d, want 2", len(entries))
	}
	if entries[0].CallerID != "alice.proj.dev" {
		t.Errorf("CallersListDB() entries[0].CallerID = %q, want %q", entries[0].CallerID, "alice.proj.dev")
	}
}

func TestCallersListDBValidation(t *testing.T) {
	open := testOpener(t)
	listFn := func(db *gorm.DB, scope string) ([]CallerListEntry, error) {
		return nil, nil
	}
	if _, err := CallersListDB(validOpts(), nil, listFn); err == nil {
		t.Fatal("CallersListDB() expected error for nil opener")
	}
	if _, err := CallersListDB(validOpts(), open, nil); err == nil {
		t.Fatal("CallersListDB() expected error for nil listFn")
	}
}
