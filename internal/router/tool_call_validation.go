// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Tool-call failure classes. They are the vocabulary shared by the routing
// telemetry, the validator and the case-study reports; never rename one without
// updating the docs that cite it.
const (
	toolFailureInvalidJSON     = "invalid_json"
	toolFailureUnknownTool     = "unknown_tool"
	toolFailureMissingRequired = "missing_required"
	toolFailureSchemaViolation = "schema_violation"
	toolFailureForcedMismatch  = "forced_tool_mismatch"
	toolFailureTruncatedArgs   = "truncated_arguments"
)

const (
	toolSchemaModeOff   = "off"
	toolSchemaModeBasic = "basic"
	// maxToolCallArgumentBytes bounds how much argument text one call may
	// contribute to validation. A larger call is recorded as invalid_json; it is
	// never parsed and never forwarded differently.
	maxToolCallArgumentBytes = 256 * 1024
	maxToolSchemaNodes       = 2048
	maxToolSchemaDepth       = 16
)

// toolValidationOptions is the per-request validation policy. The zero value
// validates nothing, so callers that do not opt in pay one boolean check.
type toolValidationOptions struct {
	ArgumentsJSON     bool
	Schema            string
	NameMustBeOffered bool
	// ForcedToolNames is non-empty when the caller forced or required a tool
	// call; a response without a call to one of these names is a mismatch.
	ForcedToolNames []string
}

func (o toolValidationOptions) enabled() bool {
	return o.ArgumentsJSON || o.Schema == toolSchemaModeBasic || o.NameMustBeOffered || len(o.ForcedToolNames) > 0
}

// toolCallValidation is the bounded outcome of validating one response's calls.
type toolCallValidation struct {
	Calls              int
	Valid              int
	Invalid            int
	FirstFailure       string
	FirstFailureTool   string
	UnsupportedKeyword bool
	failures           map[string]int
}

func (v toolCallValidation) classCounts() map[string]int {
	if len(v.failures) == 0 {
		return nil
	}
	out := make(map[string]int, len(v.failures))
	for class, count := range v.failures {
		out[class] = count
	}
	return out
}

// validateToolCalls checks the decoded calls of one response against the tools
// the caller offered. It reports the first failure class for telemetry and does
// not stop at it, so a run can see every class present.
func validateToolCalls(calls []IRToolCall, tools []map[string]any, finishReason string, opts toolValidationOptions) toolCallValidation {
	result := toolCallValidation{Calls: len(calls)}
	if !opts.enabled() {
		result.Valid = len(calls)
		return result
	}
	offeredNames, schemas := offeredToolSchemas(tools)
	truncatedResponse := strings.EqualFold(finishReason, "length") || strings.EqualFold(finishReason, "max_tokens")
	for _, call := range calls {
		class := validateOneToolCall(call, offeredNames, schemas, truncatedResponse, opts, &result)
		if class == "" {
			result.Valid++
			continue
		}
		result.Invalid++
		if result.failures == nil {
			result.failures = map[string]int{}
		}
		result.failures[class]++
		if result.FirstFailure == "" {
			result.FirstFailure = class
			result.FirstFailureTool = call.Name
		}
	}
	if len(opts.ForcedToolNames) > 0 && len(calls) == 0 {
		result.Invalid++
		result.FirstFailure = toolFailureForcedMismatch
		if result.failures == nil {
			result.failures = map[string]int{}
		}
		result.failures[toolFailureForcedMismatch]++
	}
	return result
}

func validateOneToolCall(call IRToolCall, offered map[string]bool, schemas map[string]map[string]any, truncated bool, opts toolValidationOptions, result *toolCallValidation) string {
	if opts.NameMustBeOffered && len(offered) > 0 && !offered[call.Name] {
		return toolFailureUnknownTool
	}
	if len(opts.ForcedToolNames) > 0 && !stringSliceContains(opts.ForcedToolNames, call.Name) {
		return toolFailureForcedMismatch
	}
	if !opts.ArgumentsJSON {
		return ""
	}
	if len(call.Arguments) > maxToolCallArgumentBytes {
		return toolFailureInvalidJSON
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		if truncated {
			return toolFailureTruncatedArgs
		}
		return toolFailureInvalidJSON
	}
	if arguments == nil {
		if truncated {
			return toolFailureTruncatedArgs
		}
		return toolFailureInvalidJSON
	}
	if opts.Schema != toolSchemaModeBasic {
		return ""
	}
	schema, ok := schemas[call.Name]
	if !ok || len(schema) == 0 {
		return ""
	}
	budget := maxToolSchemaNodes
	problems, unsupported := validateSchemaValue(schema, arguments, "arguments", 0, &budget)
	if unsupported {
		result.UnsupportedKeyword = true
	}
	if len(problems) == 0 {
		return ""
	}
	if hasSchemaProblem(problems, schemaProblemMissingRequired) {
		return toolFailureMissingRequired
	}
	return toolFailureSchemaViolation
}

