package epm

import (
	"fmt"
	"runtime/debug"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// VerifyEPMBuffer proves an EPM received from another peer is memory-safe to
// read before any generated accessor touches it (security re-audit NET-01b,
// 2026-10-15). Every EPM consumer in the node reads GetSizePrefixedRootAsEPM,
// so that is the form verified: every table, string and vector offset of the
// profile, its ADDRESS, KEYS, CHAIN_PROOFS and DOMAIN_PROOFS must land inside
// the buffer. Without it, a peer that advertises the epm-exchange protocol
// could answer with a 12-byte EPM whose field offset points past its end and
// crash the node inside VerifyEPMSignature.
func VerifyEPMBuffer(epmData []byte) error {
	if len(epmData) == 0 {
		return fmt.Errorf("empty EPM")
	}
	if err := sds.VerifySizePrefixedStructure("EPM.fbs", epmData); err != nil {
		return fmt.Errorf("malformed EPM: %w", err)
	}
	return nil
}

// verifyPNMBuffer is VerifyEPMBuffer's counterpart for the PNM announcements
// that carry EPMs over pubsub.
func verifyPNMBuffer(pnmData []byte) error {
	if err := sds.VerifySizePrefixedStructure("PNM.fbs", pnmData); err != nil {
		return fmt.Errorf("malformed PNM: %w", err)
	}
	return nil
}

// recoverIngress converts a panic while handling a peer's bytes into a
// logged drop. It is the backstop behind VerifyEPMBuffer: libp2p runs stream
// handlers and the node runs gossip loops on bare goroutines, where an
// unrecovered panic ends the process.
func recoverIngress(what, from string) {
	if r := recover(); r != nil {
		log.Errorf("%s from %s dropped: handler panicked on malformed input: %v\n%s", what, from, r, debug.Stack())
	}
}
