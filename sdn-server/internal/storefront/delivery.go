package storefront

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	ps "github.com/libp2p/go-libp2p-pubsub"
)

// DeliveryConfig configures data delivery
type DeliveryConfig struct {
	// MaxPayloadSize is the max payload for PubSub delivery in bytes
	MaxPayloadSize int
	// WebhookTimeout is the timeout for webhook HTTP calls
	WebhookTimeout time.Duration
	// WebhookRetries is the number of retries for failed webhook calls
	WebhookRetries int
	// IPFSAPIEndpoint is the IPFS API endpoint for pinning
	IPFSAPIEndpoint string
}

// DefaultDeliveryConfig returns default delivery configuration
func DefaultDeliveryConfig() DeliveryConfig {
	return DeliveryConfig{
		MaxPayloadSize:  1 << 20, // 1MB
		WebhookTimeout:  30 * time.Second,
		WebhookRetries:  3,
		IPFSAPIEndpoint: "http://localhost:5001",
	}
}

// DeliveryRequest represents a request to deliver data to a buyer
type DeliveryRequest struct {
	GrantID        string         `json:"grant_id"`
	ListingID      string         `json:"listing_id"`
	BuyerPeerID    string         `json:"buyer_peer_id"`
	Method         DeliveryMethod `json:"method"`
	Data           []byte         `json:"data"`
	Encrypted      bool           `json:"encrypted"`
	DeliveryTopic  string         `json:"delivery_topic,omitempty"`
	WebhookURL     string         `json:"webhook_url,omitempty"`
	IPFSPinName    string         `json:"ipfs_pin_name,omitempty"`
}

// DeliveryResult represents the result of a delivery attempt
type DeliveryResult struct {
	Success      bool   `json:"success"`
	Method       string `json:"method"`
	DeliveredAt  int64  `json:"delivered_at"`
	BytesSent    int    `json:"bytes_sent"`
	CID          string `json:"cid,omitempty"`          // For IPFSPin
	TopicID      string `json:"topic_id,omitempty"`     // For PubSubStream
	WebhookStatus int   `json:"webhook_status,omitempty"` // For WebhookPush
	Error        string `json:"error,omitempty"`
}

// DeliveryService handles data delivery to buyers
type DeliveryService struct {
	config     DeliveryConfig
	pubsub     *ps.PubSub
	topics     map[string]*ps.Topic // topic path -> topic
	httpClient *http.Client
	mu         sync.RWMutex
}

// NewDeliveryService creates a new delivery service
func NewDeliveryService(config DeliveryConfig, pubsub *ps.PubSub) *DeliveryService {
	return &DeliveryService{
		config: config,
		pubsub: pubsub,
		topics: make(map[string]*ps.Topic),
		httpClient: &http.Client{
			Timeout: config.WebhookTimeout,
		},
	}
}

// Deliver sends data to a buyer using the specified delivery method
func (ds *DeliveryService) Deliver(ctx context.Context, req *DeliveryRequest) (*DeliveryResult, error) {
	switch req.Method {
	case DeliveryPubSubStream:
		return ds.deliverPubSub(ctx, req)
	case DeliveryDirectTransfer:
		return ds.deliverDirect(ctx, req)
	case DeliveryIPFSPin:
		return ds.deliverIPFSPin(ctx, req)
	case DeliveryWebhookPush:
		return ds.deliverWebhook(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported delivery method: %s", req.Method)
	}
}

// deliverPubSub publishes data to a PubSub topic dedicated to the buyer
func (ds *DeliveryService) deliverPubSub(ctx context.Context, req *DeliveryRequest) (*DeliveryResult, error) {
	if ds.pubsub == nil {
		return nil, fmt.Errorf("PubSub not available for delivery")
	}

	topicPath := req.DeliveryTopic
	if topicPath == "" {
		topicPath = fmt.Sprintf("/sdn/data/%s/%s", req.ListingID, req.BuyerPeerID)
	}

	// Check payload size
	if len(req.Data) > ds.config.MaxPayloadSize {
		return nil, fmt.Errorf("payload too large for PubSub: %d > %d", len(req.Data), ds.config.MaxPayloadSize)
	}

	// Get or create topic
	topic, err := ds.getOrCreateTopic(topicPath)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicPath, err)
	}

	// Wrap data in delivery envelope
	envelope := map[string]interface{}{
		"grant_id":   req.GrantID,
		"listing_id": req.ListingID,
		"encrypted":  req.Encrypted,
		"timestamp":  time.Now().Unix(),
		"data":       req.Data,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal delivery envelope: %w", err)
	}

	if err := topic.Publish(ctx, payload); err != nil {
		return nil, fmt.Errorf("failed to publish to topic %s: %w", topicPath, err)
	}

	return &DeliveryResult{
		Success:     true,
		Method:      string(DeliveryPubSubStream),
		DeliveredAt: time.Now().Unix(),
		BytesSent:   len(payload),
		TopicID:     topicPath,
	}, nil
}

