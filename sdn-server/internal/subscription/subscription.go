// Package subscription provides subscription management and message routing for SDN.
package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("sdn-subscription")

// Common errors
var (
	ErrInvalidConfig     = errors.New("invalid subscription configuration")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
)

// FilterOperator represents a filter comparison operator
type FilterOperator string

const (
	OpEqual      FilterOperator = "eq"
	OpNotEqual   FilterOperator = "ne"
	OpGreater    FilterOperator = "gt"
	OpGreaterEq  FilterOperator = "gte"
	OpLess       FilterOperator = "lt"
	OpLessEq     FilterOperator = "lte"
	OpContains   FilterOperator = "contains"
	OpStartsWith FilterOperator = "startsWith"
	OpEndsWith   FilterOperator = "endsWith"
	OpIn         FilterOperator = "in"
	OpNotIn      FilterOperator = "notIn"
)

// QueryFilter represents a field-level filter
type QueryFilter struct {
	Field    string         `json:"field"`
	Operator FilterOperator `json:"operator"`
	Value    interface{}    `json:"value"`
}

// SubscriptionConfig represents subscription configuration
type SubscriptionConfig struct {
	// DataTypes to subscribe to (e.g., ["OMM.fbs", "CDM.fbs"])
	DataTypes []string `json:"dataTypes"`
	// SourcePeers to receive data from, or ["all"] for all peers
	SourcePeers []string `json:"sourcePeers"`
	// Encrypted indicates whether to receive encrypted or plaintext
	Encrypted bool `json:"encrypted"`
	// Streaming indicates whether to use real-time streaming or batch
	Streaming bool `json:"streaming"`
	// Filters for field-level filtering
	Filters []QueryFilter `json:"filters,omitempty"`
	// RateLimit in messages per minute (0 = unlimited)
	RateLimit int `json:"rateLimit,omitempty"`
	// TTL for received messages in milliseconds
	TTL int64 `json:"ttl,omitempty"`
}

// SubscriptionStatus represents subscription state
type SubscriptionStatus string

const (
	StatusActive  SubscriptionStatus = "active"
	StatusPaused  SubscriptionStatus = "paused"
	StatusError   SubscriptionStatus = "error"
)

// Subscription represents an active subscription
type Subscription struct {
	ID            string             `json:"id"`
	Config        SubscriptionConfig `json:"config"`
	CreatedAt     time.Time          `json:"createdAt"`
	MessageCount  int64              `json:"messageCount"`
	LastMessageAt *time.Time         `json:"lastMessageAt,omitempty"`
	Status        SubscriptionStatus `json:"status"`
	ErrorMessage  string             `json:"errorMessage,omitempty"`

	// Internal rate limiting
	rateLimitMu   sync.Mutex
	rateLimitCount int
	rateLimitReset time.Time
}

// Priority levels
type Priority uint8

const (
	PriorityLow      Priority = 0
	PriorityNormal   Priority = 64
	PriorityHigh     Priority = 128
	PriorityCritical Priority = 255
)

// EncryptionMode represents payload encryption mode
type EncryptionMode uint8

const (
	EncryptionNone       EncryptionMode = 0
	EncryptionECIES      EncryptionMode = 1
	EncryptionSessionKey EncryptionMode = 2
	EncryptionHybrid     EncryptionMode = 3
)

// RoutingHeader represents the routing header for messages
type RoutingHeader struct {
	SchemaType       string         `json:"schemaType"`
	DestinationPeers []string       `json:"destinationPeers,omitempty"`
	TTL              uint8          `json:"ttl"`
	Priority         Priority       `json:"priority"`
	Encrypted        bool           `json:"encrypted"`
	EncryptionMode   EncryptionMode `json:"encryptionMode"`
	SessionKeyID     string         `json:"sessionKeyId,omitempty"`
	SourcePeer       string         `json:"sourcePeer,omitempty"`
	Sequence         uint64         `json:"sequence,omitempty"`
	Timestamp        uint64         `json:"timestamp"`
	TopicOverride    string         `json:"topicOverride,omitempty"`
	HeaderSignature  []byte         `json:"headerSignature,omitempty"`
}

