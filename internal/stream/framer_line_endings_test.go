// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestFramerLineLimitAcrossSSEEndings(t *testing.T) {
	for _, ending := range []string{"\n", "\r", "\r\n"} {
		for _, prefix := range []string{"data:", "data: ", "data:  ", "event:", "event: ", "event:  "} {
			value := strings.Repeat("x", maxFramerLineBytes-len(prefix)-1)
			wire := prefix + value + ending
			if strings.HasPrefix(prefix, "event:") {
				wire += "data: x" + ending
			}
			wire += ending
			event, err := NewFramer(iotest.OneByteReader(strings.NewReader(wire)), 0).Next()
			if err != nil {
				t.Fatalf("prefix=%q ending=%q: %v", prefix, ending, err)
			}
			key, _, _ := strings.Cut(prefix, ":")
			want := strings.TrimPrefix(prefix[len(key)+1:]+value, " ")
			if key == "event" {
				if event.Name != want || string(event.Data) != "x" {
					t.Fatal("maximal event field changed")
				}
			} else if string(event.Data) != want {
				t.Fatal("maximal data field changed")
			}
			parsed, err := NewFramer(bytes.NewReader(event.Frame()), 0).Next()
			if err != nil || parsed.Name != event.Name || !bytes.Equal(parsed.Data, event.Data) {
				t.Fatalf("maximal line round trip prefix=%q ending=%q: %v", prefix, ending, err)
			}
			oversized := prefix + value + "x" + ending + ending
			if _, err := NewFramer(strings.NewReader(oversized), 0).Next(); !errors.Is(err, ErrFrameTooLarge) {
				t.Fatalf("oversized prefix=%q ending=%q: %v", prefix, ending, err)
			}
		}
	}
}

func TestFramerSSELineEndings(t *testing.T) {
	for _, ending := range []string{"\n", "\r", "\r\n"} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(ending, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
			wire := strings.Join([]string{": comment", "event: test", "data: first", "data: second", "", "data: last", "", ""}, ending)
			fr := NewFramer(iotest.OneByteReader(strings.NewReader(wire)), 0)
			event, err := fr.Next()
			if err != nil || event.Name != "test" || string(event.Data) != "first\nsecond" {
				t.Fatalf("first: %+v %v", event, err)
			}
			event, err = fr.Next()
			if err != nil || event.Name != "" || string(event.Data) != "last" {
				t.Fatalf("second: %+v %v", event, err)
			}
			if _, err = fr.Next(); !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			if fr.Bytes != int64(len(wire)) {
				t.Fatalf("bytes=%d want=%d", fr.Bytes, len(wire))
			}
		})
	}
}

func TestFramerBareCRRegression(t *testing.T) {
	for _, wire := range []string{"data: x\r\r\n\n", "event: name\rdata: x\r\r", "event: name\r\ndata: x\rdata: y\n\r\n"} {
		event, err := NewFramer(iotest.OneByteReader(strings.NewReader(wire)), 0).Next()
		if err != nil || bytes.ContainsRune(event.Data, '\r') || strings.ContainsRune(event.Name, '\r') {
			t.Fatalf("%q: %+v %v", wire, event, err)
		}
		parsed, err := NewFramer(bytes.NewReader(event.Frame()), 0).Next()
		if err != nil || parsed.Name != event.Name || !bytes.Equal(parsed.Data, event.Data) {
			t.Fatalf("%q: %+v -> %+v %v", wire, event, parsed, err)
		}
		if wire == "data: x\r\r\n\n" && string(event.Data) != "x" {
			t.Fatalf("regression data=%q", event.Data)
		}
	}
}

type framerReadFunc func([]byte) (int, error)

func (f framerReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestFramerBareCRDoesNotReadAhead(t *testing.T) {
	reads := 0
	fr := NewFramer(framerReadFunc(func(p []byte) (int, error) {
		reads++
		if reads > 1 {
			return 0, errors.New("read beyond completed bare-CR event")
		}
		return copy(p, "data: x\r\r"), nil
	}), 0)
	event, err := fr.Next()
	if err != nil || string(event.Data) != "x" || reads != 1 {
		t.Fatalf("event=%+v err=%v reads=%d", event, err, reads)
	}
}

func TestFramerLineEndingByteLimit(t *testing.T) {
	const wire = "data: x\r\n\r\n"
	for _, limit := range []int64{int64(len(wire)), int64(len(wire) - 1)} {
		fr := NewFramer(iotest.OneByteReader(strings.NewReader(wire)), limit)
		if _, err := fr.Next(); err != nil {
			t.Fatal(err)
		}
		_, err := fr.Next() // includes the optional LF after the CR that dispatched the event
		want := io.EOF
		if limit < int64(len(wire)) {
			want = ErrLimit
		}
		if !errors.Is(err, want) {
			t.Fatalf("limit=%d bytes=%d err=%v want=%v", limit, fr.Bytes, err, want)
		}
		if _, err := fr.Next(); !errors.Is(err, want) {
			t.Fatalf("terminal error changed: %v want %v", err, want)
		}
	}
	for _, wire := range []string{"data: x", "data: x\r", "data: x\r\n"} {
		if _, err := NewFramer(strings.NewReader(wire), 0).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("%q: %v", wire, err)
		}
	}
}
