package pinpolicy

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"
)

// Document is a file every node keeps pinned whatever its rules say: the SDN
// README (internal/nodereadme). It is not a class: no rule turns it off, and
// the node pins it again when anything removes the pin.
type Document struct {
	Name  string
	Bytes []byte
}

// KeptDocument is a Document as this node holds it.
type KeptDocument struct {
	Name string
	// CID is the document's content address. Every node adds it with the
	// same options, so the same bytes give the same CID on every node.
	CID  string
	Size int64
	// Pinned reports the pin as last checked, at CheckedAt.
	Pinned    bool
	CheckedAt time.Time
}

// documentAddOptions fix how a kept document is added, so its CID depends on
// its bytes alone: CIDv1, raw leaves, sha2-256, 256 KiB chunks.
var documentAddOptions = AddOptions{CIDv1: true, Chunker: "size-262144"}

const (
	documentRetry    = 5 * time.Second
	documentInterval = time.Minute
	documentCheck    = 30 * time.Second
)

type keptDocuments struct {
	// check serializes checks; mu guards the fields below and is never held
	// across a Kubo call, so a slow Kubo cannot hold up a reader.
	check sync.Mutex
	mu    sync.Mutex
	docs  []Document
	kept  []KeptDocument
}

// KeepDocuments adds and pins docs at once, then checks their pins every
// minute and pins again any that was removed, until ctx ends. Until every
// document is pinned (Kubo still starting) it tries every few seconds. A
// node without Kubo keeps nothing.
func (p *Policy) KeepDocuments(ctx context.Context, docs []Document) {
	if p == nil || p.kubo == nil || len(docs) == 0 {
		return
	}
	p.documents.mu.Lock()
	p.documents.docs = append([]Document(nil), docs...)
	p.documents.kept = make([]KeptDocument, len(docs))
	for i, doc := range docs {
		p.documents.kept[i] = KeptDocument{Name: doc.Name, Size: int64(len(doc.Bytes))}
	}
	p.documents.mu.Unlock()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, documentCheck)
		wait := documentInterval
		if !p.CheckDocuments(checkCtx) {
			wait = documentRetry
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// CheckDocuments checks every kept document's pin and pins again what is not
// pinned. It reports whether every document is pinned. A check already
// running is not waited for: the answer is the last one.
func (p *Policy) CheckDocuments(ctx context.Context) bool {
	if p == nil || p.kubo == nil {
		return false
	}
	if !p.documents.check.TryLock() {
		return p.documentsPinned()
	}
	defer p.documents.check.Unlock()
	p.documents.mu.Lock()
	docs := append([]Document(nil), p.documents.docs...)
	kept := append([]KeptDocument(nil), p.documents.kept...)
	p.documents.mu.Unlock()

	all := true
	for i, doc := range docs {
		row := &kept[i]
		if row.CID != "" {
			if pinned, err := p.kubo.pinned(ctx, row.CID); err == nil && pinned {
				row.Pinned, row.CheckedAt = true, time.Now().UTC()
				continue
			}
		}
		// Adding with pin=true stores the blocks again if they are gone and
		// pins the root; the same bytes give the same CID.
		added, err := p.kubo.add(ctx, doc.Name, bytes.NewReader(doc.Bytes), documentAddOptions, true)
		if err != nil {
			log.Warnf("pin policy: could not pin %s: %v", doc.Name, err)
			row.Pinned = false
			all = false
			continue
		}
		cid := strings.TrimSpace(added.CID)
		switch {
		case row.CID == "":
			log.Infof("pin policy: pinned %s as %s", doc.Name, cid)
		case row.CID != cid:
			log.Warnf("pin policy: %s changed address from %s to %s", doc.Name, row.CID, cid)
		default:
			log.Infof("pin policy: %s (%s) was not pinned; pinned it again", doc.Name, cid)
		}
		row.CID, row.Pinned, row.CheckedAt = cid, true, time.Now().UTC()
	}

	p.documents.mu.Lock()
	if len(p.documents.kept) == len(kept) {
		p.documents.kept = kept
	}
	p.documents.mu.Unlock()
	return all
}

func (p *Policy) documentsPinned() bool {
	p.documents.mu.Lock()
	defer p.documents.mu.Unlock()
	for _, row := range p.documents.kept {
		if !row.Pinned {
			return false
		}
	}
	return len(p.documents.kept) > 0
}

// Documents answers the kept documents in the order KeepDocuments was given
// them, as last checked. A document not yet added has no CID.
func (p *Policy) Documents() []KeptDocument {
	if p == nil {
		return nil
	}
	p.documents.mu.Lock()
	defer p.documents.mu.Unlock()
	return append([]KeptDocument(nil), p.documents.kept...)
}
