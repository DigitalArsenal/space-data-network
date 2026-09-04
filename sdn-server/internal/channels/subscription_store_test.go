package channels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSubscriptionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "channel-subscriptions.json")

	registry := NewSubscriptionRegistry()
	// Loading a file that does not exist yet is not an error.
	if err := registry.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom missing file: %v", err)
	}
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot of an empty registry = %v", got)
	}

	omm, err := ParseChannelID("space-data-network-02-OMM")
	if err != nil {
		t.Fatalf("parse OMM channel: %v", err)
	}
	spw, err := ParseChannelID("space-data-network-02-SPW")
	if err != nil {
		t.Fatalf("parse SPW channel: %v", err)
	}
	cat, err := ParseChannelID("celestrak-CAT")
	if err != nil {
		t.Fatalf("parse CAT channel: %v", err)
	}
	registry.Subscribe(spw)
	registry.Subscribe(omm)
	registry.Subscribe(cat)
	registry.Unsubscribe(cat)

	if err := registry.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var listed []SubscriptionEntry
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("saved file is not a JSON list of entries: %v (%s)", err, raw)
	}
	if len(listed) != 2 || listed[0].ChannelID != omm.ChannelID || listed[1].ChannelID != spw.ChannelID {
		t.Fatalf("saved list = %v, want sorted [%s %s]", listed, omm.ChannelID, spw.ChannelID)
	}
	for _, entry := range listed {
		if entry.Retention != RetentionReplaceCurrent {
			t.Fatalf("saved retention for %s = %q, want the default %q", entry.ChannelID, entry.Retention, RetentionReplaceCurrent)
		}
	}

	restored := NewSubscriptionRegistry()
	if err := restored.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !restored.Get(omm).Subscribed || !restored.Get(spw).Subscribed {
		t.Fatalf("restored subscriptions = %v / %v, want both subscribed", restored.Get(omm), restored.Get(spw))
	}
	if restored.Get(cat).Subscribed {
		t.Fatalf("CAT was unsubscribed before save but restored as subscribed")
	}
	if got := restored.Snapshot(); len(got) != 2 || got[0] != omm.ChannelID || got[1] != spw.ChannelID {
		t.Fatalf("restored Snapshot = %v", got)
	}

	// A malformed file is reported, never silently ignored.
	if err := os.WriteFile(path, []byte("{not a list"), 0o600); err != nil {
		t.Fatalf("write malformed file: %v", err)
	}
	if err := NewSubscriptionRegistry().LoadFrom(path); err == nil {
		t.Fatalf("LoadFrom accepted a malformed file")
	}
	// A list of numbers is neither shape.
	if err := os.WriteFile(path, []byte("[1, 2]"), 0o600); err != nil {
		t.Fatalf("write numeric list: %v", err)
	}
	if err := NewSubscriptionRegistry().LoadFrom(path); err == nil {
		t.Fatalf("LoadFrom accepted a list of numbers")
	}
}

