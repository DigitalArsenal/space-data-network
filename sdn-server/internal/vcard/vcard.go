// Package vcard provides bidirectional conversion between EPM (Entity Profile Message)
// FlatBuffers and iPhone-compatible vCard format.
package vcard

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/emersion/go-vcard"
)

// epmCIDString computes the canonical CIDv1(raw, sha2-256) of a serialized
// EPM — the same identity epm.ComputeEPMCID publishes (duplicated here to
// avoid an import cycle; both are locked to the same construction).
func epmCIDString(epmBytes []byte) (string, error) {
	hash := sha256.Sum256(epmBytes)
	multihash, err := mh.Encode(hash[:], mh.SHA2_256)
	if err != nil {
		return "", err
	}
	return cid.NewCidV1(cid.Raw, multihash).String(), nil
}

// Errors
var (
	ErrEmptyEPM   = errors.New("EPM data is empty")
	ErrEmptyVCard = errors.New("vCard data is empty")
	ErrInvalidEPM = errors.New("invalid EPM data")
)

const (
	FieldSDNEPMBase64             = "X-SDN-EPM-B64"
	FieldSDNEPMSignature          = "X-SDN-EPM-SIGNATURE"
	FieldSDNEPMSignatureTimestamp = "X-SDN-EPM-SIGNATURE-TIMESTAMP"
)

const (
	iphoneVCardProdID       = "-//Apple Inc.//iPhone OS 15.1.1//EN"
	signingAliasDomain      = "signing.spacedatanetwork.org"
	encryptionAliasDomain   = "encryption.spacedatanetwork.org"
	bitcoinAliasDomain      = "bitcoin.spacedatanetwork.org"
	ethereumAliasDomain     = "ethereum.spacedatanetwork.org"
	solanaAliasDomain       = "solana.spacedatanetwork.org"
	vcardFoldLineLimitBytes = 74

	// Owner rule (graph task nst-qr-identity-verify): vCard v3 importers
	// (iPhone/Android) drop X-* properties, so EVERY piece of the EPM
	// verification chain must ride in EMAIL aliases shaped
	// <value>@<kind>.spacedatanetwork.org.
	//
	// §21 (owner ruling 2026-08-19): the sign/encrypt aliases carry the
	// LITERAL PUBLIC KEY BYTES (base64url), not the derivation path. xpub and
	// derivation paths are PRIVATE — a verifier reads the key from the alias
	// and the record's KEYS[], fetches the record by CID, and verifies the
	// signature against the published key. The xpub alias is RETIRED. Domains
	// are UNCHANGED (sign./encrypt.); the constants are renamed in code only
	// to say what the local part now is (key bytes, not a path).
	signKeyAliasDomain    = "sign.spacedatanetwork.org"
	encryptKeyAliasDomain = "encrypt.spacedatanetwork.org"
	epmSigAliasDomain     = "epmsig.spacedatanetwork.org"
	epmTsAliasDomain      = "epmts.spacedatanetwork.org"
	epmCidAliasDomain     = "epmcid.spacedatanetwork.org"
	// PeerAliasDomain carries the libp2p peer id for nodes that have not
	// published an EPM yet (their compact card would otherwise hold no
	// machine identity at all).
	PeerAliasDomain = "peer.spacedatanetwork.org"
)

