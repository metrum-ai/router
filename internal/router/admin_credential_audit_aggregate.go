// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	adminCredentialAuditCounterMigrationID = 2026111629
	adminCredentialAuditWindow             = 5 * time.Minute
	adminCredentialAuditMaxPending         = 4096
	adminCredentialAuditCounterTable       = "router_admin_credential_audit_counters"
)

var errAdminCredentialAuditCounterCapacity = errors.New("credential audit counter capacity reached")

func adminCredentialAuditCounterMigration() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: adminCredentialAuditCounterMigrationID, Scope: configControlPlaneScope, Name: "durable credential audit counters", Release: "2026.11", Checksum: MigrationChecksum("router-config.admin-credential-audit-counters.v1"), SchemaVersion: 21, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "router-config.credential-audit-counters.apply.v1@applyAdminCredentialAuditCounters", PostconditionKey: "router-config.credential-audit-counters.verify.v1@verifyAdminCredentialAuditCounters", Dependencies: []int{adminAuditChainControlMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyAdminCredentialAuditCounters, Verify: verifyAdminCredentialAuditCounters})
}

func adminCredentialAuditCounterUsageMigration() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: adminCredentialAuditCounterMigrationID, Scope: usageMigrationScope, Name: "durable credential audit counters", Release: "2026.11", Checksum: MigrationChecksum("usage.admin-credential-audit-counters.v1"), SchemaVersion: 25, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "usage.credential-audit-counters.apply.v1@applyAdminCredentialAuditCounters", PostconditionKey: "usage.credential-audit-counters.verify.v1@verifyAdminCredentialAuditCounters", Dependencies: []int{adminAuditChainUsageMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyAdminCredentialAuditCounters, Verify: verifyAdminCredentialAuditCounters})
}

func applyAdminCredentialAuditCounters(db *gorm.DB) error {
	if db == nil {
		return errors.New("control database required")
	}
	if err := db.AutoMigrate(&adminCredentialAuditCounter{}); err != nil {
		return err
	}
	return verifyAdminCredentialAuditCounters(db)
}
func verifyAdminCredentialAuditCounters(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(adminCredentialAuditCounterTable) {
		return errors.New("credential audit counter table missing")
	}
	return nil
}

type adminCredentialAuditCounter struct {
	BucketID     string    `gorm:"column:bucket_id;primaryKey"`
	ReplicaID    string    `gorm:"column:replica_id;not null;index"`
	WindowStart  time.Time `gorm:"column:window_start;not null;index"`
	Kind         string    `gorm:"column:kind"`
	CredentialID string    `gorm:"column:credential_id"`
	Subject      string    `gorm:"column:subject"`
	Issuer       string    `gorm:"column:issuer"`
	Outcome      string    `gorm:"column:outcome"`
	ReasonCode   string    `gorm:"column:reason_code"`
	ClientIPHash string    `gorm:"column:client_ip_hash"`
	Count        int64     `gorm:"column:count;not null"`
}

func (adminCredentialAuditCounter) TableName() string { return adminCredentialAuditCounterTable }

func adminCredentialAuditBucketID(replicaID string, event AdminCredentialEvent, start time.Time) string {
	data := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", replicaID, start.UTC().Format(time.RFC3339Nano), event.Kind, event.CredentialID, event.Subject, event.Issuer, event.Outcome, event.ReasonCode, event.ClientIPHash)
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// addAdminCredentialAuditCounter durably accepts an authentication outcome without
// taking the audit-chain lock. The unique window row is the crash-recovery journal.
func (s *Service) addAdminCredentialAuditCounter(ctx context.Context, event AdminCredentialEvent) error {
	db := s.adminCredentialAuditCounterDB()
	if db == nil {
		return errors.New("database required for aggregated credential audit")
	}
	replicaID, err := s.adminCredentialAuditReplicaIDValue()
	if err != nil {
		return fmt.Errorf("identify credential audit replica: %w", err)
	}
	start := event.At.UTC().Truncate(adminCredentialAuditWindow)
	row := adminCredentialAuditCounter{BucketID: adminCredentialAuditBucketID(replicaID, event, start), ReplicaID: replicaID, WindowStart: start, Kind: event.Kind, CredentialID: event.CredentialID, Subject: event.Subject, Issuer: event.Issuer, Outcome: event.Outcome, ReasonCode: event.ReasonCode, ClientIPHash: event.ClientIPHash, Count: 1}
	db = db.WithContext(ctx)
	return withSQLiteBusyRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var exists int64
			if err := tx.Table(adminCredentialAuditCounterTable).Where("bucket_id = ?", row.BucketID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				// Serialize only the new-bucket path in PostgreSQL. Existing bucket
				// upserts remain independent and lock only their own counter row.
				if tx.Dialector.Name() == "postgres" {
					if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", adminCredentialAuditCounterTable).Error; err != nil {
						return err
					}
					if err := tx.Table(adminCredentialAuditCounterTable).Where("bucket_id = ?", row.BucketID).Count(&exists).Error; err != nil {
						return err
					}
				}
				if exists == 0 {
					var n int64
					if err := tx.Table(adminCredentialAuditCounterTable).Count(&n).Error; err != nil {
						return err
					}
					if n >= adminCredentialAuditMaxPending {
						return errAdminCredentialAuditCounterCapacity
					}
				}
			}
			return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bucket_id"}}, DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr(adminCredentialAuditCounterTable + ".count + 1")})}).Create(&row).Error
		})
	})
}

