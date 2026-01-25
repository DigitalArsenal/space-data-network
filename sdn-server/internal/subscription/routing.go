// Package subscription provides routing header handling for SDN messages.
package subscription

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Routing errors
var (
	ErrInvalidHeader    = errors.New("invalid routing header")
	ErrHeaderTooShort   = errors.New("routing header too short")
	ErrTTLExpired       = errors.New("message TTL expired")
	ErrInvalidPriority  = errors.New("invalid priority level")
)

// SerializeRoutingHeader serializes a routing header to binary format
// Format: [schemaTypeLen(1)][schemaType(n)][destCount(1)][destPeers...][ttl(1)][priority(1)][flags(1)][optional fields...]
func SerializeRoutingHeader(header *RoutingHeader) ([]byte, error) {
	if header.SchemaType == "" {
		return nil, fmt.Errorf("%w: schema type is required", ErrInvalidHeader)
	}

	schemaBytes := []byte(header.SchemaType)
	if len(schemaBytes) > 255 {
		return nil, fmt.Errorf("%w: schema type too long", ErrInvalidHeader)
	}

	// Calculate size
	size := 1 + len(schemaBytes) // schema type length + schema type
	size += 1                     // destination count

	destBytes := make([][]byte, len(header.DestinationPeers))
	for i, dest := range header.DestinationPeers {
		destBytes[i] = []byte(dest)
		if len(destBytes[i]) > 255 {
			return nil, fmt.Errorf("%w: destination peer ID too long", ErrInvalidHeader)
		}
		size += 1 + len(destBytes[i])
	}

	size += 3 // ttl + priority + flags

	// Optional fields based on flags
	var sessionKeyBytes []byte
	var sourcePeerBytes []byte
	var topicOverrideBytes []byte

	if header.SessionKeyID != "" {
		sessionKeyBytes = []byte(header.SessionKeyID)
		size += 1 + len(sessionKeyBytes)
	}

	if header.SourcePeer != "" {
		sourcePeerBytes = []byte(header.SourcePeer)
		size += 1 + len(sourcePeerBytes)
	}

	if header.TopicOverride != "" {
		topicOverrideBytes = []byte(header.TopicOverride)
		size += 1 + len(topicOverrideBytes)
	}

	// Add sequence and timestamp
	size += 16 // 8 bytes each for sequence and timestamp

	// Add signature length
	if len(header.HeaderSignature) > 0 {
		size += 2 + len(header.HeaderSignature) // 2 bytes for length
	}

	buffer := make([]byte, size)
	offset := 0

	// Schema type
	buffer[offset] = byte(len(schemaBytes))
	offset++
	copy(buffer[offset:], schemaBytes)
	offset += len(schemaBytes)

	// Destination peers
	buffer[offset] = byte(len(destBytes))
	offset++
	for _, dest := range destBytes {
		buffer[offset] = byte(len(dest))
		offset++
		copy(buffer[offset:], dest)
		offset += len(dest)
	}

	// TTL
	buffer[offset] = header.TTL
	offset++

	// Priority
	buffer[offset] = byte(header.Priority)
	offset++

	// Flags
	// bit 0: encrypted
	// bit 1: has session key
	// bit 2: has source peer
	// bit 3: has topic override
	// bit 4: has signature
	// bits 5-6: encryption mode
	var flags byte
	if header.Encrypted {
		flags |= 0x01
	}
	if header.SessionKeyID != "" {
		flags |= 0x02
	}
	if header.SourcePeer != "" {
		flags |= 0x04
	}
	if header.TopicOverride != "" {
		flags |= 0x08
	}
	if len(header.HeaderSignature) > 0 {
		flags |= 0x10
	}
	flags |= byte(header.EncryptionMode&0x03) << 5
	buffer[offset] = flags
	offset++

	// Optional session key ID
	if sessionKeyBytes != nil {
		buffer[offset] = byte(len(sessionKeyBytes))
		offset++
		copy(buffer[offset:], sessionKeyBytes)
		offset += len(sessionKeyBytes)
	}

	// Optional source peer
	if sourcePeerBytes != nil {
		buffer[offset] = byte(len(sourcePeerBytes))
		offset++
		copy(buffer[offset:], sourcePeerBytes)
		offset += len(sourcePeerBytes)
	}

	// Optional topic override
	if topicOverrideBytes != nil {
		buffer[offset] = byte(len(topicOverrideBytes))
		offset++
		copy(buffer[offset:], topicOverrideBytes)
		offset += len(topicOverrideBytes)
	}

	// Sequence (8 bytes, big endian)
	binary.BigEndian.PutUint64(buffer[offset:], header.Sequence)
	offset += 8

	// Timestamp (8 bytes, big endian)
	binary.BigEndian.PutUint64(buffer[offset:], header.Timestamp)
	offset += 8

	// Optional signature
	if len(header.HeaderSignature) > 0 {
		binary.BigEndian.PutUint16(buffer[offset:], uint16(len(header.HeaderSignature)))
		offset += 2
		copy(buffer[offset:], header.HeaderSignature)
		offset += len(header.HeaderSignature)
	}

	return buffer[:offset], nil
}

