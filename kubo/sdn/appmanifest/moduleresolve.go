package appmanifest

// Module resolution — the NODE half of "modules loaded from IPFS".
//
// An $APP record references each member module by APPModuleRef.CONTENT_HASH: 64
// lowercase hex characters of the SHA-256 of the module's portable WASM bytes
// (see schema/APP APPModuleRef.CONTENT_HASH and ModuleRef.ContentHash). The
// bytes themselves are NOT embedded in the record — they live in the
// content-addressed blockstore. This file bridges the two: it maps a
// CONTENT_HASH to the kubo blockstore's CID space and back so a serving node
// can (a) store a module's bytes and advertise their CONTENT_HASH in an $APP,
// and (b) resolve any $APP's module refs back to their exact bytes on demand.
//
// CID mapping (byte-identical to sdnstore.blockCID): a CONTENT_HASH is the
// SHA-256 digest, so the module block is a raw-codec CIDv1 over that same
// SHA2-256 multihash — cid.NewCidV1(cid.Raw, multihash(SHA2_256, digest)).
// StoreModuleBytes derives that CID from mh.Sum over the bytes;
// ContentHashToCID derives the identical CID from the hex digest alone, so a
// hash advertised in an $APP resolves to the very block StoreModuleBytes wrote.
// Every resolve additionally re-hashes the fetched bytes and checks them
// against the requested CONTENT_HASH (verify-by-hash after fetch), so a
// corrupted or substituted block is rejected rather than returned.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	blockstore "github.com/ipfs/boxo/blockstore"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// ModuleBlockstore is the minimal blockstore surface module resolution needs:
// content-addressed Put/Get/Has. kubo's blockstore.Blockstore satisfies it, so
// callers pass the node's blockstore directly; the narrow interface keeps this
// package testable with an in-memory blockstore and free of node/HTTP cruft.
type ModuleBlockstore interface {
	Put(ctx context.Context, b blocks.Block) error
	Get(ctx context.Context, c cid.Cid) (blocks.Block, error)
	Has(ctx context.Context, c cid.Cid) (bool, error)
}

// compile-time assertion that kubo's blockstore satisfies ModuleBlockstore.
var _ ModuleBlockstore = (blockstore.Blockstore)(nil)

// normalizeContentHash validates a CONTENT_HASH and returns its lowercase form
// plus the decoded 32-byte SHA-256 digest. A CONTENT_HASH is exactly 64
// lowercase hex characters per schema/APP; anything else is rejected here
// rather than producing a CID that silently misses.
func normalizeContentHash(contentHash string) (string, []byte, error) {
	h := strings.ToLower(strings.TrimSpace(contentHash))
	if len(h) != 2*sha256.Size {
		return "", nil, fmt.Errorf("appmanifest: content hash must be %d hex chars (sha-256), got %d", 2*sha256.Size, len(h))
	}
	digest, err := hex.DecodeString(h)
	if err != nil {
		return "", nil, fmt.Errorf("appmanifest: content hash is not valid hex: %w", err)
	}
	return h, digest, nil
}

// ContentHashToCID maps an APPModuleRef.CONTENT_HASH (hex SHA-256) to the
// raw-codec CIDv1 its module block is addressed by, without needing the bytes.
// The CID is byte-identical to the one StoreModuleBytes derives from the bytes
// themselves, so a hash advertised in an $APP addresses exactly the stored block.
func ContentHashToCID(contentHash string) (cid.Cid, error) {
	_, digest, err := normalizeContentHash(contentHash)
	if err != nil {
		return cid.Undef, err
	}
	encoded, err := mh.Encode(digest, mh.SHA2_256)
	if err != nil {
		return cid.Undef, fmt.Errorf("appmanifest: encode multihash: %w", err)
	}
	return cid.NewCidV1(cid.Raw, mh.Multihash(encoded)), nil
}

