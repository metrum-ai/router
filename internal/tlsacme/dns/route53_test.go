// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

const route53NS = `xmlns="https://route53.amazonaws.com/doc/2013-04-01/"`

// route53Fake emulates the Route 53 record-set API closely enough to test
// value-level TXT merging: it keeps one value set per name+type.
type route53Fake struct {
	t        *testing.T
	api      *fakeAPI
	sets     map[string][]string // "name|TYPE" -> values
	ttls     map[string]int
	requests []string // action + name from each change batch
}

func newRoute53Fake(t *testing.T) *route53Fake {
	fake := &route53Fake{t: t, sets: map[string][]string{}, ttls: map[string]int{}}
	fake.api = newFakeAPI(t, fake.handle)
	return fake
}

func (f *route53Fake) provider() *Route53 {
	f.t.Helper()
	provider, err := NewRoute53(context.Background(), Route53Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"},
		Options{Endpoint: f.api.server.URL})
	if err != nil {
		f.t.Fatalf("new provider: %v", err)
	}
	provider.changePollInterval = time.Millisecond
	provider.changeTimeout = 5 * time.Second
	return provider
}

var (
	changeActionRE = regexp.MustCompile(`<Action>(\w+)</Action>`)
	changeNameRE   = regexp.MustCompile(`<Name>([^<]+)</Name>`)
	changeTypeRE   = regexp.MustCompile(`<Type>([^<]+)</Type>`)
	changeValueRE  = regexp.MustCompile(`<Value>([^<]*)</Value>`)
)

func (f *route53Fake) handle(req requestLog, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	switch {
	case strings.HasSuffix(req.path, "/hostedzonesbyname"):
		fmt.Fprintf(w, `<?xml version="1.0"?><ListHostedZonesByNameResponse %s><HostedZones><HostedZone><Id>/hostedzone/Z1</Id><Name>example.com.</Name><CallerReference>ref</CallerReference></HostedZone></HostedZones><IsTruncated>false</IsTruncated></ListHostedZonesByNameResponse>`, route53NS)
	case strings.HasSuffix(req.path, "/rrset") && req.method == http.MethodGet:
		var builder strings.Builder
		fmt.Fprintf(&builder, `<?xml version="1.0"?><ListResourceRecordSetsResponse %s><ResourceRecordSets>`, route53NS)
		for key, values := range f.sets {
			name, recordType, _ := strings.Cut(key, "|")
			fmt.Fprintf(&builder, `<ResourceRecordSet><Name>%s</Name><Type>%s</Type><TTL>%d</TTL><ResourceRecords>`, name, recordType, f.ttls[key])
			for _, value := range values {
				fmt.Fprintf(&builder, `<ResourceRecord><Value>%s</Value></ResourceRecord>`, xmlEscape(value))
			}
			builder.WriteString(`</ResourceRecords></ResourceRecordSet>`)
		}
		builder.WriteString(`</ResourceRecordSets><IsTruncated>false</IsTruncated></ListResourceRecordSetsResponse>`)
		fmt.Fprint(w, builder.String())
	case strings.HasSuffix(req.path, "/rrset") && req.method == http.MethodPost:
		action := changeActionRE.FindStringSubmatch(req.body)
		if action == nil {
			f.t.Errorf("change batch has no action: %s", req.body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		name := strings.TrimSuffix(changeNameRE.FindStringSubmatch(req.body)[1], ".")
		recordType := changeTypeRE.FindStringSubmatch(req.body)[1]
		var values []string
		for _, match := range changeValueRE.FindAllStringSubmatch(req.body, -1) {
			values = append(values, xmlUnescape(match[1]))
		}
		key := name + "|" + recordType
		switch action[1] {
		case "CREATE":
			if _, exists := f.sets[key]; exists {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `<ErrorResponse><Error><Code>InvalidChangeBatch</Code><Message>already exists</Message></Error></ErrorResponse>`)
				return
			}
			f.sets[key] = values
		case "UPSERT":
			f.sets[key] = values
		case "DELETE":
			if _, exists := f.sets[key]; !exists {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `<ErrorResponse><Error><Code>InvalidChangeBatch</Code><Message>not found</Message></Error></ErrorResponse>`)
				return
			}
			delete(f.sets, key)
		}
		f.ttls[key] = 60
		f.requests = append(f.requests, action[1]+" "+key)
		fmt.Fprintf(w, `<?xml version="1.0"?><ChangeResourceRecordSetsResponse %s><ChangeInfo><Id>/change/C1</Id><Status>PENDING</Status><SubmittedAt>2026-11-06T00:00:00Z</SubmittedAt></ChangeInfo></ChangeResourceRecordSetsResponse>`, route53NS)
	case strings.Contains(req.path, "/change/"):
		fmt.Fprintf(w, `<?xml version="1.0"?><GetChangeResponse %s><ChangeInfo><Id>/change/C1</Id><Status>INSYNC</Status><SubmittedAt>2026-11-06T00:00:00Z</SubmittedAt></ChangeInfo></GetChangeResponse>`, route53NS)
	default:
		f.t.Errorf("unexpected request %s", req)
		w.WriteHeader(http.StatusBadRequest)
	}
}

// lastChangeBody returns the body of the most recent ChangeResourceRecordSets
// call (the GetChange poll that follows it has no body).
func (f *route53Fake) lastChangeBody() string {
	for i := len(f.api.requests) - 1; i >= 0; i-- {
		if f.api.requests[i].method == http.MethodPost && strings.HasSuffix(f.api.requests[i].path, "/rrset") {
			return f.api.requests[i].body
		}
	}
	return ""
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return replacer.Replace(value)
}

