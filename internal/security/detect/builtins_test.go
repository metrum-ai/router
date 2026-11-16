// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package detect

import (
	"context"
	"strings"
	"testing"

	"github.com/metrum-ai/router/pkg/detector"
)

func TestBuiltinsSignalOnlyForTheirEvents(t *testing.T) {
	keyID := strings.Repeat("a", 32)
	for _, test := range []struct {
		name           string
		implementation detector.Detector
		kind           string
		want           string
	}{
		{"honeytoken", HoneytokenDetector{}, EventHoneytokenUsed, "honeytoken"},
		{"spend breaker", SpendBreakerDetector{}, EventSpendBreakerTripped, "spend_breaker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			signals, err := test.implementation.Detect(context.Background(), detector.Event{SchemaVersion: detector.SchemaVersion, Kind: test.kind, KeyID: keyID})
			if err != nil || len(signals) != 1 || signals[0].DetectorID != test.want {
				t.Fatalf("signals=%+v err=%v", signals, err)
			}
			if err := detector.ValidateSignal(signals[0]); err != nil {
				t.Fatal(err)
			}
			empty, err := test.implementation.Detect(context.Background(), detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "benign"})
			if err != nil || len(empty) != 0 {
				t.Fatalf("benign signals=%v err=%v", empty, err)
			}
		})
	}
}
