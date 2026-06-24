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

func TestRunConjunctionScreenDryRunExportsManeuverEphemerisProvenance(t *testing.T) {
	var out bytes.Buffer
	if err := runConjunctionScreen(context.Background(), &out, conjunctionScreenOptions{
		DryRun:              true,
		PrimarySchema:       "MPE",
		SecondarySchema:     "OMM",
		Encrypted:           true,
		GrantID:             "grant-private-mpe",
		ChannelID:           "channel-private-ca",
		ResultChannelID:     "channel-private-results",
		AssessorPeerID:      "16Uiu2HAssessor",
		PrimaryProviderID:   "operator-private",
		PrimarySourceName:   "private-maneuver-ephemeris",
		PrimaryPNMCID:       "bafyprimarypnm",
		PrimaryQuery:        "ENTITY_ID = 'SAT-MANEUVER'",
		SecondaryProviderID: "space-data-network-02",
		SecondarySourceName: "celestrak-gp",
		SecondaryPNMCID:     "bafysecondarypnm",
		SecondaryQuery:      "NORAD_CAT_ID = 25544",
		ModuleID:            "com.space-data-network.conjunction-assessment",
		ModuleVersion:       "1.0.0",
		Format:              "json",
		Limit:               25,
	}); err != nil {
		t.Fatalf("runConjunctionScreen dry-run failed: %v", err)
	}

	var response struct {
		Workflow        string           `json:"workflow"`
		Mode            string           `json:"mode"`
		Status          string           `json:"status"`
		PrimarySchema   string           `json:"primary_schema"`
		SecondarySchema string           `json:"secondary_schema"`
		Encrypted       bool             `json:"encrypted"`
		GrantID         string           `json:"grant_id"`
		ChannelID       string           `json:"channel_id"`
		ResultChannelID string           `json:"result_channel_id"`
		AssessorPeerID  string           `json:"assessor_peer_id"`
		Sources         []map[string]any `json:"sources"`
		Provenance      map[string]any   `json:"provenance"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("dry-run output is not JSON: %v\n%s", err, out.String())
	}

	if response.Workflow != "encrypted-conjunction-assessment" || response.Mode != "private-maneuver-ephemeris" || response.Status != "dry-run" {
		t.Fatalf("unexpected dry-run workflow fields: %#v", response)
	}
	if response.PrimarySchema != "MPE.fbs" || response.SecondarySchema != "OMM.fbs" || !response.Encrypted {
		t.Fatalf("unexpected dry-run schemas/encryption: %#v", response)
	}
	if response.GrantID != "grant-private-mpe" || response.ChannelID != "channel-private-ca" || response.ResultChannelID != "channel-private-results" {
		t.Fatalf("unexpected dry-run grant/channel identifiers: %#v", response)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %#v, want primary and secondary", response.Sources)
	}
	if response.Sources[0]["role"] != "primary" ||
		response.Sources[0]["schema"] != "MPE.fbs" ||
		response.Sources[0]["provider_id"] != "operator-private" ||
		response.Sources[0]["source_name"] != "private-maneuver-ephemeris" ||
		response.Sources[0]["pnm_cid"] != "bafyprimarypnm" ||
		response.Sources[0]["query"] != "ENTITY_ID = 'SAT-MANEUVER'" ||
		response.Sources[0]["encrypted"] != true {
		t.Fatalf("primary source provenance = %#v", response.Sources[0])
	}
	if response.Sources[1]["role"] != "secondary" ||
		response.Sources[1]["schema"] != "OMM.fbs" ||
		response.Sources[1]["provider_id"] != "space-data-network-02" ||
		response.Sources[1]["source_name"] != "celestrak-gp" ||
		response.Sources[1]["pnm_cid"] != "bafysecondarypnm" ||
		response.Sources[1]["query"] != "NORAD_CAT_ID = 25544" ||
		response.Sources[1]["encrypted"] != false {
		t.Fatalf("secondary source provenance = %#v", response.Sources[1])
	}

	module, ok := response.Provenance["module"].(map[string]any)
	if !ok || module["id"] != "com.space-data-network.conjunction-assessment" || module["version"] != "1.0.0" {
		t.Fatalf("module provenance = %#v", response.Provenance["module"])
	}
	config, ok := response.Provenance["ca_configuration"].(map[string]any)
	if !ok || config["limit"] != float64(25) || config["primary_schema"] != "MPE.fbs" || config["secondary_schema"] != "OMM.fbs" {
		t.Fatalf("CA configuration provenance = %#v", response.Provenance["ca_configuration"])
	}
	if response.Provenance["dry_run"] != true ||
		response.Provenance["grant_id"] != "grant-private-mpe" ||
		response.Provenance["channel_id"] != "channel-private-ca" ||
		response.Provenance["result_channel_id"] != "channel-private-results" ||
		response.Provenance["assessor_peer_id"] != "16Uiu2HAssessor" {
		t.Fatalf("dry-run provenance identifiers = %#v", response.Provenance)
	}
}
