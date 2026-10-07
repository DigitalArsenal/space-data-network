package channels

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Retention words: what a lane keeps when a new publication lands. The
// config file, the CLI and the subscription file use these words; the $DSS
// wire carries the dssRetention ordinals (ReplaceCurrent=0, ArchiveAll=1,
// KeepAll=2).
//
//   - replace-current: each publication replaces the lane's previous batch,
//     so the node holds one current set (a full replacement);
//   - keep-all: every publication stays in the store; nothing is superseded
//     and nothing is pinned;
//   - archive-all: every publication stays and is pinned.
//
// A lane follows its standard's default until someone chooses a rule for
// it. Owner 2026-10-06: CAT, "a record of the catalog state that is
// continuously updated", is a full replacement on every pull; every other
// standard keeps every pull ("the OMM is multiple").
const (
	RetentionReplaceCurrent = "replace-current"
	RetentionKeepAll        = "keep-all"
	RetentionArchiveAll     = "archive-all"
)

// NodeDefaultKey names the node default in StandardRetention listings: the
// rule of every standard without a default of its own.
const NodeDefaultKey = "*"

// builtinStandardRetention is a standard's default when this node sets none.
// A standard missing here follows the node default.
var builtinStandardRetention = map[string]string{
	"CAT": RetentionReplaceCurrent,
}

// NormalizeRetention maps a retention word — the registry form
// ("replace-current", "keep-all", "archive-all") or the SDS enum name
// ("ReplaceCurrent", "KeepAll", "ArchiveAll"), any case — onto the registry
// word. Anything else, the empty word included, reports false.
func NormalizeRetention(word string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "replace-current", "replacecurrent", "replace_current":
		return RetentionReplaceCurrent, true
	case "keep-all", "keepall", "keep_all":
		return RetentionKeepAll, true
	case "archive-all", "archiveall", "archive_all":
		return RetentionArchiveAll, true
	}
	return "", false
}

// BuiltinRetentionFor is the rule a lane of the standard follows on a node
// that set no defaults: replace-current for CAT, keep-all otherwise.
func BuiltinRetentionFor(standard string) string {
	return defaultForCode(nil, "", RetentionStandardCode(standard))
}

// RetentionStandardCode is the key defaults are kept under: "omm.fbs",
// "$OMM" and "OMM" are all "OMM".
func RetentionStandardCode(schema string) string {
	code := strings.TrimPrefix(strings.TrimSpace(schema), "$")
	if i := strings.IndexByte(code, '.'); i >= 0 {
		code = code[:i]
	}
	return strings.ToUpper(code)
}

type SubscriptionState struct {
	ChannelID       string
	Subscribed      bool
	Visibility      string
	GrantState      string
	EncryptionState string
	// Retention is the rule the lane follows: its own choice, else its
	// standard's default. Always populated by Get.
	Retention string
	// RetentionChosen is true when the lane has a rule of its own.
	RetentionChosen bool
	UpdatedAt       time.Time
}

type SubscriptionRegistry struct {
	mu sync.RWMutex
	// states keeps each lane's own choice in Retention ("" follows the
	// standard's default).
	states map[string]SubscriptionState
	// nodeDefault is the rule of a standard without a default of its own
	// (config subscriptions.default_retention); empty means keep-all.
	nodeDefault string
	// standardDefaults are defaults set on this node, by standard code. They
	// win over builtinStandardRetention.
	standardDefaults map[string]string
	// file is where every change is written once AttachFile ran.
	file *subscriptionFile
}

func NewSubscriptionRegistry() *SubscriptionRegistry {
	return &SubscriptionRegistry{
		states:           make(map[string]SubscriptionState),
		standardDefaults: make(map[string]string),
	}
}

// SetDefaultRetention sets the node default. An unknown or empty word means
// keep-all.
func (r *SubscriptionRegistry) SetDefaultRetention(word string) {
	if r == nil {
		return
	}
	normalized, ok := NormalizeRetention(word)
	if !ok {
		normalized = RetentionKeepAll
	}
	r.mu.Lock()
	r.nodeDefault = normalized
	r.mu.Unlock()
}

// DefaultRetention reports the node default.
func (r *SubscriptionRegistry) DefaultRetention() string {
	if r == nil {
		return RetentionKeepAll
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.nodeDefaultLocked()
}

func (r *SubscriptionRegistry) nodeDefaultLocked() string {
	if r.nodeDefault == "" {
		return RetentionKeepAll
	}
	return r.nodeDefault
}

// DefaultRetentionFor reports the rule a lane of the standard follows when
// it has none of its own: the default set on this node, else the built-in
// one, else the node default.
func (r *SubscriptionRegistry) DefaultRetentionFor(standard string) string {
	if r == nil {
		return defaultForCode(nil, "", RetentionStandardCode(standard))
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return defaultForCode(r.standardDefaults, r.nodeDefaultLocked(), RetentionStandardCode(standard))
}

func defaultForCode(set map[string]string, nodeDefault, code string) string {
	if word := set[code]; word != "" {
		return word
	}
	if word := builtinStandardRetention[code]; word != "" {
		return word
	}
	if nodeDefault == "" {
		return RetentionKeepAll
	}
	return nodeDefault
}

// SetStandardRetention sets one standard's default. The empty word, or the
// default the standard has anyway, removes the node's own setting. An
// unknown word reports false and changes nothing.
func (r *SubscriptionRegistry) SetStandardRetention(standard, word string) (string, bool) {
	if r == nil {
		return "", false
	}
	code := RetentionStandardCode(standard)
	if code == "" || code == NodeDefaultKey {
		return "", false
	}
	normalized := ""
	if strings.TrimSpace(word) != "" {
		var ok bool
		if normalized, ok = NormalizeRetention(word); !ok {
			return "", false
		}
	}
	r.mu.Lock()
	if normalized == "" || normalized == defaultForCode(nil, r.nodeDefaultLocked(), code) {
		delete(r.standardDefaults, code)
	} else {
		r.standardDefaults[code] = normalized
	}
	effective := defaultForCode(r.standardDefaults, r.nodeDefaultLocked(), code)
	r.mu.Unlock()
	r.persist()
	return effective, true
}

// StandardRetention lists the defaults by standard code: every standard
// with a default set on this node or built in, and NodeDefaultKey for the
// rest.
func (r *SubscriptionRegistry) StandardRetention() map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for code := range builtinStandardRetention {
		out[code] = defaultForCode(r.standardDefaults, r.nodeDefaultLocked(), code)
	}
	for code := range r.standardDefaults {
		out[code] = defaultForCode(r.standardDefaults, r.nodeDefaultLocked(), code)
	}
	out[NodeDefaultKey] = r.nodeDefaultLocked()
	return out
}

// standardDefaultsSnapshot is the node's own defaults, for the file.
func (r *SubscriptionRegistry) standardDefaultsSnapshot() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.standardDefaults))
	for code, word := range r.standardDefaults {
		out[code] = word
	}
	return out
}

