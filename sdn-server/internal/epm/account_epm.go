package epm

// ACCOUNT EPMs — the identity record an ACCOUNT publishes through the node it
// signs in to (owner directive 2026-08-28).
//
// # Why the NODE signs a record about somebody else
//
// The built-in wallet's origin controller exposes exactly ONE page-facing
// signing operation (sdn.auth.raw-challenge.v1). A browser account therefore
// CANNOT hand the node a self-signed $EPM: there is no wallet call that would
// produce one. What the account can do is prove, through the ordinary sign-in
// challenge, that it holds the signing key the node has bound to its row.
//
// So an account EPM is a CUSTODIAL record: the subject is the account, the
// issuer is the node, and the node's own signing key produces SIGNATURE. That
// is exactly what the record says on the wire —
//
//	KEYS[0] = the ACCOUNT's literal signing public key   (the subject)
//	KEYS[1] = the NODE's literal signing public key      (the issuer)
//
// — and VerifyEPMSignature (the wire verifier, which accepts a signature from
// any signing key in KEYS) accepts it. Chain verification bound to a card's
// sign-alias key (VerifyEPMSignatureBindingKey) deliberately does NOT: an
// account record is not a self-signed card and must never be mistaken for one.
//
// # Literal keys only (identity-literal-pubkeys policy, owner flip 2026-08-19)
//
// The record carries PUBLIC KEYS, never xpubs or derivation paths. The account's
// key is the literal `signing_pubkey_hex` the auth store bound at sign-in. No
// key is derived, inferred or invented here: what the auth store knows is what
// the record says, and a profile field the account did not send is absent
// rather than filled in.

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
)

var (
	// ErrNoAccountSigningKey is returned when the subject account has no bound
	// signing key. Without one the record would have no subject at all.
	ErrNoAccountSigningKey = errors.New("account has no bound signing key")
	// ErrNoIssuerSigningKey is returned when this node holds no signing key, so
	// it cannot issue a custodial record for anybody.
	ErrNoIssuerSigningKey = errors.New("node holds no signing key to issue an account EPM with")
)

// AccountSubject is everything the node is allowed to say about WHO an account
// EPM is about: the literal signing public key the auth store bound to that
// account. Nothing else about the account reaches the record except the profile
// fields the account itself sent.
type AccountSubject struct {
	// SigningPubKeyHex is the account's Ed25519 signing public key, hex, as the
	// auth user store holds it (users.signing_pubkey_hex).
	SigningPubKeyHex string
}

// BuildAccountEPM builds and signs a custodial $EPM record whose subject is
// `subject` and whose profile fields are `profile` — the same Profile shape
// PUT /api/node/epm accepts, decoded by the same JSON decoder.
//
// The signing discipline is identical to rebuildEPMLocked: build once unsigned
// to obtain the exact wire bytes, derive the canonical payload FROM those
// bytes, sign, rebuild with the signature embedded. Signer and verifier
// therefore agree by construction.
func (s *Service) BuildAccountEPM(profile *Profile, subject AccountSubject) ([]byte, error) {
	if profile == nil {
		profile = &Profile{}
	}
	// §18 validation runs on account input too: a rejected derivation path is
	// operator input and must be reported verbatim, exactly as on the node lane.
	if err := ValidateProfileKeyPaths(profile); err != nil {
		return nil, err
	}

	subjectKeyHex := strings.ToLower(strings.TrimSpace(subject.SigningPubKeyHex))
	if subjectKeyHex == "" {
		return nil, ErrNoAccountSigningKey
	}
	if raw, err := hex.DecodeString(subjectKeyHex); err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: signing_pubkey_hex is not a 32-byte Ed25519 key", ErrNoAccountSigningKey)
	}

	s.mu.RLock()
	issuerKeyHex, signer := s.issuerSigningMaterialLocked()
	s.mu.RUnlock()
	if issuerKeyHex == "" || signer == nil {
		return nil, ErrNoIssuerSigningKey
	}

	signatureTimestamp := time.Now().Unix()
	unsigned, err := buildAccountEPMBytes(profile, subjectKeyHex, issuerKeyHex, "", signatureTimestamp)
	if err != nil {
		return nil, err
	}
	payload, err := EPMSigningPayload(unsigned)
	if err != nil {
		return nil, fmt.Errorf("derive account EPM signing payload: %w", err)
	}
	signature, err := signer(payload)
	if err != nil {
		return nil, fmt.Errorf("sign account EPM: %w", err)
	}
	return buildAccountEPMBytes(profile, subjectKeyHex, issuerKeyHex, hex.EncodeToString(signature), signatureTimestamp)
}

