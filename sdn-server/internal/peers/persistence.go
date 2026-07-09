// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdspgm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PGM"
	sdsprr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PRR"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	peerRegistrySchema                = "PRR.fbs"
	peerGroupSchema                   = "PGM.fbs"
	defaultPeerRegistryProviderPeerID = "sdn-peer-registry"
	peerProjectionLoadPageSize        = 1000
	defaultPeerRecordBufferLength     = 512
)

// FlatSQLPersistence stores peer registry state as SDS records in the node's
// shared FlatSQL store. It does not create a private peer registry sidecar.
type FlatSQLPersistence struct {
	flatStore      *storage.FlatSQLStore
	providerPeerID string
}

// NewFlatSQLPersistence creates a peer-registry persistence provider over the
// node FlatSQL store.
func NewFlatSQLPersistence(flatStore *storage.FlatSQLStore) (*FlatSQLPersistence, error) {
	if flatStore == nil {
		return nil, errors.New("peers: FlatSQL store is required")
	}
	return &FlatSQLPersistence{
		flatStore:      flatStore,
		providerPeerID: defaultPeerRegistryProviderPeerID,
	}, nil
}

// Save appends current peer and group records, plus tombstones for registry
// entries that disappeared since the previous projection.
func (fp *FlatSQLPersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	if fp == nil || fp.flatStore == nil {
		return errors.New("peers: FlatSQL store is required")
	}

	currentPeers, currentGroups, err := fp.loadProjection()
	if err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	nextPeerIDs := make(map[string]struct{}, len(peers))
	for _, tp := range peers {
		if tp == nil || tp.ID == "" {
			continue
		}
		id := strings.TrimSpace(tp.ID.String())
		if id == "" {
			continue
		}
		nextPeerIDs[id] = struct{}{}
		record := peerRegistryRecordFromTrustedPeer(tp, now)
		record.ProviderPeerID = fp.providerPeerID
		if err := fp.storePeerRecord(record); err != nil {
			return err
		}
	}
	for id, current := range currentPeers {
		if current.Deleted {
			continue
		}
		if _, ok := nextPeerIDs[id]; ok {
			continue
		}
		tombstone := current
		tombstone.UpdatedAtMs = now
		tombstone.Deleted = true
		tombstone.ProviderPeerID = fp.providerPeerID
		if err := fp.storePeerRecord(tombstone); err != nil {
			return err
		}
	}

	nextGroupNames := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if group == nil || strings.TrimSpace(group.Name) == "" {
			continue
		}
		name := strings.TrimSpace(group.Name)
		nextGroupNames[name] = struct{}{}
		record := peerGroupRecordFromGroup(group, now)
		record.ProviderPeerID = fp.providerPeerID
		if err := fp.storeGroupRecord(record); err != nil {
			return err
		}
	}
	for name, current := range currentGroups {
		if current.Deleted {
			continue
		}
		if _, ok := nextGroupNames[name]; ok {
			continue
		}
		tombstone := current
		tombstone.UpdatedAtMs = now
		tombstone.Deleted = true
		tombstone.ProviderPeerID = fp.providerPeerID
		if err := fp.storeGroupRecord(tombstone); err != nil {
			return err
		}
	}

	return nil
}

// Load rebuilds the in-memory peer registry from the latest PRR/PGM records.
func (fp *FlatSQLPersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	peerRecords, groupRecords, err := fp.loadProjection()
	if err != nil {
		return nil, nil, err
	}

	peers := make(map[peer.ID]*TrustedPeer, len(peerRecords))
	for _, record := range peerRecords {
		if record.Deleted {
			continue
		}
		peerID, err := peer.Decode(record.ID)
		if err != nil {
			continue
		}
		peers[peerID] = record.toTrustedPeer(peerID)
	}

	groups := make(map[string]*PeerGroup, len(groupRecords))
	for _, record := range groupRecords {
		if record.Deleted || record.Name == "" {
			continue
		}
		groups[record.Name] = record.toPeerGroup()
	}

	return peers, groups, nil
}

