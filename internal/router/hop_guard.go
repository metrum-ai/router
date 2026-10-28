// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"strings"
)

const routerViaName = "metrum-ai-router"

// routerViaHops counts our received-by pseudonym in Via entries. A comma in
// an RFC comment is not a field separator; nested and escaped comments are
// accepted. Malformed comments fail closed rather than hiding a router hop.
func routerViaHops(values []string) (int, bool) {
	count := 0
	for _, value := range values {
		start, depth := 0, 0
		escaped := false
		for i := 0; i <= len(value); i++ {
			if i == len(value) || (value[i] == ',' && depth == 0) {
				if depth != 0 || escaped {
					return 0, false
				}
				entry := value[start:i]
				if pos := strings.IndexByte(entry, '('); pos >= 0 {
					entry = entry[:pos]
				}
				fields := strings.Fields(entry)
				if len(fields) != 2 {
					return 0, false
				}
				if strings.EqualFold(fields[1], routerViaName) {
					count++
				}
				start = i + 1
				continue
			}
			c := value[i]
			if escaped {
				escaped = false
				continue
			}
			if depth > 0 && c == '\\' {
				escaped = true
				continue
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth < 0 {
					return 0, false
				}
			}
		}
	}
	return count, true
}

// prepareRouterVia enforces the limit on authenticated data-plane requests.
// Do not log the incoming Via value: it can contain client-controlled data.
func (s *Service) prepareRouterVia(w http.ResponseWriter, r *http.Request, rc *requestContext) bool {
	values := r.Header.Values("Via")
	hops, valid := routerViaHops(values)
	if !valid {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-via")
		return false
	}
	if hops >= s.cfg.Server.Upstream.MaxRouterHops {
		s.writeError(w, rc, http.StatusLoopDetected, "router-hop-limit")
		return false
	}
	version := "1.1"
	if r.ProtoMajor > 0 {
		version = fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor)
	}
	rc.via = append(append([]string(nil), values...), version+" "+routerViaName)
	return true
}

func forwardRouterVia(header http.Header, via []string) {
	for _, entry := range via {
		header.Add("Via", entry)
	}
}
