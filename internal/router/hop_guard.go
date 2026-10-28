// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"strings"
)

const routerViaName = "metrum-ai-router"

// routerViaEntries retains only parseable entries bearing our received-by
// pseudonym. It never forwards comments or third-party proxy identifiers.
// Unparseable entries are ignored; Via is not a trusted security boundary.
func routerViaEntries(values []string) []string {
	var entries []string
	for _, value := range values {
		var plain strings.Builder
		depth := 0
		escaped, malformed := false, false
		finish := func() {
			if !malformed && depth == 0 && !escaped {
				fields := strings.Fields(plain.String())
				if len(fields) == 2 && strings.EqualFold(fields[1], routerViaName) {
					if version, ok := routerViaVersion(fields[0]); ok {
						entries = append(entries, version+" "+routerViaName)
					}
				}
			}
			plain.Reset()
			malformed = false
		}
		for i := 0; i < len(value); i++ {
			c := value[i]
			if depth == 0 && c == ',' {
				finish()
				continue
			}
			if escaped {
				escaped = false
				continue
			}
			if depth > 0 && c == '\\' {
				escaped = true
				continue
			}
			switch c {
			case '(':
				if depth == 0 {
					plain.WriteByte(' ')
				}
				depth++
			case ')':
				if depth == 0 {
					malformed = true
				} else {
					depth--
				}
			default:
				if depth == 0 {
					plain.WriteByte(c)
				}
			}
		}
		finish()
	}
	return entries
}

// Router-generated received-protocols are HTTP versions. Restricting this
// field to short numeric versions prevents forwarding client-provided data as
// a forged protocol name. HTTP/ is optional in the Via grammar.
func routerViaVersion(protocol string) (string, bool) {
	if len(protocol) >= 5 && strings.EqualFold(protocol[:5], "HTTP/") {
		protocol = protocol[5:]
	}
	if len(protocol) == 0 || len(protocol) > 16 {
		return "", false
	}
	dots := 0
	for i := 0; i < len(protocol); i++ {
		if protocol[i] == '.' {
			dots++
			if i == 0 || i == len(protocol)-1 || dots > 1 {
				return "", false
			}
		} else if protocol[i] < '0' || protocol[i] > '9' {
			return "", false
		}
	}
	return protocol, true
}

// prepareRouterVia enforces the limit on authenticated data-plane requests.
// Do not log incoming Via values: they can contain client-controlled data.
func (s *Service) prepareRouterVia(w http.ResponseWriter, r *http.Request, rc *requestContext) bool {
	entries := routerViaEntries(r.Header.Values("Via"))
	if len(entries) >= s.cfg.Server.Upstream.MaxRouterHops {
		s.writeError(w, rc, http.StatusLoopDetected, "router-hop-limit")
		return false
	}
	version := "1.1"
	if r.ProtoMajor > 0 {
		version = fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor)
	}
	rc.via = append(entries, version+" "+routerViaName)
	return true
}

func forwardRouterVia(header http.Header, via []string) {
	// Do not leak third-party entries from either the client or static provider
	// headers. Only sanitized router entries prepared above are forwarded.
	header.Del("Via")
	for _, entry := range via {
		header.Add("Via", entry)
	}
}
