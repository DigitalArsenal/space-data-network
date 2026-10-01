package format4proof

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// The format-4 side of the digests and the crash checks, through the
// engine's own API (contract §5.2) on a closed store.

// IntegrityNote says how integrity_check is covered: REBUILD what=8 runs
// PRAGMA integrity_check on every live file of every type inside the engine
// (contract C-27; no Go-side SQLite for records) and counts a file that is
// not ok as a mismatch, beside the derived state rebuilt from the files.
const IntegrityNote = "integrity_check: every live file, inside the engine (REBUILD what=8, C-27); 0 mismatches = every file ok"

// digestPage is the digest's SCAN page, and so the CID list of each GET and
// TAGS: a list must fit a read slot's 64 KiB request area (contract C-6;
// 36 B per CID).
const digestPage = 1024

// openFormat4 opens a closed format-4 store's engine directly (no daemon).
func openFormat4(ctx context.Context, store string) (*format4.Engine, error) {
	root, err := filepath.Abs(store)
	if err != nil {
		return nil, err
	}
	return format4.Open(ctx, format4.Options{DataRoot: root, Create: format4.OpenExisting,
		AOTCacheDir: storage.EngineAOTCacheDir(), CompileOnMiss: true})
}

// DigestAPI digests the given schemas of a format-4 store through its API:
// every seq (SCAN), every copy with its bytes (GET, all copies), every tag
// instance (TAGS).
func DigestAPI(ctx context.Context, api format4.API, schemas []string) (map[string]TypeDigest, error) {
	out := map[string]TypeDigest{}
	for _, schema := range schemas {
		typ, err := format4.TypeOf(schema)
		if err != nil {
			return nil, err
		}
		d := TypeDigest{Schema: schema, Partitions: map[string]PartitionCount{}}
		var data, copies, tags, tagsAt Multiset
		var after int64
		for {
			recs, err := api.Scan(ctx, format4.Query{Type: typ, Order: format4.OrderSeqAsc, SeqAfter: after, Limit: digestPage, Bulk: true})
			if err != nil {
				return nil, fmt.Errorf("digest %s: scan after %d: %w", schema, after, err)
			}
			if len(recs) == 0 {
				break
			}
			cids := make([]string, 0, len(recs))
			for _, r := range recs {
				cids = append(cids, r.CID)
				after = r.Seq
			}
			all, err := api.Get(ctx, typ, cids, true, true)
			if err != nil {
				return nil, fmt.Errorf("digest %s: get: %w", schema, err)
			}
			seen := map[string]bool{}
			for _, r := range all {
				pc := d.Partitions[r.Producer]
				pc.Records++
				pc.Bytes += int64(len(r.Data))
				d.Partitions[r.Producer] = pc
				copies.Add(r.Producer, r.CID)
				if seen[r.CID] {
					continue
				}
				seen[r.CID] = true
				data.Add(r.CID, digest(r.Data))
				d.Bytes += int64(len(r.Data))
			}
			if len(seen) != len(cids) {
				return nil, fmt.Errorf("digest %s: SCAN named %d records, GET returned %d", schema, len(cids), len(seen))
			}
			trs, err := api.Tags(ctx, typ, cids)
			if err != nil {
				return nil, fmt.Errorf("digest %s: tags: %w", schema, err)
			}
			for _, t := range trs {
				f := tagFields(t.CID, storage.SourceTags{ProviderID: t.Provider, SourceName: t.Source, SourceURL: t.SourceURL,
					BatchID: t.Batch, ContentKeyID: t.ContentKeyID, ProducerPeerID: t.ProducerPeer, ProducerPublicKey: t.ProducerPubkey})
				tags.Add(f...)
				tagsAt.Add(append(f, i64(t.At))...)
			}
		}
		d.Records, d.Copies = data.n, copies.n
		d.Data, d.CopySet, d.Tags, d.TagsAt = data.String(), copies.String(), tags.String(), tagsAt.String()
		out[schema] = d
	}
	return out, nil
}

