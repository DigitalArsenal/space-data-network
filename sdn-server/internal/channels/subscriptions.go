package channels

import (
	"sync"
	"time"
)

type SubscriptionState struct {
	ChannelID       string
	Subscribed      bool
	Visibility      string
	GrantState      string
	EncryptionState string
	UpdatedAt       time.Time
}

type SubscriptionRegistry struct {
	mu     sync.RWMutex
	states map[string]SubscriptionState
}

func NewSubscriptionRegistry() *SubscriptionRegistry {
	return &SubscriptionRegistry{
		states: make(map[string]SubscriptionState),
	}
}

func (r *SubscriptionRegistry) Subscribe(channel ChannelID) SubscriptionState {
	return r.set(channel, true)
}

func (r *SubscriptionRegistry) Unsubscribe(channel ChannelID) SubscriptionState {
	return r.set(channel, false)
}

func (r *SubscriptionRegistry) Get(channel ChannelID) SubscriptionState {
	if r == nil {
		return DefaultSubscriptionState(channel)
	}
	r.mu.RLock()
	state, ok := r.states[channel.ChannelID]
	r.mu.RUnlock()
	if !ok {
		return DefaultSubscriptionState(channel)
	}
	return state
}

func (r *SubscriptionRegistry) set(channel ChannelID, subscribed bool) SubscriptionState {
	state := DefaultSubscriptionState(channel)
	state.Subscribed = subscribed
	state.UpdatedAt = time.Now().UTC()
	if r == nil {
		return state
	}
	r.mu.Lock()
	r.states[channel.ChannelID] = state
	r.mu.Unlock()
	return state
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