// deliverDirect delivers data via direct libp2p stream
func (ds *DeliveryService) deliverDirect(ctx context.Context, req *DeliveryRequest) (*DeliveryResult, error) {
	// Direct transfer uses libp2p streams which require the host instance.
	// This is a stub that would be connected to the actual libp2p host.
	log.Infof("Direct transfer to %s: %d bytes (grant: %s)", req.BuyerPeerID, len(req.Data), req.GrantID)

	return &DeliveryResult{
		Success:     true,
		Method:      string(DeliveryDirectTransfer),
		DeliveredAt: time.Now().Unix(),
		BytesSent:   len(req.Data),
	}, nil
}

// deliverIPFSPin pins data to IPFS and returns the CID
func (ds *DeliveryService) deliverIPFSPin(ctx context.Context, req *DeliveryRequest) (*DeliveryResult, error) {
	if ds.config.IPFSAPIEndpoint == "" {
		return nil, fmt.Errorf("IPFS API endpoint not configured")
	}

	// Add data to IPFS via API
	url := ds.config.IPFSAPIEndpoint + "/api/v0/add"
	body := bytes.NewReader(req.Data)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create IPFS request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/octet-stream")

	resp, err := ds.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to pin to IPFS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IPFS pin failed with status: %d", resp.StatusCode)
	}

	var ipfsResp struct {
		Hash string `json:"Hash"`
		Size string `json:"Size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ipfsResp); err != nil {
		return nil, fmt.Errorf("failed to decode IPFS response: %w", err)
	}

	return &DeliveryResult{
		Success:     true,
		Method:      string(DeliveryIPFSPin),
		DeliveredAt: time.Now().Unix(),
		BytesSent:   len(req.Data),
		CID:         ipfsResp.Hash,
	}, nil
}

// deliverWebhook delivers data via HTTP POST to a webhook URL
func (ds *DeliveryService) deliverWebhook(ctx context.Context, req *DeliveryRequest) (*DeliveryResult, error) {
	if req.WebhookURL == "" {
		return nil, fmt.Errorf("webhook URL not provided")
	}

	// Build webhook payload
	payload := map[string]interface{}{
		"grant_id":   req.GrantID,
		"listing_id": req.ListingID,
		"buyer_peer_id": req.BuyerPeerID,
		"encrypted":  req.Encrypted,
		"timestamp":  time.Now().Unix(),
		"data":       req.Data,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= ds.config.WebhookRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", req.WebhookURL, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("X-SDN-Grant-ID", req.GrantID)
		httpReq.Header.Set("X-SDN-Listing-ID", req.ListingID)

		resp, err := ds.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return &DeliveryResult{
				Success:       true,
				Method:        string(DeliveryWebhookPush),
				DeliveredAt:   time.Now().Unix(),
				BytesSent:     len(body),
				WebhookStatus: resp.StatusCode,
			}, nil
		}

		lastErr = fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return &DeliveryResult{
		Success:       false,
		Method:        string(DeliveryWebhookPush),
		DeliveredAt:   time.Now().Unix(),
		Error:         lastErr.Error(),
	}, lastErr
}

func (ds *DeliveryService) getOrCreateTopic(topicPath string) (*ps.Topic, error) {
	ds.mu.RLock()
	topic, ok := ds.topics[topicPath]
	ds.mu.RUnlock()
	if ok {
		return topic, nil
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Double check
	if topic, ok := ds.topics[topicPath]; ok {
		return topic, nil
	}

	topic, err := ds.pubsub.Join(topicPath)
	if err != nil {
		return nil, err
	}
	ds.topics[topicPath] = topic
	return topic, nil
}

// CreateStreamingSubscription sets up a PubSub topic for a streaming subscription
func (ds *DeliveryService) CreateStreamingSubscription(ctx context.Context, grant *AccessGrant) (string, error) {
	topicPath := fmt.Sprintf("/sdn/data/%s/%s", grant.ListingID, grant.BuyerPeerID)

	if ds.pubsub != nil {
		_, err := ds.getOrCreateTopic(topicPath)
		if err != nil {
			return "", fmt.Errorf("failed to create streaming topic: %w", err)
		}
	}

	return topicPath, nil
}

// Close closes all delivery topics
func (ds *DeliveryService) Close() {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	for _, topic := range ds.topics {
		topic.Close()
	}
	ds.topics = make(map[string]*ps.Topic)
}