// StoreModuleBytes puts a module's portable WASM bytes into the blockstore as a
// content-addressed raw block and returns the CONTENT_HASH (lowercase hex
// SHA-256) an $APP's APPModuleRef should advertise, along with the block CID.
// It is idempotent: byte-identical modules map to the same CID, so a repeat
// Store is a no-op past the Has check.
func StoreModuleBytes(ctx context.Context, bs ModuleBlockstore, wasm []byte) (contentHash string, c cid.Cid, err error) {
	if bs == nil {
		return "", cid.Undef, errors.New("appmanifest: blockstore is nil")
	}
	if len(wasm) == 0 {
		return "", cid.Undef, errors.New("appmanifest: module bytes must be non-empty")
	}
	sum := sha256.Sum256(wasm)
	contentHash = hex.EncodeToString(sum[:])

	encoded, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		return "", cid.Undef, fmt.Errorf("appmanifest: encode multihash: %w", err)
	}
	c = cid.NewCidV1(cid.Raw, mh.Multihash(encoded))

	has, err := bs.Has(ctx, c)
	if err != nil {
		return "", cid.Undef, fmt.Errorf("appmanifest: blockstore Has: %w", err)
	}
	if !has {
		blk, err := blocks.NewBlockWithCid(wasm, c)
		if err != nil {
			return "", cid.Undef, err
		}
		if err := bs.Put(ctx, blk); err != nil {
			return "", cid.Undef, fmt.Errorf("appmanifest: blockstore Put: %w", err)
		}
	}
	return contentHash, c, nil
}

// ResolveModuleByContentHash fetches a module's bytes from the blockstore by
// its APPModuleRef.CONTENT_HASH. It re-hashes the fetched block and rejects it
// unless the digest matches the requested CONTENT_HASH exactly, so a corrupt or
// substituted block is never returned. The returned slice is a copy the caller
// owns — it never aliases blockstore-owned memory.
func ResolveModuleByContentHash(ctx context.Context, bs ModuleBlockstore, contentHash string) ([]byte, error) {
	if bs == nil {
		return nil, errors.New("appmanifest: blockstore is nil")
	}
	want, _, err := normalizeContentHash(contentHash)
	if err != nil {
		return nil, err
	}
	c, err := ContentHashToCID(want)
	if err != nil {
		return nil, err
	}
	blk, err := bs.Get(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("appmanifest: blockstore Get %s (content hash %s): %w", c, want, err)
	}
	raw := blk.RawData()
	got := sha256.Sum256(raw)
	if gotHex := hex.EncodeToString(got[:]); gotHex != want {
		return nil, fmt.Errorf("appmanifest: fetched block for %s hashes to %s, not the requested content hash %s", c, gotHex, want)
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

// ResolveModuleRef resolves a single member module's bytes from the blockstore
// by its ContentHash. A ModuleRef with no ContentHash cannot be resolved (the
// $APP pinned only PluginID/Version), which is reported as an error.
func ResolveModuleRef(ctx context.Context, bs ModuleBlockstore, ref ModuleRef) ([]byte, error) {
	if strings.TrimSpace(ref.ContentHash) == "" {
		return nil, fmt.Errorf("appmanifest: module ref %q has no content hash to resolve", ref.ID)
	}
	return ResolveModuleByContentHash(ctx, bs, ref.ContentHash)
}

// ResolveAppModules is the node-side of app serving: given an $APP's manifest,
// it resolves every member module that carries a ContentHash to its exact bytes
// from the blockstore, keyed by the app-local ModuleRef.ID. Modules that pin
// only PluginID/Version (no ContentHash) are skipped and reported in the
// returned unresolved list rather than failing the whole app — a caller can
// decide whether the app is still launchable without them. A blockstore miss or
// a hash mismatch on a module that DOES carry a ContentHash is a hard error.
func ResolveAppModules(ctx context.Context, bs ModuleBlockstore, a *AppManifest) (resolved map[string][]byte, unresolved []string, err error) {
	if a == nil {
		return nil, nil, errors.New("app manifest is nil")
	}
	if err := a.Validate(); err != nil {
		return nil, nil, err
	}
	resolved = make(map[string][]byte, len(a.Modules))
	for _, ref := range a.Modules {
		if strings.TrimSpace(ref.ContentHash) == "" {
			unresolved = append(unresolved, ref.ID)
			continue
		}
		bytes, err := ResolveModuleByContentHash(ctx, bs, ref.ContentHash)
		if err != nil {
			return nil, nil, fmt.Errorf("appmanifest: resolve module %q: %w", ref.ID, err)
		}
		resolved[ref.ID] = bytes
	}
	return resolved, unresolved, nil
}
