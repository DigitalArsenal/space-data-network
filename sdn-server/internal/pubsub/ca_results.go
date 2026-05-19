package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// CAResultTopicPrefix is the private-node topic namespace for CA result automation.
	CAResultTopicPrefix = "/sdn/private-node/"
	CAResultMessageType = "sdn.ca.result_summary.v1"
)

// TopicPublisher publishes bytes to an explicit SDN pub/sub topic.
type TopicPublisher interface {
	PublishToTopic(ctx context.Context, topic string, data []byte) error
}

// CAResultSummary is the typed JSON payload delivered to private-node CA result
// channels. CDMCIDs may point at signed CDM artifacts when a run produces them.
type CAResultSummary struct {
	MessageType        string            `json:"message_type"`
	PrivateNodePeerID  string            `json:"private_node_peer_id"`
	RunID              string            `json:"run_id"`
	ModuleID           string            `json:"module_id,omitempty"`
	ModuleVersion      string            `json:"module_version,omitempty"`
	ModuleArtifactHash string            `json:"module_artifact_hash,omitempty"`
	ResultCount        int               `json:"result_count"`
	CDMCIDs            []string          `json:"cdm_cids,omitempty"`
	SourcePNMCIDs      []string          `json:"source_pnm_cids,omitempty"`
	QueryHashes        []string          `json:"query_hashes,omitempty"`
	CAConfigHash       string            `json:"ca_config_hash,omitempty"`
	ResultHash         string            `json:"result_hash"`
	Signature          string            `json:"signature"`
	SigningKeyID       string            `json:"signing_key_id"`
	SignedAt           string            `json:"signed_at,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// CAResultPublication describes one private-node CA result publication.
type CAResultPublication struct {
	PrivateNodePeerID string
	Summary           CAResultSummary
}

// CAResultTopic returns the private-node topic that downstream automation can
// subscribe to for all CA result summaries owned by that private node.
func CAResultTopic(privateNodePeerID string) string {
	return CAResultTopicPrefix + privateNodePeerID + "/ca-results"
}

// PublishCAResultSummary publishes a signed, typed CA result summary to the
// target private-node result channel.
func PublishCAResultSummary(ctx context.Context, publisher TopicPublisher, publication CAResultPublication) error {
	if publisher == nil {
		return ErrNoPublisher
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateCAResultPublication(publication); err != nil {
		return err
	}

	summary := publication.Summary
	summary.MessageType = CAResultMessageType
	summary.PrivateNodePeerID = strings.TrimSpace(publication.PrivateNodePeerID)

	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal CA result summary: %w", err)
	}
	topic := CAResultTopic(summary.PrivateNodePeerID)
	if err := publisher.PublishToTopic(ctx, topic, payload); err != nil {
		return fmt.Errorf("%s: %w", topic, err)
	}
	return nil
}

func validateCAResultPublication(publication CAResultPublication) error {
	privateNodePeerID := strings.TrimSpace(publication.PrivateNodePeerID)
	if privateNodePeerID == "" {
		return errors.New("private node peer ID is required")
	}
	if strings.ContainsAny(privateNodePeerID, "/ \t\r\n") {
		return fmt.Errorf("invalid private node peer ID: %q", publication.PrivateNodePeerID)
	}
	summary := publication.Summary
	if strings.TrimSpace(summary.RunID) == "" {
		return errors.New("CA result run ID is required")
	}
	if summary.ResultCount < 0 {
		return errors.New("CA result count cannot be negative")
	}
	if strings.TrimSpace(summary.ResultHash) == "" {
		return errors.New("CA result hash is required")
	}
	if strings.TrimSpace(summary.Signature) == "" {
		return errors.New("CA result signature is required")
	}
	if strings.TrimSpace(summary.SigningKeyID) == "" {
		return errors.New("CA result signing key ID is required")
	}
	return nil
}
