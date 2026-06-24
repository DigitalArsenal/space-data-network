package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConjunctionCommandIsRegisteredForEncryptedManeuverEphemeris(t *testing.T) {
	requireCommand(t, []string{"conjunction"}, "conjunction")
	requireCommand(t, []string{"conjunction", "screen"}, "screen")

	help := conjunctionScreenCmd.UsageString()
	for _, want := range []string{
		"--primary-schema",
		"--secondary-schema",
		"--encrypted",
		"--grant-id",
		"--channel-id",
		"--assessor-peer-id",
		"--format",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("conjunction screen help missing %q:\n%s", want, help)
		}
	}
}

func TestRunConjunctionScreenPostsEncryptedManeuverWorkflow(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatalf("decode screen body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"events": [{
				"event_id": "ca-private-mpe-001",
				"primary_schema": "MPE.fbs",
				"secondary_schema": "OMM.fbs",
				"screening_result": "clear",
				"min_range_m": 1250.5
			}],
			"provenance": {
				"grant_id": "grant-private-mpe",
				"channel_id": "private-ca-channel",
				"assessor_peer_id": "16Uiu2Assessor",
				"encrypted": true
			}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := runConjunctionScreen(context.Background(), &out, conjunctionScreenOptions{
		BaseURL:         server.URL,
		PrimarySchema:   "MPE",
		SecondarySchema: "OMM",
		Encrypted:       true,
		GrantID:         "grant-private-mpe",
		ChannelID:       "private-ca-channel",
		AssessorPeerID:  "16Uiu2Assessor",
		Format:          "json",
	}); err != nil {
		t.Fatalf("runConjunctionScreen failed: %v", err)
	}

	if receivedPath != "/api/v1/conjunction/screen" {
		t.Fatalf("screen path = %q, want /api/v1/conjunction/screen", receivedPath)
	}
	for key, want := range map[string]any{
		"primary_schema":     "MPE.fbs",
		"secondary_schema":   "OMM.fbs",
		"encrypted":          true,
		"grant_id":           "grant-private-mpe",
		"channel_id":         "private-ca-channel",
		"assessor_peer_id":   "16Uiu2Assessor",
		"include_provenance": true,
	} {
		if receivedBody[key] != want {
			t.Fatalf("screen body[%s] = %#v, want %#v in %#v", key, receivedBody[key], want, receivedBody)
		}
	}

	var response struct {
		Events     []map[string]any `json:"events"`
		Provenance map[string]any   `json:"provenance"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("screen output is not JSON: %v\n%s", err, out.String())
	}
	if len(response.Events) != 1 || response.Events[0]["event_id"] != "ca-private-mpe-001" {
		t.Fatalf("events = %#v", response.Events)
	}
	if response.Provenance["grant_id"] != "grant-private-mpe" || response.Provenance["encrypted"] != true {
		t.Fatalf("provenance = %#v", response.Provenance)
	}
}
