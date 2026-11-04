package storage

// $OCM ELEMENT SETS (program design row 9): SGP4XP and AP sets travel as $OCM
// element-set records. The store keys a set on Metadata.NORAD_CAT_ID and
// MEAN_ELEMENTS.EPOCH, per MEAN_ELEMENTS.MEAN_ELEMENT_THEORY. The engine
// indexes no epoch for $OCM (that column is routed to the FlatSQL engine's
// owner), so this read pages the lane's records and decodes the two keys.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
)

const ocmElementSetPage = 10000

// OCMElementSet is one stored $OCM element set and its keys.
type OCMElementSet struct {
	Record     *Record
	NoradCatID uint32    // Metadata.NORAD_CAT_ID
	Theory     string    // MEAN_ELEMENTS.MEAN_ELEMENT_THEORY by name
	Epoch      time.Time // MEAN_ELEMENTS.EPOCH
}

// DecodeOCM reads a bare or size-prefixed $OCM record.
func DecodeOCM(data []byte) (*OCM.OCM, error) {
	switch {
	case OCM.SizePrefixedOCMBufferHasIdentifier(data):
		return OCM.GetSizePrefixedRootAsOCM(data, 0), nil
	case OCM.OCMBufferHasIdentifier(data):
		return OCM.GetRootAsOCM(data, 0), nil
	default:
		return nil, fmt.Errorf("invalid OCM buffer")
	}
}

// ocmElementSetKeys returns a record's keys; ok is false for a record that is
// not an element set: no MEAN_ELEMENTS, theory UNSPECIFIED, no
// NORAD_CAT_ID (no catalog key) or an unreadable EPOCH.
func ocmElementSetKeys(data []byte) (norad uint32, theory string, epoch time.Time, ok bool, err error) {
	ocm, err := DecodeOCM(data)
	if err != nil {
		return 0, "", time.Time{}, false, err
	}
	mean := ocm.MEAN_ELEMENTS(nil)
	meta := ocm.METADATA(nil)
	if mean == nil || meta == nil {
		return 0, "", time.Time{}, false, nil
	}
	theory = mean.MEAN_ELEMENT_THEORY().String()
	norad = meta.NORAD_CAT_ID()
	if theory == "UNSPECIFIED" || norad == 0 {
		return 0, "", time.Time{}, false, nil
	}
	unix, perr := parseEpochString(strings.TrimSpace(string(mean.EPOCH())))
	if perr != nil {
		return 0, "", time.Time{}, false, nil
	}
	return norad, theory, time.Unix(unix, 0).UTC(), true, nil
}

// QueryOCMElementSetsNearest returns, for one provider and source lane, the
// stored $OCM element set whose MEAN_ELEMENTS.EPOCH is nearest at for each
// (Metadata.NORAD_CAT_ID, MEAN_ELEMENTS.MEAN_ELEMENT_THEORY), a tie going to
// the lower CID, sorted by NORAD_CAT_ID then theory.
func (s *FlatSQLStore) QueryOCMElementSetsNearest(providerID, sourceName string, at time.Time) ([]OCMElementSet, error) {
	type key struct {
		norad  uint32
		theory string
	}
	type pick struct {
		set   OCMElementSet
		delta int64
	}
	best := map[key]pick{}
	for offset := 0; ; offset += ocmElementSetPage {
		records, err := s.QueryIndexedRecords(IndexedRecordQuery{
			SchemaName: "OCM.fbs", ProviderID: providerID, SourceName: sourceName,
			Limit: ocmElementSetPage, Offset: offset, AllowLargeResultSet: true, OrderByCID: true,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			norad, theory, epoch, ok, err := ocmElementSetKeys(r.Data)
			if err != nil {
				return nil, fmt.Errorf("OCM record %s: %w", r.CID, err)
			}
			if !ok {
				continue
			}
			k := key{norad, theory}
			delta := absInt64(epoch.Unix() - at.Unix())
			if prev, seen := best[k]; !seen || delta < prev.delta || (delta == prev.delta && r.CID < prev.set.Record.CID) {
				best[k] = pick{OCMElementSet{r, norad, theory, epoch}, delta}
			}
		}
		if len(records) < ocmElementSetPage {
			break
		}
	}
	out := make([]OCMElementSet, 0, len(best))
	for _, p := range best {
		out = append(out, p.set)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NoradCatID != out[j].NoradCatID {
			return out[i].NoradCatID < out[j].NoradCatID
		}
		return out[i].Theory < out[j].Theory
	})
	return out, nil
}
