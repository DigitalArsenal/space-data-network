package deliveryclient

import (
	"bytes"
	"context"
	"errors"
	"testing"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	flatbuffers "github.com/google/flatbuffers/go"
)

func makeKey() []byte {
	k := make([]byte, contentKeySize)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func makeIV() []byte {
	iv := make([]byte, aesGCMIVSize)
	for i := range iv {
		iv[i] = byte(0xf0 + i)
	}
	return iv
}

// encodeTestKMF builds a plaintext $KMF buffer carrying key as KEY_BYTES.
func encodeTestKMF(key []byte) []byte {
	b := flatbuffers.NewBuilder(64)
	keyVec := b.CreateByteVector(key)
	kmf.KMFStart(b)
	kmf.KMFAddKEY_BYTES(b, keyVec)
	root := kmf.KMFEnd(b)
	kmf.FinishKMFBuffer(b, root)
	return b.FinishedBytes()
}

func TestAESGCMBundleRoundTrip(t *testing.T) {
	key := makeKey()
	plaintext := []byte("the decrypted wasm module bytes")

	for _, aad := range [][]byte{nil, []byte("bundle-aad")} {
		bundle, err := EncryptBundleAESGCM(key, makeIV(), plaintext, aad)
		if err != nil {
			t.Fatalf("EncryptBundleAESGCM() error = %v", err)
		}
		got, err := AESGCMBundleDecryptor{AAD: aad}.Decrypt(bundle, key)
		if err != nil {
			t.Fatalf("Decrypt() error = %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("round-trip = %q, want %q", got, plaintext)
		}
	}
}

func TestAESGCMBundleFailures(t *testing.T) {
	key := makeKey()
	bundle, _ := EncryptBundleAESGCM(key, makeIV(), []byte("hello world payload"), nil)

	// Wrong key.
	wrong := makeKey()
	wrong[0] ^= 0xff
	if _, err := (AESGCMBundleDecryptor{}).Decrypt(bundle, wrong); err == nil {
		t.Error("expected auth failure with wrong key")
	}
	// Wrong AAD.
	if _, err := (AESGCMBundleDecryptor{AAD: []byte("x")}).Decrypt(bundle, key); err == nil {
		t.Error("expected auth failure with wrong AAD")
	}
	// Too short.
	if _, err := (AESGCMBundleDecryptor{}).Decrypt([]byte{1, 2, 3}, key); err == nil {
		t.Error("expected error for too-short bundle")
	}
	// Bad key length.
	if _, err := (AESGCMBundleDecryptor{}).Decrypt(bundle, []byte{1, 2, 3}); err == nil {
		t.Error("expected error for non-32-byte key")
	}
}

func TestPlaintextKMFUnwrapper(t *testing.T) {
	key := makeKey()
	grant := &Grant{WrappedContentKeyPayload: encodeTestKMF(key)}

	got, err := PlaintextKMFUnwrapper{}.Unwrap(grant)
	if err != nil {
		t.Fatalf("Unwrap() error = %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Errorf("unwrapped key mismatch")
	}

	// Missing payload.
	if _, err := (PlaintextKMFUnwrapper{}).Unwrap(&Grant{}); err == nil {
		t.Error("expected error for missing payload")
	}
	// Non-KMF payload -> must defer to client-decrypt.
	_, err = PlaintextKMFUnwrapper{}.Unwrap(&Grant{WrappedContentKeyPayload: []byte("not-a-kmf-buffer")})
	if !errors.Is(err, ErrEncryptedContentKeyRequiresClientDecrypt) {
		t.Errorf("error = %v, want ErrEncryptedContentKeyRequiresClientDecrypt", err)
	}
}

type fakeClientDecrypt struct {
	key []byte
	err error
}

func (f fakeClientDecrypt) DecryptArtifact(grantBytes, priv []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(grantBytes) == 0 {
		return nil, errors.New("no grant bytes")
	}
	return f.key, nil
}

func TestClientDecryptUnwrapper(t *testing.T) {
	key := makeKey()
	u := ClientDecryptUnwrapper{
		Client:              fakeClientDecrypt{key: key},
		GrantResponseBytes:  []byte("grant-bytes"),
		RecipientPrivateKey: []byte("priv"),
	}
	got, err := u.Unwrap(nil)
	if err != nil {
		t.Fatalf("Unwrap() error = %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Error("delegated key mismatch")
	}

	// Nil client.
	if _, err := (ClientDecryptUnwrapper{GrantResponseBytes: []byte("g")}).Unwrap(nil); err == nil {
		t.Error("expected error for nil client")
	}
	// Missing grant bytes.
	if _, err := (ClientDecryptUnwrapper{Client: fakeClientDecrypt{key: key}}).Unwrap(nil); err == nil {
		t.Error("expected error for missing grant bytes")
	}
}

func TestUnwrapContentKeyDispatch(t *testing.T) {
	key := makeKey()

	// Plaintext KMF -> native path, no client needed.
	kmfGrant := &Grant{WrappedContentKeyPayload: encodeTestKMF(key)}
	got, err := UnwrapContentKey(kmfGrant, nil, nil, nil)
	if err != nil {
		t.Fatalf("native path error = %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Error("native path key mismatch")
	}

	// Encrypted envelope -> client-decrypt path.
	encGrant := &Grant{
		WrappedContentKeyPayload:  []byte("opaque-enc-payload"),
		WrappedContentKeyRootType: "ENC",
		HasWrappedContentKey:      true,
	}
	got, err = UnwrapContentKey(encGrant, []byte("grant-bytes"), []byte("priv"), fakeClientDecrypt{key: key})
	if err != nil {
		t.Fatalf("client-decrypt path error = %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Error("client-decrypt path key mismatch")
	}

	// Encrypted envelope but no client supplied -> explicit error.
	if _, err := UnwrapContentKey(encGrant, nil, nil, nil); !errors.Is(err, ErrEncryptedContentKeyRequiresClientDecrypt) {
		t.Errorf("error = %v, want ErrEncryptedContentKeyRequiresClientDecrypt", err)
	}
}

// TestConsumerPullWithRealCrypto drives the full consumer flow with the real
// AES-GCM bundle decryptor and plaintext-KMF unwrapper: the provider seals a
// bundle and delivers the content key as a plaintext KMF, and the consumer
// recovers the original bytes.
func TestConsumerPullWithRealCrypto(t *testing.T) {
	key := makeKey()
	wasm := []byte("\x00asm\x01\x00\x00\x00 pretend module body")
	sealed, err := EncryptBundleAESGCM(key, makeIV(), wasm, nil)
	if err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{
		peerID:       "peerP",
		moduleCID:    "bafyrealcid",
		kmfPayload:   encodeTestKMF(key),
		sealedBundle: sealed,
	}
	c, err := NewConsumer(provider, testIdentity(nil))
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.PullModule(context.Background(), Provider{PeerID: "peerP"}, testPullParams(),
		PlaintextKMFUnwrapper{}, AESGCMBundleDecryptor{})
	if err != nil {
		t.Fatalf("PullModule() error = %v", err)
	}
	if !bytes.Equal(got, wasm) {
		t.Errorf("recovered bundle = %q, want %q", got, wasm)
	}
}
