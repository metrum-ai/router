// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log"
	"net/http"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

func main() {
	server := &http.Server{
		Addr:              ":8081",
		Handler:           detector.Handler(newRuleDetector()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}
