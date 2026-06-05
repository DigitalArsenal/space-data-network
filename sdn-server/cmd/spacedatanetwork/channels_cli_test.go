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
