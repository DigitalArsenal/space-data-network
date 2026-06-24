package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const ProviderFeedSchema = "org.spacedatanetwork.update.index.v1"

type ProviderFeed struct {
	Schema      string               `json:"schema"`
	GeneratedAt string               `json:"generated_at"`
	FeedBaseURL string               `json:"feed_base_url"`
	Updates     []ProviderFeedUpdate `json:"updates"`
}

type ProviderFeedUpdate struct {
	UpdateID    string         `json:"update_id"`
	Version     string         `json:"version"`
	Sequence    int64          `json:"sequence"`
	Channel     string         `json:"channel"`
	Target      ManifestTarget `json:"target"`
	ExpiresAt   string         `json:"expires_at,omitempty"`
	ManifestURL string         `json:"manifest_url"`
	CarrierURL  string         `json:"carrier_url"`
}

type ProviderFeedSelection struct {
	UpdateID        string
	Version         string
	Channel         string
	Platform        string
	Arch            string
	Kind            string
	CurrentSequence int64
}

func ParseProviderFeed(raw []byte) (*ProviderFeed, error) {
	var feed ProviderFeed
	if err := json.Unmarshal(raw, &feed); err != nil {
		return nil, fmt.Errorf("parse update provider feed: %w", err)
	}
	if feed.Schema != ProviderFeedSchema {
		return nil, fmt.Errorf("unsupported update provider feed schema: %s", feed.Schema)
	}
	if err := requireHTTPSURL(feed.FeedBaseURL, "feed_base_url"); err != nil {
		return nil, err
	}
	if len(feed.Updates) == 0 {
		return nil, errors.New("update provider feed has no updates")
	}
	for i := range feed.Updates {
		if err := feed.Updates[i].validate(); err != nil {
			return nil, err
		}
	}
	return &feed, nil
}

func (u ProviderFeedUpdate) validate() error {
	if strings.TrimSpace(u.UpdateID) == "" {
		return errors.New("update provider feed entry missing update_id")
	}
	if strings.TrimSpace(u.Version) == "" {
		return fmt.Errorf("update provider feed entry %s missing version", u.UpdateID)
	}
	if u.Sequence == 0 {
		return fmt.Errorf("update provider feed entry %s missing sequence", u.UpdateID)
	}
	if strings.TrimSpace(u.Channel) == "" {
		return fmt.Errorf("update provider feed entry %s missing channel", u.UpdateID)
	}
	if strings.TrimSpace(u.Target.Platform) == "" || strings.TrimSpace(u.Target.Arch) == "" || strings.TrimSpace(u.Target.Kind) == "" {
		return fmt.Errorf("update provider feed entry %s missing target", u.UpdateID)
	}
	if err := requireHTTPSURL(u.ManifestURL, "manifest_url"); err != nil {
		return fmt.Errorf("update provider feed entry %s: %w", u.UpdateID, err)
	}
	if err := requireHTTPSURL(u.CarrierURL, "carrier_url"); err != nil {
		return fmt.Errorf("update provider feed entry %s: %w", u.UpdateID, err)
	}
	return nil
}

func requireHTTPSURL(raw string, name string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%s must use HTTPS", name)
	}
	return nil
}

func (f *ProviderFeed) Select(selection ProviderFeedSelection) (*ProviderFeedUpdate, error) {
	if f == nil {
		return nil, errors.New("missing update provider feed")
	}
	var candidates []ProviderFeedUpdate
	for _, update := range f.Updates {
		if !providerUpdateMatches(update, selection) {
			continue
		}
		candidates = append(candidates, update)
	}
	if len(candidates) == 0 {
		return nil, errors.New("no compatible update is available from provider feed")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Sequence != candidates[j].Sequence {
			return candidates[i].Sequence > candidates[j].Sequence
		}
		return candidates[i].UpdateID < candidates[j].UpdateID
	})
	selected := candidates[0]
	return &selected, nil
}

func providerUpdateMatches(update ProviderFeedUpdate, selection ProviderFeedSelection) bool {
	if selection.UpdateID != "" && update.UpdateID != selection.UpdateID {
		return false
	}
	if selection.Version != "" && update.Version != selection.Version {
		return false
	}
	if selection.Channel != "" && update.Channel != selection.Channel {
		return false
	}
	if selection.Kind != "" && update.Target.Kind != selection.Kind {
		return false
	}
	if selection.Platform != "" && !platformMatches(update.Target.Platform, selection.Platform) {
		return false
	}
	if selection.Arch != "" && !archMatches(update.Target.Arch, selection.Arch) {
		return false
	}
	if update.Sequence <= selection.CurrentSequence {
		return false
	}
	return true
}
