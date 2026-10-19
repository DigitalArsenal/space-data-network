package pinpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	gocid "github.com/ipfs/go-cid"
)

// Scope is the policy for one class and feed: every add and pin of content in
// it goes through here.
type Scope struct {
	p     *Policy
	class Class
	feed  string
}

// Scope binds a class and feed.
func (p *Policy) Scope(class Class, feed string) Scope {
	return Scope{p: p, class: class, feed: strings.TrimSpace(feed)}
}

// Configured reports a policy with a Kubo RPC API behind it.
func (s Scope) Configured() bool { return s.p != nil && s.p.kubo != nil }

// Allowed reports whether the policy pins this scope's content.
func (s Scope) Allowed() bool { return s.p.Allowed(s.class, s.feed) }

// PutBlock stores data as a CIDv1 raw block, pinned when the policy allows.
func (s Scope) PutBlock(ctx context.Context, data []byte) (string, error) {
	if !s.Configured() {
		return "", ErrNoKubo
	}
	if len(data) == 0 {
		return "", errors.New("block bytes are required")
	}
	pin := s.Allowed()
	cid, err := s.p.kubo.blockPut(ctx, data, pin)
	if err != nil {
		return "", err
	}
	s.record(cid, int64(len(data)), pin)
	return cid, nil
}

// AddFile adds a local file as UnixFS, pinned when the policy allows.
func (s Scope) AddFile(ctx context.Context, path string, opts AddOptions) (Added, error) {
	if !s.Configured() {
		return Added{}, ErrNoKubo
	}
	pin := s.Allowed() && !opts.NoPin
	added, err := s.p.kubo.addFile(ctx, path, opts, pin)
	if err != nil {
		return Added{}, err
	}
	if !opts.OnlyHash {
		s.record(added.CID, added.Size, added.Pinned)
	}
	return added, nil
}

// AddBytes adds data as one UnixFS file, pinned when the policy allows.
func (s Scope) AddBytes(ctx context.Context, name string, data []byte, opts AddOptions) (Added, error) {
	if !s.Configured() {
		return Added{}, ErrNoKubo
	}
	if strings.TrimSpace(name) == "" {
		name = "data"
	}
	pin := s.Allowed() && !opts.NoPin
	added, err := s.p.kubo.add(ctx, name, bytes.NewReader(data), opts, pin)
	if err != nil {
		return Added{}, err
	}
	if !opts.OnlyHash {
		s.record(added.CID, added.Size, added.Pinned)
	}
	return added, nil
}

// PinCID pins a CID by reference (fetching what the node does not hold) when
// the policy allows, and ledgers it either way so a later enable pins it.
// It reports whether it pinned.
func (s Scope) PinCID(ctx context.Context, cid string, byteCount int64) (bool, error) {
	cid, err := canonicalCID(cid)
	if err != nil {
		return false, err
	}
	if !s.Configured() {
		return false, ErrNoKubo
	}
	pin := s.Allowed()
	if pin {
		if err := s.p.kubo.pinAdd(ctx, cid, false); err != nil {
			return false, err
		}
	}
	s.record(cid, byteCount, pin)
	return pin, nil
}

// Unpin drops this scope's hold on cid and removes the Kubo pin unless
// another allowed rule still holds it.
func (s Scope) Unpin(ctx context.Context, cid string) error {
	cid, err := canonicalCID(cid)
	if err != nil {
		return err
	}
	if !s.Configured() {
		return ErrNoKubo
	}
	row := Pin{Class: s.class, Feed: s.feed, CID: cid}
	if !s.p.heldElsewhere(row) {
		if err := s.p.kubo.pinRm(ctx, cid); err != nil {
			return err
		}
	}
	if s.p.ledger != nil {
		if err := s.p.ledger.DeleteKuboPin(s.class, s.feed, cid); err != nil {
			log.Warnf("pin policy: unpinned %s but could not ledger it: %v", cid, err)
		}
	}
	return nil
}

// Held reports whether this scope holds cid: added under it and, when the
// policy pins it, pinned in Kubo.
func (s Scope) Held(ctx context.Context, cid string) (bool, error) {
	cid = strings.TrimSpace(cid)
	if !s.Configured() {
		return false, ErrNoKubo
	}
	if s.p.ledger == nil {
		return s.p.kubo.pinned(ctx, cid)
	}
	rows, err := s.p.ledger.KuboPinsByCID(cid)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Class != s.class || row.Feed != s.feed {
			continue
		}
		if !row.Pinned {
			return true, nil
		}
		return s.p.kubo.pinned(ctx, cid)
	}
	return false, nil
}

// canonicalCID parses a CID argument; the answer is the CID as written.
func canonicalCID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("cid is required")
	}
	if _, err := gocid.Decode(value); err != nil {
		return "", fmt.Errorf("invalid cid: %w", err)
	}
	return value, nil
}

func (s Scope) record(cid string, byteCount int64, pinned bool) {
	if s.p.ledger == nil {
		return
	}
	if err := s.p.ledger.UpsertKuboPin(Pin{
		Class: s.class, Feed: s.feed, CID: cid, Bytes: byteCount, Pinned: pinned, At: time.Now().UTC(),
	}); err != nil {
		log.Warnf("pin policy: could not ledger %s %s %s: %v", s.class, s.feed, cid, err)
	}
}

// PinLs answers Kubo's pin/ls for a pin type ("" = all), as Kubo answers it.
func (p *Policy) PinLs(ctx context.Context, pinType string) ([]byte, error) {
	if p == nil || p.kubo == nil {
		return nil, ErrNoKubo
	}
	query := url.Values{}
	if pinType = strings.TrimSpace(pinType); pinType != "" {
		query.Set("type", pinType)
	}
	return p.kubo.post(ctx, "/api/v0/pin/ls", query, nil, "")
}

// PinnedByFeed counts, per feed of class, the CIDs the policy holds pinned
// under exactly that feed.
func (p *Policy) PinnedByFeed(class Class) map[string]uint64 {
	out := map[string]uint64{}
	if p == nil || p.ledger == nil {
		return out
	}
	rows, err := p.ledger.KuboPins(class)
	if err != nil {
		log.Warnf("pin policy: ledger of %s unavailable: %v", class, err)
		return out
	}
	for _, row := range rows {
		if row.Pinned {
			out[row.Feed]++
		}
	}
	return out
}
