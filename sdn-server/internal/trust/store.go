package trust

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdstnr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TNR"
	sdstre "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRE"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	trustNodeSchema                = "TNR.fbs"
	trustEdgeSchema                = "TRE.fbs"
	defaultTrustProviderPeerID     = "sdn-trust-graph"
	trustProjectionLoadPageSize    = 1000
	defaultTrustRecordBufferLength = 256
)

// Store persists the trust DAG as SDS TNR/TRE records in the node FlatSQL store
// and rebuilds the current graph as a projection over those records.
type Store struct {
	flatStore      *storage.FlatSQLStore
	providerPeerID string
}

// NewStoreWithFlatSQL stores trust graph state as SDS records in the node's
// FlatSQL store. No private sidecar database is opened.
func NewStoreWithFlatSQL(flatStore *storage.FlatSQLStore) (*Store, error) {
	if flatStore == nil {
		return nil, errors.New("trust: FlatSQL store is required")
	}
	return &Store{
		flatStore:      flatStore,
		providerPeerID: defaultTrustProviderPeerID,
	}, nil
}

// Close is kept for callers that own trust stores. FlatSQL lifecycle is owned
// by the node-level storage layer.
func (s *Store) Close() error {
	return nil
}

// UpsertNode persists a node record (idempotent).
func (s *Store) UpsertNode(id string) error {
	return s.upsertNodeFlatSQL(id)
}

// UpsertEdge persists an edge record. Callers MUST have inserted the edge into a
// Graph first (which enforces acyclicity) - the store is the durable image,
// not the invariant keeper.
func (s *Store) UpsertEdge(e Edge) error {
	return s.upsertEdgeFlatSQL(e)
}

// DeleteEdge tombstones an edge record.
func (s *Store) DeleteEdge(truster, trustee string) error {
	return s.deleteEdgeFlatSQL(truster, trustee)
}

// DeleteNode tombstones a node record and every edge touching it.
func (s *Store) DeleteNode(id string) error {
	return s.deleteNodeFlatSQL(id)
}

// LoadGraph rebuilds the in-memory DAG from the persisted records. Every edge
// is re-validated through Graph.SetEdge, so a corrupted/cyclic projection fails
// loudly instead of silently producing a cyclic graph.
func (s *Store) LoadGraph() (*Graph, error) {
	return s.loadGraphFlatSQL()
}

func (s *Store) upsertNodeFlatSQL(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("trust: node id required")
	}
	nodes, _, err := s.loadTrustProjection()
	if err != nil {
		return err
	}
	current := nodes[id]
	if current.ID != "" && !current.Deleted {
		return nil
	}

	now := time.Now().UnixMilli()
	createdAt := now
	if current.CreatedAtMs > 0 {
		createdAt = current.CreatedAtMs
	}
	record := trustNodeRecord{
		ID:             id,
		CreatedAtMs:    createdAt,
		UpdatedAtMs:    now,
		ProviderPeerID: s.providerPeerID,
	}
	return s.storeTrustNodeRecord(record)
}

func (s *Store) upsertEdgeFlatSQL(e Edge) error {
	e.Truster = strings.TrimSpace(e.Truster)
	e.Trustee = strings.TrimSpace(e.Trustee)
	if e.Truster == "" || e.Trustee == "" {
		return errors.New("trust: edge endpoints required")
	}
	if err := s.UpsertNode(e.Truster); err != nil {
		return err
	}
	if err := s.UpsertNode(e.Trustee); err != nil {
		return err
	}
	if e.UpdatedAtMs <= 0 {
		e.UpdatedAtMs = time.Now().UnixMilli()
	}
	record := trustEdgeRecord{
		EdgeID:         trustEdgeID(e.Truster, e.Trustee),
		Edge:           e,
		ProviderPeerID: s.providerPeerID,
	}
	return s.storeTrustEdgeRecord(record)
}

func (s *Store) deleteEdgeFlatSQL(truster, trustee string) error {
	truster = strings.TrimSpace(truster)
	trustee = strings.TrimSpace(trustee)
	if truster == "" || trustee == "" {
		return errors.New("trust: edge endpoints required")
	}
	record := trustEdgeRecord{
		EdgeID: trustEdgeID(truster, trustee),
		Edge: Edge{
			Truster:     truster,
			Trustee:     trustee,
			UpdatedAtMs: time.Now().UnixMilli(),
		},
		Deleted:        true,
		ProviderPeerID: s.providerPeerID,
	}
	return s.storeTrustEdgeRecord(record)
}

