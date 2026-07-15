package appmanifest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
)

// FlatBuffer round-trip choice (H1 task 1): AppManifest's canonical form is
// JSON (see MarshalCanonicalJSON in manifest.go). To also satisfy
// "round-trips as a FlatBuffer" without minting a new .fbs type, ToMBL
// wraps that same canonical JSON as a single MANIFEST-role
// ModuleBundleEntry inside an existing $MBL (Module Bundle Listing)
// FlatBuffer (third_party/spacedatastandards-go/MBL,
// internal/sds/schemas/MBL.fbs).
//
// Why $MBL specifically: $MBL's ModuleBundleEntry is already a generic
// {entry_id, role, section_name, type_ref, payload_encoding, media_type,
// sha256, payload, description} envelope for named/typed/hashed byte
// payloads — MANIFEST is one of its six documented roles (MANIFEST,
// AUTHORIZATION, SIGNATURE, TRANSPORT, ATTESTATION, AUXILIARY), and
// internal/modulert/publication_signature.go already embeds a JSON payload
// (a detached-signature envelope) as one MBL entry using exactly this
// payload_encoding=JSON_UTF8 pattern. Using an AUXILIARY/MANIFEST entry to
// carry a JSON app manifest is the same reuse, not a new one. $MBL's doc
// comment describes it narrowly as "for a single-file module delivery
// artifact" (one module's own bundle sections); this widens that to "one
// artifact under management" — an app manifest is exactly one such
// artifact, just not a wasm binary. No field is repurposed to mean
// something it doesn't already mean; module_format documents the
// deviation explicitly (see mblModuleFormat below) so a reader who only
// knows $MBL's original single-module-delivery use is not misled.
const (
	// mblModuleFormat labels the $MBL buffer's module_format field so a
	// reader (or future tooling) can tell an app-manifest $MBL apart from a
	// single-module delivery bundle at a glance, without inspecting entries.
	mblModuleFormat = "sdn-app-manifest/1"
	mblEntryID      = "app-manifest"
	mblSectionName  = "sdn.app.manifest"
	mblMediaType    = "application/json"
)

// ToMBL wraps the manifest's canonical JSON inside an $MBL FlatBuffer as a
// single MANIFEST-role entry, giving the manifest a FlatBuffer-backed
// round-trip through an existing SDS type. The entry's sha256 field is the
// digest of the JSON payload, so FromMBL can detect corruption/tampering
// independent of the outer FlatBuffer's own integrity.
func (a *AppManifest) ToMBL() ([]byte, error) {
	if a == nil {
		return nil, errors.New("app manifest is nil")
	}
	payload, err := a.MarshalCanonicalJSON()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)

	b := flatbuffers.NewBuilder(len(payload) + 256)

	entryIDOff := b.CreateString(mblEntryID)
	sectionOff := b.CreateString(mblSectionName)
	mediaOff := b.CreateString(mblMediaType)
	descOff := b.CreateString(a.Description)
	sha256Off := b.CreateByteVector(sum[:])
	payloadOff := b.CreateByteVector(payload)

	MBL.ModuleBundleEntryStart(b)
	MBL.ModuleBundleEntryAddEntryId(b, entryIDOff)
	MBL.ModuleBundleEntryAddRole(b, MBL.ModuleBundleEntryRoleMANIFEST)
	MBL.ModuleBundleEntryAddSectionName(b, sectionOff)
	MBL.ModuleBundleEntryAddPayloadEncoding(b, MBL.ModulePayloadEncodingJSON_UTF8)
	MBL.ModuleBundleEntryAddMediaType(b, mediaOff)
	MBL.ModuleBundleEntryAddSha256(b, sha256Off)
	MBL.ModuleBundleEntryAddPayload(b, payloadOff)
	MBL.ModuleBundleEntryAddDescription(b, descOff)
	entryOff := MBL.ModuleBundleEntryEnd(b)

	MBL.MBLStartEntriesVector(b, 1)
	b.PrependUOffsetT(entryOff)
	entriesOff := b.EndVector(1)

	formatOff := b.CreateString(mblModuleFormat)

	MBL.MBLStart(b)
	MBL.MBLAddBundleVersion(b, 1)
	MBL.MBLAddModuleFormat(b, formatOff)
	MBL.MBLAddEntries(b, entriesOff)
	root := MBL.MBLEnd(b)

	MBL.FinishMBLBuffer(b, root)
	return b.FinishedBytes(), nil
}

// FromMBL is the inverse of ToMBL: it locates the MANIFEST-role entry in an
// $MBL FlatBuffer, verifies its declared sha256 against the payload bytes,
// and parses+validates the JSON payload as an AppManifest.
//
// buf may come from an untrusted source (e.g. a peer-delivered app
// manifest), so every FlatBuffer accessor call is guarded by recover: a
// crafted/corrupt buffer must produce an error, never a panic — the same
// defensive posture internal/modulert/manifest.go's parseManifestFlatBuffer
// and publication_signature.go's findModuleSignatureEntry already take for
// untrusted FlatBuffer input.
func FromMBL(buf []byte) (manifest *AppManifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			manifest = nil
			err = fmt.Errorf("malformed app manifest $MBL flatbuffer: %v", r)
		}
	}()

	if len(buf) < 8 || !MBL.MBLBufferHasIdentifier(buf) {
		return nil, errors.New("buffer does not carry the $MBL file identifier")
	}
	root := MBL.GetRootAsMBL(buf, 0)

	var entry MBL.ModuleBundleEntry
	for i := 0; i < root.EntriesLength(); i++ {
		if !root.Entries(&entry, i) {
			continue
		}
		if entry.Role() != MBL.ModuleBundleEntryRoleMANIFEST {
			continue
		}
		payload := entry.PayloadBytes()
		if len(payload) == 0 {
			continue
		}
		if want := entry.Sha256Bytes(); len(want) == sha256.Size {
			got := sha256.Sum256(payload)
			if !bytes.Equal(want, got[:]) {
				return nil, fmt.Errorf("app manifest payload sha256 mismatch: entry declares %x, payload hashes to %x", want, got)
			}
		}
		return Parse(append([]byte(nil), payload...))
	}
	return nil, errors.New("$MBL buffer carries no MANIFEST-role app-manifest entry")
}
