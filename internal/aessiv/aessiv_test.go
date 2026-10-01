// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package aessiv

import (
	"bytes"
	"encoding/hex"
	"sync"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, 64)
	aead, err := NewCMAC(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range [][]byte{
		nil,
		{},
		[]byte("x"),
		bytes.Repeat([]byte("a"), 17),
		bytes.Repeat([]byte("b"), 64),
		[]byte("日本語"),
	} {
		ad := []byte("mr_A")
		ct := aead.Seal(nil, nil, plaintext, ad)
		got, err := aead.Open(nil, nil, ct, ad)
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatalf("roundtrip len=%d: err=%v", len(plaintext), err)
		}
	}
}

func TestRFC5297AppendixA1(t *testing.T) {
	// RFC 5297 Appendix A.1 — deterministic SIV with AD, empty nonce.
	key, err := hex.DecodeString("fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0" +
		"f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff")
	if err != nil {
		t.Fatal(err)
	}
	ad, err := hex.DecodeString("101112131415161718191a1b1c1d1e1f" +
		"2021222324252627")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := hex.DecodeString("112233445566778899aabbccddee")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString("85632d07c6e8f37f950acd320a2ecc93" +
		"40c02b9690c4dc04daef7f6afe5c")
	if err != nil {
		t.Fatal(err)
	}
	aead, err := NewCMAC(key)
	if err != nil {
		t.Fatal(err)
	}
	got := aead.Seal(nil, nil, plaintext, ad)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
	out, err := aead.Open(nil, nil, got, ad)
	if err != nil || !bytes.Equal(out, plaintext) {
		t.Fatalf("open: %v", err)
	}
}

func TestConcurrentSerialized(t *testing.T) {
	key := bytes.Repeat([]byte{0xcd}, 64)
	aead, err := NewCMAC(key)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := bytes.Repeat([]byte{byte('A' + w%26)}, 17)
			ad := []byte("mr_Z")
			for i := 0; i < 2000; i++ {
				mu.Lock()
				ct := aead.Seal(nil, nil, id, ad)
				got, err := aead.Open(nil, nil, ct, ad)
				mu.Unlock()
				if err != nil || !bytes.Equal(got, id) {
					t.Errorf("worker %d: %v", w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
