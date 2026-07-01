package deliveryclient

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	lpf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
)

// fakeProvider is an in-process module-delivery provider: it answers an LCH
// challenge request with a challenge, an LPF proof with an LGR grant, and serves
// the encrypted bundle by CID. It records what it emitted/received so tests can
// assert the consumer signed the exact challenge bytes.
type fakeProvider struct {
	peerID        string
	moduleCID     string
	deny          bool
	fetchErr      error
	challengeSent []byte
	proofReceived []byte
}

func (f *fakeProvider) Dial(_ context.Context, _ string, request []byte) ([]byte, error) {
	switch {
	case lch.LCHBufferHasIdentifier(request):
		m := lch.GetRootAsLCH(request, 0)
		f.challengeSent = encodeTestChallengeResponse(
			string(m.REQUEST_ID()), string(m.MODULE_ID()), string(m.MODULE_VERSION()),
			f.peerID, []byte{1, 2, 3, 4}, 9_000_000, false,
		)
		return f.challengeSent, nil
	case lpf.LPFBufferHasIdentifier(request):
		f.proofReceived = append([]byte(nil), request...)
		m := lpf.GetRootAsLPF(request, 0)
		return encodeTestGrant(testGrant{
			denied:        f.deny,
			requestID:     string(m.REQUEST_ID()),
			moduleID:      string(m.MODULE_ID()),
			moduleVersion: string(m.MODULE_VERSION()),
			grantedDomain: "orbpro.default", grantedTimeoutMs: 30_000, expiresAtMs: 9_000_000,
			grantStatus: "granted", denialReason: "not authorized", moduleCID: f.moduleCID,
			wrappedPayload: []byte{0x11}, verifierPubKey: []byte{0x22}, providerSig: []byte{0x33},
		}), nil
	default:
		return nil, fmt.Errorf("fakeProvider: unknown request frame")
	}
}

func (f *fakeProvider) FetchCID(_ context.Context, cid string) ([]byte, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	if cid != f.moduleCID {
		return nil, fmt.Errorf("fakeProvider: unexpected cid %q", cid)
	}
	return []byte("ENCRYPTED-WASM-BYTES"), nil
}

type fakeUnwrapper struct{ key []byte }

func (u fakeUnwrapper) Unwrap(g *Grant) ([]byte, error) {
	if len(g.WrappedContentKeyPayload) == 0 {
		return nil, fmt.Errorf("no wrapped payload")
	}
	return u.key, nil
}

type fakeDecryptor struct {
	key       []byte
	plaintext []byte
}

func (d fakeDecryptor) Decrypt(_, key []byte) ([]byte, error) {
	if !bytes.Equal(key, d.key) {
		return nil, fmt.Errorf("wrong content key")
	}
	return d.plaintext, nil
}

func testIdentity(sink *[][]byte) RequesterIdentity {
	return RequesterIdentity{
		PeerID:             "peerA",
		XPub:               "xpubA",
		SigningPublicKey:   []byte{1, 2, 3},
		EphemeralPublicKey: []byte{4, 5, 6},
		Sign: func(msg []byte) ([]byte, error) {
			if sink != nil {
				*sink = append(*sink, append([]byte(nil), msg...))
			}
			return []byte{0x99}, nil
		},
	}
}

func testPullParams() PullParams {
	return PullParams{
		RequestID: "req-1", ModuleID: "com.orbpro.x", ModuleVersion: "1.0.0",
		RequestedDomain: "orbpro.default", RequestedTimeoutMs: 30_000, NowMs: 1000,
	}
}

func TestConsumerPullModuleEndToEnd(t *testing.T) {
	provider := &fakeProvider{peerID: "peerP", moduleCID: "bafycid"}
	var signed [][]byte
	c, err := NewConsumer(provider, testIdentity(&signed))
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("content-key")

	plaintext, err := c.PullModule(context.Background(), Provider{PeerID: "peerP"}, testPullParams(),
		fakeUnwrapper{key: key}, fakeDecryptor{key: key, plaintext: []byte("WASM")})
	if err != nil {
		t.Fatalf("PullModule() error = %v", err)
	}
	if string(plaintext) != "WASM" {
		t.Errorf("plaintext = %q, want WASM", plaintext)
	}

	// The consumer must sign the exact challenge response bytes the provider sent.
	if len(signed) != 1 || !bytes.Equal(signed[0], provider.challengeSent) {
		t.Fatal("consumer must sign the exact challenge response bytes")
	}
	// The proof frame must carry that signature.
	pm := lpf.GetRootAsLPF(provider.proofReceived, 0)
	if !bytes.Equal(pm.SignatureBytes(), []byte{0x99}) {
		t.Error("proof frame did not carry the requester signature")
	}
	if got := string(pm.REQUEST_ID()); got != "req-1" {
		t.Errorf("proof REQUEST_ID = %q", got)
	}
}

func TestConsumerRequestGrantDenied(t *testing.T) {
	provider := &fakeProvider{peerID: "peerP", moduleCID: "bafycid", deny: true}
	c, _ := NewConsumer(provider, testIdentity(nil))
	_, err := c.RequestGrant(context.Background(), Provider{PeerID: "peerP"}, testPullParams())
	if err == nil {
		t.Fatal("expected error for a denied grant")
	}
}

func TestConsumerFetchError(t *testing.T) {
	provider := &fakeProvider{peerID: "peerP", moduleCID: "bafycid", fetchErr: fmt.Errorf("cid unavailable")}
	c, _ := NewConsumer(provider, testIdentity(nil))
	key := []byte("content-key")
	_, err := c.PullModule(context.Background(), Provider{PeerID: "peerP"}, testPullParams(),
		fakeUnwrapper{key: key}, fakeDecryptor{key: key, plaintext: []byte("WASM")})
	if err == nil {
		t.Fatal("expected error when CID fetch fails")
	}
}

func TestConsumerWrongContentKeyFailsDecrypt(t *testing.T) {
	provider := &fakeProvider{peerID: "peerP", moduleCID: "bafycid"}
	c, _ := NewConsumer(provider, testIdentity(nil))
	// Unwrapper yields a different key than the decryptor expects.
	_, err := c.PullModule(context.Background(), Provider{PeerID: "peerP"}, testPullParams(),
		fakeUnwrapper{key: []byte("wrong")}, fakeDecryptor{key: []byte("right"), plaintext: []byte("WASM")})
	if err == nil {
		t.Fatal("expected decrypt failure on content-key mismatch")
	}
}

func TestNewConsumerValidation(t *testing.T) {
	if _, err := NewConsumer(nil, testIdentity(nil)); err == nil {
		t.Error("expected error for nil transport")
	}
	if _, err := NewConsumer(&fakeProvider{}, RequesterIdentity{}); err == nil {
		t.Error("expected error for missing Sign func")
	}
}

func TestConsumerFetchAndDecryptRequiresCrypto(t *testing.T) {
	c, _ := NewConsumer(&fakeProvider{moduleCID: "x"}, testIdentity(nil))
	grant := &Grant{MessageType: grantMessageTypeGranted, ModuleDescriptorCID: "x"}
	if _, err := c.FetchAndDecrypt(context.Background(), grant, nil, nil); err == nil {
		t.Error("expected error when unwrapper/decryptor are nil")
	}
}
