package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// Regression for ops-update-shutdown-401: on a require_auth node the wallet
// wall default-denied /api/v1/admin/update/shutdown before the handler's own
// designed gate (loopback RemoteAddr + one-time control token) could run, so
// an unattended fleet update reported success while the OLD binary kept
// running. The wall must wave through a self-gated admin path ONLY when the
// request demonstrably originates on loopback; everything remote stays behind
// the wallet wall.
func TestAdminWalletWallAdmitsLoopbackSelfGatedUpdateControl(t *testing.T) {
	authHandler, _ := newAdminSession(t, peers.Standard)
	adminMux := http.NewServeMux()
	reached := 0
	adminMux.HandleFunc("/api/v1/admin/update/shutdown", func(w http.ResponseWriter, r *http.Request) {
		reached++
		// Stand-in for the real control handler's own gate output.
		w.WriteHeader(http.StatusForbidden)
	})

	// Loopback origin: must REACH the handler (whose own token gate answers),
	// not be swallowed by the wallet wall's 401.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI, nil, false)
	if reached != 1 {
		t.Fatalf("loopback self-gated request never reached its handler (status=%d)", rec.Code)
	}
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("loopback self-gated request still wallet-walled: %d", rec.Code)
	}

	// IPv6 loopback too — the helper daemon may dial ::1.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", nil)
	req.RemoteAddr = "[::1]:54321"
	rec = httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI, nil, false)
	if reached != 2 {
		t.Fatalf("IPv6 loopback self-gated request never reached its handler (status=%d)", rec.Code)
	}

	// Remote origin: the wall must hold exactly as before.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", nil)
	req.RemoteAddr = "192.0.2.9:44444"
	rec = httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI, nil, false)
	if reached != 2 {
		t.Fatal("REMOTE request to the self-gated path reached the handler — the wall is open")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote self-gated request status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// A non-self-gated admin path from loopback must still be wallet-walled:
	// the carve-out is the two designed paths, not "loopback bypasses auth".
	adminMux.HandleFunc("/api/peers/protected", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("non-self-gated admin path bypassed the wall from loopback")
	})
	req = httptest.NewRequest(http.MethodPost, "/api/peers/protected", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec = httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI, nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("loopback non-self-gated admin path status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// datasetUpdatesFake stands in for the node's publication service behind the
// REAL dataset-updates handler, so the test exercises the wall and the
// handler's own local-client rule together.
type datasetUpdatesFake struct {
	published int
	retained  int
}

func (f *datasetUpdatesFake) PublishDatasetUpdate(context.Context, api.DatasetPublicationRequest) (*api.DatasetPublicationResult, error) {
	f.published++
	return &api.DatasetPublicationResult{Schema: "IQC.fbs"}, nil
}

func (f *datasetUpdatesFake) RunPublicationRetention(_ context.Context, req api.DatasetPublicationRetentionRequest) (*api.DatasetPublicationRetentionReport, error) {
	f.retained++
	return &api.DatasetPublicationRetentionReport{Schema: req.Schema}, nil
}

// Regression (host-02 beta.75): POST /api/v1/admin/dataset-updates/retention
// answered {"code":"unauthorized","message":"not authenticated"} to a loopback
// curl on the node while /publish accepted the same call. Both routes now
// follow ONE local-client rule, on either auth profile: a loopback caller
// reaches the handler, a remote caller does not, and a loopback request that
// carries reverse-proxy headers (a proxy on this host) does not either.
func TestDatasetUpdatesRoutesShareOneLocalClientRule(t *testing.T) {
	authHandler, _ := newAdminSession(t, peers.Standard)
	type outcome struct {
		code    int
		reached bool
	}
	for _, profile := range []struct {
		name        string
		requireAuth bool
		// On a require_auth node the wallet wall answers the requests it
		// refuses (401); on the retriever profile (require_auth:false, the
		// host-02 shape) the handler's own gate does (403).
		refused int
	}{
		{name: "require_auth", requireAuth: true, refused: http.StatusUnauthorized},
		{name: "require_auth_false", requireAuth: false, refused: http.StatusForbidden},
	} {
		byRoute := map[string]map[string]outcome{}
		for _, route := range []struct {
			path string
			body string
			ok   int
		}{
			{path: "/api/v1/admin/dataset-updates/publish", body: `{"schema":"IQC.fbs"}`, ok: http.StatusAccepted},
			{path: "/api/v1/admin/dataset-updates/retention", body: `{"schema":"IQC.fbs","sourceName":"IQEngine"}`, ok: http.StatusOK},
		} {
			if !isLoopbackSelfGatedAdminPath(route.path) {
				t.Fatalf("%s must be a loopback self-gated admin path", route.path)
			}
			fake := &datasetUpdatesFake{}
			adminMux := http.NewServeMux()
			api.NewDatasetPublicationHandler(fake).RegisterRoutes(adminMux)
			calls := func() int { return fake.published + fake.retained }

			results := map[string]outcome{}
			for _, caller := range []struct {
				name       string
				remoteAddr string
				header     string
			}{
				{name: "loopback", remoteAddr: "127.0.0.1:40000"},
				{name: "loopback-v6", remoteAddr: "[::1]:40000"},
				{name: "remote", remoteAddr: "203.0.113.9:40000"},
				{name: "proxied", remoteAddr: "127.0.0.1:40000", header: "X-Forwarded-For"},
			} {
				before := calls()
				req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
				req.RemoteAddr = caller.remoteAddr
				if caller.header != "" {
					req.Header.Set(caller.header, "198.51.100.7")
				}
				rec := httptest.NewRecorder()
				serveAdminMuxRequest(rec, req, adminMux, profile.requireAuth, false, authHandler, notPublicAPI, nil, false)
				results[caller.name] = outcome{code: rec.Code, reached: calls() > before}
			}
			for _, name := range []string{"loopback", "loopback-v6"} {
				if got := results[name]; got.code != route.ok || !got.reached {
					t.Fatalf("%s %s from %s: status %d reached=%v, want %d and the handler reached",
						profile.name, route.path, name, got.code, got.reached, route.ok)
				}
			}
			for _, name := range []string{"remote", "proxied"} {
				if got := results[name]; got.code != profile.refused || got.reached {
					t.Fatalf("%s %s from %s: status %d reached=%v, want %d and the handler untouched",
						profile.name, route.path, name, got.code, got.reached, profile.refused)
				}
			}
			byRoute[route.path] = results
		}
		// The same rule: every caller class gets the same answer class on both
		// routes (the success codes differ only because publish is 202).
		publish := byRoute["/api/v1/admin/dataset-updates/publish"]
		retention := byRoute["/api/v1/admin/dataset-updates/retention"]
		for caller, p := range publish {
			r := retention[caller]
			if p.reached != r.reached || (!p.reached && p.code != r.code) {
				t.Fatalf("%s: publish and retention disagree for %s caller: publish %+v, retention %+v",
					profile.name, caller, p, r)
			}
		}
	}
}