// NewRoutingHeader creates a new routing header with defaults
func NewRoutingHeader(schemaType string, sourcePeer string) *RoutingHeader {
	return &RoutingHeader{
		SchemaType:     schemaType,
		TTL:            7,
		Priority:       PriorityNormal,
		Encrypted:      true,
		EncryptionMode: EncryptionECIES,
		SourcePeer:     sourcePeer,
		Timestamp:      uint64(time.Now().UnixMilli()),
	}
}

// Manager manages subscriptions and message routing
type Manager struct {
	subscriptions map[string]*Subscription
	handlers      map[string][]MessageHandler
	globalHandler []MessageHandler
	mu            sync.RWMutex
}

// MessageHandler is called when a message matches a subscription
type MessageHandler func(sub *Subscription, schema string, data []byte, from string, header *RoutingHeader)

// NewManager creates a new subscription manager
func NewManager() *Manager {
	return &Manager{
		subscriptions: make(map[string]*Subscription),
		handlers:      make(map[string][]MessageHandler),
	}
}

// CreateSubscription creates a new subscription
func (m *Manager) CreateSubscription(config SubscriptionConfig) (*Subscription, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	sub := &Subscription{
		ID:        generateID(),
		Config:    config,
		CreatedAt: time.Now(),
		Status:    StatusActive,
	}

	m.mu.Lock()
	m.subscriptions[sub.ID] = sub
	m.mu.Unlock()

	log.Infof("Created subscription %s for schemas %v", sub.ID, config.DataTypes)
	return sub, nil
}

// GetSubscription gets a subscription by ID
func (m *Manager) GetSubscription(id string) (*Subscription, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sub, ok := m.subscriptions[id]
	if !ok {
		return nil, ErrSubscriptionNotFound
	}
	return sub, nil
}

// ListSubscriptions lists all subscriptions
func (m *Manager) ListSubscriptions() []*Subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()

	subs := make([]*Subscription, 0, len(m.subscriptions))
	for _, sub := range m.subscriptions {
		subs = append(subs, sub)
	}
	return subs
}

// UpdateSubscription updates a subscription configuration
func (m *Manager) UpdateSubscription(id string, config SubscriptionConfig) (*Subscription, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	sub, ok := m.subscriptions[id]
	if !ok {
		return nil, ErrSubscriptionNotFound
	}

	sub.Config = config
	log.Infof("Updated subscription %s", id)
	return sub, nil
}

// DeleteSubscription removes a subscription
func (m *Manager) DeleteSubscription(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.subscriptions[id]; !ok {
		return ErrSubscriptionNotFound
	}

	delete(m.subscriptions, id)
	delete(m.handlers, id)
	log.Infof("Deleted subscription %s", id)
	return nil
}

// PauseSubscription pauses a subscription
func (m *Manager) PauseSubscription(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sub, ok := m.subscriptions[id]
	if !ok {
		return ErrSubscriptionNotFound
	}

	sub.Status = StatusPaused
	log.Infof("Paused subscription %s", id)
	return nil
}

// ResumeSubscription resumes a subscription
func (m *Manager) ResumeSubscription(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sub, ok := m.subscriptions[id]
	if !ok {
		return ErrSubscriptionNotFound
	}

	sub.Status = StatusActive
	sub.ErrorMessage = ""
	log.Infof("Resumed subscription %s", id)
	return nil
}

// AddHandler adds a message handler for a subscription
func (m *Manager) AddHandler(subscriptionID string, handler MessageHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.handlers[subscriptionID] = append(m.handlers[subscriptionID], handler)
}

// AddGlobalHandler adds a handler for all subscriptions
func (m *Manager) AddGlobalHandler(handler MessageHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.globalHandler = append(m.globalHandler, handler)
}

