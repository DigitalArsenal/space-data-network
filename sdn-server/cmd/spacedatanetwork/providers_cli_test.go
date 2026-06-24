package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestProvidersCommandIsRegisteredWithRequesterSubcommands(t *testing.T) {
	requireCommand(t, []string{"providers"}, "providers")
	for _, command := range []struct {
		args []string
		use  string
	}{
		{[]string{"providers", "list"}, "list"},
		{[]string{"providers", "search"}, "search [query]"},
		{[]string{"providers", "show"}, "show <provider>"},
		{[]string{"providers", "connect"}, "connect [provider-url]"},
		{[]string{"providers", "query"}, "query"},
		{[]string{"providers", "descriptor"}, "descriptor [provider-url]"},
	} {
		requireCommand(t, command.args, command.use)
	}

	commands := providersCmd.Commands()
	got := make([]string, 0, len(commands))
	for _, command := range commands {
		got = append(got, command.Name())
	}
	want := []string{"connect", "descriptor", "list", "query", "search", "show"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers subcommands = %#v, want %#v", got, want)
	}
}

func TestProvidersCommandHelpDocumentsDataStandardFiltering(t *testing.T) {
	help := providersCmd.UsageString()
	for _, want := range []string{"--schema", "--provider-id", "--source-name", "--format"} {
		if !strings.Contains(help, want) {
			t.Fatalf("providers help missing %q:\n%s", want, help)
		}
	}
}

func TestRunProvidersDescriptorFetchesProviderDescriptor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/module-delivery/provider" {
			t.Fatalf("unexpected descriptor path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"peerId": "16Uiu2Provider",
			"publicKey": "provider-public-key",
			"protocols": ["/space-data-network/module-delivery/1.0.0"]
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := runProvidersDescriptor(context.Background(), &out, providersDescriptorOptions{
		ProviderURL: server.URL + "/api/module-delivery/provider",
		Format:      "json",
	}); err != nil {
		t.Fatalf("runProvidersDescriptor failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("descriptor output is not JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("descriptor output = %#v", body)
	}
	if body.Results[0]["peer_id"] != "16Uiu2Provider" || body.Results[0]["public_key"] != "provider-public-key" {
		t.Fatalf("descriptor output = %#v", body)
	}
}

func TestRunProvidersQueryUsesUnifiedDataQueryEndpoint(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatalf("decode query body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"records": [{
				"schema_name": "OMM.fbs",
				"provider_id": "space-data-network-02",
				"source_name": "celestrak-gp",
				"cid": "bafkreiquery"
			}]
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := runProvidersQuery(context.Background(), &out, providersQueryOptions{
		BaseURL:    server.URL,
		Schema:     "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Format:     "json",
	}); err != nil {
		t.Fatalf("runProvidersQuery failed: %v", err)
	}

	if receivedPath != "/api/v1/data/query" {
		t.Fatalf("query path = %q, want /api/v1/data/query", receivedPath)
	}
	if receivedBody["schema"] != "OMM.fbs" ||
		receivedBody["provider_id"] != "space-data-network-02" ||
		receivedBody["source_name"] != "celestrak-gp" {
		t.Fatalf("query body = %#v", receivedBody)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("query output is not JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 || body.Results[0]["cid"] != "bafkreiquery" {
		t.Fatalf("query output = %#v", body)
	}
}
