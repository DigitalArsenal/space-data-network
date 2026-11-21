package storage

// Third review of stack #651 (unlimited SQL visibility) and stack #848 (no
// silent truncation): the setting that decides a stream store's visibility,
// the answer to a statement naming a relation the store does not have, and
// QueryAll's truncation report.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// Unset, the store's layout decides: a build whose default is bounded opens
// a store a later build (or an explicit setting) promoted, for example after
// the update channel rolled back to it; a build whose engine supports layout
// 2 creates a fresh store unlimited (it holds no records a rollback needs)
// and keeps an existing bounded store bounded. Set, the setting decides, and
// false refuses a promoted store instead of silently bounding it.
func TestFormat4VisibilitySettingFollowsStore(t *testing.T) {
	setting := func(opts ...StoreOption) storeConfig {
		var c storeConfig
		for _, o := range opts {
			o(&c)
		}
		return c
	}
	prev := format4StampedLayout
	t.Cleanup(func() { format4StampedLayout = prev })
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		cfg     storeConfig
		layout  uint16
		fresh   bool
		stamped int
		want    bool
		refused bool
	}{
		{"unset, layout 1, layout-1 build", setting(WithUnlimitedSQLVisibilitySetting(nil)), 1, false, 1, false, false},
		{"unset, layout 1, layout-2 build (an existing bounded store stays bounded)", setting(WithUnlimitedSQLVisibilitySetting(nil)), 1, false, 2, false, false},
		{"unset, fresh, layout-1 build", setting(), 0, true, 1, false, false},
		{"unset, fresh, layout-2 build (created unlimited)", setting(), 0, true, 2, true, false},
		{"unset, layout 2, layout-1 stamp still opens it", setting(WithUnlimitedSQLVisibilitySetting(nil)), 2, false, 1, true, false},
		{"unset, layout 2, layout-2 build", setting(WithUnlimitedSQLVisibilitySetting(nil)), 2, false, 2, true, false},
		{"true, layout 1 (promotes)", setting(WithUnlimitedSQLVisibilitySetting(&yes)), 1, false, 2, true, false},
		{"true, fresh", setting(WithUnlimitedSQLVisibility(true)), 0, true, 1, true, false},
		{"true, layout 2", setting(WithUnlimitedSQLVisibility(true)), 2, false, 2, true, false},
		{"false, layout 1", setting(WithUnlimitedSQLVisibilitySetting(&no)), 1, false, 2, false, false},
		{"false, fresh, layout-2 build (created bounded)", setting(WithUnlimitedSQLVisibility(false)), 0, true, 2, false, false},
		{"false, layout 2 refuses", setting(WithUnlimitedSQLVisibility(false)), 2, false, 2, false, true},
	} {
		format4StampedLayout = tc.stamped
		got, err := format4UnlimitedVisibility(tc.cfg, marker.Markers{Format: 5, Layout: tc.layout, RequiredLayout: tc.layout}, tc.fresh)
		if tc.refused {
			if err == nil || !strings.Contains(err.Error(), "unlimited_sql_visibility") {
				t.Fatalf("%s: want a refusal naming the setting, got %v, %v", tc.name, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

// The rollout rule for the first release that pins a layout-2 engine, on
// real stores: with the setting unset, a new store is created at layout 2
// with unlimited visibility; a store created bounded stays at layout 1 and
// bounded; a promoted store stays unlimited. A build stamped layout 1 (the
// published pin before that release) creates a new store bounded.
func TestFormat4UnsetSettingCreatesNewStoreUnlimited(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		prev := format4StampedLayout
		t.Cleanup(func() { format4StampedLayout = prev })
		open := func(root string, opts ...StoreOption) (bool, marker.Markers) {
			t.Helper()
			s := openFormat4ForTest(t, root, opts...)
			unlimited := s.f4.opt.UnlimitedVisibility
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			m, err := marker.Read(root)
			if err != nil {
				t.Fatal(err)
			}
			return unlimited, m
		}

		format4StampedLayout = 2
		fresh := t.TempDir()
		if unlimited, m := open(fresh); !unlimited || m.Layout != 2 || m.RequiredLayout != 2 {
			t.Fatalf("new store, unset, layout-2 build: unlimited=%v markers=%+v", unlimited, m)
		}
		if unlimited, m := open(fresh); !unlimited || m.Layout != 2 {
			t.Fatalf("reopened new store, unset: unlimited=%v markers=%+v", unlimited, m)
		}

		bounded := t.TempDir()
		if unlimited, m := open(bounded, WithUnlimitedSQLVisibility(false)); unlimited || m.Layout != 1 {
			t.Fatalf("new store, false: unlimited=%v markers=%+v", unlimited, m)
		}
		if unlimited, m := open(bounded); unlimited || m.Layout != 1 || m.RequiredLayout != 1 {
			t.Fatalf("existing bounded store, unset, layout-2 build: unlimited=%v markers=%+v", unlimited, m)
		}
		if unlimited, m := open(bounded, WithUnlimitedSQLVisibility(true)); !unlimited || m.Layout != 2 {
			t.Fatalf("existing bounded store, true: unlimited=%v markers=%+v", unlimited, m)
		}
		if unlimited, m := open(bounded); !unlimited || m.Layout != 2 {
			t.Fatalf("promoted store, unset: unlimited=%v markers=%+v", unlimited, m)
		}

		format4StampedLayout = 1
		older := t.TempDir()
		if unlimited, m := open(older); unlimited || m.Layout != 1 {
			t.Fatalf("new store, unset, layout-1 build: unlimited=%v markers=%+v", unlimited, m)
		}
	})
}

// A statement naming a relation the stream store does not have is an error:
// an empty answer reads as "no records" (a module asking for a standard this
// node does not know, or for a field-sealed one; QQQ is no SDS standard: MOR
// was the example until SDS 1.289.0 made it one). A registered standard with
// no records still answers an empty stream.
func TestFormat4RawStreamNoRelationIsAnError(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		s := openFormat4ForTest(t, t.TempDir())
		defer s.Close()
		st, err := s.QueryRawStream("SELECT _data FROM CAT")
		if err != nil || st == nil || st.Rows != 0 {
			t.Fatalf("a registered standard with no records: stream=%v err=%v", st, err)
		}
		for _, q := range []string{"SELECT _data FROM QQQ", "SELECT COUNT(*) FROM OMMM", `SELECT _data FROM "OMM@no-such-source"`} {
			st, err := s.QueryRawStream(q)
			if !errors.Is(err, ErrNoSQLRelation) {
				t.Fatalf("%s: want ErrNoSQLRelation, got stream=%v err=%v", q, st, err)
			}
		}
	})
}