// EPMToVCard converts an EPM FlatBuffer to an iPhone-compatible vCard 3.0 string.
func EPMToVCard(epmBytes []byte) (string, error) {
	if len(epmBytes) == 0 {
		return "", ErrEmptyEPM
	}

	// Check for size-prefixed buffer
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return "", ErrInvalidEPM
	}

	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	card := vcard.Card{}
	card.Set("VERSION", &vcard.Field{Value: "3.0"})
	card.Set("PRODID", &vcard.Field{
		Value:  iphoneVCardProdID,
		Params: vcard.Params{"VALUE": []string{"TEXT"}},
	})

	// Distinguished Name -> FN (Formatted Name)
	// Default node DNs embed libp2p's "<peer.ID 16*abc123>" short form —
	// machine noise, never a contact name (same guard as the compact cards).
	// vCard 3.0 requires FN, so noisy/absent DNs fall back like they do there.
	fn := strings.TrimSpace(safeString(epm.DN()))
	if fn == "" || strings.Contains(fn, "<peer.ID") {
		fn = "SDN Node"
	}
	card.Add("FN", &vcard.Field{Value: fn})

	// Legal Name -> ORG (Organization)
	if legalName := epm.LEGAL_NAME(); legalName != nil {
		card.Add("ORG", &vcard.Field{Value: string(legalName)})
	}

	// Name components -> N (Structured Name)
	familyName := safeString(epm.FAMILY_NAME())
	givenName := safeString(epm.GIVEN_NAME())
	if familyName != "" || givenName != "" {
		additionalName := safeString(epm.ADDITIONAL_NAME())
		honorificPrefix := safeString(epm.HONORIFIC_PREFIX())
		honorificSuffix := safeString(epm.HONORIFIC_SUFFIX())

		// vCard N format: family;given;additional;prefix;suffix
		n := []string{familyName, givenName, additionalName, honorificPrefix, honorificSuffix}
		card.Add("N", &vcard.Field{Value: strings.Join(n, ";")})
	}

	// Email
	if email := epm.EMAIL(); email != nil {
		card.Add("EMAIL", &vcard.Field{Value: string(email)})
	}

	// Telephone
	if telephone := epm.TELEPHONE(); telephone != nil {
		card.Add("TEL", &vcard.Field{Value: string(telephone)})
	}

	// Job Title -> TITLE
	if jobTitle := epm.JOB_TITLE(); jobTitle != nil {
		card.Add("TITLE", &vcard.Field{Value: string(jobTitle)})
	}

	// Occupation -> ROLE
	if occupation := epm.OCCUPATION(); occupation != nil {
		card.Add("ROLE", &vcard.Field{Value: string(occupation)})
	}

	// Address -> ADR
	addr := new(EPM.Address)
	if epm.ADDRESS(addr) != nil {
		// vCard ADR format: pobox;ext;street;locality;region;code;country
		addrParts := []string{
			safeString(addr.POST_OFFICE_BOX_NUMBER()),
			"", // extended address (not in EPM)
			safeString(addr.STREET()),
			safeString(addr.LOCALITY()),
			safeString(addr.REGION()),
			safeString(addr.POSTAL_CODE()),
			safeString(addr.COUNTRY()),
		}
		card.Add("ADR", &vcard.Field{Value: strings.Join(addrParts, ";")})
	}

	// OWNER RULING 2026-08-04: multiaddrs/protocol lists are NOT contact
	// data and no longer ride on any card (they used to be emitted here as
	// URL lines). They live on the record and the API surfaces. Alternate
	// names ride in NICKNAME — the official vCard 3.0 property — instead of
	// the old X-ALTERNATE-NAME extension.
	var altNames []string
	for i := 0; i < epm.ALTERNATE_NAMESLength(); i++ {
		if name := strings.TrimSpace(safeString(epm.ALTERNATE_NAMES(i))); name != "" {
			altNames = append(altNames, name)
		}
	}
	if len(altNames) > 0 {
		card.Add("NICKNAME", &vcard.Field{Value: strings.Join(altNames, ",")})
	}

	// OWNER DIRECTIVE 2026-07-27: no key BYTES on the vCard surface, and no
	// embedded EPM blob. What used to be emitted here — X-SIGNING-KEY,
	// X-ENCRYPTION-KEY, X-PUBLIC-KEY and X-SDN-EPM-B64 — is gone.
	//
	// The vCard still carries everything a verifier needs, in the email-alias
	// chain built below: the account xpub, the sign/encrypt DERIVATION PATHS,
	// and epmsig/epmts/epmcid. A verifier derives the secp256k1 key from
	// xpub + path (that is the whole point of the paradigm) and fetches the
	// authoritative record by CID. The blob was redundant to that chain, and
	// the key bytes were redundant to the xpub.
	//
	// ⚠ THIS IS THE VCARD SURFACE ONLY. The EPM RECORD's KEYS[] still carries
	// the ed25519 public key and MUST keep carrying it: SLIP-10 ed25519 has no
	// public derivation, so that key cannot be derived from any xpub and
	// un-publishing it from the RECORD would make every ed25519 EPM signature
	// unverifiable (§17 of graph/tasks/nst-node-admin-contract.md). vCard is a
	// contact card; the record is the record. Removing bytes from the card does
	// not un-publish them from the record.

	// OWNER RULING 2026-08-04: NO extension properties on any card — not
	// X-SDN-EPM-SIGNATURE/-TIMESTAMP (they ride in the epmsig/epmts EMAIL
	// aliases below, which vCard importers actually keep), and not the
	// itemN.X-ABLabel/X-ABRELATEDNAMES Apple mirror this function used to
	// append (which also embedded the binary EPM — the single biggest source
	// of card bloat). The machine identity is EXACTLY the EMAIL alias chain.

	// Encode to string
	var b strings.Builder
	enc := vcard.NewEncoder(&b)
	if err := enc.Encode(card); err != nil {
		return "", err
	}

	return insertRawVCardLines(b.String(), AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes)), nil
}

