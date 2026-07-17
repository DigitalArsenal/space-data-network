package OEM

// mutate_helpers.go — hand-authored augmentation (NOT generated). Exposes in-place
// identity mutation on a decoded $OEM so callers OUTSIDE this package can stamp a
// per-object NORAD without a full rebuild. The generated ephemerisDataBlock table
// type is unexported, so the navigation OEM -> block -> OBJECT(CAT) must live here.

// MutateBlockNORAD rewrites NORAD_CAT_ID on ephemeris-data-block j's OBJECT (CAT),
// in place, returning false if the block, its object, or the NORAD field is absent
// (FlatBuffers can only mutate a field that was written with a non-default slot).
// The receiver's backing buffer is modified directly, so operate on a copy when
// synthesizing distinct records from one template.
func (rcv *OEM) MutateBlockNORAD(j int, norad uint32) bool {
	var blk ephemerisDataBlock
	if !rcv.EphemerisDataBlock(&blk, j) {
		return false
	}
	var cat CAT
	if blk.Object(&cat) == nil {
		return false
	}
	return cat.MutateNORAD_CAT_ID(norad)
}

// BlockNORAD reads NORAD_CAT_ID from ephemeris-data-block j's OBJECT (CAT), or
// (0, false) if the block/object is absent.
func (rcv *OEM) BlockNORAD(j int) (uint32, bool) {
	var blk ephemerisDataBlock
	if !rcv.EphemerisDataBlock(&blk, j) {
		return 0, false
	}
	var cat CAT
	if blk.Object(&cat) == nil {
		return 0, false
	}
	return cat.NORAD_CAT_ID(), true
}