// QueryAll answers the newest records and says when more exist: never the
// newest N presented as all of them.
func TestFormat4QueryAllReportsTruncation(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		s := openFormat4ForTest(t, t.TempDir())
		defer s.Close()
		base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		var recs [][]byte
		for i := 0; i < 30; i++ {
			recs = append(recs, f2TestOMM(uint32(41000+i), base.Add(time.Duration(i)*time.Minute), "R651-"+string(rune('A'+i%26))))
		}
		if n, err := s.StoreBatch("OMM.fbs", recs, "peer-review", nil); err != nil || n != len(recs) {
			t.Fatalf("store: %d %v", n, err)
		}
		for _, tc := range []struct {
			limit, want, wantLimit int
			truncated              bool
		}{
			{30, 30, 30, false},
			{31, 30, 31, false},
			{29, 29, 29, true},
			{1, 1, 1, true},
			{0, 30, 1000, false},
			{20000, 30, QueryAllMaxRecords, false},
		} {
			page, err := s.QueryAll("OMM.fbs", tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Records) != tc.want || page.Truncated != tc.truncated || page.Limit != tc.wantLimit {
				t.Fatalf("QueryAll(OMM, %d): %d records, truncated=%v, limit=%d; want %d, %v, %d",
					tc.limit, len(page.Records), page.Truncated, page.Limit, tc.want, tc.truncated, tc.wantLimit)
			}
		}
		// Newest first: a truncated page drops the oldest record.
		page, err := s.QueryAll("OMM.fbs", 29)
		if err != nil {
			t.Fatal(err)
		}
		oldest := ComputeCID(recs[0])
		for _, r := range page.Records {
			if ComputeCID(r) == oldest {
				t.Fatal("the truncated page holds the oldest record, not the newest 29")
			}
		}
		if page, err := s.QueryAll("XYZ.fbs", 10); err == nil && (len(page.Records) != 0 || page.Truncated) {
			t.Fatalf("unknown type: %+v", page)
		}
	})
}