type appleIdentityLine struct {
	Label       string
	Value       string
	EmailType   string
	EmailDomain string
}

// AppleIdentityLinesFromEPM mirrors cryptographic EPM fields into the same
// vCard 3.0 itemN.X-ABRELATEDNAMES shape used by hd-wallet-wasm. iOS Contacts
// preserves these fields, unlike arbitrary X-SDN custom fields.
func AppleIdentityLinesFromEPM(epm *EPM.EPM, epmBytes []byte, includeBinaryEPM bool) []string {
	if epm == nil {
		return nil
	}
	entries := appleIdentityEntriesFromEPM(epm, epmBytes, includeBinaryEPM)
	if len(entries) == 0 {
		return nil
	}

	lines := make([]string, 0, len(entries)*3)
	seenAlias := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.EmailDomain == "" || entry.EmailType == "" || !isSafeEmailLocalPart(entry.Value) {
			continue
		}
		line := foldVCardLine("EMAIL;type=INTERNET;type=" + entry.EmailType + ":" + entry.Value + "@" + entry.EmailDomain)
		if _, ok := seenAlias[line]; ok {
			continue
		}
		seenAlias[line] = struct{}{}
		lines = append(lines, line)
	}

	item := 1
	for _, entry := range entries {
		value := strings.TrimSpace(entry.Value)
		if value == "" {
			continue
		}
		lines = append(lines,
			foldVCardLine("item"+strconv.Itoa(item)+".X-ABLabel:"+escapeVCardText(entry.Label)),
			foldVCardLine("item"+strconv.Itoa(item)+".X-ABRELATEDNAMES:"+escapeVCardText(value)),
		)
		item++
	}

	return lines
}

// AppleIdentityEmailAliasLinesFromEPM returns only the iPhone-visible email
// aliases for EPM identity material. When kinds are given, only those alias
// kinds are kept (used by the compact QR card to hold the verification chain
// without the bulkier public-key aliases). Pass epmBytes to include the
// epmcid alias.
func AppleIdentityEmailAliasLinesFromEPM(epm *EPM.EPM, epmBytes []byte, kinds ...string) []string {
	if epm == nil {
		return nil
	}
	entries := appleIdentityEntriesFromEPM(epm, epmBytes, false)
	if len(entries) == 0 {
		return nil
	}
	var allowed map[string]struct{}
	if len(kinds) > 0 {
		allowed = make(map[string]struct{}, len(kinds))
		for _, k := range kinds {
			allowed[k] = struct{}{}
		}
	}

	lines := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.EmailDomain == "" || entry.EmailType == "" || !isSafeEmailLocalPart(entry.Value) {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[entry.EmailType]; !ok {
				continue
			}
		}
		line := foldVCardLine("EMAIL;type=INTERNET;type=" + entry.EmailType + ":" + entry.Value + "@" + entry.EmailDomain)
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}

	return lines
}

// CompactQRVCardKinds is the alias-kind allowlist for scannable QR cards.
// OWNER RULING 2026-08-04 ("there's too much data, I can't scan the QR —
// just the fields I requested, with the fields for digital signature and
// encryption like the current node's EPM card"): the two literal-key aliases
// and the EPM signature. §21 (2026-08-19) retired the xpub alias — the local
// parts are now b64url(key bytes), not derivation paths, so the xpub is no
// longer on the card at all. epmts/epmcid stay dropped from the QR — both are
// recoverable from the record the signature already binds, and every alias
// line costs scan density. The downloadable .vcf keeps the full alias chain.
var CompactQRVCardKinds = []string{"sign", "encrypt", "epmsig"}

