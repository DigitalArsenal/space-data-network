package modulert

// BUNDLE-SCOPE module signature verification.
//
// WHY THIS FILE EXISTS. Until 2026-07-30 this binary could not verify a
// bundle-scope signature at all. verifyPublicationSignature compared the
// recorded signedHashHex against sha256(portable) unconditionally, so an
// artifact signed with signatureScope "bundle" — a digest over the WHOLE MBL,
// not over the portable payload — could never match, and the node reported
// hash_mismatch for an artifact the module SDK and the kubo twin both verified.
// One artifact, three verifiers, two verdicts, with the browser agreeing with
// the wrong one (graph/tasks/sdn-modulert-bundle-scope-twin-divergence.md).
//
// The digest computed here MUST stay byte-identical to:
//
//	kubo/sdn/modulert/publication_signature.go   (moduleBundleSignatureDigest)
//	space-data-module-sdk/src/bundle/signing.js  (computeModuleBundleSignatureHash)
//
// It is a canonical JSON statement over the bundle's structure — every
// non-signature member's identity and payload hash, the canonicalization rule,
// the canonical module hash and the manifest hash — with entries sorted by
// entryId. Field order, the integer encoding of the enums, and the null-vs-
// empty-string distinction are all part of the preimage: a "harmless" cleanup
// here changes what every published bundle-scope artifact hashes to.
//
// The agreed verdicts are pinned as data in
// testdata/statement-domain-vectors.json, which the SDK suite and the kubo
// suite read from byte-identical copies.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
)

// decodedBundleEntry is one MBL member, carried in both the form the digest
// needs and the form a caller could inspect. The payload stays opaque.
type decodedBundleEntry struct {
	entryID          string
	role             MBL.ModuleBundleEntryRole
	sectionName      string
	payloadEncoding  MBL.ModulePayloadEncoding
	flags            uint32
	sha256Hex        string
	payload          []byte
	sectionValue     interface{}
	typeRefValue     interface{}
	mediaValue       interface{}
	descriptionValue interface{}
}

type decodedModuleBundle struct {
	version              uint16
	moduleFormat         interface{}
	canonicalPresent     bool
	canonicalVersion     uint16
	customSectionPrefix  interface{}
	bundleSectionName    interface{}
	hashAlgorithm        interface{}
	canonicalModuleHash  []byte
	manifestHash         []byte
	manifestExportSymbol interface{}
	manifestSizeSymbol   interface{}
	entries              []decodedBundleEntry
	signaturePayload     []byte
}

