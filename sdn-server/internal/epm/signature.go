package epm

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

var (
	ErrEmptyEPMData        = errors.New("empty EPM data")
	ErrInvalidEPMData      = errors.New("invalid EPM data")
	ErrMissingEPMSignature = errors.New("missing EPM signature")
	ErrInvalidEPMSignature = errors.New("invalid EPM signature")
)

// VerifyEPMSignature verifies the embedded EPM signature against the canonical
// EPM payload. The signature field itself is excluded; the signature timestamp
// is included so it cannot be altered without invalidating the signature.
//
// This is the WIRE verifier: it accepts a signature produced by ANY signing key
// carried in the record's KEYS vector. Chain verification bound to a card's
// sign-alias key (§21) must use VerifyEPMSignatureBindingKey instead — there the
// record's SIGNATURE has to verify against the FIRST signing key, the one the
// vCard advertises as the sign@ alias.
func VerifyEPMSignature(epmData []byte) error {
	epmRecord, signature, payload, err := verifyEPMSignaturePrep(epmData)
	if err != nil {
		return err
	}
	if verifyEPMSignatureAgainstKeys(epmRecord, payload, signature) {
		return nil
	}
	return ErrInvalidEPMSignature
}

// VerifyEPMSignatureBindingKey verifies the embedded EPM signature against
// EXACTLY the card's sign-alias key (§21). signAliasKey is the raw public key
// bytes carried on the vCard sign@ alias — the base64url literal decoded.
//
// The sign alias is emitted from the FIRST signing key's PUBLIC_KEY
// (appleIdentityEntriesFromEPM, one-row invariant), so binding enforces:
//  1. the record's first signing key IS signAliasKey (card and record
//     advertise the same key), and
//  2. SIGNATURE verifies against that key.
//
// A signature produced by any OTHER signing key in the record — even a key that
// would verify on its own — is rejected. This closes the loophole where a
// record could carry the legitimate key first and an attacker's signing key
// second and still pass chain verification.
func VerifyEPMSignatureBindingKey(epmData []byte, signAliasKey []byte) error {
	epmRecord, signature, payload, err := verifyEPMSignaturePrep(epmData)
	if err != nil {
		return err
	}
	if len(signAliasKey) == 0 {
		return ErrInvalidEPMSignature
	}
	if verifyEPMSignatureAgainstSignAlias(epmRecord, payload, signature, signAliasKey) {
		return nil
	}
	return ErrInvalidEPMSignature
}

// VerifyDetachedSignature verifies a detached signature against any Signing
// key carried by an already-held EPM. Callers that use the EPM as an identity
// binding must first verify the EPM's own signature and peer ID; this helper
// deliberately does only the detached-signature operation so it is reusable by
// SDS records such as TRE.
func VerifyDetachedSignature(epmData, payload, signature []byte) error {
	if len(epmData) == 0 {
		return ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmData) {
		return ErrInvalidEPMData
	}
	if len(payload) == 0 || len(signature) == 0 {
		return ErrInvalidEPMSignature
	}
	record := EPM.GetSizePrefixedRootAsEPM(epmData, 0)
	if verifyEPMSignatureAgainstKeys(record, payload, signature) {
		return nil
	}
	return ErrInvalidEPMSignature
}

// verifyEPMSignaturePrep loads and validates an EPM buffer, returning the parsed
// record, its decoded SIGNATURE bytes, and the canonical signing payload.
func verifyEPMSignaturePrep(epmData []byte) (*EPM.EPM, []byte, []byte, error) {
	if len(epmData) == 0 {
		return nil, nil, nil, ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmData) {
		return nil, nil, nil, ErrInvalidEPMData
	}

	epmRecord := EPM.GetSizePrefixedRootAsEPM(epmData, 0)
	signatureHex := strings.TrimSpace(string(epmRecord.SIGNATURE()))
	if signatureHex == "" {
		return nil, nil, nil, ErrMissingEPMSignature
	}
	if epmRecord.SIGNATURE_TIMESTAMP() == 0 {
		return nil, nil, nil, fmt.Errorf("%w: missing signature timestamp", ErrMissingEPMSignature)
	}

	signature, err := decodeHexString(signatureHex)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrInvalidEPMSignature, err)
	}
	payload, err := canonicalSigningContentFromEPM(epmRecord)
	if err != nil {
		return nil, nil, nil, err
	}
	return epmRecord, signature, payload, nil
}

