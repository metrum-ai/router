// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log"
	"net/http"

	"github.com/metrum-ai/router/pkg/detector"
)

type exampleDetector struct{}

func (exampleDetector) Detect(_ context.Context, event detector.Event) ([]detector.Signal, error) {
	if event.Kind != "example.match" {
		return nil, nil
	}
	return []detector.Signal{{
		SchemaVersion:      detector.SchemaVersion,
		DetectorID:         "example",
		Severity:           "medium",
		Score:              0.75,
		Key:                event.KeyPrefix,
		EvidenceRequestIDs: []string{event.RequestID},
	}}, nil
}

func main() {
	server := &http.Server{Addr: ":8081", Handler: detector.Handler(exampleDetector{})}
	log.Fatal(server.ListenAndServe())
}
