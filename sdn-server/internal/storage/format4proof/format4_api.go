package format4proof

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// The format-4 side of the digests and the crash checks, through the
// engine's own API (contract §5.2): format4.Engine on a store, or
// format4test.Fake in the unit tests.

// IntegrityNote says how integrity_check is covered: the engine's REBUILD
// verify (what=8) compares the derived state with the files; integrity_check
// on every file has been requested of the engine as part of it (contract
// §4), and is not otherwise checkable without a Go-side SQLite.
const IntegrityNote = "integrity_check: through REBUILD verify only (requested of the engine, contract §4)"

const digestPage = 5000

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

// VerifyAPI runs REBUILD verify (what=8) over every type: the type index,
// object directory, file and lane counters rebuilt from the partition files
// must equal the live ones. It returns the problems.
func VerifyAPI(ctx context.Context, api format4.API) ([]string, error) {
	rows, err := api.Rebuild(ctx, "", format4.RebuildVerify)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if r.Mismatches != 0 {
			out = append(out, fmt.Sprintf("%s: REBUILD verify: %d mismatches over %d entries", r.Type, r.Mismatches, r.Entries))
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
