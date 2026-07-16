package nodeepm

// Ported from sdn-server/internal/epm/signature.go (read-only source). The
// canonical signing content is byte-identical to the sdn-server / wallet / wasm
// verifiers (RFC 8785 JCS over the EPM wire fields, SIGNATURE excluded,
// SIGNATURE_TIMESTAMP included), so a node EPM built here verifies under every
// SDN verifier — not just this node.
//
// The kubo node only ever holds an Ed25519 libp2p identity, so this port keeps
// the Ed25519 verification path (the sdn-server default) and drops the
// secp256k1 branch, which would pull a secp256k1 dependency the node never
// needs. The canonical-content builder is copied verbatim so the payload does
// not drift from the other verifiers.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

var (
	ErrEmptyEPMData        = errors.New("empty EPM data")
	ErrInvalidEPMData      = errors.New("invalid EPM data")
	ErrMissingEPMSignature = errors.New("missing EPM signature")
	ErrInvalidEPMSignature = errors.New("invalid EPM signature")
)

// VerifyEPMSignature verifies the embedded EPM signature against the canonical
// EPM payload. The signature field itself is excluded; the signature timestamp
// is included so it cannot be altered without invalidating the signature. Only
// the Ed25519 signing path is accepted (the node identity is Ed25519).
func VerifyEPMSignature(epmData []byte) error {
	if len(epmData) == 0 {
		return ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmData) {
		return ErrInvalidEPMData
	}

	epmRecord := EPM.GetSizePrefixedRootAsEPM(epmData, 0)
	signatureHex := strings.TrimSpace(string(epmRecord.SIGNATURE()))
	if signatureHex == "" {
		return ErrMissingEPMSignature
	}
	if epmRecord.SIGNATURE_TIMESTAMP() == 0 {
		return fmt.Errorf("%w: missing signature timestamp", ErrMissingEPMSignature)
	}

	signature, err := decodeHexString(signatureHex)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEPMSignature, err)
	}
	payload, err := canonicalSigningContentFromEPM(epmRecord)
	if err != nil {
		return err
	}
	if verifyEPMSignatureAgainstKeys(epmRecord, payload, signature) {
		return nil
	}
	return ErrInvalidEPMSignature
}

// verifyEPMSignatureAgainstKeys tries each Ed25519 signing key in the EPM. The
// node's self-signature is produced by its libp2p Ed25519 identity, so the
// ed25519 (empty or "ed25519" ADDRESS_TYPE) path is the only one accepted.
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
		switch strings.ToLower(strings.TrimSpace(string(key.ADDRESS_TYPE()))) {
		case "", "ed25519":
			if len(pub) == ed25519.PublicKeySize && len(signature) == ed25519.SignatureSize &&
				ed25519.Verify(ed25519.PublicKey(pub), payload, signature) {
				return true
			}
		}
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

// marshalEPMSigningContent serializes the canonical EPM content as RFC 8785
// (JCS): encoding/json sorts map keys recursively and SetEscapeHTML(false)
// emits & < > raw. Byte-identical to the sdn-server / wasm / wallet verifiers.
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
