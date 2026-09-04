package trust

// Binary $TRE edge records used by the node-first dashboard lane. The FlatSQL
// store keeps its historical bare-buffer form internally; HTTP always uses the
// canonical size-prefixed form assembled here.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	sdstre "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRE"
	flatbuffers "github.com/google/flatbuffers/go"
)

var edgeSignatureDomain = []byte("SDN-TRUST-EDGE-V1\n")

// EdgeRecord is the complete durable $TRE record, including its tombstone and
// signer fields.
type EdgeRecord struct {
	EdgeID string
	Edge
	Deleted           bool
	ProviderPeerID    string
	ProviderSignature []byte
}

// EdgeRecordID returns the stable schema identifier for an edge.
func EdgeRecordID(truster, trustee string) string {
	return trustEdgeID(truster, trustee)
}

// EdgeSigningPayload returns the deterministic, domain-separated bytes covered
// by PROVIDER_SIGNATURE. The signature field itself is excluded.
func EdgeSigningPayload(record EdgeRecord) ([]byte, error) {
	record, err := normalizeEdgeRecord(record)
	if err != nil {
		return nil, err
	}
	deleted := "0"
	if record.Deleted {
		deleted = "1"
	}
	var b strings.Builder
	b.Write(edgeSignatureDomain)
	b.WriteString("edge:" + record.EdgeID + "\n")
	b.WriteString("truster:" + record.Truster + "\n")
	b.WriteString("trustee:" + record.Trustee + "\n")
	b.WriteString("weight:" + strconv.FormatFloat(record.Weight, 'g', -1, 64) + "\n")
	b.WriteString("updated:" + strconv.FormatInt(record.UpdatedAtMs, 10) + "\n")
	b.WriteString("deleted:" + deleted + "\n")
	b.WriteString("provider:" + record.ProviderPeerID + "\n")
	return []byte(b.String()), nil
}

// EncodeEdgeFrame serializes one size-prefixed $TRE record.
func EncodeEdgeFrame(record EdgeRecord) ([]byte, error) {
	record, err := normalizeEdgeRecord(record)
	if err != nil {
		return nil, err
	}
	b := flatbuffers.NewBuilder(defaultTrustRecordBufferLength)
	edgeID := trustStringOffset(b, record.EdgeID)
	truster := trustStringOffset(b, record.Truster)
	trustee := trustStringOffset(b, record.Trustee)
	providerPeerID := trustStringOffset(b, record.ProviderPeerID)
	signature := trustByteVector(b, record.ProviderSignature, sdstre.TREStartPROVIDER_SIGNATUREVector)

	sdstre.TREStart(b)
	sdstre.TREAddEDGE_ID(b, edgeID)
	sdstre.TREAddTRUSTER_ID(b, truster)
	sdstre.TREAddTRUSTEE_ID(b, trustee)
	sdstre.TREAddWEIGHT(b, record.Weight)
	sdstre.TREAddUPDATED_AT(b, trustInt64ToUint64(record.UpdatedAtMs))
	sdstre.TREAddDELETED(b, record.Deleted)
	sdstre.TREAddPROVIDER_PEER_ID(b, providerPeerID)
	sdstre.TREAddPROVIDER_SIGNATURE(b, signature)
	root := sdstre.TREEnd(b)
	sdstre.FinishSizePrefixedTREBuffer(b, root)
	return b.FinishedBytes(), nil
}

// DecodeEdgeFrame parses one canonical size-prefixed $TRE record.
func DecodeEdgeFrame(frame []byte) (record EdgeRecord, err error) {
	record, err = DecodeEdgeDraftFrame(frame)
	if err != nil {
		return EdgeRecord{}, err
	}
	return normalizeEdgeRecord(record)
}

// DecodeEdgeDraftFrame parses one size-prefixed $TRE frame without requiring
// the signer fields. Operator clients use this form to ask the node to stamp
// and sign its own edge; fully signed network records still pass through
// DecodeEdgeFrame and its complete endpoint/identifier validation.
func DecodeEdgeDraftFrame(frame []byte) (record EdgeRecord, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			record = EdgeRecord{}
			err = fmt.Errorf("trust: invalid TRE frame: %v", recovered)
		}
	}()
	if len(frame) == 0 || !sdstre.SizePrefixedTREBufferHasIdentifier(frame) {
		return EdgeRecord{}, errors.New("trust: frame is not a size-prefixed TRE record")
	}
	root := sdstre.GetSizePrefixedRootAsTRE(frame, 0)
	record = EdgeRecord{
		EdgeID: strings.TrimSpace(string(root.EDGE_ID())),
		Edge: Edge{
			Truster:     strings.TrimSpace(string(root.TRUSTER_ID())),
			Trustee:     strings.TrimSpace(string(root.TRUSTEE_ID())),
			Weight:      root.WEIGHT(),
			UpdatedAtMs: trustUint64ToInt64(root.UPDATED_AT()),
		},
		Deleted:           root.DELETED(),
		ProviderPeerID:    strings.TrimSpace(string(root.PROVIDER_PEER_ID())),
		ProviderSignature: append([]byte(nil), root.PROVIDER_SIGNATUREBytes()...),
	}
	return record, nil
}

func normalizeEdgeRecord(record EdgeRecord) (EdgeRecord, error) {
	record.Truster = strings.TrimSpace(record.Truster)
	record.Trustee = strings.TrimSpace(record.Trustee)
	record.EdgeID = strings.TrimSpace(record.EdgeID)
	record.ProviderPeerID = strings.TrimSpace(record.ProviderPeerID)
	if record.Truster == "" || record.Trustee == "" {
		return EdgeRecord{}, errors.New("trust: edge endpoints required")
	}
	wantID := EdgeRecordID(record.Truster, record.Trustee)
	if record.EdgeID == "" {
		record.EdgeID = wantID
	}
	if record.EdgeID != wantID {
		return EdgeRecord{}, fmt.Errorf("trust: EDGE_ID %q does not match %q", record.EdgeID, wantID)
	}
	if record.Weight < 0 || record.Weight > 1 {
		return EdgeRecord{}, fmt.Errorf("trust: weight %v outside [0,1]", record.Weight)
	}
	return record, nil
}

// StoreEdgeRecord persists the complete record without dropping its provider
// identity, signature, or DELETED tombstone.
func (s *Store) StoreEdgeRecord(record EdgeRecord) error {
	record, err := normalizeEdgeRecord(record)
	if err != nil {
		return err
	}
	return s.storeTrustEdgeRecord(trustEdgeRecord{
		EdgeID:            record.EdgeID,
		Edge:              record.Edge,
		Deleted:           record.Deleted,
		ProviderPeerID:    record.ProviderPeerID,
		ProviderSignature: append([]byte(nil), record.ProviderSignature...),
	})
}

// EdgeRecords returns the latest record for every known edge, including
// tombstones, in stable edge-ID order.
func (s *Store) EdgeRecords() ([]EdgeRecord, error) {
	_, projection, err := s.loadTrustProjection()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(projection))
	for id := range projection {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]EdgeRecord, 0, len(ids))
	for _, id := range ids {
		stored := projection[id]
		out = append(out, EdgeRecord{
			EdgeID:            stored.EdgeID,
			Edge:              stored.Edge,
			Deleted:           stored.Deleted,
			ProviderPeerID:    stored.ProviderPeerID,
			ProviderSignature: append([]byte(nil), stored.ProviderSignature...),
		})
	}
	return out, nil
}
