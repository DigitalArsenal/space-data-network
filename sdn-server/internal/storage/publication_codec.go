package storage

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

func publicationRefusalsFB(m map[int]int32) []*pst.RefusalT {
	out := make([]*pst.RefusalT, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, &pst.RefusalT{Index: int32(k), Code: m[k]})
	}
	return out
}
func publicationRefusalsFromFB(v []*pst.RefusalT) (map[int]int32, error) {
	out := make(map[int]int32, len(v))
	for _, r := range v {
		if r == nil || r.Index < 0 {
			return nil, fmt.Errorf("invalid publication refusal index")
		}
		if _, ok := out[int(r.Index)]; ok {
			return nil, fmt.Errorf("duplicate publication refusal index")
		}
		out[int(r.Index)] = r.Code
	}
	return out, nil
}
func publicationTagsFB(v SourceTags) *pst.SourceTagsT {
	return &pst.SourceTagsT{ProviderId: v.ProviderID, SourceName: v.SourceName, SourceUrl: v.SourceURL, BatchId: v.BatchID, ContentKeyId: v.ContentKeyID, ProducerPeerId: v.ProducerPeerID, ProducerPublicKey: v.ProducerPublicKey, License: v.License, LicenseUrl: v.LicenseURL, Citation: v.Citation, ShareAlike: v.ShareAlike}
}
func publicationTagsFromFB(v *pst.SourceTagsT) SourceTags {
	if v == nil {
		return SourceTags{}
	}
	return SourceTags{ProviderID: v.ProviderId, SourceName: v.SourceName, SourceURL: v.SourceUrl, BatchID: v.BatchId, ContentKeyID: v.ContentKeyId, ProducerPeerID: v.ProducerPeerId, ProducerPublicKey: v.ProducerPublicKey, License: v.License, LicenseURL: v.LicenseUrl, Citation: v.Citation, ShareAlike: v.ShareAlike}
}

// Publication metadata has a closed set of typed FlatBuffers representations.
// Sorted map entries make hashes and durable bytes independent of map iteration.
func marshalPublication(v any) ([]byte, error) {
	var body pst.BodyT
	switch x := v.(type) {
	case publicationEntry:
		body = pst.BodyT{Type: pst.BodyJournalEntry, Value: &pst.JournalEntryT{Digest: x.Digest, Sequence: x.Sequence, SourceSequence: x.SourceSequence, Schema: x.Schema, Tags: publicationTagsFB(x.Tags), Peer: x.Peer, Signature: x.Signature, Identity: x.Identity, OwnTs: x.OwnTS, Applied: x.Applied, Refused: publicationRefusalsFB(x.Refused)}}
	case []PublicationSource:
		list := &pst.SourceListT{}
		for _, s := range x {
			list.Sources = append(list.Sources, &pst.RetainedSourceT{Schema: s.Schema, Provider: s.Provider, Source: s.Source, ProductSource: s.ProductSource, CatalogObjectLimit: int32(s.CatalogObjectLimit)})
		}
		body = pst.BodyT{Type: pst.BodySourceList, Value: list}
	case map[int]int32:
		body = pst.BodyT{Type: pst.BodyOutcome, Value: &pst.OutcomeT{Refused: publicationRefusalsFB(x)}}
	case publicationWatermarks:
		value := &pst.WatermarksT{Global: x.Global}
		for _, k := range slices.Sorted(maps.Keys(x.Sources)) {
			value.Sources = append(value.Sources, &pst.SourceSequenceT{Id: k, Sequence: x.Sources[k]})
		}
		body = pst.BodyT{Type: pst.BodyWatermarks, Value: value}
	case publicationFailure:
		body = pst.BodyT{Type: pst.BodyFailure, Value: &pst.FailureT{Attempts: int32(x.Attempts), Reason: x.Reason, RetryAt: x.RetryAt.UTC().Format(time.RFC3339Nano)}}
	case []uint64:
		body = pst.BodyT{Type: pst.BodySequenceList, Value: &pst.SequenceListT{Sequences: x}}
	case map[string]bool:
		value := &pst.ExclusionsT{}
		for _, k := range slices.Sorted(maps.Keys(x)) {
			value.Entries = append(value.Entries, &pst.ExclusionT{Cid: k, Excluded: x[k]})
		}
		body = pst.BodyT{Type: pst.BodyExclusions, Value: value}
	case SourceTags:
		body = pst.BodyT{Type: pst.BodySourceTags, Value: publicationTagsFB(x)}
	case map[string]string:
		value := &pst.NoticeListT{}
		for _, k := range slices.Sorted(maps.Keys(x)) {
			value.Entries = append(value.Entries, &pst.NoticeT{Key: k, Reason: x[k]})
		}
		body = pst.BodyT{Type: pst.BodyNoticeList, Value: value}
	case publicationReclaimCheckpoint:
		body = pst.BodyT{Type: pst.BodyReclamation, Value: &pst.ReclamationT{HighWater: x.HighWater, Reclaimed: x.Reclaimed, Pending: x.Pending}}
	case DatasetExportIndex:
		body = pst.BodyT{Type: pst.BodyExportIndex, Value: publicationExportFB(x)}
	default:
		return nil, fmt.Errorf("unsupported publication state type %T", v)
	}
	return publicationstate.Encode(&body), nil
}