// Subscribe marks the channel subscribed; its rule is unchanged.
func (r *SubscriptionRegistry) Subscribe(channel ChannelID) SubscriptionState {
	return r.update(channel, func(state *SubscriptionState) { state.Subscribed = true }, nil)
}

// SubscribeWithRetention marks the channel subscribed under the given rule
// (see SetLaneRetention for how the word is read).
func (r *SubscriptionRegistry) SubscribeWithRetention(channel ChannelID, word string) SubscriptionState {
	return r.update(channel, func(state *SubscriptionState) { state.Subscribed = true }, &word)
}

// SetLaneRetention sets one lane's rule and leaves its subscription alone.
// The empty word, or the standard's default, makes the lane follow the
// default again; an unknown word keeps the lane's rule.
func (r *SubscriptionRegistry) SetLaneRetention(channel ChannelID, word string) SubscriptionState {
	return r.update(channel, nil, &word)
}

// Unsubscribe marks the channel unsubscribed; its rule is kept, so a later
// subscribe starts from the last choice.
func (r *SubscriptionRegistry) Unsubscribe(channel ChannelID) SubscriptionState {
	return r.update(channel, func(state *SubscriptionState) { state.Subscribed = false }, nil)
}

// Get reports the channel's state with its effective rule. An unknown
// channel reads unsubscribed, following its standard's default.
func (r *SubscriptionRegistry) Get(channel ChannelID) SubscriptionState {
	if r == nil {
		state := DefaultSubscriptionState(channel)
		state.Retention = defaultForCode(nil, "", RetentionStandardCode(channel.StandardCode))
		return state
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.states[channel.ChannelID]
	if !ok {
		state = DefaultSubscriptionState(channel)
	}
	return r.effectiveLocked(state, channel.StandardCode)
}

func (r *SubscriptionRegistry) effectiveLocked(state SubscriptionState, standard string) SubscriptionState {
	state.RetentionChosen = state.Retention != ""
	if !state.RetentionChosen {
		state.Retention = defaultForCode(r.standardDefaults, r.nodeDefaultLocked(), RetentionStandardCode(standard))
	}
	return state
}

// update applies one change to the channel's entry and writes the file.
// retention, when given, is read as SetLaneRetention describes.
func (r *SubscriptionRegistry) update(channel ChannelID, change func(*SubscriptionState), retention *string) SubscriptionState {
	if r == nil {
		state := DefaultSubscriptionState(channel)
		if change != nil {
			change(&state)
		}
		state.Retention = defaultForCode(nil, "", RetentionStandardCode(channel.StandardCode))
		return state
	}
	r.mu.Lock()
	state, ok := r.states[channel.ChannelID]
	if !ok {
		state = DefaultSubscriptionState(channel)
	}
	if change != nil {
		change(&state)
	}
	if retention != nil {
		code := RetentionStandardCode(channel.StandardCode)
		switch word, known := NormalizeRetention(*retention); {
		case strings.TrimSpace(*retention) == "":
			state.Retention = ""
		case !known:
		case word == defaultForCode(r.standardDefaults, r.nodeDefaultLocked(), code):
			state.Retention = ""
		default:
			state.Retention = word
		}
	}
	state.UpdatedAt = time.Now().UTC()
	r.states[channel.ChannelID] = state
	out := r.effectiveLocked(state, channel.StandardCode)
	r.mu.Unlock()
	r.persist()
	return out
}

// lanesSnapshot lists the lanes the file keeps: subscribed, or with a rule
// of their own. Retention is the lane's own choice ("" follows the default).
func (r *SubscriptionRegistry) lanesSnapshot() []SubscriptionEntry {
	r.mu.RLock()
	out := make([]SubscriptionEntry, 0, len(r.states))
	for id, state := range r.states {
		if !state.Subscribed && state.Retention == "" {
			continue
		}
		out = append(out, SubscriptionEntry{ChannelID: id, Subscribed: state.Subscribed, Retention: state.Retention})
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out
}

func DefaultSubscriptionState(channel ChannelID) SubscriptionState {
	return SubscriptionState{
		ChannelID:       channel.ChannelID,
		Subscribed:      false,
		Visibility:      "public",
		GrantState:      "not-required",
		EncryptionState: "none",
	}
}
