package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChannelsListPrintsStandardCodesOnly(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--standard", "OMM"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels list failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"standardCode=OMM",
		"topic=/spacedatanetwork/channels/OMM",
		"visibility=public",
		"subscribed=false",
		"grantState=not-required",
		"encryptionState=none",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels list output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, string([]byte{'.', 'f', 'b', 's'})) {
		t.Fatalf("channels list output exposed internal schema suffix:\n%s", body)
	}
}

func TestChannelsListUsesLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels" || r.URL.Query().Get("standardCode") != "OMM" {
			t.Fatalf("unexpected list request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"count":1,
			"results":[{
				"channelId":"spaceaware-OMM",
				"sourceId":"spaceaware",
				"standardCode":"OMM",
				"topic":"/spacedatanetwork/channels/OMM",
				"visibility":"private-listed",
				"subscribed":true,
				"grantState":"verified",
				"encryptionState":"encrypted"
			}]
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--standard", "OMM", "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels list failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"topic=/spacedatanetwork/channels/OMM",
		"visibility=private-listed",
		"subscribed=true",
		"grantState=verified",
		"encryptionState=encrypted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels list output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsListPassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels" {
			t.Fatalf("unexpected list request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if got := query.Get("standardCode"); got != "OMM" {
			t.Fatalf("list standardCode query = %q", got)
		}
		if got := query.Get("visibility"); got != "private-listed" {
			t.Fatalf("list visibility query = %q", got)
		}
		if got := query.Get("subject"); got != "peer-alpha" {
			t.Fatalf("list subject query = %q", got)
		}
		if got := query.Get("grantId"); got != "grant-123" {
			t.Fatalf("list grantId query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"count":1,
			"results":[{
				"channelId":"spaceaware-OMM",
				"sourceId":"spaceaware",
				"standardCode":"OMM",
				"visibility":"private-listed",
				"grantState":"verified",
				"encryptionState":"encrypted"
			}]
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"list",
		"--standard", "OMM",
		"--visibility", "private-listed",
		"--subject", "peer-alpha",
		"--grant-id", "grant-123",
		"--api-url", server.URL,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels list failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"standardCode=OMM",
		"visibility=private-listed",
		"grantState=verified",
		"encryptionState=encrypted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels list output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsShowParsesHyphenatedSource(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "celestrak-eth-CDM"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels show failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=celestrak-eth-CDM",
		"sourceId=celestrak-eth",
		"standardCode=CDM",
		"visibility=unknown",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels show output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsShowUsesLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/celestrak-eth-CDM" {
			t.Fatalf("unexpected show request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"celestrak-eth-CDM",
			"sourceId":"celestrak-eth",
			"standardCode":"CDM",
			"visibility":"public",
			"subscribed":false,
			"pnmVerified":true,
			"dpmVerified":true,
			"pnmCid":"bafyhead",
			"grantState":"not-required",
			"encryptionState":"none"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "celestrak-eth-CDM", "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels show failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=celestrak-eth-CDM",
		"sourceId=celestrak-eth",
		"standardCode=CDM",
		"visibility=public",
		"subscribed=false",
		"pnmVerified=true",
		"dpmVerified=true",
		"pnmCid=bafyhead",
		"grantState=not-required",
		"encryptionState=none",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels show output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsShowPassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM" {
			t.Fatalf("unexpected show request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if query.Get("subject") != "peer-alpha" || query.Get("grantId") != "grant-1" || query.Get("visibility") != "private-hidden" {
			t.Fatalf("private show query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"visibility":"private-hidden",
			"grantState":"verified",
			"encryptionState":"encrypted",
			"pnmVerified":true,
			"dpmVerified":true
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"show",
		"spaceaware-OMM",
		"--subject",
		"peer-alpha",
		"--grant-id",
		"grant-1",
		"--visibility",
		"private-hidden",
		"--api-url",
		server.URL,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels show failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"visibility=private-hidden",
		"grantState=verified",
		"encryptionState=encrypted",
		"pnmVerified=true",
		"dpmVerified=true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels show output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsMonitorReportsRequiredFields(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"monitor", "spaceaware-OMM-550e8400-e29b-41d4-a716-446655440000"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels monitor failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelHead=",
		"pnmVerified=",
		"dpmVerified=",
		"providerPeer=",
		"localRows=",
		"remoteRows=",
		"syncedRows=",
		"missingRows=",
		"pinnedCount=",
		"pinnedRows=",
		"pinnedBytes=",
		"syncedBytes=",
		"throughputBytesPerSecond=",
		"wireSpeedUtilization=",
		"wireSpeedTarget=",
		"requiredBytesPerSecond=",
		"targetMet=",
		"grantState=",
		"encryptionState=",
		"lastVerifiedUpdate=",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels monitor output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsMonitorReadsLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/monitor" {
			t.Fatalf("unexpected monitor request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"channelHead":"bafyhead",
			"pnmVerified":true,
			"dpmVerified":true,
			"providerPeer":"12D3KooProvider",
			"localRows":10,
			"remoteRows":12,
			"syncedRows":10,
			"missingRows":2,
			"pinnedCount":8,
			"pinnedRows":8,
			"pinnedBytes":4096,
			"syncedBytes":4096,
			"throughputBytesPerSecond":2048,
			"wireSpeedUtilization":0.91,
			"wireSpeedTarget":0.9,
			"requiredBytesPerSecond":225000000,
			"targetMet":true,
			"timingsMs":{
				"discovery":11,
				"grantNegotiation":12,
				"pnmDpmVerification":13,
				"transfer":14,
				"decrypt":15,
				"hashVerification":16,
				"durableImport":17
			},
			"grantState":"verified",
			"encryptionState":"public",
			"lastVerifiedUpdate":"2026-06-04T00:00:00Z"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"monitor", "spaceaware-OMM", "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels monitor failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"channelHead=bafyhead",
		"pnmVerified=true",
		"dpmVerified=true",
		"providerPeer=12D3KooProvider",
		"localRows=10",
		"remoteRows=12",
		"syncedRows=10",
		"missingRows=2",
		"pinnedCount=8",
		"pinnedRows=8",
		"pinnedBytes=4096",
		"syncedBytes=4096",
		"throughputBytesPerSecond=2048",
		"wireSpeedUtilization=0.91",
		"wireSpeedTarget=0.9",
		"requiredBytesPerSecond=225000000",
		"targetMet=true",
		"timingDiscoveryMs=11",
		"timingGrantNegotiationMs=12",
		"timingPnmDpmVerificationMs=13",
		"timingTransferMs=14",
		"timingDecryptMs=15",
		"timingHashVerificationMs=16",
		"timingDurableImportMs=17",
		"grantState=verified",
		"encryptionState=public",
		"lastVerifiedUpdate=2026-06-04T00:00:00Z",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels monitor output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsMonitorAllowsLoopbackSelfSignedTLS(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/monitor" {
			t.Fatalf("unexpected monitor request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"channelHead":"bafyhead",
			"pnmVerified":true
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"monitor",
		"spaceaware-OMM",
		"--api-url",
		server.URL,
		"--insecure-skip-tls-verify",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels monitor failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"channelHead=bafyhead",
		"pnmVerified=true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels monitor output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsMonitorRejectsInsecureTLSForNonLoopbackAPI(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"monitor",
		"spaceaware-OMM",
		"--api-url",
		"https://spaceaware.io",
		"--insecure-skip-tls-verify",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("channels monitor unexpectedly allowed insecure TLS for non-loopback API")
	}
	if !strings.Contains(err.Error(), "only allowed for loopback") {
		t.Fatalf("channels monitor error = %q", err.Error())
	}
}

func TestChannelsMonitorPassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/monitor" {
			t.Fatalf("unexpected monitor request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if query.Get("subject") != "peer-alpha" || query.Get("grantId") != "grant-1" || query.Get("visibility") != "private-listed" {
			t.Fatalf("private monitor query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"visibility":"private-listed",
			"grantState":"verified",
			"encryptionState":"encrypted",
			"pnmVerified":true
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"monitor",
		"spaceaware-OMM",
		"--subject",
		"peer-alpha",
		"--grant-id",
		"grant-1",
		"--visibility",
		"private-listed",
		"--api-url",
		server.URL,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels monitor failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"visibility=private-listed",
		"grantState=verified",
		"encryptionState=encrypted",
		"pnmVerified=true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels monitor output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsPublishSendsNativeStreamToLocalAPI(t *testing.T) {
	t.Parallel()

	streamFile := filepath.Join(t.TempDir(), "stream.bin")
	streamBytes := bytes.Join([][]byte{
		channelCLITestNativeFrame("OMM1", []byte{1, 2, 3}),
		channelCLITestNativeFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	if err := os.WriteFile(streamFile, streamBytes, 0o600); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/publish" || r.URL.Query().Get("stream") != "1" {
			t.Fatalf("unexpected publish request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("publish Content-Type = %q", got)
		}
		body := new(bytes.Buffer)
		if _, err := body.ReadFrom(r.Body); err != nil {
			t.Fatalf("read publish body: %v", err)
		}
		if !bytes.Equal(body.Bytes(), streamBytes) {
			t.Fatalf("publish body mismatch: %v", body.Bytes())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"contentType":"application/vnd.sdn.flatbuffers.stream",
			"streamBytes":23,
			"streamFrames":2,
			"throughputBytesPerSecond":2048,
			"wireSpeedUtilization":0.91,
			"wireSpeedTarget":0.9,
			"requiredBytesPerSecond":225000000,
			"targetMet":true
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"publish", "spaceaware-OMM", "--from", streamFile, "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels publish failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"contentType=application/vnd.sdn.flatbuffers.stream",
		"streamBytes=23",
		"streamFrames=2",
		"throughputBytesPerSecond=2048",
		"wireSpeedUtilization=0.91",
		"wireSpeedTarget=0.9",
		"requiredBytesPerSecond=225000000",
		"targetMet=true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels publish output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsPublishPassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	streamFile := filepath.Join(t.TempDir(), "private-stream.bin")
	streamBytes := channelCLITestNativeFrame("OMM1", []byte{1, 2, 3})
	if err := os.WriteFile(streamFile, streamBytes, 0o600); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/publish" {
			t.Fatalf("unexpected publish request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if query.Get("stream") != "1" || query.Get("subject") != "peer-alpha" || query.Get("grantId") != "grant-1" || query.Get("visibility") != "private-listed" {
			t.Fatalf("private publish query = %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("publish Content-Type = %q", got)
		}
		if got := r.Header.Get("X-SDN-Encrypted-Stream"); got != "true" {
			t.Fatalf("publish X-SDN-Encrypted-Stream = %q, want true", got)
		}
		if got := r.Header.Get("X-SDN-Encrypted-Stream-Header"); got != `{"algorithm":"x25519","context":"spaceaware-OMM","ephemeral_public_key":"pub","nonce_start":"nonce"}` {
			t.Fatalf("publish X-SDN-Encrypted-Stream-Header = %q", got)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if !bytes.Equal(body.Bytes(), streamBytes) {
			t.Fatalf("publish body mismatch: %v", body.Bytes())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"contentType":"application/vnd.sdn.flatbuffers.stream",
			"streamBytes":11,
			"streamFrames":1,
			"grantState":"verified",
			"encryptionState":"encrypted"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"publish", "spaceaware-OMM",
		"--from", streamFile,
		"--api-url", server.URL,
		"--subject", "peer-alpha",
		"--grant-id", "grant-1",
		"--visibility", "private-listed",
		"--encrypted-stream-header", `{"algorithm":"x25519","context":"spaceaware-OMM","ephemeral_public_key":"pub","nonce_start":"nonce"}`,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels publish failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"standardCode=OMM",
		"contentType=application/vnd.sdn.flatbuffers.stream",
		"streamFrames=1",
		"grantState=verified",
		"encryptionState=encrypted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels publish output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsPublishReadsEncryptedStreamHeaderFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	streamFile := filepath.Join(tempDir, "private-stream.bin")
	streamBytes := channelCLITestNativeFrame("OMM1", []byte{1, 2, 3})
	if err := os.WriteFile(streamFile, streamBytes, 0o600); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}
	headerFile := filepath.Join(tempDir, "stream-header.json")
	headerJSON := `{"version":2,"algorithm":"x25519","senderPublicKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","recipientKeyId":"bbbbbbbbbbbbbbbb","nonceStart":"cccccccccccccccccccccccc","context":"spaceaware-OMM"}`
	if err := os.WriteFile(headerFile, []byte(headerJSON+"\n"), 0o600); err != nil {
		t.Fatalf("write header fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/publish" {
			t.Fatalf("unexpected publish request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if query.Get("stream") != "1" || query.Get("subject") != "peer-alpha" || query.Get("grantId") != "grant-1" || query.Get("visibility") != "private-listed" {
			t.Fatalf("private publish query = %s", r.URL.RawQuery)
		}
		if strings.Contains(r.URL.RawQuery, "senderPublicKey") || strings.Contains(r.URL.RawQuery, "nonceStart") {
			t.Fatalf("encrypted stream header leaked into URL query: %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("X-SDN-Encrypted-Stream"); got != "true" {
			t.Fatalf("publish X-SDN-Encrypted-Stream = %q, want true", got)
		}
		if got := r.Header.Get("X-SDN-Encrypted-Stream-Header"); got != headerJSON {
			t.Fatalf("publish X-SDN-Encrypted-Stream-Header = %q", got)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if !bytes.Equal(body.Bytes(), streamBytes) {
			t.Fatalf("publish body mismatch: %v", body.Bytes())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"standardCode":"OMM",
			"contentType":"application/vnd.sdn.flatbuffers.stream",
			"streamBytes":11,
			"streamFrames":1,
			"grantState":"verified",
			"encryptionState":"encrypted"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"publish", "spaceaware-OMM",
		"--from", streamFile,
		"--api-url", server.URL,
		"--subject", "peer-alpha",
		"--grant-id", "grant-1",
		"--visibility", "private-listed",
		"--encrypted-stream-header-file", headerFile,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels publish failed: %v", err)
	}
	if !strings.Contains(out.String(), "encryptionState=encrypted") {
		t.Fatalf("channels publish output missing encryption state:\n%s", out.String())
	}
}

func TestChannelsPublishRejectsStreamOutsideChannelStandard(t *testing.T) {
	t.Parallel()

	streamFile := filepath.Join(t.TempDir(), "stream.bin")
	streamBytes := bytes.Join([][]byte{
		channelCLITestNativeFrame("OMM1", []byte{1, 2, 3}),
		channelCLITestNativeFrame("CDM1", []byte{4, 5, 6}),
	}, nil)
	if err := os.WriteFile(streamFile, streamBytes, 0o600); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"publish", "spaceaware-OMM", "--from", streamFile})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected channels publish to reject mismatched stream standard")
	}
	if !strings.Contains(err.Error(), "does not match channel standardCode OMM") {
		t.Fatalf("publish mismatch error = %q", err.Error())
	}
}

func TestChannelsStreamReadsNativeStreamFromLocalAPI(t *testing.T) {
	t.Parallel()

	streamBytes := bytes.Join([][]byte{
		channelCLITestNativeFrame("OMM1", []byte{1, 2, 3}),
		channelCLITestNativeFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	outFile := filepath.Join(t.TempDir(), "stream.bin")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/stream" {
			t.Fatalf("unexpected stream request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("stream Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
		w.Header().Set("X-SDN-Stream-Frames", "2")
		_, _ = w.Write(streamBytes)
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"stream", "spaceaware-OMM", "--out", outFile, "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels stream failed: %v", err)
	}
	written, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read stream output: %v", err)
	}
	if !bytes.Equal(written, streamBytes) {
		t.Fatalf("stream output mismatch: %v", written)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"contentType=application/vnd.sdn.flatbuffers.stream",
		"streamBytes=23",
		"streamFrames=2",
		"out=" + outFile,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels stream output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "base64") || strings.Contains(body, "records=") {
		t.Fatalf("channels stream output exposed JSON/base64 hot path:\n%s", body)
	}
}

func TestChannelsStreamRejectsStreamOutsideChannelStandardBeforeWrite(t *testing.T) {
	t.Parallel()

	streamBytes := bytes.Join([][]byte{
		channelCLITestNativeFrame("OMM1", []byte{1, 2, 3}),
		channelCLITestNativeFrame("CDM1", []byte{4, 5, 6}),
	}, nil)
	outFile := filepath.Join(t.TempDir(), "stream.bin")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
		_, _ = w.Write(streamBytes)
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"stream", "spaceaware-OMM", "--out", outFile, "--api-url", server.URL})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected channels stream to reject mismatched stream standard")
	}
	if !strings.Contains(err.Error(), "does not match channel standardCode OMM") {
		t.Fatalf("stream mismatch error = %q", err.Error())
	}
	if _, statErr := os.Stat(outFile); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched stream wrote output file, statErr=%v", statErr)
	}
}

func TestChannelsStreamPassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	streamBytes := channelCLITestNativeFrame("OMM1", []byte{1, 2, 3})
	outFile := filepath.Join(t.TempDir(), "private-stream.bin")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/stream" {
			t.Fatalf("unexpected stream request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if got := query.Get("subject"); got != "epm-subject-alpha" {
			t.Fatalf("stream subject query = %q", got)
		}
		if got := query.Get("grantId"); got != "grant-123" {
			t.Fatalf("stream grantId query = %q", got)
		}
		if got := query.Get("visibility"); got != "private-listed" {
			t.Fatalf("stream visibility query = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("stream Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
		_, _ = w.Write(streamBytes)
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"stream", "spaceaware-OMM",
		"--subject", "epm-subject-alpha",
		"--grant-id", "grant-123",
		"--visibility", "private-listed",
		"--out", outFile,
		"--api-url", server.URL,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels stream failed: %v", err)
	}
}

func TestChannelsPNMReadsVerifiedPNMFromLocalAPI(t *testing.T) {
	t.Parallel()

	pnmBytes := []byte{11, 0, 0, 0, 80, 78, 77, 49, 1, 2, 3, 4, 5, 6, 7}
	outFile := filepath.Join(t.TempDir(), "channel.pnm")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/pnm" {
			t.Fatalf("unexpected PNM request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.sdn.pnm" {
			t.Fatalf("PNM Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.sdn.pnm")
		_, _ = w.Write(pnmBytes)
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"pnm", "spaceaware-OMM", "--out", outFile, "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels pnm failed: %v", err)
	}
	written, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read PNM output: %v", err)
	}
	if !bytes.Equal(written, pnmBytes) {
		t.Fatalf("PNM output mismatch: %v", written)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"contentType=application/vnd.sdn.pnm",
		"pnmBytes=15",
		"out=" + outFile,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels pnm output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "base64") || strings.Contains(body, "records=") {
		t.Fatalf("channels pnm output exposed JSON/base64 hot path:\n%s", body)
	}
}

func TestChannelsPNMPassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	pnmBytes := []byte{11, 0, 0, 0, 80, 78, 77, 49, 1, 2, 3, 4, 5, 6, 7}
	outFile := filepath.Join(t.TempDir(), "private-channel.pnm")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channels/spaceaware-OMM/pnm" {
			t.Fatalf("unexpected PNM request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if got := query.Get("subject"); got != "epm-subject-alpha" {
			t.Fatalf("PNM subject query = %q", got)
		}
		if got := query.Get("grantId"); got != "grant-123" {
			t.Fatalf("PNM grantId query = %q", got)
		}
		if got := query.Get("visibility"); got != "private-hidden" {
			t.Fatalf("PNM visibility query = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.sdn.pnm" {
			t.Fatalf("PNM Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.sdn.pnm")
		_, _ = w.Write(pnmBytes)
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"pnm", "spaceaware-OMM",
		"--subject", "epm-subject-alpha",
		"--grant-id", "grant-123",
		"--visibility", "private-hidden",
		"--out", outFile,
		"--api-url", server.URL,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels pnm failed: %v", err)
	}
}

func TestChannelsKeyUnwrapRequestsWrappedEnvelopeFromLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/key-unwrap" {
			t.Fatalf("unexpected key unwrap request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if got := query.Get("subject"); got != "epm-subject-alpha" {
			t.Fatalf("key unwrap subject query = %q", got)
		}
		if got := query.Get("grantId"); got != "grant-123" {
			t.Fatalf("key unwrap grantId query = %q", got)
		}
		if got := query.Get("visibility"); got != "private-listed" {
			t.Fatalf("key unwrap visibility query = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("key unwrap Content-Type = %q", got)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode key unwrap request: %v", err)
		}
		if request["recipientKeyId"] != "peer-alpha-x25519" || request["contentKeyId"] != "channel-private-key" {
			t.Fatalf("unexpected key unwrap body: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"standardCode":"OMM",
			"grantState":"verified",
			"contentKeyId":"channel-private-key",
			"recipientKeyId":"peer-alpha-x25519",
			"keyEpoch":"epoch-2026-06-05T00",
			"algorithm":"DigitalArsenal-FlatBuffers-X25519-AES256GCM",
			"envelopeCid":"bafywrappedpeeralpha",
			"wrappedKeyEnvelopeBase64":"d3JhcHBlZA=="
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"key-unwrap", "spaceaware-OMM",
		"--subject", "epm-subject-alpha",
		"--grant-id", "grant-123",
		"--visibility", "private-listed",
		"--content-key-id", "channel-private-key",
		"--recipient-key-id", "peer-alpha-x25519",
		"--api-url", server.URL,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels key-unwrap failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"standardCode=OMM",
		"grantState=verified",
		"contentKeyId=channel-private-key",
		"recipientKeyId=peer-alpha-x25519",
		"keyEpoch=epoch-2026-06-05T00",
		"algorithm=DigitalArsenal-FlatBuffers-X25519-AES256GCM",
		"envelopeCid=bafywrappedpeeralpha",
		"wrappedKeyEnvelopeBase64=d3JhcHBlZA==",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels key-unwrap output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "plaintext") {
		t.Fatalf("channels key-unwrap output referenced plaintext key material:\n%s", body)
	}
}

func TestChannelsSubscribeAndUnsubscribePublicChannel(t *testing.T) {
	t.Parallel()

	cmd := newChannelsCommand()

	var subscribeOut bytes.Buffer
	cmd.SetOut(&subscribeOut)
	cmd.SetErr(&subscribeOut)
	cmd.SetArgs([]string{"subscribe", "spaceaware-OMM"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels subscribe failed: %v", err)
	}
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"subscribed=true",
		"visibility=public",
		"grantState=not-required",
		"encryptionState=none",
	} {
		if !strings.Contains(subscribeOut.String(), want) {
			t.Fatalf("channels subscribe output missing %q:\n%s", want, subscribeOut.String())
		}
	}

	var unsubscribeOut bytes.Buffer
	cmd.SetOut(&unsubscribeOut)
	cmd.SetErr(&unsubscribeOut)
	cmd.SetArgs([]string{"unsubscribe", "spaceaware-OMM"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels unsubscribe failed: %v", err)
	}
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"subscribed=false",
		"visibility=public",
		"grantState=not-required",
	} {
		if !strings.Contains(unsubscribeOut.String(), want) {
			t.Fatalf("channels unsubscribe output missing %q:\n%s", want, unsubscribeOut.String())
		}
	}
}

func TestChannelsSubscribeUsesLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/subscribe" {
			t.Fatalf("unexpected subscribe request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"subscribed":true,
			"visibility":"private-listed",
			"grantState":"verified",
			"encryptionState":"encrypted"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"subscribe", "spaceaware-OMM", "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels subscribe failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"subscribed=true",
		"visibility=private-listed",
		"grantState=verified",
		"encryptionState=encrypted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels subscribe output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsSubscribePassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/subscribe" {
			t.Fatalf("unexpected subscribe request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if query.Get("subject") != "peer-alpha" || query.Get("grantId") != "grant-1" || query.Get("visibility") != "private-listed" {
			t.Fatalf("private subscribe query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"subscribed":true,
			"visibility":"private-listed",
			"grantState":"verified",
			"encryptionState":"encrypted"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"subscribe", "spaceaware-OMM",
		"--api-url", server.URL,
		"--subject", "peer-alpha",
		"--grant-id", "grant-1",
		"--visibility", "private-listed",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels subscribe failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"visibility=private-listed",
		"grantState=verified",
		"encryptionState=encrypted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels subscribe output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsUnsubscribeUsesLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/unsubscribe" {
			t.Fatalf("unexpected unsubscribe request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"subscribed":false,
			"visibility":"public",
			"grantState":"not-required",
			"encryptionState":"none"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"unsubscribe", "spaceaware-OMM", "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels unsubscribe failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"sourceId=spaceaware",
		"standardCode=OMM",
		"subscribed=false",
		"visibility=public",
		"grantState=not-required",
		"encryptionState=none",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels unsubscribe output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsUnsubscribePassesPrivateGrantContextToLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/unsubscribe" {
			t.Fatalf("unexpected unsubscribe request %s %s", r.Method, r.URL.String())
		}
		query := r.URL.Query()
		if query.Get("subject") != "peer-alpha" || query.Get("grantId") != "grant-1" || query.Get("visibility") != "private-hidden" {
			t.Fatalf("private unsubscribe query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"channelId":"spaceaware-OMM",
			"sourceId":"spaceaware",
			"standardCode":"OMM",
			"subscribed":false,
			"visibility":"private-hidden",
			"grantState":"verified",
			"encryptionState":"encrypted"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"unsubscribe", "spaceaware-OMM",
		"--api-url", server.URL,
		"--subject", "peer-alpha",
		"--grant-id", "grant-1",
		"--visibility", "private-hidden",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels unsubscribe failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"visibility=private-hidden",
		"grantState=verified",
		"encryptionState=encrypted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels unsubscribe output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsPrivateSubscribeFailsClosed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"subscribe", "spaceaware-OMM", "--visibility", "private"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("private subscribe unexpectedly succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "verified channel grant required") {
		t.Fatalf("private subscribe error = %v", err)
	}
}

func TestChannelsGrantIssuePrintsScopedGrant(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"grants", "issue", "spaceaware-OMM", "--to", "peer-alpha", "--scope", "subscribe", "--scope", "stream_open"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels grants issue failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"subject=peer-alpha",
		"grantState=verified",
		"scope=subscribe",
		"scope=stream_open",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("grant issue output missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "grantId=grant-") {
		t.Fatalf("grant issue output missing generated grantId:\n%s", body)
	}
}

func TestChannelsGrantIssueAllowsPrivateListScope(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"grants", "issue", "spaceaware-OMM", "--to", "peer-alpha", "--scope", "list_private"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels grants issue failed: %v", err)
	}
	if !strings.Contains(out.String(), "scope=list_private") {
		t.Fatalf("grant output missing list_private scope:\n%s", out.String())
	}
}

func TestChannelsGrantIssueUsesLocalAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channels/spaceaware-OMM/grants" {
			t.Fatalf("unexpected grant request %s %s", r.Method, r.URL.String())
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode grant request: %v", err)
		}
		if body["subject"] != "peer-alpha" {
			t.Fatalf("grant subject = %#v", body["subject"])
		}
		scopes, ok := body["scopes"].([]interface{})
		if !ok || len(scopes) != 2 || scopes[0] != "subscribe" || scopes[1] != "stream_open" {
			t.Fatalf("grant scopes = %#v", body["scopes"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"grantId":"grant-api-1",
			"channelId":"spaceaware-OMM",
			"subject":"peer-alpha",
			"grantState":"verified",
			"scopes":["subscribe","stream_open"],
			"expiresAt":"2026-06-05T00:00:00Z"
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"grants", "issue", "spaceaware-OMM", "--to", "peer-alpha", "--scope", "subscribe", "--scope", "stream_open", "--api-url", server.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels grants issue failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"grantId=grant-api-1",
		"channelId=spaceaware-OMM",
		"subject=peer-alpha",
		"grantState=verified",
		"scope=subscribe",
		"scope=stream_open",
		"expiresAt=2026-06-05T00:00:00Z",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("grant issue output missing %q:\n%s", want, body)
		}
	}
}

func TestChannelsPublishValidatesNativeStreamFile(t *testing.T) {
	t.Parallel()

	streamPath := filepath.Join(t.TempDir(), "stream.bin")
	streamBytes := bytes.Join([][]byte{
		channelCLITestNativeFrame("OMM1", []byte{1, 2, 3}),
		channelCLITestNativeFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	if err := os.WriteFile(streamPath, streamBytes, 0o600); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}

	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"publish", "spaceaware-OMM", "--from", streamPath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels publish failed: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"channelId=spaceaware-OMM",
		"standardCode=OMM",
		"streamBytes=23",
		"streamFrames=2",
		"contentType=application/vnd.sdn.flatbuffers.stream",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels publish output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "base64") || strings.Contains(body, "records=") {
		t.Fatalf("channels publish output exposed JSON/base64 hot path:\n%s", body)
	}
}

func channelCLITestNativeFrame(fileIdentifier string, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(4+len(payload)))
	copy(frame[4:8], []byte(fileIdentifier))
	copy(frame[8:], payload)
	return frame
}
