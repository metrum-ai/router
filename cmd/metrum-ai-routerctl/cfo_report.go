// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/metrum-ai/router/internal/smartrouterctl"
)

func reportsCommand(args []string) {
	if len(args) == 0 || args[0] != "cfo" {
		die("usage: metrum-ai-routerctl reports cfo [flags]")
	}
	if err := runCFOReport(args[1:], os.Stdout); err != nil {
		die("%v", err)
	}
}

// runCFOReport uses a dedicated admin key from the established credential
// resolver. It never accepts the key in an argument or follows redirects.
func runCFOReport(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reports cfo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	routerURL := fs.String("router-url", "", "router URL or ROUTER_URL")
	keyFile := fs.String("admin-key-file", "", "admin credential file")
	prefix := fs.String("reports-prefix", "/admin/v1/reports", "admin reports path prefix")
	format := fs.String("format", "json", "json, csv or markdown")
	since := fs.String("since", "", "relative period, for example 24h")
	from := fs.String("from", "", "inclusive RFC3339 start")
	to := fs.String("to", "", "exclusive RFC3339 end")
	baseline := fs.String("baseline", "", "configured baseline id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected reports cfo arguments")
	}
	p := strings.TrimSpace(*prefix)
	if path.Clean(p) != p || (p != "/admin/v1/reports" && !strings.HasPrefix(p, "/admin/v1/reports/")) || strings.ContainsAny(p, "?#%\\") {
		return errors.New("invalid reports prefix")
	}
	endpoint := "/cfo"
	switch *format {
	case "json":
	case "csv":
		endpoint = "/cfo/export.csv"
	case "markdown":
		endpoint = "/cfo/export.md"
	default:
		return errors.New("format must be json, csv or markdown")
	}
	query := url.Values{}
	for _, item := range []struct{ name, value string }{{"since", *since}, {"from", *from}, {"to", *to}, {"baseline", *baseline}} {
		if item.value != "" {
			query.Set(item.name, item.value)
		}
	}
	base := strings.TrimSpace(*routerURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("ROUTER_URL"))
	}
	client, err := smartrouterctl.NewAdminAPIClient(base, *keyFile)
	if err != nil {
		return err
	}
	requestPath := p + endpoint
	if len(query) > 0 {
		requestPath += "?" + query.Encode()
	}
	const maxReportBytes = 16 << 20
	data, status, _, err := client.DoWithHeadersLimit(http.MethodGet, requestPath, nil, nil, maxReportBytes)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("unexpected report HTTP status %d", status)
	}
	if len(data) > maxReportBytes {
		return errors.New("report exceeds the 16 MiB client limit; narrow the range or filters")
	}
	_, err = out.Write(data)
	return err
}
