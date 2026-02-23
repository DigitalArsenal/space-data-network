// Package subscription provides streaming mode management for SDN.
package subscription

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// StreamMode represents the delivery mode for subscriptions
type StreamMode uint8

const (
	// StreamModeSingle delivers messages individually
	StreamModeSingle StreamMode = 0
	// StreamModeStreaming delivers messages in real-time as they arrive
	StreamModeStreaming StreamMode = 1
	// StreamModeBatch collects messages and delivers in batches
	StreamModeBatch StreamMode = 2
)

// Streaming errors
var (
	ErrSessionNotFound   = errors.New("streaming session not found")
	ErrSessionExpired    = errors.New("streaming session expired")
	ErrSessionClosed     = errors.New("streaming session closed")
	ErrInvalidStreamMode = errors.New("invalid streaming mode")
)

// StreamingSession represents an active streaming session between peers
type StreamingSession struct {
	ID            string         `json:"id"`
	SubscriptionID string       `json:"subscriptionId"`
	PeerID        string         `json:"peerId"`
	SchemaTypes   []string       `json:"schemaTypes"`
	Mode          StreamMode     `json:"mode"`
	EncMode       EncryptionMode `json:"encryptionMode"`
	SessionKeyID  string         `json:"sessionKeyId,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
	LastActivity  time.Time      `json:"lastActivity"`
	MessagesSent  int64          `json:"messagesSent"`
	BytesSent     int64          `json:"bytesSent"`
	Active        bool           `json:"active"`

	// Internal
	ctx    context.Context
	cancel context.CancelFunc
	msgCh  chan StreamMessage
}

// StreamMessage is a message delivered through a streaming session
type StreamMessage struct {
	SchemaType string
	Data       []byte
	Header     *RoutingHeader
	From       string
	Timestamp  time.Time
}

// StreamingConfig configures streaming behavior
type StreamingConfig struct {
	// MaxSessions per peer
	MaxSessionsPerPeer int `json:"maxSessionsPerPeer"`
	// SessionTimeout is how long an idle session stays alive
	SessionTimeout time.Duration `json:"sessionTimeout"`
	// BatchSize is the number of messages per batch in batch mode
	BatchSize int `json:"batchSize"`
	// BatchInterval is the max wait time before flushing a partial batch
	BatchInterval time.Duration `json:"batchInterval"`
	// ChannelBufferSize is the size of the per-session message channel
	ChannelBufferSize int `json:"channelBufferSize"`
}

// DefaultStreamingConfig returns sensible defaults for streaming
func DefaultStreamingConfig() StreamingConfig {
	return StreamingConfig{
		MaxSessionsPerPeer: 10,
		SessionTimeout:     5 * time.Minute,
		BatchSize:          100,
		BatchInterval:      5 * time.Second,
		ChannelBufferSize:  1000,
	}
}

// StreamingManager manages streaming sessions
type StreamingManager struct {
	config   StreamingConfig
	sessions map[string]*StreamingSession // session ID -> session
	byPeer   map[string][]string          // peer ID -> session IDs
	mu       sync.RWMutex

	// onDeliver is called to send a message to a peer
	onDeliver func(session *StreamingSession, messages []StreamMessage) error
}

// NewStreamingManager creates a new streaming manager
func NewStreamingManager(config StreamingConfig) *StreamingManager {
	return &StreamingManager{
		config:   config,
		sessions: make(map[string]*StreamingSession),
		byPeer:   make(map[string][]string),
	}
}

// SetDeliveryHandler sets the function called to deliver messages to peers
func (sm *StreamingManager) SetDeliveryHandler(handler func(session *StreamingSession, messages []StreamMessage) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onDeliver = handler
}

// CreateSession creates a new streaming session
func (sm *StreamingManager) CreateSession(subscriptionID, peerID string, schemaTypes []string, mode StreamMode, encMode EncryptionMode) (*StreamingSession, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check per-peer limit
	if peerSessions := sm.byPeer[peerID]; len(peerSessions) >= sm.config.MaxSessionsPerPeer {
		return nil, fmt.Errorf("peer %s has reached max sessions (%d)", peerID, sm.config.MaxSessionsPerPeer)
	}

	sessionID := generateSessionID()
	ctx, cancel := context.WithCancel(context.Background())

	session := &StreamingSession{
		ID:             sessionID,
		SubscriptionID: subscriptionID,
		PeerID:         peerID,
		SchemaTypes:    schemaTypes,
		Mode:           mode,
		EncMode:        encMode,
		CreatedAt:      time.Now(),
		LastActivity:   time.Now(),
		Active:         true,
		ctx:            ctx,
		cancel:         cancel,
		msgCh:          make(chan StreamMessage, sm.config.ChannelBufferSize),
	}

	// Generate session key ID for session-key encryption
	if encMode == EncryptionSessionKey {
		session.SessionKeyID = generateSessionKeyID()
	}

	sm.sessions[sessionID] = session
	sm.byPeer[peerID] = append(sm.byPeer[peerID], sessionID)

	// Start delivery goroutine based on mode
	switch mode {
	case StreamModeStreaming:
		go sm.streamDeliveryLoop(session)
	case StreamModeBatch:
		go sm.batchDeliveryLoop(session)
	}

	log.Infof("Created streaming session %s for peer %s (mode=%d, enc=%d)", sessionID, peerID, mode, encMode)
	return session, nil
}

// CloseSession closes a streaming session
func (sm *StreamingManager) CloseSession(sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	session.Active = false
	session.cancel()
	close(session.msgCh)

	// Remove from peer index
	peerSessions := sm.byPeer[session.PeerID]
	for i, id := range peerSessions {
		if id == sessionID {
			sm.byPeer[session.PeerID] = append(peerSessions[:i], peerSessions[i+1:]...)
			break
		}
	}

	delete(sm.sessions, sessionID)
	log.Infof("Closed streaming session %s", sessionID)
	return nil
}

// GetSession returns a session by ID
func (sm *StreamingManager) GetSession(sessionID string) (*StreamingSession, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

// ListSessions returns all active sessions
func (sm *StreamingManager) ListSessions() []*StreamingSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessions := make([]*StreamingSession, 0, len(sm.sessions))
	for _, session := range sm.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// GetPeerSessions returns sessions for a specific peer
func (sm *StreamingManager) GetPeerSessions(peerID string) []*StreamingSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessionIDs := sm.byPeer[peerID]
	sessions := make([]*StreamingSession, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		if session, ok := sm.sessions[id]; ok {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

// DeliverMessage delivers a message to all matching sessions
func (sm *StreamingManager) DeliverMessage(schemaType string, data []byte, from string, header *RoutingHeader) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	msg := StreamMessage{
		SchemaType: schemaType,
		Data:       data,
		Header:     header,
		From:       from,
		Timestamp:  time.Now(),
	}

	for _, session := range sm.sessions {
		if !session.Active {
			continue
		}

		// Check if session subscribes to this schema type
		if !matchesSchemaTypes(session.SchemaTypes, schemaType) {
			continue
		}

		// Check encryption mode compatibility
		if header != nil && session.EncMode != EncryptionNone && !header.Encrypted {
			continue
		}

		// For single mode, deliver immediately
		if session.Mode == StreamModeSingle {
			sm.deliverSingle(session, msg)
			continue
		}

		// For streaming/batch, send to channel
		select {
		case session.msgCh <- msg:
			session.LastActivity = time.Now()
		default:
			log.Warnf("Session %s channel full, dropping message", session.ID)
		}
	}
}

// deliverSingle delivers a single message immediately
func (sm *StreamingManager) deliverSingle(session *StreamingSession, msg StreamMessage) {
	if sm.onDeliver == nil {
		return
	}

	if err := sm.onDeliver(session, []StreamMessage{msg}); err != nil {
		log.Warnf("Failed to deliver to session %s: %v", session.ID, err)
	} else {
		session.MessagesSent++
		session.BytesSent += int64(len(msg.Data))
	}
}

// streamDeliveryLoop delivers messages in real-time as they arrive
func (sm *StreamingManager) streamDeliveryLoop(session *StreamingSession) {
	timeout := time.NewTimer(sm.config.SessionTimeout)
	defer timeout.Stop()

	for {
		select {
		case <-session.ctx.Done():
			return
		case msg, ok := <-session.msgCh:
			if !ok {
				return
			}
			timeout.Reset(sm.config.SessionTimeout)
			sm.deliverSingle(session, msg)
		case <-timeout.C:
			log.Infof("Session %s timed out", session.ID)
			go func() {
				sm.CloseSession(session.ID)
			}()
			return
		}
	}
}

// batchDeliveryLoop collects messages into batches before delivery
func (sm *StreamingManager) batchDeliveryLoop(session *StreamingSession) {
	batch := make([]StreamMessage, 0, sm.config.BatchSize)
	ticker := time.NewTicker(sm.config.BatchInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if sm.onDeliver != nil {
			if err := sm.onDeliver(session, batch); err != nil {
				log.Warnf("Failed to deliver batch to session %s: %v", session.ID, err)
			} else {
				for _, msg := range batch {
					session.MessagesSent++
					session.BytesSent += int64(len(msg.Data))
				}
			}
		}
		batch = batch[:0]
	}

	timeout := time.NewTimer(sm.config.SessionTimeout)
	defer timeout.Stop()

	for {
		select {
		case <-session.ctx.Done():
			flush()
			return
		case msg, ok := <-session.msgCh:
			if !ok {
				flush()
				return
			}
			timeout.Reset(sm.config.SessionTimeout)
			batch = append(batch, msg)
			if len(batch) >= sm.config.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-timeout.C:
			flush()
			log.Infof("Session %s timed out", session.ID)
			go func() {
				sm.CloseSession(session.ID)
			}()
			return
		}
	}
}

// CleanupExpiredSessions removes sessions that have been idle too long
func (sm *StreamingManager) CleanupExpiredSessions() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-sm.config.SessionTimeout)
	expired := make([]string, 0)

	for id, session := range sm.sessions {
		if session.LastActivity.Before(cutoff) {
			expired = append(expired, id)
		}
	}

	for _, id := range expired {
		session := sm.sessions[id]
		session.Active = false
		session.cancel()
		close(session.msgCh)

		// Remove from peer index
		peerSessions := sm.byPeer[session.PeerID]
		for i, sid := range peerSessions {
			if sid == id {
				sm.byPeer[session.PeerID] = append(peerSessions[:i], peerSessions[i+1:]...)
				break
			}
		}

		delete(sm.sessions, id)
	}

	return len(expired)
}

// Stats returns streaming statistics
func (sm *StreamingManager) Stats() StreamingStats {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stats := StreamingStats{
		ActiveSessions:   len(sm.sessions),
		SessionsByMode:   make(map[StreamMode]int),
		SessionsByEncMode: make(map[EncryptionMode]int),
	}

	for _, session := range sm.sessions {
		stats.SessionsByMode[session.Mode]++
		stats.SessionsByEncMode[session.EncMode]++
		stats.TotalMessagesSent += session.MessagesSent
		stats.TotalBytesSent += session.BytesSent
	}

	return stats
}

// StreamingStats holds streaming statistics
type StreamingStats struct {
	ActiveSessions    int                    `json:"activeSessions"`
	SessionsByMode    map[StreamMode]int     `json:"sessionsByMode"`
	SessionsByEncMode map[EncryptionMode]int `json:"sessionsByEncMode"`
	TotalMessagesSent int64                  `json:"totalMessagesSent"`
	TotalBytesSent    int64                  `json:"totalBytesSent"`
}

// matchesSchemaTypes checks if a schema type matches a list of schema types
func matchesSchemaTypes(schemaTypes []string, schemaType string) bool {
	for _, st := range schemaTypes {
		if st == schemaType || st == "all" {
			return true
		}
	}
	return false
}

func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "sess_" + hex.EncodeToString(b)
}

func generateSessionKeyID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "sk_" + hex.EncodeToString(b)
}