// CompactQRVCard builds the scannable VERSION:3.0 contact card for an EPM:
// structured name + human contact fields (email, phone, work address) plus
// the verification-chain email aliases. This is the card behind every node
// QR — self and observed peers alike — so iPhone/Android imports carry both
// the human identity and the machine identity.
func CompactQRVCard(epmBytes []byte) (string, error) {
	if len(epmBytes) == 0 {
		return "", ErrEmptyEPM
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return "", ErrInvalidEPM
	}
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	displayName := strings.TrimSpace(safeString(epm.DN()))
	if displayName == "" {
		displayName = strings.TrimSpace(safeString(epm.LEGAL_NAME()))
	}
	// Default node DNs embed libp2p's "<peer.ID 16*abc123>" short form —
	// ugly on a phone contact and a peer-id leak; neutralize it.
	if displayName == "" || strings.Contains(displayName, "<peer.ID") {
		displayName = "SDN Node"
	}
	familyName := strings.TrimSpace(safeString(epm.FAMILY_NAME()))
	givenName := strings.TrimSpace(safeString(epm.GIVEN_NAME()))
	additionalName := strings.TrimSpace(safeString(epm.ADDITIONAL_NAME()))
	honorificPrefix := strings.TrimSpace(safeString(epm.HONORIFIC_PREFIX()))
	honorificSuffix := strings.TrimSpace(safeString(epm.HONORIFIC_SUFFIX()))
	if familyName+givenName+additionalName+honorificPrefix+honorificSuffix == "" {
		givenName = displayName
	}
	joinComponents := func(values ...string) string {
		escaped := make([]string, len(values))
		for i, v := range values {
			escaped[i] = escapeVCardText(v)
		}
		return strings.Join(escaped, ";")
	}

	lines := []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"N:" + joinComponents(familyName, givenName, additionalName, honorificPrefix, honorificSuffix),
		"FN:" + escapeVCardText(displayName),
	}
	if org := strings.TrimSpace(safeString(epm.LEGAL_NAME())); org != "" && org != displayName {
		lines = append(lines, "ORG:"+escapeVCardText(org))
	}
	if email := strings.TrimSpace(safeString(epm.EMAIL())); email != "" {
		lines = append(lines, "EMAIL:"+escapeVCardText(email))
	}
	if tel := strings.TrimSpace(safeString(epm.TELEPHONE())); tel != "" {
		lines = append(lines, "TEL:"+escapeVCardText(tel))
	}
	address := new(EPM.Address)
	if epm.ADDRESS(address) != nil {
		addressValues := []string{
			strings.TrimSpace(safeString(address.POST_OFFICE_BOX_NUMBER())),
			"",
			strings.TrimSpace(safeString(address.STREET())),
			strings.TrimSpace(safeString(address.LOCALITY())),
			strings.TrimSpace(safeString(address.REGION())),
			strings.TrimSpace(safeString(address.POSTAL_CODE())),
			strings.TrimSpace(safeString(address.COUNTRY())),
		}
		if strings.Join(addressValues, "") != "" {
			lines = append(lines, "ADR;TYPE=WORK:"+joinComponents(addressValues...))
		}
	}
	// Contact lines fold here; the alias lines arrive pre-folded from
	// AppleIdentityEmailAliasLinesFromEPM (re-folding a folded line would
	// corrupt it).
	for i, line := range lines {
		lines[i] = foldVCardLine(line)
	}
	lines = append(lines, AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes, CompactQRVCardKinds...)...)
	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

// CardCarriesCryptoIdentity reports whether a vCard string carries the
// minimum crypto identity a scannable card must have (OWNER LAW 2026-07-31;
// §21 amendment 2026-08-19): the two literal-key aliases (sign + encrypt) and
// the EPM signature chain (epmsig). The xpub alias is no longer required — it
// is retired under §21 (the local parts are now key bytes, not derivation
// paths, so the xpub is not on the card). Cards failing this must never be
// served as QR.
func CardCarriesCryptoIdentity(card string) bool {
	// Alias lines are folded at 75 octets, so the needle must not span the
	// fold point; the "@<domain>" needles sit directly after short local
	// parts' unfolded prefix only for epmts — use domain needles on the
	// UNFOLDED text to be safe.
	unfolded := strings.ReplaceAll(strings.ReplaceAll(card, "\r\n ", ""), "\n ", "")
	for _, domain := range []string{
		"@" + signKeyAliasDomain,
		"@" + encryptKeyAliasDomain,
		"@" + epmSigAliasDomain,
	} {
		if !strings.Contains(unfolded, domain) {
			return false
		}
	}
	return true
}

// CompactQRVCardForPeer was the minimal name+peer-id scannable card for
// peers without an EPM. DELETED under owner law 2026-07-31: a QR card
// without the full crypto identity (sign/encrypt literal keys + epmsig
// chain) must never exist — CardCarriesCryptoIdentity gates every QR
// serving path, and a peer we hold no signed EPM for gets NO card at all.