func unmarshalPublication(data []byte, target any) error {
	var kind pst.Body
	switch target.(type) {
	case *publicationEntry:
		kind = pst.BodyJournalEntry
	case *[]PublicationSource:
		kind = pst.BodySourceList
	case *map[int]int32:
		kind = pst.BodyOutcome
	case *publicationWatermarks:
		kind = pst.BodyWatermarks
	case *publicationFailure:
		kind = pst.BodyFailure
	case *[]uint64:
		kind = pst.BodySequenceList
	case *map[string]bool:
		kind = pst.BodyExclusions
	case *SourceTags:
		kind = pst.BodySourceTags
	case *map[string]string:
		kind = pst.BodyNoticeList
	case *publicationReclaimCheckpoint:
		kind = pst.BodyReclamation
	case *DatasetExportIndex:
		kind = pst.BodyExportIndex
	default:
		return fmt.Errorf("unsupported publication state target %T", target)
	}
	body, err := publicationstate.Decode(data, kind)
	if err != nil {
		return err
	}
	switch out := target.(type) {
	case *publicationEntry:
		x := body.Value.(*pst.JournalEntryT)
		refused, err := publicationRefusalsFromFB(x.Refused)
		if err != nil {
			return err
		}
		*out = publicationEntry{Digest: x.Digest, Sequence: x.Sequence, SourceSequence: x.SourceSequence, Schema: x.Schema, Tags: publicationTagsFromFB(x.Tags), Peer: x.Peer, Signature: x.Signature, Identity: x.Identity, OwnTS: x.OwnTs, Applied: x.Applied, Refused: refused}
	case *[]PublicationSource:
		x := body.Value.(*pst.SourceListT)
		sources := make([]PublicationSource, 0, len(x.Sources))
		for _, s := range x.Sources {
			if s == nil || s.CatalogObjectLimit < 0 {
				return fmt.Errorf("invalid publication source")
			}
			sources = append(sources, PublicationSource{Schema: s.Schema, Provider: s.Provider, Source: s.Source, ProductSource: s.ProductSource, CatalogObjectLimit: int(s.CatalogObjectLimit)})
		}
		*out = sources
	case *map[int]int32:
		*out, err = publicationRefusalsFromFB(body.Value.(*pst.OutcomeT).Refused)
		return err
	case *publicationWatermarks:
		x := body.Value.(*pst.WatermarksT)
		m := map[string]uint64{}
		for _, s := range x.Sources {
			if s == nil {
				return fmt.Errorf("invalid publication watermark")
			}
			if _, ok := m[s.Id]; ok {
				return fmt.Errorf("duplicate publication watermark")
			}
			m[s.Id] = s.Sequence
		}
		*out = publicationWatermarks{Global: x.Global, Sources: m}
	case *publicationFailure:
		x := body.Value.(*pst.FailureT)
		retry, err := time.Parse(time.RFC3339Nano, x.RetryAt)
		if err != nil {
			return err
		}
		if x.Attempts < 0 {
			return fmt.Errorf("invalid publication attempt count")
		}
		*out = publicationFailure{Attempts: int(x.Attempts), Reason: x.Reason, RetryAt: retry}
	case *[]uint64:
		*out = body.Value.(*pst.SequenceListT).Sequences
	case *map[string]bool:
		m := map[string]bool{}
		for _, e := range body.Value.(*pst.ExclusionsT).Entries {
			if e == nil {
				return fmt.Errorf("invalid publication exclusion")
			}
			if _, ok := m[e.Cid]; ok {
				return fmt.Errorf("duplicate publication exclusion")
			}
			m[e.Cid] = e.Excluded
		}
		*out = m
	case *SourceTags:
		*out = publicationTagsFromFB(body.Value.(*pst.SourceTagsT))
	case *map[string]string:
		m := map[string]string{}
		for _, e := range body.Value.(*pst.NoticeListT).Entries {
			if e == nil {
				return fmt.Errorf("invalid publication notice")
			}
			if _, ok := m[e.Key]; ok {
				return fmt.Errorf("duplicate publication notice")
			}
			m[e.Key] = e.Reason
		}
		*out = m
	case *publicationReclaimCheckpoint:
		x := body.Value.(*pst.ReclamationT)
		*out = publicationReclaimCheckpoint{HighWater: x.HighWater, Reclaimed: x.Reclaimed, Pending: x.Pending}
	case *DatasetExportIndex:
		*out = publicationExportFromFB(body.Value.(*pst.ExportIndexT))
	}
	return nil
}