// DeserializeRoutingHeader deserializes a routing header from binary format
func DeserializeRoutingHeader(data []byte) (*RoutingHeader, error) {
	if len(data) < 5 {
		return nil, ErrHeaderTooShort
	}

	header := &RoutingHeader{}
	offset := 0

	// Schema type
	schemaLen := int(data[offset])
	offset++
	if offset+schemaLen > len(data) {
		return nil, ErrHeaderTooShort
	}
	header.SchemaType = string(data[offset : offset+schemaLen])
	offset += schemaLen

	// Destination peers
	if offset >= len(data) {
		return nil, ErrHeaderTooShort
	}
	destCount := int(data[offset])
	offset++

	header.DestinationPeers = make([]string, destCount)
	for i := 0; i < destCount; i++ {
		if offset >= len(data) {
			return nil, ErrHeaderTooShort
		}
		destLen := int(data[offset])
		offset++
		if offset+destLen > len(data) {
			return nil, ErrHeaderTooShort
		}
		header.DestinationPeers[i] = string(data[offset : offset+destLen])
		offset += destLen
	}

	// TTL, priority, flags
	if offset+3 > len(data) {
		return nil, ErrHeaderTooShort
	}
	header.TTL = data[offset]
	offset++
	header.Priority = Priority(data[offset])
	offset++
	flags := data[offset]
	offset++

	header.Encrypted = flags&0x01 != 0
	hasSessionKey := flags&0x02 != 0
	hasSourcePeer := flags&0x04 != 0
	hasTopicOverride := flags&0x08 != 0
	hasSignature := flags&0x10 != 0
	header.EncryptionMode = EncryptionMode((flags >> 5) & 0x03)

	// Optional session key ID
	if hasSessionKey {
		if offset >= len(data) {
			return nil, ErrHeaderTooShort
		}
		keyLen := int(data[offset])
		offset++
		if offset+keyLen > len(data) {
			return nil, ErrHeaderTooShort
		}
		header.SessionKeyID = string(data[offset : offset+keyLen])
		offset += keyLen
	}

	// Optional source peer
	if hasSourcePeer {
		if offset >= len(data) {
			return nil, ErrHeaderTooShort
		}
		peerLen := int(data[offset])
		offset++
		if offset+peerLen > len(data) {
			return nil, ErrHeaderTooShort
		}
		header.SourcePeer = string(data[offset : offset+peerLen])
		offset += peerLen
	}

	// Optional topic override
	if hasTopicOverride {
		if offset >= len(data) {
			return nil, ErrHeaderTooShort
		}
		topicLen := int(data[offset])
		offset++
		if offset+topicLen > len(data) {
			return nil, ErrHeaderTooShort
		}
		header.TopicOverride = string(data[offset : offset+topicLen])
		offset += topicLen
	}

	// Sequence and timestamp
	if offset+16 <= len(data) {
		header.Sequence = binary.BigEndian.Uint64(data[offset:])
		offset += 8
		header.Timestamp = binary.BigEndian.Uint64(data[offset:])
		offset += 8
	}

	// Optional signature
	if hasSignature && offset+2 <= len(data) {
		sigLen := int(binary.BigEndian.Uint16(data[offset:]))
		offset += 2
		if offset+sigLen <= len(data) {
			header.HeaderSignature = make([]byte, sigLen)
			copy(header.HeaderSignature, data[offset:offset+sigLen])
		}
	}

	return header, nil
}

// Router handles message routing based on headers
type Router struct {
	manager      *Manager
	localPeerID  string
	relayMode    bool // If true, forward messages without processing
	onForward    func(header *RoutingHeader, payload []byte) error
}

// NewRouter creates a new message router
func NewRouter(manager *Manager, localPeerID string) *Router {
	return &Router{
		manager:     manager,
		localPeerID: localPeerID,
	}
}

// SetRelayMode enables or disables relay mode
func (r *Router) SetRelayMode(enabled bool) {
	r.relayMode = enabled
}

// SetForwardHandler sets the handler for forwarding messages
func (r *Router) SetForwardHandler(handler func(header *RoutingHeader, payload []byte) error) {
	r.onForward = handler
}

// RouteMessage routes an incoming message based on its header
func (r *Router) RouteMessage(headerData []byte, payload []byte, from string) error {
	header, err := DeserializeRoutingHeader(headerData)
	if err != nil {
		return fmt.Errorf("failed to deserialize header: %w", err)
	}

	// Check TTL
	if header.TTL == 0 {
		return ErrTTLExpired
	}

	// Determine if this message is for us
	isForUs := r.isDestinedForUs(header)

	// Process locally if destined for us
	if isForUs && !r.relayMode {
		r.manager.ProcessMessage(header.SchemaType, payload, from, header)
	}

	// Forward if needed
	if r.shouldForward(header, isForUs) {
		return r.forwardMessage(header, payload)
	}

	return nil
}

