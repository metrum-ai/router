// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const route53NS = `xmlns="https://route53.amazonaws.com/doc/2013-04-01/"`

func TestRoute53ProviderCalls(t *testing.T) {
	changes := 0
	insyncPolls := 0
	var changeBodies []string
	var recordSets = `<?xml version="1.0"?>
<ListResourceRecordSetsResponse ` + route53NS + `>
  <ResourceRecordSets>
    <ResourceRecordSet>
      <Name>_acme-challenge.example.com.</Name>
      <Type>TXT</Type>
      <TTL>60</TTL>
      <ResourceRecords><ResourceRecord><Value>"token-value"</Value></ResourceRecord></ResourceRecords>
    </ResourceRecordSet>
  </ResourceRecordSets>
  <IsTruncated>false</IsTruncated>
</ListResourceRecordSetsResponse>`

	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case strings.HasSuffix(req.path, "/hostedzonesbyname"):
			fmt.Fprintf(w, `<?xml version="1.0"?><ListHostedZonesByNameResponse %s><HostedZones><HostedZone><Id>/hostedzone/Z1</Id><Name>example.com.</Name><CallerReference>ref</CallerReference></HostedZone></HostedZones><DNSName>example.com.</DNSName><HostedZoneId>Z1</HostedZoneId><MaxItems>100</MaxItems><IsTruncated>false</IsTruncated></ListHostedZonesByNameResponse>`, route53NS)
		case strings.HasSuffix(req.path, "/rrset") && req.method == http.MethodGet:
			fmt.Fprint(w, recordSets)
		case strings.HasSuffix(req.path, "/rrset") && req.method == http.MethodPost:
			changes++
			changeBodies = append(changeBodies, req.body)
			fmt.Fprintf(w, `<?xml version="1.0"?><ChangeResourceRecordSetsResponse %s><ChangeInfo><Id>/change/C1</Id><Status>PENDING</Status><SubmittedAt>2026-11-06T00:00:00Z</SubmittedAt></ChangeInfo></ChangeResourceRecordSetsResponse>`, route53NS)
		case strings.Contains(req.path, "/change/") && req.method == http.MethodGet:
			insyncPolls++
			status := "PENDING"
			if insyncPolls >= 2 {
				status = "INSYNC"
			}
			fmt.Fprintf(w, `<?xml version="1.0"?><GetChangeResponse %s><ChangeInfo><Id>/change/C1</Id><Status>%s</Status><SubmittedAt>2026-11-06T00:00:00Z</SubmittedAt></ChangeInfo></GetChangeResponse>`, route53NS, status)
		default:
			t.Errorf("unexpected request %s", req)
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	provider, err := NewRoute53(context.Background(), Route53Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"}, Options{Endpoint: api.server.URL})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	provider.changePollInterval = time.Millisecond
	provider.changeTimeout = 5 * time.Second
	ctx := context.Background()

	zones, err := provider.Zones(ctx)
	if err != nil || len(zones) != 1 || zones[0] != "example.com" {
		t.Fatalf("zones = %v, %v", zones, err)
	}

	records, err := provider.Records(ctx, "example.com")
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 1 || records[0].Value != "token-value" || records[0].Name != "_acme-challenge.example.com" {
		t.Fatalf("records = %+v (TXT quoting must be stripped)", records)
	}

	id, err := provider.Create(ctx, "example.com", Record{Type: "TXT", Name: "_acme-challenge.example.com", Value: "token-value", TTL: 60})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "_acme-challenge.example.com|TXT" {
		t.Fatalf("create id = %q", id)
	}
	if err := provider.Update(ctx, "example.com", id, Record{Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := provider.Delete(ctx, "example.com", "_acme-challenge.example.com|TXT"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if changes != 3 {
		t.Fatalf("change calls = %d, want 3", changes)
	}
	create, update, del := changeBodies[0], changeBodies[1], changeBodies[2]
	for _, want := range []string{
		"<Action>CREATE</Action>",
		"<Name>_acme-challenge.example.com.</Name>",
		"<Type>TXT</Type>",
		`<Value>&#34;token-value&#34;</Value>`,
		"managed by metrum-ai-router",
	} {
		if !strings.Contains(create, want) {
			t.Fatalf("create body missing %q:\n%s", want, create)
		}
	}
	for _, want := range []string{"<Action>UPSERT</Action>", "<Name>router.example.com.</Name>", "<Type>A</Type>", "<Value>203.0.113.10</Value>"} {
		if !strings.Contains(update, want) {
			t.Fatalf("update body missing %q:\n%s", want, update)
		}
	}
	// The DELETE must carry the record's current value or Route 53 rejects it.
	for _, want := range []string{"<Action>DELETE</Action>", `<Value>&#34;token-value&#34;</Value>`} {
		if !strings.Contains(del, want) {
			t.Fatalf("delete body missing %q:\n%s", want, del)
		}
	}
	if insyncPolls < 2 {
		t.Fatalf("GetChange polled %d times, want at least 2 (PENDING then INSYNC)", insyncPolls)
	}
	// The AWS SDK must have signed the requests.
	if api.requests[0].header.Get("Authorization") == "" {
		t.Fatal("request was not SigV4 signed")
	}
	if !strings.Contains(api.requests[0].header.Get("Authorization"), "AKIAEXAMPLE") {
		t.Fatalf("Authorization does not name the access key: %q", api.requests[0].header.Get("Authorization"))
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
	if !strings.Contains(quoted, `" `) {
		t.Fatalf("long TXT value must be split into adjacent strings: %s", quoted)
	}
	if got := unquoteTXT(quoted); got != long {
		t.Fatalf("round trip = %q", got)
	}
	if got := unquoteTXT(`"with \"quote\" and \\ slash"`); got != `with "quote" and \ slash` {
		t.Fatalf("escaped round trip = %q", got)
	}
}
