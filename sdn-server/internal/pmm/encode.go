package pmm

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"

	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
)

// NOTE on the enums: the generated Go binding declares its enum TYPES unexported
// (`pmmTrustTier`), so they cannot be named from another package. The
// EnumValues* maps ARE exported and a value read from one carries the right
// type, so encoding goes through them at the call sites below. That also makes
// the IDL's own symbol table the authority — Validate has already rejected any
// symbol the IDL does not define, and a zero value here would be UNSPECIFIED,
// never a silently-valid substitute.

// MarshalJSON renders the JSON projection: IDL capitalization on every record
// field, enums as IDL symbol names, [ubyte] signatures as lowercase hex, and the
// synthesized browse data under a single lowercase envelope key that is a
// SIBLING of the record and excluded from the signed statement.
func MarshalJSON(m *Manifest, browse []BrowseHint) ([]byte, error) {
	type envelope struct {
		*Manifest
		Browse []BrowseHint `json:"browse"`
	}
	if browse == nil {
		browse = []BrowseHint{}
	}
	return json.MarshalIndent(envelope{Manifest: m, Browse: browse}, "", " ")
}

// MarshalBinary renders the size-prefixed $PMM FlatBuffer.
//
// Re-serialisation is safe: PMM.SIGNATURE covers the canonical STATEMENT, never
// these bytes, so a different-but-equivalent buffer still verifies.
func MarshalBinary(m *Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b := flatbuffers.NewBuilder(1 << 16)

	// Strings and vectors must be created before the tables that reference them.
	entries := make([]Entry, len(m.Modules))
	copy(entries, m.Modules)
	SortModules(entries)

	moduleOffsets := make([]flatbuffers.UOffsetT, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		moduleID := b.CreateString(e.ModuleID)
		pluginID := b.CreateString(e.PluginID)
		plgCID := b.CreateString(e.PLGCID)
		name := b.CreateString(e.Name)
		desc := b.CreateString(e.Description)
		version := b.CreateString(e.Version)
		contentHash := b.CreateString(e.ContentHash)
		artifactPath := b.CreateString(e.ArtifactPath)
		artifactCID := b.CreateString(e.ArtifactCID)
		license := b.CreateString(e.License)
		docURL := b.CreateString(e.DocumentationURL)
		iconURL := b.CreateString(e.IconURL)
		supersedes := b.CreateString(e.SupersedesHash)
		updatedAt := b.CreateString(e.UpdatedAt)

		sigBytes, err := hex.DecodeString(e.ArtifactSignature)
		if err != nil {
			return nil, fmt.Errorf("pmm: %s ARTIFACT_SIGNATURE is not hex: %w", e.ModuleID, err)
		}
		sigVec := b.CreateByteVector(sigBytes)
		targets := createStringVector(b, e.RuntimeTargets, sdspmm.PMMModuleEntryStartRUNTIME_TARGETSVector)
		schemas := createStringVector(b, e.RequiredSchemas, sdspmm.PMMModuleEntryStartREQUIRED_SCHEMASVector)
		perms := createStringVector(b, e.MinPermissions, sdspmm.PMMModuleEntryStartMIN_PERMISSIONSVector)

		sdspmm.PMMModuleEntryStart(b)
		sdspmm.PMMModuleEntryAddMODULE_ID(b, moduleID)
		sdspmm.PMMModuleEntryAddPLUGIN_ID(b, pluginID)
		sdspmm.PMMModuleEntryAddPLG_CID(b, plgCID)
		sdspmm.PMMModuleEntryAddNAME(b, name)
		sdspmm.PMMModuleEntryAddDESCRIPTION(b, desc)
		sdspmm.PMMModuleEntryAddVERSION(b, version)
		sdspmm.PMMModuleEntryAddEPOCH(b, e.Epoch)
		sdspmm.PMMModuleEntryAddCONTENT_HASH(b, contentHash)
		sdspmm.PMMModuleEntryAddARTIFACT_SIZE_BYTES(b, e.SizeBytes)
		sdspmm.PMMModuleEntryAddARTIFACT_PATH(b, artifactPath)
		sdspmm.PMMModuleEntryAddARTIFACT_CID(b, artifactCID)
		sdspmm.PMMModuleEntryAddARTIFACT_SIGNATURE(b, sigVec)
		sdspmm.PMMModuleEntryAddTRUST_TIER(b, sdspmm.EnumValuespmmTrustTier[e.TrustTier])
		sdspmm.PMMModuleEntryAddDEFAULT_ENABLED(b, e.DefaultEnabled)
		sdspmm.PMMModuleEntryAddACCESS_POLICY(b, sdspmm.EnumValuespmmAccessPolicy[e.AccessPolicy])
		sdspmm.PMMModuleEntryAddENTRY_STATE(b, sdspmm.EnumValuespmmEntryState[e.EntryState])
		sdspmm.PMMModuleEntryAddRUNTIME_TARGETS(b, targets)
		sdspmm.PMMModuleEntryAddREQUIRED_SCHEMAS(b, schemas)
		sdspmm.PMMModuleEntryAddMIN_PERMISSIONS(b, perms)
		sdspmm.PMMModuleEntryAddLICENSE(b, license)
		sdspmm.PMMModuleEntryAddDOCUMENTATION_URL(b, docURL)
		sdspmm.PMMModuleEntryAddICON_URL(b, iconURL)
		sdspmm.PMMModuleEntryAddSUPERSEDES_CONTENT_HASH(b, supersedes)
		sdspmm.PMMModuleEntryAddUPDATED_AT(b, updatedAt)
		moduleOffsets = append(moduleOffsets, sdspmm.PMMModuleEntryEnd(b))
	}

	sdspmm.PMMStartMODULESVector(b, len(moduleOffsets))
	for i := len(moduleOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(moduleOffsets[i])
	}
	modulesVec := b.EndVector(len(moduleOffsets))

	trustOff := buildTrustAnchor(b, &m.Trust)

	providerDomain := b.CreateString(m.ProviderDomain)
	providerName := b.CreateString(m.ProviderName)
	description := b.CreateString(m.Description)
	canonicalURL := b.CreateString(m.CanonicalURL)
	createdAt := b.CreateString(m.CreatedAt)
	updatedAt := b.CreateString(m.UpdatedAt)
	expiresAt := b.CreateString(m.ExpiresAt)
	statement := b.CreateString(m.SignedStatement)
	sigRaw, err := hex.DecodeString(m.Signature)
	if err != nil {
		return nil, fmt.Errorf("pmm: SIGNATURE is not hex: %w", err)
	}
	sigVec := b.CreateByteVector(sigRaw)

	sdspmm.PMMStart(b)
	sdspmm.PMMAddPROVIDER_DOMAIN(b, providerDomain)
	sdspmm.PMMAddPROVIDER_NAME(b, providerName)
	sdspmm.PMMAddDESCRIPTION(b, description)
	sdspmm.PMMAddEPOCH(b, m.Epoch)
	sdspmm.PMMAddTRUST(b, trustOff)
	sdspmm.PMMAddMODULES(b, modulesVec)
	sdspmm.PMMAddCANONICAL_URL(b, canonicalURL)
	sdspmm.PMMAddCREATED_AT(b, createdAt)
	sdspmm.PMMAddUPDATED_AT(b, updatedAt)
	sdspmm.PMMAddEXPIRES_AT(b, expiresAt)
	sdspmm.PMMAddSIGNATURE(b, sigVec)
	sdspmm.PMMAddSIGNED_STATEMENT(b, statement)
	root := sdspmm.PMMEnd(b)

	sdspmm.FinishSizePrefixedPMMBuffer(b, root)
	return b.FinishedBytes(), nil
}

