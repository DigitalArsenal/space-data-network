package modulert_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/deliveryclient"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
	fil "github.com/spacedatanetwork/sdn-server/internal/typedfb/FIL"
)

// A 32-byte key reaches a module through the typed secrets.get as its exact
// bytes. The typed licensing artifact's decrypt_and_verify reads its DEK by the
// ID its config names and opens AES-256-GCM content with it, so a key that
// lost or changed one byte on the way fails authentication. The key is random
// and starts with 0xff, which is not UTF-8 and did not survive the old string
// round trip.
func TestSecretsGetCarriesTheExactKeyBytes(t *testing.T) {
	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	dek[0] = 0xff
	signerPublic, signerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("signed module bytes")
	signed := append(append(append([]byte(nil), content...), ed25519.Sign(signerPrivate, content)...), binary.LittleEndian.AppendUint32(nil, uint32(len(content)))...)
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	protected, err := deliveryclient.EncryptBundleAESGCM(dek, iv, signed, nil)
	if err != nil {
		t.Fatal(err)
	}

	load := func(t *testing.T, secretID string, approve bool, capReg *modulert.CapabilityRegistry) *modulert.Module {
		t.Helper()
		policy, err := modulert.NewCapabilityPolicyStore("")
		if err != nil {
			t.Fatal(err)
		}
		hash := modulert.ContentHashHex(wasm)
		lanes := []string{"ipfs", "protocol_dial", "wallet_sign"}
		if approve {
			lanes = append(lanes, modulert.SecretsCapabilityPrefix+secretID)
		}
		for _, capability := range lanes {
			if _, err := policy.Approve(modulert.CapabilityApproval{ModuleHash: hash, Capability: capability, PluginID: "licensing", ApprovedBy: "test"}); err != nil {
				t.Fatal(err)
			}
		}
		// The signature check is the crypto capability's (crypto.ed25519.verify).
		capReg.Register("crypto_verify", caps.NewCryptoCapFactory())
		secrets := modulert.NewHostSecrets()
		secrets.Set("licensing/dek", dek)
		mod, err := modulert.NewModule(wasm, capReg, &modulert.NodeContext{
			CapabilityPolicy: policy,
			HostSecrets:      secrets,
			Config:           map[string]interface{}{"dek_secret_id": secretID},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mod.Close() })
		if !hasMethod(mod.Manifest(), "decrypt_and_verify") {
			t.Skipf("%s predates the typed licensing contract", wasmPath)
		}
		return mod
	}
	decrypt := func(mod *modulert.Module) ([]byte, error) {
		outputs, err := mod.InvokeMethodOutputs(context.Background(), "decrypt_and_verify", []modulert.InvokeInputFrame{
			{PortID: "protected_content", SchemaName: "FIL.fbs", FileIdentifier: "$FIL", RootTypeName: "FIL", Payload: filFrame(protected)},
			{PortID: "signer_key", SchemaName: "KMF.fbs", FileIdentifier: "$KMF", RootTypeName: "KMF", Payload: verificationKeyFrame(signerPublic)},
		})
		if err != nil {
			return nil, err
		}
		for _, o := range outputs {
			if o.PortID == "plaintext" {
				return fil.GetRootAsFIL(o.Payload, 0).CONTENTBytes(), nil
			}
		}
		return nil, nil
	}

	t.Run("host-held secret", func(t *testing.T) {
		plaintext, err := decrypt(load(t, "licensing/dek", true, modulert.NewCapabilityRegistry()))
		if err != nil || !bytes.Equal(plaintext, content) {
			t.Fatalf("plaintext %q, err %v", plaintext, err)
		}
	})
	t.Run("host-held secret, lane not approved", func(t *testing.T) {
		if _, err := decrypt(load(t, "licensing/dek", false, modulert.NewCapabilityRegistry())); err == nil || !strings.Contains(err.Error(), "secrets.get did not answer") {
			t.Fatalf("an unapproved lane answered: %v", err)
		}
	})
	t.Run("binary secret from the secrets capability", func(t *testing.T) {
		capReg := modulert.NewCapabilityRegistry()
		capReg.Register("secrets", func(_ *modulert.Module) modulert.CapHandler {
			return func(op string, payload []byte) ([]byte, error) {
				var req struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(payload, &req)
				if op != "secrets.get" || req.ID != "keystore/dek" {
					return []byte(`{"ok":false,"error":"unknown secret"}`), nil
				}
				return json.Marshal(map[string]any{"ok": true, "result": map[string]any{"id": req.ID,
					"secret": map[string]any{"__type": "bytes", "base64": base64.StdEncoding.EncodeToString(dek)}}})
			}
		})
		plaintext, err := decrypt(load(t, "keystore/dek", false, capReg))
		if err != nil || !bytes.Equal(plaintext, content) {
			t.Fatalf("plaintext %q, err %v", plaintext, err)
		}
	})
}

func filFrame(content []byte) []byte {
	b := flatbuffers.NewBuilder(len(content) + 64)
	data := b.CreateByteVector(content)
	fil.FILStart(b)
	fil.FILAddSIZE(b, uint64(len(content)))
	fil.FILAddCONTENT(b, data)
	fil.FinishFILBuffer(b, fil.FILEnd(b))
	return b.FinishedBytes()
}

func verificationKeyFrame(publicKey []byte) []byte {
	b := flatbuffers.NewBuilder(128)
	id := b.CreateString("module-signer")
	key := b.CreateByteVector(publicKey)
	kmf.KMFStart(b)
	kmf.KMFAddKEY_ID(b, id)
	kmf.KMFAddROLE(b, kmf.EnumValueskeyMaterialRole["VerificationKey"])
	kmf.KMFAddALGORITHM(b, kmf.EnumValueskeyMaterialAlgorithm["Ed25519Public"])
	kmf.KMFAddENCODING(b, kmf.EnumValueskeyMaterialEncoding["PublicKey32"])
	kmf.KMFAddKEY_BYTES(b, key)
	kmf.FinishKMFBuffer(b, kmf.KMFEnd(b))
	return b.FinishedBytes()
}

func hasMethod(m *modulert.Manifest, methodID string) bool {
	if m == nil {
		return false
	}
	for _, method := range m.Methods {
		if method.MethodID == methodID {
			return true
		}
	}
	return false
}
