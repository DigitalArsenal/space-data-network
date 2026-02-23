package subscription

import (
	"bytes"
	"testing"
)

func TestSerializeDeserializeRoutingHeader(t *testing.T) {
	original := &RoutingHeader{
		SchemaType:       "OMM",
		DestinationPeers: []string{"peer1", "peer2"},
		TTL:              7,
		Priority:         PriorityHigh,
		Encrypted:        true,
		EncryptionMode:   EncryptionECIES,
		SessionKeyID:     "session123",
		SourcePeer:       "sourcePeer",
		Sequence:         42,
		Timestamp:        1234567890,
		TopicOverride:    "/custom/topic",
		HeaderSignature:  []byte("signature"),
	}

	serialized, err := SerializeRoutingHeader(original)
	if err != nil {
		t.Fatalf("Failed to serialize: %v", err)
	}

	deserialized, err := DeserializeRoutingHeader(serialized)
	if err != nil {
		t.Fatalf("Failed to deserialize: %v", err)
	}

	if deserialized.SchemaType != original.SchemaType {
		t.Errorf("SchemaType mismatch: %s != %s", deserialized.SchemaType, original.SchemaType)
	}

	if len(deserialized.DestinationPeers) != len(original.DestinationPeers) {
		t.Errorf("DestinationPeers length mismatch: %d != %d", len(deserialized.DestinationPeers), len(original.DestinationPeers))
	}

	for i, peer := range deserialized.DestinationPeers {
		if peer != original.DestinationPeers[i] {
			t.Errorf("DestinationPeer[%d] mismatch: %s != %s", i, peer, original.DestinationPeers[i])
		}
	}

	if deserialized.TTL != original.TTL {
		t.Errorf("TTL mismatch: %d != %d", deserialized.TTL, original.TTL)
	}

	if deserialized.Priority != original.Priority {
		t.Errorf("Priority mismatch: %d != %d", deserialized.Priority, original.Priority)
	}

	if deserialized.Encrypted != original.Encrypted {
		t.Errorf("Encrypted mismatch: %v != %v", deserialized.Encrypted, original.Encrypted)
	}

	if deserialized.EncryptionMode != original.EncryptionMode {
		t.Errorf("EncryptionMode mismatch: %d != %d", deserialized.EncryptionMode, original.EncryptionMode)
	}

	if deserialized.SessionKeyID != original.SessionKeyID {
		t.Errorf("SessionKeyID mismatch: %s != %s", deserialized.SessionKeyID, original.SessionKeyID)
	}

	if deserialized.SourcePeer != original.SourcePeer {
		t.Errorf("SourcePeer mismatch: %s != %s", deserialized.SourcePeer, original.SourcePeer)
	}

	if deserialized.Sequence != original.Sequence {
		t.Errorf("Sequence mismatch: %d != %d", deserialized.Sequence, original.Sequence)
	}

	if deserialized.Timestamp != original.Timestamp {
		t.Errorf("Timestamp mismatch: %d != %d", deserialized.Timestamp, original.Timestamp)
	}

	if deserialized.TopicOverride != original.TopicOverride {
		t.Errorf("TopicOverride mismatch: %s != %s", deserialized.TopicOverride, original.TopicOverride)
	}

	if !bytes.Equal(deserialized.HeaderSignature, original.HeaderSignature) {
		t.Errorf("HeaderSignature mismatch")
	}
}

func TestSerializeMinimalHeader(t *testing.T) {
	header := &RoutingHeader{
		SchemaType: "OMM",
	}

	serialized, err := SerializeRoutingHeader(header)
	if err != nil {
		t.Fatalf("Failed to serialize minimal header: %v", err)
	}

	deserialized, err := DeserializeRoutingHeader(serialized)
	if err != nil {
		t.Fatalf("Failed to deserialize minimal header: %v", err)
	}

	if deserialized.SchemaType != "OMM" {
		t.Errorf("SchemaType mismatch: %s", deserialized.SchemaType)
	}
}

