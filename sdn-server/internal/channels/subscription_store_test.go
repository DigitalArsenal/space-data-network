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
	var listed []string
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("saved file is not a JSON list: %v (%s)", err, raw)
	}
	if len(listed) != 2 || listed[0] != omm.ChannelID || listed[1] != spw.ChannelID {
		t.Fatalf("saved list = %v, want sorted [%s %s]", listed, omm.ChannelID, spw.ChannelID)
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
}
