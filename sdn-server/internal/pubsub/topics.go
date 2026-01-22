// Package pubsub provides PubSub topic management for SDS.
package pubsub

import (
	"fmt"

	logging "github.com/ipfs/go-log/v2"
	ps "github.com/libp2p/go-libp2p-pubsub"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var log = logging.Logger("sds-pubsub")

// TopicPrefix is the prefix for all SDS PubSub topics.
const TopicPrefix = "/spacedatanetwork/sds/"

// EdgeRelayTopic is the topic for edge relay announcements.
const EdgeRelayTopic = "/spacedatanetwork/edge-relays"

// TopicManager manages PubSub topics for SDS schemas.
type TopicManager struct {
	pubsub    *ps.PubSub
	validator *sds.Validator
	topics    map[string]*ps.Topic
	subs      map[string]*ps.Subscription
}

// NewTopicManager creates a new topic manager.
func NewTopicManager(pubsub *ps.PubSub, validator *sds.Validator) (*TopicManager, error) {
	tm := &TopicManager{
		pubsub:    pubsub,
		validator: validator,
		topics:    make(map[string]*ps.Topic),
		subs:      make(map[string]*ps.Subscription),
	}

	return tm, nil
}

// SetupSDSTopics creates and joins topics for all SDS schemas.
func (tm *TopicManager) SetupSDSTopics() error {
	for _, schemaName := range tm.validator.Schemas() {
		topicName := TopicPrefix + schemaName

		topic, err := tm.pubsub.Join(topicName)
		if err != nil {
			log.Warnf("Failed to join topic %s: %v", topicName, err)
			continue
		}

		tm.topics[schemaName] = topic
		log.Debugf("Joined topic: %s", topicName)
	}

	return nil
}

// Subscribe subscribes to a schema topic.
func (tm *TopicManager) Subscribe(schemaName string) (*ps.Subscription, error) {
	topic, ok := tm.topics[schemaName]
	if !ok {
		return nil, fmt.Errorf("unknown schema: %s", schemaName)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	tm.subs[schemaName] = sub
	return sub, nil
}

// Publish publishes data to a schema topic.
func (tm *TopicManager) Publish(schemaName string, data []byte) error {
	topic, ok := tm.topics[schemaName]
	if !ok {
		return fmt.Errorf("unknown schema: %s", schemaName)
	}

	return topic.Publish(tm.pubsub.GetTopics()[0], data)
}

// GetTopic returns the topic for a schema.
func (tm *TopicManager) GetTopic(schemaName string) (*ps.Topic, bool) {
	topic, ok := tm.topics[schemaName]
	return topic, ok
}

// Close closes all subscriptions and leaves all topics.
func (tm *TopicManager) Close() error {
	for _, sub := range tm.subs {
		sub.Cancel()
	}

	for _, topic := range tm.topics {
		topic.Close()
	}

	return nil
}

// TopicName returns the full topic name for a schema.
func TopicName(schemaName string) string {
	return TopicPrefix + schemaName
}

// SchemaFromTopic extracts the schema name from a topic name.
func SchemaFromTopic(topicName string) string {
	if len(topicName) <= len(TopicPrefix) {
		return ""
	}
	return topicName[len(TopicPrefix):]
}
