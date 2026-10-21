// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"testing"
)

func FuzzSSEFrame(f *testing.F) {
	f.Add([]byte("data: hello\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		fr := NewFramer(bytes.NewReader(data), 1<<20)
		for {
			event, err := fr.Next()
			if err != nil {
				return
			}
			// Every accepted event must survive serialization.
			parsed, err := NewFramer(bytes.NewReader(event.Frame()), 0).Next()
			if err != nil || parsed.Name != event.Name || !bytes.Equal(parsed.Data, event.Data) {
				t.Fatalf("frame round trip: %v", err)
			}
		}
	})
}
