// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// Route53Credentials is the resolved Route 53 authentication.
type Route53Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// UseAmbientCredentials uses the AWS default chain (IRSA, IMDS, profile),
	// which is the recommended deployment for EKS.
	UseAmbientCredentials bool
}

// Route53 is the Amazon Route 53 provider.
type Route53 struct {
	client *route53.Client
	opts   Options
	// changePollInterval and changeTimeout bound the INSYNC wait.
	changePollInterval time.Duration
	changeTimeout      time.Duration
}

// NewRoute53 builds the provider. The endpoint override exists for tests: the
// AWS SDK signs a normal request and our test server asserts the exact XML.
func NewRoute53(ctx context.Context, creds Route53Credentials, opts Options) (*Route53, error) {
	var loadOptions []func(*awsconfig.LoadOptions) error
	if !creds.UseAmbientCredentials {
		if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
			return nil, fmt.Errorf("dns: route53 needs an access key id and secret access key, or use_ambient_credentials")
		}
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)))
	}
	if endpoint := strings.TrimSpace(opts.Endpoint); endpoint != "" {
		loadOptions = append(loadOptions, awsconfig.WithBaseEndpoint(endpoint))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("dns: route53 load AWS configuration: %w", err)
	}
	client := route53.NewFromConfig(cfg, func(o *route53.Options) {
		if endpoint := strings.TrimSpace(opts.Endpoint); endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &Route53{
		client:             client,
		opts:               opts,
		changePollInterval: 2 * time.Second,
		changeTimeout:      60 * time.Second,
	}, nil
}

// Name implements Provider.
func (r *Route53) Name() string { return "route53" }

// Zones lists the hosted zones of the account.
func (r *Route53) Zones(ctx context.Context) ([]string, error) {
	var zones []string
	input := &route53.ListHostedZonesByNameInput{MaxItems: aws.Int32(100)}
	for {
		out, err := r.client.ListHostedZonesByName(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("dns: route53 list hosted zones: %w", r.redactError(err))
		}
		for _, zone := range out.HostedZones {
			zones = append(zones, fqdn(aws.ToString(zone.Name)))
		}
		if !out.IsTruncated || aws.ToString(out.NextDNSName) == "" {
			break
		}
		input = &route53.ListHostedZonesByNameInput{
			DNSName:      out.NextDNSName,
			HostedZoneId: out.NextHostedZoneId,
			MaxItems:     aws.Int32(100),
		}
	}
	return zones, nil
}

// Records lists the record sets of a zone.
func (r *Route53) Records(ctx context.Context, zone string) ([]Record, error) {
	zoneID, err := r.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}
	var out []Record
	paginator := route53.NewListResourceRecordSetsPaginator(r.client, &route53.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("dns: route53 list records in %s: %w", zone, r.redactError(err))
		}
		for _, set := range page.ResourceRecordSets {
			name := fqdn(aws.ToString(set.Name))
			recordType := string(set.Type)
			if recordType == "TXT" {
				// A TXT record set may carry several values; each is one entry.
				for _, value := range set.ResourceRecords {
					out = append(out, Record{
						ID:    defaultRecordID(Record{Name: name, Type: recordType}),
						Type:  recordType,
						Name:  name,
						Value: unquoteTXT(aws.ToString(value.Value)),
						TTL:   int(aws.ToInt64(set.TTL)),
					})
				}
				continue
			}
			values := make([]string, 0, len(set.ResourceRecords))
			for _, value := range set.ResourceRecords {
				values = append(values, aws.ToString(value.Value))
			}
			out = append(out, Record{
				ID:    defaultRecordID(Record{Name: name, Type: recordType}),
				Type:  recordType,
				Name:  name,
				Value: strings.Join(values, "\n"),
				TTL:   int(aws.ToInt64(set.TTL)),
			})
		}
	}
	sortRecords(out)
	return out, nil
}

// Create adds a record set and returns its composite id.
func (r *Route53) Create(ctx context.Context, zone string, record Record) (string, error) {
	if err := r.change(ctx, zone, types.ChangeActionCreate, record); err != nil {
		return "", err
	}
	return defaultRecordID(record), nil
}

// Update replaces a single-value record set (UPSERT).
func (r *Route53) Update(ctx context.Context, zone string, id string, record Record) error {
	_ = id
	return r.change(ctx, zone, types.ChangeActionUpsert, record)
}

// Delete removes the record set, using its current values so Route 53 accepts
// the DELETE.
func (r *Route53) Delete(ctx context.Context, zone string, id string) error {
	name, recordType, ok := splitRecordID(id)
	if !ok {
		return fmt.Errorf("dns: route53 delete: malformed record id %q", id)
	}
	records, err := r.Records(ctx, zone)
	if err != nil {
		return err
	}
	var current *Record
	for i, record := range records {
		if record.Type == recordType && record.Name == name {
			current = &records[i]
			break
		}
	}
	if current == nil {
		return nil
	}
	return r.change(ctx, zone, types.ChangeActionDelete, *current)
}

