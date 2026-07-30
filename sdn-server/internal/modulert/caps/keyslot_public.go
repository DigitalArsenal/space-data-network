package caps

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"golang.org/x/crypto/ed25519"
)

// keyslotPublicProofMessage is the fixed, domain-separated vector signed by the
// self-proof in KeyslotEd25519PublicKey. It is never a guest input and never
// leaves the host; it exists only so the derivation can be checked against the
// oracle that will actually be used to sign, rather than against itself.
const keyslotPublicProofMessage = "sdn-server/keyslot.public/v1/self-proof"

// KeyslotEd25519PublicKey returns the PUBLIC half of a host-managed Ed25519
// signing slot.
//
// WHY THIS EXISTS. keyslot.sign is a one-way oracle: the guest hands the host a
// payload and gets a signature, and the slot's seed never crosses the boundary
// (that is the whole point — the raw-export keyslot.get op was removed). But a
// verifier needs the public key, and for the licensing module the public key is
// broadcast material: it is stamped into every issued grant as
// LGR.GRANT_VERIFIER_PUBKEY and is exactly what clients verify the grant's
// PROVIDER_SIGNATURE against. Only the host can supply it, so the host must
// have ONE derivation of it that is provably the same key keyslot.sign uses.
//
// That "provably" is the reason this is not a two-line helper. Deriving the
// public key from a slot the signer does not use ships a self-consistent lie:
// every internal check passes and every external verify fails, which is the
// exact failure mode this function was written to end (task
// sdn-module-delivery-grant-sig-broken — grants carried 32 zero bytes here and
// no host-side test could see it). So the derivation is proven against the real
// oracle: the same handleKeyslotSign that will sign the grants signs a fixed
// vector, and the derived key must verify it. A mismatch is returned as an
// error and callers MUST fail closed on it.
func KeyslotEd25519PublicKey(nodeCtx *modulert.NodeContext, slotID string) (ed25519.PublicKey, error) {
	// resolveKeySlot enforces the slot's declared-algorithm gate, so a slot
	// declared for X25519 cannot be milked for an Ed25519 public key here.
	seed, err := resolveKeySlot(nodeCtx, slotID, modulert.KeySlotAlgorithmEd25519)
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("keyslot %q is not a %d-byte ed25519 seed", slotID, ed25519.SeedSize)
	}

	publicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)

	// Self-proof through the real signing oracle, not a re-implementation of it.
	request, err := json.Marshal(map[string]string{
		"slotId":    slotID,
		"payload":   base64.StdEncoding.EncodeToString([]byte(keyslotPublicProofMessage)),
		"algorithm": modulert.KeySlotAlgorithmEd25519,
	})
	if err != nil {
		return nil, fmt.Errorf("keyslot %q public-key self-proof request: %w", slotID, err)
	}
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			Signature string `json:"signature"`
			Algorithm string `json:"algorithm"`
		} `json:"result"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(handleKeyslotSign(nodeCtx, request), &response); err != nil {
		return nil, fmt.Errorf("keyslot %q public-key self-proof response: %w", slotID, err)
	}
	if !response.OK {
		return nil, fmt.Errorf("keyslot %q public-key self-proof refused: %s", slotID, response.Error.Message)
	}
	signature, err := base64.StdEncoding.DecodeString(response.Result.Signature)
	if err != nil {
		return nil, fmt.Errorf("keyslot %q public-key self-proof signature: %w", slotID, err)
	}
	if !ed25519.Verify(publicKey, []byte(keyslotPublicProofMessage), signature) {
		return nil, fmt.Errorf(
			"keyslot %q public key does not verify a signature from keyslot.sign on the same slot",
			slotID,
		)
	}
	return publicKey, nil
}
