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

// Managed reports how many records the reconciliation changed.
func (r ReconcileResult) Managed() int { return r.Created + r.Updated }

// Reconcile makes the managed record match want, idempotently, and never
// touches a record this router does not own.
//
// Ownership is tracked by a TXT marker at _metrum-router.<want.Name>:
//
//	metrum-ai-router-managed=1;owner=<owner>;name=<record>;type=<type>;id=<id>
//
// The marker also records the provider id of the record it owns, so a record
// that somebody deleted and recreated with the same name is not silently taken
// over: its id no longer matches the marker and the run reports a conflict.
//
// Rules:
//   - a record of that name and type without our marker is a conflict: it is
//     left untouched and the conflict counter rises;
//   - a marker for another owner is a conflict too;
//   - a marker whose id no longer matches the existing record is a conflict;
//   - a record whose value already matches is not written again;
//   - a record is only ever deleted with its marker, by the id in the marker.
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
	markerName := markerPrefix + "." + want.Name

	records, err := provider.Records(ctx, zone)
	if err != nil {
		return result, fmt.Errorf("dns: list records in %s: %w", zone, err)
	}
	var existing []Record
	var marker *Record
	for i := range records {
		if records[i].Type == "TXT" && fqdn(records[i].Name) == markerName {
			marker = &records[i]
			continue
		}
		if records[i].Type == want.Type && fqdn(records[i].Name) == want.Name {
			existing = append(existing, records[i])
		}
	}
	if len(existing) > 1 {
		result.Conflicts++
		opts.instruments().DNSConflict(provider.Name(), "multiple_records")
		opts.logf("dns: %s %s has %d values; refusing to manage a multi-value record", want.Type, want.Name, len(existing))
		return result, nil
	}

	if marker != nil {
		markerOwner, markerNameValue, markerType, markerID, ok := parseMarker(*marker)
		if !ok || markerOwner != owner || markerNameValue != want.Name || markerType != want.Type {
			result.Conflicts++
			opts.instruments().DNSConflict(provider.Name(), "foreign_marker")
			opts.logf("dns: %s %s: ownership marker belongs to another owner (%s); leaving the record untouched",
				want.Type, want.Name, markerOwner)
			return result, nil
		}
		if len(existing) == 1 {
			current := existing[0]
			if markerID != "" && current.ID != "" && current.ID != markerID {
				result.Conflicts++
				opts.instruments().DNSConflict(provider.Name(), "replaced_record")
				opts.logf("dns: %s %s was replaced outside the router (record id %s, marker id %s); leaving it untouched",
					want.Type, want.Name, current.ID, markerID)
				return result, nil
			}
			if current.Value == want.Value {
				result.Unchanged++
				if err := ensureMarkerIDAttached(ctx, provider, zone, *marker, current.ID); err != nil {
					return result, err
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
			if err := ensureMarkerIDAttached(ctx, provider, zone, *marker, id); err != nil {
				return result, err
			}
			opts.instruments().DNSRecordsManaged(provider.Name(), want.Type, 1)
			return result, nil
		}
		// Our marker exists but the record does not (deleted by hand): recreate
		// it and re-attach the id.
		id, err := provider.Create(ctx, zone, want)
		if err != nil {
			return result, fmt.Errorf("dns: create %s %s: %w", want.Type, want.Name, err)
		}
		result.Created++
		if err := ensureMarkerIDAttached(ctx, provider, zone, *marker, id); err != nil {
			return result, err
		}
		opts.instruments().DNSRecordsManaged(provider.Name(), want.Type, 1)
		return result, nil
	}

	if len(existing) == 1 {
		// A record exists without our marker: somebody else's.
		result.Conflicts++
		opts.instruments().DNSConflict(provider.Name(), "unowned_record")
		opts.logf("dns: %s %s already exists without an ownership marker; leaving it untouched", want.Type, want.Name)
		return result, nil
	}

	// Nothing exists: claim ownership with the marker first, so a crash between
	// the two writes leaves a recoverable state (marker without id) rather than
	// an unowned record that the next run would refuse.
	claim := markerFor(owner, want, "")
	markerID, err := provider.Create(ctx, zone, claim)
	if err != nil {
		return result, fmt.Errorf("dns: create ownership marker %s: %w", claim.Name, err)
	}
	createdID, err := provider.Create(ctx, zone, want)
	if err != nil {
		// Keep the claim: the next run adopts it and retries the record.
		return result, fmt.Errorf("dns: create %s %s: %w", want.Type, want.Name, err)
	}
	result.Created++
	if err := ensureMarkerIDAttached(ctx, provider, zone, Record{ID: markerID, Name: claim.Name, Type: "TXT", Value: claim.Value, TTL: claim.TTL}, createdID); err != nil {
		return result, err
	}
	opts.instruments().DNSRecordsManaged(provider.Name(), want.Type, 1)
	return result, nil
}

// DeleteManaged removes a record only when its ownership marker is present for
// this owner and names that record's id.
func DeleteManaged(ctx context.Context, provider Provider, zone string, want Record, owner string, opts Options) (bool, error) {
	name := fqdn(want.Name)
	typ := strings.ToUpper(strings.TrimSpace(want.Type))
	markerName := markerPrefix + "." + name
	records, err := provider.Records(ctx, zone)
	if err != nil {
		return false, fmt.Errorf("dns: list records in %s: %w", zone, err)
	}
	var markerRecord *Record
	var markerID string
	for i := range records {
		if records[i].Type != "TXT" || fqdn(records[i].Name) != markerName {
			continue
		}
		markerOwner, markerNameValue, markerType, ownedID, ok := parseMarker(records[i])
		if ok && markerOwner == owner && markerNameValue == name && markerType == typ {
			markerRecord = &records[i]
			markerID = ownedID
		}
	}
	if markerRecord == nil {
		return false, nil
	}
	if markerID == "" {
		// Ownership was claimed before the record was created; nothing to do.
		if err := provider.Delete(ctx, zone, markerRecord.ID); err != nil {
			return false, fmt.Errorf("dns: delete ownership marker: %w", err)
		}
		return true, nil
	}
	// The marker goes first so a partial failure never leaves an orphan that
	// looks owned.
	if err := provider.Delete(ctx, zone, markerRecord.ID); err != nil {
		return false, fmt.Errorf("dns: delete ownership marker: %w", err)
	}
	if err := provider.Delete(ctx, zone, markerID); err != nil {
		return false, fmt.Errorf("dns: delete %s %s: %w", typ, name, err)
	}
	opts.instruments().DNSRecordsManaged(provider.Name(), typ, -1)
	return true, nil
}

// ensureMarkerIDAttached records the managed record's provider id in the marker
// when it is missing or stale.
func ensureMarkerIDAttached(ctx context.Context, provider Provider, zone string, marker Record, recordID string) error {
	if recordID == "" || marker.ID == "" {
		return nil
	}
	owner, name, typ, existingID, ok := parseMarker(marker)
	if !ok || existingID == recordID {
		return nil
	}
	updated := marker
	updated.Value = markerValue + fmt.Sprintf(";owner=%s;name=%s;type=%s;id=%s",
		sanitizeMarkerValue(owner), name, typ, sanitizeMarkerValue(recordID))
	updated.TTL = marker.TTL
	if err := provider.Update(ctx, zone, marker.ID, updated); err != nil {
		return fmt.Errorf("dns: update ownership marker %s: %w", marker.Name, err)
	}
	return nil
}

// markerFor builds the ownership TXT record for a managed record.
func markerFor(owner string, want Record, recordID string) Record {
	value := fmt.Sprintf("%s;owner=%s;name=%s;type=%s",
		markerValue, sanitizeMarkerValue(owner), fqdn(want.Name), strings.ToUpper(want.Type))
	if recordID != "" {
		value += ";id=" + sanitizeMarkerValue(recordID)
	}
	return Record{
		Type:  "TXT",
		Name:  markerPrefix + "." + fqdn(want.Name),
		Value: value,
		TTL:   want.TTL,
	}
}

// parseMarker decodes an ownership marker value, including the record id that
// binds the marker to one provider record.
func parseMarker(record Record) (owner, name, recordType, recordID string, ok bool) {
	if record.Type != "TXT" {
		return "", "", "", "", false
	}
	fields := strings.Split(strings.Trim(record.Value, "\""), ";")
	if len(fields) == 0 || strings.TrimSpace(fields[0]) != markerValue {
		return "", "", "", "", false
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
	recordID = values["id"]
	if owner == "" || name == "" || recordType == "" {
		return "", "", "", "", false
	}
	return owner, name, recordType, recordID, true
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
