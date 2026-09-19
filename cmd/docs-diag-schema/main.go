// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const (
	startMarker = "<!-- @generated diagnostics-schema -->"
	endMarker   = "<!-- /@generated diagnostics-schema -->"
)

type tableDoc struct {
	StructName string
	TableName  string
	SourceFile string
	SourceLine int
	Columns    []columnDoc
}

type columnDoc struct {
	Name     string
	GoName   string
	GoType   string
	Scalar   string
	Nullable bool
	Primary  bool
	Indexes  []string
	GormTag  string
}

func main() {
	var (
		check = flag.Bool("check", false, "fail if diagnostics schema docs are stale")
		write = flag.Bool("write", false, "write generated diagnostics schema docs")
		page  = flag.String("page", "docs-site/docs/reference/diagnostics-schema.md", "markdown page to update")
	)
	flag.Parse()
	if !*check && !*write {
		*write = true
	}

	root, err := findRepoRoot()
	must(err)
	sources := []string{
		filepath.Join(root, "internal/router/usage_db.go"),
		filepath.Join(root, "internal/router/content_capture.go"),
	}
	tables, err := parseTables(root, sources)
	must(err)
	generated := render(tables)

	pagePath := filepath.Join(root, *page)
	current, err := os.ReadFile(pagePath)
	must(err)
	next, err := replaceGenerated(current, generated)
	must(err)
	if bytes.Equal(current, next) {
		return
	}
	if *check {
		fmt.Fprintf(os.Stderr, "%s is stale; run `rtk make docs-diag-schema`\n", *page)
		os.Exit(1)
	}
	must(os.WriteFile(pagePath, next, 0o644))
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("go.mod not found")
		}
		wd = parent
	}
}

func parseTables(root string, paths []string) ([]tableDoc, error) {
	tableByStruct := map[string]string{}
	structs := map[string]*ast.StructType{}
	aliases := map[string]string{}
	sourceByStruct := map[string]string{}
	lineByStruct := map[string]int{}
	fset := token.NewFileSet()

	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						if id, ok := ts.Type.(*ast.Ident); ok {
							aliases[ts.Name.Name] = id.Name
							sourceByStruct[ts.Name.Name] = rel(root, path)
							lineByStruct[ts.Name.Name] = fset.Position(ts.Pos()).Line
						}
						continue
					}
					structs[ts.Name.Name] = st
					sourceByStruct[ts.Name.Name] = rel(root, path)
					lineByStruct[ts.Name.Name] = fset.Position(ts.Pos()).Line
				}
			case *ast.FuncDecl:
				receiver := receiverName(d)
				if receiver == "" || d.Name.Name != "TableName" || d.Body == nil {
					continue
				}
				if table := returnedString(d.Body); table != "" {
					tableByStruct[receiver] = table
				}
			}
		}
	}

	var tables []tableDoc
	for structName, tableName := range tableByStruct {
		st := structs[structName]
		if st == nil && aliases[structName] != "" {
			st = structs[aliases[structName]]
		}
		if st == nil || !includeTable(tableName) {
			continue
		}
		t := tableDoc{
			StructName: structName,
			TableName:  tableName,
			SourceFile: sourceByStruct[structName],
			SourceLine: lineByStruct[structName],
		}
		for _, f := range st.Fields.List {
			if len(f.Names) == 0 || f.Tag == nil {
				continue
			}
			tag := strings.Trim(f.Tag.Value, "`")
			gormTag := reflect.StructTag(tag).Get("gorm")
			column := gormColumn(gormTag)
			if column == "" {
				continue
			}
			goType := exprString(f.Type)
			primary := strings.Contains(gormTag, "primaryKey")
			t.Columns = append(t.Columns, columnDoc{
				Name:     column,
				GoName:   f.Names[0].Name,
				GoType:   goType,
				Scalar:   scalarType(goType, gormTag),
				Nullable: nullable(goType, gormTag, primary),
				Primary:  primary,
				Indexes:  indexes(gormTag),
				GormTag:  gormTag,
			})
		}
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool {
		return tableOrder(tables[i].TableName) < tableOrder(tables[j].TableName)
	})
	return tables, nil
}