// isDestinedForUs checks if a message is destined for this node
func (r *Router) isDestinedForUs(header *RoutingHeader) bool {
	// If no specific destinations, it's a broadcast
	if len(header.DestinationPeers) == 0 {
		return true
	}

	// Check if we're in the destination list
	for _, dest := range header.DestinationPeers {
		if dest == r.localPeerID {
			return true
		}
	}

	return false
}

// shouldForward determines if a message should be forwarded
func (r *Router) shouldForward(header *RoutingHeader, isForUs bool) bool {
	// Don't forward if TTL would reach 0
	if header.TTL <= 1 {
		return false
	}

	// In relay mode, always forward (even if for us)
	if r.relayMode {
		return true
	}

	// Forward broadcasts
	if len(header.DestinationPeers) == 0 {
		return true
	}

	// Forward if not (only) for us
	return !isForUs || len(header.DestinationPeers) > 1
}

// forwardMessage forwards a message after decrementing TTL
func (r *Router) forwardMessage(header *RoutingHeader, payload []byte) error {
	if r.onForward == nil {
		return nil
	}

	// Decrement TTL
	forwardHeader := *header
	forwardHeader.TTL--

	return r.onForward(&forwardHeader, payload)
}

// GetRoutingTopic determines the topic for a message based on its header
func GetRoutingTopic(header *RoutingHeader) string {
	// Use topic override if specified
	if header.TopicOverride != "" {
		return header.TopicOverride
	}

	// If specific destinations, use peer routing
	if len(header.DestinationPeers) == 1 {
		return GetPeerRoutingTopic(header.DestinationPeers[0])
	}

	// Default to schema-based routing
	return GetSchemaRoutingTopic(header.SchemaType)
}

// CreateMessageWithHeader creates a message with routing header prepended
// Format: [headerLen(2)][header(n)][payload(m)]
func CreateMessageWithHeader(header *RoutingHeader, payload []byte) ([]byte, error) {
	headerBytes, err := SerializeRoutingHeader(header)
	if err != nil {
		return nil, err
	}

	if len(headerBytes) > 65535 {
		return nil, fmt.Errorf("header too large: %d bytes", len(headerBytes))
	}

	message := make([]byte, 2+len(headerBytes)+len(payload))
	binary.BigEndian.PutUint16(message[0:], uint16(len(headerBytes)))
	copy(message[2:], headerBytes)
	copy(message[2+len(headerBytes):], payload)

	return message, nil
}

// ParseMessageWithHeader parses a message to extract header and payload
func ParseMessageWithHeader(message []byte) (*RoutingHeader, []byte, error) {
	if len(message) < 2 {
		return nil, nil, ErrHeaderTooShort
	}

	headerLen := int(binary.BigEndian.Uint16(message[0:]))
	if len(message) < 2+headerLen {
		return nil, nil, ErrHeaderTooShort
	}

	header, err := DeserializeRoutingHeader(message[2 : 2+headerLen])
	if err != nil {
		return nil, nil, err
	}

	payload := message[2+headerLen:]
	return header, payload, nil
}

// TopicMatcher helps match topics for subscriptions
type TopicMatcher struct {
	schemaTopics map[string]bool
	peerTopics   map[string]bool
}

// NewTopicMatcher creates a new topic matcher
func NewTopicMatcher() *TopicMatcher {
	return &TopicMatcher{
		schemaTopics: make(map[string]bool),
		peerTopics:   make(map[string]bool),
	}
}

// AddSchemaType adds a schema type to match
func (tm *TopicMatcher) AddSchemaType(schemaType string) {
	tm.schemaTopics[GetSchemaRoutingTopic(schemaType)] = true
	tm.schemaTopics[GetSDNTopic(schemaType)] = true
}

// AddPeer adds a peer to match
func (tm *TopicMatcher) AddPeer(peerID string) {
	tm.peerTopics[GetPeerRoutingTopic(peerID)] = true
}

// Matches checks if a topic matches the configured criteria
func (tm *TopicMatcher) Matches(topic string) bool {
	if tm.schemaTopics[topic] {
		return true
	}
	if tm.peerTopics[topic] {
		return true
	}
	return false
}

// Topics returns all topics that should be subscribed to
func (tm *TopicMatcher) Topics() []string {
	topics := make([]string, 0, len(tm.schemaTopics)+len(tm.peerTopics))
	for topic := range tm.schemaTopics {
		topics = append(topics, topic)
	}
	for topic := range tm.peerTopics {
		topics = append(topics, topic)
	}
	return topics
}