func publicationRowsFB(rows []int) []int32 {
	out := make([]int32, len(rows))
	for i, v := range rows {
		out[i] = int32(v)
	}
	return out
}
func publicationRowsFromFB(rows []int32) []int {
	out := make([]int, len(rows))
	for i, v := range rows {
		out[i] = int(v)
	}
	return out
}
func publicationGroupsFB(m map[string][]int) []*pst.IndexGroupT {
	out := make([]*pst.IndexGroupT, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, &pst.IndexGroupT{Key: k, Rows: publicationRowsFB(m[k])})
	}
	return out
}
func publicationGroupsFromFB(groups []*pst.IndexGroupT) map[string][]int {
	out := map[string][]int{}
	for _, g := range groups {
		out[g.Key] = publicationRowsFromFB(g.Rows)
	}
	return out
}
func publicationExportFB(x DatasetExportIndex) *pst.ExportIndexT {
	out := &pst.ExportIndexT{PublicationCutoff: x.PublicationCutoff, PublicationSourceCutoff: x.PublicationSourceCutoff, PublicationSourceSequences: x.PublicationSourceSequences, Version: int32(x.Version), SchemaName: x.SchemaName, ProviderId: x.ProviderID, SourceName: x.SourceName, BatchId: x.BatchID, QuerySha256: x.QuerySHA256, ResultSha256: x.ResultSHA256, ShardSha256: x.ShardSHA256, ShardCid: x.ShardCID, ShardFile: x.ShardFile, RecordCount: int32(x.RecordCount)}
	for _, r := range x.Records {
		out.Records = append(out.Records, &pst.IndexRecordT{Cid: r.CID, Offset: r.Offset, Length: r.Length, NoradCatId: r.NoradCatID, EntityId: r.EntityID, ObjectType: r.ObjectType, OpsStatusCode: r.OpsStatusCode, EpochUnix: r.EpochUnix, EpochDay: r.EpochDay, Tags: publicationTagsFB(r.SourceTags)})
	}
	m := x.Indexes
	out.Indexes = &pst.MaterializedIndexesT{ByNorad: publicationGroupsFB(m.ByNORAD), ByEntityId: publicationGroupsFB(m.ByEntityID), ByObjectType: publicationGroupsFB(m.ByObjectType), ByOpsStatusCode: publicationGroupsFB(m.ByOpsStatusCode), CaReadyResident: publicationRowsFB(m.CAReadyResident), ActivePayloads: publicationRowsFB(m.ActivePayloads)}
	return out
}
func publicationExportFromFB(x *pst.ExportIndexT) DatasetExportIndex {
	out := DatasetExportIndex{PublicationCutoff: x.PublicationCutoff, PublicationSourceCutoff: x.PublicationSourceCutoff, PublicationSourceSequences: x.PublicationSourceSequences, Version: int(x.Version), SchemaName: x.SchemaName, ProviderID: x.ProviderId, SourceName: x.SourceName, BatchID: x.BatchId, QuerySHA256: x.QuerySha256, ResultSHA256: x.ResultSha256, ShardSHA256: x.ShardSha256, ShardCID: x.ShardCid, ShardFile: x.ShardFile, RecordCount: int(x.RecordCount)}
	for _, r := range x.Records {
		out.Records = append(out.Records, DatasetExportIndexRecord{CID: r.Cid, Offset: r.Offset, Length: r.Length, NoradCatID: r.NoradCatId, EntityID: r.EntityId, ObjectType: r.ObjectType, OpsStatusCode: r.OpsStatusCode, EpochUnix: r.EpochUnix, EpochDay: r.EpochDay, SourceTags: publicationTagsFromFB(r.Tags)})
	}
	if m := x.Indexes; m != nil {
		out.Indexes = DatasetExportMaterializedMap{ByNORAD: publicationGroupsFromFB(m.ByNorad), ByEntityID: publicationGroupsFromFB(m.ByEntityId), ByObjectType: publicationGroupsFromFB(m.ByObjectType), ByOpsStatusCode: publicationGroupsFromFB(m.ByOpsStatusCode), CAReadyResident: publicationRowsFromFB(m.CaReadyResident), ActivePayloads: publicationRowsFromFB(m.ActivePayloads)}
	}
	return out
}
