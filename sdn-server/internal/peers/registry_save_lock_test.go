package peers

import (
	"testing"
	"time"
)

// sdn-registry-save-lock-starvation: Save() must not append records that
// carry no change — the write timestamp and provider signature vary on every
// serialization and must not defeat the comparison.
func TestPeerRecordsEquivalentIgnoresTimestampAndSignature(t *testing.T) {
	base := peerRegistryRecord{
		ID:         "16UiuTest",
		Addrs:      []string{"/ip4/10.0.0.1/tcp/4001"},
		TrustLevel: Trusted,
		Name:       "config:bcPpYr2U",
		EPMData:    []byte{1, 2, 3},
		VCardData:  "BEGIN:VCARD",
	}
	same := base
	same.UpdatedAtMs = time.Now().UnixMilli()
	same.ProviderSignature = []byte{9, 9}
	if !peerRecordsEquivalent(base, same) {
		t.Error("records differing only in UpdatedAtMs/ProviderSignature must be equivalent")
	}

	changed := base
	changed.EPMData = []byte{1, 2, 3, 4}
	if peerRecordsEquivalent(base, changed) {
		t.Error("EPMData change must not be equivalent")
	}

	trustChanged := base
	trustChanged.TrustLevel = Marginal
	if peerRecordsEquivalent(base, trustChanged) {
		t.Error("trust change must not be equivalent")
	}
}

func TestGroupRecordsEquivalent(t *testing.T) {
	base := peerGroupRecord{Name: "ops", Members: []string{"a", "b"}}
	same := base
	same.UpdatedAtMs = 42
	if !groupRecordsEquivalent(base, same) {
		t.Error("group records differing only in UpdatedAtMs must be equivalent")
	}
	changed := base
	changed.Members = []string{"a"}
	if groupRecordsEquivalent(base, changed) {
		t.Error("membership change must not be equivalent")
	}
}
