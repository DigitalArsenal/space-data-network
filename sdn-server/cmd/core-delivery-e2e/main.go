// Command core-delivery-e2e proves the SEC-001 hotfixes and the core-delivery
// admission rules against a live node, from outside the process:
//
//   - a root-key sign-in is refused when the request crossed a proxy hop and
//     admitted from the node's own box;
//   - the sealed /api/rpc transport refuses a root signer through a hop, and
//     refuses an admin row bound to the leaked SEC-001 xpub;
//   - a module publish by the leaked identity is refused, while a control
//     admin's publish lands;
//   - the open-policy bucket refuses past its burst, one connection is capped
//     at 32 in-flight handshakes, and a superseded version is served during its
//     grace and refused after it.
//
// `--prepare` writes the fixture (plugin catalog, grant policy, capability
// approvals for the licensing WASM, a throwaway mnemonic) before the daemon
// starts; run.sh drives both phases.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sealed"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

const (
	openModule   = "com.e2e.open"
	paidModule   = "com.e2e.paid"
	retireModule = "com.e2e.retire"
	requester    = "xpub-test-requester"
	// The leaked SEC-001 identity's xpub (public; internal/auth denylist).
	leakedXPub  = "xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa"
	controlXPub = "xpub-e2e-control-admin"
	hop         = "for=203.0.113.7;proto=https"
)

