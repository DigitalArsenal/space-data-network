package storage

import (
	"errors"
	"io"
	"strings"
)

// A publication's shard and index are files under the node's publication
// folder, and the same bytes are IPFS content (the shard a UnixFS file, the
// index a raw block) addressed by the publication's SHARD_CID and INDEX_CID.
// A follower's lane retention removes the files of the batches it no longer
// keeps as current, while its IPFS repository may still hold their content
// (the node does not collect garbage, and an operator may pin it). Bitswap
// readers fetch the content, not the file, so a publication whose content the
// repository holds in full is servable: it lists as available, and read-shard
// and read-index serve it from the repository (host-01, 2026-11-21: it listed
// four of host-02's five GP batches as "shard_missing" while it pinned and
// served their content, and readers that drop unavailable rows saw one batch).

// PublicationContent reads publication content from the node's IPFS
// repository, without any network fetch.
type PublicationContent interface {
	// Held reports whether the repository holds the whole content of cid
	// (every block of its DAG), and its byte size.
	Held(cid string) (size int64, ok bool)
	// Open reads the content of cid; Held must report it held.
	Open(cid string) (io.ReadSeekCloser, int64, error)
}

type publicationContentHolder struct{ content PublicationContent }

// SetPublicationContent installs the node's IPFS repository as the fallback
// for publication files that are not on disk (nil removes it).
func (s *FlatSQLStore) SetPublicationContent(content PublicationContent) {
	if s == nil {
		return
	}
	if content == nil {
		s.publicationContent.Store(nil)
		return
	}
	s.publicationContent.Store(&publicationContentHolder{content: content})
}

// PublicationContentHeld reports whether the repository holds the content of
// cid in full, with its size; false without a repository.
func (s *FlatSQLStore) PublicationContentHeld(cid string) (int64, bool) {
	if s == nil || strings.TrimSpace(cid) == "" {
		return 0, false
	}
	holder := s.publicationContent.Load()
	if holder == nil || holder.content == nil {
		return 0, false
	}
	return holder.content.Held(strings.TrimSpace(cid))
}

// OpenPublicationContent reads the content of cid from the repository.
func (s *FlatSQLStore) OpenPublicationContent(cid string) (io.ReadSeekCloser, int64, error) {
	if s == nil {
		return nil, 0, errNoPublicationContent
	}
	holder := s.publicationContent.Load()
	if holder == nil || holder.content == nil {
		return nil, 0, errNoPublicationContent
	}
	return holder.content.Open(strings.TrimSpace(cid))
}

var errNoPublicationContent = errors.New("no IPFS repository is installed for publication content")
