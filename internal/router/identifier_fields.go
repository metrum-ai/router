// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// Nodes retain source offsets so replacing an ID never reserializes a content delta.
type idJSONNode struct {
	start, end int
	text       string
	fields     map[string]*idJSONNode
	items      []*idJSONNode
}

func parseIDJSON(raw []byte) (*idJSONNode, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var parse func() (*idJSONNode, error)
	parse = func() (*idJSONNode, error) {
		start := int(d.InputOffset())
		for start < len(raw) && (raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\r' || raw[start] == '\t' || raw[start] == ':' || raw[start] == ',') {
			start++
		}
		token, e := d.Token()
		if e != nil {
			return nil, e
		}
		n := &idJSONNode{start: start}
		switch v := token.(type) {
		case string:
			n.text = v
		case json.Delim:
			if v == '{' {
				n.fields = map[string]*idJSONNode{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, e
					}
					child, e := parse()
					if e != nil {
						return nil, e
					}
					n.fields[k.(string)] = child
				}
			} else if v == '[' {
				for d.More() {
					child, e := parse()
					if e != nil {
						return nil, e
					}
					n.items = append(n.items, child)
				}
			}
			if _, e = d.Token(); e != nil {
				return nil, e
			}
		}
		n.end = int(d.InputOffset())
		return n, nil
	}
	n, e := parse()
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		if e == nil {
			e = io.ErrUnexpectedEOF
		}
		return nil, e
	}
	return n, nil
}
func (n *idJSONNode) field(path ...string) *idJSONNode {
	for _, p := range path {
		if n == nil {
			return nil
		}
		n = n.fields[p]
	}
	return n
}
func (n *idJSONNode) value() string {
	if n == nil {
		return ""
	}
	return n.text
}

type idReplacement struct {
	start, end int
	value      []byte
}

func applyIDReplacements(raw []byte, changes []idReplacement) []byte {
	sort.Slice(changes, func(i, j int) bool { return changes[i].start < changes[j].start })
	var out []byte
	last := 0
	for _, c := range changes {
		out = append(out, raw[last:c.start]...)
		out = append(out, c.value...)
		last = c.end
	}
	return append(out, raw[last:]...)
}
func decodeIdentifierFields(raw []byte, t IdentifierTransform) ([]byte, error) {
	if t == nil {
		return raw, nil
	}
	root, e := parseIDJSON(raw)
	if e != nil {
		return raw, nil
	} // Ordinary request validation handles malformed JSON.
	var changes []idReplacement
	var walk func(*idJSONNode) error
	walk = func(n *idJSONNode) error {
		for k, v := range n.fields {
			switch k {
			case "tool_call_id", "tool_use_id", "call_id", "previous_response_id":
				if strings.HasPrefix(v.text, "mr_") {
					id, e := t.Decode(v.text)
					if e != nil {
						return e
					}
					b, _ := json.Marshal(id)
					changes = append(changes, idReplacement{v.start, v.end, b})
				}
			}
			// Recurse only through protocol containers, never tool arguments,
			// metadata, schemas, or arbitrary user-provided JSON objects.
			if k == "messages" || k == "content" || k == "tool_calls" || (k == "input" && n == root) {
				if v.items != nil {
					if e := walk(v); e != nil {
						return e
					}
				}
			}
		}
		for _, v := range n.items {
			if e := walk(v); e != nil {
				return e
			}
		}
		return nil
	}
	if e = walk(root); e != nil {
		return nil, e
	}
	if len(changes) == 0 {
		return raw, nil
	}
	return applyIDReplacements(raw, changes), nil
}

type nativeIDRewriter struct {
	transform     IdentifierTransform
	chatSeen      map[string]bool
	functionItems map[string]bool
}

func (r *nativeIDRewriter) rewrite(frame []byte, dialect string) []byte {
	if r.transform == nil {
		return frame
	}
	// Keep an offset map for multiline SSE data, preserving all event framing.
	var data []byte
	var offsets []int
	for pos := 0; pos < len(frame); {
		end := bytes.IndexByte(frame[pos:], '\n')
		if end < 0 {
			end = len(frame)
		} else {
			end += pos
		}
		lineEnd := end
		if lineEnd > pos && frame[lineEnd-1] == '\r' {
			lineEnd--
		}
		if bytes.HasPrefix(frame[pos:lineEnd], []byte("data:")) {
			start := pos + 5
			if start < lineEnd && frame[start] == ' ' {
				start++
			}
			if len(data) > 0 {
				data = append(data, '\n')
				offsets = append(offsets, -1)
			}
			for i := start; i < lineEnd; i++ {
				data = append(data, frame[i])
				offsets = append(offsets, i)
			}
		}
		pos = end + 1
	}
	root, e := parseIDJSON(data)
	if e != nil {
		return frame
	}
	var changes []idReplacement
	add := func(n *idJSONNode) {
		if n == nil || n.text == "" {
			return
		}
		b, _ := json.Marshal(r.transform.Encode(n.text))
		changes = append(changes, idReplacement{offsets[n.start], offsets[n.end-1] + 1, b})
	}
	typ := root.field("type").value()
	switch normalizeDialect(dialect) {
	case "anthropic":
		if typ == "message_start" {
			add(root.field("message", "id"))
		}
		if typ == "content_block_start" && root.field("content_block", "type").value() == "tool_use" {
			add(root.field("content_block", "id"))
		}
	case "openai-responses":
		switch typ {
		case "response.created", "response.in_progress", "response.completed":
			add(root.field("response", "id"))
		}
		item := root.field("item")
		if item.field("type").value() == "function_call" {
			if r.functionItems == nil {
				r.functionItems = map[string]bool{}
			}
			r.functionItems[item.field("id").value()] = true
			add(item.field("id"))
			add(item.field("call_id"))
		}
		if strings.HasPrefix(typ, "response.function_call_arguments.") || r.functionItems[root.field("item_id").value()] {
			add(root.field("item_id"))
		}
		if response := root.field("response", "output"); response != nil {
			for _, item := range response.items {
				if item.field("type").value() == "function_call" {
					add(item.field("id"))
					add(item.field("call_id"))
				}
			}
		}
	case "openai-chat":
		add(root.field("id"))
		if choices := root.field("choices"); choices != nil {
			for _, choice := range choices.items {
				if calls := choice.field("delta", "tool_calls"); calls != nil {
					for _, call := range calls.items {
						index := call.field("index")
						ci := choice.field("index")
						if index == nil {
							continue
						}
						key := string(data[index.start:index.end])
						if ci != nil {
							key = string(data[ci.start:ci.end]) + ":" + key
						}
						if r.chatSeen == nil {
							r.chatSeen = map[string]bool{}
						}
						if !r.chatSeen[key] && call.field("id").value() != "" {
							add(call.field("id"))
							r.chatSeen[key] = true
						}
					}
				}
			}
		}
	}
	if len(changes) == 0 {
		return frame
	}
	return applyIDReplacements(frame, changes)
}