func includeTable(name string) bool {
	return name == "security_access_events" ||
		strings.HasPrefix(name, "request_") ||
		strings.HasPrefix(name, "usage_rollup_") ||
		strings.HasPrefix(name, "retention_") ||
		strings.HasPrefix(name, "legal_hold") ||
		strings.HasPrefix(name, "legal_holds")
}

func tableOrder(name string) string {
	prefixes := map[string]string{
		"request_usage":                    "00",
		"request_attempts":                 "01",
		"request_trace_events":             "02",
		"request_errors":                   "03",
		"request_upstream_error_details":   "04",
		"request_traffic_shape_events":     "05",
		"request_upstream_shape_events":    "06",
		"request_shapes":                   "07",
		"request_translation_shapes":       "08",
		"request_translation_field_events": "09",
	}
	if p, ok := prefixes[name]; ok {
		return p + name
	}
	return "50" + name
}

func receiverName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	switch rt := d.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return rt.Name
	case *ast.StarExpr:
		if id, ok := rt.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

func returnedString(body *ast.BlockStmt) string {
	for _, stmt := range body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			continue
		}
		lit, ok := ret.Results[0].(*ast.BasicLit)
		if !ok {
			continue
		}
		return strings.Trim(lit.Value, `"`)
	}
	return ""
}

func gormColumn(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return ""
}

func indexes(tag string) []string {
	var out []string
	for _, part := range strings.Split(tag, ";") {
		if strings.HasPrefix(part, "index:") || strings.HasPrefix(part, "uniqueIndex:") {
			value := strings.TrimPrefix(strings.TrimPrefix(part, "index:"), "uniqueIndex:")
			if i := strings.Index(value, ","); i >= 0 {
				value = value[:i]
			}
			if value != "" {
				out = append(out, value)
			}
		}
	}
	sort.Strings(out)
	return out
}

func nullable(goType, tag string, primary bool) bool {
	if primary {
		return false
	}
	return strings.HasPrefix(goType, "*") || !strings.Contains(tag, "not null")
}

func scalarType(goType, tag string) string {
	if strings.Contains(tag, "type:text") {
		return "string"
	}
	switch strings.TrimPrefix(goType, "*") {
	case "string":
		return "string"
	case "bool":
		return "boolean"
	case "int", "int64", "uint":
		return "integer"
	case "float64":
		return "decimal"
	default:
		return "scalar"
	}
}

func exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(e.Elt)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func render(tables []tableDoc) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "This section is generated from the router usage and content-capture schema used by the running product.\n\n")
	fmt.Fprintf(&b, "| Table | Schema type | Retention class | Foreign keys | Indexes |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|\n")
	for _, t := range tables {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s |\n",
			t.TableName, t.StructName, retentionClass(t.TableName), escapeMD(foreignKeys(t)), escapeMD(tableIndexes(t)))
	}
	fmt.Fprintf(&b, "\n")
	for _, t := range tables {
		fmt.Fprintf(&b, "### `%s`\n\n", t.TableName)
		fmt.Fprintf(&b, "- Schema type: `%s`\n", t.StructName)
		fmt.Fprintf(&b, "- Retention class: %s\n", retentionClass(t.TableName))
		fmt.Fprintf(&b, "- Foreign keys: %s\n", foreignKeys(t))
		fmt.Fprintf(&b, "- Indexes: %s\n\n", tableIndexes(t))
		fmt.Fprintf(&b, "| Column | Scalar type | Nullable | Populated when | Safe to log/share | Example or range |\n")
		fmt.Fprintf(&b, "|---|---|---|---|---|---|\n")
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n",
				c.Name, c.Scalar, yesNo(c.Nullable), escapeMD(populatedWhen(t.TableName, c)), escapeMD(safeStatus(t.TableName, c.Name)), escapeMD(exampleValue(t.TableName, c)))
		}
		fmt.Fprintf(&b, "\n")
	}
	return []byte(b.String())
}