// hexPublicKeyToAlias decodes a hex-encoded PUBLIC_KEY (as carried in the EPM
// record's CryptoKey) to raw bytes and re-encodes as base64url — the email-safe
// local part the §21 sign/encrypt aliases carry. Returns ("", false) when the
// hex is absent or undecodable, so the caller simply skips the alias rather
// than emitting a dead row.
func hexPublicKeyToAlias(hexKey string) (string, bool) {
	hexKey = strings.TrimSpace(hexKey)
	if hexKey == "" {
		return "", false
	}
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(raw), true
}

func appleIdentityEntriesFromEPM(epm *EPM.EPM, epmBytes []byte, includeBinaryEPM bool) []appleIdentityLine {
	var entries []appleIdentityLine

	key := new(EPM.CryptoKey)
	// ONE-ROW INVARIANT (§21): exactly ONE sign alias (the first signing key
	// — the one that produced SIGNATURE) + ONE encrypt alias (the first
	// encryption key). A record that carries two of the same KEY_TYPE must
	// yield one row, not two: two rows of the same alias kind are
	// indistinguishable to every consumer that scans the card
	// (sdn-vcf-duplicate-sign-alias precedent).
	signEmitted := false
	encryptEmitted := false
	for i := 0; i < epm.KEYSLength(); i++ {
		if !epm.KEYS(key, i) {
			continue
		}
		publicKey := strings.TrimSpace(string(key.PUBLIC_KEY()))
		if publicKey == "" {
			continue
		}

		addressType := strings.TrimSpace(string(key.ADDRESS_TYPE()))
		// §21 (owner ruling 2026-08-19): the sign/encrypt aliases carry the
		// LITERAL PUBLIC KEY BYTES (b64url of the hex-decoded key), not the
		// derivation path. xpub and KEY_PATH are private — a verifier reads
		// the key from this alias and the record's KEYS[], fetches the record
		// by CID, and verifies the signature against the published key. No
		// derivation, no derivable gate, no xpub alias.
		keyAlias, ok := hexPublicKeyToAlias(publicKey)
		if !ok {
			continue
		}
		switch key.KEY_TYPE() {
		case EPM.KeyTypeSigning:
			if signEmitted {
				continue
			}
			entries = append(entries, appleIdentityLine{
				Label:       joinedLabel("Signing Public Key", addressType, ""),
				Value:       keyAlias,
				EmailType:   "sign",
				EmailDomain: signKeyAliasDomain,
			})
			signEmitted = true
		case EPM.KeyTypeEncryption:
			if encryptEmitted {
				continue
			}
			entries = append(entries, appleIdentityLine{
				Label:       joinedLabel("Encryption Public Key", addressType, ""),
				Value:       keyAlias,
				EmailType:   "encrypt",
				EmailDomain: encryptKeyAliasDomain,
			})
			encryptEmitted = true
		default:
			// Unclassified key: no alias.
		}
	}

	proof := new(EPM.ChainProof)
	for i := 0; i < epm.CHAIN_PROOFSLength(); i++ {
		if !epm.CHAIN_PROOFS(proof, i) {
			continue
		}
		chain := strings.ToLower(strings.TrimSpace(string(proof.CHAIN())))
		address := strings.TrimSpace(string(proof.ADDRESS()))
		if chain == "" || address == "" {
			continue
		}
		emailDomain := chainAddressAliasDomain(chain)
		entries = append(entries, appleIdentityLine{
			Label:       joinedLabel(chainDisplayName(chain)+" Address", strings.TrimSpace(string(proof.KEY_PATH()))),
			Value:       address,
			EmailType:   chain,
			EmailDomain: emailDomain,
		})
	}

	if signature := strings.TrimSpace(string(epm.SIGNATURE())); signature != "" {
		entries = append(entries, appleIdentityLine{
			Label: "EPM Signature",
			Value: signature,
		})
		// Alias form: base64url of the raw signature bytes (hex is not a
		// compact email local part; b64url is email-safe).
		if sigBytes, err := hex.DecodeString(signature); err == nil && len(sigBytes) > 0 {
			entries = append(entries, appleIdentityLine{
				Label:       "EPM Signature",
				Value:       base64.RawURLEncoding.EncodeToString(sigBytes),
				EmailType:   "epmsig",
				EmailDomain: epmSigAliasDomain,
			})
		}
	}
	if ts := epm.SIGNATURE_TIMESTAMP(); ts != 0 {
		entries = append(entries, appleIdentityLine{
			Label:       "EPM Signature Timestamp",
			Value:       strconv.FormatInt(ts, 10),
			EmailType:   "epmts",
			EmailDomain: epmTsAliasDomain,
		})
	}
	if len(epmBytes) > 0 {
		if epmCid, err := epmCIDString(epmBytes); err == nil {
			entries = append(entries, appleIdentityLine{
				Label:       "EPM CID",
				Value:       epmCid,
				EmailType:   "epmcid",
				EmailDomain: epmCidAliasDomain,
			})
		}
	}
	// The "Binary EPM" related-name used to carry the entire serialized record
	// base64-encoded — the same blob as X-SDN-EPM-B64, wearing a different
	// property name. Removed by the same owner directive: the record is fetched
	// by its epmcid alias, so a copy of it on the card is redundant.
	_ = includeBinaryEPM

	return entries
}

