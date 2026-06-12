package api

import (
	"encoding/hex"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
)

func TestFlatBuffersEncryptedNativeStreamDecryptorDecryptsJSVector(t *testing.T) {
	t.Parallel()

	recipientPrivate := decodeHexFixture(t, "b096fac6064d1777e18c58179c10386d11ba04f9fc155bf1888fed9fab2cea7c")
	ciphertext := decodeHexFixture(t, "bbd30fac58a41b0a11ee4c")
	wantPlaintext := decodeHexFixture(t, "070000004f4d4d31010203")
	decryptor := NewFlatBuffersEncryptedNativeStreamDecryptor(recipientPrivate)

	plaintext, err := decryptor.DecryptNativeStream(EncryptedNativeStreamDecryptRequest{
		Channel: channels.ChannelID{
			ChannelID:    "spaceaware-OMM",
			SourceID:     "spaceaware",
			StandardCode: "OMM",
		},
		Header: EncryptedNativeStreamHeader{
			Algorithm:       "x25519",
			Context:         "spaceaware-OMM",
			SenderPublicKey: "5f8bfd2b52f392a5bd000509945ac8ff840974f0bab1c918cbec18869f79b75c",
			NonceStart:      "00112233445566778899aabb",
		},
		RecordIndex: 7,
		Ciphertext:  ciphertext,
	})
	if err != nil {
		t.Fatalf("DecryptNativeStream failed: %v", err)
	}
	if string(plaintext) != string(wantPlaintext) {
		t.Fatalf("plaintext = %x, want %x", plaintext, wantPlaintext)
	}
}

func decodeHexFixture(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex fixture: %v", err)
	}
	return decoded
}
