package storage

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// publicationSetFingerprintPage is the rows read per window by
// PagedDatasetPublicationSetFingerprint.
var publicationSetFingerprintPage int64 = 20000

// PagedDatasetPublicationSetFingerprint is DatasetPublicationSetFingerprint
// read a page at a time on a format-4 store, in CID order: one window over a
// whole lane decoded every row at once (the 895,910-row asteroid lane took
// the node's heap from ~0.2 to ~3.4 GB on every auto-publish; multi-body
// round 2, coordinator D3). The fingerprint and the count are the same.
// Other stores answer as DatasetPublicationSetFingerprint does.
func (s *FlatSQLStore) PagedDatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return s.DatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID)
	}
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" {
		return "", 0, errors.New("schema, provider and source are required for a publication fingerprint")
	}
	if err := b.closed(); err != nil {
		return "", 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return "", 0, err
	}
	h := newPublicationSetHash(schemaName, providerID, sourceName, batchID)
	count := 0
	for offset := int64(0); ; offset += publicationSetFingerprintPage {
		recs, err := b.d.api().Window(b.d.ctx, format4.Query{Type: typ, Lane: f4Lane(providerID, sourceName, batchID, "", ""),
			Order: format4.OrderCID, Bulk: true, Limit: publicationSetFingerprintPage, Offset: offset})
		if errors.Is(err, format4.ErrNoType) {
			break
		}
		if err != nil {
			return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
		}
		for _, r := range recs {
			h.Write([]byte(r.CID))
			h.Write([]byte{'\n'})
		}
		count += len(recs)
		if int64(len(recs)) < publicationSetFingerprintPage {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), count, nil
}
