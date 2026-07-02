package trust

// WS11.4 — web-of-trust pub/sub events. When a subject's trust status flips
// (WS11.3 StatusChange), the event is published to the gossipsub topics of
// every node within the subject's web-of-trust neighborhood (the DAG
// neighborhood in both directions, depth-bounded).
//
// Event wire format (no SDS schema fits a trust-status event; this is the
// documented v1 envelope, framed like the channel-chat envelope):
//
//	u32LE(len(eventJSON)) || eventJSON || senderPub(32) || sig(64)
//
// eventJSON is the compact JSON of TrustEvent (deterministic field order via
// struct marshalling), and sig = ed25519(senderPriv,
// "SDN-TRUST-EVT\0" || eventJSON). Events are signed-plaintext: a trust
// status is an assertion the evaluator WANTS its web to see; authenticity
// (not confidentiality) is the requirement.

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// TrustTopic is the per-node gossipsub topic trust events are delivered to.
func TrustTopic(nodeID string) string {
	return "/spacedatanetwork/trust/" + nodeID
}

var trustEventSigPrefix = []byte("SDN-TRUST-EVT\x00")

// TrustEvent is the v1 published trust-status change.
type TrustEvent struct {
	Version    int     `json:"v"`
	Evaluator  string  `json:"evaluator"`
	Subject    string  `json:"subject"`
	OldScore   float64 `json:"oldScore"`
	NewScore   float64 `json:"newScore"`
	OldTrusted bool    `json:"oldTrusted"`
	NewTrusted bool    `json:"newTrusted"`
	AtMs       int64   `json:"atMs"`
}

// EncodeTrustEvent seals a StatusChange into the signed v1 envelope.
func EncodeTrustEvent(change StatusChange, senderPriv ed25519.PrivateKey) ([]byte, error) {
	if len(senderPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("trust: sender private key must be ed25519")
	}
	evt := TrustEvent{
		Version:    1,
		Evaluator:  change.Evaluator,
		Subject:    change.Subject,
		OldScore:   change.OldScore,
		NewScore:   change.NewScore,
		OldTrusted: change.OldTrusted,
		NewTrusted: change.NewTrusted,
		AtMs:       change.AtMs,
	}
	body, err := json.Marshal(evt)
	if err != nil {
		return nil, err
	}
	sigMsg := append(append([]byte(nil), trustEventSigPrefix...), body...)
	sig := ed25519.Sign(senderPriv, sigMsg)
	pub := senderPriv.Public().(ed25519.PublicKey)

	out := make([]byte, 0, 4+len(body)+ed25519.PublicKeySize+ed25519.SignatureSize)
	var lenLE [4]byte
	binary.LittleEndian.PutUint32(lenLE[:], uint32(len(body)))
	out = append(out, lenLE[:]...)
	out = append(out, body...)
	out = append(out, pub...)
	out = append(out, sig...)
	return out, nil
}

// DecodeTrustEvent opens + signature-verifies a v1 envelope, returning the
// event and the sender's ed25519 public key.
func DecodeTrustEvent(envelope []byte) (*TrustEvent, ed25519.PublicKey, error) {
	if len(envelope) < 4 {
		return nil, nil, errors.New("trust: event envelope too short")
	}
	n := binary.LittleEndian.Uint32(envelope[:4])
	need := 4 + int(n) + ed25519.PublicKeySize + ed25519.SignatureSize
	if len(envelope) != need {
		return nil, nil, fmt.Errorf("trust: event envelope length %d, want %d", len(envelope), need)
	}
	body := envelope[4 : 4+n]
	pub := ed25519.PublicKey(envelope[4+n : 4+int(n)+ed25519.PublicKeySize])
	sig := envelope[4+int(n)+ed25519.PublicKeySize:]

	sigMsg := append(append([]byte(nil), trustEventSigPrefix...), body...)
	if !ed25519.Verify(pub, sigMsg, sig) {
		return nil, nil, errors.New("trust: event signature invalid")
	}
	var evt TrustEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return nil, nil, err
	}
	if evt.Version != 1 {
		return nil, nil, fmt.Errorf("trust: unsupported event version %d", evt.Version)
	}
	return &evt, append(ed25519.PublicKey(nil), pub...), nil
}

// PublishFunc delivers raw bytes to a gossipsub topic.
type PublishFunc func(topic string, data []byte) error

// EventPublisher fans trust flips out to the subject's neighborhood topics.
type EventPublisher struct {
	// Sign key for event authenticity (the local node's ed25519 key).
	SenderPriv ed25519.PrivateKey
	// Publish delivers to gossipsub (node pubsub adapter; tests fake it).
	Publish PublishFunc
	// MaxDepth bounds the neighborhood (0 = unbounded).
	MaxDepth int
}

// FanOut publishes every status change to TrustTopic(member) for each member
// of the flipped subject's web-of-trust neighborhood, plus the subject's own
// topic. Returns the number of deliveries and the first error (best-effort:
// remaining deliveries continue).
func (p *EventPublisher) FanOut(svc *Service, changes []StatusChange) (int, error) {
	if p.Publish == nil {
		return 0, errors.New("trust: EventPublisher.Publish not set")
	}
	delivered := 0
	var firstErr error
	for _, change := range changes {
		envelope, err := EncodeTrustEvent(change, p.SenderPriv)
		if err != nil {
			return delivered, err
		}
		audience := svc.NeighborhoodOf(change.Subject, p.MaxDepth)
		seen := map[string]struct{}{}
		for _, member := range append(audience, change.Subject) {
			if _, dup := seen[member]; dup {
				continue
			}
			seen[member] = struct{}{}
			if err := p.Publish(TrustTopic(member), envelope); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			delivered++
		}
	}
	return delivered, firstErr
}
