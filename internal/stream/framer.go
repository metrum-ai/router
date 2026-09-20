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

// Framer retains at most one event. Limit bounds total upstream bytes; zero is unlimited.
type Framer struct {
	reader       *bufio.Reader
	Limit, Bytes int64
}

func NewFramer(r io.Reader, limit int64) *Framer {
	if limit > 0 {
		r = io.LimitReader(r, limit+1)
	}
	return &Framer{reader: bufio.NewReader(r), Limit: limit}
}
func (f *Framer) Next() (Event, error) {
	var e Event
	var data [][]byte
	for {
		line, err := f.reader.ReadBytes('\n')
		f.Bytes += int64(len(line))
		if f.Limit > 0 && f.Bytes > f.Limit {
			return Event{}, ErrLimit
		}
		if err != nil {
			if len(line) > 0 || len(data) > 0 || e.Name != "" {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, err
		}
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
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

// Frame serializes a complete event without imposing a line-size limit.
func (e Event) Frame() []byte {
	var b bytes.Buffer
	if e.Name != "" {
		b.WriteString("event: ")
		b.WriteString(e.Name)
		b.WriteByte('\n')
	}
	for _, line := range bytes.Split(e.Data, []byte{'\n'}) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.Bytes()
}
