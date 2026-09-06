package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// These fixtures were emitted by the SDK JS writer, exercising the real Go
// reader across implementations rather than using a same-language crypto mock.
func TestModuleCustomerDecryptsOnlyForTheIntendedKey(t *testing.T) {
	root := "../license/testdata/protected-publication"
	var manifest struct {
		Key      string `json:"key"`
		Fixtures []struct {
			Name string `json:"name"`
			Hash string `json:"plaintextSha256"`
		} `json:"fixtures"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(manifest.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range manifest.Fixtures {
		if f.Name != "gcm-mbl-enc-pnm" {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(root, f.Name+".bin"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(blob)
		digest := hex.EncodeToString(sum[:])
		plain, err := verifyCustomerModule(blob, key, digest, f.Hash)
		if err != nil {
			t.Fatal(err)
		}
		clear(plain)
		if _, err := verifyCustomerModule(blob, bytes.Repeat([]byte{9}, 32), digest, f.Hash); err == nil {
			t.Fatal("another customer decrypted the module")
		}
		changed := bytes.Clone(blob)
		changed[0] ^= 1
		if _, err := verifyCustomerModule(changed, key, digest, f.Hash); err == nil {
			t.Fatal("accepted tampered ciphertext")
		}
		if _, err := verifyCustomerModule(blob, key, digest, hex.EncodeToString(make([]byte, 32))); err == nil {
			t.Fatal("accepted the wrong module")
		}
		return
	}
	t.Fatal("SDK protected publication fixture missing")
}
func TestModuleCustomerRejectsUnpinnedAndMalformedArtifacts(t *testing.T) {
	for _, blob := range [][]byte{nil, {1, 2, 3}, bytes.Repeat([]byte{255}, 64)} {
		sum := sha256.Sum256(blob)
		if _, err := verifyCustomerModule(blob, make([]byte, 32), hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:])); err == nil {
			t.Fatal("accepted malformed module")
		}
	}
	if _, err := decodeModuleHash("../../somewhere"); err == nil {
		t.Fatal("accepted a path as a hash")
	}
}
