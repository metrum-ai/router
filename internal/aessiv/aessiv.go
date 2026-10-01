// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
//
// AES-SIV-CMAC (RFC 5297) adapted from the pure-Go path in
// github.com/secure-io/siv-go (MIT License, Copyright 2018 SecureIO /
// Andreas Auernhammer). The upstream amd64 assembly AEAD is not used:
// it SIGSEGVs under concurrent router load on modern Go toolchains.

// Package aessiv implements AES-SIV-CMAC as a cipher.AEAD.
package aessiv

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"errors"
	"hash"

	cmac "github.com/aead/cmac/aes"
)

var errOpen = errors.New("aessiv: message authentication failed")

// NewCMAC returns a cipher.AEAD implementing AES-SIV-CMAC (RFC 5297).
// key must be 32, 48, or 64 bytes (two AES keys concatenated).
// The AEAD accepts a nil/empty nonce or a NonceSize()-byte nonce.
// Seal/Open on one AEAD are not safe for concurrent use; callers must serialize.
func NewCMAC(key []byte) (cipher.AEAD, error) {
	if k := len(key); k != 32 && k != 48 && k != 64 {
		return nil, aes.KeySizeError(k)
	}
	mac, err := cmac.New(key[:len(key)/2])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key[len(key)/2:])
	if err != nil {
		return nil, err
	}
	return &aesSIV{mac: mac, block: block}, nil
}

type aesSIV struct {
	mac   hash.Hash
	block cipher.Block
}

func (c *aesSIV) NonceSize() int { return aes.BlockSize }
func (c *aesSIV) Overhead() int  { return aes.BlockSize }

func (c *aesSIV) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	if n := len(nonce); n != 0 && n != c.NonceSize() {
		panic("aessiv: incorrect nonce length given to AES-SIV-CMAC")
	}
	ret, ciphertext := sliceForAppend(dst, c.Overhead()+len(plaintext))
	v := s2v(additionalData, nonce, plaintext, c.mac)
	copy(ciphertext, v[:])
	iv := newIV(v)
	cipher.NewCTR(c.block, iv[:]).XORKeyStream(ciphertext[len(v):], plaintext)
	return ret
}

func (c *aesSIV) Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	if n := len(nonce); n != 0 && n != c.NonceSize() {
		panic("aessiv: incorrect nonce length given to AES-SIV-CMAC")
	}
	if len(ciphertext) < c.Overhead() {
		return dst, errOpen
	}
	ret, plaintext := sliceForAppend(dst, len(ciphertext)-c.Overhead())
	var tag [16]byte
	copy(tag[:], ciphertext[:16])
	body := ciphertext[16:]
	iv := newIV(tag)
	cipher.NewCTR(c.block, iv[:]).XORKeyStream(plaintext, body)
	v := s2v(additionalData, nonce, plaintext, c.mac)
	if subtle.ConstantTimeCompare(v[:], tag[:]) != 1 {
		for i := range plaintext {
			plaintext[i] = 0
		}
		return ret, errOpen
	}
	return ret, nil
}

func s2v(additionalData, nonce, plaintext []byte, mac hash.Hash) [16]byte {
	var b0, b1 [16]byte
	mac.Write(b0[:])
	mac.Sum(b1[:0])
	mac.Reset()

	if len(additionalData) > 0 || len(nonce) > 0 {
		mac.Write(additionalData)
		mac.Sum(b0[:0])
		mac.Reset()

		dbl(&b1)
		for i := range b1 {
			b1[i] ^= b0[i]
		}
		if len(nonce) > 0 {
			mac.Write(nonce)
			mac.Sum(b0[:0])
			mac.Reset()

			dbl(&b1)
			for i := range b1 {
				b1[i] ^= b0[i]
			}
		}
		for i := range b0 {
			b0[i] = 0
		}
	}

	if len(plaintext) >= 16 {
		n := len(plaintext) - 16
		copy(b0[:], plaintext[n:])
		mac.Write(plaintext[:n])
	} else {
		copy(b0[:], plaintext)
		b0[len(plaintext)] = 0x80
		dbl(&b1)
	}

	for i := range b0 {
		b0[i] ^= b1[i]
	}
	mac.Write(b0[:])
	mac.Sum(b0[:0])
	mac.Reset()
	return b0
}

func newIV(v [16]byte) [16]byte {
	v[8] &= 0x7f
	v[12] &= 0x7f
	return v
}

func dbl(b *[16]byte) {
	var z byte
	for i := 15; i >= 0; i-- {
		zz := b[i] >> 7
		b[i] = b[i]<<1 | z
		z = zz
	}
	b[15] ^= byte(subtle.ConstantTimeSelect(int(z), 0x87, 0))
}

func sliceForAppend(in []byte, n int) (head, tail []byte) {
	if n < 0 {
		panic("aessiv: negative append size")
	}
	if len(in) > int(^uint(0)>>1)-n {
		panic("aessiv: append size overflow")
	}
	total := len(in) + n
	if cap(in) >= total {
		head = in[:total]
	} else {
		head = make([]byte, total)
		copy(head, in)
	}
	tail = head[len(in):]
	return
}
