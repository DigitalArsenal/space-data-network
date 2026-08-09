package update

// ROLLBACK RETENTION: the box keeps the last FIVE builds, not one.
//
// OWNER RULING 2026-08-09, verbatim: "We should be building locally and then
// pushing an update signal to all installs to upgrade in place, and only save
// the last five binaries for rollback purposes. That's the point of the update
// server." This SUPERSEDES the owner ruling of 2026-07-30 that permitted
// exactly one rollback binary per box.
//
// WHY THE OLD RULE EXISTED AND WHY FIVE IS BETTER. One slot was a DISK rule: an
// SDN bundle is ~56 MB of binary inside a ~20 MB compressed artifact, boxes were
// at 87% full, and unbounded rollback dirs were being pruned by hand every few
// days (twelve stale dirs in one 2026-08-08 sweep). But one slot is also a
// SINGLE POINT OF NO RETURN, and that cost was paid repeatedly: on 2026-08-09
// host-01 rolled eleven distinct binaries in one day, and twice the reverse
// target for a live build did not exist at all because the previous roll had
// consumed the only slot. Worse, the one slot could not answer the question
// operators actually asked — "which of today's builds was the last one that was
// GOOD?" — because by construction it only ever held the most recent one.
//
// Five slots costs ~280 MB and answers that question. It is also the smallest
// number that spans a bad day: the reconciliation census recorded four to five
// distinct binaries per agent-lane per day on the busiest box.
//
// WHAT THIS FILE IS NOT. It is not unbounded accumulation with a nicer name. The
// pruning here is MANDATORY and runs on every apply — before this change nothing
// in the codebase ever deleted a rollback directory, which is why they had to be
// swept by hand. Growth is now bounded by policy instead of by whoever noticed.
//
// NAMING TRAP, preserved deliberately. A rollback directory is named after the
// update that DISPLACED its contents, not the update whose payload it holds:
// Apply computes rollbackDir = updates/rollback/<incoming update id> and moves
// the OUTGOING payload into it. That is how every state.json on the fleet
// already reads (host-01: previous.update_id 6c536b3b, rollback_path .../197cd28a),
// so renaming it would orphan every existing reverse target on every box. The
// slot record therefore carries BOTH: UpdateID/Version identify the build you
// get back, Path is where it physically lives.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RollbackSlotLimit is the number of previous builds a box retains (owner
// ruling 2026-08-09). Slots beyond this are pruned by Apply, newest kept.
const RollbackSlotLimit = 5

// StateSlot is one retained reverse target.
//
// It is a superset of StatePrevious, which it does not replace: Previous stays
// in state.json as the back-compat mirror of Slots[0] so a box that is rolled
// BACK onto a pre-2026-08-09 binary still finds a reverse target in the shape
// that binary understands. A one-way state file would make the retention change
// itself irreversible, which is precisely the property this whole lane exists
// to avoid.
type StateSlot struct {
	Sequence   int64  `json:"sequence"`
	UpdateID   string `json:"update_id,omitempty"`
	Version    string `json:"version,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Path       string `json:"rollback_path,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
}

// Empty reports a slot that names no reverse target.
//
// The test is the PATH and only the path. The first apply on a never-updated
// bundle displaces a payload with no recorded identity — sequence 0, no update
// id — but that payload is a perfectly good reverse target: it is the bundle
// that shipped with the box. Judging emptiness by identity would discard
// exactly that slot, leaving the one install most likely to need reversing with
// nothing to reverse to.
func (s StateSlot) Empty() bool { return strings.TrimSpace(s.Path) == "" }

// Describe names a slot for a human, including the pre-lane case where the
// displaced payload has no recorded identity.
func (s StateSlot) Describe() string {
	id := strings.TrimSpace(s.UpdateID)
	version := strings.TrimSpace(s.Version)
	switch {
	case id != "" && version != "":
		return fmt.Sprintf("%s (%s)", id, version)
	case id != "":
		return id
	case version != "":
		return version
	default:
		return "the payload this bundle shipped with (no recorded update id) at " + s.Path
	}
}

