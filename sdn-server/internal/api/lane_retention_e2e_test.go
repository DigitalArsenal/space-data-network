package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// Owner 2026-10-06: CAT is a full replacement on every pull, every other
// standard keeps every pull, and full replacement is a rule anyone can set
// for a standard or a lane. This drives the real sync routes over the real
// store and subscription file, through a restart.
func TestLaneRetentionDefaultsChoicesAndRestart(t *testing.T) {
	const provider = "space-data-network-02"
	store := newConnectorsTestStore(t)
	start := func() (*http.ServeMux, *AdminMountDeps) {
		deps := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForSyncTest", Channels: NewChannelHandler(store)}
		mux, _ := newSyncTestMux(t, deps)
		return mux, deps
	}
	rule := func(mux *http.ServeMux, schema, source string) int8 {
		t.Helper()
		_, frames := syncFrames(t, mux, http.MethodGet, SyncPath+"?schema="+schema, nil)
		return int8(findDSS(t, frames, schema+".fbs", provider, source).RETENTION())
	}
	set := func(mux *http.ServeMux, schema, source string, retention *int8) {
		t.Helper()
		lane := ""
		if source != "" {
			lane = provider
		}
		rec, _ := syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSSetRetention(schema, lane, source, retention))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("SetRetention %s %s: status %d", schema, source, rec.Code)
		}
	}
	defaults := func(mux *http.ServeMux) map[string]int8 {
		t.Helper()
		_, frames := syncFrames(t, mux, http.MethodGet, SyncPath+"/defaults", nil)
		out := map[string]int8{}
		for _, frame := range frames {
			dss := decodeDSSFrame(t, frame)
			out[channels.RetentionStandardCode(string(dss.SCHEMA_NAME()))] = int8(dss.RETENTION())
		}
		return out
	}
	ordinal := func(v int8) *int8 { return &v }

	// A subscription file written before this change: its writer saved the
	// rule it applied, so OMM's replace-current was the old default and now
	// follows the new one, while SPW's archive-all was a choice and stays.
	subsPath := filepath.Join(filepath.Dir(store.ArchiveOutputDir()), SubscriptionRegistryFileName)
	legacy := `[{"channel_id":"space-data-network-02-OMM","retention":"replace-current"},{"channel_id":"space-data-network-02-SPW","retention":"archive-all"}]`
	if err := os.WriteFile(subsPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	mux, deps := start()

	if got := defaults(mux); len(got) != 2 || got["CAT"] != DSSRetentionReplaceCurrent || got[channels.NodeDefaultKey] != DSSRetentionKeepAll {
		t.Fatalf("defaults = %v, want CAT replace-current and * keep-all", got)
	}
	if got := rule(mux, "OMM", "celestrak-gp"); got != DSSRetentionKeepAll {
		t.Fatalf("OMM from the old file reads %d, want KeepAll (it followed the default)", got)
	}
	if got := rule(mux, "SPW", "celestrak-space-weather"); got != DSSRetentionArchiveAll {
		t.Fatalf("SPW from the old file reads %d, want its chosen ArchiveAll", got)
	}
	var file struct {
		Version int                          `json:"version"`
		Lanes   []channels.SubscriptionEntry `json:"lanes"`
	}
	raw, err := os.ReadFile(subsPath)
	if err != nil || json.Unmarshal(raw, &file) != nil || file.Version != 2 || len(file.Lanes) != 2 {
		t.Fatalf("the old file was not rewritten in the current shape: %s (%v)", raw, err)
	}

	// Full replacement chosen for one lane; ReplaceCurrent is the FlatBuffers
	// default, so the request must carry it.
	set(mux, "OMM", "celestrak-gp", ordinal(DSSRetentionReplaceCurrent))
	if got := deps.Channels.LaneRetention("OMM.fbs", provider, "celestrak-gp"); got != channels.RetentionReplaceCurrent {
		t.Fatalf("OMM lane rule = %q after choosing full replacement", got)
	}
	// Subscribe without a rule leaves the rule alone.
	syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM", provider, "celestrak-gp", DSSActionSubscribe))
	if got := rule(mux, "OMM", "celestrak-gp"); got != DSSRetentionReplaceCurrent {
		t.Fatalf("a plain Subscribe changed the OMM rule to %d", got)
	}

	// A standard's default reaches its lanes unless a lane chose otherwise.
	set(mux, "SPW", "", ordinal(DSSRetentionReplaceCurrent))
	if got := rule(mux, "SPW", "celestrak-space-weather"); got != DSSRetentionArchiveAll {
		t.Fatalf("SPW lane with its own rule reads %d after a standard default", got)
	}
	set(mux, "SPW", "celestrak-space-weather", nil) // clear the lane's choice
	if got := rule(mux, "SPW", "celestrak-space-weather"); got != DSSRetentionReplaceCurrent {
		t.Fatalf("cleared SPW lane reads %d, want the standard default ReplaceCurrent", got)
	}
	set(mux, "OMM", "celestrak-gp", ordinal(DSSRetentionKeepAll)) // the default: no choice left
	set(mux, "OMM", "", ordinal(DSSRetentionArchiveAll))
	if got := rule(mux, "OMM", "celestrak-gp"); got != DSSRetentionArchiveAll {
		t.Fatalf("OMM lane reads %d after the OMM default became ArchiveAll", got)
	}

	// Everything survives a restart.
	mux, _ = start()
	if got := defaults(mux); got["OMM"] != DSSRetentionArchiveAll || got["SPW"] != DSSRetentionReplaceCurrent || got["CAT"] != DSSRetentionReplaceCurrent {
		t.Fatalf("defaults after restart = %v", got)
	}
	if rule(mux, "OMM", "celestrak-gp") != DSSRetentionArchiveAll || rule(mux, "SPW", "celestrak-space-weather") != DSSRetentionReplaceCurrent {
		t.Fatalf("lane rules did not survive a restart")
	}

	// Clearing a standard default restores the built-in one.
	set(mux, "OMM", "", nil)
	if got := rule(mux, "OMM", "celestrak-gp"); got != DSSRetentionKeepAll {
		t.Fatalf("OMM reads %d after its default was cleared, want KeepAll", got)
	}
}