func digestFormat4(store string, schemas []string) (map[string]TypeDigest, error) {
	ctx := context.Background()
	e, err := openFormat4(ctx, store)
	if err != nil {
		return nil, err
	}
	defer e.Close(ctx)
	return DigestAPI(ctx, e, schemas)
}

// VerifyAPI runs REBUILD verify (what=8) over every type: the derived state
// rebuilt from the partition files must equal the live one, and every live
// file must pass integrity_check (C-27). It returns the problems.
func VerifyAPI(ctx context.Context, api format4.API) ([]string, error) {
	rows, err := api.Rebuild(ctx, "", format4.RebuildVerify)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if r.Mismatches != 0 {
			out = append(out, fmt.Sprintf("%s: REBUILD verify (derived state, integrity_check): %d mismatches over %d entries", r.Type, r.Mismatches, r.Entries))
		}
	}
	return out, nil
}

// VerifyFormat4Store checks a closed format-4 store (VerifyAPI on its engine)
// and returns the problems; an engine that cannot open is one.
func VerifyFormat4Store(store string) []string {
	ctx := context.Background()
	e, err := openFormat4(ctx, store)
	if err != nil {
		return []string{"format 4 verification: open: " + err.Error()}
	}
	defer e.Close(ctx)
	probs, err := VerifyAPI(ctx, e)
	if err != nil {
		return append(probs, "format 4 verification: "+err.Error())
	}
	return probs
}

// QuotaByArrival checks a format-4 quota GC (W10; contract C-32: quota
// deletes each type's oldest records by arrival). In every type the records
// kept must be exactly the records the store held before the GC from the
// lowest kept seq up. before is an untouched clone of the store the GC ran
// on (after); both are closed and opened one at a time. It returns the
// problems.
func QuotaByArrival(before, after string, schemas []string) ([]string, error) {
	ctx := context.Background()
	e, err := openFormat4(ctx, after)
	if err != nil {
		return nil, err
	}
	kept, err := keptByArrival(ctx, e, schemas)
	if cerr := e.Close(ctx); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	b, err := openFormat4(ctx, before)
	if err != nil {
		return nil, err
	}
	defer b.Close(ctx)
	return checkArrival(ctx, b, schemas, kept)
}

// keptSpan is what a type holds after a quota GC: its records and lowest seq.
type keptSpan struct{ N, From int64 }

// keptByArrival reads each type's keptSpan from the store after the GC.
func keptByArrival(ctx context.Context, api format4.API, schemas []string) (map[string]keptSpan, error) {
	out := map[string]keptSpan{}
	for _, schema := range schemas {
		typ, err := format4.TypeOf(schema)
		if err != nil {
			return nil, err
		}
		h, err := api.Head(ctx, format4.Query{Type: typ})
		if err != nil {
			return nil, fmt.Errorf("%s: head after the GC: %w", schema, err)
		}
		k := keptSpan{N: h.N}
		if h.N > 0 {
			first, err := api.Scan(ctx, format4.Query{Type: typ, Order: format4.OrderSeqAsc, Limit: 1})
			if err != nil || len(first) == 0 {
				return nil, fmt.Errorf("%s: the oldest record kept: %v", schema, err)
			}
			k.From = first[0].Seq
		}
		out[schema] = k
	}
	return out, nil
}

// checkArrival compares the spans kept with the store before the GC: from
// each type's lowest kept seq up, the store held exactly the records kept.
func checkArrival(ctx context.Context, before format4.API, schemas []string, kept map[string]keptSpan) ([]string, error) {
	var out []string
	for _, schema := range schemas {
		k := kept[schema]
		if k.N == 0 {
			continue // every record went: oldest first trivially
		}
		typ, err := format4.TypeOf(schema)
		if err != nil {
			return nil, err
		}
		h, err := before.Head(ctx, format4.Query{Type: typ, SeqAfter: k.From - 1})
		if err != nil {
			return nil, fmt.Errorf("%s: head before the GC: %w", schema, err)
		}
		if h.N != k.N {
			out = append(out, fmt.Sprintf("%s: the GC kept %d records from seq %d, the store held %d from there: not oldest first by arrival",
				schema, k.N, k.From, h.N))
		}
	}
	return out, nil
}