func (s *Store) deleteNodeFlatSQL(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("trust: node id required")
	}

	nodes, edges, err := s.loadTrustProjection()
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, edge := range edges {
		if edge.Deleted || (edge.Truster != id && edge.Trustee != id) {
			continue
		}
		tombstone := edge
		tombstone.Deleted = true
		tombstone.UpdatedAtMs = now
		tombstone.ProviderPeerID = s.providerPeerID
		if err := s.storeTrustEdgeRecord(tombstone); err != nil {
			return err
		}
	}

	record := trustNodeRecord{
		ID:             id,
		CreatedAtMs:    nodes[id].CreatedAtMs,
		UpdatedAtMs:    now,
		Deleted:        true,
		ProviderPeerID: s.providerPeerID,
	}
	return s.storeTrustNodeRecord(record)
}

func (s *Store) loadGraphFlatSQL() (*Graph, error) {
	nodes, edges, err := s.loadTrustProjection()
	if err != nil {
		return nil, err
	}

	g := NewGraph()
	deletedNodes := make(map[string]struct{}, len(nodes))
	for id, node := range nodes {
		if node.Deleted {
			deletedNodes[id] = struct{}{}
			continue
		}
		if err := g.AddNode(id); err != nil {
			return nil, err
		}
	}

	activeEdges := make([]trustEdgeRecord, 0, len(edges))
	for _, edge := range edges {
		if edge.Deleted {
			continue
		}
		if _, deleted := deletedNodes[edge.Truster]; deleted {
			continue
		}
		if _, deleted := deletedNodes[edge.Trustee]; deleted {
			continue
		}
		activeEdges = append(activeEdges, edge)
	}
	sort.Slice(activeEdges, func(i, j int) bool {
		if activeEdges[i].UpdatedAtMs != activeEdges[j].UpdatedAtMs {
			return activeEdges[i].UpdatedAtMs < activeEdges[j].UpdatedAtMs
		}
		return activeEdges[i].EdgeID < activeEdges[j].EdgeID
	})

	for _, edge := range activeEdges {
		if err := g.SetEdge(edge.Edge); err != nil {
			return nil, fmt.Errorf("trust: persisted edge %s->%s invalid: %w", edge.Truster, edge.Trustee, err)
		}
	}
	return g, nil
}

type trustNodeRecord struct {
	ID                string
	CreatedAtMs       int64
	UpdatedAtMs       int64
	Deleted           bool
	ProviderPeerID    string
	ProviderSignature []byte
}

type trustEdgeRecord struct {
	EdgeID string
	Edge
	Deleted           bool
	ProviderPeerID    string
	ProviderSignature []byte
}

