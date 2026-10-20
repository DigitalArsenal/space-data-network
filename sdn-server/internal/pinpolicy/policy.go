// Package pinpolicy is the node's pin policy ($PIN) and the only code in the
// node that talks to Kubo's pin surface (block/put, add, pin/add, pin/rm,
// pin/ls).
//
// Pinning is separate from storage. The node's own store keeps and serves its
// records and files by CID whether or not they are pinned; a Kubo pin keeps
// bytes out of IPFS garbage collection and in the pinned reprovide set. By
// default the node pins only its own $EPM and its account $EPMs. Every other
// class is off until an operator turns it on, for the whole class or for one
// feed within it (a feed rule overrides its class rule). The node keeps and
// persists only the rules an operator set: a class without one follows its
// default, so a change of default reaches every node that never chose.
//
// Every add and pin goes through a Scope (one class, one feed). Content the
// policy does not allow is still added (the CID is computed and the blocks
// land in the local blockstore) but not pinned. The ledger records every CID a
// scope added or pinned, so enabling a rule later pins what is already held
// locally, and disabling it unpins exactly what it pinned.
package pinpolicy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	PIN "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIN"
	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("sdn-pinpolicy")

// Class is a $PIN pinClass ordinal.
type Class uint8

// The $PIN pinClass values (schema/PIN/main.fbs).
const (
	NodeEPM Class = iota
	AccountEPM
	Publication
	Share
	FlowAsset
	Tip
	Storefront
	AssetModel
	SiteAsset
	ProfilePhoto
	classCount
)

func init() {
	// The ordinals above must be the generated enum's, one for one.
	if len(PIN.EnumNamespinClass) != int(classCount) {
		panic(fmt.Sprintf("pinpolicy: $PIN has %d classes, this package %d", len(PIN.EnumNamespinClass), classCount))
	}
	for value := range PIN.EnumNamespinClass {
		if byte(value) >= byte(classCount) {
			panic(fmt.Sprintf("pinpolicy: $PIN class %d is unknown here", byte(value)))
		}
	}
}

// Locked reports a class whose rules no client can change.
func (c Class) Locked() bool { return c == NodeEPM || c == AccountEPM }

func (c Class) valid() bool { return c < classCount }

// String is the IDL name of the class.
func (c Class) String() string {
	for value, name := range PIN.EnumNamespinClass {
		if byte(value) == byte(c) {
			return name
		}
	}
	return fmt.Sprintf("pinClass(%d)", uint8(c))
}

// StandardFeed is a standard as a feed: its code ("OMM" for "OMM.fbs").
func StandardFeed(schema string) string {
	return strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(schema), ".fbs"))
}

// PublicationFeed is the PUBLICATION feed of a lane:
// "<standard>/<provider>/<source>", the standard as its code ("OMM").
func PublicationFeed(schema, providerID, sourceName string) string {
	return StandardFeed(schema) + "/" + strings.TrimSpace(providerID) + "/" + strings.TrimSpace(sourceName)
}

// Pin is one ledger row: a CID a scope added or pinned, and whether the
// policy holds a Kubo pin on it for that scope.
type Pin struct {
	Class  Class
	Feed   string
	CID    string
	Bytes  int64
	Pinned bool
	At     time.Time
}

// Ledger is the node store's half of the policy: the persisted $PIN and the
// pin rows.
type Ledger interface {
	// LoadPinPolicy returns the persisted size-prefixed $PIN, nil when none.
	LoadPinPolicy() ([]byte, error)
	SavePinPolicy(frame []byte) error
	UpsertKuboPin(Pin) error
	DeleteKuboPin(class Class, feed, cid string) error
	KuboPins(class Class) ([]Pin, error)
	KuboPinsByCID(cid string) ([]Pin, error)
}

// Errors a route maps onto 400.
var (
	ErrBadFrame = errors.New("the body is not one size-prefixed $PIN")
	ErrLocked   = errors.New("NODE_EPM and ACCOUNT_EPM are always pinned")
)

const (
	reconcilePinTimeout = 30 * time.Second
	reconcileTimeout    = 10 * time.Minute
)

type ruleKey struct {
	class Class
	feed  string
}

