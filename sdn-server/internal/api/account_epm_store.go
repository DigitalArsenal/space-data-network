package api

// The record + pin lane behind /api/auth/epm — the concrete side of
// internal/auth's AccountEPMStore port (owner directive 2026-08-28).
//
// # What "pin" concretely means here
//
// Two things, and the first one is not optional:
//
//  1. THE STORE RETAINS IT. The record is written through the ordinary
//     engine-routed EPM.fbs lane with account source tags, so it is a real
//     queryable record, and a row is written to the pin ledger
//     (sdn_pin_ledger) under role "account-epm". The ledger is what tells every
//     retention/GC sweep that these bytes are held on purpose. On a node with
//     no Kubo attached, THIS IS THE WHOLE PIN, and it is sufficient: the record
//     is durable, served, and never collectable.
//
//  2. KUBO HOLDS THE BLOCK, when a blockstore is attached. The record bytes are
//     put as a pinned CIDv1 raw block (sha2-256) — the same codec/hash the
//     store's own CID uses, so the block CID and the record CID are the same
//     string, and the epmcid an account advertises resolves for an anonymous
//     off-box fetcher. This half FAILS OPEN: a node whose Kubo is down or
//     unconfigured still stores and ledgers the record, and logs the gap.
//
// Store failure fails CLOSED — an identity the node cannot retain is never
// reported as stored.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	// accountEPMProviderID tags the lane so account EPMs are queryable as one.
	accountEPMProviderID = "account"
	// accountEPMPinRole distinguishes these ledger rows from dataset pins.
	accountEPMPinRole = "account-epm"
	// accountEPMSchema is the SDS standard an account identity is stored as.
	accountEPMSchema = "EPM.fbs"
)

// AccountEPMBlockstore is the optional Kubo half of the pin: bytes in, the CID
// Kubo reports back out, pinned. main.go binds it; an unbound blockstore is a
// valid node.
type AccountEPMBlockstore interface {
	PutPinnedRawBlock(ctx context.Context, data []byte) (string, error)
}

// AccountEPMStore stores and pins the $EPM records of accounts tied to this node.
type AccountEPMStore struct {
	store      *storage.FlatSQLStore
	nodePeerID string
	blockstore AccountEPMBlockstore
}

// NewAccountEPMStore binds the lane. nodePeerID is this node's peer ID: it is
// the ProducerPeerID every account EPM is attributed to, which is what makes
// "accounts tied to THIS node" a queryable fact rather than a convention.
func NewAccountEPMStore(store *storage.FlatSQLStore, nodePeerID string, blockstore AccountEPMBlockstore) *AccountEPMStore {
	if store == nil {
		return nil
	}
	return &AccountEPMStore{
		store:      store,
		nodePeerID: strings.TrimSpace(nodePeerID),
		blockstore: blockstore,
	}
}

// StoreAccountEPM stores the signed record, ledgers its pin, and (when a
// blockstore is attached) pins the block in Kubo. Returns the record CID.
func (s *AccountEPMStore) StoreAccountEPM(ctx context.Context, sourceName string, epmBytes []byte) (string, error) {
	if s == nil || s.store == nil {
		return "", fmt.Errorf("account EPM store is not bound")
	}
	if len(epmBytes) == 0 {
		return "", fmt.Errorf("account EPM record bytes are required")
	}
	sourceName = strings.TrimSpace(sourceName)
	if sourceName == "" {
		return "", fmt.Errorf("account EPM source name is required")
	}

	tags := storage.SourceTags{
		ProviderID:     accountEPMProviderID,
		SourceName:     sourceName,
		ProducerPeerID: s.nodePeerID,
	}
	cid, err := s.store.StoreWithSourceTags(accountEPMSchema, epmBytes, s.nodePeerID, nil, tags)
	if err != nil {
		return "", fmt.Errorf("store account EPM record: %w", err)
	}

	// Fail CLOSED on the ledger: a record with no pin entry is GC-eligible, and
	// reporting it as pinned would be the exact lie the fleet law exists to
	// prevent.
	if err := s.store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               cid,
		SchemaName:        accountEPMSchema,
		ProviderPeerID:    s.nodePeerID,
		ProviderID:        accountEPMProviderID,
		SourceName:        sourceName,
		Role:              accountEPMPinRole,
		RowCount:          1,
		ByteCount:         int64(len(epmBytes)),
		VerificationState: "verified",
		VerifiedAt:        time.Now().UTC(),
	}); err != nil {
		return "", fmt.Errorf("ledger account EPM pin: %w", err)
	}

	// Fail OPEN on Kubo: the record is already durable without it.
	if s.blockstore != nil {
		blockCID, err := s.blockstore.PutPinnedRawBlock(ctx, epmBytes)
		if err != nil {
			log.Warnf("Could not pin account EPM %s in the local blockstore: %v", cid, err)
		} else if strings.TrimSpace(blockCID) != cid {
			log.Warnf("Account EPM %s pinned in the local blockstore under a different CID (%s)", cid, blockCID)
		}
	}
	return cid, nil
}

// AccountEPMPinned reports whether the record is BOTH still in the store and
// still covered by a pin-ledger entry. Either half missing is "not pinned": the
// reconciler's job is to restore both.
func (s *AccountEPMStore) AccountEPMPinned(ctx context.Context, cid string) (bool, error) {
	if s == nil || s.store == nil {
		return false, fmt.Errorf("account EPM store is not bound")
	}
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return false, nil
	}

	records, err := s.store.QueryRawRecordRefs(storage.RawRecordQuery{
		SchemaName: accountEPMSchema,
		CID:        cid,
		Limit:      1,
	})
	if err != nil {
		return false, fmt.Errorf("probe account EPM record: %w", err)
	}
	if len(records) == 0 {
		return false, nil
	}

	return s.store.HasPinLedgerEntry(storage.PinLedgerQuery{
		CID:        cid,
		SchemaName: accountEPMSchema,
		Role:       accountEPMPinRole,
	})
}
