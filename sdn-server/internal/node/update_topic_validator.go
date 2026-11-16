package node

import (
	"context"
 "errors"
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
	return n.pubsub.RegisterTopicValidator(topic, func(_ context.Context, from peer.ID, message *ps.Message) ps.ValidationResult {
		if n.host != nil && from == n.host.ID() { return ps.ValidationAccept }
		if message == nil {
			return ps.ValidationReject
		}
		signal, err := update.ParseSignal(message.Data)
		if err != nil {
			return ps.ValidationReject
		}
		if err := signal.Verify(update.SignalVerifyOptions{TrustedRoots: roots, Channel: strings.TrimPrefix(topic, prefix)}); err != nil {
 var clockError *update.SignalTimeError
 if errors.As(err, &clockError) { return ps.ValidationIgnore }
			return ps.ValidationReject
		}
		return ps.ValidationAccept
	})
}