// ProcessMessage processes an incoming message against all subscriptions
func (m *Manager) ProcessMessage(schema string, data []byte, from string, header *RoutingHeader) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, sub := range m.subscriptions {
		if sub.Status != StatusActive {
			continue
		}

		if !m.matchesSubscription(sub, schema, from, header, data) {
			continue
		}

		// Check rate limit
		if !sub.checkRateLimit() {
			log.Debugf("Rate limit exceeded for subscription %s", sub.ID)
			continue
		}

		// Update stats
		sub.MessageCount++
		now := time.Now()
		sub.LastMessageAt = &now

		// Call subscription-specific handlers
		if handlers, ok := m.handlers[sub.ID]; ok {
			for _, handler := range handlers {
				go handler(sub, schema, data, from, header)
			}
		}

		// Call global handlers
		for _, handler := range m.globalHandler {
			go handler(sub, schema, data, from, header)
		}
	}
}

// matchesSubscription checks if a message matches subscription criteria
func (m *Manager) matchesSubscription(sub *Subscription, schema string, from string, header *RoutingHeader, data []byte) bool {
	config := sub.Config

	// Check schema match
	schemaMatches := false
	for _, dt := range config.DataTypes {
		if dt == schema {
			schemaMatches = true
			break
		}
	}
	if !schemaMatches {
		return false
	}

	// Check source peer match
	peerMatches := false
	for _, sp := range config.SourcePeers {
		if sp == "all" || sp == from {
			peerMatches = true
			break
		}
	}
	if !peerMatches {
		return false
	}

	// Check encryption preference
	if header != nil && config.Encrypted != header.Encrypted {
		return false
	}

	// Check filters
	if len(config.Filters) > 0 {
		if !evaluateFilters(data, config.Filters) {
			return false
		}
	}

	return true
}

// checkRateLimit checks and updates rate limit for a subscription
func (sub *Subscription) checkRateLimit() bool {
	if sub.Config.RateLimit <= 0 {
		return true
	}

	sub.rateLimitMu.Lock()
	defer sub.rateLimitMu.Unlock()

	now := time.Now()
	if now.After(sub.rateLimitReset) {
		sub.rateLimitCount = 0
		sub.rateLimitReset = now.Add(time.Minute)
	}

	if sub.rateLimitCount >= sub.Config.RateLimit {
		return false
	}

	sub.rateLimitCount++
	return true
}

// GetRequiredTopics returns all topics needed for active subscriptions
func (m *Manager) GetRequiredTopics() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	topicSet := make(map[string]bool)

	for _, sub := range m.subscriptions {
		if sub.Status != StatusActive {
			continue
		}

		for _, dataType := range sub.Config.DataTypes {
			// Schema-based topic
			topicSet[GetSchemaRoutingTopic(dataType)] = true
			// Standard SDN topic
			topicSet[GetSDNTopic(dataType)] = true
		}

		// Peer-specific topics
		for _, peer := range sub.Config.SourcePeers {
			if peer != "all" {
				topicSet[GetPeerRoutingTopic(peer)] = true
			}
		}
	}

	topics := make([]string, 0, len(topicSet))
	for topic := range topicSet {
		topics = append(topics, topic)
	}
	return topics
}

// GetSchemaRoutingTopic returns the topic for schema-based routing
func GetSchemaRoutingTopic(schemaType string) string {
	// Remove .fbs suffix if present
	schema := schemaType
	if len(schema) > 4 && schema[len(schema)-4:] == ".fbs" {
		schema = schema[:len(schema)-4]
	}
	return fmt.Sprintf("/sdn/data/%s", schema)
}

// GetPeerRoutingTopic returns the topic for peer-based routing
func GetPeerRoutingTopic(peerID string) string {
	return fmt.Sprintf("/sdn/peer/%s", peerID)
}

// GetSDNTopic returns the standard SDN topic for a schema
func GetSDNTopic(schemaType string) string {
	return fmt.Sprintf("/spacedatanetwork/sds/%s", schemaType)
}