// Policy is the node's pin policy. One instance per node.
type Policy struct {
	kubo   *kuboClient
	ledger Ledger

	mu sync.RWMutex
	// rules holds only what an operator set, by class or by feed; a class
	// without a rule follows pinnedByDefault.
	rules     map[ruleKey]bool
	updatedAt uint64

	// applyMu serializes rule changes with their reconcile pass.
	applyMu sync.Mutex
}

// New loads the persisted policy (the default when none). apiURL is the
// node's Kubo RPC API; empty means no Kubo (adds fail, Allowed still
// answers). ledger may be nil (nothing persisted, nothing ledgered).
func New(apiURL string, ledger Ledger) (*Policy, error) {
	p := &Policy{
		kubo:   newKuboClient(apiURL),
		ledger: ledger,
		rules:  map[ruleKey]bool{},
	}
	if ledger == nil {
		return p, nil
	}
	frame, err := ledger.LoadPinPolicy()
	if err != nil {
		return nil, fmt.Errorf("load pin policy: %w", err)
	}
	if len(frame) == 0 {
		return p, nil
	}
	changes, updatedAt, err := decodeRules(frame)
	if err != nil {
		return nil, fmt.Errorf("load pin policy: %w", err)
	}
	for _, change := range changes {
		if change.key.class.Locked() {
			continue
		}
		p.rules[change.key] = change.enabled
	}
	p.updatedAt = updatedAt
	return p, nil
}

// pinnedByDefault reports a class the node pins when no operator rule says
// otherwise.
func (c Class) pinnedByDefault() bool { return c.Locked() }

// Allowed reports whether the policy pins content of class (and feed).
func (p *Policy) Allowed(class Class, feed string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedLocked(class, feed)
}

func (p *Policy) allowedLocked(class Class, feed string) bool {
	if class.Locked() {
		return true
	}
	if feed = strings.TrimSpace(feed); feed != "" {
		if enabled, ok := p.rules[ruleKey{class: class, feed: feed}]; ok {
			return enabled
		}
	}
	if enabled, ok := p.rules[ruleKey{class: class}]; ok {
		return enabled
	}
	return class.pinnedByDefault()
}

// effectiveLocked is the rule that governs content of (class, feed).
func (p *Policy) effectiveLocked(class Class, feed string) ruleKey {
	if feed != "" {
		if _, ok := p.rules[ruleKey{class: class, feed: feed}]; ok {
			return ruleKey{class: class, feed: feed}
		}
	}
	return ruleKey{class: class}
}

// Encode answers the whole policy as one size-prefixed $PIN: one rule per
// class, then every feed rule, each with the roots and bytes it holds pinned.
func (p *Policy) Encode() []byte {
	p.mu.RLock()
	rules := p.rulesLocked()
	updatedAt := p.updatedAt
	p.mu.RUnlock()

	roots := map[ruleKey]uint64{}
	bytes := map[ruleKey]uint64{}
	if p.ledger != nil {
		for c := Class(0); c < classCount; c++ {
			rows, err := p.ledger.KuboPins(c)
			if err != nil {
				log.Warnf("pin policy: ledger of %s unavailable: %v", c, err)
				continue
			}
			p.mu.RLock()
			for _, row := range rows {
				if !row.Pinned {
					continue
				}
				key := p.effectiveLocked(row.Class, row.Feed)
				roots[key]++
				if row.Bytes > 0 {
					bytes[key] += uint64(row.Bytes)
				}
			}
			p.mu.RUnlock()
		}
	}
	for i := range rules {
		key := ruleKey{class: rules[i].Class, feed: rules[i].Feed}
		rules[i].PinnedRoots, rules[i].PinnedBytes = roots[key], bytes[key]
	}
	return encodeFrame(rules, updatedAt)
}

// rulesLocked lists every rule in force: one per class (the operator's, else
// the default), then every feed rule, by class then feed.
func (p *Policy) rulesLocked() []RuleChange {
	rules := make([]RuleChange, 0, int(classCount)+len(p.rules))
	for c := Class(0); c < classCount; c++ {
		rules = append(rules, RuleChange{Class: c, Enabled: p.allowedLocked(c, ""), Locked: c.Locked()})
	}
	for _, rule := range p.operatorRulesLocked() {
		if rule.Feed != "" {
			rules = append(rules, rule)
		}
	}
	return rules
}