// verifyWholeBundleSignature is the bundle-scope counterpart of
// verifyPublicationSignature. It is reached only from there, and only for an
// artifact that declares the bundle hash algorithm and NO statement domain —
// declaring both is refused upstream as ambiguous rather than silently
// resolved, in either direction.
func verifyWholeBundleSignature(wasmBytes []byte, trustedKeys []ed25519.PublicKey) (portable []byte, status ModuleSignatureStatus, err error) {
	portable = StripPublicationTrailer(wasmBytes)
	portableHash := sha256.Sum256(portable)
	status.ContentHash = hex.EncodeToString(portableHash[:])

	decoded, err := decodeModuleBundle(PublicationTrailerRecordBytes(wasmBytes), portable)
	if err != nil {
		status.Reason = "invalid_bundle"
		return portable, status, err
	}
	if len(decoded.signaturePayload) == 0 {
		status.Reason = "unsigned"
		return portable, status, errors.New("module bundle has no signature entry")
	}
	status.Signed = true

	var payload moduleSignaturePayload
	if err := decodeModuleSignaturePayload(decoded.signaturePayload, &payload); err != nil {
		status.Reason = "invalid_signature_payload"
		return portable, status, err
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Algorithm), moduleSignatureAlgorithm) {
		status.Reason = "unsupported_algorithm"
		return portable, status, fmt.Errorf("unsupported module signature algorithm %q", payload.Algorithm)
	}
	if strings.TrimSpace(payload.SignedHashAlgorithm) != bundleSignatureHashAlgorithm {
		status.Reason = "unsupported_hash_algorithm"
		return portable, status, fmt.Errorf("unsupported module bundle signature hash algorithm %q", payload.SignedHashAlgorithm)
	}
	status.KeyID = payload.KeyID
	status.SignatureScope = signatureScopeBundle

	pubKeyHex := strings.ToLower(strings.TrimSpace(payload.PublicKeyHex))
	pubKeyBytes, decodeErr := hex.DecodeString(pubKeyHex)
	if decodeErr != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		status.Reason = "invalid_public_key"
		return portable, status, errors.New("module signer public key must be 32-byte hex")
	}
	status.SignerPubKeyHex = pubKeyHex
	trusted := false
	for _, key := range trustedKeys {
		if len(key) == ed25519.PublicKeySize && hex.EncodeToString(key) == pubKeyHex {
			trusted = true
			break
		}
	}
	if !trusted {
		status.Reason = "untrusted_signer"
		return portable, status, fmt.Errorf("module signer %s is not a trusted publisher key", pubKeyHex)
	}

	sigBytes, sigDecodeErr := hex.DecodeString(strings.TrimSpace(payload.SignatureHex))
	if sigDecodeErr != nil || len(sigBytes) != ed25519.SignatureSize || isAllZeroBytes(sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module signature must be a non-zero 64-byte hex Ed25519 signature")
	}
	digest, digestErr := moduleBundleSignatureDigest(decoded)
	if digestErr != nil {
		status.Reason = "invalid_bundle"
		return portable, status, digestErr
	}
	status.SignedHash = hex.EncodeToString(digest)
	if strings.ToLower(strings.TrimSpace(payload.SignedHashHex)) != status.SignedHash {
		status.Reason = "hash_mismatch"
		return portable, status, fmt.Errorf("module bundle signed digest mismatch: recorded %s computed %s", payload.SignedHashHex, status.SignedHash)
	}
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), digest, sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module bundle signature verification failed")
	}

	status.Verified = true
	status.Reason = "ok"
	return portable, status, nil
}

