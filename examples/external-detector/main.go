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
	flag.Parse()
	if (*certFile == "") != (*keyFile == "") {
		log.Fatal("--tls-cert and --tls-key must be set together")
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           newHandler(),
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
