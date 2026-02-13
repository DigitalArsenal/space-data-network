// Package subscription provides PubSub-to-Subscription bridge for SDN.
package subscription

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// TopicRouter routes messages from PubSub topics to subscriptions
// based on the routing header. It acts as the bridge between the
// libp2p PubSub layer and the subscription manager.
type TopicRouter struct {
	manager   *Manager
	router    *Router
	streaming *StreamingManager
	localPeer string

	// Topic-to-handler mapping for edge relay filtering
	topicFilters map[string][]TopicFilterFunc
	mu           sync.RWMutex
}

// TopicFilterFunc decides whether a message on a topic should be forwarded.
// Returns true to forward, false to drop.
type TopicFilterFunc func(topic string, header *RoutingHeader, payload []byte) bool

// NewTopicRouter creates a new topic router connecting PubSub to subscriptions
func NewTopicRouter(manager *Manager, localPeerID string, streamingConfig StreamingConfig) *TopicRouter {
	router := NewRouter(manager, localPeerID)
	streaming := NewStreamingManager(streamingConfig)

	tr := &TopicRouter{
		manager:      manager,
		router:       router,
		streaming:    streaming,
		localPeer:    localPeerID,
		topicFilters: make(map[string][]TopicFilterFunc),
	}

	// Wire streaming into the subscription manager as a global handler
	manager.AddGlobalHandler(func(sub *Subscription, schema string, data []byte, from string, header *RoutingHeader) {
		if sub.Config.Streaming {
			streaming.DeliverMessage(schema, data, from, header)
		}
	})

	return tr
}

// Manager returns the underlying subscription manager
func (tr *TopicRouter) Manager() *Manager {
	return tr.manager
}

// Router returns the underlying message router
func (tr *TopicRouter) Router() *Router {
	return tr.router
}

// Streaming returns the streaming manager
func (tr *TopicRouter) Streaming() *StreamingManager {
	return tr.streaming
}

// HandleTopicMessage handles a message received on a PubSub topic.
// This is the main entry point called when a message arrives from PubSub.
func (tr *TopicRouter) HandleTopicMessage(topic string, data []byte, from string) error {
	// Determine if this is a routed message (has header) or raw
	if len(data) < 4 {
		// Too small for a header-prefixed message, treat as raw
		schema := tr.schemaFromTopic(topic)
		if schema != "" {
			tr.manager.ProcessMessage(schema, data, from, nil)
		}
		return nil
	}

	// Try to parse as header-prefixed message
	header, payload, err := ParseMessageWithHeader(data)
	if err != nil {
		// Not a header-prefixed message, treat as raw on this topic
		schema := tr.schemaFromTopic(topic)
		if schema != "" {
			tr.manager.ProcessMessage(schema, data, from, nil)
		}
		return nil
	}

	// Verify source peer matches routing header to prevent spoofing.
	// An empty SourcePeer in the header could bypass this check, so reject it.
	if header.SourcePeer == "" {
		log.Warnf("Rejecting message with empty SourcePeer in routing header from %q on topic %s", from, topic)
		return fmt.Errorf("routing header has empty SourcePeer")
	}
	if header.SourcePeer != from {
		log.Warnf("Source peer mismatch: header says %q but message from %q on topic %s — rejecting", header.SourcePeer, from, topic)
		return fmt.Errorf("source peer mismatch: header=%q from=%q", header.SourcePeer, from)
	}

	// Apply topic filters (for edge relay filtering)
	tr.mu.RLock()
	filters := tr.topicFilters[topic]
	globalFilters := tr.topicFilters["*"]
	tr.mu.RUnlock()

	for _, filter := range filters {
		if !filter(topic, header, payload) {
			log.Debugf("Message filtered on topic %s", topic)
			return nil
		}
	}
	for _, filter := range globalFilters {
		if !filter(topic, header, payload) {
			log.Debugf("Message filtered by global filter on topic %s", topic)
			return nil
		}
	}

	// Route through the router (handles TTL, destination check, forwarding)
	headerData, err := SerializeRoutingHeader(header)
	if err != nil {
		return fmt.Errorf("failed to re-serialize header: %w", err)
	}

	return tr.router.RouteMessage(headerData, payload, from)
}

// AddTopicFilter adds a filter for a specific topic. Use "*" for all topics.
func (tr *TopicRouter) AddTopicFilter(topic string, filter TopicFilterFunc) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.topicFilters[topic] = append(tr.topicFilters[topic], filter)
}

// ClearTopicFilters removes all filters for a topic
func (tr *TopicRouter) ClearTopicFilters(topic string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.topicFilters, topic)
}

// schemaFromTopic extracts the schema name from a topic string
func (tr *TopicRouter) schemaFromTopic(topic string) string {
	// /sdn/data/{schema_type}
	if strings.HasPrefix(topic, "/sdn/data/") {
		return strings.TrimPrefix(topic, "/sdn/data/")
	}
	// /spacedatanetwork/sds/{schema}.fbs
	if strings.HasPrefix(topic, "/spacedatanetwork/sds/") {
		return strings.TrimPrefix(topic, "/spacedatanetwork/sds/")
	}
	return ""
}