// verifyEPMSignatureAgainstKeys tries each signing key in the EPM, dispatching on
// its ALGORITHM (§21: the verifier dispatches on ALGORITHM, never on
// ADDRESS_TYPE, which is an address-format tag). ed25519 (empty or "ed25519") is
// the default fast path; secp256k1 signing keys are verified as ECDSA-DER over
// sha256(payload). Exactly one signing key produced SIGNATURE, so accepting on
// the first that verifies is correct, and ed25519 is tried first to preserve
// the 25519 default.
//
// Binding to the card's sign-alias key — NOT "any" signing key — is
// VerifyEPMSignatureBindingKey / verifyEPMSignatureAgainstSignAlias.
func verifyEPMSignatureAgainstKeys(epmRecord *EPM.EPM, payload, signature []byte) bool {
	key := new(EPM.CryptoKey)
	for i := 0; i < epmRecord.KEYSLength(); i++ {
		if !epmRecord.KEYS(key, i) || key.KEY_TYPE() != EPM.KeyTypeSigning {
			continue
		}
		pub, err := decodeHexString(string(key.PUBLIC_KEY()))
		if err != nil {
			continue
		}
		if verifySignatureForKey(pub, string(key.ALGORITHM()), payload, signature) {
			return true
		}
	}
	return false
}

// verifyEPMSignatureAgainstSignAlias verifies SIGNATURE against EXACTLY the
// card's sign-alias key. The record's FIRST signing key is the sign alias
// (appleIdentityEntriesFromEPM emits the first signing key as the one-row sign@
// alias); if that key is not signAliasKey, or SIGNATURE does not verify against
// it, verification fails — no other signing key in the record is ever tried.
func verifyEPMSignatureAgainstSignAlias(epmRecord *EPM.EPM, payload, signature, signAliasKey []byte) bool {
	key := new(EPM.CryptoKey)
	for i := 0; i < epmRecord.KEYSLength(); i++ {
		if !epmRecord.KEYS(key, i) || key.KEY_TYPE() != EPM.KeyTypeSigning {
			continue
		}
		pub, err := decodeHexString(string(key.PUBLIC_KEY()))
		if err != nil {
			return false
		}
		if !bytes.Equal(pub, signAliasKey) {
			return false
		}
		return verifySignatureForKey(pub, string(key.ALGORITHM()), payload, signature)
	}
	return false
}

// verifySignatureForKey verifies SIGNATURE against a single public key of the
// given ALGORITHM (§21: dispatch on ALGORITHM, never on ADDRESS_TYPE). ed25519
// (empty or "ed25519") is the default fast path; secp256k1 keys verify as
// ECDSA-DER over sha256(payload).
func verifySignatureForKey(pub []byte, algorithm string, payload, signature []byte) bool {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "", "ed25519":
		return len(pub) == ed25519.PublicKeySize && len(signature) == ed25519.SignatureSize &&
			ed25519.Verify(ed25519.PublicKey(pub), payload, signature)
	case "secp256k1":
		pk, err := secp256k1.ParsePubKey(pub)
		if err != nil {
			return false
		}
		sig, err := ecdsa.ParseDERSignature(signature)
		if err != nil {
			return false
		}
		digest := sha256.Sum256(payload)
		return sig.Verify(digest[:], pk)
	}
	return false
}

// EPMSigningPayload returns the deterministic payload covered by the embedded
// EPM signature.
func EPMSigningPayload(epmData []byte) ([]byte, error) {
	if len(epmData) == 0 {
		return nil, ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmData) {
		return nil, ErrInvalidEPMData
	}
	return canonicalSigningContentFromEPM(EPM.GetSizePrefixedRootAsEPM(epmData, 0))
}