// Close is kept for callers that own persistence providers. FlatSQL lifecycle
// is owned by the node storage layer.
func (fp *FlatSQLPersistence) Close() error {
	return nil
}

type peerRegistryRecord struct {
	ID                string
	Addrs             []string
	TrustLevel        TrustLevel
	Name              string
	Organization      string
	Groups            []string
	Notes             string
	AddedAtMs         int64
	LastSeenMs        int64
	LastConnectedMs   int64
	ConnectionCount   int64
	MessagesReceived  int64
	MessagesSent      int64
	BytesReceived     int64
	BytesSent         int64
	EPMData           []byte
	VCardData         string
	Metadata          map[string]string
	UpdatedAtMs       int64
	Deleted           bool
	ProviderPeerID    string
	ProviderSignature []byte
}

type peerGroupRecord struct {
	Name              string
	Description       string
	DefaultTrustLevel TrustLevel
	Members           []string
	CreatedAtMs       int64
	UpdatedAtMs       int64
	Metadata          map[string]string
	Deleted           bool
	ProviderPeerID    string
	ProviderSignature []byte
}

func (fp *FlatSQLPersistence) loadProjection() (map[string]peerRegistryRecord, map[string]peerGroupRecord, error) {
	if fp == nil || fp.flatStore == nil {
		return nil, nil, errors.New("peers: FlatSQL store is required")
	}
	peerRecords := map[string]peerRegistryRecord{}
	groupRecords := map[string]peerGroupRecord{}

	var afterRowID int64
	for {
		records, err := fp.flatStore.QueryRawRecords(storage.RawRecordQuery{
			SchemaName:     peerRegistrySchema,
			Limit:          peerProjectionLoadPageSize,
			UseRowIDCursor: true,
			AfterRowID:     afterRowID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("peers: load PRR records: %w", err)
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
			peerRecord, err := decodePeerRegistryRecord(record.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("peers: decode PRR record %s: %w", record.CID, err)
			}
			if peerRecord.UpdatedAtMs <= 0 && !record.Timestamp.IsZero() {
				peerRecord.UpdatedAtMs = record.Timestamp.UnixMilli()
			}
			if peerRecord.ID == "" {
				continue
			}
			current := peerRecords[peerRecord.ID]
			if current.ID == "" || peerRecord.UpdatedAtMs >= current.UpdatedAtMs {
				peerRecords[peerRecord.ID] = peerRecord
			}
		}
		if len(records) < peerProjectionLoadPageSize {
			break
		}
	}

	afterRowID = 0
	for {
		records, err := fp.flatStore.QueryRawRecords(storage.RawRecordQuery{
			SchemaName:     peerGroupSchema,
			Limit:          peerProjectionLoadPageSize,
			UseRowIDCursor: true,
			AfterRowID:     afterRowID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("peers: load PGM records: %w", err)
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
			groupRecord, err := decodePeerGroupRecord(record.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("peers: decode PGM record %s: %w", record.CID, err)
			}
			if groupRecord.UpdatedAtMs <= 0 && !record.Timestamp.IsZero() {
				groupRecord.UpdatedAtMs = record.Timestamp.UnixMilli()
			}
			if groupRecord.Name == "" {
				continue
			}
			current := groupRecords[groupRecord.Name]
			if current.Name == "" || groupRecord.UpdatedAtMs >= current.UpdatedAtMs {
				groupRecords[groupRecord.Name] = groupRecord
			}
		}
		if len(records) < peerProjectionLoadPageSize {
			break
		}
	}

	return peerRecords, groupRecords, nil
}

func (fp *FlatSQLPersistence) storePeerRecord(record peerRegistryRecord) error {
	data, err := encodePeerRegistryRecord(record)
	if err != nil {
		return err
	}
	if _, err := fp.flatStore.Store(peerRegistrySchema, data, peerRecordProviderID(record.ProviderPeerID, fp.providerPeerID), record.ProviderSignature); err != nil {
		return fmt.Errorf("peers: store PRR record: %w", err)
	}
	return nil
}

func (fp *FlatSQLPersistence) storeGroupRecord(record peerGroupRecord) error {
	data, err := encodePeerGroupRecord(record)
	if err != nil {
		return err
	}
	if _, err := fp.flatStore.Store(peerGroupSchema, data, peerRecordProviderID(record.ProviderPeerID, fp.providerPeerID), record.ProviderSignature); err != nil {
		return fmt.Errorf("peers: store PGM record: %w", err)
	}
	return nil
}

func peerRegistryRecordFromTrustedPeer(tp *TrustedPeer, updatedAtMs int64) peerRegistryRecord {
	addedAtMs := peerTimeToUnixMilli(tp.AddedAt)
	if addedAtMs <= 0 {
		addedAtMs = updatedAtMs
	}
	return peerRegistryRecord{
		ID:               strings.TrimSpace(tp.ID.String()),
		Addrs:            multiaddrsToStrings(tp.Addrs),
		TrustLevel:       normalizeTrustLevel(tp.TrustLevel),
		Name:             tp.Name,
		Organization:     tp.Organization,
		Groups:           compactStrings(tp.Groups),
		Notes:            tp.Notes,
		AddedAtMs:        addedAtMs,
		LastSeenMs:       peerTimeToUnixMilli(tp.LastSeen),
		LastConnectedMs:  peerTimeToUnixMilli(tp.LastConnected),
		ConnectionCount:  tp.ConnectionCount,
		MessagesReceived: tp.MessagesReceived,
		MessagesSent:     tp.MessagesSent,
		BytesReceived:    tp.BytesReceived,
		BytesSent:        tp.BytesSent,
		EPMData:          append([]byte(nil), tp.EPMData...),
		VCardData:        tp.VCardData,
		Metadata:         copyStringMap(tp.Metadata),
		UpdatedAtMs:      updatedAtMs,
	}
}

func peerGroupRecordFromGroup(group *PeerGroup, updatedAtMs int64) peerGroupRecord {
	createdAtMs := peerTimeToUnixMilli(group.CreatedAt)
	if createdAtMs <= 0 {
		createdAtMs = updatedAtMs
	}
	return peerGroupRecord{
		Name:              strings.TrimSpace(group.Name),
		Description:       group.Description,
		DefaultTrustLevel: normalizeTrustLevel(group.DefaultTrustLevel),
		Members:           peerIDsToStrings(group.Members),
		CreatedAtMs:       createdAtMs,
		UpdatedAtMs:       updatedAtMs,
		Metadata:          copyStringMap(group.Metadata),
	}
}

func (record peerRegistryRecord) toTrustedPeer(peerID peer.ID) *TrustedPeer {
	return &TrustedPeer{
		ID:               peerID,
		Addrs:            stringsToMultiaddrs(record.Addrs),
		TrustLevel:       normalizeTrustLevel(record.TrustLevel),
		Name:             record.Name,
		Organization:     record.Organization,
		Groups:           append([]string(nil), record.Groups...),
		Notes:            record.Notes,
		AddedAt:          peerUnixMilliToTime(record.AddedAtMs),
		LastSeen:         peerUnixMilliToTime(record.LastSeenMs),
		LastConnected:    peerUnixMilliToTime(record.LastConnectedMs),
		ConnectionCount:  record.ConnectionCount,
		MessagesReceived: record.MessagesReceived,
		MessagesSent:     record.MessagesSent,
		BytesReceived:    record.BytesReceived,
		BytesSent:        record.BytesSent,
		EPMData:          append([]byte(nil), record.EPMData...),
		VCardData:        record.VCardData,
		Metadata:         copyStringMap(record.Metadata),
	}
}

func (record peerGroupRecord) toPeerGroup() *PeerGroup {
	return &PeerGroup{
		Name:              record.Name,
		Description:       record.Description,
		DefaultTrustLevel: normalizeTrustLevel(record.DefaultTrustLevel),
		Members:           stringsToPeerIDs(record.Members),
		CreatedAt:         peerUnixMilliToTime(record.CreatedAtMs),
		Metadata:          copyStringMap(record.Metadata),
	}
}

func encodePeerRegistryRecord(record peerRegistryRecord) ([]byte, error) {
	record.ID = strings.TrimSpace(record.ID)
	record.ProviderPeerID = strings.TrimSpace(record.ProviderPeerID)
	if record.ID == "" {
		return nil, errors.New("peers: peer id required")
	}

	builder := flatbuffers.NewBuilder(defaultPeerRecordBufferLength)
	peerID := peerStringOffset(builder, record.ID)
	addrs := peerStringVector(builder, record.Addrs, sdsprr.PRRStartMULTIFORMAT_ADDRESSVector)
	name := peerStringOffset(builder, record.Name)
	organization := peerStringOffset(builder, record.Organization)
	groups := peerStringVector(builder, record.Groups, sdsprr.PRRStartGROUPSVector)
	notes := peerStringOffset(builder, record.Notes)
	epmData := peerByteVector(builder, record.EPMData, sdsprr.PRRStartEPM_DATAVector)
	vcardData := peerStringOffset(builder, record.VCardData)
	metadata := peerRegistryMetadataVector(builder, record.Metadata)
	providerPeerID := peerStringOffset(builder, record.ProviderPeerID)
	signature := peerByteVector(builder, record.ProviderSignature, sdsprr.PRRStartPROVIDER_SIGNATUREVector)

	sdsprr.PRRStart(builder)
	sdsprr.PRRAddPEER_ID(builder, peerID)
	sdsprr.PRRAddMULTIFORMAT_ADDRESS(builder, addrs)
	builder.PrependInt8Slot(2, int8(normalizeTrustLevel(record.TrustLevel)), 2)
	sdsprr.PRRAddNAME(builder, name)
	sdsprr.PRRAddORGANIZATION(builder, organization)
	sdsprr.PRRAddGROUPS(builder, groups)
	sdsprr.PRRAddNOTES(builder, notes)
	sdsprr.PRRAddADDED_AT(builder, int64ToUint64(record.AddedAtMs))
	sdsprr.PRRAddLAST_SEEN(builder, int64ToUint64(record.LastSeenMs))
	sdsprr.PRRAddLAST_CONNECTED(builder, int64ToUint64(record.LastConnectedMs))
	sdsprr.PRRAddCONNECTION_COUNT(builder, int64ToUint64(record.ConnectionCount))
	sdsprr.PRRAddMESSAGES_RECEIVED(builder, int64ToUint64(record.MessagesReceived))
	sdsprr.PRRAddMESSAGES_SENT(builder, int64ToUint64(record.MessagesSent))
	sdsprr.PRRAddBYTES_RECEIVED(builder, int64ToUint64(record.BytesReceived))
	sdsprr.PRRAddBYTES_SENT(builder, int64ToUint64(record.BytesSent))
	sdsprr.PRRAddEPM_DATA(builder, epmData)
	sdsprr.PRRAddVCARD_DATA(builder, vcardData)
	sdsprr.PRRAddMETADATA(builder, metadata)
	sdsprr.PRRAddUPDATED_AT(builder, int64ToUint64(record.UpdatedAtMs))
	sdsprr.PRRAddDELETED(builder, record.Deleted)
	sdsprr.PRRAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdsprr.PRRAddPROVIDER_SIGNATURE(builder, signature)
	root := sdsprr.PRREnd(builder)
	sdsprr.FinishPRRBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func decodePeerRegistryRecord(data []byte) (peerRegistryRecord, error) {
	if len(data) == 0 {
		return peerRegistryRecord{}, errors.New("empty PRR record")
	}
	if !sdsprr.PRRBufferHasIdentifier(data) {
		return peerRegistryRecord{}, errors.New("record is not PRR.fbs")
	}
	record := sdsprr.GetRootAsPRR(data, 0)
	metadata := make(map[string]string)
	var entry sdsprr.PRRMetadataEntry
	for i := 0; i < record.METADATALength(); i++ {
		if !record.METADATA(&entry, i) {
			continue
		}
		key := strings.TrimSpace(string(entry.KEY()))
		if key == "" {
			continue
		}
		metadata[key] = string(entry.VALUE())
	}

	return peerRegistryRecord{
		ID:                strings.TrimSpace(string(record.PEER_ID())),
		Addrs:             decodePRRStrings(record.MULTIFORMAT_ADDRESSLength(), record.MULTIFORMAT_ADDRESS),
		TrustLevel:        normalizeTrustLevel(TrustLevel(int8(record.TRUST_LEVEL()))),
		Name:              string(record.NAME()),
		Organization:      string(record.ORGANIZATION()),
		Groups:            decodePRRStrings(record.GROUPSLength(), record.GROUPS),
		Notes:             string(record.NOTES()),
		AddedAtMs:         uint64ToInt64(record.ADDED_AT()),
		LastSeenMs:        uint64ToInt64(record.LAST_SEEN()),
		LastConnectedMs:   uint64ToInt64(record.LAST_CONNECTED()),
		ConnectionCount:   uint64ToInt64(record.CONNECTION_COUNT()),
		MessagesReceived:  uint64ToInt64(record.MESSAGES_RECEIVED()),
		MessagesSent:      uint64ToInt64(record.MESSAGES_SENT()),
		BytesReceived:     uint64ToInt64(record.BYTES_RECEIVED()),
		BytesSent:         uint64ToInt64(record.BYTES_SENT()),
		EPMData:           append([]byte(nil), record.EPM_DATABytes()...),
		VCardData:         string(record.VCARD_DATA()),
		Metadata:          metadata,
		UpdatedAtMs:       uint64ToInt64(record.UPDATED_AT()),
		Deleted:           record.DELETED(),
		ProviderPeerID:    strings.TrimSpace(string(record.PROVIDER_PEER_ID())),
		ProviderSignature: append([]byte(nil), record.PROVIDER_SIGNATUREBytes()...),
	}, nil
}

func encodePeerGroupRecord(record peerGroupRecord) ([]byte, error) {
	record.Name = strings.TrimSpace(record.Name)
	record.ProviderPeerID = strings.TrimSpace(record.ProviderPeerID)
	if record.Name == "" {
		return nil, errors.New("peers: group name required")
	}

	builder := flatbuffers.NewBuilder(defaultPeerRecordBufferLength)
	groupName := peerStringOffset(builder, record.Name)
	description := peerStringOffset(builder, record.Description)
	members := peerStringVector(builder, record.Members, sdspgm.PGMStartMEMBERSVector)
	metadata := peerGroupMetadataVector(builder, record.Metadata)
	providerPeerID := peerStringOffset(builder, record.ProviderPeerID)
	signature := peerByteVector(builder, record.ProviderSignature, sdspgm.PGMStartPROVIDER_SIGNATUREVector)

	sdspgm.PGMStart(builder)
	sdspgm.PGMAddGROUP_NAME(builder, groupName)
	sdspgm.PGMAddDESCRIPTION(builder, description)
	builder.PrependInt8Slot(2, int8(normalizeTrustLevel(record.DefaultTrustLevel)), 2)
	sdspgm.PGMAddMEMBERS(builder, members)
	sdspgm.PGMAddCREATED_AT(builder, int64ToUint64(record.CreatedAtMs))
	sdspgm.PGMAddUPDATED_AT(builder, int64ToUint64(record.UpdatedAtMs))
	sdspgm.PGMAddMETADATA(builder, metadata)
	sdspgm.PGMAddDELETED(builder, record.Deleted)
	sdspgm.PGMAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdspgm.PGMAddPROVIDER_SIGNATURE(builder, signature)
	root := sdspgm.PGMEnd(builder)
	sdspgm.FinishPGMBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func decodePeerGroupRecord(data []byte) (peerGroupRecord, error) {
	if len(data) == 0 {
		return peerGroupRecord{}, errors.New("empty PGM record")
	}
	if !sdspgm.PGMBufferHasIdentifier(data) {
		return peerGroupRecord{}, errors.New("record is not PGM.fbs")
	}
	record := sdspgm.GetRootAsPGM(data, 0)
	metadata := make(map[string]string)
	var entry sdspgm.PGMMetadataEntry
	for i := 0; i < record.METADATALength(); i++ {
		if !record.METADATA(&entry, i) {
			continue
		}
		key := strings.TrimSpace(string(entry.KEY()))
		if key == "" {
			continue
		}
		metadata[key] = string(entry.VALUE())
	}

	return peerGroupRecord{
		Name:              strings.TrimSpace(string(record.GROUP_NAME())),
		Description:       string(record.DESCRIPTION()),
		DefaultTrustLevel: normalizeTrustLevel(TrustLevel(int8(record.DEFAULT_TRUST_LEVEL()))),
		Members:           decodePGMStrings(record.MEMBERSLength(), record.MEMBERS),
		CreatedAtMs:       uint64ToInt64(record.CREATED_AT()),
		UpdatedAtMs:       uint64ToInt64(record.UPDATED_AT()),
		Metadata:          metadata,
		Deleted:           record.DELETED(),
		ProviderPeerID:    strings.TrimSpace(string(record.PROVIDER_PEER_ID())),
		ProviderSignature: append([]byte(nil), record.PROVIDER_SIGNATUREBytes()...),
	}, nil
}

func peerRegistryMetadataVector(builder *flatbuffers.Builder, metadata map[string]string) flatbuffers.UOffsetT {
	keys := sortedMetadataKeys(metadata)
	if len(keys) == 0 {
		return 0
	}
	entries := make([]flatbuffers.UOffsetT, 0, len(keys))
	for _, key := range keys {
		keyOffset := peerStringOffset(builder, key)
		valueOffset := peerStringOffset(builder, metadata[key])
		sdsprr.PRRMetadataEntryStart(builder)
		sdsprr.PRRMetadataEntryAddKEY(builder, keyOffset)
		sdsprr.PRRMetadataEntryAddVALUE(builder, valueOffset)
		entries = append(entries, sdsprr.PRRMetadataEntryEnd(builder))
	}
	sdsprr.PRRStartMETADATAVector(builder, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(entries[i])
	}
	return builder.EndVector(len(entries))
}

func peerGroupMetadataVector(builder *flatbuffers.Builder, metadata map[string]string) flatbuffers.UOffsetT {
	keys := sortedMetadataKeys(metadata)
	if len(keys) == 0 {
		return 0
	}
	entries := make([]flatbuffers.UOffsetT, 0, len(keys))
	for _, key := range keys {
		keyOffset := peerStringOffset(builder, key)
		valueOffset := peerStringOffset(builder, metadata[key])
		sdspgm.PGMMetadataEntryStart(builder)
		sdspgm.PGMMetadataEntryAddKEY(builder, keyOffset)
		sdspgm.PGMMetadataEntryAddVALUE(builder, valueOffset)
		entries = append(entries, sdspgm.PGMMetadataEntryEnd(builder))
	}
	sdspgm.PGMStartMETADATAVector(builder, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(entries[i])
	}
	return builder.EndVector(len(entries))
}

func peerStringOffset(builder *flatbuffers.Builder, value string) flatbuffers.UOffsetT {
	if value == "" {
		return 0
	}
	return builder.CreateString(value)
}

func peerStringVector(builder *flatbuffers.Builder, values []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	values = compactStrings(values)
	if len(values) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, 0, len(values))
	for _, value := range values {
		offsets = append(offsets, builder.CreateString(value))
	}
	start(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func peerByteVector(builder *flatbuffers.Builder, data []byte, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(data) == 0 {
		return 0
	}
	start(builder, len(data))
	for i := len(data) - 1; i >= 0; i-- {
		builder.PrependByte(data[i])
	}
	return builder.EndVector(len(data))
}

func decodePRRStrings(length int, get func(int) []byte) []string {
	values := make([]string, 0, length)
	for i := 0; i < length; i++ {
		value := strings.TrimSpace(string(get(i)))
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func decodePGMStrings(length int, get func(int) []byte) []string {
	values := make([]string, 0, length)
	for i := 0; i < length; i++ {
		value := strings.TrimSpace(string(get(i)))
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func multiaddrsToStrings(addrs []multiaddr.Multiaddr) []string {
	strs := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr == nil {
			continue
		}
		strs = append(strs, addr.String())
	}
	return strs
}

func stringsToMultiaddrs(strs []string) []multiaddr.Multiaddr {
	addrs := make([]multiaddr.Multiaddr, 0, len(strs))
	for _, s := range strs {
		if addr, err := multiaddr.NewMultiaddr(s); err == nil {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

func peerIDsToStrings(ids []peer.ID) []string {
	strs := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		strs = append(strs, id.String())
	}
	return strs
}

func stringsToPeerIDs(strs []string) []peer.ID {
	ids := make([]peer.ID, 0, len(strs))
	for _, s := range strs {
		if id, err := peer.Decode(s); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func peerTimeToUnixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func peerUnixMilliToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func int64ToUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func uint64ToInt64(value uint64) int64 {
	if value > uint64(^uint64(0)>>1) {
		return 0
	}
	return int64(value)
}

// normalizeTrustLevel clamps a TrustLevel to the valid persisted range
// [Never, Ultimate]. Out-of-range/corrupted values (including the pre-C1
// range check's old [Untrusted, Admin] bound) fail closed to Unknown
// rather than the previous Standard fallback: a value we can't make sense
// of should never be silently upgraded to a positively-trusted default.
func normalizeTrustLevel(level TrustLevel) TrustLevel {
	if level < Never || level > Ultimate {
		return Unknown
	}
	return level
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = value
		}
	}
	return out
}

func sortedMetadataKeys(metadata map[string]string) []string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func peerRecordProviderID(providerPeerID, fallback string) string {
	if providerPeerID = strings.TrimSpace(providerPeerID); providerPeerID != "" {
		return providerPeerID
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return defaultPeerRegistryProviderPeerID
}

// JSONFilePersistence provides simple JSON file-based persistence for explicit
// operator export/import paths. Production SDN nodes use FlatSQLPersistence.
type JSONFilePersistence struct {
	path string
}

// NewJSONFilePersistence creates a new JSON file persistence provider.
func NewJSONFilePersistence(path string) *JSONFilePersistence {
	return &JSONFilePersistence{path: path}
}

// Save saves peers and groups to a JSON file.
func (jp *JSONFilePersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	dir := filepath.Dir(jp.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data := struct {
		Peers  []*TrustedPeer `json:"peers"`
		Groups []*PeerGroup   `json:"groups"`
	}{
		Peers:  make([]*TrustedPeer, 0, len(peers)),
		Groups: make([]*PeerGroup, 0, len(groups)),
	}

	for _, tp := range peers {
		data.Peers = append(data.Peers, tp)
	}
	for _, g := range groups {
		data.Groups = append(data.Groups, g)
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(jp.path, jsonData, 0644)
}

// Load loads peers and groups from a JSON file.
func (jp *JSONFilePersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	peers := make(map[peer.ID]*TrustedPeer)
	groups := make(map[string]*PeerGroup)

	data, err := os.ReadFile(jp.path)
	if err != nil {
		if os.IsNotExist(err) {
			return peers, groups, nil
		}
		return nil, nil, err
	}

	var loaded struct {
		Peers  []*TrustedPeer `json:"peers"`
		Groups []*PeerGroup   `json:"groups"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, nil, err
	}

	for _, tp := range loaded.Peers {
		peers[tp.ID] = tp
	}
	for _, g := range loaded.Groups {
		groups[g.Name] = g
	}

	return peers, groups, nil
}