var (
	prepare   = flag.Bool("prepare", false, "write the fixture and exit")
	storage   = flag.String("storage", "", "node storage.path (prepare)")
	licWASM   = flag.String("licensing-wasm", "", "licensing WASM the node loads (prepare)")
	mnemonic  = flag.String("mnemonic-file", "", "fixture mnemonic file (written by prepare, read by the run)")
	hdWallet  = flag.String("hd-wallet-out", "", "write the embedded hd-wallet WASM here, for `key import` (prepare)")
	adminURL  = flag.String("admin", "", "base URL of the node's admin API")
	daemonLog = flag.String("daemon-log", "", "the daemon's log file")
	timeout   = flag.Duration("timeout", 180*time.Second, "overall timeout")
)

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *prepare {
		if err := writeFixture(ctx); err != nil {
			fmt.Printf("CORE-DELIVERY-E2E FAIL: prepare: %v\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(run(ctx))
}

// ---- fixture -------------------------------------------------------------

func writeFixture(ctx context.Context) error {
	if *storage == "" || *licWASM == "" || *mnemonic == "" {
		return fmt.Errorf("--storage, --licensing-wasm and --mnemonic-file are required")
	}
	root := license.DefaultPluginRoot(*storage)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	entry := func(id string, xpubs ...string) license.PluginCatalogEntry {
		return license.PluginCatalogEntry{
			ID: id, Version: "1.0.0", RequiredScope: "orbpro.default",
			EncryptedPath: id + ".enc", KeyPath: id + ".key",
			ContentType: "application/wasm+encrypted", MaxGrantTimeoutMs: 30_000,
			AllowedXpubs: xpubs,
		}
	}
	entries := []license.PluginCatalogEntry{entry(openModule), entry(paidModule, requester), entry(retireModule, requester)}
	for _, e := range entries {
		if err := os.WriteFile(filepath.Join(root, e.EncryptedPath), []byte("encrypted-bundle-"+e.ID), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, e.KeyPath), []byte(base64.RawStdEncoding.EncodeToString(random(32))), 0o600); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(root, "catalog.json"), license.PluginCatalogFile{Plugins: entries}); err != nil {
		return err
	}
	grace := 2
	policy := license.GrantPolicyConfig{
		Modules:                      []license.GrantPolicyRule{{Match: openModule, Policy: license.GrantPolicyOpen}},
		DeliveryLimits:               &license.DeliveryLimitsConfig{OpenRatePerSecond: 0.001, OpenBurst: 3},
		RetireSupersededAfterSeconds: &grace,
	}
	if err := writeJSON(filepath.Join(root, license.GrantPolicyFileName), policy); err != nil {
		return err
	}

	// The licensing module's sensitive capabilities, approved for its hash
	// exactly as the operator does on host-01.
	raw, err := os.ReadFile(*licWASM)
	if err != nil {
		return err
	}
	store, err := modulert.NewCapabilityPolicyStore(modulert.DefaultCapabilityPolicyPath(*storage))
	if err != nil {
		return err
	}
	for _, c := range []string{"ipfs", "protocol_dial", "wallet_sign"} {
		if _, err := store.Approve(modulert.CapabilityApproval{
			ModuleHash: modulert.ContentHashHex(raw), Capability: c, PluginID: "licensing",
			ApprovedBy: "core-delivery-e2e", Note: "fixture",
		}); err != nil {
			return err
		}
	}

	// A throwaway node identity, so the run can sign as this node's root.
	hw, err := wasm.NewHDWalletModuleFromBytes(ctx, wasm.EmbeddedHDWalletWasm())
	if err != nil {
		return err
	}
	defer hw.Close(ctx)
	if err := hw.InjectEntropy(ctx, random(64)); err != nil {
		return err
	}
	if *hdWallet != "" {
		if err := os.WriteFile(*hdWallet, wasm.EmbeddedHDWalletWasm(), 0o644); err != nil {
			return err
		}
	}
	phrase, _, err := hw.GenerateNewIdentity(ctx, 24)
	if err != nil {
		return err
	}
	return os.WriteFile(*mnemonic, []byte(phrase+"\n"), 0o600)
}

// ---- run -----------------------------------------------------------------

type result struct {
	name string
	pass bool
}

func run(ctx context.Context) int {
	var results []result
	add := func(name string, pass bool, note string) {
		results = append(results, result{name, pass})
		status := "PASS"
		if !pass {
			status = "FAIL"
		}
		fmt.Printf("%s %s: %s\n", status, name, note)
	}
	base := strings.TrimRight(*adminURL, "/")

	var id struct {
		PeerID       string `json:"peer_id"`
		PublisherKey string `json:"publisher_key"`
	}
	if err := getJSON(ctx, base+"/api/v1/id", nil, &id); err != nil {
		fmt.Printf("CORE-DELIVERY-E2E FAIL: /api/v1/id: %v\n", err)
		return 1
	}
	rootPriv, err := rootKey(ctx)
	if err != nil {
		fmt.Printf("CORE-DELIVERY-E2E FAIL: fixture root key: %v\n", err)
		return 1
	}
	rootPub := hex.EncodeToString(rootPriv.Public().(ed25519.PublicKey))
	add("fixture/root-is-the-node", strings.EqualFold(rootPub, id.PublisherKey), "the fixture key is the node's publisher key "+short(id.PublisherKey))

	// SEC-001 hotfix 1: root sign-in only from the box.
	code, body := challenge(ctx, base, rootPub, true)
	add("hotfix1/root-challenge-through-hop", code == http.StatusForbidden && strings.Contains(body, "authentication_failed"), fmt.Sprintf("%d %s", code, clip(body)))
	add("hotfix1/root-log", logHas("SEC-001 hotfix: refused a root-key auth challenge"), "the node logged the refusal")
	cookie, err := rootSignIn(ctx, base, rootPub, rootPriv)
	add("hotfix1/root-sign-in-on-box", err == nil && cookie != nil, fmt.Sprintf("loopback root sign-in: %v", err))
	if cookie == nil {
		return finish(results)
	}

	// Two admins: one bound to the leaked xpub, one control.
	leakPub, leakPriv, _ := ed25519.GenerateKey(rand.Reader)
	ctrlPub, ctrlPriv, _ := ed25519.GenerateKey(rand.Reader)
	for _, u := range []struct {
		xpub, name string
		pub        ed25519.PublicKey
	}{{leakedXPub, "e2e leaked", leakPub}, {controlXPub, "e2e control", ctrlPub}} {
		c, b := postJSON(ctx, base+"/api/auth/users", cookie, map[string]string{
			"xpub": u.xpub, "name": u.name, "trust_level": "admin", "signing_pubkey_hex": hex.EncodeToString(u.pub),
		})
		add("fixture/enrol "+u.name, c/100 == 2, fmt.Sprintf("%d %s", c, clip(b)))
	}

	// SEC-001 hotfix 3: sealed /api/rpc.
	var info struct {
		Sealed struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"sealed_transport"`
	}
	_ = getJSON(ctx, base+"/api/node/info", nil, &info)
	keys, err := sealed.ReadNodeKeys(ctx, http.DefaultClient, base, info.Sealed.Fingerprint)
	if err != nil {
		add("hotfix3/sealed-keys", false, err.Error())
	} else {
		hopClient := &http.Client{Transport: hopTransport{http.DefaultTransport}}
		st := sealedStatus(ctx, hopClient, base, keys, rootPriv)
		add("hotfix3/root-signer-through-hop", st == http.StatusForbidden || st == http.StatusUnauthorized, fmt.Sprintf("status %d", st))
		st = sealedStatus(ctx, http.DefaultClient, base, keys, rootPriv)
		add("hotfix3/root-signer-on-box", st == http.StatusOK, fmt.Sprintf("status %d", st))
		st = sealedStatus(ctx, http.DefaultClient, base, keys, leakPriv)
		add("hotfix3/leaked-account-refused", st == http.StatusForbidden || st == http.StatusUnauthorized, fmt.Sprintf("status %d", st))
		st = sealedStatus(ctx, http.DefaultClient, base, keys, ctrlPriv)
		add("hotfix3/control-admin-admitted", st == http.StatusOK, fmt.Sprintf("status %d", st))
	}

	target, err := dialTarget(ctx, base, id.PeerID)
	if err != nil {
		add("libp2p/target", false, err.Error())
		return finish(results)
	}
	newPeer := func() host.Host {
		h, err := libp2p.New(libp2p.NoListenAddrs)
		if err == nil {
			h.Peerstore().AddAddrs(target.ID, target.Addrs, time.Hour)
			err = h.Connect(ctx, target)
		}
		if err != nil {
			fmt.Printf("CORE-DELIVERY-E2E FAIL: libp2p peer: %v\n", err)
			os.Exit(1)
		}
		return h
	}

	// Core delivery B: the open bucket (burst 3) and the per-connection cap.
	a := newPeer()
	defer a.Close()
	issued := 0
	for i := 0; i < 3; i++ {
		if ok, _ := lchChallenge(ctx, a, target.ID, fmt.Sprintf("open-%d", i), openModule, "1.0.0"); ok {
			issued++
		}
	}
	ok, code4 := lchChallenge(ctx, a, target.ID, "open-over", openModule, "1.0.0")
	add("admission/open-burst", issued == 3 && !ok && code4 == license.DeliveryRefusedRateLimited, fmt.Sprintf("%d/3 issued, 4th %q", issued, code4))

	b := newPeer()
	defer b.Close()
	issued = 0
	for i := 0; i < license.MaxInFlightHandshakesPerConnection; i++ {
		if ok, _ := lchChallenge(ctx, b, target.ID, fmt.Sprintf("paid-%d", i), paidModule, "1.0.0"); ok {
			issued++
		}
	}
	ok, code33 := lchChallenge(ctx, b, target.ID, "paid-over", paidModule, "1.0.0")
	add("admission/per-connection-cap", issued == license.MaxInFlightHandshakesPerConnection && !ok && code33 == license.DeliveryRefusedTooManyInFlight,
		fmt.Sprintf("%d/%d issued, next %q", issued, license.MaxInFlightHandshakesPerConnection, code33))

	// SEC-001 hotfix 2: the leaked identity never authorizes a publish.
	resp := publish(ctx, a, target.ID, leakedXPub, leakPub, leakPriv)
	add("hotfix2/leaked-publish-refused", !resp.OK && strings.Contains(resp.Error, "not an admin"), "error: "+resp.Error)
	add("hotfix2/leaked-publish-log", logHas("SEC-001 hotfix: refused a module publish by the leaked identity"), "the node logged the refusal")
	resp = publish(ctx, a, target.ID, controlXPub, ctrlPub, ctrlPriv)
	add("hotfix2/control-publish-lands", resp.OK, fmt.Sprintf("ok=%v %s", resp.OK, resp.Error))

	// Core delivery E: v1 is served during the 2 s grace, refused after it.
	c := newPeer()
	defer c.Close()
	v1Grace, _ := lchChallenge(ctx, c, target.ID, "grace-v1", retireModule, "1.0.0")
	v2Grace, _ := lchChallenge(ctx, c, target.ID, "grace-v2", retireModule, "2.0.0")
	add("retire/both-served-in-grace", v1Grace && v2Grace, fmt.Sprintf("v1=%v v2=%v", v1Grace, v2Grace))
	time.Sleep(3500 * time.Millisecond)
	v1After, codeAfter := lchChallenge(ctx, c, target.ID, "after-v1", retireModule, "1.0.0")
	v2After, _ := lchChallenge(ctx, c, target.ID, "after-v2", retireModule, "2.0.0")
	add("retire/old-version-refused", !v1After && codeAfter == license.DeliveryRefusedVersionRetired, fmt.Sprintf("v1 %q", codeAfter))
	add("retire/new-version-served", v2After, fmt.Sprintf("v2=%v", v2After))
	add("retire/log", logHas("dir=retire module="+retireModule+" version=1.0.0 outcome=scheduled"), "the node scheduled the retirement")

	hc, _ := getStatus(ctx, base+"/api/v1/health")
	add("node/alive", hc == http.StatusOK, fmt.Sprintf("health %d", hc))
	return finish(results)
}

func finish(results []result) int {
	fails := 0
	for _, r := range results {
		if !r.pass {
			fails++
		}
	}
	if fails > 0 {
		fmt.Printf("CORE-DELIVERY-E2E FAIL (%d/%d cases)\n", fails, len(results))
		return 1
	}
	fmt.Printf("CORE-DELIVERY-E2E PASS (%d cases)\n", len(results))
	return 0
}

// ---- auth and sealed ------------------------------------------------------

func rootKey(ctx context.Context) (ed25519.PrivateKey, error) {
	phrase, err := os.ReadFile(*mnemonic)
	if err != nil {
		return nil, err
	}
	hw, err := wasm.NewHDWalletModuleFromBytes(ctx, wasm.EmbeddedHDWalletWasm())
	if err != nil {
		return nil, err
	}
	defer hw.Close(ctx)
	ident, err := hw.IdentityFromMnemonic(ctx, strings.TrimSpace(string(phrase)), "", 0)
	if err != nil {
		return nil, err
	}
	seed, err := ident.RawSigningKey()
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func challenge(ctx context.Context, base, pubHex string, throughHop bool) (int, string) {
	hdr := map[string]string{}
	if throughHop {
		hdr["Forwarded"] = hop
	}
	return postWith(ctx, base+"/api/auth/challenge", nil, hdr, map[string]any{"client_pubkey_hex": pubHex, "ts": time.Now().Unix()})
}

func rootSignIn(ctx context.Context, base, pubHex string, priv ed25519.PrivateKey) (*http.Cookie, error) {
	code, body := challenge(ctx, base, pubHex, false)
	if code != http.StatusOK {
		return nil, fmt.Errorf("challenge %d %s", code, clip(body))
	}
	var ch struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal([]byte(body), &ch); err != nil {
		return nil, err
	}
	raw, err := base64.RawStdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]string{
		"challenge_id": ch.ChallengeID, "client_pubkey_hex": pubHex, "challenge": ch.Challenge,
		"signature_hex": hex.EncodeToString(ed25519.Sign(priv, raw)),
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/auth/verify", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verify %d %s", resp.StatusCode, clip(string(b)))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "sdn_wallet_session" {
			return c, nil
		}
	}
	return nil, fmt.Errorf("verify set no session cookie")
}

type hopTransport struct{ next http.RoundTripper }

func (t hopTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Forwarded", hop)
	return t.next.RoundTrip(r)
}

// sealedStatus is the status a sealed admin read (GET /api/auth/users) ends
// with: the outer status when the transport refused, else the inner one.
func sealedStatus(ctx context.Context, client *http.Client, base string, keys sealed.NodeKeys, signer ed25519.PrivateKey) int {
	res, err := sealed.Call(ctx, client, base, keys, signer, http.MethodGet, "/api/auth/users", nil, "")
	if err != nil {
		fmt.Printf("  sealed call: %v\n", err)
		return 0
	}
	return res.Status
}

// ---- libp2p ----------------------------------------------------------------

func dialTarget(ctx context.Context, base, peerID string) (peer.AddrInfo, error) {
	var info struct {
		PeerID          string   `json:"peer_id"`
		ListenAddresses []string `json:"listen_addresses"`
	}
	if err := getJSON(ctx, base+"/api/node/info", nil, &info); err != nil {
		return peer.AddrInfo{}, err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	for _, a := range info.ListenAddresses {
		if strings.Contains(a, "/ip4/127.0.0.1/tcp/") && !strings.Contains(a, "/ws") && !strings.Contains(a, "p2p-circuit") {
			ma, err := multiaddr.NewMultiaddr(a)
			if err == nil {
				return peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{ma}}, nil
			}
		}
	}
	return peer.AddrInfo{}, fmt.Errorf("no loopback tcp address in %v", info.ListenAddresses)
}

// lchChallenge sends one $LCH challenge request and reports whether a
// challenge was issued, or the refusal code.
func lchChallenge(ctx context.Context, h host.Host, to peer.ID, requestID, module, version string) (bool, string) {
	s, err := h.NewStream(ctx, to, protocol.ID(license.ModuleDeliveryProtocolID))
	if err != nil {
		return false, "stream: " + err.Error()
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := s.Write(challengeFrame(requestID, module, version, h.ID().String(), to.String())); err != nil {
		return false, "write: " + err.Error()
	}
	_ = s.CloseWrite()
	answer, _ := io.ReadAll(io.LimitReader(s, 1<<20))
	if len(answer) < 8 || !lch.LCHBufferHasIdentifier(answer) {
		return false, fmt.Sprintf("no $LCH answer (%d bytes)", len(answer))
	}
	msg := lch.GetRootAsLCH(answer, 0)
	code := string(msg.ERROR_CODE())
	return msg.MESSAGE_TYPE() == lch.EnumValueslicensingChallengeMessageType["Response"] && code == "", code
}

func challengeFrame(requestID, module, version, requesterPeer, providerPeer string) []byte {
	b := flatbuffers.NewBuilder(512)
	reqID := b.CreateString(requestID)
	mod := b.CreateString(module)
	ver := b.CreateString(version)
	peerID := b.CreateString(requesterPeer)
	xpub := b.CreateString(requester)
	signing := b.CreateByteVector(random(32))
	ephemeral := b.CreateByteVector(random(32))
	domain := b.CreateString("localhost")
	provider := b.CreateString(providerPeer)
	b.StartObject(17)
	b.PrependByteSlot(0, 0, 0)
	b.PrependByteSlot(1, 0, 0)
	b.PrependUOffsetTSlot(2, reqID, 0)
	b.PrependUOffsetTSlot(3, mod, 0)
	b.PrependUOffsetTSlot(4, ver, 0)
	b.PrependUOffsetTSlot(5, peerID, 0)
	b.PrependUOffsetTSlot(6, xpub, 0)
	b.PrependUOffsetTSlot(7, signing, 0)
	b.PrependUOffsetTSlot(8, ephemeral, 0)
	b.PrependUOffsetTSlot(9, domain, 0)
	b.PrependUint64Slot(10, 30_000, 0)
	b.PrependUint64Slot(11, uint64(time.Now().UnixMilli()), 0)
	b.PrependUOffsetTSlot(14, provider, 0)
	b.FinishWithFileIdentifier(b.EndObject(), []byte("$LCH"))
	return b.FinishedBytes()
}

// publish sends one signed module-publish request: com.e2e.retire 2.0.0.
func publish(ctx context.Context, h host.Host, to peer.ID, xpub string, pub ed25519.PublicKey, priv ed25519.PrivateKey) license.ModulePublishResponse {
	req := license.ModulePublishRequest{
		Type: license.ModulePublishRequestType, IssuedAtMs: time.Now().UnixMilli(), Nonce: hex.EncodeToString(random(16)),
		Modules: []license.ModulePublishEntry{{
			ID: retireModule, Version: "2.0.0", RequiredScope: "orbpro.default",
			EncryptedBundle: []byte("encrypted-bundle-2.0.0"), KeyMaterial: []byte(base64.RawStdEncoding.EncodeToString(random(32))),
			ContentType: "application/wasm+encrypted", AllowedXpubs: []string{requester}, MaxGrantTimeoutMs: 30_000,
		}},
	}
	if err := license.SignModulePublishRequest(&req, xpub, pub, priv); err != nil {
		return license.ModulePublishResponse{Error: "sign: " + err.Error()}
	}
	s, err := h.NewStream(ctx, to, protocol.ID(license.ModulePublishProtocolID))
	if err != nil {
		return license.ModulePublishResponse{Error: "stream: " + err.Error()}
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(60 * time.Second))
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return license.ModulePublishResponse{Error: "write: " + err.Error()}
	}
	_ = s.CloseWrite()
	var resp license.ModulePublishResponse
	raw, _ := io.ReadAll(io.LimitReader(s, 1<<20))
	if err := json.Unmarshal(bytes.TrimSpace(raw), &resp); err != nil {
		return license.ModulePublishResponse{Error: fmt.Sprintf("answer %q: %v", clip(string(raw)), err)}
	}
	return resp
}

// ---- helpers ---------------------------------------------------------------

func logHas(needle string) bool {
	if *daemonLog == "" {
		return false
	}
	for i := 0; i < 10; i++ {
		raw, _ := os.ReadFile(*daemonLog)
		if bytes.Contains(raw, []byte(needle)) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func getJSON(ctx context.Context, url string, cookie *http.Cookie, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func getStatus(ctx context.Context, url string) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func postJSON(ctx context.Context, url string, cookie *http.Cookie, body any) (int, string) {
	return postWith(ctx, url, cookie, nil, body)
}

func postWith(ctx context.Context, url string, cookie *http.Cookie, hdr map[string]string, body any) (int, string) {
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, string(b)
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
