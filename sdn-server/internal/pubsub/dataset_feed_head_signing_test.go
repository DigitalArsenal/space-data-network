package pubsub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

type capturingTopicPublisher struct {
	lastTopic   string
	lastPayload []byte
}

func (p *capturingTopicPublisher) PublishToTopic(_ context.Context, topic string, data []byte) error {
	p.lastTopic = topic
	p.lastPayload = append([]byte(nil), data...)
	return nil
}

func signedTestAnnouncement(t *testing.T, key ed25519.PrivateKey) DatasetFeedHeadAnnouncement {
	t.Helper()
	ann := DatasetFeedHeadAnnouncement{
		MessageType:  DatasetFeedHeadMessageType,
		Schema:       OMMSchema,
		ProviderID:   "provider-1",
		QueryProfile: "default",
		FeedSequence: 7,
		FeedHead:     "bafyfeedhead",
		ShardCID:     "bafyshard",
	}
	if err := SignDatasetFeedHead(&ann, key); err != nil {
		t.Fatalf("SignDatasetFeedHead: %v", err)
	}
	return ann
}

func TestSignAndVerifyDatasetFeedHead(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ann := signedTestAnnouncement(t, priv)
	if ann.Signature == "" || ann.SignatureType != "Ed25519" || ann.SigningPublicKey == "" {
		t.Fatalf("signature fields not populated: %+v", ann)
	}
	if err := VerifyDatasetFeedHead(ann, nil); err != nil {
		t.Fatalf("verify with embedded key: %v", err)
	}
	if err := VerifyDatasetFeedHead(ann, pub); err != nil {
		t.Fatalf("verify with provider key: %v", err)
	}
}

func TestVerifyDatasetFeedHeadRejectsTampering(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tampered := signedTestAnnouncement(t, priv)
	tampered.FeedHead = "bafyevil"
	if err := VerifyDatasetFeedHead(tampered, nil); err == nil {
		t.Fatal("verify accepted a tampered feed head")
	}

	// Swapping in a different signing key must fail: the public key is
	// covered by the signature.
	swapped := signedTestAnnouncement(t, priv)
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDatasetFeedHead(swapped, otherPub); err == nil {
		t.Fatal("verify accepted a signature against the wrong provider key")
	}

	unsigned := signedTestAnnouncement(t, priv)
	unsigned.Signature = ""
	if err := VerifyDatasetFeedHead(unsigned, nil); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("expected unsigned rejection, got %v", err)
	}
}

func TestParseDatasetFeedHeadPreservesSignatureFields(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ann := signedTestAnnouncement(t, priv)

	publisher := &capturingTopicPublisher{}
	if err := PublishDatasetFeedHead(t.Context(), publisher, ann); err != nil {
		t.Fatalf("publish: %v", err)
	}
	parsed, err := ParseDatasetFeedHeadAnnouncement(publisher.lastPayload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := VerifyDatasetFeedHead(parsed, nil); err != nil {
		t.Fatalf("round-tripped announcement failed verification: %v", err)
	}
}

func TestPublishSignedDatasetFeedHeadSigns(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ann := DatasetFeedHeadAnnouncement{
		Schema:       "OMM",
		QueryProfile: "default",
		FeedSequence: 1,
		FeedHead:     "bafyhead",
	}
	publisher := &capturingTopicPublisher{}
	if err := PublishSignedDatasetFeedHead(t.Context(), publisher, ann, priv); err != nil {
		t.Fatalf("publish signed: %v", err)
	}
	parsed, err := ParseDatasetFeedHeadAnnouncement(publisher.lastPayload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := VerifyDatasetFeedHead(parsed, nil); err != nil {
		t.Fatalf("published announcement not verifiable: %v", err)
	}
}
