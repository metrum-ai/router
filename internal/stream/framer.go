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
var ErrFrameTooLarge = errors.New("stream: line exceeds configured limit")
var ErrLineBufferBudget = errors.New("stream: line buffer budget exhausted")

type LineBufferBudget interface {
	TryAcquire(int64) bool
	Release(int64)
}

const maxFramerLineBytes = 64 << 10

// Framer retains at most one event. Limit bounds total upstream bytes; zero is unlimited.
// Individual SSE lines are bounded including one normalized line ending.
// The default is 64 KiB; the router supplies its validated server ceiling.
type Framer struct {
	maxLineBytes int
	reader       *bufio.Reader
	skipLF       bool // A CR ended the previous line; swallow an optional LF lazily.
	Limit, Bytes int64
	lineBudget   LineBufferBudget
}

func NewFramer(r io.Reader, limit int64, maxLineBytes ...int) *Framer {
	lineLimit := maxFramerLineBytes
	if len(maxLineBytes) > 0 && maxLineBytes[0] > 0 {
		lineLimit = maxLineBytes[0]
	}
	if limit > 0 {
		r = io.LimitReader(r, limit+1)
	}
	// Hide an existing *bufio.Reader: NewReaderSize otherwise reuses a larger
	// supplied buffer, allowing oversized lines when the total limit is zero.
	return &Framer{reader: bufio.NewReaderSize(struct{ io.Reader }{r}, 64<<10), Limit: limit, maxLineBytes: lineLimit}
}

func (f *Framer) SetLineBufferBudget(budget LineBufferBudget) { f.lineBudget = budget }
func (f *Framer) Next() (Event, error) {
	var e Event
	var data [][]byte
	for {
		line, reserved, err := f.nextLine()
		if errors.Is(err, ErrLimit) || errors.Is(err, ErrFrameTooLarge) || errors.Is(err, ErrLineBufferBudget) {
			return Event{}, err
		}
		if err != nil {
			f.releaseLine(reserved)
			if len(line) > 0 || len(data) > 0 || e.Name != "" {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, err
		}
		if len(line) == 0 {
			if len(data) == 0 {
				f.releaseLine(reserved)
				e = Event{}
				continue
			}
			e.Data = bytes.Join(data, []byte{'\n'})
			f.releaseLine(reserved)
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
		f.releaseLine(reserved)
	}
}

// nextLine recognizes all SSE line endings, including CRLF split across reads.
// Do not peek after CR: a bare-CR event must be deliverable before the upstream
// sends another byte. Every consumed byte, including a swallowed LF, counts
// against the total stream limit.
func (f *Framer) nextLine() ([]byte, int64, error) {
	if f.Limit > 0 && f.Bytes > f.Limit {
		return nil, 0, ErrLimit
	}
	var line []byte
	var reserved int64
	for {
		b, err := f.reader.ReadByte()
		if err != nil {
			return line, reserved, err
		}
		f.Bytes++
		if f.Limit > 0 && f.Bytes > f.Limit {
			f.releaseLine(reserved)
			return nil, 0, ErrLimit
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
			return line, reserved, nil
		case '\n':
			return line, reserved, nil
		default:
			// Reserve one byte for the normalized terminator. CRLF counts as one
			// line ending, but both bytes still count against the stream budget.
			if len(line) >= f.maxLineBytes-1 {
				f.releaseLine(reserved)
				return nil, 0, ErrFrameTooLarge
			}
			if len(line) == cap(line) {
				newCap := cap(line) * 2
				if newCap < 256 {
					newCap = 256
				}
				if newCap > f.maxLineBytes-1 {
					newCap = f.maxLineBytes - 1
				}
				if f.lineBudget != nil && !f.lineBudget.TryAcquire(int64(newCap)) {
					f.releaseLine(reserved)
					return nil, 0, ErrLineBufferBudget
				}
				grown := make([]byte, len(line), newCap)
				copy(grown, line)
				f.releaseLine(reserved)
				reserved = int64(newCap)
				line = grown
			}
			line = append(line, b)
		}
	}
}

func (f *Framer) releaseLine(reserved int64) {
	if f.lineBudget != nil && reserved > 0 {
		f.lineBudget.Release(reserved)
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
