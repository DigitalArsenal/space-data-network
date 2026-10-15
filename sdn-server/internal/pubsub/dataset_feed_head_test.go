package pubsub

// SEC-031: feed-head authorization verifies against a key bound to the
// author, never the key the message carries, and refuses forged ranking.

import (
	"crypto/ed25519"
	"crypto/rand"
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
