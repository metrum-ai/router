// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"math"
)

const maxEmbeddingInputs = 2048

type embeddingInput struct {
	Model      string
	Dimensions int
	TokenIDs   bool
	Items      []int // Per-item admission estimates; never input content.
	Raw        map[string]any
}

func decodeEmbeddingInput(body []byte) (embeddingInput, string, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return embeddingInput{}, "", fmt.Errorf("invalid JSON object")
	}
	req := embeddingInput{Raw: raw}
	if value, present := raw["model"]; present {
		var ok bool
		req.Model, ok = value.(string)
		if !ok || req.Model == "" {
			return embeddingInput{}, "model", fmt.Errorf("invalid model")
		}
	}
	if value, present := raw["dimensions"]; present {
		dim, ok := embeddingInt(value)
		if !ok || dim <= 0 {
			return embeddingInput{}, "dimensions", fmt.Errorf("invalid dimensions")
		}
		req.Dimensions = dim
	}
	if value, present := raw["encoding_format"]; present {
		format, ok := value.(string)
		if !ok || (format != "float" && format != "base64") {
			return embeddingInput{}, "encoding_format", fmt.Errorf("invalid encoding format")
		}
	}
	countString := func(v any) (int, bool) {
		s, ok := v.(string)
		if !ok || s == "" {
			return 0, false
		}
		return estimateBytesAsTokens(len(s)), true
	}
	countTokens := func(v any) (int, bool) {
		values, ok := v.([]any)
		if !ok || len(values) == 0 {
			return 0, false
		}
		for _, value := range values {
			token, valid := embeddingInt(value)
			if !valid || token < 0 {
				return 0, false
			}
		}
		return len(values), true
	}
	value, exists := raw["input"]
	if !exists || value == nil {
		return embeddingInput{}, "input", fmt.Errorf("missing input")
	}
	if count, ok := countString(value); ok {
		req.Items = []int{count}
		return req, "", nil
	}
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return embeddingInput{}, "input", fmt.Errorf("invalid input")
	}
	if _, firstIsNumber := values[0].(float64); firstIsNumber {
		count, ok := countTokens(values)
		if !ok {
			return embeddingInput{}, "input", fmt.Errorf("invalid token input")
		}
		req.Items = []int{count}
		req.TokenIDs = true
		return req, "", nil
	}
	if len(values) > maxEmbeddingInputs {
		return embeddingInput{}, "input", fmt.Errorf("too many input items")
	}
	req.Items = make([]int, 0, len(values))
	_, stringsBatch := values[0].(string)
	req.TokenIDs = !stringsBatch
	for _, item := range values {
		var count int
		var valid bool
		if stringsBatch {
			count, valid = countString(item)
		} else {
			count, valid = countTokens(item)
		}
		if !valid {
			return embeddingInput{}, "input", fmt.Errorf("invalid input item")
		}
		req.Items = append(req.Items, count)
	}
	return req, "", nil
}

func embeddingInt(v any) (int, bool) {
	n, ok := v.(float64)
	if !ok || n < 0 || n > math.MaxInt32 || math.Trunc(n) != n {
		return 0, false
	}
	return int(n), true
}
