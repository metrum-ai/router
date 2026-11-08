// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"gorm.io/gorm"
)

var errAdminEmergencyOutput = errors.New("emergency output unavailable")

// Controller for the local routerctl entrypoint. Configuration/enrollment and
// database access are trusted local dependencies, never an HTTP request. The
// production entrypoint must supply #816's shared redactor/transaction adapters.
// It must not expose this controller through an admin route.
type adminEmergencyOfflineCommand struct {
	store       *adminEmergencyActivationStore
	lookup      func(string) (adminEmergencyCredential, bool)
	activated   func(*gorm.DB, adminEmergencyActivation) error
	deactivated func(*gorm.DB, adminEmergencyDeactivation) error
}

func newAdminEmergencyOfflineCommand(store *adminEmergencyActivationStore, lookup func(string) (adminEmergencyCredential, bool), activated func(*gorm.DB, adminEmergencyActivation) error, deactivated func(*gorm.DB, adminEmergencyDeactivation) error) (*adminEmergencyOfflineCommand, error) {
	if store == nil || lookup == nil || activated == nil || deactivated == nil {
		return nil, errAdminEmergencyInvalid
	}
	return &adminEmergencyOfflineCommand{store: store, lookup: lookup, activated: activated, deactivated: deactivated}, nil
}
func (c *adminEmergencyOfflineCommand) Run(ctx context.Context, args []string, out io.Writer) error {
	if c == nil || ctx == nil || ctx.Err() != nil || out == nil || len(args) == 0 {
		return errAdminEmergencyInvalid
	}
	action := args[0]
	if action != "activate" && action != "deactivate" {
		return errAdminEmergencyInvalid
	}
	fs := flag.NewFlagSet("emergency "+action, flag.ContinueOnError)
	// Parsing errors must never echo supplied arguments, which may accidentally
	// contain credentials. Only metadata is accepted; there is no --url/token.
	fs.SetOutput(io.Discard)
	id := fs.String("credential-id", "", "enrolled credential ID")
	reason := fs.String("reason", "", "bounded activation/deactivation reason")
	until := fs.String("until", "", "absolute activation deadline (RFC3339)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || len(*id) == 0 || len(*id) > 128 || len(*reason) > 512 || strings.TrimSpace(*reason) == "" {
		return errAdminEmergencyInvalid
	}
	for _, v := range *id {
		if !(v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || v == '-' || v == '_' || v == '.') {
			return errAdminEmergencyInvalid
		}
	}
	var result any
	switch action {
	case "activate":
		deadline, err := time.Parse(time.RFC3339Nano, *until)
		if err != nil {
			return errAdminEmergencyInvalid
		}
		credential, ok := c.lookup(*id)
		if !ok || credential.ID != *id {
			return errAdminEmergencyInvalid
		}
		active, err := c.store.Activate(ctx, credential, *reason, deadline, c.activated)
		if err != nil {
			return err
		}
		result = active
	case "deactivate":
		if *until != "" {
			return errAdminEmergencyInvalid
		}
		// No current enrollment lookup: removed/expired credentials still need an
		// explicit durable close. Deactivate preserves the original history row.
		stopped, err := c.store.Deactivate(ctx, *id, *reason, c.deactivated)
		if err != nil {
			return err
		}
		result = stopped
	}
	// State and event are already committed. An output failure is not an audit
	// failure and cannot roll the transaction back; report it without raw detail.
	if json.NewEncoder(out).Encode(result) != nil {
		return errAdminEmergencyOutput
	}
	return nil
}
