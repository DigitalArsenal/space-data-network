package kubo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeKuboAPI is enough of the Kubo RPC API to answer /api/v0/id and to read and
// write one config key.
type fakeKuboAPI struct {
	mu       sync.Mutex
	agent    string
	config   map[string]string
	readOnly bool
	sets     int
}

func newFakeKuboAPI(agent string) (*fakeKuboAPI, *httptest.Server) {
	f := &fakeKuboAPI{agent: agent, config: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v0/id"):
			_ = json.NewEncoder(w).Encode(map[string]string{"AgentVersion": f.agent})
		case strings.HasPrefix(r.URL.Path, "/api/v0/config"):
			args := r.URL.Query()["arg"]
			if len(args) >= 2 {
				if f.readOnly {
					http.Error(w, "read-only", http.StatusForbidden)
					return
				}
				f.config[args[0]] = args[1]
				f.sets++
				_ = json.NewEncoder(w).Encode(map[string]any{"Key": args[0], "Value": args[1]})
				return
			}
			if len(args) == 1 {
				v, ok := f.config[args[0]]
				if !ok {
					http.Error(w, "no such key", http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Key": args[0], "Value": v})
				return
			}
			http.Error(w, "bad args", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	return f, srv
}

// A Kubo that already identifies as SDN is left alone.
func TestExternalKuboAlreadyIdentifiesAsSDN(t *testing.T) {
	f, srv := newFakeKuboAPI("kubo/0.39.0/spacedatanetwork/1.0.5")
	defer srv.Close()

	status, err := EnsureExternalAgentSuffix(context.Background(), srv.URL, "spacedatanetwork/1.0.5")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !status.IsSDN {
		t.Fatalf("agent %q should be recognised as SDN", status.AgentVersion)
	}
	if status.RestartNeeded {
		t.Fatal("no restart should be asked for when the daemon already identifies correctly")
	}
	if f.sets != 0 {
		t.Fatalf("config was written %d time(s) against a Kubo that needed nothing", f.sets)
	}
}

// A stock Kubo gets the suffix set, and the caller is told a restart is needed —
// Kubo reads the suffix once, at daemon start, so the running process keeps
// announcing the old string until it restarts.
func TestExternalKuboWithoutSDNIdentityIsCorrected(t *testing.T) {
	f, srv := newFakeKuboAPI("kubo/0.39.0")
	defer srv.Close()

	want := "spacedatanetwork/1.0.5-beta.68"
	status, err := EnsureExternalAgentSuffix(context.Background(), srv.URL, want)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if status.IsSDN {
		t.Fatal("a bare kubo/<version> must not be reported as an SDN node")
	}
	if !status.SuffixSet || !status.RestartNeeded {
		t.Fatalf("expected the suffix set and a restart requested, got %+v", status)
	}
	if got := f.config["Version.AgentSuffix"]; got != want {
		t.Fatalf("Version.AgentSuffix = %q, want %q", got, want)
	}
	// And what it will present after that restart must satisfy the board.
	if !agentVersionIsSDN("kubo/0.39.0/" + f.config["Version.AgentSuffix"]) {
		t.Fatal("even after the fix the daemon would not be recognised as an SDN node")
	}
}

// A Kubo whose API refuses writes must surface an error, not claim success —
// the operator has to run the command themselves.
func TestExternalKuboReadOnlyAPIReportsFailure(t *testing.T) {
	f, srv := newFakeKuboAPI("kubo/0.39.0")
	f.readOnly = true
	defer srv.Close()

	status, err := EnsureExternalAgentSuffix(context.Background(), srv.URL, "spacedatanetwork/1.0.5")
	if err == nil {
		t.Fatalf("a refused write must be an error, got status %+v", status)
	}
	if status.SuffixSet {
		t.Fatal("SuffixSet must not be claimed when the write was refused")
	}
}
