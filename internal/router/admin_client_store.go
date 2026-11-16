// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	adminClientTable           = "router_admin_clients"
	adminClientCredentialTable = "router_admin_client_credentials"
	adminClientMaxClients      = 1000
	adminClientMaxCredentials  = 50
)

var errAdminClientConflict = errors.New("admin client conflict")
var errAdminClientNotFound = errors.New("admin client not found")
var errAdminClientInvalidRequest = errors.New("invalid admin client request")

type adminClientCredential struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Identity        string     `json:"identity"`
	Subject         string     `json:"subject"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	LastUsedAt      *time.Time `json:"lastUsedAt,omitempty"`
	Status          string     `json:"status"`
	EffectiveGrants [][]string `json:"effectiveGrants,omitempty" gorm:"-"`
	ExpiresSoon     bool       `json:"expiresSoon,omitempty" gorm:"-"`
	Unused          bool       `json:"unused,omitempty" gorm:"-"`
}

type adminClient struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Status      string                  `json:"status"`
	CreatedAt   time.Time               `json:"createdAt"`
	UpdatedAt   time.Time               `json:"updatedAt"`
	Credentials []adminClientCredential `json:"credentials" gorm:"-"`
}

type adminClientStore struct {
	db       *gorm.DB
	now      func() time.Time
	recorder func(*gorm.DB, adminClientLifecycleEvent) error
}

type adminClientLifecycleEvent struct {
	ClientID, CredentialID, Subject, Actor, RequestID, Action string
}

func adminClientAuditValues(values []string) (actor, requestID string) {
	if len(values) > 0 {
		actor = values[0]
	}
	if len(values) > 1 {
		requestID = values[1]
	}
	return actor, requestID
}

func (s *adminClientStore) record(tx *gorm.DB, event adminClientLifecycleEvent) error {
	if s.recorder == nil {
		return nil
	}
	return s.recorder(tx, event)
}

type adminClientBindingAuthenticator struct {
	inner Authenticator
	store func() *adminClientStore
}

func (a *adminClientBindingAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	principal, err := a.inner.Authenticate(ctx, r)
	if err != nil {
		return Principal{}, err
	}
	store := a.store()
	if store == nil {
		return principal, nil
	}
	allowed, err := store.authenticate(ctx, principal)
	if err != nil || !allowed {
		return Principal{}, ErrInvalidCredential
	}
	return principal, nil
}

func adminClientMigrationDefinition(previous, schemaVersion int) MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: 2026111605, Scope: configControlPlaneScope,
		Name: "admin client identities and credential metadata", Release: "v5.0.0",
		Checksum: MigrationChecksum("router-config.phase17.admin-clients.v1"), SchemaVersion: schemaVersion,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey: "router-config.phase17.apply.v1@applyAdminClientSchema", PostconditionKey: "router-config.phase17.schema.v1@verifyAdminClientSchema",
		Dependencies: []int{previous}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		Apply: applyAdminClientSchema, Verify: verifyAdminClientSchema})
}

func verifyAdminClientSchema(tx *gorm.DB) error {
	for _, table := range []string{adminClientTable, adminClientCredentialTable} {
		if !tx.Migrator().HasTable(table) {
			return errors.New("admin client schema incomplete")
		}
	}
	if !tx.Migrator().HasColumn(adminClientCredentialTable, "unused_warning_emitted_at") {
		return errors.New("admin client unused-warning schema incomplete")
	}
	if tx.Migrator().HasTable(adminCredentialEventsTable) {
		for _, column := range []string{"client_id", "actor"} {
			if !tx.Migrator().HasColumn(adminCredentialEventsTable, column) {
				return errors.New("admin client lifecycle audit schema incomplete")
			}
		}
	}
	return nil
}

func (s *adminClientStore) create(ctx context.Context, client adminClient, audit ...string) error {
	actor, requestID := adminClientAuditValues(audit)
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	if !validAdminClientID(client.ID) || strings.TrimSpace(client.Name) == "" || len(client.Name) > 128 {
		return errAdminClientInvalidRequest
	}
	now := s.now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw("SELECT COUNT(*) FROM " + adminClientTable + " WHERE status='active'").Scan(&count).Error; err != nil {
			return err
		}
		if count >= adminClientMaxClients {
			return errAdminClientConflict
		}
		if err := tx.Exec("INSERT INTO "+adminClientTable+" (id,name,status,created_at,updated_at) VALUES (?,?, 'active',?,?)", client.ID, client.Name, now, now).Error; err != nil {
			return errAdminClientConflict
		}
		return s.record(tx, adminClientLifecycleEvent{ClientID: client.ID, Actor: actor, RequestID: requestID, Action: "created"})
	})
}

func (s *adminClientStore) list(ctx context.Context) ([]adminClient, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("admin client store unavailable")
	}
	var rows []adminClient
	if err := s.db.WithContext(ctx).Raw("SELECT id,name,status,created_at,updated_at FROM "+adminClientTable+" WHERE status <> 'deleted' ORDER BY CASE WHEN status='active' THEN 0 ELSE 1 END,id LIMIT ?", adminClientMaxClients).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Credentials = []adminClientCredential{}
		var creds []adminClientCredential
		// Active bindings are the first-class access-review set. Keep them ahead
		// of the bounded revoked-history tail so old rows cannot hide a working
		// credential from operators.
		if err := s.db.WithContext(ctx).Raw("SELECT id,kind,identity,subject,expires_at,last_used_at,status FROM "+adminClientCredentialTable+" WHERE client_id = ? ORDER BY CASE WHEN status='active' THEN 0 ELSE 1 END,id LIMIT ?", rows[i].ID, adminClientMaxCredentials*2).Scan(&creds).Error; err != nil {
			return nil, err
		}
		rows[i].Credentials = creds
	}
	return rows, nil
}

func (s *adminClientStore) get(ctx context.Context, id string) (adminClient, error) {
	if s == nil || s.db == nil {
		return adminClient{}, errors.New("admin client store unavailable")
	}
	var row adminClient
	if err := s.db.WithContext(ctx).Raw("SELECT id,name,status,created_at,updated_at FROM "+adminClientTable+" WHERE id=?", id).Scan(&row).Error; err != nil {
		return adminClient{}, err
	}
	if row.ID == "" {
		return adminClient{}, errAdminClientNotFound
	}
	row.Credentials = []adminClientCredential{}
	if err := s.db.WithContext(ctx).Raw("SELECT id,kind,identity,subject,expires_at,last_used_at,status FROM "+adminClientCredentialTable+" WHERE client_id=? ORDER BY CASE WHEN status='active' THEN 0 ELSE 1 END,id LIMIT ?", id, adminClientMaxCredentials*2).Scan(&row.Credentials).Error; err != nil {
		return adminClient{}, err
	}
	return row, nil
}

func (s *adminClientStore) setStatus(ctx context.Context, id, status string, audit ...string) error {
	actor, requestID := adminClientAuditValues(audit)
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	if status != "active" && status != "disabled" && status != "deleted" {
		return errAdminClientInvalidRequest
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current string
		if err := tx.Raw("SELECT status FROM "+adminClientTable+" WHERE id=?", id).Row().Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAdminClientNotFound
			}
			return err
		}
		if current == "deleted" && status == "active" {
			return errAdminClientConflict
		}
		result := tx.Exec("UPDATE "+adminClientTable+" SET status=?,updated_at=? WHERE id=? AND status <> 'deleted'", status, s.now().UTC(), id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errAdminClientNotFound
		}
		if status != "active" {
			if err := tx.Exec("UPDATE "+adminClientCredentialTable+" SET status='revoked' WHERE client_id=?", id).Error; err != nil {
				return err
			}
		}
		return s.record(tx, adminClientLifecycleEvent{ClientID: id, Actor: actor, RequestID: requestID, Action: status})
	})
}

func (s *adminClientStore) update(ctx context.Context, id, name string, audit ...string) error {
	actor, requestID := adminClientAuditValues(audit)
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return errAdminClientInvalidRequest
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE "+adminClientTable+" SET name=?,updated_at=? WHERE id=? AND status='active'", strings.TrimSpace(name), s.now().UTC(), id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errAdminClientNotFound
		}
		return s.record(tx, adminClientLifecycleEvent{ClientID: id, Actor: actor, RequestID: requestID, Action: "updated"})
	})
}

func (s *adminClientStore) delete(ctx context.Context, id string, audit ...string) error {
	return s.setStatus(ctx, id, "deleted", audit...)
}

func (s *adminClientStore) revokeCredential(ctx context.Context, clientID, credentialID string, audit ...string) error {
	actor, requestID := adminClientAuditValues(audit)
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var subject string
		if err := tx.Raw("SELECT subject FROM "+adminClientCredentialTable+" WHERE client_id=? AND id=? AND status='active'", clientID, credentialID).Row().Scan(&subject); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAdminClientNotFound
			}
			return err
		}
		if err := tx.Exec("UPDATE "+adminClientCredentialTable+" SET status='revoked' WHERE client_id=? AND id=?", clientID, credentialID).Error; err != nil {
			return err
		}
		return s.record(tx, adminClientLifecycleEvent{ClientID: clientID, CredentialID: credentialID, Subject: subject, Actor: actor, RequestID: requestID, Action: "credential_revoked"})
	})
}

func (s *adminClientStore) warningCredentials(ctx context.Context, before time.Duration) ([]adminClientCredential, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("admin client store unavailable")
	}
	var rows []adminClientCredential
	cutoff := s.now().UTC().Add(before)
	err := s.db.WithContext(ctx).Raw("SELECT id,kind,identity,subject,expires_at,last_used_at,status FROM "+adminClientCredentialTable+" WHERE status='active' AND expires_at>? AND expires_at<=? ORDER BY expires_at LIMIT 1000", s.now().UTC(), cutoff).Scan(&rows).Error
	return rows, err
}

func (s *adminClientStore) emitDueExpiryWarnings(ctx context.Context, before time.Duration) error {
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	now := s.now().UTC()
	cutoff := now.Add(before)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			ID        string
			ClientID  string
			Kind      string
			Subject   string
			ExpiresAt time.Time
		}
		if err := tx.Raw("SELECT id,client_id,kind,subject,expires_at FROM "+adminClientCredentialTable+" WHERE status='active' AND warning_emitted_at IS NULL AND expires_at>? AND expires_at<=? ORDER BY expires_at LIMIT 1000", now, cutoff).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			result := tx.Exec("UPDATE "+adminClientCredentialTable+" SET warning_emitted_at=? WHERE id=? AND kind=? AND warning_emitted_at IS NULL", now, row.ID, row.Kind)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if err := s.record(tx, adminClientLifecycleEvent{ClientID: row.ClientID, CredentialID: row.ID, Subject: row.Subject, Actor: "system", RequestID: requestID(), Action: "expiry_warning"}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *adminClientStore) expiringCount(ctx context.Context, before time.Duration) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("admin client store unavailable")
	}
	now := s.now().UTC()
	var count int64
	err := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM "+adminClientCredentialTable+" WHERE status='active' AND expires_at>? AND expires_at<=?", now, now.Add(before)).Scan(&count).Error
	return count, err
}

func (s *adminClientStore) unusedCount(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("admin client store unavailable")
	}
	var count int64
	err := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM "+adminClientCredentialTable+" x JOIN "+adminClientTable+" c ON c.id=x.client_id WHERE x.status='active' AND c.status='active' AND x.expires_at>? AND x.last_used_at IS NULL", s.now().UTC()).Scan(&count).Error
	return count, err
}

func (s *adminClientStore) emitUnusedWarnings(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	now := s.now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct{ ID, ClientID, Kind, Subject string }
		if err := tx.Raw("SELECT x.id,x.client_id,x.kind,x.subject FROM "+adminClientCredentialTable+" x JOIN "+adminClientTable+" c ON c.id=x.client_id WHERE x.status='active' AND c.status='active' AND x.expires_at>? AND x.last_used_at IS NULL AND x.unused_warning_emitted_at IS NULL ORDER BY x.client_id,x.id LIMIT ?", now, adminClientMaxClients*adminClientMaxCredentials).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			res := tx.Exec("UPDATE "+adminClientCredentialTable+" SET unused_warning_emitted_at=? WHERE id=? AND kind=? AND unused_warning_emitted_at IS NULL", now, row.ID, row.Kind)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			if err := s.record(tx, adminClientLifecycleEvent{ClientID: row.ClientID, CredentialID: row.ID, Subject: row.Subject, Actor: "system", RequestID: requestID(), Action: "unused_warning"}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *adminClientStore) registerCredential(ctx context.Context, clientID string, credential adminClientCredential) error {
	return s.registerCredentialWithAudit(ctx, clientID, credential, "", "")
}

func (s *adminClientStore) registerCredentialWithAudit(ctx context.Context, clientID string, credential adminClientCredential, actor, requestID string) error {
	if s == nil || s.db == nil {
		return errors.New("admin client store unavailable")
	}
	if !validAdminClientID(clientID) || !validAdminClientCredential(credential, s.now()) {
		return errAdminClientInvalidRequest
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists int
		if err := tx.Raw("SELECT COUNT(*) FROM "+adminClientTable+" WHERE id=? AND status='active'", clientID).Scan(&exists).Error; err != nil {
			return err
		}
		if exists != 1 {
			return errAdminClientNotFound
		}
		var owner string
		if err := tx.Raw("SELECT x.client_id FROM "+adminClientCredentialTable+" x JOIN "+adminClientTable+" c ON c.id=x.client_id WHERE x.kind=? AND x.identity=? AND x.client_id<>? AND x.status='active' AND c.status='active' LIMIT 1", credential.Kind, credential.Identity, clientID).Row().Scan(&owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if owner != "" {
			return errAdminClientConflict
		}
		var count int64
		if err := tx.Raw("SELECT COUNT(*) FROM "+adminClientCredentialTable+" WHERE client_id=? AND status='active'", clientID).Scan(&count).Error; err != nil {
			return err
		}
		if count >= adminClientMaxCredentials {
			return errAdminClientConflict
		}
		if err := tx.Exec("INSERT INTO "+adminClientCredentialTable+" (id,client_id,kind,identity,subject,expires_at,status) VALUES (?,?,?,?,?,?,'active') ON CONFLICT(id,kind) DO UPDATE SET client_id=excluded.client_id,identity=excluded.identity,subject=excluded.subject,expires_at=excluded.expires_at,last_used_at=NULL,warning_emitted_at=NULL,status='active' WHERE client_id=excluded.client_id AND identity=excluded.identity", credential.ID, clientID, credential.Kind, credential.Identity, credential.Subject, credential.ExpiresAt.UTC()).Error; err != nil {
			return errAdminClientConflict
		}
		var active int64
		if err := tx.Raw("SELECT COUNT(*) FROM "+adminClientCredentialTable+" WHERE id=? AND kind=? AND client_id=? AND status='active'", credential.ID, credential.Kind, clientID).Scan(&active).Error; err != nil {
			return err
		}
		if active == 0 {
			return errAdminClientConflict
		}
		return s.record(tx, adminClientLifecycleEvent{ClientID: clientID, CredentialID: credential.ID, Subject: credential.Subject, Actor: actor, RequestID: requestID, Action: "credential_registered"})
	})
}

func (s *adminClientStore) authenticate(ctx context.Context, p Principal) (bool, error) {
	if s == nil || s.db == nil {
		return true, nil
	}
	if strings.TrimSpace(p.Subject) == "" {
		return false, nil
	}
	type match struct {
		ID               string
		ClientID         string
		ClientStatus     string
		CredentialStatus string
		Subject          string
		Identity         string
		ExpiresAt        time.Time
	}
	var rows []match
	query := "SELECT x.id,x.client_id,c.status AS client_status,x.status AS credential_status,x.subject,x.identity,x.expires_at FROM " + adminClientCredentialTable + " x JOIN " + adminClientTable + " c ON c.id=x.client_id WHERE x.kind=? AND x.subject=?"
	args := []any{p.Kind, p.Subject}
	if p.Kind == "mtls" {
		identities := adminPrincipalMTLSIdentities(p)
		if len(identities) == 0 {
			return false, nil
		}
		query += " AND x.identity IN (" + adminSQLPlaceholders(len(identities)) + ")"
		for _, identity := range identities {
			args = append(args, identity)
		}
	} else {
		query += " AND x.id=?"
		args = append(args, p.CredentialID)
	}
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 {
		// Preserve unmanaged identities for compatibility, but once any client
		// claims this policy subject, an unregistered credential cannot bypass its
		// disabled, deleted, revoked, or expired state by changing its credential ID.
		var managed int64
		managedQuery := "SELECT COUNT(*) FROM " + adminClientCredentialTable + " x WHERE x.subject=?"
		managedArgs := []any{p.Subject}
		if p.Kind == "mtls" {
			identities := adminPrincipalMTLSIdentities(p)
			managedQuery = "SELECT COUNT(*) FROM " + adminClientCredentialTable + " x WHERE (x.subject=?"
			managedArgs = []any{p.Subject}
			if len(identities) > 0 {
				managedQuery += " OR x.identity IN (" + adminSQLPlaceholders(len(identities)) + ")"
				for _, identity := range identities {
					managedArgs = append(managedArgs, identity)
				}
			}
			managedQuery += ")"
		} else {
			managedQuery = "SELECT COUNT(*) FROM " + adminClientCredentialTable + " x WHERE (x.subject=? OR (x.kind=? AND x.id=?))"
			managedArgs = []any{p.Subject, p.Kind, p.CredentialID}
		}
		if err := s.db.WithContext(ctx).Raw(managedQuery, managedArgs...).Scan(&managed).Error; err != nil {
			return false, err
		}
		return managed == 0, nil
	}
	now := s.now().UTC()
	valid := false
	for _, row := range rows {
		if row.Subject != p.Subject || row.ClientStatus != "active" || row.CredentialStatus != "active" || !row.ExpiresAt.After(now) {
			continue
		}
		valid = true
		break
	}
	if !valid {
		return false, nil
	}
	var result *gorm.DB
	if p.Kind == "mtls" {
		identities := adminPrincipalMTLSIdentities(p)
		args := []any{now, p.Kind}
		for _, identity := range identities {
			args = append(args, identity)
		}
		args = append(args, p.Subject, now)
		result = s.db.WithContext(ctx).Exec("UPDATE "+adminClientCredentialTable+" SET last_used_at=? WHERE kind=? AND identity IN ("+adminSQLPlaceholders(len(identities))+") AND subject=? AND status='active' AND expires_at>? AND EXISTS (SELECT 1 FROM "+adminClientTable+" c WHERE c.id="+adminClientCredentialTable+".client_id AND c.status='active')", args...)
	} else {
		result = s.db.WithContext(ctx).Exec("UPDATE "+adminClientCredentialTable+" SET last_used_at=? WHERE id=? AND kind=? AND subject=? AND status='active' AND expires_at>? AND EXISTS (SELECT 1 FROM "+adminClientTable+" c WHERE c.id="+adminClientCredentialTable+".client_id AND c.status='active')", now, p.CredentialID, p.Kind, p.Subject, now)
	}
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func adminSQLPlaceholders(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func validAdminClientID(id string) bool {
	if len(id) < 2 || len(id) > 64 || id == "access-review" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validAdminClientCredential(c adminClientCredential, now time.Time) bool {
	if !safeClientCredentialDescriptor(c.ID, 256) || !safeClientCredentialDescriptor(c.Identity, 512) || !safeClientCredentialDescriptor(c.Subject, 512) || !c.ExpiresAt.After(now) || c.ExpiresAt.After(now.Add(366*24*time.Hour)) {
		return false
	}
	switch c.Kind {
	case "adminkey", "mtls", "oauth", "federated":
	default:
		return false
	}
	return true
}

func safeClientCredentialDescriptor(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value || strings.HasPrefix(value, adminAPIKeyPrefix+"_") || strings.HasPrefix(value, "rtr_metrum_") {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func applyAdminClientSchema(tx *gorm.DB) error {
	if tx.Migrator().HasTable(adminCredentialEventsTable) && !tx.Migrator().HasColumn(adminCredentialEventsTable, "client_id") {
		if err := tx.Exec("ALTER TABLE " + adminCredentialEventsTable + " ADD COLUMN client_id TEXT NOT NULL DEFAULT ''").Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(adminCredentialEventsTable) && !tx.Migrator().HasColumn(adminCredentialEventsTable, "actor") {
		if err := tx.Exec("ALTER TABLE " + adminCredentialEventsTable + " ADD COLUMN actor TEXT NOT NULL DEFAULT ''").Error; err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS " + adminClientTable + " (id TEXT PRIMARY KEY,name TEXT NOT NULL,status TEXT NOT NULL,created_at TIMESTAMP NOT NULL,updated_at TIMESTAMP NOT NULL)",
		"CREATE TABLE IF NOT EXISTS " + adminClientCredentialTable + " (id TEXT NOT NULL,client_id TEXT NOT NULL,kind TEXT NOT NULL,identity TEXT NOT NULL,subject TEXT NOT NULL,expires_at TIMESTAMP NOT NULL,last_used_at TIMESTAMP NULL,warning_emitted_at TIMESTAMP NULL,unused_warning_emitted_at TIMESTAMP NULL,status TEXT NOT NULL,PRIMARY KEY (id,kind),FOREIGN KEY (client_id) REFERENCES " + adminClientTable + "(id))",
		"CREATE INDEX IF NOT EXISTS router_admin_client_credential_client ON " + adminClientCredentialTable + " (client_id,id)",
	} {
		if err := tx.Exec(stmt).Error; err != nil {
			return err
		}
	}
	if !tx.Migrator().HasColumn(adminClientCredentialTable, "unused_warning_emitted_at") {
		if err := tx.Exec("ALTER TABLE " + adminClientCredentialTable + " ADD COLUMN unused_warning_emitted_at TIMESTAMP NULL").Error; err != nil {
			return err
		}
	}
	return nil
}
