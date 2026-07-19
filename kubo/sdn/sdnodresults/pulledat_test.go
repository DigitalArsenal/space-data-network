package sdnodresults

// pulledat_test.go — a pure unit test of formatPulledAt (unexported, hence an
// internal test file), proving the unix-milliseconds -> RFC3339 conversion
// the module's pulled_at column feeds (space-data-network-modules commit
// 5a6d684: "[u64le unix_ms] fire timestamp"). reader.go's queryRange/
// providerAggregates always SELECT pulled_at (the flowrt schema mirror this
// binary compiles against declares it additively — see storeRow's doc — so
// there is exactly one column set, no version-skew fallback to test); this
// file's tests are exercised against every LinkedStore reader_test.go opens,
// confirming the column read itself never errors. What is NOT exercised
// locally is a REAL non-zero pulled_at value flowing through to a
// ProviderStat.LastPulled: writing one requires extending
// flowrt.BuildTestWrapperRow/IngestTestRow's signature, a flowrt file this
// round is scoped to avoid (the deploy agent owns flowrt concurrently) —
// every fixture row here ingests with the field unset (reads 0, formats to
// ""), which is exactly reader_test.go's real, honest, already-covered
// backward-compatible case (a pre-attribution-era pulled_at, not just a
// pre-attribution-era provider).

import "testing"

func TestFormatPulledAt(t *testing.T) {
	cases := []struct {
		unixMs int64
		want   string
	}{
		{0, ""},
		{-1, ""},
		{1784419200000, "2026-07-19T00:00:00Z"}, // 2026-07-19T00:00:00Z in unix ms
	}
	for _, c := range cases {
		if got := formatPulledAt(c.unixMs); got != c.want {
			t.Errorf("formatPulledAt(%d) = %q, want %q", c.unixMs, got, c.want)
		}
	}
}

func TestAsIntAndAsInt64(t *testing.T) {
	if got := asInt(int64(42)); got != 42 {
		t.Errorf("asInt(int64(42)) = %d, want 42", got)
	}
	if got := asInt(7); got != 7 {
		t.Errorf("asInt(7) = %d, want 7", got)
	}
	if got := asInt("not a number"); got != 0 {
		t.Errorf("asInt(string) = %d, want 0", got)
	}
	if got := asInt64(int64(1784419200000)); got != 1784419200000 {
		t.Errorf("asInt64(int64) = %d, want 1784419200000", got)
	}
	if got := asInt64(nil); got != 0 {
		t.Errorf("asInt64(nil) = %d, want 0", got)
	}
}