func canonicalSigningContentFromEPM(epmRecord *EPM.EPM) ([]byte, error) {
	if epmRecord == nil {
		return nil, ErrInvalidEPMData
	}
	content := make(map[string]interface{})

	addBytesString(content, "DN", epmRecord.DN())
	addBytesString(content, "LEGAL_NAME", epmRecord.LEGAL_NAME())
	addBytesString(content, "FAMILY_NAME", epmRecord.FAMILY_NAME())
	addBytesString(content, "GIVEN_NAME", epmRecord.GIVEN_NAME())
	addBytesString(content, "ADDITIONAL_NAME", epmRecord.ADDITIONAL_NAME())
	addBytesString(content, "HONORIFIC_PREFIX", epmRecord.HONORIFIC_PREFIX())
	addBytesString(content, "HONORIFIC_SUFFIX", epmRecord.HONORIFIC_SUFFIX())
	addBytesString(content, "JOB_TITLE", epmRecord.JOB_TITLE())
	addBytesString(content, "OCCUPATION", epmRecord.OCCUPATION())
	addBytesString(content, "EMAIL", epmRecord.EMAIL())
	addBytesString(content, "TELEPHONE", epmRecord.TELEPHONE())

	addr := new(EPM.Address)
	if epmRecord.ADDRESS(addr) != nil {
		address := make(map[string]interface{})
		addBytesString(address, "COUNTRY", addr.COUNTRY())
		addBytesString(address, "REGION", addr.REGION())
		addBytesString(address, "LOCALITY", addr.LOCALITY())
		addBytesString(address, "POSTAL_CODE", addr.POSTAL_CODE())
		addBytesString(address, "STREET", addr.STREET())
		addBytesString(address, "POST_OFFICE_BOX_NUMBER", addr.POST_OFFICE_BOX_NUMBER())
		if len(address) > 0 {
			content["ADDRESS"] = address
		}
	}

	if n := epmRecord.ALTERNATE_NAMESLength(); n > 0 {
		values := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if value := strings.TrimSpace(string(epmRecord.ALTERNATE_NAMES(i))); value != "" {
				values = append(values, value)
			}
		}
		if len(values) > 0 {
			content["ALTERNATE_NAMES"] = values
		}
	}

	key := new(EPM.CryptoKey)
	if n := epmRecord.KEYSLength(); n > 0 {
		keys := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			if !epmRecord.KEYS(key, i) {
				continue
			}
			entry := make(map[string]interface{})
			addBytesString(entry, "PUBLIC_KEY", key.PUBLIC_KEY())
			addBytesString(entry, "XPUB", key.XPUB())
			addBytesString(entry, "ADDRESS_TYPE", key.ADDRESS_TYPE())
			addBytesString(entry, "KEY_ADDRESS", key.KEY_ADDRESS())
			// §21 / canonical-serialization annex rule 6: KEY_PATH, ALGORITHM
			// and ENCODING participate in the JCS preimage. They were absent
			// here, which would have made post-flip records carrying
			// ALGORITHM fail Go-side verification (Themis finding). Under §21
			// published records carry no XPUB/KEY_ADDRESS/KEY_PATH, so
			// addBytesString's empty-skip omits them naturally; pre-flip
			// records that DO carry them still verify (annex §3: verification
			// is over stored order).
			addBytesString(entry, "KEY_PATH", key.KEY_PATH())
			addBytesString(entry, "ALGORITHM", key.ALGORITHM())
			addBytesString(entry, "ENCODING", key.ENCODING())
			switch key.KEY_TYPE() {
			case EPM.KeyTypeSigning:
				entry["KEY_TYPE"] = "Signing"
			case EPM.KeyTypeEncryption:
				entry["KEY_TYPE"] = "Encryption"
			}
			if len(entry) > 0 {
				keys = append(keys, entry)
			}
		}
		if len(keys) > 0 {
			content["KEYS"] = keys
		}
	}

	if n := epmRecord.MULTIFORMAT_ADDRESSLength(); n > 0 {
		addresses := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if value := strings.TrimSpace(string(epmRecord.MULTIFORMAT_ADDRESS(i))); value != "" {
				addresses = append(addresses, value)
			}
		}
		if len(addresses) > 0 {
			content["MULTIFORMAT_ADDRESS"] = addresses
		}
	}

	content["ENTITY_TYPE"] = epmRecord.ENTITY_TYPE().String()
	if ts := epmRecord.SIGNATURE_TIMESTAMP(); ts != 0 {
		content["SIGNATURE_TIMESTAMP"] = ts
	}

	proof := new(EPM.ChainProof)
	if n := epmRecord.CHAIN_PROOFSLength(); n > 0 {
		proofs := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			if !epmRecord.CHAIN_PROOFS(proof, i) {
				continue
			}
			entry := make(map[string]interface{})
			addBytesString(entry, "CHAIN", proof.CHAIN())
			addBytesString(entry, "ADDRESS", proof.ADDRESS())
			addBytesString(entry, "PUBLIC_KEY", proof.PUBLIC_KEY())
			addBytesString(entry, "KEY_PATH", proof.KEY_PATH())
			addBytesString(entry, "SIGNATURE", proof.SIGNATURE())
			addBytesString(entry, "SIGNED_PAYLOAD", proof.SIGNED_PAYLOAD())
			addBytesString(entry, "ALGORITHM", proof.ALGORITHM())
			addBytesString(entry, "ENCODING", proof.ENCODING())
			if len(entry) > 0 {
				proofs = append(proofs, entry)
			}
		}
		if len(proofs) > 0 {
			content["CHAIN_PROOFS"] = proofs
		}
	}

	return marshalEPMSigningContent(content)
}

// marshalEPMSigningContent serializes the canonical EPM content as RFC 8785 (JCS):
// encoding/json sorts map keys recursively (the EPM key set is ASCII, so byte
// order == UTF-16 code-unit order), and SetEscapeHTML(false) emits & < > and
// U+2028/U+2029 raw instead of \u00XX. The result is byte-identical to the
// isomorphic wasm verifier (space-data-network-modules common/jcs) and the wallet
// (hd-wallet-wasm buildEPMSigningContent), so wallet/node/module signatures match.
func marshalEPMSigningContent(content map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(content); err != nil {
		return nil, fmt.Errorf("marshal EPM signing content: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func decodeHexString(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	trimmed = strings.TrimPrefix(trimmed, "0X")
	return hex.DecodeString(trimmed)
}

func addBytesString(target map[string]interface{}, key string, value []byte) {
	if trimmed := strings.TrimSpace(string(value)); trimmed != "" {
		target[key] = trimmed
	}
}
