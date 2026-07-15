package sdnservices

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/channels"
	"github.com/ipfs/kubo/sdn/modulert"
)

const pubsubPublishTimeout = 5 * time.Second

// NewPubSubCapFactory returns a BridgeCapFactory serving the "pubsub"
// capability against the kubo-native channels fan-out — the Phase-6
// reconnection of the deferred sdn-server pubsub capability
// (sdn-server/internal/modulert/caps/pubsub.go) to the new services.
//
// The sdn-server handler operated on raw gossipsub topic strings. This one
// operates on the SDN (source, standard) channel grammar (kubo/sdn/channels),
// so a module publishes/subscribes a per-(provider, standard) channel — the
// same topics the store fans records out on. This keeps module traffic on the
// exact channels sdnstore already publishes to, rather than arbitrary topics.
//
// "pubsub" is an operator-gated sensitive capability (modulert
// sensitiveCapabilities), so the bridge is only granted it after the
// capability-policy gate approves the module's content hash (fail closed). Each
// operation additionally re-checks the grant against the calling bridge.
//
// Supported operations:
//
//	pubsub.publish     — {"source":"...","standard":"OMM","data":"<base64>"}   -> {"ok":true}
//	pubsub.subscribe   — {"standard":"OMM","sources":["a","b"]}  subscribes; messages delivered to
//	                     the module via InvokeMethod("on_channel_message", {...}) -> {"status":"subscribed"}
//	pubsub.unsubscribe — {"standard":"OMM"}                                     -> {"ok":true}
//	pubsub.list        — {}                                                     -> {"standards":["OMM",...]}
func NewPubSubCapFactory(ch *channels.Channels) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		h := &pubsubCapAdapter{
			ch:     ch,
			mod:    mod,
			bridge: bridge,
			subs:   make(map[string]*channels.Subscription),
		}
		return h.handle
	}
}

type pubsubCapAdapter struct {
	ch     *channels.Channels
	mod    *modulert.Module
	bridge *modulert.HostBridge

	mu   sync.Mutex
	subs map[string]*channels.Subscription // standard code -> subscription
}

func (h *pubsubCapAdapter) has(cap string) bool {
	return h.bridge != nil && h.bridge.HasCapability(cap)
}

func (h *pubsubCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	if !h.has("pubsub") {
		return errCapJSON("pubsub operations require the pubsub capability grant"), nil
	}

	var p map[string]interface{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &p)
	}
	str := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			return strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		return ""
	}

	switch operation {
	case "pubsub.publish":
		source := str("source")
		standard := str("standard")
		if source == "" || standard == "" {
			return errCapJSON("pubsub.publish requires source and standard"), nil
		}
		raw := decodeBase64Cap(str("data"))
		if len(raw) == 0 {
			return errCapJSON("data missing or not valid base64 bytes"), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), pubsubPublishTimeout)
		defer cancel()
		if err := h.ch.Publish(ctx, source, standard, raw); err != nil {
			return errCapJSON("publish failed: " + err.Error()), nil
		}
		return okCapJSON(true), nil

	case "pubsub.subscribe":
		standard := str("standard")
		if standard == "" {
			return errCapJSON("pubsub.subscribe requires standard"), nil
		}
		sources := toStringSlice(p["sources"])
		if s := str("source"); s != "" {
			sources = append(sources, s)
		}
		if len(sources) == 0 {
			return errCapJSON("pubsub.subscribe requires at least one source (gossipsub has no wildcard subscription)"), nil
		}
		h.mu.Lock()
		if _, already := h.subs[strings.ToUpper(standard)]; already {
			h.mu.Unlock()
			return okCapJSON(map[string]string{"status": "already subscribed", "standard": strings.ToUpper(standard)}), nil
		}
		h.mu.Unlock()

		sub, err := h.ch.Subscribe(standard, sources...)
		if err != nil {
			return errCapJSON("subscribe failed: " + err.Error()), nil
		}
		h.mu.Lock()
		h.subs[strings.ToUpper(standard)] = sub
		h.mu.Unlock()

		if h.mod != nil {
			go h.drain(sub)
		}
		return okCapJSON(map[string]interface{}{"status": "subscribed", "standard": sub.Standard, "sources": sources}), nil

	case "pubsub.unsubscribe":
		standard := strings.ToUpper(str("standard"))
		if standard == "" {
			return errCapJSON("pubsub.unsubscribe requires standard"), nil
		}
		h.mu.Lock()
		sub, ok := h.subs[standard]
		if ok {
			delete(h.subs, standard)
		}
		h.mu.Unlock()
		if ok {
			sub.Cancel()
		}
		return okCapJSON(true), nil

	case "pubsub.list":
		h.mu.Lock()
		standards := make([]string, 0, len(h.subs))
		for std := range h.subs {
			standards = append(standards, std)
		}
		h.mu.Unlock()
		return okCapJSON(map[string]interface{}{"standards": standards}), nil

	default:
		return errCapJSON(fmt.Sprintf("unknown pubsub operation: %s", operation)), nil
	}
}

// drain delivers incoming channel messages to the module as
// InvokeMethod("on_channel_message", {...}) calls until the module's lifecycle
// context is cancelled or the subscription is closed. Best-effort: a module
// that does not export on_channel_message simply ignores the delivery.
func (h *pubsubCapAdapter) drain(sub *channels.Subscription) {
	ctx := h.mod.Context()
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"standard": sub.Standard,
			"from":     msg.ReceivedFrom.String(),
			"data":     encodeBase64Cap(msg.Data),
		})
		_, _ = h.mod.InvokeMethod(ctx, "on_channel_message", payload)
	}
}

func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s := strings.TrimSpace(fmt.Sprintf("%v", e)); s != "" {
			out = append(out, s)
		}
	}
	return out
}
