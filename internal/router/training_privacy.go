// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "regexp"

var trainingEmail = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+`)
var trainingPhone = regexp.MustCompile(`\+?[0-9][0-9 ()-]{7,}[0-9]`)

func trainingPrivacy(v any) any {
	switch x := v.(type) {
	case string:
		x = redactDiagnosticSecrets(x)
		x = trainingEmail.ReplaceAllString(x, "[EMAIL_REDACTED]")
		return trainingPhone.ReplaceAllString(x, "[PHONE_REDACTED]")
	case map[string]any:
		for k, child := range x {
			x[k] = trainingPrivacy(child)
		}
	case []any:
		for i, child := range x {
			x[i] = trainingPrivacy(child)
		}
	}
	return v
}