// recordRollbackSlot returns the new retention list: slot first (it is the
// immediately-previous build and therefore the default reverse target),
// followed by the existing slots, deduplicated by path, capped at
// RollbackSlotLimit.
//
// Dedupe is by PATH, not by update id, because the path is what actually gets
// deleted. Two records pointing at one directory would let a prune remove a
// directory another record still claims — the exact shape of the "previous that
// can no longer be fetched" defect this lane is closing.
func recordRollbackSlot(previous []StateSlot, slot StateSlot) []StateSlot {
	out := make([]StateSlot, 0, len(previous)+1)
	seen := make(map[string]bool, len(previous)+1)
	add := func(s StateSlot) {
		if s.Empty() {
			return
		}
		key := filepath.Clean(s.Path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	add(slot)
	for _, s := range previous {
		add(s)
	}
	if len(out) > RollbackSlotLimit {
		out = out[:RollbackSlotLimit]
	}
	return out
}

// migrateSlots reconstructs a retention list for a state file written before
// this field existed. A pre-2026-08-09 box has exactly one reverse target, in
// Previous; promoting it means the first apply after the upgrade does not
// silently start from an empty inventory and prune the one slot the box has.
func migrateSlots(state *State) []StateSlot {
	if state == nil {
		return nil
	}
	if len(state.Slots) > 0 {
		return state.Slots
	}
	if state.Previous == nil {
		return nil
	}
	slot := StateSlot{
		Sequence: state.Previous.Sequence,
		UpdateID: state.Previous.UpdateID,
		Version:  state.Previous.Version,
		Channel:  state.Previous.Channel,
		Path:     state.Previous.Rollback,
	}
	if slot.Empty() {
		return nil
	}
	return []StateSlot{slot}
}

// SlotInventory is what a box holds, reconciled against what is actually on
// disk. This is the value a feed-retention policy must consult before reaping
// anything (see graph task ops-update-feed-unbounded-growth): a published
// artifact that any box still names here is a live reverse target.
type SlotInventory struct {
	// Current is the running build.
	Current StateSlot `json:"current"`
	// Slots are the retained reverse targets, newest first. Slots[0] is what
	// an undirected rollback restores.
	Slots []StateSlot `json:"slots"`
	// Missing are recorded slots whose directory is gone — a recorded reverse
	// target that cannot actually be reversed to. Reported, never silently
	// dropped: a slot that vanished is evidence of an out-of-band deletion.
	Missing []StateSlot `json:"missing,omitempty"`
	// Unmanaged are directories under updates/rollback/ that no slot record
	// claims. Before this change every rollback dir was unmanaged, which is why
	// they accumulated; they are listed so a prune is explainable rather than
	// surprising.
	Unmanaged []string `json:"unmanaged,omitempty"`
	Limit     int      `json:"limit"`
}

// Inventory reports the box's rollback holdings, reconciled against disk.
func Inventory(paths Paths) (*SlotInventory, error) {
	state, err := LoadState(paths)
	if err != nil {
		return nil, err
	}
	inv := &SlotInventory{
		Current: StateSlot{
			Sequence:   state.Sequence,
			UpdateID:   state.UpdateID,
			Version:    state.Version,
			Channel:    state.Channel,
			Path:       paths.Root,
			RecordedAt: state.AppliedAt,
		},
		Limit: RollbackSlotLimit,
	}
	claimed := make(map[string]bool)
	for _, slot := range migrateSlots(state) {
		claimed[filepath.Clean(slot.Path)] = true
		if info, err := os.Stat(slot.Path); err != nil || !info.IsDir() {
			inv.Missing = append(inv.Missing, slot)
			continue
		}
		inv.Slots = append(inv.Slots, slot)
	}

	entries, err := os.ReadDir(paths.Rollback)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read rollback directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		full := filepath.Clean(filepath.Join(paths.Rollback, entry.Name()))
		if claimed[full] {
			continue
		}
		inv.Unmanaged = append(inv.Unmanaged, full)
	}
	sort.Strings(inv.Unmanaged)
	return inv, nil
}

// planRollbackPrune lists the directories under paths.Rollback that the
// retention policy no longer keeps: everything not named by keep.
//
// It deliberately scopes itself to paths.Rollback and refuses to return
// anything outside it. The list is computed BEFORE anything is deleted so it
// can be written to the deploy ledger first — an unrecordable deletion does not
// happen, exactly as an unrecordable apply does not (see deployledger.go).
func planRollbackPrune(paths Paths, keep []StateSlot) ([]string, error) {
	entries, err := os.ReadDir(paths.Rollback)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read rollback directory: %w", err)
	}
	kept := make(map[string]bool, len(keep))
	for _, slot := range keep {
		kept[filepath.Clean(slot.Path)] = true
	}
	root := filepath.Clean(paths.Rollback)
	var plan []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		full := filepath.Clean(filepath.Join(root, entry.Name()))
		if kept[full] {
			continue
		}
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			// Unreachable through ReadDir, asserted anyway: this list is fed
			// straight to RemoveAll.
			return nil, fmt.Errorf("refusing to prune outside the rollback root: %s", full)
		}
		plan = append(plan, full)
	}
	sort.Strings(plan)
	return plan, nil
}

