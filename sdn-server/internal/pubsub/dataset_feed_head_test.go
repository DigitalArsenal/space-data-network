package pubsub

// SEC-031: feed-head authorization verifies against a key bound to the
// author, never the key the message carries, and refuses forged ranking.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestVerifyDatasetFeedHeadWithKeyNeverUsesTheEmbeddedKey(t *testing.T) {
	authorPub, authorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ann := DatasetFeedHeadAnnouncement{Schema: "OMM.fbs", QueryProfile: "p", FeedSequence: 1, FeedHead: "h", PublishedAt: time.Now()}

	forged := ann
	if err := SignDatasetFeedHead(&forged, attackerPriv); err != nil {
		t.Fatal(err)
	}
	// The integrity-only check passes on the embedded key...
	if err := VerifyDatasetFeedHead(forged, nil); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	// ...but the authorization check refuses it against the author's key,
	// and refuses to run without one.
	if err := VerifyDatasetFeedHeadWithKey(forged, authorPub); err == nil {
		t.Fatal("a head signed by the embedded attacker key verified against the author's key")
	}
	if err := VerifyDatasetFeedHeadWithKey(forged, nil); err == nil {
		t.Fatal("authorization without a key bound to the author succeeded")
	}

	genuine := ann
	if err := SignDatasetFeedHead(&genuine, authorPriv); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDatasetFeedHeadWithKey(genuine, authorPub); err != nil {
		t.Fatalf("genuine head: %v", err)
	}
}

func TestCheckDatasetFeedHeadBounds(t *testing.T) {
	now := time.Now()
	ok := DatasetFeedHeadAnnouncement{FeedSequence: 7, PublishedAt: now.Add(-time.Hour)}
	if err := CheckDatasetFeedHeadBounds(ok, now); err != nil {
		t.Fatalf("plausible head: %v", err)
	}
	for name, ann := range map[string]DatasetFeedHeadAnnouncement{
		"MaxInt64 sequence":  {FeedSequence: math.MaxInt64, PublishedAt: now},
		"negative sequence":  {FeedSequence: -1, PublishedAt: now},
		"zero sequence":      {FeedSequence: 0, PublishedAt: now},
		"far future":         {FeedSequence: 1, PublishedAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)},
		"six minutes ahead":  {FeedSequence: 1, PublishedAt: now.Add(6 * time.Minute)},
		"no published_at":    {FeedSequence: 1},
		"one past the bound": {FeedSequence: DatasetFeedHeadMaxSequence + 1, PublishedAt: now},
	} {
		if err := CheckDatasetFeedHeadBounds(ann, now); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// TestParseDatasetFeedHeadRefusesIncompleteOrImplausibleHeads: the verifier's
// S2 (an empty lane field skipped the lane checks and was filled from the
// unsigned index) and the audit's forged ranking, refused at the parser that
// every gossiped head goes through.
func TestParseDatasetFeedHeadRefusesIncompleteOrImplausibleHeads(t *testing.T) {
	now := time.Now().UTC()
	base := DatasetFeedHeadAnnouncement{
		MessageType: DatasetFeedHeadMessageType, Schema: "OMM.fbs",
		ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-1",
		QueryProfile: "dataset-publication-v1", Limit: 10, FeedSequence: 1, FeedHead: "h",
		ShardCID: "bafyshard", IndexCID: "bafyindex", PublishedAt: now.Add(-time.Minute),
	}
	encode := func(ann DatasetFeedHeadAnnouncement) []byte {
		data, err := json.Marshal(ann)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, err := ParseDatasetFeedHeadAnnouncement(encode(base)); err != nil {
		t.Fatalf("a complete head: %v", err)
	}
	for name, mutate := range map[string]func(*DatasetFeedHeadAnnouncement){
		"empty provider_id":      func(a *DatasetFeedHeadAnnouncement) { a.ProviderID = "" },
		"blank source_name":      func(a *DatasetFeedHeadAnnouncement) { a.SourceName = "  " },
		"empty batch_id":         func(a *DatasetFeedHeadAnnouncement) { a.BatchID = "" },
		"all lane fields empty":  func(a *DatasetFeedHeadAnnouncement) { a.ProviderID, a.SourceName, a.BatchID = "", "", "" },
		"no shard cid":           func(a *DatasetFeedHeadAnnouncement) { a.ShardCID = "" },
		"no index cid":           func(a *DatasetFeedHeadAnnouncement) { a.IndexCID = "" },
		"no window limit":        func(a *DatasetFeedHeadAnnouncement) { a.Limit = 0 },
		"MaxInt64 sequence":      func(a *DatasetFeedHeadAnnouncement) { a.FeedSequence = math.MaxInt64 },
		"sequence past 2^24":     func(a *DatasetFeedHeadAnnouncement) { a.FeedSequence = DatasetFeedHeadMaxSequence + 1 },
		"year 2099 published_at": func(a *DatasetFeedHeadAnnouncement) { a.PublishedAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) },
		"missing published_at":   func(a *DatasetFeedHeadAnnouncement) { a.PublishedAt = time.Time{} },
	} {
		ann := base
		mutate(&ann)
		if _, err := ParseDatasetFeedHeadAnnouncement(encode(ann)); err == nil {
			t.Fatalf("%s: parsed", name)
		}
	}
}
