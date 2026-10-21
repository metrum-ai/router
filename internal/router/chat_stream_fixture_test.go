// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// writeChatTestSSE makes protocol-correct streaming replies from unary fixture data.
func writeChatTestSSE(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	if choices, ok := body["choices"].([]map[string]any); ok {
		for _, choice := range choices {
			choice["delta"] = choice["message"]
			delete(choice, "message")
		}
	}
	raw, _ := json.Marshal(body)
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
}
