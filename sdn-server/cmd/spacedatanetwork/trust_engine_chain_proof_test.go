package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/directory"
	sdnepm "github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// forgedChainProofEPM is the SEC-013 audit repro's record: self-signed by an
// attacker key, carrying a bitcoin ChainProof for a wallet the attacker does
// not control (SIGNATURE "00", SIGNED_PAYLOAD "00").
func forgedChainProofEPM(t *testing.T, whale string) (peer.ID, []byte) {
	t.Helper()
	idPriv, _, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x13}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(idPriv)
	if err != nil {
		t.Fatal(err)
	}
	signer := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, 32))
	build := func(signatureHex string, ts int64) []byte {
		b := flatbuffers.NewBuilder(1024)
		chain, addr := b.CreateString("bitcoin"), b.CreateString(whale)
		pub, path := b.CreateString("02"+hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32))), b.CreateString("m/44'/0'/0'/0/0")
		sig, payload := b.CreateString("00"), b.CreateString("00")
		alg, enc := b.CreateString("secp256k1-compact-bitcoin"), b.CreateString("compact")
		EPM.ChainProofStart(b)
		EPM.ChainProofAddCHAIN(b, chain)
		EPM.ChainProofAddADDRESS(b, addr)
		EPM.ChainProofAddPUBLIC_KEY(b, pub)
		EPM.ChainProofAddKEY_PATH(b, path)
		EPM.ChainProofAddSIGNATURE(b, sig)
		EPM.ChainProofAddSIGNED_PAYLOAD(b, payload)
		EPM.ChainProofAddALGORITHM(b, alg)
		EPM.ChainProofAddENCODING(b, enc)
		proof := EPM.ChainProofEnd(b)
		EPM.EPMStartCHAIN_PROOFSVector(b, 1)
		b.PrependUOffsetT(proof)
		proofs := b.EndVector(1)

		keyHex := b.CreateString(hex.EncodeToString(signer.Public().(ed25519.PublicKey)))
		EPM.CryptoKeyStart(b)
		EPM.CryptoKeyAddPUBLIC_KEY(b, keyHex)
		EPM.CryptoKeyAddKEY_TYPE(b, EPM.KeyTypeSigning)
		key := EPM.CryptoKeyEnd(b)
		EPM.EPMStartKEYSVector(b, 1)
		b.PrependUOffsetT(key)
		keys := b.EndVector(1)

		ipns := b.CreateString("/ipns/" + pid.String())
		EPM.EPMStartMULTIFORMAT_ADDRESSVector(b, 1)
		b.PrependUOffsetT(ipns)
		addrs := b.EndVector(1)
		legal := b.CreateString("Attacker LLC")
		var sigOff flatbuffers.UOffsetT
		if signatureHex != "" {
			sigOff = b.CreateString(signatureHex)
		}
		EPM.EPMStart(b)
		EPM.EPMAddLEGAL_NAME(b, legal)
		EPM.EPMAddKEYS(b, keys)
		EPM.EPMAddMULTIFORMAT_ADDRESS(b, addrs)
		EPM.EPMAddCHAIN_PROOFS(b, proofs)
		EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeNode)
		EPM.EPMAddSIGNATURE_TIMESTAMP(b, ts)
		if signatureHex != "" {
			EPM.EPMAddSIGNATURE(b, sigOff)
		}
		b.FinishSizePrefixedWithFileIdentifier(EPM.EPMEnd(b), []byte("$EPM"))
		return b.FinishedBytes()
	}
	ts := time.Now().Unix()
	payload, err := sdnepm.EPMSigningPayload(build("", ts))
	if err != nil {
		t.Fatal(err)
	}
	signed := build(hex.EncodeToString(ed25519.Sign(signer, payload)), ts)
	if err := sdnepm.VerifyEPMSignature(signed); err != nil {
		t.Fatalf("forged EPM must self-verify, as in the audit repro: %v", err)
	}
	return pid, signed
}

// TestDirectoryChainAddressesIgnoresUnprovenAddresses is the SEC-013 audit
// repro against the trust engine's address resolver: a directory row whose
// projected JSON names a whale wallet (as rows indexed before the fix do)
// must not hand that address to the bond lane, and a $1M MinValueLocked
// predicate must not pass on someone else's funds.
func TestDirectoryChainAddressesIgnoresUnprovenAddresses(t *testing.T) {
	const whale = "34xp4vRoCGJym3xR7yCVPFHoCNxv4Twseo"
	pid, epmBytes := forgedChainProofEPM(t, whale)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flat.Close() })

	// The row as the pre-fix projection wrote it: the unproven address in
	// chain_proofs and in the flattened bitcoin_address field.
	row := map[string]any{
		"directory_kind":  "node",
		"peer_id":         pid.String(),
		"legal_name":      "Attacker LLC",
		"epm_base64":      base64.StdEncoding.EncodeToString(epmBytes),
		"bitcoin_address": whale,
		"chain_proofs": []map[string]any{{
			"chain": "bitcoin", "address": whale, "signature": "00", "signed_payload": "00",
		}},
	}
	if err := directory.NewService(flat).UpsertNodeEPMJSON(row, "", "dht-discovery"); err != nil {
		t.Fatalf("UpsertNodeEPMJSON: %v", err)
	}

	addrs, err := directoryChainAddresses(flat)(pid.String())
	if err != nil {
		t.Fatalf("directoryChainAddresses: %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("directoryChainAddresses = %+v, want no address from an unverified chain proof", addrs)
	}

	lane := trust.BalanceSourceFunc(func(ctx context.Context, a []trust.ChainAddress) ([]trust.Holding, error) {
		var out []trust.Holding
		for _, x := range a {
			out = append(out, trust.Holding{ChainID: x.ChainID, Address: x.Address, Symbol: "BTC", Amount: 250000, Decimals: 8, USD: 250000 * 60000, PricedInUSD: true})
		}
		return out, nil
	})
	holdings, err := lane.Balances(context.Background(), addrs)
	if err != nil {
		t.Fatal(err)
	}
	predicate := trust.Predicate{ID: "bond-1m", Kind: trust.PredicateMinValueLocked, MinValue: 100_000_000, ValueCurrency: "USD"}
	if result := trust.EvaluatePredicate(predicate, trust.SubjectFacts{Holdings: holdings}, 0); result.Passed {
		t.Fatalf("a $1M bond predicate passed on an unproven address: %+v", result)
	}
}

// TestVerifiedChainAddressesRequireSignedEPMBytes: a row with only projected
// JSON (no signed bytes) yields no address at all.
func TestVerifiedChainAddressesRequireSignedEPMBytes(t *testing.T) {
	rowJSON := `{"peer_id":"16Uiu2HAmAPZQ22WpSgQeUxStqr5ah3cJYrtpMQBJiTH3oQRHD31L","bitcoin_address":"34xp4vRoCGJym3xR7yCVPFHoCNxv4Twseo","chain_proofs":[{"chain":"bitcoin","address":"34xp4vRoCGJym3xR7yCVPFHoCNxv4Twseo"}]}`
	addrs, err := verifiedChainAddressesFromDirectoryRow(rowJSON, "16Uiu2HAmAPZQ22WpSgQeUxStqr5ah3cJYrtpMQBJiTH3oQRHD31L", time.Now())
	if err == nil || len(addrs) != 0 {
		t.Fatalf("addrs=%v err=%v, want an error and no address", addrs, err)
	}
}