func buildTrustAnchor(b *flatbuffers.Builder, t *TrustAnchor) flatbuffers.UOffsetT {
	bondOffsets := make([]flatbuffers.UOffsetT, 0, len(t.BondAddresses))
	for i := range t.BondAddresses {
		cp := &t.BondAddresses[i]
		chain := b.CreateString(cp.Chain)
		addr := b.CreateString(cp.Address)
		sdspmm.ChainProofStart(b)
		sdspmm.ChainProofAddCHAIN(b, chain)
		sdspmm.ChainProofAddADDRESS(b, addr)
		bondOffsets = append(bondOffsets, sdspmm.ChainProofEnd(b))
	}
	sdspmm.PMMTrustAnchorStartBOND_ADDRESSESVector(b, len(bondOffsets))
	for i := len(bondOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(bondOffsets[i])
	}
	bondVec := b.EndVector(len(bondOffsets))

	domain := b.CreateString(t.ProviderDomain)
	peerID := b.CreateString(t.NodePeerID)
	xpub := b.CreateString(t.NodeXpub)
	signingKey := b.CreateString(t.SigningPublicKey)
	keyPath := b.CreateString(t.SigningKeyPath)
	algo := b.CreateString(t.SignatureAlgorithm)
	epmCID := b.CreateString(t.EPMCID)
	dnsName := b.CreateString(t.DNSProofRecordName)
	dnsTXT := b.CreateString(t.DNSProofTXT)
	dnsStmt := b.CreateString(t.DNSProofStatement)
	bondURL := b.CreateString(t.BondAttestationURL)

	sdspmm.PMMTrustAnchorStart(b)
	sdspmm.PMMTrustAnchorAddPROVIDER_DOMAIN(b, domain)
	sdspmm.PMMTrustAnchorAddNODE_PEER_ID(b, peerID)
	sdspmm.PMMTrustAnchorAddNODE_XPUB(b, xpub)
	sdspmm.PMMTrustAnchorAddSIGNING_PUBLIC_KEY(b, signingKey)
	sdspmm.PMMTrustAnchorAddSIGNING_KEY_PATH(b, keyPath)
	sdspmm.PMMTrustAnchorAddSIGNATURE_ALGORITHM(b, algo)
	sdspmm.PMMTrustAnchorAddEPM_CID(b, epmCID)
	sdspmm.PMMTrustAnchorAddDNS_PROOF_RECORD_NAME(b, dnsName)
	sdspmm.PMMTrustAnchorAddDNS_PROOF_TXT(b, dnsTXT)
	sdspmm.PMMTrustAnchorAddDNS_PROOF_STATEMENT(b, dnsStmt)
	sdspmm.PMMTrustAnchorAddBOND_ADDRESSES(b, bondVec)
	sdspmm.PMMTrustAnchorAddBOND_ATTESTATION_URL(b, bondURL)
	return sdspmm.PMMTrustAnchorEnd(b)
}

func createStringVector(b *flatbuffers.Builder, values []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	offsets := make([]flatbuffers.UOffsetT, 0, len(values))
	for _, v := range values {
		offsets = append(offsets, b.CreateString(v))
	}
	start(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets))
}