// GetRequiredTopics returns all topics needed for the current subscriptions,
// including schema-based and peer-based topics
func (tr *TopicRouter) GetRequiredTopics() []string {
	return tr.manager.GetRequiredTopics()
}

// --- Edge Relay Filtering ---

// EdgeRelayFilter provides topic-based filtering for edge relay nodes.
// Edge relays forward messages without decrypting payloads, using only
// the unencrypted routing header for routing decisions.
type EdgeRelayFilter struct {
	// AllowedSchemas limits which schema types are forwarded. Empty = all.
	AllowedSchemas map[string]bool `json:"allowedSchemas,omitempty"`
	// AllowedPeers limits which destination peers are served. Empty = all.
	AllowedPeers map[string]bool `json:"allowedPeers,omitempty"`
	// MinPriority is the minimum priority to forward (0 = forward all)
	MinPriority Priority `json:"minPriority"`
	// MaxTTL is the maximum TTL accepted (0 = no limit)
	MaxTTL uint8 `json:"maxTTL"`
	// AllowEncrypted controls whether encrypted messages are forwarded
	AllowEncrypted bool `json:"allowEncrypted"`
	// AllowUnencrypted controls whether unencrypted messages are forwarded
	AllowUnencrypted bool `json:"allowUnencrypted"`
}

// DefaultEdgeRelayFilter returns a permissive default filter
func DefaultEdgeRelayFilter() *EdgeRelayFilter {
	return &EdgeRelayFilter{
		AllowedSchemas:   nil, // all schemas
		AllowedPeers:     nil, // all peers
		MinPriority:      PriorityLow,
		MaxTTL:           0, // no limit
		AllowEncrypted:   true,
		AllowUnencrypted: true,
	}
}

// ToTopicFilter converts the edge relay filter to a TopicFilterFunc
func (erf *EdgeRelayFilter) ToTopicFilter() TopicFilterFunc {
	return func(topic string, header *RoutingHeader, payload []byte) bool {
		if header == nil {
			return true // pass through raw messages
		}

		// Schema filter
		if len(erf.AllowedSchemas) > 0 {
			if !erf.AllowedSchemas[header.SchemaType] {
				return false
			}
		}

		// Peer filter
		if len(erf.AllowedPeers) > 0 && len(header.DestinationPeers) > 0 {
			allowed := false
			for _, dest := range header.DestinationPeers {
				if erf.AllowedPeers[dest] {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}

		// Priority filter
		if header.Priority < erf.MinPriority {
			return false
		}

		// TTL filter
		if erf.MaxTTL > 0 && header.TTL > erf.MaxTTL {
			return false
		}

		// Encryption filter
		if header.Encrypted && !erf.AllowEncrypted {
			return false
		}
		if !header.Encrypted && !erf.AllowUnencrypted {
			return false
		}

		return true
	}
}

// --- PubSub Integration Helper ---

// PubSubBridge provides a high-level integration between PubSub and subscriptions.
// It manages topic joins/leaves based on active subscriptions.
type PubSubBridge struct {
	topicRouter    *TopicRouter
	joinTopic      func(topic string) error
	leaveTopic     func(topic string) error
	subscribedTopics map[string]bool
	mu             sync.Mutex
}

// NewPubSubBridge creates a new PubSub bridge
func NewPubSubBridge(topicRouter *TopicRouter,
	joinTopic func(topic string) error,
	leaveTopic func(topic string) error,
) *PubSubBridge {
	return &PubSubBridge{
		topicRouter:      topicRouter,
		joinTopic:        joinTopic,
		leaveTopic:       leaveTopic,
		subscribedTopics: make(map[string]bool),
	}
}

// SyncTopics joins/leaves PubSub topics to match current subscriptions
func (pb *PubSubBridge) SyncTopics(ctx context.Context) error {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	required := pb.topicRouter.GetRequiredTopics()
	requiredSet := make(map[string]bool, len(required))
	for _, topic := range required {
		requiredSet[topic] = true
	}

	// Join new topics
	for topic := range requiredSet {
		if !pb.subscribedTopics[topic] {
			if err := pb.joinTopic(topic); err != nil {
				log.Warnf("Failed to join topic %s: %v", topic, err)
				continue
			}
			pb.subscribedTopics[topic] = true
			log.Infof("Joined topic: %s", topic)
		}
	}

	// Leave old topics
	for topic := range pb.subscribedTopics {
		if !requiredSet[topic] {
			if err := pb.leaveTopic(topic); err != nil {
				log.Warnf("Failed to leave topic %s: %v", topic, err)
				continue
			}
			delete(pb.subscribedTopics, topic)
			log.Infof("Left topic: %s", topic)
		}
	}

	return nil
}

// SubscribedTopics returns currently subscribed topics
func (pb *PubSubBridge) SubscribedTopics() []string {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	topics := make([]string, 0, len(pb.subscribedTopics))
	for topic := range pb.subscribedTopics {
		topics = append(topics, topic)
	}
	return topics
}