const schemaProblemMissingRequired = "missing required"

// offeredToolSchemas indexes the caller's tools by the name the response must
// use, with the argument schema when the caller supplied one.
func offeredToolSchemas(tools []map[string]any) (map[string]bool, map[string]map[string]any) {
	names := make(map[string]bool, len(tools))
	schemas := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		name, schema := toolNameAndSchema(tool)
		if name == "" {
			continue
		}
		names[name] = true
		if schema != nil {
			schemas[name] = schema
		}
	}
	return names, schemas
}

// toolNameAndSchema reads both the OpenAI shape (function.name/parameters) and
// the Anthropic shape (name/input_schema) so one validator covers both.
func toolNameAndSchema(tool map[string]any) (string, map[string]any) {
	if function, ok := tool["function"].(map[string]any); ok {
		name := stringValue(function["name"])
		schema, _ := function["parameters"].(map[string]any)
		if name != "" {
			return name, schema
		}
	}
	name := stringValue(tool["name"])
	schema, _ := tool["input_schema"].(map[string]any)
	if schema == nil {
		schema, _ = tool["parameters"].(map[string]any)
	}
	return name, schema
}

// validateSchemaValue checks a value against the documented basic JSON Schema
// subset. Unsupported keywords make the affected sub-schema unknown: the value
// is not failed on them, and the caller records that the schema went beyond the
// subset. A validator that invented failures here would cause exactly the false
// escalation this work exists to avoid.
func validateSchemaValue(schema map[string]any, value any, path string, depth int, budget *int) ([]string, bool) {
	if depth > maxToolSchemaDepth {
		return nil, true
	}
	*budget--
	if *budget < 0 {
		return nil, true
	}
	var problems []string
	unsupported := false

	if raw, ok := schema["type"]; ok {
		types := schemaTypes(raw)
		if len(types) > 0 && !matchesAnyType(types, value) {
			return []string{path + ": type"}, unsupported
		}
	}
	if raw, ok := schema["const"]; ok {
		if !schemaValuesEqual(raw, value) {
			problems = append(problems, path+": const")
		}
	}
	if raw, ok := schema["enum"]; ok {
		values, isArray := raw.([]any)
		if !isArray {
			unsupported = true
		} else if !anySchemaValueEqual(values, value) {
			problems = append(problems, path+": enum")
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if raw, ok := schema[keyword]; ok {
			branches, isArray := raw.([]any)
			if !isArray || len(branches) == 0 {
				unsupported = true
				continue
			}
			matched := 0
			branchUnsupported := false
			for _, branch := range branches {
				branchSchema, isMap := branch.(map[string]any)
				if !isMap {
					branchUnsupported = true
					continue
				}
				branchProblems, branchUnsupportedSchema := validateSchemaValue(branchSchema, value, path, depth+1, budget)
				if branchUnsupportedSchema {
					branchUnsupported = true
					continue
				}
				if len(branchProblems) == 0 {
					matched++
				}
			}
			if branchUnsupported {
				unsupported = true
				continue
			}
			if keyword == "anyOf" && matched == 0 {
				problems = append(problems, path+": anyOf")
			}
			if keyword == "oneOf" && matched != 1 {
				problems = append(problems, path+": oneOf")
			}
		}
	}
	if raw, ok := schema["allOf"]; ok {
		branches, isArray := raw.([]any)
		if !isArray {
			unsupported = true
		} else {
			for _, branch := range branches {
				branchSchema, isMap := branch.(map[string]any)
				if !isMap {
					unsupported = true
					continue
				}
				branchProblems, branchUnsupported := validateSchemaValue(branchSchema, value, path, depth+1, budget)
				if branchUnsupported {
					unsupported = true
				}
				problems = append(problems, branchProblems...)
			}
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := schema["properties"]; ok {
			properties, isMap := raw.(map[string]any)
			if !isMap {
				unsupported = true
			} else {
				keys := make([]string, 0, len(properties))
				for key := range properties {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					childSchema, isMap := properties[key].(map[string]any)
					if !isMap {
						unsupported = true
						continue
					}
					if childValue, present := typed[key]; present {
						childProblems, childUnsupported := validateSchemaValue(childSchema, childValue, path+"."+key, depth+1, budget)
						if childUnsupported {
							unsupported = true
						}
						problems = append(problems, childProblems...)
					}
				}
				if allow, declared := schema["additionalProperties"].(bool); declared && !allow {
					for key := range typed {
						if _, known := properties[key]; !known {
							problems = append(problems, path+"."+key+": additionalProperties")
						}
					}
				} else if raw, ok := schema["additionalProperties"]; ok {
					if additional, isMap := raw.(map[string]any); isMap {
						for key, childValue := range typed {
							if _, known := properties[key]; known {
								continue
							}
							childProblems, childUnsupported := validateSchemaValue(additional, childValue, path+"."+key, depth+1, budget)
							if childUnsupported {
								unsupported = true
							}
							problems = append(problems, childProblems...)
						}
					} else if _, isBool := raw.(bool); !isBool {
						unsupported = true
					}
				}
			}
		}
		if raw, ok := schema["required"]; ok {
			required, isArray := raw.([]any)
			if !isArray {
				unsupported = true
			} else {
				for _, entry := range required {
					name, isString := entry.(string)
					if !isString {
						unsupported = true
						continue
					}
					if _, present := typed[name]; !present {
						problems = append(problems, schemaProblemMissingRequired+" "+path+"."+name)
					}
				}
			}
		}
	case []any:
		if raw, ok := schema["items"]; ok {
			itemSchema, isMap := raw.(map[string]any)
			if !isMap {
				unsupported = true
			} else {
				for index, childValue := range typed {
					childProblems, childUnsupported := validateSchemaValue(itemSchema, childValue, path+"["+strconv.Itoa(index)+"]", depth+1, budget)
					if childUnsupported {
						unsupported = true
					}
					problems = append(problems, childProblems...)
				}
			}
		}
		if raw, ok := schema["minItems"]; ok {
			if limit, valid := numberAsFloat(raw); valid && float64(len(typed)) < limit {
				problems = append(problems, path+": minItems")
			} else if !valid {
				unsupported = true
			}
		}
		if raw, ok := schema["maxItems"]; ok {
			if limit, valid := numberAsFloat(raw); valid && float64(len(typed)) > limit {
				problems = append(problems, path+": maxItems")
			} else if !valid {
				unsupported = true
			}
		}
		if raw, ok := schema["properties"]; ok {
			if _, isMap := raw.(map[string]any); !isMap {
				unsupported = true
			}
		}
	}
	// Any keyword outside the supported set makes this sub-schema unknown. It is
	// not a failure: the caller records it and keeps the call.
	for keyword := range schema {
		if !supportedSchemaKeyword(keyword) {
			unsupported = true
		}
	}
	return problems, unsupported
}

func supportedSchemaKeyword(keyword string) bool {
	switch keyword {
	case "type", "properties", "required", "items", "enum", "const",
		"additionalProperties", "minItems", "maxItems", "anyOf", "oneOf", "allOf",
		"description", "title", "$schema", "default":
		return true
	default:
		return false
	}
}

func schemaTypes(raw any) []string {
	switch value := raw.(type) {
	case string:
		return []string{value}
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			if name, ok := entry.(string); ok {
				out = append(out, name)
			}
		}
		return out
	default:
		return nil
	}
}

func matchesAnyType(types []string, value any) bool {
	for _, name := range types {
		if matchesSchemaType(name, value) {
			return true
		}
	}
	return false
}

func matchesSchemaType(name string, value any) bool {
	switch name {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	default:
		// An unknown type name makes the check unknown rather than failing it.
		return true
	}
}

func anySchemaValueEqual(values []any, value any) bool {
	for _, candidate := range values {
		if schemaValuesEqual(candidate, value) {
			return true
		}
	}
	return false
}

func schemaValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func hasSchemaProblem(problems []string, prefix string) bool {
	for _, problem := range problems {
		if strings.HasPrefix(problem, prefix) {
			return true
		}
	}
	return false
}