func (r *Route53) change(ctx context.Context, zone string, action types.ChangeAction, record Record) error {
	zoneID, err := r.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	values := strings.Split(record.Value, "\n")
	resourceRecords := make([]types.ResourceRecord, 0, len(values))
	for _, value := range values {
		if record.Type == "TXT" {
			value = quoteTXT(value)
		}
		resourceRecords = append(resourceRecords, types.ResourceRecord{Value: aws.String(value)})
	}
	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}
	comment := fmt.Sprintf("managed by metrum-ai-router (%s %s %s)", action, record.Type, fqdn(record.Name))
	out, err := r.client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &types.ChangeBatch{
			Comment: aws.String(comment),
			Changes: []types.Change{{
				Action: action,
				ResourceRecordSet: &types.ResourceRecordSet{
					// Route 53 expects the fully qualified name with its
					// trailing dot.
					Name:            aws.String(fqdn(absoluteName(record.Name, zone)) + "."),
					Type:            types.RRType(strings.ToUpper(record.Type)),
					TTL:             aws.Int64(int64(ttl)),
					ResourceRecords: resourceRecords,
				},
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("dns: route53 %s %s %s: %w", action, record.Type, record.Name, r.redactError(err))
	}
	if out.ChangeInfo == nil || out.ChangeInfo.Id == nil {
		return nil
	}
	return r.waitInsync(ctx, aws.ToString(out.ChangeInfo.Id))
}

// waitInsync polls GetChange until the change is INSYNC, which is what makes a
// presented challenge record readable.
func (r *Route53) waitInsync(ctx context.Context, changeID string) error {
	deadline := time.Now().Add(r.changeTimeout)
	for {
		out, err := r.client.GetChange(ctx, &route53.GetChangeInput{Id: aws.String(changeID)})
		if err != nil {
			return fmt.Errorf("dns: route53 get change: %w", r.redactError(err))
		}
		if out.ChangeInfo != nil && out.ChangeInfo.Status == types.ChangeStatusInsync {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dns: route53 change %s did not reach INSYNC within %s", changeID, r.changeTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.changePollInterval):
		}
	}
}

func (r *Route53) zoneID(ctx context.Context, zone string) (string, error) {
	zone = fqdn(zone)
	out, err := r.client.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{
		DNSName:  aws.String(zone),
		MaxItems: aws.Int32(100),
	})
	if err != nil {
		return "", fmt.Errorf("dns: route53 look up zone %s: %w", zone, r.redactError(err))
	}
	for _, hosted := range out.HostedZones {
		if fqdn(aws.ToString(hosted.Name)) != zone {
			continue
		}
		id := strings.TrimPrefix(aws.ToString(hosted.Id), "/hostedzone/")
		if id == "" {
			return "", fmt.Errorf("dns: route53 zone %s has no id", zone)
		}
		return id, nil
	}
	return "", fmt.Errorf("dns: route53 zone %q not found for these credentials", zone)
}

// redactError keeps credentials out of SDK error text.
func (r *Route53) redactError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", r.opts.redact(err.Error()))
}

// splitRecordID reverses defaultRecordID.
func splitRecordID(id string) (string, string, bool) {
	name, recordType, found := strings.Cut(strings.TrimSpace(id), "|")
	if !found || name == "" || recordType == "" {
		return "", "", false
	}
	return fqdn(name), strings.ToUpper(recordType), true
}

// quoteTXT renders a TXT value the way Route 53 stores it, splitting values
// longer than 255 bytes into adjacent quoted strings.
func quoteTXT(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	var parts []string
	for len(escaped) > 255 {
		parts = append(parts, `"`+escaped[:255]+`"`)
		escaped = escaped[255:]
	}
	parts = append(parts, `"`+escaped+`"`)
	return strings.Join(parts, " ")
}

// unquoteTXT reverses quoteTXT for a value returned by Route 53.
func unquoteTXT(value string) string {
	var b strings.Builder
	inQuotes := false
	escaped := false
	for _, ch := range value {
		switch {
		case escaped:
			b.WriteRune(ch)
			escaped = false
		case ch == '\\' && inQuotes:
			escaped = true
		case ch == '"':
			inQuotes = !inQuotes
		case inQuotes:
			b.WriteRune(ch)
		}
	}
	if b.Len() == 0 {
		return strings.Trim(value, `"`)
	}
	return b.String()
}

// ensure the provider satisfies the interface at compile time.
var _ Provider = (*Route53)(nil)
var _ Provider = (*Cloudflare)(nil)
var _ Provider = (*DigitalOcean)(nil)
