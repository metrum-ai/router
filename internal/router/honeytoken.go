// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"hash/crc32"
	"time"
)

const honeytokenCompletionText = "I can help with that."

func honeytokenSinkholeProfile(requestID string) (string, Usage) {
	variation := crc32.ChecksumIEEE([]byte(requestID)) % 4
	texts := [...]string{
		honeytokenCompletionText,
		honeytokenCompletionText + " Let's work through it.",
		honeytokenCompletionText + " Here's a useful starting point.",
		honeytokenCompletionText + " What would you like to focus on?",
	}
	time.Sleep(time.Duration(variation) * time.Millisecond)
	input := int(1 + variation%2)
	output := int(1 + variation%3)
	return texts[variation], Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
}

// HoneytokenStatus contains planted metadata and last-use time, never a key or verifier.
type HoneytokenStatus struct {
	CallerID  string `json:"caller_id"`
	Label     string `json:"label"`
	PlantedAt string `json:"planted_at,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// HoneytokenReport returns planted decoys and their most recent observed use.
func HoneytokenReport(cfg *Config) ([]HoneytokenStatus, error) {
	if cfg == nil {
		return nil, errors.New("honeytoken report requires config")
	}
	copy := *cfg
	copy.setDefaults()
	store, err := OpenUsageStore(copy.Server.UsageDB)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	lastSeen, err := store.honeytokenLastSeen()
	if err != nil {
		return nil, err
	}
	rows := make([]HoneytokenStatus, 0)
	for _, caller := range cfg.Callers {
		if caller.Honeytoken == nil {
			continue
		}
		rows = append(rows, HoneytokenStatus{
			CallerID:  caller.ID,
			Label:     caller.Honeytoken.Label,
			PlantedAt: caller.Honeytoken.PlantedAt,
			LastSeen:  lastSeen[caller.ID],
		})
	}
	return rows, nil
}
