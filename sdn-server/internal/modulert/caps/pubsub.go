package caps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
)

// NewPubSubCapFactory returns a CapFactory for the "pubsub" capability.
// It allows modules to publish to and subscribe from libp2p GossipSub topics.
//
// Supported operations:
//
//	pubsub.publish   — {"topic":"...", "data":"utf8 string"}
//	pubsub.subscribe — {"topic":"..."}  → subscribes; messages are delivered
//	                    via InvokeMethod("on_pubsub_message", ...) on the module
//	pubsub.unsubscribe — {"topic":"..."}
//	pubsub.list_topics — {} → {"topics":["..."]}
func NewPubSubCapFactory(ps *pubsub.PubSub) modulert.CapFactory {
	return func(mod *modulert.Module) modulert.CapHandler {
		h := &pubsubCapHandler{
			ps:     ps,
			mod:    mod,
			topics: make(map[string]*pubsub.Topic),
			subs:   make(map[string]*pubsub.Subscription),
		}
		return h.handle
	}
}

type pubsubCapHandler struct {
	ps     *pubsub.PubSub
	mod    *modulert.Module
	mu     sync.Mutex
	topics map[string]*pubsub.Topic
	subs   map[string]*pubsub.Subscription
}

func (h *pubsubCapHandler) handle(operation string, payload []byte) ([]byte, error) {
	var p map[string]interface{}
	if len(payload) > 0 {
		json.Unmarshal(payload, &p) //nolint:errcheck
	}
	str := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			return capText(v)
		}
		return ""
	}

	switch operation {
	case "pubsub.publish":
		topic := str("topic")
		if topic == "" {
			return errCapJSON("missing topic"), nil
		}
		// Binary data arrives as "base64" (typed hostcalls); "data" is UTF-8 text.
		data := []byte(str("data"))
		if raw := str("base64"); raw != "" {
			decoded, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return errCapJSON("invalid base64 payload"), nil
			}
			data = decoded
		}

		t, err := h.joinTopic(topic)
		if err != nil {
			return errCapJSON("failed to join topic: " + err.Error()), nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := t.Publish(ctx, data); err != nil {
			return errCapJSON("publish failed: " + err.Error()), nil
		}
		return okCapJSON(true), nil

	case "pubsub.subscribe":
		topic := str("topic")
		if topic == "" {
			return errCapJSON("missing topic"), nil
		}

		// Messages are delivered by invoking the module's on_pubsub_message; a flow bundle (mod == nil) has no
		// module to deliver to, and the drain goroutine dereferenced it.
		if h.mod == nil {
			return errCapJSON("pubsub.subscribe is not supported for flow bundles: there is no module to deliver messages to"), nil
		}

		h.mu.Lock()
		if _, already := h.subs[topic]; already {
			h.mu.Unlock()
			return okCapJSON(map[string]string{"status": "already subscribed"}), nil
		}
		h.mu.Unlock()

		t, err := h.joinTopic(topic)
		if err != nil {
			return errCapJSON("failed to join topic: " + err.Error()), nil
		}
		sub, err := t.Subscribe()
		if err != nil {
			return errCapJSON("subscribe failed: " + err.Error()), nil
		}

		h.mu.Lock()
		h.subs[topic] = sub
		h.mu.Unlock()

		// Start a goroutine to deliver messages to the module via InvokeMethod.
		go h.drainSubscription(topic, sub)

		return okCapJSON(map[string]string{"status": "subscribed", "topic": topic}), nil

	case "pubsub.unsubscribe":
		topic := str("topic")
		if topic == "" {
			return errCapJSON("missing topic"), nil
		}

		h.mu.Lock()
		sub, ok := h.subs[topic]
		if ok {
			delete(h.subs, topic)
		}
		h.mu.Unlock()

		if ok {
			sub.Cancel()
		}
		return okCapJSON(true), nil

	case "pubsub.list_topics":
		h.mu.Lock()
		topics := make([]string, 0, len(h.topics))
		for t := range h.topics {
			topics = append(topics, t)
		}
		h.mu.Unlock()
		return okCapJSON(map[string]interface{}{"topics": topics}), nil

	default:
		return errCapJSON(fmt.Sprintf("unknown pubsub operation: %s", operation)), nil
	}
}

func (h *pubsubCapHandler) joinTopic(name string) (*pubsub.Topic, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.topics[name]; ok {
		return t, nil
	}
	if err := sdnpubsub.RegisterTopicStructureValidator(h.ps, name); err != nil {
		return nil, err
	}
	t, err := h.ps.Join(name)
	if err != nil {
		return nil, err
	}
	h.topics[name] = t
	return t, nil
}

// drainSubscription delivers incoming pubsub messages to the module as
// InvokeMethod("on_pubsub_message", jsonPayload) calls. It stops when
// the module's lifecycle context is cancelled or the subscription is closed.
func (h *pubsubCapHandler) drainSubscription(topic string, sub *pubsub.Subscription) {
	ctx := h.mod.Context()
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			// Subscription cancelled or closed — stop the goroutine.
			return
		}
		h.deliverMessage(ctx, topic, msg)
	}
}

func (h *pubsubCapHandler) deliverMessage(ctx context.Context, topic string, msg *pubsub.Message) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("module pubsub delivery from %s on %s dropped: panicked: %v\n%s", msg.ReceivedFrom, topic, recovered, debug.Stack())
		}
	}()
	payload, _ := json.Marshal(map[string]interface{}{
		"topic":  topic,
		"from":   msg.ReceivedFrom.String(),
		"data":   string(msg.Data),
		"seq_no": msg.Seqno,
	})
	// Best-effort: if the module doesn't export on_pubsub_message, ignore.
	h.mod.InvokeMethod(ctx, "on_pubsub_message", payload) //nolint:errcheck
}
