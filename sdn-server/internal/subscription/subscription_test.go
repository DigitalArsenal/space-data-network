package subscription

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCreateSubscription(t *testing.T) {
	manager := NewManager()

	config := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs", "CDM.fbs"},
		SourcePeers: []string{"all"},
		Encrypted:   true,
		Streaming:   true,
		RateLimit:   100,
	}

	sub, err := manager.CreateSubscription(config)
	if err != nil {
		t.Fatalf("Failed to create subscription: %v", err)
	}

	if sub.ID == "" {
		t.Error("Subscription ID should not be empty")
	}

	if sub.Status != StatusActive {
		t.Errorf("Expected status %s, got %s", StatusActive, sub.Status)
	}

	if len(sub.Config.DataTypes) != 2 {
		t.Errorf("Expected 2 data types, got %d", len(sub.Config.DataTypes))
	}
}

func TestCreateSubscriptionValidation(t *testing.T) {
	manager := NewManager()

	tests := []struct {
		name   string
		config SubscriptionConfig
		errMsg string
	}{
		{
			name:   "empty data types",
			config: SubscriptionConfig{SourcePeers: []string{"all"}},
			errMsg: "at least one data type",
		},
		{
			name:   "empty source peers",
			config: SubscriptionConfig{DataTypes: []string{"OMM.fbs"}},
			errMsg: "at least one source peer",
		},
		{
			name: "negative rate limit",
			config: SubscriptionConfig{
				DataTypes:   []string{"OMM.fbs"},
				SourcePeers: []string{"all"},
				RateLimit:   -1,
			},
			errMsg: "rate limit must be non-negative",
		},
		{
			name: "invalid filter operator",
			config: SubscriptionConfig{
				DataTypes:   []string{"OMM.fbs"},
				SourcePeers: []string{"all"},
				Filters:     []QueryFilter{{Field: "test", Operator: "invalid"}},
			},
			errMsg: "invalid operator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manager.CreateSubscription(tt.config)
			if err == nil {
				t.Error("Expected error but got none")
			}
		})
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	manager := NewManager()

	config := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs"},
		SourcePeers: []string{"all"},
		Encrypted:   true,
		Streaming:   true,
	}

	sub, err := manager.CreateSubscription(config)
	if err != nil {
		t.Fatalf("Failed to create subscription: %v", err)
	}

	// Get subscription
	retrieved, err := manager.GetSubscription(sub.ID)
	if err != nil {
		t.Fatalf("Failed to get subscription: %v", err)
	}
	if retrieved.ID != sub.ID {
		t.Error("Retrieved subscription ID mismatch")
	}

	// Pause subscription
	if err := manager.PauseSubscription(sub.ID); err != nil {
		t.Fatalf("Failed to pause subscription: %v", err)
	}
	retrieved, _ = manager.GetSubscription(sub.ID)
	if retrieved.Status != StatusPaused {
		t.Errorf("Expected status %s, got %s", StatusPaused, retrieved.Status)
	}

	// Resume subscription
	if err := manager.ResumeSubscription(sub.ID); err != nil {
		t.Fatalf("Failed to resume subscription: %v", err)
	}
	retrieved, _ = manager.GetSubscription(sub.ID)
	if retrieved.Status != StatusActive {
		t.Errorf("Expected status %s, got %s", StatusActive, retrieved.Status)
	}

	// Delete subscription
	if err := manager.DeleteSubscription(sub.ID); err != nil {
		t.Fatalf("Failed to delete subscription: %v", err)
	}

	_, err = manager.GetSubscription(sub.ID)
	if err != ErrSubscriptionNotFound {
		t.Error("Expected subscription not found error")
	}
}

func TestProcessMessage(t *testing.T) {
	manager := NewManager()

	config := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs"},
		SourcePeers: []string{"all"},
		Encrypted:   true,
		Streaming:   true,
	}

	sub, _ := manager.CreateSubscription(config)

	received := make(chan struct{}, 1)
	manager.AddHandler(sub.ID, func(s *Subscription, schema string, data []byte, from string, header *RoutingHeader) {
		received <- struct{}{}
	})

	// Process matching message
	header := NewRoutingHeader("OMM.fbs", "peer1")
	manager.ProcessMessage("OMM.fbs", []byte("{}"), "peer1", header)

	select {
	case <-received:
		// Success
	case <-time.After(time.Second):
		t.Error("Handler was not called")
	}

	// Process non-matching schema
	manager.ProcessMessage("CDM.fbs", []byte("{}"), "peer1", header)
	select {
	case <-received:
		t.Error("Handler should not be called for non-matching schema")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestFilterMatching(t *testing.T) {
	manager := NewManager()

	config := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs"},
		SourcePeers: []string{"all"},
		Encrypted:   true,
		Filters: []QueryFilter{
			{Field: "OBJECT_NAME", Operator: OpEqual, Value: "ISS"},
		},
	}

	sub, _ := manager.CreateSubscription(config)

	received := make(chan struct{}, 1)
	manager.AddHandler(sub.ID, func(s *Subscription, schema string, data []byte, from string, header *RoutingHeader) {
		received <- struct{}{}
	})

	// Process matching message
	matchingData, _ := json.Marshal(map[string]interface{}{"OBJECT_NAME": "ISS"})
	header := NewRoutingHeader("OMM.fbs", "peer1")
	manager.ProcessMessage("OMM.fbs", matchingData, "peer1", header)

	select {
	case <-received:
		// Success
	case <-time.After(time.Second):
		t.Error("Handler was not called for matching filter")
	}

	// Process non-matching message
	nonMatchingData, _ := json.Marshal(map[string]interface{}{"OBJECT_NAME": "Hubble"})
	manager.ProcessMessage("OMM.fbs", nonMatchingData, "peer1", header)

	select {
	case <-received:
		t.Error("Handler should not be called for non-matching filter")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestRateLimit(t *testing.T) {
	manager := NewManager()

	config := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs"},
		SourcePeers: []string{"all"},
		Encrypted:   true, // Match the header
		RateLimit:   2,    // Only 2 messages per minute
	}

	sub, _ := manager.CreateSubscription(config)

	// Use a channel to count messages in a thread-safe way
	received := make(chan struct{}, 10)
	manager.AddHandler(sub.ID, func(s *Subscription, schema string, data []byte, from string, header *RoutingHeader) {
		received <- struct{}{}
	})

	header := NewRoutingHeader("OMM.fbs", "peer1")

	// Send 5 messages, only 2 should go through
	for i := 0; i < 5; i++ {
		manager.ProcessMessage("OMM.fbs", []byte("{}"), "peer1", header)
	}

	// Wait for async handlers with timeout
	count := 0
	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-received:
			count++
		case <-timeout:
			break loop
		}
	}

	if count != 2 {
		t.Errorf("Expected 2 messages (rate limited), got %d", count)
	}
}