func TestSerializeEmptySchemaType(t *testing.T) {
	header := &RoutingHeader{}

	_, err := SerializeRoutingHeader(header)
	if err == nil {
		t.Error("Expected error for empty schema type")
	}
}

func TestDeserializeInvalidData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too short", []byte{0x03, 'O', 'M'}},
		{"invalid schema length", []byte{0xFF}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DeserializeRoutingHeader(tt.data)
			if err == nil {
				t.Error("Expected error for invalid data")
			}
		})
	}
}

func TestCreateParseMessageWithHeader(t *testing.T) {
	header := &RoutingHeader{
		SchemaType: "CDM",
		TTL:        5,
		Priority:   PriorityCritical,
		Encrypted:  true,
	}

	payload := []byte(`{"collision_probability": 0.001}`)

	message, err := CreateMessageWithHeader(header, payload)
	if err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	parsedHeader, parsedPayload, err := ParseMessageWithHeader(message)
	if err != nil {
		t.Fatalf("Failed to parse message: %v", err)
	}

	if parsedHeader.SchemaType != header.SchemaType {
		t.Errorf("Schema type mismatch: %s != %s", parsedHeader.SchemaType, header.SchemaType)
	}

	if parsedHeader.TTL != header.TTL {
		t.Errorf("TTL mismatch: %d != %d", parsedHeader.TTL, header.TTL)
	}

	if parsedHeader.Priority != header.Priority {
		t.Errorf("Priority mismatch: %d != %d", parsedHeader.Priority, header.Priority)
	}

	if !bytes.Equal(parsedPayload, payload) {
		t.Errorf("Payload mismatch: %s != %s", parsedPayload, payload)
	}
}

