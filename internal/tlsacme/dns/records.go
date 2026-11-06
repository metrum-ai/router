// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// markerPrefix is the label prefix of the ownership TXT record that proves this
// router created a managed record. The marker is the only thing that lets a
// later run update or delete the record.
const markerPrefix = "_metrum-router"

// markerValue is the fixed part of an ownership marker value.
const markerValue = "metrum-ai-router-managed=1"

// ReconcileResult counts what one reconciliation did.
type ReconcileResult struct {
	Created   int
	Updated   int
	Unchanged int
	Conflicts int
}

// Managed reports whether any change was made.
func (r ReconcileResult) Managed() int { return r.Created + r.Updated }

// Reconcile makes the managed record match want, idempotently, and never
// touches a record this router does not own.
//
// Ownership is tracked by a TXT marker at _metrum-router.<want.Name>:
//
//	metrum-ai-router-managed=1;owner=<owner>;name=<record>;type=<type>
//
// Rules:
//   - an existing record of that name and type without our marker is a
//     conflict: it is left untouched and the conflict counter rises;
//   - an existing record with a marker for another owner is a conflict too;
//   - a record whose value already matches is not written again;
//   - the record is only ever deleted when its marker is present and ours.
func Reconcile(ctx context.Context, provider Provider, zone string, want Record, owner string, opts Options) (ReconcileResult, error) {
	var result ReconcileResult
	want = Record{
		Type:  strings.ToUpper(strings.TrimSpace(want.Type)),
		Name:  fqdn(want.Name),
		Value: strings.TrimSuffix(strings.TrimSpace(want.Value), "."),
		TTL:   want.TTL,
	}
	if want.TTL <= 0 {
		want.TTL = 300
	}
	if want.Name == "" || want.Type == "" || want.Value == "" {
		return result, fmt.Errorf("dns: managed record needs a name, type and value")
	}
	if !inZone(want.Name, zone) {
		return result, fmt.Errorf("dns: record %q is outside zone %q", want.Name, zone)
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = "unknown"
	}
	marker := markerFor(owner, want)

	records, err := provider.Records(ctx, zone)
	if err != nil {
		return result, fmt.Errorf("dns: list records in %s: %w", zone, err)
	}
	var existing []Record
	var existingMarker *Record
	for i, record := range records {
		if record.Type == "TXT" && fqdn(record.Name) == marker.Name {
			existingMarker = &records[i]
			continue
		}
		if record.Type == want.Type && fqdn(record.Name) == want.Name {
			existing = append(existing, record)
		}
	}

	switch {
	case existingMarker != nil:
		markerOwner, markerName, markerType, ok := parseMarker(*existingMarker)
		if !ok || markerOwner != owner || markerName != want.Name || markerType != want.Type {
			result.Conflicts++
			opts.instruments().DNSConflict(provider.Name(), "foreign_marker")
			opts.logf("dns: %s %s: ownership marker belongs to %q (%s %s); leaving the record untouched",
				want.Type, want.Name, markerOwner, markerType, markerName)
			return result, nil
		}
	case len(existing) > 0:
		// A record exists without our marker: somebody else's.
		result.Conflicts++
		opts.instruments().DNSConflict(provider.Name(), "unowned_record")
		opts.logf("dns: %s %s already exists without an ownership marker; leaving it untouched", want.Type, want.Name)
		return result, nil
	}

	if len(existing) > 1 {
		result.Conflicts++
		opts.instruments().DNSConflict(provider.Name(), "multiple_records")
		opts.logf("dns: %s %s has %d values; refusing to manage a multi-value record", want.Type, want.Name, len(existing))
		return result, nil
	}

	switch {
	case len(existing) == 1:
		current := existing[0]
		if current.Value == want.Value {
			result.Unchanged++
			if existingMarker == nil {
				if _, err := provider.Create(ctx, zone, marker); err != nil {
					return result, fmt.Errorf("dns: create ownership marker %s: %w", marker.Name, err)
				}
			}
			opts.instruments().DNSRecordsManaged(provider.Name(), want.Type, 1)
			return result, nil
		}
		id := current.ID
		if id == "" {
			id = defaultRecordID(current)
		}
		if err := provider.Update(ctx, zone, id, want); err != nil {
			return result, fmt.Errorf("dns: update %s %s: %w", want.Type, want.Name, err)
		}
		result.Updated++
	default:
		if _, err := provider.Create(ctx, zone, want); err != nil {
			return result, fmt.Errorf("dns: create %s %s: %w", want.Type, want.Name, err)
		}
		result.Created++
	}
	if existingMarker == nil {
		if _, err := provider.Create(ctx, zone, marker); err != nil {
			return result, fmt.Errorf("dns: create ownership marker %s: %w", marker.Name, err)
		}
	}
	opts.instruments().DNSRecordsManaged(provider.Name(), want.Type, 1)
	return result, nil
}