func chainAddressAliasDomain(chain string) string {
	switch chain {
	case "bitcoin":
		return bitcoinAliasDomain
	case "ethereum":
		return ethereumAliasDomain
	case "solana":
		return solanaAliasDomain
	default:
		return ""
	}
}

func chainDisplayName(chain string) string {
	switch chain {
	case "bitcoin":
		return "Bitcoin"
	case "ethereum":
		return "Ethereum"
	case "solana":
		return "Solana"
	default:
		return chain
	}
}

func joinedLabel(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " ")
}

func isSafeEmailLocalPart(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '.', '_', '-', '+':
			continue
		default:
			return false
		}
	}
	return true
}

func insertRawVCardLines(vcardStr string, lines []string) string {
	return insertRawVCardLinesCRLF(vcardStr, lines)
}

// insertRawVCardLinesCRLF splices lines before END:VCARD and emits strict
// CRLF line endings — vCard 3.0 importers (iOS in particular) expect CRLF
// throughout; the previous implementation left the body \n-separated.
func insertRawVCardLinesCRLF(vcardStr string, lines []string) string {
	if len(lines) == 0 {
		if strings.Contains(vcardStr, "\r\n") {
			return vcardStr
		}
		return strings.ReplaceAll(strings.TrimRight(vcardStr, "\n"), "\n", "\r\n") + "\r\n"
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(vcardStr), "\r\n", "\n")
	insert := strings.ReplaceAll(strings.Join(lines, "\n"), "\r\n", "\n")
	var out string
	if strings.Contains(normalized, "\nEND:VCARD") {
		out = strings.Replace(normalized, "\nEND:VCARD", "\n"+insert+"\nEND:VCARD", 1)
	} else {
		out = strings.TrimRight(normalized, "\n") + "\n" + insert + "\nEND:VCARD"
	}
	return strings.ReplaceAll(out, "\n", "\r\n") + "\r\n"
}

func foldVCardLine(line string) string {
	if len(line) <= vcardFoldLineLimitBytes {
		return line
	}
	var b strings.Builder
	for len(line) > vcardFoldLineLimitBytes {
		b.WriteString(line[:vcardFoldLineLimitBytes])
		b.WriteString("\r\n ")
		line = line[vcardFoldLineLimitBytes:]
	}
	b.WriteString(line)
	return b.String()
}

func escapeVCardText(value string) string {
	return strings.NewReplacer(
		"\\", "\\\\",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
		",", "\\,",
		";", "\\;",
	).Replace(value)
}

