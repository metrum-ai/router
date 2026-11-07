// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestOptionsLogfRedactsArguments(t *testing.T) {
	const secret = "dns-provider-token-value"
	var buffer bytes.Buffer
	opts := Options{
		Logger: log.New(&buffer, "", 0),
		Redact: func(text string) string {
			return strings.ReplaceAll(text, secret, "<redacted>")
		},
	}
	// The credential is an argument, not part of the format string: the
	// formatted message must still be scrubbed before it is logged.
	opts.logf("dns: provider rejected token %s for zone %s", secret, "example.com")
	logged := buffer.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("logf leaked the credential: %q", logged)
	}
	if !strings.Contains(logged, "<redacted>") || !strings.Contains(logged, "example.com") {
		t.Fatalf("logf lost the message or the redaction label: %q", logged)
	}
}

func TestOptionsErrorfRedactsArgumentsAndIsNilSafe(t *testing.T) {
	const secret = "dns-provider-token-value"
	opts := Options{Redact: func(text string) string {
		return strings.ReplaceAll(text, secret, "<redacted>")
	}}
	err := opts.errorf("dns: rejected %s", secret)
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("errorf leaked the credential: %q", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("errorf did not redact: %q", err)
	}

	// A nil Options must not panic on either path.
	var nilOpts *Options
	if err := nilOpts.errorf("dns: plain %s", "message"); err == nil || err.Error() != "dns: plain message" {
		t.Fatalf("nil Options errorf = %v", err)
	}
	nilOpts.logf("dns: nothing to log")
	if opts.instruments() == nil {
		t.Fatal("instruments must never be nil")
	}
}