func xmlUnescape(value string) string {
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'")
	return replacer.Replace(value)
}

func TestRoute53ProviderCalls(t *testing.T) {
	fake := newRoute53Fake(t)
	provider := fake.provider()
	ctx := context.Background()

	zones, err := provider.Zones(ctx)
	if err != nil || len(zones) != 1 || zones[0] != "example.com" {
		t.Fatalf("zones = %v, %v", zones, err)
	}

	// First TXT value: CREATE with a single quoted value.
	id, err := provider.Create(ctx, "example.com", Record{Type: "TXT", Name: "_acme-challenge.example.com", Value: "token-value", TTL: 60})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := "_acme-challenge.example.com|TXT|token-value"; id != want {
		t.Fatalf("create id = %q, want %q (the id names the value)", id, want)
	}
	if got := fake.requests[len(fake.requests)-1]; got != "CREATE _acme-challenge.example.com|TXT" {
		t.Fatalf("change = %q", got)
	}
	createBody := fake.lastChangeBody()
	for _, want := range []string{
		"<Action>CREATE</Action>",
		"<Name>_acme-challenge.example.com.</Name>",
		"<Type>TXT</Type>",
		`<Value>&#34;token-value&#34;</Value>`,
		"managed by metrum-ai-router",
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("create body missing %q:\n%s", want, createBody)
		}
	}

	// A wildcard and its apex share the challenge name: the second value must
	// merge into the existing record set, not fail with InvalidChangeBatch.
	if _, err := provider.Create(ctx, "example.com", Record{Type: "TXT", Name: "_acme-challenge.example.com", Value: "wildcard-value", TTL: 60}); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if got := fake.requests[len(fake.requests)-1]; got != "UPSERT _acme-challenge.example.com|TXT" {
		t.Fatalf("second change = %q, want an UPSERT that keeps the first value", got)
	}
	mergeBody := fake.lastChangeBody()
	if !strings.Contains(mergeBody, "token-value") || !strings.Contains(mergeBody, "wildcard-value") {
		t.Fatalf("merge dropped a value:\n%s", mergeBody)
	}

	// Listing exposes one entry per value with its own id.
	records, err := provider.Records(ctx, "example.com")
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want two TXT values", records)
	}
	for _, record := range records {
		if record.Type != "TXT" || !strings.HasPrefix(record.ID, "_acme-challenge.example.com|TXT|") {
			t.Fatalf("record = %+v", record)
		}
	}

	// Removing one challenge value keeps the other in place.
	if err := provider.Delete(ctx, "example.com", "_acme-challenge.example.com|TXT|token-value"); err != nil {
		t.Fatalf("delete one value: %v", err)
	}
	if got := fake.requests[len(fake.requests)-1]; got != "UPSERT _acme-challenge.example.com|TXT" {
		t.Fatalf("delete change = %q, want an UPSERT of the remaining value", got)
	}
	if values := fake.sets["_acme-challenge.example.com|TXT"]; len(values) != 1 || values[0] != `"wildcard-value"` {
		t.Fatalf("remaining values = %v", values)
	}

	// Removing the last value deletes the record set with its exact contents.
	if err := provider.Delete(ctx, "example.com", "_acme-challenge.example.com|TXT|wildcard-value"); err != nil {
		t.Fatalf("delete last value: %v", err)
	}
	if got := fake.requests[len(fake.requests)-1]; got != "DELETE _acme-challenge.example.com|TXT" {
		t.Fatalf("final delete = %q", got)
	}
	if _, exists := fake.sets["_acme-challenge.example.com|TXT"]; exists {
		t.Fatal("record set was not deleted")
	}

	// Non-TXT records use whole-record-set semantics.
	aID, err := provider.Create(ctx, "example.com", Record{Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	if aID != "router.example.com|A" {
		t.Fatalf("A id = %q", aID)
	}
	if err := provider.Update(ctx, "example.com", aID, Record{Type: "A", Name: "router.example.com", Value: "203.0.113.11", TTL: 300}); err != nil {
		t.Fatalf("update A: %v", err)
	}
	updateBody := fake.lastChangeBody()
	if !strings.Contains(updateBody, "<Action>UPSERT</Action>") || !strings.Contains(updateBody, "203.0.113.11") {
		t.Fatalf("update body:\n%s", updateBody)
	}
	if err := provider.Delete(ctx, "example.com", "router.example.com|A"); err != nil {
		t.Fatalf("delete A: %v", err)
	}
	if !strings.Contains(fake.lastChangeBody(), "203.0.113.11") {
		t.Fatal("delete must carry the record's current value")
	}
	if api := fake.api; api.requests[0].header.Get("Authorization") == "" || !strings.Contains(api.requests[0].header.Get("Authorization"), "AKIAEXAMPLE") {
		t.Fatalf("request was not SigV4 signed: %q", api.requests[0].header.Get("Authorization"))
	}
}

func TestRoute53RequiresCredentials(t *testing.T) {
	if _, err := NewRoute53(context.Background(), Route53Credentials{}, Options{}); err == nil {
		t.Fatal("expected an error without credentials")
	}
}

func TestRoute53TXTQuotingRoundTrip(t *testing.T) {
	long := strings.Repeat("a", 300)
	quoted := quoteTXT(long)
	if !strings.Contains(quoted, `" "`) {
		t.Fatalf("long TXT value must be split into adjacent strings: %s", quoted)
	}
	if got := unquoteTXT(quoted); got != long {
		t.Fatalf("round trip = %q", got)
	}
	if got := unquoteTXT(`"with \"quote\" and \\ slash"`); got != `with "quote" and \ slash` {
		t.Fatalf("escaped round trip = %q", got)
	}
}
