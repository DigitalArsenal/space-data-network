package node

import (
	"fmt"
	"runtime/debug"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// recoverPeerIngress belongs to one message/request, never the enclosing
// subscription or catch-up loop: a panic drops that input and later work
// goes on. A recovered panic means peer bytes reached code that should have
// refused them first, so it is logged as an error with its stack.
func recoverPeerIngress(lane string, from peer.ID, _ bool, err *error) {
	if recovered := recover(); recovered != nil {
		failure := fmt.Errorf("%s: malformed input from %s rejected: %v", lane, from, recovered)
		if err != nil {
			*err = failure
		}
		log.Errorf("%v\n%s", failure, debug.Stack())
	}
}

func runPeerIngress(lane string, from peer.ID, action func()) {
	defer recoverPeerIngress(lane, from, false, nil)
	action()
}

func recoverPeerReply(lane string, stream network.Stream, err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("malformed input: %v", r)
	}
	if *err != nil {
		_ = stream.Reset()
		log.Warnf("%s: reply from %s rejected: %.1024q", lane, stream.Conn().RemotePeer(), (*err).Error())
	}
}

// runUpdateGossip handles one update-topic message: a panic drops that
// message and the read loop goes on.
func runUpdateGossip(lane string, handle func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("%s: message dropped: handler panicked: %v\n%s", lane, recovered, debug.Stack())
		}
	}()
	handle()
}
