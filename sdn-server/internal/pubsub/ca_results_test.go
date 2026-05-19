package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type recordingTopicPublisher struct {
	topics   []string
	payloads map[string][]byte
}

func (p *recordingTopicPublisher) PublishToTopic(ctx context.Context, topic string, data []byte) error {
	if p.payloads == nil {
		p.payloads = make(map[string][]byte)
	}
	p.topics = append(p.topics, topic)
	p.payloads[topic] = append([]byte(nil), data...)
	return nil
}

func TestPublishCAResultSummaryPublishesTypedPayloadToPrivateNodeTopic(t *testing.T) {
	publisher := &recordingTopicPublisher{}

	err := PublishCAResultSummary(context.Background(), publisher, CAResultPublication{
		PrivateNodePeerID: "12D3KooWPrivateNode",
		Summary: CAResultSummary{
			RunID:              "ca-run-20260505-001",
			ModuleID:           "conjunction-assessment",
			ModuleVersion:      "1.2.3",
			ModuleArtifactHash: "sha256:module",
			ResultCount:        2,
			CDMCIDs:            []string{"bafycdm1", "bafycdm2"},
			SourcePNMCIDs:      []string{"bafypnm"},
			QueryHashes:        []string{"sha256:query"},
			CAConfigHash:       "sha256:config",
			ResultHash:         "sha256:result",
			Signature:          "ed25519:signature",
			SigningKeyID:       "did:sdn:provider#ca",
			SignedAt:           "2026-05-05T12:00:00Z",
			Metadata: map[string]string{
				"provider_id": "celestrak.eth",
			},
		},
	})
	if err != nil {
		t.Fatalf("PublishCAResultSummary failed: %v", err)
	}

	topic := CAResultTopic("12D3KooWPrivateNode")
	if !reflect.DeepEqual(publisher.topics, []string{topic}) {
		t.Fatalf("published topics = %#v, want %#v", publisher.topics, []string{topic})
	}

	var payload map[string]any
	if err := json.Unmarshal(publisher.payloads[topic], &payload); err != nil {
		t.Fatalf("payload was not JSON: %v", err)
	}
	if payload["message_type"] != "sdn.ca.result_summary.v1" {
		t.Fatalf("message_type = %#v", payload["message_type"])
	}
	if payload["private_node_peer_id"] != "12D3KooWPrivateNode" {
		t.Fatalf("private_node_peer_id = %#v", payload["private_node_peer_id"])
	}
	if payload["run_id"] != "ca-run-20260505-001" {
		t.Fatalf("run_id = %#v", payload["run_id"])
	}
	if payload["signature"] != "ed25519:signature" {
		t.Fatalf("signature = %#v", payload["signature"])
	}
	if payload["cdm_cids"] == nil || payload["source_pnm_cids"] == nil || payload["query_hashes"] == nil {
		t.Fatalf("payload missing CDM/provenance pointers: %#v", payload)
	}
}

func TestPublishCAResultSummaryRejectsInvalidPublicationBeforePublish(t *testing.T) {
	publisher := &recordingTopicPublisher{}

	err := PublishCAResultSummary(context.Background(), publisher, CAResultPublication{
		PrivateNodePeerID: "bad/node",
		Summary: CAResultSummary{
			RunID:        "ca-run",
			ResultHash:   "sha256:result",
			ResultCount:  1,
			SigningKeyID: "did:sdn:provider#ca",
			Signature:    "ed25519:signature",
		},
	})
	if err == nil {
		t.Fatalf("expected invalid private-node peer ID error")
	}
	if len(publisher.topics) != 0 {
		t.Fatalf("published invalid CA result to %#v", publisher.topics)
	}
}

func TestPublishCAResultSummaryReturnsPublisherErrors(t *testing.T) {
	wantErr := errors.New("publish failed")
	publisher := topicPublisherFunc(func(ctx context.Context, topic string, data []byte) error {
		return wantErr
	})

	err := PublishCAResultSummary(context.Background(), publisher, CAResultPublication{
		PrivateNodePeerID: "12D3KooWPrivateNode",
		Summary: CAResultSummary{
			RunID:        "ca-run",
			ResultHash:   "sha256:result",
			ResultCount:  1,
			SigningKeyID: "did:sdn:provider#ca",
			Signature:    "ed25519:signature",
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

type topicPublisherFunc func(context.Context, string, []byte) error

func (f topicPublisherFunc) PublishToTopic(ctx context.Context, topic string, data []byte) error {
	return f(ctx, topic, data)
}
