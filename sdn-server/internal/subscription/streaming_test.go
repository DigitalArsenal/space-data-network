package subscription

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamingManagerCreateSession(t *testing.T) {
	sm := NewStreamingManager(DefaultStreamingConfig())

	session, err := sm.CreateSession("sub_1", "peer_abc", []string{"OMM", "CDM"}, StreamModeStreaming, EncryptionECIES)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if session.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if session.PeerID != "peer_abc" {
		t.Errorf("expected peer_abc, got %s", session.PeerID)
	}
	if session.Mode != StreamModeStreaming {
		t.Errorf("expected streaming mode, got %d", session.Mode)
	}
	if session.EncMode != EncryptionECIES {
		t.Errorf("expected ECIES encryption, got %d", session.EncMode)
	}
	if !session.Active {
		t.Error("expected session to be active")
	}

	// Cleanup
	sm.CloseSession(session.ID)
}

func TestStreamingManagerMaxSessions(t *testing.T) {
	config := DefaultStreamingConfig()
	config.MaxSessionsPerPeer = 2
	sm := NewStreamingManager(config)

	_, err := sm.CreateSession("s1", "peer_1", []string{"OMM"}, StreamModeSingle, EncryptionNone)
	if err != nil {
		t.Fatalf("first session failed: %v", err)
	}
	_, err = sm.CreateSession("s2", "peer_1", []string{"CDM"}, StreamModeSingle, EncryptionNone)
	if err != nil {
		t.Fatalf("second session failed: %v", err)
	}
	_, err = sm.CreateSession("s3", "peer_1", []string{"EPM"}, StreamModeSingle, EncryptionNone)
	if err == nil {
		t.Error("expected error for third session exceeding max")
	}
}

func TestStreamingManagerDeliverMessage(t *testing.T) {
	config := DefaultStreamingConfig()
	sm := NewStreamingManager(config)

	var delivered int64
	sm.SetDeliveryHandler(func(session *StreamingSession, messages []StreamMessage) error {
		atomic.AddInt64(&delivered, int64(len(messages)))
		return nil
	})

	session, _ := sm.CreateSession("s1", "peer_1", []string{"OMM"}, StreamModeSingle, EncryptionNone)

	header := NewRoutingHeader("OMM", "sender")
	header.Encrypted = false
	sm.DeliverMessage("OMM", []byte("test data"), "sender", header)

	// Single mode delivers synchronously
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt64(&delivered) != 1 {
		t.Errorf("expected 1 delivered message, got %d", delivered)
	}

	sm.CloseSession(session.ID)
}

func TestStreamingSessionKeyGeneration(t *testing.T) {
	sm := NewStreamingManager(DefaultStreamingConfig())

	session, err := sm.CreateSession("s1", "peer_1", []string{"OMM"}, StreamModeSingle, EncryptionSessionKey)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if session.SessionKeyID == "" {
		t.Error("expected session key ID for session-key encryption")
	}

	sm.CloseSession(session.ID)
}

func TestStreamingStats(t *testing.T) {
	sm := NewStreamingManager(DefaultStreamingConfig())

	sm.CreateSession("s1", "p1", []string{"OMM"}, StreamModeStreaming, EncryptionECIES)
	sm.CreateSession("s2", "p2", []string{"CDM"}, StreamModeBatch, EncryptionNone)

	stats := sm.Stats()
	if stats.ActiveSessions != 2 {
		t.Errorf("expected 2 active sessions, got %d", stats.ActiveSessions)
	}
	if stats.SessionsByMode[StreamModeStreaming] != 1 {
		t.Error("expected 1 streaming session")
	}
	if stats.SessionsByMode[StreamModeBatch] != 1 {
		t.Error("expected 1 batch session")
	}
}

func TestEdgeRelayFilter(t *testing.T) {
	filter := &EdgeRelayFilter{
		AllowedSchemas:   map[string]bool{"OMM": true, "CDM": true},
		MinPriority:      PriorityNormal,
		AllowEncrypted:   true,
		AllowUnencrypted: false,
	}

	filterFn := filter.ToTopicFilter()

	// Test: allowed schema, encrypted, normal priority
	header := &RoutingHeader{
		SchemaType: "OMM",
		Priority:   PriorityNormal,
		Encrypted:  true,
	}
	if !filterFn("/sdn/data/OMM", header, nil) {
		t.Error("expected message to pass filter")
	}

	// Test: disallowed schema
	header.SchemaType = "EPM"
	if filterFn("/sdn/data/EPM", header, nil) {
		t.Error("expected EPM to be filtered")
	}

	// Test: unencrypted (not allowed)
	header.SchemaType = "OMM"
	header.Encrypted = false
	if filterFn("/sdn/data/OMM", header, nil) {
		t.Error("expected unencrypted message to be filtered")
	}

	// Test: low priority
	header.Encrypted = true
	header.Priority = PriorityLow
	if filterFn("/sdn/data/OMM", header, nil) {
		t.Error("expected low priority message to be filtered")
	}
}

func TestTopicRouterHandleMessage(t *testing.T) {
	manager := NewManager()
	config := SubscriptionConfig{
		DataTypes:   []string{"OMM"},
		SourcePeers: []string{"all"},
		Encrypted:   true,
		Streaming:   false,
	}
	manager.CreateSubscription(config)

	received := make(chan struct{}, 1)
	manager.AddGlobalHandler(func(sub *Subscription, schema string, data []byte, from string, header *RoutingHeader) {
		if schema == "OMM" {
			received <- struct{}{}
		}
	})

	tr := NewTopicRouter(manager, "local_peer", DefaultStreamingConfig())

	// Create a message with routing header
	header := NewRoutingHeader("OMM", "sender_peer")
	msg, err := CreateMessageWithHeader(header, []byte(`{"OBJECT_NAME":"ISS"}`))
	if err != nil {
		t.Fatalf("CreateMessageWithHeader failed: %v", err)
	}

	err = tr.HandleTopicMessage("/sdn/data/OMM", msg, "sender_peer")
	if err != nil {
		t.Fatalf("HandleTopicMessage failed: %v", err)
	}

	select {
	case <-received:
		// ok
	case <-time.After(time.Second):
		t.Error("expected to receive message")
	}
}
