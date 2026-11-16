package node

import (
	"context"
	"strings"

	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

func (n *Node) registerUpdateTopicValidator(topic string) error {
	const prefix = "/sdn/updates/v1/"
	if !strings.HasPrefix(topic, prefix) {
		return nil
	}
	roots, err := update.LoadTrustRoots(update.PathsFor(bundle.ResolveCurrent().Root))
	if err != nil {
		return err
	}
	return n.pubsub.RegisterTopicValidator(topic, func(_ context.Context, _ peer.ID, message *ps.Message) ps.ValidationResult {
		if message == nil {
			return ps.ValidationReject
		}
		signal, err := update.ParseSignal(message.Data)
		if err != nil {
			return ps.ValidationReject
		}
		if signal.Verify(update.SignalVerifyOptions{TrustedRoots: roots, Channel: strings.TrimPrefix(topic, prefix)}) != nil {
			return ps.ValidationReject
		}
		return ps.ValidationAccept
	})
}
