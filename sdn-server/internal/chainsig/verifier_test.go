package chainsig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

const vectorPath = "testdata/eip191-personal-sign-vectors.json"

var updateVectors = flag.Bool("update-vectors", false, "rewrite deterministic EIP-191 cross-implementation vectors")

type vectorFile struct {
	Schema  string               `json:"schema"`
	Vectors []personalSignVector `json:"vectors"`
}

type personalSignVector struct {
	Name                   string `json:"name"`
	MessageUTF8            string `json:"message_utf8"`
	MessageHex             string `json:"message_hex"`
	SignatureCompactVRSHex string `json:"signature_compact_vrs_hex"`
	SignatureWalletRSVHex  string `json:"signature_wallet_rsv_hex"`
	CompressedPublicKeyHex string `json:"compressed_public_key_hex"`
	EthereumAddress        string `json:"ethereum_address"`
}

func TestCrossImplementationVectors(t *testing.T) {
	generated := vectorFile{
		Schema:  "sdn-eip191-personal-sign-v1",
		Vectors: []personalSignVector{deterministicWrongOrderVector(t)},
	}
	if *updateVectors {
		encoded, err := json.MarshalIndent(generated, "", "  ")
		if err != nil {
			t.Fatalf("marshal vectors: %v", err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(vectorPath, encoded, 0o644); err != nil {
			t.Fatalf("write vectors: %v", err)
		}
	}

	committedBytes, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var committed vectorFile
	if err := json.Unmarshal(committedBytes, &committed); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if !reflect.DeepEqual(committed, generated) {
		t.Fatal("committed EIP-191 vectors differ from deterministic generation; run go test ./internal/chainsig -args -update-vectors")
	}

	for _, vector := range committed.Vectors {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			message := []byte(vector.MessageUTF8)
			if got := "0x" + hex.EncodeToString(message); got != vector.MessageHex {
				t.Fatalf("message hex = %s, want %s", got, vector.MessageHex)
			}

			compact := mustDecodeHex(t, vector.SignatureCompactVRSHex)
			wallet := mustDecodeHex(t, vector.SignatureWalletRSVHex)
			assertRecoveredVector(t, message, compact, SignatureEncodingCompactVRS, vector)
			compactUncompressedHeader := append([]byte(nil), compact...)
			compactUncompressedHeader[0] -= 4
			assertRecoveredVector(t, message, compactUncompressedHeader, SignatureEncodingCompactVRS, vector)
			assertRecoveredVector(t, message, wallet, SignatureEncodingWalletRSV, vector)

			walletParityV := append([]byte(nil), wallet...)
			walletParityV[64] -= 27
			assertRecoveredVector(t, message, walletParityV, SignatureEncodingWalletRSV, vector)

			// This is the retired EPM behavior: wallet RSV bytes were fed directly
			// to dcrd's VRS recovery. The call succeeds but identifies a different
			// valid secp256k1 key, which is why encoding must be explicit.
			oldRecovered, _, err := ecdsa.RecoverCompact(wallet, PersonalSignHash(message))
			if err != nil {
				t.Fatalf("old wrong-order recovery unexpectedly failed: %v", err)
			}
			if got := "0x" + hex.EncodeToString(oldRecovered.SerializeCompressed()); got == vector.CompressedPublicKeyHex {
				t.Fatalf("old wrong-order recovery returned the expected key %s; vector no longer proves the regression", got)
			}
		})
	}
}

func TestVerifyPersonalSignDerivesKnownEthereumIdentity(t *testing.T) {
	privateKeyBytes := make([]byte, 32)
	privateKeyBytes[31] = 1
	privateKey := secp256k1.PrivKeyFromBytes(privateKeyBytes)
	message := []byte("SDN EIP-191 known identity")
	signature := ecdsa.SignCompact(privateKey, PersonalSignHash(message), true)

	recovered, err := VerifyPersonalSign(message, signature, SignatureEncodingCompactVRS)
	if err != nil {
		t.Fatalf("VerifyPersonalSign: %v", err)
	}
	if got, want := hex.EncodeToString(recovered.CompressedPublicKey), "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"; got != want {
		t.Fatalf("compressed public key = %s, want %s", got, want)
	}
	if got, want := recovered.EthereumAddress, "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"; got != want {
		t.Fatalf("Ethereum address = %s, want %s", got, want)
	}
}

func TestVerifyPersonalSignRejectsWrongLengths(t *testing.T) {
	for _, length := range []int{63, 66} {
		_, err := VerifyPersonalSign([]byte("length rejection"), make([]byte, length), SignatureEncodingWalletRSV)
		if !errors.Is(err, ErrInvalidSignatureLength) {
			t.Fatalf("length %d error = %v, want ErrInvalidSignatureLength", length, err)
		}
	}
}

func TestVerifyPersonalSignRejectsRecoveryIDsOutsideAcceptedSets(t *testing.T) {
	vector := deterministicWrongOrderVector(t)
	wallet := mustDecodeHex(t, vector.SignatureWalletRSVHex)
	for _, invalidV := range []byte{2, 26, 29, 255} {
		invalid := append([]byte(nil), wallet...)
		invalid[64] = invalidV
		_, err := VerifyPersonalSign([]byte(vector.MessageUTF8), invalid, SignatureEncodingWalletRSV)
		if !errors.Is(err, ErrInvalidRecoveryID) {
			t.Fatalf("wallet V=%d error = %v, want ErrInvalidRecoveryID", invalidV, err)
		}
	}

	compact := mustDecodeHex(t, vector.SignatureCompactVRSHex)
	for _, invalidV := range []byte{0, 26, 35, 255} {
		invalid := append([]byte(nil), compact...)
		invalid[0] = invalidV
		_, err := VerifyPersonalSign([]byte(vector.MessageUTF8), invalid, SignatureEncodingCompactVRS)
		if !errors.Is(err, ErrInvalidRecoveryID) {
			t.Fatalf("compact V=%d error = %v, want ErrInvalidRecoveryID", invalidV, err)
		}
	}
}