func (s *Store) loadTrustProjection() (map[string]trustNodeRecord, map[string]trustEdgeRecord, error) {
	if s == nil || s.flatStore == nil {
		return nil, nil, errors.New("trust: FlatSQL store is required")
	}
	nodes := map[string]trustNodeRecord{}
	edges := map[string]trustEdgeRecord{}

	var afterRowID int64
	for {
		records, err := s.flatStore.QueryRawRecords(storage.RawRecordQuery{
			SchemaName:     trustNodeSchema,
			Limit:          trustProjectionLoadPageSize,
			UseRowIDCursor: true,
			AfterRowID:     afterRowID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("trust: load TNR records: %w", err)
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			if record == nil {
				continue
			}
			if record.RowID > afterRowID {
				afterRowID = record.RowID
			}
			node, err := decodeTrustNodeRecord(record.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("trust: decode TNR record %s: %w", record.CID, err)
			}
			if node.UpdatedAtMs <= 0 && !record.Timestamp.IsZero() {
				node.UpdatedAtMs = record.Timestamp.UnixMilli()
			}
			if node.ID == "" {
				continue
			}
			current := nodes[node.ID]
			if current.ID == "" || node.UpdatedAtMs >= current.UpdatedAtMs {
				nodes[node.ID] = node
			}
		}
		if len(records) < trustProjectionLoadPageSize {
			break
		}
	}

	afterRowID = 0
	for {
		records, err := s.flatStore.QueryRawRecords(storage.RawRecordQuery{
			SchemaName:     trustEdgeSchema,
			Limit:          trustProjectionLoadPageSize,
			UseRowIDCursor: true,
			AfterRowID:     afterRowID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("trust: load TRE records: %w", err)
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			if record == nil {
				continue
			}
			if record.RowID > afterRowID {
				afterRowID = record.RowID
			}
			edge, err := decodeTrustEdgeRecord(record.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("trust: decode TRE record %s: %w", record.CID, err)
			}
			if edge.UpdatedAtMs <= 0 && !record.Timestamp.IsZero() {
				edge.UpdatedAtMs = record.Timestamp.UnixMilli()
			}
			if edge.EdgeID == "" {
				edge.EdgeID = trustEdgeID(edge.Truster, edge.Trustee)
			}
			if edge.EdgeID == "" {
				continue
			}
			current := edges[edge.EdgeID]
			if current.EdgeID == "" || edge.UpdatedAtMs >= current.UpdatedAtMs {
				edges[edge.EdgeID] = edge
			}
		}
		if len(records) < trustProjectionLoadPageSize {
			break
		}
	}

	return nodes, edges, nil
}

func (s *Store) storeTrustNodeRecord(record trustNodeRecord) error {
	if s == nil || s.flatStore == nil {
		return errors.New("trust: FlatSQL store is required")
	}
	data, err := encodeTrustNodeRecord(record)
	if err != nil {
		return err
	}
	if _, err := s.flatStore.Store(trustNodeSchema, data, trustRecordPeerID(record.ProviderPeerID, s.providerPeerID), record.ProviderSignature); err != nil {
		return fmt.Errorf("trust: store TNR record: %w", err)
	}
	return nil
}

func (s *Store) storeTrustEdgeRecord(record trustEdgeRecord) error {
	if s == nil || s.flatStore == nil {
		return errors.New("trust: FlatSQL store is required")
	}
	data, err := encodeTrustEdgeRecord(record)
	if err != nil {
		return err
	}
	if _, err := s.flatStore.Store(trustEdgeSchema, data, trustRecordPeerID(record.ProviderPeerID, s.providerPeerID), record.ProviderSignature); err != nil {
		return fmt.Errorf("trust: store TRE record: %w", err)
	}
	return nil
}

func encodeTrustNodeRecord(record trustNodeRecord) ([]byte, error) {
	record.ID = strings.TrimSpace(record.ID)
	record.ProviderPeerID = strings.TrimSpace(record.ProviderPeerID)
	if record.ID == "" {
		return nil, errors.New("trust: node id required")
	}
	builder := flatbuffers.NewBuilder(defaultTrustRecordBufferLength)
	nodeID := trustStringOffset(builder, record.ID)
	providerPeerID := trustStringOffset(builder, record.ProviderPeerID)
	signature := trustByteVector(builder, record.ProviderSignature, sdstnr.TNRStartPROVIDER_SIGNATUREVector)

	sdstnr.TNRStart(builder)
	sdstnr.TNRAddNODE_ID(builder, nodeID)
	sdstnr.TNRAddCREATED_AT(builder, trustInt64ToUint64(record.CreatedAtMs))
	sdstnr.TNRAddUPDATED_AT(builder, trustInt64ToUint64(record.UpdatedAtMs))
	sdstnr.TNRAddDELETED(builder, record.Deleted)
	sdstnr.TNRAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdstnr.TNRAddPROVIDER_SIGNATURE(builder, signature)
	root := sdstnr.TNREnd(builder)
	sdstnr.FinishTNRBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func decodeTrustNodeRecord(data []byte) (trustNodeRecord, error) {
	if len(data) == 0 {
		return trustNodeRecord{}, errors.New("empty TNR record")
	}
	if !sdstnr.TNRBufferHasIdentifier(data) {
		return trustNodeRecord{}, errors.New("record is not TNR.fbs")
	}
	record := sdstnr.GetRootAsTNR(data, 0)
	return trustNodeRecord{
		ID:                strings.TrimSpace(string(record.NODE_ID())),
		CreatedAtMs:       trustUint64ToInt64(record.CREATED_AT()),
		UpdatedAtMs:       trustUint64ToInt64(record.UPDATED_AT()),
		Deleted:           record.DELETED(),
		ProviderPeerID:    strings.TrimSpace(string(record.PROVIDER_PEER_ID())),
		ProviderSignature: append([]byte(nil), record.PROVIDER_SIGNATUREBytes()...),
	}, nil
}

func encodeTrustEdgeRecord(record trustEdgeRecord) ([]byte, error) {
	record.Truster = strings.TrimSpace(record.Truster)
	record.Trustee = strings.TrimSpace(record.Trustee)
	record.EdgeID = strings.TrimSpace(record.EdgeID)
	record.ProviderPeerID = strings.TrimSpace(record.ProviderPeerID)
	if record.Truster == "" || record.Trustee == "" {
		return nil, errors.New("trust: edge endpoints required")
	}
	if record.EdgeID == "" {
		record.EdgeID = trustEdgeID(record.Truster, record.Trustee)
	}

	builder := flatbuffers.NewBuilder(defaultTrustRecordBufferLength)
	edgeID := trustStringOffset(builder, record.EdgeID)
	truster := trustStringOffset(builder, record.Truster)
	trustee := trustStringOffset(builder, record.Trustee)
	providerPeerID := trustStringOffset(builder, record.ProviderPeerID)
	signature := trustByteVector(builder, record.ProviderSignature, sdstre.TREStartPROVIDER_SIGNATUREVector)

	sdstre.TREStart(builder)
	sdstre.TREAddEDGE_ID(builder, edgeID)
	sdstre.TREAddTRUSTER_ID(builder, truster)
	sdstre.TREAddTRUSTEE_ID(builder, trustee)
	sdstre.TREAddWEIGHT(builder, record.Weight)
	sdstre.TREAddUPDATED_AT(builder, trustInt64ToUint64(record.UpdatedAtMs))
	sdstre.TREAddDELETED(builder, record.Deleted)
	sdstre.TREAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdstre.TREAddPROVIDER_SIGNATURE(builder, signature)
	root := sdstre.TREEnd(builder)
	sdstre.FinishTREBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func decodeTrustEdgeRecord(data []byte) (trustEdgeRecord, error) {
	if len(data) == 0 {
		return trustEdgeRecord{}, errors.New("empty TRE record")
	}
	if !sdstre.TREBufferHasIdentifier(data) {
		return trustEdgeRecord{}, errors.New("record is not TRE.fbs")
	}
	record := sdstre.GetRootAsTRE(data, 0)
	edge := trustEdgeRecord{
		EdgeID: strings.TrimSpace(string(record.EDGE_ID())),
		Edge: Edge{
			Truster:     strings.TrimSpace(string(record.TRUSTER_ID())),
			Trustee:     strings.TrimSpace(string(record.TRUSTEE_ID())),
			Weight:      record.WEIGHT(),
			UpdatedAtMs: trustUint64ToInt64(record.UPDATED_AT()),
		},
		Deleted:           record.DELETED(),
		ProviderPeerID:    strings.TrimSpace(string(record.PROVIDER_PEER_ID())),
		ProviderSignature: append([]byte(nil), record.PROVIDER_SIGNATUREBytes()...),
	}
	if edge.EdgeID == "" {
		edge.EdgeID = trustEdgeID(edge.Truster, edge.Trustee)
	}
	return edge, nil
}

func trustEdgeID(truster, trustee string) string {
	truster = strings.TrimSpace(truster)
	trustee = strings.TrimSpace(trustee)
	if truster == "" || trustee == "" {
		return ""
	}
	return truster + "->" + trustee
}

func trustRecordPeerID(providerPeerID, fallback string) string {
	if providerPeerID = strings.TrimSpace(providerPeerID); providerPeerID != "" {
		return providerPeerID
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return defaultTrustProviderPeerID
}

func trustStringOffset(builder *flatbuffers.Builder, value string) flatbuffers.UOffsetT {
	if value == "" {
		return 0
	}
	return builder.CreateString(value)
}

func trustByteVector(builder *flatbuffers.Builder, data []byte, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(data) == 0 {
		return 0
	}
	start(builder, len(data))
	for i := len(data) - 1; i >= 0; i-- {
		builder.PrependByte(data[i])
	}
	return builder.EndVector(len(data))
}

func trustInt64ToUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func trustUint64ToInt64(value uint64) int64 {
	if value > uint64(^uint64(0)>>1) {
		return 0
	}
	return int64(value)
}