// validateConfig validates subscription configuration
func validateConfig(config SubscriptionConfig) error {
	if len(config.DataTypes) == 0 {
		return errors.New("at least one data type must be specified")
	}

	if len(config.SourcePeers) == 0 {
		return errors.New("at least one source peer must be specified")
	}

	if config.RateLimit < 0 {
		return errors.New("rate limit must be non-negative")
	}

	if config.TTL < 0 {
		return errors.New("TTL must be non-negative")
	}

	// Validate filters
	validOps := map[FilterOperator]bool{
		OpEqual: true, OpNotEqual: true, OpGreater: true, OpGreaterEq: true,
		OpLess: true, OpLessEq: true, OpContains: true, OpStartsWith: true,
		OpEndsWith: true, OpIn: true, OpNotIn: true,
	}

	for i, filter := range config.Filters {
		if filter.Field == "" {
			return fmt.Errorf("filter %d: field must be non-empty", i)
		}
		if !validOps[filter.Operator] {
			return fmt.Errorf("filter %d: invalid operator %q", i, filter.Operator)
		}
	}

	return nil
}

// evaluateFilters evaluates filters against JSON data
func evaluateFilters(data []byte, filters []QueryFilter) bool {
	if len(filters) == 0 {
		return true
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return false
	}

	for _, filter := range filters {
		if !evaluateFilter(jsonData, filter) {
			return false
		}
	}

	return true
}

// evaluateFilter evaluates a single filter against data
func evaluateFilter(data map[string]interface{}, filter QueryFilter) bool {
	value := getNestedValue(data, filter.Field)
	if value == nil {
		return filter.Operator == OpNotEqual || filter.Operator == OpNotIn
	}

	switch filter.Operator {
	case OpEqual:
		return compareEqual(value, filter.Value)
	case OpNotEqual:
		return !compareEqual(value, filter.Value)
	case OpGreater:
		return compareNumeric(value, filter.Value) > 0
	case OpGreaterEq:
		return compareNumeric(value, filter.Value) >= 0
	case OpLess:
		return compareNumeric(value, filter.Value) < 0
	case OpLessEq:
		return compareNumeric(value, filter.Value) <= 0
	case OpContains:
		return containsString(value, filter.Value)
	case OpStartsWith:
		return startsWithString(value, filter.Value)
	case OpEndsWith:
		return endsWithString(value, filter.Value)
	case OpIn:
		return inArray(value, filter.Value)
	case OpNotIn:
		return !inArray(value, filter.Value)
	default:
		return false
	}
}

// getNestedValue gets a nested value using dot notation
func getNestedValue(data map[string]interface{}, path string) interface{} {
	parts := splitPath(path)
	current := interface{}(data)

	for _, part := range parts {
		if m, ok := current.(map[string]interface{}); ok {
			current = m[part]
		} else {
			return nil
		}
	}

	return current
}

// splitPath splits a dot-notation path
func splitPath(path string) []string {
	return regexp.MustCompile(`\.`).Split(path, -1)
}

// Helper comparison functions
func compareEqual(a, b interface{}) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func compareNumeric(a, b interface{}) int {
	af, aok := toFloat64(a)
	bf, bok := toFloat64(b)
	if !aok || !bok {
		return 0
	}
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	return 0
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}

func containsString(a, b interface{}) bool {
	as, aok := a.(string)
	bs, bok := b.(string)
	if !aok || !bok {
		return false
	}
	return regexp.MustCompile(regexp.QuoteMeta(bs)).MatchString(as)
}

func startsWithString(a, b interface{}) bool {
	as, aok := a.(string)
	bs, bok := b.(string)
	if !aok || !bok {
		return false
	}
	return len(as) >= len(bs) && as[:len(bs)] == bs
}

func endsWithString(a, b interface{}) bool {
	as, aok := a.(string)
	bs, bok := b.(string)
	if !aok || !bok {
		return false
	}
	return len(as) >= len(bs) && as[len(as)-len(bs):] == bs
}

func inArray(value, arr interface{}) bool {
	arrSlice, ok := arr.([]interface{})
	if !ok {
		return false
	}
	for _, item := range arrSlice {
		if compareEqual(value, item) {
			return true
		}
	}
	return false
}

// generateID generates a unique subscription ID
func generateID() string {
	return fmt.Sprintf("sub_%d_%x", time.Now().UnixNano(), time.Now().UnixNano()%0xFFFF)
}