func (s *Service) flushAdminCredentialAuditCounters(ctx context.Context, all bool, now time.Time) error {
	db := s.adminCredentialAuditCounterDB()
	if db == nil {
		return nil
	}
	db = db.WithContext(ctx)
	var ids []string
	q := db.Table(adminCredentialAuditCounterTable)
	if all {
		replicaID, err := s.adminCredentialAuditReplicaIDValue()
		if err != nil {
			return fmt.Errorf("identify credential audit replica for shutdown flush: %w", err)
		}
		q = q.Where("replica_id = ?", replicaID)
	} else {
		q = q.Where("window_start <= ?", now.UTC().Add(-adminCredentialAuditWindow))
	}
	q = q.Order("window_start ASC").Limit(adminCredentialAuditMaxPending).Pluck("bucket_id", &ids)
	if q.Error != nil {
		return q.Error
	}
	for _, id := range ids {
		err := withSQLiteBusyRetry(func() error {
			return db.Transaction(func(tx *gorm.DB) error {
				var row adminCredentialAuditCounter
				res := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bucket_id = ?", id).First(&row)
				if errors.Is(res.Error, gorm.ErrRecordNotFound) {
					return nil
				}
				if res.Error != nil {
					return res.Error
				}
				if !all && now.UTC().Before(row.WindowStart.Add(adminCredentialAuditWindow)) {
					return nil
				}
				event := AdminCredentialEvent{At: row.WindowStart.Add(adminCredentialAuditWindow), Kind: row.Kind, CredentialID: row.CredentialID, Subject: row.Subject, Issuer: row.Issuer, Outcome: row.Outcome, ReasonCode: row.ReasonCode, ClientIPHash: row.ClientIPHash, Lifecycle: "auth", Count: row.Count}
				if s.configControlPlane != nil {
					if err := insertAdminCredentialEventTx(tx, event); err != nil {
						return err
					}
				} else if err := insertAdminAuditUsageTx(tx, AdminAuditRecord{
					At: event.At, PrincipalSubject: event.Subject, PrincipalKind: event.Kind,
					CredentialID: event.CredentialID, Issuer: event.Issuer, Domain: AdminGlobalDomain,
					Outcome: event.Outcome, ReasonCode: event.ReasonCode, OperationID: "authentication",
					Action: event.Lifecycle, Detail: fmt.Sprintf("authentication_count=%d", event.Count),
				}); err != nil {
					return err
				}
				return tx.Delete(&row).Error
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) adminCredentialAuditReplicaIDValue() (string, error) {
	if s == nil {
		return "", errors.New("service is nil")
	}
	s.adminCredentialAuditReplicaIDMu.Lock()
	defer s.adminCredentialAuditReplicaIDMu.Unlock()
	if s.adminCredentialAuditReplicaID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		s.adminCredentialAuditReplicaID = hex.EncodeToString(raw[:])
	}
	return s.adminCredentialAuditReplicaID, nil
}

func (s *Service) runAdminCredentialAuditCounterFlusher() {
	db := s.adminCredentialAuditCounterDB()
	if db == nil {
		return
	}
	if !db.Migrator().HasTable(adminCredentialAuditCounterTable) {
		return
	}
	s.adminCredentialAuditFlushMu.Lock()
	if s.adminCredentialAuditFlushStop != nil {
		s.adminCredentialAuditFlushMu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	s.adminCredentialAuditFlushStop, s.adminCredentialAuditFlushDone = stop, done
	s.adminCredentialAuditFlushMu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.flushAdminCredentialAuditCounters(context.Background(), false, time.Now().UTC())
			case <-stop:
				return
			}
		}
	}()
}

func (s *Service) closeAdminCredentialAuditCounters() {
	if s == nil {
		return
	}
	s.adminCredentialAuditFlushMu.Lock()
	stop, done := s.adminCredentialAuditFlushStop, s.adminCredentialAuditFlushDone
	s.adminCredentialAuditFlushStop = nil
	s.adminCredentialAuditFlushDone = nil
	if stop != nil {
		close(stop)
	}
	s.adminCredentialAuditFlushMu.Unlock()
	if done != nil {
		<-done
	}
	if db := s.adminCredentialAuditCounterDB(); db != nil && db.Migrator().HasTable(adminCredentialAuditCounterTable) {
		if err := s.flushAdminCredentialAuditCounters(context.Background(), true, time.Now().UTC()); err != nil {
			fmt.Printf("credential audit counter shutdown flush failed: %v\n", err)
		}
	}
}

func (s *Service) adminCredentialAuditCounterDB() *gorm.DB {
	if s == nil {
		return nil
	}
	if s.configControlPlane != nil {
		return s.configControlPlane
	}
	if s.usage != nil {
		return s.usage.db
	}
	return nil
}
