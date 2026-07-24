package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFlatSQLStoreQueriesOMMEpochProfilesByCatalogObject(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "catalogfixture-gp", BatchID: "batch-001", ContentKeyID: "public"}
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS-BACKFILL").
		WithObjectID("1998-067A").
		WithEpoch("2026-05-10T12:00:00Z").
		Build(), "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store backfill OMM failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS-FORWARD").
		WithObjectID("1998-067A").
		WithEpoch("2026-05-12T12:00:00Z").
		Build(), "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store forward OMM failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().
		WithNoradCatID(40909).
		WithObjectName("OTHER-EXACT").
		WithObjectID("2015-049A").
		WithEpoch("2026-05-11T18:00:00Z").
		Build(), "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store exact OMM failed: %v", err)
	}

	profile, ok := EpochProfileForSchema("OMM.fbs")
	if !ok {
		t.Fatal("OMM epoch profile was not registered")
	}
	if profile.EntityKeyField != "NORAD_CAT_ID" || profile.EpochField != "EPOCH" {
		t.Fatalf("unexpected OMM epoch profile: %#v", profile)
	}

	target := mustParseTime(t, "2026-05-11T12:00:00Z")
	asOf, err := store.QueryEpochRecords(EpochRecordQuery{
		SchemaName:    "OMM.fbs",
		Profile:       EpochProfileAsOf,
		At:            target,
		ProviderID:    "space-data-network-02",
		SourceName:    "catalogfixture-gp",
		Limit:         10,
		IncludeSource: true,
	})
	if err != nil {
		t.Fatalf("QueryEpochRecords as_of failed: %v", err)
	}
	if len(asOf) != 1 {
		t.Fatalf("as_of returned %d records, want 1: %#v", len(asOf), asOf)
	}
	if asOf[0].EntityKey != "25544" || asOf[0].MatchType != EpochMatchBackfill || asOf[0].DeltaSeconds != 86400 {
		t.Fatalf("unexpected as_of first match: %#v", asOf[0])
	}
	forward, err := store.QueryEpochRecords(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileForward,
		At:         target,
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryEpochRecords forward failed: %v", err)
	}
	if len(forward) != 2 {
		t.Fatalf("forward returned %d records, want 2: %#v", len(forward), forward)
	}
	if forward[0].EntityKey != "25544" || forward[0].MatchType != EpochMatchForwardFill || forward[0].DeltaSeconds != 86400 {
		t.Fatalf("unexpected forward first match: %#v", forward[0])
	}
	if forward[1].EntityKey != "40909" || forward[1].MatchType != EpochMatchForwardFill || forward[1].DeltaSeconds != 21600 {
		t.Fatalf("unexpected forward second match: %#v", forward[1])
	}

	nearest, err := store.QueryEpochRecords(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileNearest,
		At:         target,
		NoradCatID: uint32Ptr(25544),
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryEpochRecords nearest failed: %v", err)
	}
	if len(nearest) != 1 || nearest[0].MatchType != EpochMatchNearest || nearest[0].MatchedEpoch.Format(time.RFC3339) != "2026-05-10T12:00:00Z" {
		t.Fatalf("nearest tie should prefer backfill, got %#v", nearest)
	}

	coverage, err := store.QueryEpochCoverage(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileCoverage,
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
	})
	if err != nil {
		t.Fatalf("QueryEpochCoverage failed: %v", err)
	}
	if got, want := len(coverage), 3; got != want {
		t.Fatalf("coverage days = %d, want %d: %#v", got, want, coverage)
	}
	if coverage[1].Day != "2026-05-11" || coverage[1].Count != 1 {
		t.Fatalf("unexpected coverage middle day: %#v", coverage[1])
	}
}

func TestFlatSQLStoreCountsEpochProfilesWithoutApplyingPageLimit(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "catalogfixture-gp", BatchID: "batch-001", ContentKeyID: "public"}
	storeOMMEpochProfileTestRecord(t, store, 25544, "ISS-BACKFILL", "2026-05-10T12:00:00Z", tags)
	storeOMMEpochProfileTestRecord(t, store, 25544, "ISS-FORWARD", "2026-05-12T12:00:00Z", tags)
	storeOMMEpochProfileTestRecord(t, store, 40909, "DAY-A", "2026-05-11T06:00:00Z", tags)
	storeOMMEpochProfileTestRecord(t, store, 41000, "DAY-B", "2026-05-11T18:00:00Z", tags)
	storeOMMEpochProfileTestRecord(t, store, 50000, "FUTURE-ONLY", "2026-05-13T00:00:00Z", tags)

	dayCount, err := store.CountEpochRecords(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileDay,
		Day:        "2026-05-11",
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("CountEpochRecords day failed: %v", err)
	}
	if dayCount != 2 {
		t.Fatalf("day count = %d, want 2", dayCount)
	}

	target := mustParseTime(t, "2026-05-11T12:00:00Z")
	asOfCount, err := store.CountEpochRecords(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileAsOf,
		At:         target,
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("CountEpochRecords as_of failed: %v", err)
	}
	if asOfCount != 2 {
		t.Fatalf("as_of count = %d, want 2", asOfCount)
	}

	nearestCount, err := store.CountEpochRecords(EpochRecordQuery{
		SchemaName:      "OMM.fbs",
		Profile:         EpochProfileNearest,
		At:              target,
		ProviderID:      "space-data-network-02",
		SourceName:      "catalogfixture-gp",
		MaxDeltaSeconds: 6 * 3600,
		Limit:           1,
	})
	if err != nil {
		t.Fatalf("CountEpochRecords nearest failed: %v", err)
	}
	if nearestCount != 2 {
		t.Fatalf("nearest count = %d, want 2", nearestCount)
	}
}

func TestFlatSQLStoreQueriesOMMEpochWindowAsHalfOpenRange(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "catalogfixture-gp", BatchID: "batch-001", ContentKeyID: "public"}
	storeOMMEpochProfileTestRecord(t, store, 10001, "WINDOW-START", "2026-05-11T00:00:00Z", tags)
	storeOMMEpochProfileTestRecord(t, store, 10002, "WINDOW-MIDDLE", "2026-05-11T12:00:00Z", tags)
	storeOMMEpochProfileTestRecord(t, store, 10003, "WINDOW-END", "2026-05-12T00:00:00Z", tags)

	from := mustParseTime(t, "2026-05-11T00:00:00Z")
	to := mustParseTime(t, "2026-05-12T00:00:00Z")
	matches, err := store.QueryEpochRecords(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileWindow,
		From:       &from,
		To:         &to,
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryEpochRecords window failed: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("window returned %d records, want 2: %#v", len(matches), matches)
	}
	for _, match := range matches {
		if match.MatchedEpoch.Equal(to) {
			t.Fatalf("half-open window included upper bound: %#v", match)
		}
	}
}

func storeOMMEpochProfileTestRecord(t *testing.T, store *FlatSQLStore, norad uint32, objectName, epoch string, tags SourceTags) {
	t.Helper()
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(objectName).
		WithObjectID("TEST").
		WithEpoch(epoch).
		Build(), "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store OMM %s failed: %v", objectName, err)
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

func uint32Ptr(value uint32) *uint32 {
	return &value
}