// VCardToEPM converts a vCard string to an EPM FlatBuffer.
func VCardToEPM(vcardStr string) ([]byte, error) {
	if vcardStr == "" {
		return nil, ErrEmptyVCard
	}

	dec := vcard.NewDecoder(strings.NewReader(vcardStr))
	card, err := dec.Decode()
	if err != nil {
		return nil, err
	}
	if embedded, err := EmbeddedEPMFromCard(card); err != nil {
		return nil, err
	} else if len(embedded) > 0 {
		return embedded, nil
	}

	builder := flatbuffers.NewBuilder(1024)

	// Extract fields from vCard
	var dnOffset, legalNameOffset, emailOffset, telOffset flatbuffers.UOffsetT
	var familyNameOffset, givenNameOffset, additionalNameOffset flatbuffers.UOffsetT
	var prefixOffset, suffixOffset, titleOffset, roleOffset flatbuffers.UOffsetT

	// FN -> DN
	if fn := card.Get("FN"); fn != nil && fn.Value != "" {
		dnOffset = builder.CreateString(fn.Value)
	}

	// ORG -> LEGAL_NAME
	if org := card.Get("ORG"); org != nil && org.Value != "" {
		legalNameOffset = builder.CreateString(org.Value)
	}

	// EMAIL
	if email := card.Get("EMAIL"); email != nil && email.Value != "" {
		emailOffset = builder.CreateString(email.Value)
	}

	// TEL -> TELEPHONE
	if tel := card.Get("TEL"); tel != nil && tel.Value != "" {
		telOffset = builder.CreateString(tel.Value)
	}

	// TITLE -> JOB_TITLE
	if title := card.Get("TITLE"); title != nil && title.Value != "" {
		titleOffset = builder.CreateString(title.Value)
	}

	// ROLE -> OCCUPATION
	if role := card.Get("ROLE"); role != nil && role.Value != "" {
		roleOffset = builder.CreateString(role.Value)
	}

	// N -> Name components (family;given;additional;prefix;suffix)
	if n := card.Get("N"); n != nil && n.Value != "" {
		parts := strings.Split(n.Value, ";")
		if len(parts) > 0 && parts[0] != "" {
			familyNameOffset = builder.CreateString(parts[0])
		}
		if len(parts) > 1 && parts[1] != "" {
			givenNameOffset = builder.CreateString(parts[1])
		}
		if len(parts) > 2 && parts[2] != "" {
			additionalNameOffset = builder.CreateString(parts[2])
		}
		if len(parts) > 3 && parts[3] != "" {
			prefixOffset = builder.CreateString(parts[3])
		}
		if len(parts) > 4 && parts[4] != "" {
			suffixOffset = builder.CreateString(parts[4])
		}
	}

	// ADR -> Address (pobox;ext;street;locality;region;code;country)
	var addressOffset flatbuffers.UOffsetT
	if adr := card.Get("ADR"); adr != nil && adr.Value != "" {
		parts := strings.Split(adr.Value, ";")
		var poBoxOff, streetOff, localityOff, regionOff, postalOff, countryOff flatbuffers.UOffsetT

		if len(parts) > 0 && parts[0] != "" {
			poBoxOff = builder.CreateString(parts[0])
		}
		// parts[1] is extended address, skipped
		if len(parts) > 2 && parts[2] != "" {
			streetOff = builder.CreateString(parts[2])
		}
		if len(parts) > 3 && parts[3] != "" {
			localityOff = builder.CreateString(parts[3])
		}
		if len(parts) > 4 && parts[4] != "" {
			regionOff = builder.CreateString(parts[4])
		}
		if len(parts) > 5 && parts[5] != "" {
			postalOff = builder.CreateString(parts[5])
		}
		if len(parts) > 6 && parts[6] != "" {
			countryOff = builder.CreateString(parts[6])
		}

		EPM.AddressStart(builder)
		if poBoxOff != 0 {
			EPM.AddressAddPOST_OFFICE_BOX_NUMBER(builder, poBoxOff)
		}
		if streetOff != 0 {
			EPM.AddressAddSTREET(builder, streetOff)
		}
		if localityOff != 0 {
			EPM.AddressAddLOCALITY(builder, localityOff)
		}
		if regionOff != 0 {
			EPM.AddressAddREGION(builder, regionOff)
		}
		if postalOff != 0 {
			EPM.AddressAddPOSTAL_CODE(builder, postalOff)
		}
		if countryOff != 0 {
			EPM.AddressAddCOUNTRY(builder, countryOff)
		}
		addressOffset = EPM.AddressEnd(builder)
	}

	// URL -> MULTIFORMAT_ADDRESS (for IPNS addresses)
	var multiAddrOffset flatbuffers.UOffsetT
	urls := card.Values("URL")
	if len(urls) > 0 {
		urlOffsets := make([]flatbuffers.UOffsetT, 0, len(urls))
		for _, url := range urls {
			if url != "" {
				urlOffsets = append(urlOffsets, builder.CreateString(url))
			}
		}
		if len(urlOffsets) > 0 {
			EPM.EPMStartMULTIFORMAT_ADDRESSVector(builder, len(urlOffsets))
			for i := len(urlOffsets) - 1; i >= 0; i-- {
				builder.PrependUOffsetT(urlOffsets[i])
			}
			multiAddrOffset = builder.EndVector(len(urlOffsets))
		}
	}

	// X-ALTERNATE-NAME -> ALTERNATE_NAMES
	var altNamesOffset flatbuffers.UOffsetT
	altNames := card.Values("X-ALTERNATE-NAME")
	if len(altNames) > 0 {
		altNameOffsets := make([]flatbuffers.UOffsetT, 0, len(altNames))
		for _, name := range altNames {
			if name != "" {
				altNameOffsets = append(altNameOffsets, builder.CreateString(name))
			}
		}
		if len(altNameOffsets) > 0 {
			EPM.EPMStartALTERNATE_NAMESVector(builder, len(altNameOffsets))
			for i := len(altNameOffsets) - 1; i >= 0; i-- {
				builder.PrependUOffsetT(altNameOffsets[i])
			}
			altNamesOffset = builder.EndVector(len(altNameOffsets))
		}
	}

	// X-SIGNING-KEY / X-ENCRYPTION-KEY -> KEYS
	var keysOffset flatbuffers.UOffsetT
	signingKeys := card.Values("X-SIGNING-KEY")
	encryptionKeys := card.Values("X-ENCRYPTION-KEY")

	keyOffsets := make([]flatbuffers.UOffsetT, 0)

	for _, key := range signingKeys {
		if key != "" {
			keyStrOffset := builder.CreateString(key)
			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, keyStrOffset)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
			keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))
		}
	}

	for _, key := range encryptionKeys {
		if key != "" {
			keyStrOffset := builder.CreateString(key)
			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, keyStrOffset)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
			keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))
		}
	}

	if len(keyOffsets) > 0 {
		EPM.EPMStartKEYSVector(builder, len(keyOffsets))
		for i := len(keyOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(keyOffsets[i])
		}
		keysOffset = builder.EndVector(len(keyOffsets))
	}

	// Build EPM
	EPM.EPMStart(builder)
	if dnOffset != 0 {
		EPM.EPMAddDN(builder, dnOffset)
	}
	if legalNameOffset != 0 {
		EPM.EPMAddLEGAL_NAME(builder, legalNameOffset)
	}
	if familyNameOffset != 0 {
		EPM.EPMAddFAMILY_NAME(builder, familyNameOffset)
	}
	if givenNameOffset != 0 {
		EPM.EPMAddGIVEN_NAME(builder, givenNameOffset)
	}
	if additionalNameOffset != 0 {
		EPM.EPMAddADDITIONAL_NAME(builder, additionalNameOffset)
	}
	if prefixOffset != 0 {
		EPM.EPMAddHONORIFIC_PREFIX(builder, prefixOffset)
	}
	if suffixOffset != 0 {
		EPM.EPMAddHONORIFIC_SUFFIX(builder, suffixOffset)
	}
	if titleOffset != 0 {
		EPM.EPMAddJOB_TITLE(builder, titleOffset)
	}
	if roleOffset != 0 {
		EPM.EPMAddOCCUPATION(builder, roleOffset)
	}
	if addressOffset != 0 {
		EPM.EPMAddADDRESS(builder, addressOffset)
	}
	if altNamesOffset != 0 {
		EPM.EPMAddALTERNATE_NAMES(builder, altNamesOffset)
	}
	if emailOffset != 0 {
		EPM.EPMAddEMAIL(builder, emailOffset)
	}
	if telOffset != 0 {
		EPM.EPMAddTELEPHONE(builder, telOffset)
	}
	if keysOffset != 0 {
		EPM.EPMAddKEYS(builder, keysOffset)
	}
	if multiAddrOffset != 0 {
		EPM.EPMAddMULTIFORMAT_ADDRESS(builder, multiAddrOffset)
	}
	epm := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	// Return a copy
	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	return result, nil
}

// EmbeddedEPMFromVCard extracts a complete signed EPM payload from an SDN vCard
// extension. A nil slice means the vCard has no embedded EPM payload.
func EmbeddedEPMFromVCard(vcardStr string) ([]byte, error) {
	if strings.TrimSpace(vcardStr) == "" {
		return nil, ErrEmptyVCard
	}
	dec := vcard.NewDecoder(strings.NewReader(vcardStr))
	card, err := dec.Decode()
	if err != nil {
		return nil, err
	}
	return EmbeddedEPMFromCard(card)
}

// EmbeddedEPMFromCard extracts a complete signed EPM payload from a parsed card.
func EmbeddedEPMFromCard(card vcard.Card) ([]byte, error) {
	field := card.Get(FieldSDNEPMBase64)
	if field == nil || strings.TrimSpace(field.Value) == "" {
		return nil, nil
	}
	payload := strings.Join(strings.Fields(field.Value), "")
	epmBytes, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, ErrInvalidEPM
	}
	return epmBytes, nil
}

// safeString converts a byte slice to string, returning empty string for nil.
func safeString(b []byte) string {
	if b == nil {
		return ""
	}
	return string(b)
}
