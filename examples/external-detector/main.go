// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

func newHandler() http.Handler {
	return detector.Handler(newRuleDetector())
}

func main() {
	listen := flag.String("listen", ":8081", "HTTP listen address")
	certFile := flag.String("tls-cert", "", "TLS certificate PEM path; requires --tls-key")
	keyFile := flag.String("tls-key", "", "TLS private key PEM path; requires --tls-cert")
	authFailures := flag.Int("auth-failures", authFailureLimit, "authentication failures per key or source IP in one minute")
	tinyRequests := flag.Int("tiny-requests", tinyRequestLimit, "tiny requests per key in one minute")
	deniedGroups := flag.Int("denied-groups", deniedGroupLimit, "distinct denied groups per key in five minutes")
	spendMultiple := flag.Int("spend-multiple", spendPaceMultiple, "current-minute spend multiplier over observed baseline")
	flag.Parse()
	if (*certFile == "") != (*keyFile == "") {
		log.Fatal("--tls-cert and --tls-key must be set together")
	}
	if *authFailures < 1 || *tinyRequests < 1 || *deniedGroups < 1 || *spendMultiple < 1 {
		log.Fatal("rule threshold flags must be positive")
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           detector.Handler(newRuleDetectorWithThresholds(thresholds{authFailures: *authFailures, tinyRequests: *tinyRequests, deniedGroups: *deniedGroups, spendMultiple: *spendMultiple})),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if *certFile != "" {
		log.Fatal(server.ListenAndServeTLS(*certFile, *keyFile))
	}
	log.Fatal(server.ListenAndServe())
}