func TestSubscriptionStoreKeepsTheRetentionRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channel-subscriptions.json")
	omm, err := ParseChannelID("space-data-network-02-OMM")
	if err != nil {
		t.Fatalf("parse OMM channel: %v", err)
	}
	spw, err := ParseChannelID("space-data-network-02-SPW")
	if err != nil {
		t.Fatalf("parse SPW channel: %v", err)
	}
	tbs, err := ParseChannelID("mls-archive-TBS")
	if err != nil {
		t.Fatalf("parse TBS channel: %v", err)
	}

	registry := NewSubscriptionRegistry()
	// An unknown lane reads the registry default.
	if got := registry.Get(omm).Retention; got != RetentionReplaceCurrent {
		t.Fatalf("unknown lane retention = %q, want %q", got, RetentionReplaceCurrent)
	}
	registry.SetDefaultRetention("ArchiveAll")
	if got := registry.DefaultRetention(); got != RetentionArchiveAll {
		t.Fatalf("DefaultRetention after SetDefaultRetention(ArchiveAll) = %q", got)
	}
	if got := registry.Get(omm).Retention; got != RetentionArchiveAll {
		t.Fatalf("unknown lane retention after default change = %q, want %q", got, RetentionArchiveAll)
	}
	registry.SetDefaultRetention("forever")
	if got := registry.DefaultRetention(); got != RetentionReplaceCurrent {
		t.Fatalf("an unknown default word must fall back to %q, got %q", RetentionReplaceCurrent, got)
	}

	// Subscribe carries the chosen rule; a plain Subscribe keeps it; an
	// explicit word changes it; Unsubscribe keeps the stored word.
	if got := registry.SubscribeWithRetention(omm, "archive-all").Retention; got != RetentionArchiveAll {
		t.Fatalf("SubscribeWithRetention(archive-all) = %q", got)
	}
	if got := registry.Subscribe(omm).Retention; got != RetentionArchiveAll {
		t.Fatalf("Subscribe on an archive-all lane reset the rule to %q", got)
	}
	if got := registry.SubscribeWithRetention(omm, "ReplaceCurrent").Retention; got != RetentionReplaceCurrent {
		t.Fatalf("SubscribeWithRetention(ReplaceCurrent) = %q", got)
	}
	registry.SubscribeWithRetention(spw, "archive-all")
	registry.Subscribe(tbs)
	if state := registry.Unsubscribe(spw); state.Subscribed || state.Retention != RetentionArchiveAll {
		t.Fatalf("Unsubscribe = %+v, want unsubscribed with the stored archive-all rule", state)
	}
	registry.Subscribe(spw)

	if err := registry.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var listed []SubscriptionEntry
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("saved file is not a JSON list of entries: %v (%s)", err, raw)
	}
	want := map[string]string{
		tbs.ChannelID: RetentionReplaceCurrent,
		omm.ChannelID: RetentionReplaceCurrent,
		spw.ChannelID: RetentionArchiveAll,
	}
	if len(listed) != len(want) {
		t.Fatalf("saved entries = %v, want %d", listed, len(want))
	}
	for _, entry := range listed {
		if want[entry.ChannelID] != entry.Retention {
			t.Fatalf("saved retention for %s = %q, want %q", entry.ChannelID, entry.Retention, want[entry.ChannelID])
		}
	}

	restored := NewSubscriptionRegistry()
	if err := restored.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := restored.Get(spw); !got.Subscribed || got.Retention != RetentionArchiveAll {
		t.Fatalf("restored SPW = %+v, want subscribed archive-all", got)
	}
	if got := restored.Get(omm); !got.Subscribed || got.Retention != RetentionReplaceCurrent {
		t.Fatalf("restored OMM = %+v, want subscribed replace-current", got)
	}
	entries := restored.Entries()
	if len(entries) != 3 || entries[0].ChannelID != tbs.ChannelID || entries[1].ChannelID != omm.ChannelID || entries[2].ChannelID != spw.ChannelID {
		t.Fatalf("restored Entries = %v, want sorted by id", entries)
	}
}

func TestSubscriptionStoreLoadsTheLegacyIdList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channel-subscriptions.json")
	if err := os.WriteFile(path, []byte("[\n  \"space-data-network-02-OMM\",\n  \"space-data-network-02-SPW\"\n]\n"), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	omm, err := ParseChannelID("space-data-network-02-OMM")
	if err != nil {
		t.Fatalf("parse OMM channel: %v", err)
	}
	spw, err := ParseChannelID("space-data-network-02-SPW")
	if err != nil {
		t.Fatalf("parse SPW channel: %v", err)
	}

	registry := NewSubscriptionRegistry()
	registry.SetDefaultRetention("archive-all")
	if err := registry.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom legacy list: %v", err)
	}
	for _, channel := range []ChannelID{omm, spw} {
		state := registry.Get(channel)
		if !state.Subscribed {
			t.Fatalf("legacy id %s not subscribed after load", channel.ChannelID)
		}
		if state.Retention != RetentionArchiveAll {
			t.Fatalf("legacy id %s retention = %q, want the registry default %q", channel.ChannelID, state.Retention, RetentionArchiveAll)
		}
	}
	// Saving rewrites the file in the current shape.
	if err := registry.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var listed []SubscriptionEntry
	if err := json.Unmarshal(raw, &listed); err != nil || len(listed) != 2 {
		t.Fatalf("rewritten file = %s (err %v), want two entries", raw, err)
	}
}