// issuerSigningMaterialLocked reports the node's Ed25519 signing public key
// (hex) and a function that signs with its private half — the same key, in the
// same preference order, that signs the node's own EPM. Caller holds s.mu.
func (s *Service) issuerSigningMaterialLocked() (string, func([]byte) ([]byte, error)) {
	if s.identity != nil && s.identity.SigningPubKey != nil && s.identity.SigningPrivKey != nil {
		if pub, err := s.identity.SigningPubKey.Raw(); err == nil && len(pub) == ed25519.PublicKeySize {
			priv := s.identity.SigningPrivKey
			return hex.EncodeToString(pub), func(payload []byte) ([]byte, error) { return priv.Sign(payload) }
		}
	}
	if len(s.runtimeSigningKey) == ed25519.PrivateKeySize {
		key := s.runtimeSigningKey
		pub := key.Public().(ed25519.PublicKey)
		return hex.EncodeToString(pub), func(payload []byte) ([]byte, error) { return ed25519.Sign(key, payload), nil }
	}
	return "", nil
}

// AccountEPMSubjectKeyHex reads the subject key back out of an account EPM: the
// FIRST signing key in KEYS, which is the account by construction above. The
// reconciler uses it to confirm a stored record still belongs to the binding it
// is filed under.
func AccountEPMSubjectKeyHex(epmData []byte) string {
	if len(epmData) == 0 || !EPM.SizePrefixedEPMBufferHasIdentifier(epmData) {
		return ""
	}
	record := EPM.GetSizePrefixedRootAsEPM(epmData, 0)
	key := new(EPM.CryptoKey)
	for i := 0; i < record.KEYSLength(); i++ {
		if record.KEYS(key, i) && key.KEY_TYPE() == EPM.KeyTypeSigning {
			return strings.ToLower(strings.TrimSpace(string(key.PUBLIC_KEY())))
		}
	}
	return ""
}

// AccountEPMJSON projects a stored account EPM through the SAME projection the
// node EPM JSON endpoint uses, so the dashboard's profile round-trip
// (profileFrom() in spaceaware-ui data/identity.js) reads exactly the fields it
// wrote. `photo_data_url` is not on the wire — it is carried alongside by the
// caller, which is why it is a parameter rather than a lookup.
func AccountEPMJSON(epmData []byte, photoDataURL string) map[string]interface{} {
	if len(epmData) == 0 || !EPM.SizePrefixedEPMBufferHasIdentifier(epmData) {
		return nil
	}
	result := EPMRecordJSON(EPM.GetSizePrefixedRootAsEPM(epmData, 0))
	if photo := strings.TrimSpace(photoDataURL); photo != "" {
		result["photo_data_url"] = photo
	}
	result["directory_kind"] = "account"
	result["entity_type"] = "user"
	return result
}

// buildAccountEPMBytes serializes the account EPM wire bytes. Deterministic for
// a fixed profile, subject, issuer, signature and timestamp — which is what
// makes the unsigned pre-image pass and the signed pass agree.
func buildAccountEPMBytes(profile *Profile, subjectKeyHex, issuerKeyHex, signatureHex string, signatureTimestamp int64) ([]byte, error) {
	builder := flatbuffers.NewBuilder(1024)

	// Every string offset must be created BEFORE the tables that reference it.
	fields := createProfileFieldOffsets(builder, profile)

	subjectPubOff := builder.CreateString(subjectKeyHex)
	issuerPubOff := builder.CreateString(issuerKeyHex)
	ed25519AlgOff := builder.CreateString("ed25519")
	hexEncodingOff := builder.CreateString("hex")

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, subjectPubOff)
	EPM.CryptoKeyAddADDRESS_TYPE(builder, ed25519AlgOff)
	EPM.CryptoKeyAddALGORITHM(builder, ed25519AlgOff)
	EPM.CryptoKeyAddENCODING(builder, hexEncodingOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	subjectKeyOff := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, issuerPubOff)
	EPM.CryptoKeyAddADDRESS_TYPE(builder, ed25519AlgOff)
	EPM.CryptoKeyAddALGORITHM(builder, ed25519AlgOff)
	EPM.CryptoKeyAddENCODING(builder, hexEncodingOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	issuerKeyOff := EPM.CryptoKeyEnd(builder)

	// Subject first, issuer second: KEYS[0] is who the record is ABOUT.
	keyOffsets := []flatbuffers.UOffsetT{subjectKeyOff, issuerKeyOff}
	if subjectKeyHex == issuerKeyHex {
		// The node signing in as itself: one key, stated once.
		keyOffsets = keyOffsets[:1]
	}
	EPM.EPMStartKEYSVector(builder, len(keyOffsets))
	for i := len(keyOffsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(keyOffsets[i])
	}
	keysOff := builder.EndVector(len(keyOffsets))

	var signatureOff flatbuffers.UOffsetT
	if signatureHex != "" {
		signatureOff = builder.CreateString(signatureHex)
	}

	EPM.EPMStart(builder)
	EPM.EPMAddENTITY_TYPE(builder, EPM.EntityTypeUser)
	addProfileFieldOffsets(builder, fields)
	EPM.EPMAddKEYS(builder, keysOff)
	if signatureOff != 0 {
		EPM.EPMAddSIGNATURE(builder, signatureOff)
	}
	EPM.EPMAddSIGNATURE_TIMESTAMP(builder, signatureTimestamp)
	epmOff := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epmOff)

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	return result, nil
}