func replaceGenerated(current, generated []byte) ([]byte, error) {
	start := bytes.Index(current, []byte(startMarker))
	if start < 0 {
		return nil, fmt.Errorf("start marker %q not found", startMarker)
	}
	end := bytes.Index(current[start:], []byte(endMarker))
	if end < 0 {
		return nil, fmt.Errorf("end marker %q not found", endMarker)
	}
	end += start
	end += len(endMarker)
	var out bytes.Buffer
	out.Write(current[:start+len(startMarker)])
	out.Write(generated)
	out.Write([]byte(endMarker))
	out.Write(current[end:])
	return out.Bytes(), nil
}

func retentionClass(table string) string {
	switch {
	case table == "request_usage":
		return "`usage_detail`"
	case strings.HasPrefix(table, "request_content_"):
		return "`content_capture`"
	case strings.HasPrefix(table, "request_decision_") ||
		table == "request_target_candidates" ||
		table == "request_target_filter_reasons" ||
		table == "request_routing_decisions" ||
		table == "request_routing_signals" ||
		table == "request_dynamic_score_terms" ||
		table == "request_policy_executions" ||
		table == "request_fallback_transitions" ||
		table == "request_cache_reasons":
		return "`decision_telemetry`"
	case strings.HasPrefix(table, "usage_rollup_"):
		return "`usage_rollup`"
	case strings.HasPrefix(table, "retention_") || strings.HasPrefix(table, "legal_hold"):
		return "`retention_control`"
	case table == "security_access_events":
		return "`security_access_events`"
	default:
		return "`usage_diagnostics`"
	}
}

func foreignKeys(t tableDoc) string {
	var keys []string
	for _, c := range t.Columns {
		switch c.Name {
		case "request_id":
			if t.TableName != "request_usage" {
				keys = append(keys, "`request_id` joins `request_usage.request_id` when a usage row exists")
			}
		case "capture_id":
			keys = append(keys, "`capture_id` joins `request_content_captures.id`")
		case "run_id":
			keys = append(keys, "`run_id` joins `usage_rollup_runs.id`")
		case "policy_version_id":
			keys = append(keys, "`policy_version_id` joins `retention_policy_versions.id`")
		case "job_id":
			keys = append(keys, "`job_id` joins `retention_jobs.id`")
		case "hold_id":
			if t.TableName != "legal_holds" {
				keys = append(keys, "`hold_id` joins `legal_holds.hold_id`")
			}
		}
	}
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, "; ")
}

func tableIndexes(t tableDoc) string {
	set := map[string]bool{}
	for _, c := range t.Columns {
		if c.Primary {
			set["primary key"] = true
		}
		for _, idx := range c.Indexes {
			set["`"+idx+"`"] = true
		}
	}
	if len(set) == 0 {
		return "none"
	}
	var indexes []string
	for idx := range set {
		indexes = append(indexes, idx)
	}
	sort.Strings(indexes)
	return strings.Join(indexes, ", ")
}

func populatedWhen(table string, c columnDoc) string {
	name := c.Name
	switch {
	case strings.HasPrefix(table, "request_content_audit"):
		return "when a content-capture maintenance action is recorded"
	case strings.HasPrefix(table, "request_content_"):
		return "only when governed content capture is explicitly enabled"
	case strings.HasPrefix(table, "usage_rollup_"):
		return "during usage rollup generation or finalization"
	case strings.HasPrefix(table, "retention_") || strings.HasPrefix(table, "legal_hold"):
		return "during retention policy, legal-hold, or purge workflows"
	case table == "security_access_events":
		return "when an authenticated or denied admin/security surface is evaluated"
	case table == "request_usage":
		if strings.Contains(name, "cost") || strings.Contains(name, "price") || strings.Contains(name, "tokens") {
			return "after usage and request-time cost accounting"
		}
		if strings.Contains(name, "traffic_shape") {
			return "during caller traffic-shaping admission"
		}
		return "once per request at terminal request accounting"
	case table == "request_attempts":
		return "after each upstream attempt completes or fails"
	case table == "request_trace_events":
		return "as ordered router decisions occur"
	case table == "request_traffic_shape_events":
		return "during caller traffic-shaping admission and queue handling"
	case table == "request_upstream_shape_events":
		return "during provider/model/target shared admission or adaptive backoff"
	case table == "request_shapes":
		return "after safe inbound request-shape extraction"
	case table == "request_translation_shapes" || table == "request_translation_field_events":
		return "after upstream dialect translation for an attempt"
	case table == "request_errors":
		return "when the request ends in a caller-visible terminal error"
	case table == "request_upstream_error_details":
		return "when sanitized allowlisted upstream error-detail capture is enabled"
	case strings.HasPrefix(table, "request_"):
		return "when optional decision telemetry records the routing decision"
	default:
		return "when the owning workflow writes the row"
	}
}

