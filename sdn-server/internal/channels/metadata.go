package channels

import (
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

type VerifiedMetadata struct {
	ChannelID         string
	ChannelHead       string
	PNMCID            string
	PNMBytes          []byte
	PNMFileID         string
	DPMFileID         string
	SignatureType     string
	VerifiedAt        time.Time
	DPMVerifiedAt     time.Time
	ProviderPeer      string
	ProviderPublicKey string
	Visibility        string
	EncryptionState   string
	ContentKeyID      string
	EncryptionPolicy  string
	LocalRows         int
	RemoteRows        int
	SyncedRows        int
	MissingRows       int
	PinnedRows        int
	PinnedBytes       int64
	SyncedBytes       int64
	ThroughputBPS     int64
	WireUtilization   *float64
	TimingsMs         map[string]int64
	LastImportFailure string
}

type VerifiedMetadataRegistry struct {
	mu       sync.RWMutex
	metadata map[string]VerifiedMetadata
}

func NewVerifiedMetadataRegistry() *VerifiedMetadataRegistry {
	return &VerifiedMetadataRegistry{metadata: make(map[string]VerifiedMetadata)}
}

func (r *VerifiedMetadataRegistry) RecordPNM(channel ChannelID, evidence PNMTrustEvidence) VerifiedMetadata {
	metadata := VerifiedMetadata{
		ChannelID:         channel.ChannelID,
		PNMCID:            evidence.CID,
		PNMBytes:          append([]byte(nil), evidence.EnvelopeBytes...),
		PNMFileID:         evidence.FileID,
		SignatureType:     evidence.SignatureType,
		VerifiedAt:        time.Now().UTC(),
		ProviderPublicKey: hex.EncodeToString(evidence.ProviderPublicKey),
		Visibility:        "public",
		EncryptionState:   "none",
	}
	if r == nil {
		return metadata
	}
	r.mu.Lock()
	r.metadata[channel.ChannelID] = metadata
	r.mu.Unlock()
	return metadata
}

func (r *VerifiedMetadataRegistry) RecordDPM(channel ChannelID, evidence DPMTrustEvidence) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.Lock()
	metadata, ok := r.metadata[channel.ChannelID]
	if ok {
		metadata.DPMFileID = evidence.FileID
		metadata.DPMVerifiedAt = time.Now().UTC()
		if evidence.ProviderPeer != "" {
			metadata.ProviderPeer = evidence.ProviderPeer
		}
		if evidence.Encrypted {
			metadata.Visibility = dpmPolicyVisibility(evidence.PolicyID)
			metadata.EncryptionState = "encrypted"
			metadata.ContentKeyID = evidence.ContentKeyID
			metadata.EncryptionPolicy = evidence.PolicyID
		} else if metadata.Visibility == "" {
			metadata.Visibility = "public"
			metadata.EncryptionState = "none"
		}
		r.metadata[channel.ChannelID] = metadata
	}
	r.mu.Unlock()
	return metadata, ok
}

func (r *VerifiedMetadataRegistry) RecordNativeStream(channel ChannelID, snapshot NativeStreamSnapshot, throughputBPS int64, wireUtilization *float64, timingsMs map[string]int64) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.Lock()
	metadata, ok := r.metadata[channel.ChannelID]
	if ok {
		metadata.SyncedBytes = int64(snapshot.ByteCount)
		metadata.SyncedRows = snapshot.FrameCount
		metadata.RemoteRows = snapshot.FrameCount
		metadata.LocalRows = snapshot.FrameCount
		metadata.MissingRows = 0
		metadata.PinnedRows = snapshot.FrameCount
		metadata.PinnedBytes = int64(snapshot.ByteCount)
		metadata.ThroughputBPS = throughputBPS
		metadata.WireUtilization = wireUtilization
		metadata.TimingsMs = cloneTimingsMs(timingsMs)
		r.metadata[channel.ChannelID] = metadata
	}
	r.mu.Unlock()
	return metadata, ok
}

func (r *VerifiedMetadataRegistry) RecordDatasetPublication(channel ChannelID, feedHead string, recordCount int, byteCount int64) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.Lock()
	metadata, ok := r.metadata[channel.ChannelID]
	if ok {
		metadata.ChannelHead = strings.TrimSpace(feedHead)
		metadata.LocalRows = recordCount
		metadata.RemoteRows = recordCount
		metadata.SyncedRows = recordCount
		metadata.MissingRows = 0
		metadata.PinnedRows = recordCount
		metadata.PinnedBytes = byteCount
		metadata.SyncedBytes = byteCount
		r.metadata[channel.ChannelID] = metadata
	}
	r.mu.Unlock()
	return metadata, ok
}

func (r *VerifiedMetadataRegistry) Get(channel ChannelID) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.RLock()
	metadata, ok := r.metadata[channel.ChannelID]
	r.mu.RUnlock()
	metadata.PNMBytes = append([]byte(nil), metadata.PNMBytes...)
	metadata.TimingsMs = cloneTimingsMs(metadata.TimingsMs)
	return metadata, ok
}

func (r *VerifiedMetadataRegistry) List() []VerifiedMetadata {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	rows := make([]VerifiedMetadata, 0, len(r.metadata))
	for _, metadata := range r.metadata {
		metadata.PNMBytes = append([]byte(nil), metadata.PNMBytes...)
		metadata.TimingsMs = cloneTimingsMs(metadata.TimingsMs)
		rows = append(rows, metadata)
	}
	r.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].ChannelID < rows[j].ChannelID
	})
	return rows
}

func cloneTimingsMs(timings map[string]int64) map[string]int64 {
	if timings == nil {
		return nil
	}
	clone := make(map[string]int64, len(timings))
	for key, value := range timings {
		clone[key] = value
	}
	return clone
}

func dpmPolicyVisibility(policyID string) string {
	policy := strings.ToLower(strings.TrimSpace(policyID))
	switch {
	case strings.Contains(policy, "private-hidden") || policy == "hidden":
		return "private-hidden"
	case strings.Contains(policy, "private-listed") || policy == "listed" || policy == "private":
		return "private-listed"
	default:
		return "private-listed"
	}
}