func TestSourcePeerFiltering(t *testing.T) {
	manager := NewManager()

	config := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs"},
		SourcePeers: []string{"peer1", "peer2"},
		Encrypted:   true,
	}

	sub, _ := manager.CreateSubscription(config)

	received := make(chan string, 10)
	manager.AddHandler(sub.ID, func(s *Subscription, schema string, data []byte, from string, header *RoutingHeader) {
		received <- from
	})

	header := NewRoutingHeader("OMM.fbs", "peer1")

	// These should match
	manager.ProcessMessage("OMM.fbs", []byte("{}"), "peer1", header)
	manager.ProcessMessage("OMM.fbs", []byte("{}"), "peer2", header)
	// This should not match
	manager.ProcessMessage("OMM.fbs", []byte("{}"), "peer3", header)

	time.Sleep(100 * time.Millisecond)

	if len(received) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(received))
	}
}

func TestGetRequiredTopics(t *testing.T) {
	manager := NewManager()

	config1 := SubscriptionConfig{
		DataTypes:   []string{"OMM.fbs", "CDM.fbs"},
		SourcePeers: []string{"all"},
	}

	config2 := SubscriptionConfig{
		DataTypes:   []string{"EPM.fbs"},
		SourcePeers: []string{"peer1"},
	}

	manager.CreateSubscription(config1)
	manager.CreateSubscription(config2)

	topics := manager.GetRequiredTopics()

	// Should have topics for OMM, CDM, EPM, and peer1
	expectedTopics := map[string]bool{
		"/sdn/data/OMM":             true,
		"/sdn/data/CDM":             true,
		"/sdn/data/EPM":             true,
		"/spacedatanetwork/sds/OMM.fbs": true,
		"/spacedatanetwork/sds/CDM.fbs": true,
		"/spacedatanetwork/sds/EPM.fbs": true,
		"/sdn/peer/peer1":           true,
	}

	for _, topic := range topics {
		if !expectedTopics[topic] {
			t.Errorf("Unexpected topic: %s", topic)
		}
	}
}

func TestEvaluateFilter(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]interface{}
		filter   QueryFilter
		expected bool
	}{
		{
			name:     "equal string",
			data:     map[string]interface{}{"name": "ISS"},
			filter:   QueryFilter{Field: "name", Operator: OpEqual, Value: "ISS"},
			expected: true,
		},
		{
			name:     "not equal",
			data:     map[string]interface{}{"name": "ISS"},
			filter:   QueryFilter{Field: "name", Operator: OpNotEqual, Value: "Hubble"},
			expected: true,
		},
		{
			name:     "greater than",
			data:     map[string]interface{}{"altitude": 400.0},
			filter:   QueryFilter{Field: "altitude", Operator: OpGreater, Value: 300.0},
			expected: true,
		},
		{
			name:     "contains",
			data:     map[string]interface{}{"description": "International Space Station"},
			filter:   QueryFilter{Field: "description", Operator: OpContains, Value: "Space"},
			expected: true,
		},
		{
			name:     "nested field",
			data:     map[string]interface{}{"object": map[string]interface{}{"name": "ISS"}},
			filter:   QueryFilter{Field: "object.name", Operator: OpEqual, Value: "ISS"},
			expected: true,
		},
		{
			name:     "in array",
			data:     map[string]interface{}{"type": "satellite"},
			filter:   QueryFilter{Field: "type", Operator: OpIn, Value: []interface{}{"satellite", "debris"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluateFilter(tt.data, tt.filter)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestNewRoutingHeader(t *testing.T) {
	header := NewRoutingHeader("OMM", "peer123")

	if header.SchemaType != "OMM" {
		t.Errorf("Expected schema type OMM, got %s", header.SchemaType)
	}

	if header.SourcePeer != "peer123" {
		t.Errorf("Expected source peer peer123, got %s", header.SourcePeer)
	}

	if header.TTL != 7 {
		t.Errorf("Expected default TTL 7, got %d", header.TTL)
	}

	if header.Priority != PriorityNormal {
		t.Errorf("Expected normal priority, got %d", header.Priority)
	}

	if !header.Encrypted {
		t.Error("Expected encrypted to be true by default")
	}

	if header.Timestamp == 0 {
		t.Error("Expected timestamp to be set")
	}
}
