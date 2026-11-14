package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

type publicationGroupKey struct {
	schema, peer string
	tags         SourceTags
	signature    [32]byte
	opt          f4PutOpt
}
type publicationPutResult struct {
	cids     []string
	outcomes []format4.Outcome
	err      error
}
type publicationPutRequest struct {
	records [][]byte
	done    chan publicationPutResult
}
type publicationWriteGroup struct {
	requests []*publicationPutRequest
	records  int
}

// Concurrent calls for the same batch join while its leader waits for the write
// lock. The leader acknowledges every caller only after their shared intent and
// engine outcomes are durable. Sequential calls retain individual acknowledgements.
func (b format4Backend) groupPublicationPut(op, schema string, records [][]byte, peer string, sig []byte, tags SourceTags, opt f4PutOpt) ([]string, []format4.Outcome, error) {
	s := b.s
	// Validate independently so malformed input cannot reject another caller.
	for _, data := range records {
		if err := s.validator.Validate(context.Background(), schema, data); err != nil {
			return nil, nil, err
		}
	}
	key := publicationGroupKey{schema: schema, peer: peer, tags: tags, signature: sha256.Sum256(sig), opt: opt}
	request := &publicationPutRequest{records: records, done: make(chan publicationPutResult, 1)}
	s.publicationGroupMu.Lock()
	if s.publicationGroups == nil {
		s.publicationGroups = map[publicationGroupKey]*publicationWriteGroup{}
	}
	group := s.publicationGroups[key]
	leader := group == nil || group.records+len(records) > format4PutChunk
	if leader {
		group = &publicationWriteGroup{}
		s.publicationGroups[key] = group
	}
	group.requests = append(group.requests, request)
	group.records += len(records)
	s.publicationGroupMu.Unlock()
	if leader {
		s.publicationWriteMu.Lock()
		s.publicationGroupMu.Lock()
		if s.publicationGroups[key] == group {
			delete(s.publicationGroups, key)
		}
		requests := group.requests
		s.publicationGroupMu.Unlock()
		joined := make([][]byte, 0, group.records)
		for _, r := range requests {
			joined = append(joined, r.records...)
		}
		s.publicationMu.Lock()
		failure := s.publicationFailures[publicationSourceKey{schema, tags.ProviderID, tags.SourceName}]
		s.publicationMu.Unlock()
		var cids []string
		var outcomes []format4.Outcome
		err := failure
		if err == nil {
			cids, outcomes, err = b.putRetained(op, schema, joined, peer, sig, tags, opt)
		}
		s.publicationWriteMu.Unlock()
		offset := 0
		for _, r := range requests {
			end := offset + len(r.records)
			part := publicationPutResult{cids: cids[min(offset, len(cids)):min(end, len(cids))], outcomes: outcomes[min(offset, len(outcomes)):min(end, len(outcomes))], err: err}
			var refused *RefusedRecordsError
			if errors.As(err, &refused) {
				own := &RefusedRecordsError{Schema: schema, Records: len(r.records)}
				for i, out := range part.outcomes {
					if out.Action == format4.ActRejected {
						own.refuse(part.cids[i], refused.Refused[part.cids[i]])
					}
				}
				if len(own.Order) > 0 {
					part.err = own
				} else {
					part.err = nil
				}
			}
			r.done <- part
			offset = end
		}
	}
	result := <-request.done
	return result.cids, result.outcomes, result.err
}