func TestVerifyPersonalSignRejectsHighS(t *testing.T) {
	vector := deterministicWrongOrderVector(t)
	compact := mustDecodeHex(t, vector.SignatureCompactVRSHex)
	lowS := new(big.Int).SetBytes(compact[33:65])
	highS := new(big.Int).Sub(secp256k1.S256().Params().N, lowS)
	highS.FillBytes(compact[33:65])

	_, err := VerifyPersonalSign([]byte(vector.MessageUTF8), compact, SignatureEncodingCompactVRS)
	if !errors.Is(err, ErrHighS) {
		t.Fatalf("high-S error = %v, want ErrHighS", err)
	}
}

func TestVerifyPersonalSignRejectsPointAtInfinity(t *testing.T) {
	curve := secp256k1.S256()
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(curve.Params().N), 1)
	for counter := 0; counter < 100; counter++ {
		message := []byte(fmt.Sprintf("SDN EIP-191 infinity rejection %d", counter))
		hash := PersonalSignHash(message)
		e := new(big.Int).SetBytes(hash)
		if e.Sign() == 0 || e.Cmp(halfOrder) > 0 {
			continue
		}

		// R=G and S=e makes Q=r^-1(SR-eG) the point at infinity.
		signature := make([]byte, 65)
		signature[0] = 31 // compressed, recovery ID 0; G has even Y.
		curve.Params().Gx.FillBytes(signature[1:33])
		copy(signature[33:65], hash)
		_, err := VerifyPersonalSign(message, signature, SignatureEncodingCompactVRS)
		if !errors.Is(err, ErrPointAtInfinity) {
			t.Fatalf("point-at-infinity error = %v, want ErrPointAtInfinity", err)
		}
		return
	}
	t.Fatal("could not deterministically construct a low-S point-at-infinity signature")
}

func TestVerifyPersonalSignRejectsUnknownEncoding(t *testing.T) {
	_, err := VerifyPersonalSign([]byte("encoding rejection"), make([]byte, 65), SignatureEncoding("auto"))
	if !errors.Is(err, ErrInvalidSignatureEncoding) {
		t.Fatalf("unknown-encoding error = %v, want ErrInvalidSignatureEncoding", err)
	}
}

func deterministicWrongOrderVector(t *testing.T) personalSignVector {
	t.Helper()
	keyMaterial := sha256.Sum256([]byte("SDN EIP-191 cross-implementation throwaway key v1"))
	privateKey := secp256k1.PrivKeyFromBytes(keyMaterial[:])
	expectedPublicKey := privateKey.PubKey().SerializeCompressed()

	for counter := 0; counter < 10_000; counter++ {
		message := []byte(fmt.Sprintf("SDN EIP-191 cross-implementation vector %d", counter))
		hash := PersonalSignHash(message)
		compact := ecdsa.SignCompact(privateKey, hash, true)
		recoveryID := compact[0] - 31
		if recoveryID > 1 || compact[1] < 27 || compact[1] > 34 {
			continue
		}

		wallet := append([]byte(nil), compact[1:]...)
		wallet = append(wallet, 27+recoveryID)
		wrongPublicKey, _, err := ecdsa.RecoverCompact(wallet, hash)
		if err != nil || bytes.Equal(wrongPublicKey.SerializeCompressed(), expectedPublicKey) {
			continue
		}

		recovered, err := VerifyPersonalSign(message, compact, SignatureEncodingCompactVRS)
		if err != nil {
			t.Fatalf("verify generated compact signature: %v", err)
		}
		return personalSignVector{
			Name:                   "deterministic-wrong-order-regression",
			MessageUTF8:            string(message),
			MessageHex:             "0x" + hex.EncodeToString(message),
			SignatureCompactVRSHex: "0x" + hex.EncodeToString(compact),
			SignatureWalletRSVHex:  "0x" + hex.EncodeToString(wallet),
			CompressedPublicKeyHex: "0x" + hex.EncodeToString(expectedPublicKey),
			EthereumAddress:        recovered.EthereumAddress,
		}
	}
	t.Fatal("could not deterministically find a signature that demonstrates wrong-order recovery")
	return personalSignVector{}
}

func assertRecoveredVector(t *testing.T, message, signature []byte, encoding SignatureEncoding, vector personalSignVector) {
	t.Helper()
	recovered, err := VerifyPersonalSign(message, signature, encoding)
	if err != nil {
		t.Fatalf("VerifyPersonalSign(%s): %v", encoding, err)
	}
	if got := "0x" + hex.EncodeToString(recovered.CompressedPublicKey); got != vector.CompressedPublicKeyHex {
		t.Fatalf("compressed public key = %s, want %s", got, vector.CompressedPublicKeyHex)
	}
	if recovered.EthereumAddress != vector.EthereumAddress {
		t.Fatalf("Ethereum address = %s, want %s", recovered.EthereumAddress, vector.EthereumAddress)
	}
}

func mustDecodeHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil {
		t.Fatalf("decode %q: %v", encoded, err)
	}
	return decoded
}
