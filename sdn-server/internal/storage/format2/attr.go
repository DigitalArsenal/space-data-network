package format2

// RecordAttr (flatsql schemas/flatsql_attr.fbs, file identifier "FSRA"): the
// per-record attributes the router hands the writer with every record. The
// bytes are deterministic and equal the engine's own buildRecordAttr for the
// same inputs (testdata/record_attr.hex, design 22.3a-6): strings are
// created in field order and fields are added in the order flatc's C++
// Create* functions add them.
//
//	table SourceTag  { provider_id, source_name, source_url, batch_id,
//	                   content_key_id, producer_peer_id, producer_public_key: string }
//	table RecordAttr { peer_id: [ubyte]; signature: [ubyte]; supersede_key: string;
//	                   source_timestamp: long; licence_key: string; tags: [SourceTag];
//	                   migrated_gseq: ulong }

import (
	flatbuffers "github.com/google/flatbuffers/go"
)

// SourceTag is one tag instance of a record (design A2: at most one per
// RecordAttr; a record's further tags are RETAG rows). Every field is kept
// per copy; the engine's tag tuple (dedupe, reconcile) leaves SourceURL out,
// as the legacy tag table's key did.
type SourceTag struct {
	ProviderID, SourceName, SourceURL, BatchID, ContentKeyID, ProducerPeerID, ProducerPublicKey string
}

func (t SourceTag) empty() bool {
	return t.ProviderID == "" && t.SourceName == "" && t.SourceURL == "" && t.BatchID == "" && t.ContentKeyID == "" &&
		t.ProducerPeerID == "" && t.ProducerPublicKey == ""
}

// RecordAttr is the router's per-record attribute set.
type RecordAttr struct {
	PeerID          []byte // the storing call's raw peer id (A3: the partition token comes from it)
	Signature       []byte
	SupersedeKey    string // stored verbatim when present (22.3a-9); absent: the engine derives it
	// SourceTimestamp is when this copy's tag instance was stored, in unix
	// seconds (the legacy tag row's created_at, served as materialized_at);
	// 0 = absent.
	SourceTimestamp int64
	LicenceKey      string
	Tag             SourceTag
	// MigratedGseq is store-migrate's legacy sdn_record_index.rowid for a
	// FIRST copy (flatsql 3.2.0, PARTITION-STORE.md §31): the engine keeps it
	// as the copy's gseq when it is above the type's committed gseq_hi, and
	// counts a fallback otherwise. 0 = absent.
	MigratedGseq uint64
}

// BuildRecordAttr returns the RecordAttr FlatBuffer (finished with "FSRA").
// An empty source URL is absent, so a tag without one has the engine's bytes.
func BuildRecordAttr(a RecordAttr) []byte {
	b := flatbuffers.NewBuilder(256)
	var tags flatbuffers.UOffsetT
	if !a.Tag.empty() {
		provider := b.CreateString(a.Tag.ProviderID)
		source := b.CreateString(a.Tag.SourceName)
		var url flatbuffers.UOffsetT
		if a.Tag.SourceURL != "" {
			url = b.CreateString(a.Tag.SourceURL)
		}
		batch := b.CreateString(a.Tag.BatchID)
		content := b.CreateString(a.Tag.ContentKeyID)
		producerPeer := b.CreateString(a.Tag.ProducerPeerID)
		producerKey := b.CreateString(a.Tag.ProducerPublicKey)
		b.StartObject(7)
		b.PrependUOffsetTSlot(6, producerKey, 0)
		b.PrependUOffsetTSlot(5, producerPeer, 0)
		b.PrependUOffsetTSlot(4, content, 0)
		b.PrependUOffsetTSlot(3, batch, 0)
		b.PrependUOffsetTSlot(2, url, 0)
		b.PrependUOffsetTSlot(1, source, 0)
		b.PrependUOffsetTSlot(0, provider, 0)
		tag := b.EndObject()
		b.StartVector(4, 1, 4)
		b.PrependUOffsetT(tag)
		tags = b.EndVector(1)
	}
	peer := b.CreateByteVector(a.PeerID)
	var signature, sk, lk flatbuffers.UOffsetT
	if len(a.Signature) > 0 {
		signature = b.CreateByteVector(a.Signature)
	}
	if a.SupersedeKey != "" {
		sk = b.CreateString(a.SupersedeKey)
	}
	if a.LicenceKey != "" {
		lk = b.CreateString(a.LicenceKey)
	}
	b.StartObject(7)
	b.PrependUint64Slot(6, a.MigratedGseq, 0)
	b.PrependInt64Slot(3, a.SourceTimestamp, 0)
	b.PrependUOffsetTSlot(5, tags, 0)
	b.PrependUOffsetTSlot(4, lk, 0)
	b.PrependUOffsetTSlot(2, sk, 0)
	b.PrependUOffsetTSlot(1, signature, 0)
	b.PrependUOffsetTSlot(0, peer, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte("FSRA"))
	return b.FinishedBytes()
}