func TestRouterIsDestinedForUs(t *testing.T) {
	manager := NewManager()
	router := NewRouter(manager, "localPeer")

	tests := []struct {
		name     string
		header   *RoutingHeader
		expected bool
	}{
		{
			name:     "broadcast",
			header:   &RoutingHeader{SchemaType: "OMM", DestinationPeers: []string{}},
			expected: true,
		},
		{
			name:     "destined for us",
			header:   &RoutingHeader{SchemaType: "OMM", DestinationPeers: []string{"localPeer"}},
			expected: true,
		},
		{
			name:     "destined for others",
			header:   &RoutingHeader{SchemaType: "OMM", DestinationPeers: []string{"peer1", "peer2"}},
			expected: false,
		},
		{
			name:     "destined for us and others",
			header:   &RoutingHeader{SchemaType: "OMM", DestinationPeers: []string{"peer1", "localPeer"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := router.isDestinedForUs(tt.header)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestRouterShouldForward(t *testing.T) {
	manager := NewManager()
	router := NewRouter(manager, "localPeer")

	tests := []struct {
		name     string
		header   *RoutingHeader
		isForUs  bool
		expected bool
	}{
		{
			name:     "TTL expired",
			header:   &RoutingHeader{SchemaType: "OMM", TTL: 1},
			isForUs:  true,
			expected: false,
		},
		{
			name:     "broadcast with TTL",
			header:   &RoutingHeader{SchemaType: "OMM", TTL: 5, DestinationPeers: []string{}},
			isForUs:  true,
			expected: true,
		},
		{
			name:     "only for us",
			header:   &RoutingHeader{SchemaType: "OMM", TTL: 5, DestinationPeers: []string{"localPeer"}},
			isForUs:  true,
			expected: false,
		},
		{
			name:     "for us and others",
			header:   &RoutingHeader{SchemaType: "OMM", TTL: 5, DestinationPeers: []string{"localPeer", "peer2"}},
			isForUs:  true,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := router.shouldForward(tt.header, tt.isForUs)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestRouterRelayMode(t *testing.T) {
	manager := NewManager()
	router := NewRouter(manager, "localPeer")
	router.SetRelayMode(true)

	forwardCalled := false
	router.SetForwardHandler(func(header *RoutingHeader, payload []byte) error {
		forwardCalled = true
		return nil
	})

	header := &RoutingHeader{
		SchemaType:       "OMM",
		TTL:              5,
		DestinationPeers: []string{"peer1"},
	}

	headerBytes, _ := SerializeRoutingHeader(header)
	payload := []byte("test payload")

	err := router.RouteMessage(headerBytes, payload, "sender")
	if err != nil {
		t.Fatalf("RouteMessage failed: %v", err)
	}

	if !forwardCalled {
		t.Error("Forward handler should be called in relay mode")
	}
}

func TestGetRoutingTopic(t *testing.T) {
	tests := []struct {
		name     string
		header   *RoutingHeader
		expected string
	}{
		{
			name:     "schema-based",
			header:   &RoutingHeader{SchemaType: "OMM"},
			expected: "/sdn/data/OMM",
		},
		{
			name:     "single destination",
			header:   &RoutingHeader{SchemaType: "OMM", DestinationPeers: []string{"peer1"}},
			expected: "/sdn/peer/peer1",
		},
		{
			name:     "topic override",
			header:   &RoutingHeader{SchemaType: "OMM", TopicOverride: "/custom/topic"},
			expected: "/custom/topic",
		},
		{
			name:     "multiple destinations uses schema",
			header:   &RoutingHeader{SchemaType: "OMM", DestinationPeers: []string{"peer1", "peer2"}},
			expected: "/sdn/data/OMM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRoutingTopic(tt.header)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestTopicMatcher(t *testing.T) {
	tm := NewTopicMatcher()
	tm.AddSchemaType("OMM.fbs")
	tm.AddSchemaType("CDM.fbs")
	tm.AddPeer("peer1")

	tests := []struct {
		topic    string
		expected bool
	}{
		{"/sdn/data/OMM", true},
		{"/sdn/data/CDM", true},
		{"/spacedatanetwork/sds/OMM.fbs", true},
		{"/spacedatanetwork/sds/CDM.fbs", true},
		{"/sdn/peer/peer1", true},
		{"/sdn/data/EPM", false},
		{"/sdn/peer/peer2", false},
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			result := tm.Matches(tt.topic)
			if result != tt.expected {
				t.Errorf("Expected %v for topic %s, got %v", tt.expected, tt.topic, result)
			}
		})
	}

	topics := tm.Topics()
	if len(topics) == 0 {
		t.Error("Expected topics to be returned")
	}
}

func TestSchemaRoutingTopic(t *testing.T) {
	tests := []struct {
		schemaType string
		expected   string
	}{
		{"OMM", "/sdn/data/OMM"},
		{"OMM.fbs", "/sdn/data/OMM"},
		{"CDM.fbs", "/sdn/data/CDM"},
	}

	for _, tt := range tests {
		t.Run(tt.schemaType, func(t *testing.T) {
			result := GetSchemaRoutingTopic(tt.schemaType)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestPeerRoutingTopic(t *testing.T) {
	result := GetPeerRoutingTopic("QmXyz123")
	expected := "/sdn/peer/QmXyz123"
	if result != expected {
		t.Errorf("Expected %s, got %s", expected, result)
	}
}

func BenchmarkSerializeRoutingHeader(b *testing.B) {
	header := &RoutingHeader{
		SchemaType:       "OMM",
		DestinationPeers: []string{"peer1", "peer2", "peer3"},
		TTL:              7,
		Priority:         PriorityNormal,
		Encrypted:        true,
		EncryptionMode:   EncryptionECIES,
		SourcePeer:       "sourcePeer",
		Sequence:         12345,
		Timestamp:        1234567890,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SerializeRoutingHeader(header)
	}
}

func BenchmarkDeserializeRoutingHeader(b *testing.B) {
	header := &RoutingHeader{
		SchemaType:       "OMM",
		DestinationPeers: []string{"peer1", "peer2", "peer3"},
		TTL:              7,
		Priority:         PriorityNormal,
		Encrypted:        true,
		EncryptionMode:   EncryptionECIES,
		SourcePeer:       "sourcePeer",
		Sequence:         12345,
		Timestamp:        1234567890,
	}

	data, _ := SerializeRoutingHeader(header)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DeserializeRoutingHeader(data)
	}
}