func safeStatus(table, column string) string {
	switch {
	case strings.HasPrefix(table, "request_content_"):
		if column == "content_text" || column == "value" {
			return "restricted; redacted and AES-256-GCM-encrypted governed content, not ordinary diagnostics"
		}
		return "safe metadata under content-capture authorization"
	case column == "caller_ip" || column == "ip_address":
		return "restricted; operational metadata, may identify a client network"
	case column == "token_id" || strings.HasSuffix(column, "_token_id"):
		return "safe public token ID only; never a raw token or token hash"
	case strings.Contains(column, "fingerprint"):
		return "safe non-reversible HMAC/fingerprint value"
	case column == "field_value":
		return "restricted; bounded allowlisted and sanitized upstream error field"
	case strings.Contains(column, "error_message") || column == "safe_error":
		return "safe only after router sanitization"
	case strings.Contains(column, "provider") || strings.Contains(column, "model"):
		return "safe deployment metadata"
	default:
		return "safe scalar diagnostics metadata"
	}
}

func exampleValue(table string, c columnDoc) string {
	name := c.Name
	switch {
	case name == "request_id":
		return "`req_0123456789abcdef0123456789abcdef`"
	case strings.HasSuffix(name, "_at") || name == "ts" || strings.Contains(name, "window_") || strings.Contains(name, "expiry"):
		return "`2026-06-29T12:34:56Z`"
	case strings.Contains(name, "status") || strings.HasSuffix(name, "_code"):
		return "`200`, `403`, `502`"
	case strings.Contains(name, "duration") || strings.Contains(name, "latency") || strings.HasSuffix(name, "_ms"):
		return "`0..600000` milliseconds"
	case strings.Contains(name, "cost") || strings.Contains(name, "price") || strings.Contains(name, "score") || strings.Contains(name, "pct"):
		return "`0.0` or positive decimal"
	case strings.Contains(name, "tokens") || strings.Contains(name, "count") || strings.Contains(name, "bytes") || strings.Contains(name, "index") || name == "seq" || name == "rank":
		return "`0` or positive integer"
	case c.Scalar == "boolean":
		return "`true` or `false`"
	case table == "request_routing_signals" && name == "signal_name":
		return "`affinity_hit`, `affinity_miss`, `affinity_expired`, `affinity_ineligible`, `affinity_disabled`, `shadow_recommended_candidate`"
	case table == "request_policy_executions" && name == "outcome":
		return "`success`, `fallback`, `shadow_recommended`, `baseline`, `error`"
	case name == "target_dialect":
		return "`openai-chat`, `openai-responses`, `anthropic-messages`, `gemini-generate-content`, `replicate`"
	case strings.Contains(name, "dialect"):
		return "`openai-chat`, `openai-responses`, `anthropic-messages`"
	case strings.Contains(name, "provider"):
		return "`openrouter`"
	case strings.Contains(name, "model"):
		return "deployment-defined model or group label"
	case strings.Contains(name, "bucket"):
		return "`none`, `small`, `large`, or deployment bucket"
	case strings.Contains(name, "fingerprint"):
		return "opaque non-reversible fingerprint"
	default:
		return "deployment-defined scalar value"
	}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

var mdEscape = regexp.MustCompile(`[|\n\r]`)

func escapeMD(s string) string {
	return mdEscape.ReplaceAllStringFunc(s, func(m string) string {
		if m == "|" {
			return `\|`
		}
		return " "
	})
}