// operatorRulesLocked lists the rules an operator set: class rules first,
// then feed rules, each by class then feed.
func (p *Policy) operatorRulesLocked() []RuleChange {
	rules := make([]RuleChange, 0, len(p.rules))
	for key, enabled := range p.rules {
		rules = append(rules, RuleChange{Class: key.class, Feed: key.feed, Enabled: enabled})
	}
	sort.Slice(rules, func(i, j int) bool {
		if (rules[i].Feed == "") != (rules[j].Feed == "") {
			return rules[i].Feed == ""
		}
		if rules[i].Class != rules[j].Class {
			return rules[i].Class < rules[j].Class
		}
		return rules[i].Feed < rules[j].Feed
	})
	return rules
}

// EncodeChanges builds the $PIN a client sends to change rules.
func EncodeChanges(changes ...RuleChange) []byte {
	return encodeFrame(changes, 0)
}

func encodeFrame(rules []RuleChange, updatedAt uint64) []byte {
	b := flatbuffers.NewBuilder(256 + 64*len(rules))
	offsets := make([]flatbuffers.UOffsetT, len(rules))
	for i, rule := range rules {
		var feed flatbuffers.UOffsetT
		if rule.Feed != "" {
			feed = b.CreateString(rule.Feed)
		}
		PIN.PINRuleStart(b)
		// CLASS the way PINRuleAddCLASS writes it: the generated enum type
		// is not exported.
		b.PrependByteSlot(0, byte(rule.Class), 0)
		if feed != 0 {
			PIN.PINRuleAddFEED(b, feed)
		}
		PIN.PINRuleAddENABLED(b, rule.Enabled)
		PIN.PINRuleAddLOCKED(b, rule.Locked)
		PIN.PINRuleAddPINNED_ROOTS(b, rule.PinnedRoots)
		PIN.PINRuleAddPINNED_BYTES(b, rule.PinnedBytes)
		offsets[i] = PIN.PINRuleEnd(b)
	}
	PIN.PINStartRULESVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	vector := b.EndVector(len(offsets))
	PIN.PINStart(b)
	PIN.PINAddRULES(b, vector)
	PIN.PINAddUPDATED_AT(b, updatedAt)
	PIN.FinishSizePrefixedPINBuffer(b, PIN.PINEnd(b))
	return b.FinishedBytes()
}

type ruleChange struct {
	key     ruleKey
	enabled bool
}

// RuleChange is one rule of a change request or of a decoded answer.
type RuleChange struct {
	Class       Class
	Feed        string
	Enabled     bool
	Locked      bool
	PinnedRoots uint64
	PinnedBytes uint64
}

// Decode reads every rule of a size-prefixed $PIN.
func Decode(frame []byte) ([]RuleChange, uint64, error) {
	var out []RuleChange
	var updatedAt uint64
	err := walkRules(frame, func(rule *PIN.PINRule) {
		out = append(out, RuleChange{
			Class:       Class(byte(rule.CLASS())),
			Feed:        strings.TrimSpace(string(rule.FEED())),
			Enabled:     rule.ENABLED(),
			Locked:      rule.LOCKED(),
			PinnedRoots: rule.PINNED_ROOTS(),
			PinnedBytes: rule.PINNED_BYTES(),
		})
	}, &updatedAt)
	return out, updatedAt, err
}

func decodeRules(frame []byte) ([]ruleChange, uint64, error) {
	rules, updatedAt, err := Decode(frame)
	if err != nil {
		return nil, 0, err
	}
	out := make([]ruleChange, 0, len(rules))
	for _, rule := range rules {
		if !rule.Class.valid() {
			return nil, 0, fmt.Errorf("%w: unknown class %d", ErrBadFrame, rule.Class)
		}
		out = append(out, ruleChange{key: ruleKey{class: rule.Class, feed: rule.Feed}, enabled: rule.Enabled})
	}
	return out, updatedAt, nil
}

func walkRules(frame []byte, visit func(*PIN.PINRule), updatedAt *uint64) (err error) {
	if len(frame) < 8+flatbuffers.SizeUint32 || !PIN.SizePrefixedPINBufferHasIdentifier(frame) {
		return ErrBadFrame
	}
	if size := flatbuffers.GetUint32(frame); uint64(size)+flatbuffers.SizeUint32 != uint64(len(frame)) {
		return ErrBadFrame
	}
	defer func() {
		if recover() != nil {
			err = ErrBadFrame
		}
	}()
	root := PIN.GetSizePrefixedRootAsPIN(frame, 0)
	var rule PIN.PINRule
	for i := 0; i < root.RULESLength(); i++ {
		if root.RULES(&rule, i) {
			visit(&rule)
		}
	}
	*updatedAt = root.UPDATED_AT()
	return nil
}

