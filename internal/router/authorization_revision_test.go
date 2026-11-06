// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"sync"
	"testing"
)

func TestAuthorizationSnapshotRevocation(t *testing.T) {
	allowed, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, subject, project/prod, metrics, read"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: allowed}
	subject := adminAuthSubject{subject: "subject", domain: "project/prod"}
	if !svc.authorizeAdmin(subject, "metrics", "read") {
		t.Fatal("initial grant denied")
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 100 {
				svc.authorizeAdmin(subject, "metrics", "read")
			}
		})
	}
	svc.authorizerSnapshot.Store(denied)
	readers.Wait()
	for range 100 {
		if svc.authorizeAdmin(subject, "metrics", "read") {
			t.Fatal("revoked grant survived snapshot publication")
		}
	}
}
