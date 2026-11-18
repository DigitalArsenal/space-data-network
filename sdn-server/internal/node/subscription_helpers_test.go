package node

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// subscribeForTest records a subscription to the lane from producer on a
// node built by hand, without starting its followed stream: the import gate
// (subscribedFrom) admits the lane from that peer.
func subscribeForTest(t *testing.T, n *Node, schema, providerID, sourceName string, producer peer.ID) {
	t.Helper()
	if n.follows == nil {
		n.follows = newFollowManager(n, t.TempDir())
	}
	key := normalizeFollowKey(FollowKey{Schema: schema, ProviderID: providerID, SourceName: sourceName, Producer: producer.String()})
	n.follows.mu.Lock()
	n.follows.subs[key] = &followEntry{status: FollowStatus{FollowKey: key}, cancel: func() {}, done: closedChan()}
	n.follows.mu.Unlock()
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