// Apply applies a $PIN of rule changes, persists the policy, pins and unpins
// what the changed rules cover, and answers the whole policy.
func (p *Policy) Apply(ctx context.Context, frame []byte) ([]byte, error) {
	changes, _, err := decodeRules(frame)
	if err != nil {
		return nil, err
	}
	for _, change := range changes {
		if change.key.class.Locked() {
			return nil, ErrLocked
		}
	}
	p.applyMu.Lock()
	defer p.applyMu.Unlock()

	p.mu.Lock()
	touched := map[Class]bool{}
	for _, change := range changes {
		p.rules[change.key] = change.enabled
		touched[change.key.class] = true
	}
	p.updatedAt = uint64(time.Now().UnixMilli())
	persisted := p.persistFrameLocked()
	p.mu.Unlock()

	if p.ledger != nil {
		if err := p.ledger.SavePinPolicy(persisted); err != nil {
			return nil, fmt.Errorf("save pin policy: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	for c := Class(0); c < classCount; c++ {
		if touched[c] {
			p.reconcile(ctx, c)
		}
	}
	return p.Encode(), nil
}

// persistFrameLocked is the policy as stored: the operator's rules only (a
// locked class never has one), no counts.
func (p *Policy) persistFrameLocked() []byte {
	return encodeFrame(p.operatorRulesLocked(), p.updatedAt)
}

// reconcile brings every ledgered CID of class to what the policy now says:
// a CID it allows is pinned from the bytes the node holds (never fetched from
// the network); a CID it no longer allows is unpinned unless another allowed
// row holds it. Garbage collection is never run.
func (p *Policy) reconcile(ctx context.Context, class Class) {
	if p.ledger == nil || p.kubo == nil {
		return
	}
	rows, err := p.ledger.KuboPins(class)
	if err != nil {
		log.Warnf("pin policy: ledger of %s unavailable: %v", class, err)
		return
	}
	pinned, unpinned := 0, 0
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		want := p.Allowed(row.Class, row.Feed)
		switch {
		case want && !row.Pinned:
			pinCtx, cancel := context.WithTimeout(ctx, reconcilePinTimeout)
			err := p.kubo.pinAdd(pinCtx, row.CID, true)
			cancel()
			if err != nil {
				log.Warnf("pin policy: %s %s %s not pinned (not held locally?): %v", class, row.Feed, row.CID, err)
				continue
			}
			row.Pinned, row.At = true, time.Now().UTC()
			if err := p.ledger.UpsertKuboPin(row); err != nil {
				log.Warnf("pin policy: pinned %s but could not ledger it: %v", row.CID, err)
			}
			pinned++
		case !want && row.Pinned:
			if !p.heldElsewhere(row) {
				if err := p.kubo.pinRm(ctx, row.CID); err != nil {
					log.Warnf("pin policy: could not unpin %s: %v", row.CID, err)
					continue
				}
			}
			row.Pinned, row.At = false, time.Now().UTC()
			if err := p.ledger.UpsertKuboPin(row); err != nil {
				log.Warnf("pin policy: unpinned %s but could not ledger it: %v", row.CID, err)
			}
			unpinned++
		}
	}
	if pinned > 0 || unpinned > 0 {
		log.Infof("Pin policy %s: pinned %d, unpinned %d", class, pinned, unpinned)
	}
}

// heldElsewhere reports another pinned row on the same CID whose rule still
// allows it.
func (p *Policy) heldElsewhere(row Pin) bool {
	if p.ledger == nil {
		return false
	}
	others, err := p.ledger.KuboPinsByCID(row.CID)
	if err != nil {
		// Unknown: keep the pin rather than drop another rule's content.
		return true
	}
	for _, other := range others {
		if other.Class == row.Class && other.Feed == row.Feed {
			continue
		}
		if other.Pinned && p.Allowed(other.Class, other.Feed) {
			return true
		}
	}
	return false
}
