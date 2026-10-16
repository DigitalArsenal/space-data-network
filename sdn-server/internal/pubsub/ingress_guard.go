package pubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"weak"

	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var structureValidators = struct {
	sync.Mutex
	instances map[weak.Pointer[ps.PubSub]]map[string]bool
}{instances: make(map[weak.Pointer[ps.PubSub]]map[string]bool)}

// topicSchemaIndex maps a buffer's file identifier to its standard, the way
// the topic's consumer routes it (sds.Validator.RouteBuffer).
var topicSchemaIndex = sync.OnceValues(func() (*sds.Validator, error) {
	return sds.NewValidator(nil)
})

// RegisterTopicStructureValidator installs, once per PubSub instance and
// topic, a gossip validator that refuses to relay or deliver a malformed SDS
// buffer (security re-audit NET-01). It checks structure only, never policy:
// a buffer routes by its own file identifier to its standard, as the
// consumer routes it, and passes when it verifies as that standard (a PNM
// also in the size-prefixed form its consumers read). On the storefront
// listing topic a JSON listing passes on syntax; its consumer authenticates
// it. Topics outside these namespaces carry opaque payloads and get none.
func RegisterTopicStructureValidator(p *ps.PubSub, topic string) error {
	if !strings.HasPrefix(topic, TopicPrefix) && topic != StorefrontListingsTopic {
		return nil
	}
	if p == nil {
		return fmt.Errorf("topic %s: pubsub is unavailable", topic)
	}
	index, err := topicSchemaIndex()
	if err != nil {
		return err
	}
	structureValidators.Lock()
	defer structureValidators.Unlock()
	for instance := range structureValidators.instances {
		if instance.Value() == nil {
			delete(structureValidators.instances, instance)
		}
	}
	instance := weak.Make(p)
	topics := structureValidators.instances[instance]
	if topics[topic] {
		return nil
	}
	jsonListings := topic == StorefrontListingsTopic
	err = p.RegisterTopicValidator(topic, func(_ context.Context, from peer.ID, msg *ps.Message) (result ps.ValidationResult) {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("gossip validator on %s panicked on a message from %s: %v", topic, from, r)
				result = ps.ValidationReject
			}
		}()
		if msg == nil {
			return ps.ValidationReject
		}
		if structurallyValidMessage(index, msg.Data, jsonListings) {
			return ps.ValidationAccept
		}
		// Not logged per message: a peer must not be able to flood the log.
		return ps.ValidationReject
	})
	if err != nil {
		return fmt.Errorf("topic %s: register structure validator: %w", topic, err)
	}
	if topics == nil {
		topics = make(map[string]bool)
		structureValidators.instances[instance] = topics
	}
	topics[topic] = true
	return nil
}

func structurallyValidMessage(index *sds.Validator, data []byte, jsonListings bool) bool {
	if jsonListings {
		if payload := bytes.TrimSpace(data); len(payload) > 0 && payload[0] == '{' {
			return json.Valid(payload)
		}
	}
	identifier, ok := sds.FileIdentifierFromBuffer(data)
	if !ok {
		return false
	}
	schema, known := index.SchemaForIdentifier(identifier)
	if !known || sds.VerifyStructure(schema, data) != nil {
		return false
	}
	return schema != "PNM.fbs" || sds.VerifySizePrefixedStructure(schema, data) == nil
}

func recoverTipRequest(lane, from string) {
	if r := recover(); r != nil {
		log.Errorf("%s from %s refused: handler panicked: %v\n%s", lane, from, r, debug.Stack())
	}
}
