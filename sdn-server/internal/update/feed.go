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

	// BundleHash/BundleSize/WasmHash/WasmSize mirror the signed manifest's own
	// integrity fields at the INDEX level. They are advisory and optional: the
	// signed manifest is, and remains, the only authority — an index is
	// unsigned and a client that trusted it would have gained nothing.
	//
	// What they buy is divergence detection. The index and the manifest are
	// written by the same publisher at the same moment and must agree; if they
	// do not, something rewrote one of them, and the install stops instead of
	// quietly preferring whichever the code happened to read
	// (AssertMatchesPayload). Fields the feed omits are simply not checked, so
	// feeds published before this existed keep working unchanged.
	BundleHash string `json:"bundle_hash,omitempty"`
	BundleSize int64  `json:"bundle_size,omitempty"`
	WasmHash   string `json:"wasm_hash,omitempty"`
	WasmSize   int64  `json:"wasm_size,omitempty"`
}

// AssertMatchesPayload verifies that the signed manifest and carrier actually
// downloaded are the ones this index entry pointed at.
//
// Written after the 2026-08-09 truncated publish
// (graph: sdn-publish-fleet-update-wraps-an-unverified-binary), where a
// corrupt artifact was signed, indexed and served as the newest sequence. That
// one was self-consistent, so this check would not have caught it — the
// producer-side gate does that. This closes the neighbouring hole: an index
// entry and the manifest it resolves to disagreeing about which artifact is
// being installed.
//
// Only fields the entry actually declares are enforced.
func (u ProviderFeedUpdate) AssertMatchesPayload(m *Manifest, carrierLen int) error {
	if m == nil {
		return errors.New("update provider feed entry has no manifest to check against")
	}
	if u.UpdateID != "" && m.UpdateID != u.UpdateID {
		return fmt.Errorf("update provider feed entry %s resolves to a manifest for %s", u.UpdateID, m.UpdateID)
	}
	if u.Version != "" && m.Version != u.Version {
		return fmt.Errorf("update provider feed entry %s: index version %s != manifest version %s", u.UpdateID, u.Version, m.Version)
	}
	if u.Sequence != 0 {
		if m.Sequence == nil {
			return fmt.Errorf("update provider feed entry %s: manifest sequence is missing", u.UpdateID)
		}
		if *m.Sequence != u.Sequence {
			return fmt.Errorf("update provider feed entry %s: index sequence %d != manifest sequence %d", u.UpdateID, u.Sequence, *m.Sequence)
		}
	}
	if u.Channel != "" && !strings.EqualFold(u.Channel, m.Channel) {
		return fmt.Errorf("update provider feed entry %s: index channel %s != manifest channel %s", u.UpdateID, u.Channel, m.Channel)
	}
	if u.Target.Platform != "" && !platformMatches(u.Target.Platform, m.Target.Platform) {
		return fmt.Errorf("update provider feed entry %s: index platform %s != manifest platform %s", u.UpdateID, u.Target.Platform, m.Target.Platform)
	}
	if u.Target.Arch != "" && !archMatches(u.Target.Arch, m.Target.Arch) {
		return fmt.Errorf("update provider feed entry %s: index architecture %s != manifest architecture %s", u.UpdateID, u.Target.Arch, m.Target.Arch)
	}
	if u.Target.Kind != "" && u.Target.Kind != m.Target.Kind {
		return fmt.Errorf("update provider feed entry %s: index kind %s != manifest kind %s", u.UpdateID, u.Target.Kind, m.Target.Kind)
	}
	if u.BundleHash != "" && !strings.EqualFold(u.BundleHash, m.Bundle.Hash) {
		return fmt.Errorf("update provider feed entry %s: index bundle hash %s != manifest bundle hash %s", u.UpdateID, u.BundleHash, m.Bundle.Hash)
	}
	if u.BundleSize != 0 && m.Bundle.Size != u.BundleSize {
		return fmt.Errorf("update provider feed entry %s: index bundle size %d != manifest bundle size %d", u.UpdateID, u.BundleSize, m.Bundle.Size)
	}
	if u.WasmHash != "" && !strings.EqualFold(u.WasmHash, m.Wasm.Hash) {
		return fmt.Errorf("update provider feed entry %s: index wasm hash %s != manifest wasm hash %s", u.UpdateID, u.WasmHash, m.Wasm.Hash)
	}
	if u.WasmSize != 0 && carrierLen != 0 && int64(carrierLen) != u.WasmSize {
		return fmt.Errorf("update provider feed entry %s: index wasm size %d != downloaded carrier size %d", u.UpdateID, u.WasmSize, carrierLen)
	}
	return nil
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
