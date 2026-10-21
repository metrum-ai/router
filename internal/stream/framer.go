// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

var ErrLimit = errors.New("stream: size limit exceeded")
var ErrFrameTooLarge = errors.New("stream: line exceeds 64 KiB")

const maxFramerLineBytes = 64 << 10

// Framer retains at most one event. Limit bounds total upstream bytes; zero is unlimited.
// Individual SSE lines are bounded to 64 KiB including one normalized line ending.
type Framer struct {
	reader       *bufio.Reader
	skipLF       bool // A CR ended the previous line; swallow an optional LF lazily.
	Limit, Bytes int64
}

func NewFramer(r io.Reader, limit int64) *Framer {
	if limit > 0 {
		r = io.LimitReader(r, limit+1)
	}
	// Hide an existing *bufio.Reader: NewReaderSize otherwise reuses a larger
	// supplied buffer, allowing oversized lines when the total limit is zero.
	return &Framer{reader: bufio.NewReaderSize(struct{ io.Reader }{r}, maxFramerLineBytes), Limit: limit}
}
func (f *Framer) Next() (Event, error) {
	var e Event
	var data [][]byte
	for {
		line, err := f.nextLine()
		if errors.Is(err, ErrLimit) || errors.Is(err, ErrFrameTooLarge) {
			return Event{}, err
		}
		if err != nil {
			if len(line) > 0 || len(data) > 0 || e.Name != "" {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, err
		}
		if len(line) == 0 {
			if len(data) == 0 {
				e = Event{}
				continue
			}
			e.Data = bytes.Join(data, []byte{'\n'})
			return e, nil
		}
		key, value, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			value = nil
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(key) {
		case "event":
			e.Name = string(value)
		case "data":
			data = append(data, append([]byte{}, value...))
		}
	}
}

// nextLine recognizes all SSE line endings, including CRLF split across reads.
// Do not peek after CR: a bare-CR event must be deliverable before the upstream
// sends another byte. Every consumed byte, including a swallowed LF, counts
// against the total stream limit.
func (f *Framer) nextLine() ([]byte, error) {
	if f.Limit > 0 && f.Bytes > f.Limit {
		return nil, ErrLimit
	}
	var line []byte
	for {
		b, err := f.reader.ReadByte()
		if err != nil {
			return line, err
		}
		f.Bytes++
		if f.Limit > 0 && f.Bytes > f.Limit {
			return nil, ErrLimit
		}
		if f.skipLF {
			f.skipLF = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			f.skipLF = true
			return line, nil
		case '\n':
			return line, nil
		default:
			// Reserve one byte for the normalized terminator. CRLF counts as one
			// line ending, but both bytes still count against the stream budget.
			if len(line) >= maxFramerLineBytes-1 {
				return nil, ErrFrameTooLarge
			}
			line = append(line, b)
		}
	}
}

// Frame serializes a complete event without imposing a line-size limit.
func (e Event) Frame() []byte {
	var b bytes.Buffer
	writeField := func(key string, value []byte) {
		b.WriteString(key)
		b.WriteByte(':')
		// An optional formatting space must not push an accepted maximal line
		// over the parser cap. A leading value space still needs its escape space.
		if len(key)+len(value)+2 < maxFramerLineBytes || len(value) == 0 || value[0] == ' ' {
			b.WriteByte(' ')
		}
		b.Write(value)
		b.WriteByte('\n')
	}
	if e.Name != "" {
		writeField("event", []byte(e.Name))
	}
	for _, line := range bytes.Split(e.Data, []byte{'\n'}) {
		writeField("data", line)
	}
	b.WriteByte('\n')
	return b.Bytes()
}
