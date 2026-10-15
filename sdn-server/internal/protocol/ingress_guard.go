package protocol

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// PeerAuthorizer reports whether a remote peer holds a privilege. A nil
// authorizer grants it to nobody (fail closed).
type PeerAuthorizer func(peer.ID) bool

// AllowAnyPeer grants a privilege to every peer. Production never installs
// it; it exists for harnesses that talk to their own provider.
func AllowAnyPeer(peer.ID) bool { return true }

// ErrReadNotAuthorized is returned to a peer that asks for data outside the
// anonymous read surface without the privilege to read it.
var ErrReadNotAuthorized = errors.New("not authorized: this schema is not on the public read surface")

// ErrMalformedMessage marks a message whose handling panicked. The message
// is dropped and the node keeps running.
var ErrMalformedMessage = errors.New("malformed message rejected")

// PRIVATE READS (security re-audit NET-02, 2026-10-15).
//
// The HTTP data plane serves a schema anonymously only when sds.IsPublicRead-
// Schema names it. The libp2p read lanes — flatsql-sync and the SDS exchange's
// request/query messages — applied no such rule: any peer that could dial the
// node read any table by name, including storefront purchase requests (buyer
// email, payment intent, webhook token), grants, ledgers and key material.
//
// Every read lane now resolves the requested schema through publicReadSchema
// first. A peer the node's private-read authorizer does not admit gets the
// same allow-list the HTTP plane enforces, and nothing else: no non-public
// schema, no empty schema (which would mean "any"), no other datastore, no
// datastore listing. The authorizer is unset by default, so an unwired lane
// serves the public surface only.

// publicReadSchema returns the canonical name of schema when it is on the
// anonymous read surface. The caller must use the returned name for the read,
// so the name that was checked is the name that is served.
func publicReadSchema(schema string) (string, bool) {
	canonical := sds.NormalizeSchemaFileName(schema)
	if canonical == "" || !sds.IsPublicReadSchema(canonical) {
		return "", false
	}
	return canonical, true
}

// authorizedPeer reports whether fn admits id; nil admits nobody.
func authorizedPeer(fn PeerAuthorizer, id peer.ID) bool {
	return fn != nil && id != "" && fn(id)
}

// streamPeer returns the remote peer of s, or "" when s has no connection
// (never authorized).
func streamPeer(s network.Stream) (id peer.ID) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	if s == nil || s.Conn() == nil {
		return ""
	}
	return s.Conn().RemotePeer()
}

// recoverStream is deferred first in every stream handler: a panic while
// handling one peer's bytes resets that stream and is logged, and the node
// keeps running. libp2p runs stream handlers on bare goroutines, so without
// this one malformed message is a process crash (NET-01).
func recoverStream(protocolName string, s network.Stream) {
	r := recover()
	if r == nil {
		return
	}
	log.Errorf("%s: stream from %s dropped: handler panicked on malformed input: %v\n%s",
		protocolName, streamPeer(s).ShortString(), r, debug.Stack())
	if s != nil {
		_ = s.Reset()
	}
}

func panicError(r any) error {
	return fmt.Errorf("%w: %v", ErrMalformedMessage, r)
}

func requestedSchema(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
