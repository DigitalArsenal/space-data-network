package node

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// The delivery admission gate, end to end: the real licensing WASM key server,
// started by the plugin manager on a real libp2p host exactly as the node starts
// it, and a browser on its own connection sending $LCH requests over the
// module-delivery protocol. The limits come from grant-policy.json.
//
// Not parallel: the gate is the node's one gate, and the other licensing tests
// reconfigure it when they bootstrap.
func TestDeliveryAdmissionFrontsTheRealKeyServer(t *testing.T) {
	const (
		openCore = "com.orbpro.sgp4"
		paid     = "com.orbpro.hpop"
		version  = "1.0.0"
	)
	t.Cleanup(func() { licensingDeliveryAdmission.Configure(license.DefaultDeliveryLimits(), nil) })

	entry := func(id string, xpubs ...string) license.PluginCatalogEntry {
		return license.PluginCatalogEntry{
			ID: id, Version: version, RequiredScope: "orbpro.default",
			EncryptedPath: id + ".enc", KeyPath: id + ".key",
			ContentType: "application/wasm+encrypted", MaxGrantTimeoutMs: 30_000,
			AllowedXpubs: xpubs,
		}
	}
	reg := writeTestPluginRegistryWithGrantPolicy(t,
		&license.GrantPolicyConfig{
			Modules: []license.GrantPolicyRule{{Match: openCore, Policy: license.GrantPolicyOpen}},
			// Three open frames, and effectively no refill inside the test.
			DeliveryLimits: &license.DeliveryLimitsConfig{OpenRatePerSecond: 0.001, OpenBurst: 3},
		},
		entry(openCore),
		// buildChallengeRequestFrame claims this xpub; the key server's
		// early allowlist filter admits it at challenge time.
		entry(paid, "xpub-test-requester"),
	)

	mod := newLicensingTestModule(t)
	if err := bootstrapLicensingModule(mod, reg); err != nil {
		t.Fatalf("bootstrapLicensingModule: %v", err)
	}

	provider, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New(provider): %v", err)
	}
	defer provider.Close()

	manager := plugins.New()
	if err := manager.Register(mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := manager.StartAll(context.Background(), plugins.RuntimeContext{Host: provider, BaseDataPath: t.TempDir()}); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	defer manager.Close()

	browser, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("libp2p.New(browser): %v", err)
	}
	defer browser.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := browser.Connect(ctx, peer.AddrInfo{ID: provider.ID(), Addrs: provider.Addrs()}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	challenge := func(requestID, moduleID string) (issued bool, code string) {
		t.Helper()
		answer := exchangeDeliveryFrame(t, browser, provider, buildChallengeRequestFrame(
			requestID, moduleID, version, "requester.orbpro.test", "localhost", "provider.orbpro.test",
		))
		if len(answer) < 8 || !lch.LCHBufferHasIdentifier(answer) {
			t.Fatalf("%s: the provider answered %d bytes, not an $LCH", requestID, len(answer))
		}
		msg := lch.GetRootAsLCH(answer, 0)
		return msg.MESSAGE_TYPE() == lch.EnumValueslicensingChallengeMessageType["Response"], string(msg.ERROR_CODE())
	}

	for i := 0; i < 3; i++ {
		if issued, code := challenge(fmt.Sprintf("open-%d", i), openCore); !issued {
			t.Fatalf("open challenge %d inside the burst: the key server refused %q", i, code)
		}
	}
	if issued, code := challenge("open-over", openCore); issued || code != license.DeliveryRefusedRateLimited {
		t.Fatalf("open challenge past the configured burst of 3: issued=%v code=%q, want refused %q (no host limit in front of the key server)",
			issued, code, license.DeliveryRefusedRateLimited)
	}
	// The paid lane is untouched by an exhausted open bucket, and the key
	// server really issues its challenge.
	for i := 0; i < 3; i++ {
		if issued, code := challenge(fmt.Sprintf("paid-%d", i), paid); !issued {
			t.Fatalf("paid challenge %d while open is rate limited: refused %q", i, code)
		}
	}
	stats := licensingDeliveryAdmission.Stats()
	if stats.OpenPending != 3 || stats.TotalPending != 6 {
		t.Fatalf("the gate tracks %d open / %d total pending, want 3 / 6", stats.OpenPending, stats.TotalPending)
	}

	// The gate wraps the protocol the module runtime serves delivery on.
	if license.ModuleDeliveryProtocolID != modulert.ModuleDeliveryWireID {
		t.Fatalf("gate protocol %q, module runtime serves %q", license.ModuleDeliveryProtocolID, modulert.ModuleDeliveryWireID)
	}

	// The challenge TTL the key server is configured with is the one the gate
	// expires pending handshakes on.
	if licensingChallengeTTLMS != license.DeliveryChallengeTTL.Milliseconds() || licensingChallengeTTLMS != 30_000 {
		t.Fatalf("challenge TTL: key server %d ms, gate %s; both must be 30 s", licensingChallengeTTLMS, license.DeliveryChallengeTTL)
	}
}

func exchangeDeliveryFrame(t *testing.T, from, to host.Host, frame []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := from.NewStream(ctx, to.ID(), protocol.ID(license.ModuleDeliveryProtocolID))
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer s.Close()
	if _, err := s.Write(frame); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	answer, _ := io.ReadAll(s)
	return answer
}