func decodeModuleBundle(recBytes, portable []byte) (decoded *decodedModuleBundle, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decoded = nil
			err = fmt.Errorf("malformed module bundle record: %v", recovered)
		}
	}()
	if len(recBytes) < 8 || !flatbuffers.BufferHasIdentifier(recBytes, publicationTrailerMagic) {
		return nil, errors.New("module bundle publication trailer is missing")
	}
	rec := getRootAsRECTrailer(recBytes)
	var root *MBL.MBL
	for i := 0; i < rec.recordsLength(); i++ {
		record, ok := rec.record(i)
		if !ok || record.valueType() != recRecordTypeMBL {
			continue
		}
		table, ok := record.value()
		if !ok {
			continue
		}
		root = &MBL.MBL{}
		root.Init(table.Bytes, table.Pos)
		break
	}
	if root == nil {
		return nil, errors.New("publication trailer carries no MBL record")
	}

	out := &decodedModuleBundle{
		version:              root.BundleVersion(),
		moduleFormat:         nullableString(root.ModuleFormat()),
		canonicalModuleHash:  append([]byte(nil), root.CanonicalModuleHashBytes()...),
		manifestHash:         append([]byte(nil), root.ManifestHashBytes()...),
		manifestExportSymbol: nullableString(root.ManifestExportSymbol()),
		manifestSizeSymbol:   nullableString(root.ManifestSizeSymbol()),
	}
	var canonical MBL.CanonicalizationRule
	if root.Canonicalization(&canonical) != nil {
		out.canonicalPresent = true
		out.canonicalVersion = canonical.Version()
		out.customSectionPrefix = nullableString(canonical.StrippedCustomSectionPrefix())
		out.bundleSectionName = nullableString(canonical.BundleSectionName())
		out.hashAlgorithm = nullableString(canonical.HashAlgorithm())
	}
	prefix := "sds."
	if value, ok := out.customSectionPrefix.(string); ok {
		prefix = value
	}
	canonicalPortable, err := stripWasmCustomSectionsWithPrefix(portable, prefix)
	if err != nil {
		return nil, fmt.Errorf("canonicalize module: %w", err)
	}
	moduleHash := sha256.Sum256(canonicalPortable)
	if len(out.canonicalModuleHash) != sha256.Size || !bytesEqualConstantTime(out.canonicalModuleHash, moduleHash[:]) {
		return nil, errors.New("module canonical hash does not match the bundle's recorded hash")
	}

	seen := make(map[string]bool)
	var manifestPayload []byte
	var entry MBL.ModuleBundleEntry
	for i := 0; i < root.EntriesLength(); i++ {
		if !root.Entries(&entry, i) {
			continue
		}
		id := string(entry.EntryId())
		if id == "" {
			return nil, errors.New("module bundle contains an entry without an entryId")
		}
		if seen[id] {
			return nil, fmt.Errorf("module bundle contains duplicate entryId %q", id)
		}
		seen[id] = true
		payload := append([]byte(nil), entry.PayloadBytes()...)
		role := entry.Role()
		decodedEntry := decodedBundleEntry{
			entryID:          id,
			role:             role,
			sectionName:      string(entry.SectionName()),
			payloadEncoding:  entry.PayloadEncoding(),
			flags:            entry.Flags(),
			sha256Hex:        hex.EncodeToString(entry.Sha256Bytes()),
			payload:          payload,
			sectionValue:     nullableString(entry.SectionName()),
			typeRefValue:     normalizedTypeRef(entry.TypeRef()),
			mediaValue:       nullableString(entry.MediaType()),
			descriptionValue: nullableString(entry.Description()),
		}
		if !isSignatureBundleEntry(decodedEntry) {
			recordedHash := entry.Sha256Bytes()
			hash := sha256.Sum256(payload)
			if len(recordedHash) != sha256.Size || !bytesEqualConstantTime(recordedHash, hash[:]) {
				return nil, fmt.Errorf("module bundle entry %q payload hash does not match its recorded sha256", id)
			}
			if manifestPayload == nil && (id == "manifest" || role == MBL.ModuleBundleEntryRoleMANIFEST) {
				manifestPayload = payload
			}
		} else {
			if out.signaturePayload != nil {
				return nil, errors.New("module bundle contains multiple signature entries")
			}
			out.signaturePayload = payload
		}
		out.entries = append(out.entries, decodedEntry)
	}
	if manifestPayload != nil {
		hash := sha256.Sum256(manifestPayload)
		if len(out.manifestHash) != sha256.Size || !bytesEqualConstantTime(out.manifestHash, hash[:]) {
			return nil, errors.New("module manifest payload hash does not match the bundle's manifestHash")
		}
	} else if len(out.manifestHash) != 0 {
		return nil, errors.New("module bundle records a manifestHash but contains no manifest entry")
	}
	return out, nil
}