// profileFieldOffsets holds the built offsets of every operator-editable EPM
// field, so the strings/tables can be created before EPMStart (a FlatBuffers
// requirement) and added after it.
type profileFieldOffsets struct {
	dn, legalName, familyName, givenName   flatbuffers.UOffsetT
	additionalName, prefix, suffix         flatbuffers.UOffsetT
	jobTitle, occupation, email, telephone flatbuffers.UOffsetT
	address, alternateNames                flatbuffers.UOffsetT
}

func createProfileFieldOffsets(builder *flatbuffers.Builder, p *Profile) profileFieldOffsets {
	var out profileFieldOffsets
	if p == nil {
		return out
	}
	create := func(value string) flatbuffers.UOffsetT {
		if strings.TrimSpace(value) == "" {
			return 0
		}
		return builder.CreateString(value)
	}
	out.dn = create(p.DN)
	out.legalName = create(p.LegalName)
	out.familyName = create(p.FamilyName)
	out.givenName = create(p.GivenName)
	out.additionalName = create(p.AdditionalName)
	out.prefix = create(p.HonorificPrefix)
	out.suffix = create(p.HonorificSuffix)
	out.jobTitle = create(p.JobTitle)
	out.occupation = create(p.Occupation)
	out.email = create(p.Email)
	out.telephone = create(p.Telephone)

	if p.Address != nil && !p.Address.IsEmpty() {
		country := create(p.Address.Country)
		region := create(p.Address.Region)
		locality := create(p.Address.Locality)
		postal := create(p.Address.PostalCode)
		street := create(p.Address.Street)
		poBox := create(p.Address.POBox)
		EPM.AddressStart(builder)
		if country != 0 {
			EPM.AddressAddCOUNTRY(builder, country)
		}
		if region != 0 {
			EPM.AddressAddREGION(builder, region)
		}
		if locality != 0 {
			EPM.AddressAddLOCALITY(builder, locality)
		}
		if postal != 0 {
			EPM.AddressAddPOSTAL_CODE(builder, postal)
		}
		if street != 0 {
			EPM.AddressAddSTREET(builder, street)
		}
		if poBox != 0 {
			EPM.AddressAddPOST_OFFICE_BOX_NUMBER(builder, poBox)
		}
		out.address = EPM.AddressEnd(builder)
	}

	names := make([]flatbuffers.UOffsetT, 0, len(p.AlternateNames))
	for _, name := range p.AlternateNames {
		if strings.TrimSpace(name) == "" {
			continue
		}
		names = append(names, builder.CreateString(name))
	}
	if len(names) > 0 {
		EPM.EPMStartALTERNATE_NAMESVector(builder, len(names))
		for i := len(names) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(names[i])
		}
		out.alternateNames = builder.EndVector(len(names))
	}
	return out
}

func addProfileFieldOffsets(builder *flatbuffers.Builder, o profileFieldOffsets) {
	if o.dn != 0 {
		EPM.EPMAddDN(builder, o.dn)
	}
	if o.legalName != 0 {
		EPM.EPMAddLEGAL_NAME(builder, o.legalName)
	}
	if o.familyName != 0 {
		EPM.EPMAddFAMILY_NAME(builder, o.familyName)
	}
	if o.givenName != 0 {
		EPM.EPMAddGIVEN_NAME(builder, o.givenName)
	}
	if o.additionalName != 0 {
		EPM.EPMAddADDITIONAL_NAME(builder, o.additionalName)
	}
	if o.prefix != 0 {
		EPM.EPMAddHONORIFIC_PREFIX(builder, o.prefix)
	}
	if o.suffix != 0 {
		EPM.EPMAddHONORIFIC_SUFFIX(builder, o.suffix)
	}
	if o.jobTitle != 0 {
		EPM.EPMAddJOB_TITLE(builder, o.jobTitle)
	}
	if o.occupation != 0 {
		EPM.EPMAddOCCUPATION(builder, o.occupation)
	}
	if o.email != 0 {
		EPM.EPMAddEMAIL(builder, o.email)
	}
	if o.telephone != 0 {
		EPM.EPMAddTELEPHONE(builder, o.telephone)
	}
	if o.address != 0 {
		EPM.EPMAddADDRESS(builder, o.address)
	}
	if o.alternateNames != 0 {
		EPM.EPMAddALTERNATE_NAMES(builder, o.alternateNames)
	}
}
