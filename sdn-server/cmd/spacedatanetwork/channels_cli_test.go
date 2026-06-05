package main

import (
	"bytes"
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
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels list output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, string([]byte{'.', 'f', 'b', 's'})) {
		t.Fatalf("channels list output exposed internal schema suffix:\n%s", body)
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
		"providerPeer=",
		"localRows=",
		"remoteRows=",
		"syncedRows=",
		"missingRows=",
		"pinnedRows=",
		"syncedBytes=",
		"throughputBytesPerSecond=",
		"wireSpeedUtilization=",
		"grantState=",
		"encryptionState=",
		"lastVerifiedUpdate=",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("channels monitor output missing %q:\n%s", want, body)
		}
	}
}
