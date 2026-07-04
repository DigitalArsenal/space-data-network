package flatsqlrt

// Direct-linkage support (loop C.7): linked flow artifacts call the engine's
// exports in-wasm (their VM borrows the live named instance), so the Go host
// never sees the query. What the host still owns is (a) SERIALIZATION — the
// engine is SQLITE_THREADSAFE=0, so a linked dispatch must hold the same
// module lock every flatsqlrt call takes — and (b) BODY-REFERENCE
// resolution: a linked query's result stays materialized in ENGINE memory;
// the flow forwards {generation, ptr, size, fnv1a64} and the host resolves
// the bytes for the HTTP egress INSIDE the same locked section (the
// generation cannot move, so the pointer is valid by construction).
//
// Resolution keeps the loop C.5c zero-copy warm path: bodies are mirrored
// host-side keyed by (generation, fnv1a64, size). A warm identical query —
// the engine serves the same cached artifact, the flow reuses its cached
// fnv — hits the mirror with ZERO engine execution and ZERO copies; a miss
// costs the single engine→host copy, verified against the descriptor's
// canonical hash before it is served or cached.

import (
	"fmt"
	"strconv"
)

// EngineLinkHarvest is the capability handed to the function run inside
// WithLinkedDrain: resolve engine body-references while the engine lock is
// held.
type EngineLinkHarvest struct {
	d *Database
}

// WithLinkedDrain runs fn while holding the engine module lock. fn typically
// executes a linked flow's in-wasm drain (direct engine calls) and then
// resolves any engine body-references it minted. Nothing else may touch the
// engine for the duration — exactly the discipline every flatsqlrt call
// follows.
func (d *Database) WithLinkedDrain(fn func(h *EngineLinkHarvest) error) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	return fn(&EngineLinkHarvest{d: d})
}

func engineRefKey(generation uint64, fnv uint64, size uint32) string {
	return "engine-ref\x1f" + strconv.FormatUint(generation, 10) + "\x1f" +
		strconv.FormatUint(fnv, 16) + "\x1f" + strconv.FormatUint(uint64(size), 10)
}

// ResolveRef resolves one engine body-reference to host bytes. Mirror hit =
// zero copies; miss = one engine→host copy taken under the held lock, with
// the descriptor's generation checked against the engine's CURRENT
// generation and the copied bytes verified against the canonical
// word-folded FNV-1a 64 before being cached or served — a stale or corrupt
// reference can never produce wrong bytes, only an error.
func (h *EngineLinkHarvest) ResolveRef(generation uint64, ptr, size uint32, fnv uint64, frames int) (*RawStream, error) {
	d := h.d
	key := engineRefKey(generation, fnv, size)
	if d.engineRefMirror != nil {
		if cached := d.engineRefMirror.get(key); cached != nil {
			hit := *cached
			hit.MirrorHit = true
			return &hit, nil
		}
	}

	current, err := d.queryCacheGenerationLocked()
	if err != nil {
		return nil, err
	}
	if current != generation {
		return nil, fmt.Errorf("flatsqlrt: engine body-ref expired (generation %d, engine at %d)", generation, current)
	}

	var bytes []byte
	if size > 0 {
		bytes, err = d.rt.mod.ReadMemory(ptr, size)
		if err != nil {
			return nil, err
		}
	} else {
		bytes = []byte{}
	}
	if got := FNV1a64WordFolded(bytes); got != fnv {
		return nil, fmt.Errorf("flatsqlrt: engine body-ref bytes fail fnv1a64 verification (%016x != %016x)", got, fnv)
	}

	stream := &RawStream{
		Bytes:      bytes,
		FNV1a64:    fnv,
		FrameCount: frames,
	}
	if d.engineRefMirror == nil {
		d.engineRefMirror = newRawStreamMirror()
	}
	d.engineRefMirror.put(key, stream)
	return stream, nil
}
