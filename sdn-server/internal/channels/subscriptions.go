package channels

import (
	"strings"
	"sync"
	"time"
)

// Retention words: what a lane subscription keeps when a new publication
// lands. The config file and the subscription registry use these words; the
// $DSS wire carries the dssRetention enum ordinals (ReplaceCurrent=0,
// ArchiveAll=1). ReplaceCurrent is the default: each publication supersedes
// the lane's previous batch, so a subscriber holds one current set.
// ArchiveAll keeps and pins every publication.
const (
	RetentionReplaceCurrent = "replace-current"
	RetentionArchiveAll     = "archive-all"
)

// NormalizeRetention maps a retention word — the config/registry form
// ("replace-current", "archive-all") or the SDS enum name ("ReplaceCurrent",
// "ArchiveAll"), any case — onto the canonical registry word. The empty word
// is the default rule. Unknown words report false.
func NormalizeRetention(word string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "", "replace-current", "replacecurrent", "replace_current":
		return RetentionReplaceCurrent, true
	case "archive-all", "archiveall", "archive_all":
		return RetentionArchiveAll, true
	}
	return "", false
}

type SubscriptionState struct {
	ChannelID       string
	Subscribed      bool
	Visibility      string
	GrantState      string
	EncryptionState string
	// Retention is the lane's rule (RetentionReplaceCurrent or
	// RetentionArchiveAll). Always populated: the registry default when the
	// subscriber never chose one.
	Retention string
	UpdatedAt time.Time
}

type SubscriptionRegistry struct {
	mu     sync.RWMutex
	states map[string]SubscriptionState
	// defaultRetention is the node-wide rule a lane starts with
	// (config subscriptions.default_retention); empty means ReplaceCurrent.
	defaultRetention string
}

func NewSubscriptionRegistry() *SubscriptionRegistry {
	return &SubscriptionRegistry{
		states: make(map[string]SubscriptionState),
	}
}

// SetDefaultRetention sets the rule a lane starts with when the subscriber
// does not choose one. Unknown words fall back to ReplaceCurrent.
func (r *SubscriptionRegistry) SetDefaultRetention(word string) {
	if r == nil {
		return
	}
	normalized, ok := NormalizeRetention(word)
	if !ok {
		normalized = RetentionReplaceCurrent
	}
	r.mu.Lock()
	r.defaultRetention = normalized
	r.mu.Unlock()
}

// DefaultRetention reports the rule a lane starts with.
func (r *SubscriptionRegistry) DefaultRetention() string {
	if r == nil {
		return RetentionReplaceCurrent
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultRetentionLocked()
}

func (r *SubscriptionRegistry) defaultRetentionLocked() string {
	if r.defaultRetention == "" {
		return RetentionReplaceCurrent
	}
	return r.defaultRetention
}

// Subscribe marks the channel subscribed. An existing entry keeps its
// retention rule; a new one starts with the registry default.
func (r *SubscriptionRegistry) Subscribe(channel ChannelID) SubscriptionState {
	return r.set(channel, true, "")
}

// SubscribeWithRetention marks the channel subscribed under the given
// retention word (normalised; an unknown word keeps the existing rule, else
// the registry default). Re-subscribing an already subscribed channel only
// changes its rule.
func (r *SubscriptionRegistry) SubscribeWithRetention(channel ChannelID, word string) SubscriptionState {
	return r.set(channel, true, word)
}

// Unsubscribe marks the channel unsubscribed; the stored retention word is
// kept so a later re-subscribe starts from the operator's last choice.
func (r *SubscriptionRegistry) Unsubscribe(channel ChannelID) SubscriptionState {
	return r.set(channel, false, "")
}

// Get reports the channel's state. An unknown channel reads unsubscribed
// with the registry default retention.
func (r *SubscriptionRegistry) Get(channel ChannelID) SubscriptionState {
	if r == nil {
		return DefaultSubscriptionState(channel)
	}
	r.mu.RLock()
	state, ok := r.states[channel.ChannelID]
	fallback := r.defaultRetentionLocked()
	r.mu.RUnlock()
	if !ok {
		state = DefaultSubscriptionState(channel)
		state.Retention = fallback
		return state
	}
	if state.Retention == "" {
		state.Retention = fallback
	}
	return state
}

// set rebuilds the channel's entry. retention is an explicit word ("" means
// keep the existing rule, else the registry default).
func (r *SubscriptionRegistry) set(channel ChannelID, subscribed bool, retention string) SubscriptionState {
	state := DefaultSubscriptionState(channel)
	state.Subscribed = subscribed
	state.UpdatedAt = time.Now().UTC()
	explicit := ""
	if strings.TrimSpace(retention) != "" {
		if word, ok := NormalizeRetention(retention); ok {
			explicit = word
		}
	}
	if r == nil {
		if explicit != "" {
			state.Retention = explicit
		}
		return state
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case explicit != "":
		state.Retention = explicit
	default:
		if existing, ok := r.states[channel.ChannelID]; ok && existing.Retention != "" {
			state.Retention = existing.Retention
		} else {
			state.Retention = r.defaultRetentionLocked()
		}
	}
	r.states[channel.ChannelID] = state
	return state
}

func DefaultSubscriptionState(channel ChannelID) SubscriptionState {
	return SubscriptionState{
		ChannelID:       channel.ChannelID,
		Subscribed:      false,
		Visibility:      "public",
		GrantState:      "not-required",
		EncryptionState: "none",
		Retention:       RetentionReplaceCurrent,
	}
}