// moduleBundleSignatureDigest is the PREIMAGE. Do not reorder, do not tidy.
func moduleBundleSignatureDigest(bundle *decodedModuleBundle) ([]byte, error) {
	entries := make([]map[string]interface{}, 0, len(bundle.entries))
	for _, entry := range bundle.entries {
		if isSignatureBundleEntry(entry) {
			continue
		}
		entries = append(entries, map[string]interface{}{
			"entryId":         entry.entryID,
			"role":            int(entry.role),
			"sectionName":     entry.sectionValue,
			"typeRef":         entry.typeRefValue,
			"payloadEncoding": int(entry.payloadEncoding),
			"mediaType":       entry.mediaValue,
			"flags":           int(entry.flags),
			"sha256Hex":       entry.sha256Hex,
			"payloadLength":   len(entry.payload),
			"description":     entry.descriptionValue,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i]["entryId"].(string) < entries[j]["entryId"].(string)
	})
	canonicalization := map[string]interface{}{
		"version":                     1,
		"strippedCustomSectionPrefix": nil,
		"bundleSectionName":           nil,
		"hashAlgorithm":               nil,
	}
	if bundle.canonicalPresent {
		canonicalization["version"] = int(bundle.canonicalVersion)
		canonicalization["strippedCustomSectionPrefix"] = bundle.customSectionPrefix
		canonicalization["bundleSectionName"] = bundle.bundleSectionName
		canonicalization["hashAlgorithm"] = bundle.hashAlgorithm
	}
	statement := map[string]interface{}{
		"version":                1,
		"bundleVersion":          int(bundle.version),
		"moduleFormat":           bundle.moduleFormat,
		"canonicalization":       canonicalization,
		"canonicalModuleHashHex": hex.EncodeToString(bundle.canonicalModuleHash),
		"manifestHashHex":        hex.EncodeToString(bundle.manifestHash),
		"manifestExportSymbol":   bundle.manifestExportSymbol,
		"manifestSizeSymbol":     bundle.manifestSizeSymbol,
		"entries":                entries,
	}
	canonical, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("canonicalize module bundle statement: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return digest[:], nil
}

func isSignatureBundleEntry(entry decodedBundleEntry) bool {
	return entry.role == MBL.ModuleBundleEntryRoleSIGNATURE ||
		strings.EqualFold(entry.entryID, moduleSignatureEntryID) ||
		strings.EqualFold(entry.sectionName, moduleSignatureSectionName)
}

func nullableString(raw []byte) interface{} {
	if raw == nil {
		return nil
	}
	return string(raw)
}

func normalizedTypeRef(raw []byte) interface{} {
	if raw == nil {
		return nil
	}
	text := string(raw)
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return text
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return text
	}
	wire := "flatbuffer"
	if rawWire, ok := object["wireFormat"]; ok {
		switch typed := rawWire.(type) {
		case float64:
			if typed == 1 {
				wire = "aligned-binary"
			}
		case string:
			if strings.EqualFold(strings.ReplaceAll(typed, "_", "-"), "aligned-binary") {
				wire = "aligned-binary"
			}
		}
	}
	object["wireFormat"] = wire
	for _, key := range []string{"fixedStringLength", "byteLength", "requiredAlignment"} {
		if _, ok := object[key]; !ok {
			object[key] = 0
		}
	}
	return object
}

func bytesEqualConstantTime(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

func stripWasmCustomSectionsWithPrefix(wasm []byte, prefix string) ([]byte, error) {
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" || string(wasm[4:8]) != "\x01\x00\x00\x00" {
		return nil, errors.New("WASM module header is invalid")
	}
	out := append([]byte(nil), wasm[:8]...)
	for offset := 8; offset < len(wasm); {
		start := offset
		sectionID := wasm[offset]
		offset++
		size, next, err := decodeULEB128(wasm, offset)
		if err != nil {
			return nil, err
		}
		payloadEnd := next + size
		if payloadEnd < next || payloadEnd > len(wasm) {
			return nil, errors.New("WASM section exceeds module bounds")
		}
		strip := false
		if sectionID == 0 {
			nameLength, nameStart, err := decodeULEB128(wasm, next)
			if err != nil || nameStart+nameLength < nameStart || nameStart+nameLength > payloadEnd {
				return nil, errors.New("WASM custom section name exceeds section bounds")
			}
			strip = strings.HasPrefix(string(wasm[nameStart:nameStart+nameLength]), prefix)
		}
		if !strip {
			out = append(out, wasm[start:payloadEnd]...)
		}
		offset = payloadEnd
	}
	return out, nil
}

func decodeULEB128(data []byte, offset int) (value, next int, err error) {
	var shift uint
	for offset < len(data) {
		current := data[offset]
		offset++
		value |= int(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, offset, nil
		}
		shift += 7
		if shift > 56 {
			return 0, 0, errors.New("WASM ULEB128 value is too large")
		}
	}
	return 0, 0, errors.New("WASM ULEB128 value is truncated")
}
