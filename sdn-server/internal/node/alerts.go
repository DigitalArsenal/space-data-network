package node

import (
	"errors"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

// Dataset-publication alerting.
//
// On 2026-09-13..15 host-01 refused 2,122 dataset publications in 31 hours —
// every one of them a SIGNATURE_TYPE that did not match the provider's key —
// and the only trace was a warning per message in a log nobody was reading.
// A publication rejection is now an alert keyed by the PRODUCER, because that
// is the axis an operator acts on: one peer's publications are broken, or the
// whole lane is.

// raisePublicationRejected records that a dataset publication from a producer
// would not materialize. Repeats bump the count on that producer's existing
// alert; clearPublicationRejected is called when one of its publications
// lands. producer is the FULL peer id — every entry point passes the full id
// and the short form is derived here, so a raise and its clear always agree
// on the subject.
func (n *Node) raisePublicationRejected(producer string, err error) {
	if n == nil || err == nil {
		return
	}
	n.alerts.Raise(ops.KindPublicationRejected, shortPeerID(producer), ops.SeverityError, err.Error())
}

// raisePubSubPublicationRejected is the pubsub-loop variant. That loop reports
// EVERY failed message on a schema topic, most of which are ordinary record
// failures, so only a wrapped ErrPNMAnnouncement — a dataset publication this
// node refused — becomes an alert.
func (n *Node) raisePubSubPublicationRejected(producer string, err error) {
	if !errors.Is(err, protocol.ErrPNMAnnouncement) {
		return
	}
	n.raisePublicationRejected(producer, err)
}

// clearPublicationRejected records that a publication from a producer landed.
func (n *Node) clearPublicationRejected(producer string) {
	if n == nil {
		return
	}
	n.alerts.Clear(ops.KindPublicationRejected, shortPeerID(producer))
}

// shortPeerID is the identifier peer.ID.ShortString prints, without its
// "<peer.ID …>" wrapper: an alert subject an operator can paste straight into
// a grep over the logs that already name the peer that way.
func shortPeerID(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if len(peerID) <= 10 {
		return peerID
	}
	return peerID[:2] + "*" + peerID[len(peerID)-6:]
}