// applyRollbackPrune deletes the planned directories, returning the ones it
// actually removed. A failure to remove one is reported but does not abort the
// rest: a stuck directory is a disk-space problem, never a correctness one.
func applyRollbackPrune(plan []string) (removed []string, errs []error) {
	for _, dir := range plan {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("prune rollback slot %s: %w", dir, err))
			continue
		}
		removed = append(removed, dir)
	}
	return removed, errs
}

// selectSlot resolves the rollback target.
//
// The default (empty selector) is the immediately-previous verified build —
// slots[0] — because that is the only reverse target whose behaviour is known:
// it was running on this box minutes ago. An older slot is reachable ONLY by
// naming it, and that asymmetry is deliberate. A rollback that skips
// generations silently reverts every lane that landed in between; the
// 2026-08-09 reconciliation nearly did exactly that (keeping a slot two
// generations old would have reverted four landed lanes and restored a
// ~0.9 s store read floor). Naming the slot is the operator saying they mean it.
func selectSlot(slots []StateSlot, selector string) (*StateSlot, error) {
	selector = strings.TrimSpace(selector)
	if len(slots) == 0 {
		return nil, fmt.Errorf("no rollback slot is available (retention limit %d, inventory empty)", RollbackSlotLimit)
	}
	if selector == "" {
		slot := slots[0]
		return &slot, nil
	}
	for i := range slots {
		if slots[i].UpdateID == selector || slots[i].Version == selector || filepath.Clean(slots[i].Path) == filepath.Clean(selector) {
			slot := slots[i]
			return &slot, nil
		}
	}
	available := make([]string, 0, len(slots))
	for _, slot := range slots {
		available = append(available, slot.Describe())
	}
	return nil, fmt.Errorf("no rollback slot matches %q; the box holds: %s", selector, strings.Join(available, ", "))
}

// dropSlot removes the consumed slot from the retention list. Rollback
// physically consumes the directory (the payload is moved back into the bundle
// root), so the record must go with it or the next inventory reports a reverse
// target that no longer exists.
func dropSlot(slots []StateSlot, consumed StateSlot) []StateSlot {
	out := make([]StateSlot, 0, len(slots))
	target := filepath.Clean(consumed.Path)
	for _, slot := range slots {
		if filepath.Clean(slot.Path) == target {
			continue
		}
		out = append(out, slot)
	}
	return out
}

func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
