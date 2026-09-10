package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKubo records the RPC sequence a provider-peered fetch issues.
func fakeKubo(t *testing.T, connectedPeers []string, payload []byte) (*httptest.Server, *[]string) {
	t.Helper()
	calls := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := strings.TrimPrefix(r.URL.Path, "/api/v0/")
		*calls = append(*calls, op+"?"+r.URL.Query().Get("arg"))
		switch op {
		case "swarm/peers":
			var rows []string
			for _, p := range connectedPeers {
				rows = append(rows, `{"Peer":"`+p+`","Addr":"/ip4/127.0.0.1/tcp/4001"}`)
			}
			_, _ = w.Write([]byte(`{"Peers":[` + strings.Join(rows, ",") + `]}`))
		case "swarm/peering/add", "swarm/peering/rm", "swarm/connect", "swarm/disconnect":
			_, _ = w.Write([]byte(`{"Strings":["ok"]}`))
		case "cat":
			_, _ = w.Write(payload)
		default:
			http.Error(w, "unexpected "+op, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

const testProviderAddr = "/ip4/127.0.0.1/tcp/4101/p2p/12D3KooWProviderPeer"

func TestFetchIPFSBlockByCIDViaPeersConnectsFetchesAndReleases(t *testing.T) {
	srv, calls := fakeKubo(t, nil, []byte("native-bytes"))
	got, err := FetchIPFSBlockByCIDVia(context.Background(), srv.URL, "bafyprobe", testProviderAddr)
	if err != nil {
		t.Fatalf("fetch via provider: %v", err)
	}
	if string(got) != "native-bytes" {
		t.Fatalf("payload = %q, want native-bytes", got)
	}
	want := []string{
		"swarm/peers?",
		"swarm/peering/add?" + testProviderAddr,
		"swarm/connect?" + testProviderAddr,
		"cat?bafyprobe",
		"swarm/peering/rm?12D3KooWProviderPeer",
		"swarm/disconnect?" + testProviderAddr,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("RPC sequence =\n%s\nwant\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestPeerIPFSProviderKeepsAPreexistingConnection(t *testing.T) {
	srv, calls := fakeKubo(t, []string{"12D3KooWProviderPeer"}, nil)
	release, err := PeerIPFSProvider(context.Background(), srv.URL, testProviderAddr)
	if err != nil {
		t.Fatalf("peer provider: %v", err)
	}
	release()
	release() // idempotent
	for _, call := range *calls {
		if strings.HasPrefix(call, "swarm/disconnect") {
			t.Fatalf("a connection this call did not open was closed: %v", *calls)
		}
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "swarm/peering/rm?12D3KooWProviderPeer") {
		t.Fatalf("peering entry was not removed: %v", *calls)
	}
}

func TestPeerIPFSProviderRollsBackPeeringWhenConnectFails(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := strings.TrimPrefix(r.URL.Path, "/api/v0/")
		calls = append(calls, op)
		switch op {
		case "swarm/peers":
			_, _ = w.Write([]byte(`{"Peers":[]}`))
		case "swarm/connect":
			http.Error(w, `{"Message":"dial backoff"}`, http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	if _, err := PeerIPFSProvider(context.Background(), srv.URL, testProviderAddr); err == nil {
		t.Fatal("expected the failed dial to be reported")
	}
	joined := strings.Join(calls, " ")
	if !strings.Contains(joined, "swarm/peering/rm") {
		t.Fatalf("peering entry leaked after a failed dial: %v", calls)
	}
}

func TestPeerIDFromMultiaddr(t *testing.T) {
	if id, err := peerIDFromMultiaddr("/dns4/h.example/tcp/4001/p2p/12D3KooWAbc"); err != nil || id != "12D3KooWAbc" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := peerIDFromMultiaddr("/ip4/1.2.3.4/tcp/4001"); err == nil {
		t.Fatal("an address without a peer must be refused")
	}
}