// DeleteManaged removes a record only when its ownership marker is present for
// this owner.
func DeleteManaged(ctx context.Context, provider Provider, zone string, want Record, owner string, opts Options) (bool, error) {
	name := fqdn(want.Name)
	typ := strings.ToUpper(strings.TrimSpace(want.Type))
	marker := markerFor(owner, Record{Type: typ, Name: name})
	records, err := provider.Records(ctx, zone)
	if err != nil {
		return false, fmt.Errorf("dns: list records in %s: %w", zone, err)
	}
	var owned bool
	var targetIDs []string
	var markerIDs []string
	for _, record := range records {
		if record.Type == "TXT" && fqdn(record.Name) == marker.Name {
			markerOwner, markerName, markerType, ok := parseMarker(record)
			if ok && markerOwner == owner && markerName == name && markerType == typ {
				owned = true
				markerIDs = append(markerIDs, record.ID)
			}
		}
		if record.Type == typ && fqdn(record.Name) == name {
			targetIDs = append(targetIDs, record.ID)
		}
	}
	if !owned {
		return false, nil
	}
	// The marker goes first so a partial failure can never leave an orphan that
	// looks unowned.
	for _, id := range markerIDs {
		if err := provider.Delete(ctx, zone, id); err != nil {
			return false, fmt.Errorf("dns: delete ownership marker: %w", err)
		}
	}
	for _, id := range targetIDs {
		if err := provider.Delete(ctx, zone, id); err != nil {
			return false, fmt.Errorf("dns: delete %s %s: %w", typ, name, err)
		}
	}
	opts.instruments().DNSRecordsManaged(provider.Name(), typ, -1)
	return true, nil
}

// markerFor builds the ownership TXT record for a managed record.
func markerFor(owner string, want Record) Record {
	return Record{
		Type:  "TXT",
		Name:  markerPrefix + "." + fqdn(want.Name),
		Value: fmt.Sprintf("%s;owner=%s;name=%s;type=%s", markerValue, sanitizeMarkerValue(owner), fqdn(want.Name), strings.ToUpper(want.Type)),
		TTL:   want.TTL,
	}
}

// parseMarker decodes an ownership marker value.
func parseMarker(record Record) (owner, name, recordType string, ok bool) {
	if record.Type != "TXT" {
		return "", "", "", false
	}
	fields := strings.Split(strings.Trim(record.Value, "\""), ";")
	if len(fields) == 0 || strings.TrimSpace(fields[0]) != markerValue {
		return "", "", "", false
	}
	values := map[string]string{}
	for _, field := range fields[1:] {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	owner = values["owner"]
	name = fqdn(values["name"])
	recordType = strings.ToUpper(values["type"])
	if owner == "" || name == "" || recordType == "" {
		return "", "", "", false
	}
	return owner, name, recordType, true
}

// sanitizeMarkerValue keeps a marker value free of the separators it uses.
func sanitizeMarkerValue(value string) string {
	replacer := strings.NewReplacer(";", "_", "=", "_", " ", "_")
	return replacer.Replace(strings.TrimSpace(value))
}

// defaultRecordID builds a stable composite identifier for providers whose API
// has no per-record id.
func defaultRecordID(record Record) string {
	return fqdn(record.Name) + "|" + strings.ToUpper(record.Type)
}

// sortRecords orders records deterministically for provider implementations.
func sortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		if records[i].Type != records[j].Type {
			return records[i].Type < records[j].Type
		}
		return records[i].Value < records[j].Value
	})
}

// configUploadDelay is the wait Route 53 asks clients to observe between a
// change and the records being readable. Tests shorten it to zero.
var configUploadDelay = time.Second
