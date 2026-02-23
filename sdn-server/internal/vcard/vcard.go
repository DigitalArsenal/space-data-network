// Package vcard provides bidirectional conversion between EPM (Entity Profile Message)
// FlatBuffers and vCard 4.0 format.
package vcard

import (
	"errors"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/emersion/go-vcard"
)

// Errors
var (
	ErrEmptyEPM   = errors.New("EPM data is empty")
	ErrEmptyVCard = errors.New("vCard data is empty")
	ErrInvalidEPM = errors.New("invalid EPM data")
)

// EPMToVCard converts an EPM FlatBuffer to a vCard 4.0 string.
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
	card.Set("VERSION", &vcard.Field{Value: "4.0"})

	// Distinguished Name -> FN (Formatted Name)
	if dn := epm.DN(); dn != nil {
		card.Add("FN", &vcard.Field{Value: string(dn)})
	}

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

	// Multiformat addresses -> URL
	for i := 0; i < epm.MULTIFORMAT_ADDRESSLength(); i++ {
		if addrBytes := epm.MULTIFORMAT_ADDRESS(i); addrBytes != nil {
			addrStr := string(addrBytes)
			if strings.TrimSpace(addrStr) != "" {
				card.Add("URL", &vcard.Field{Value: addrStr})
			}
		}
	}

	// Alternate names -> X-ALTERNATE-NAME (custom extension)
	for i := 0; i < epm.ALTERNATE_NAMESLength(); i++ {
		if name := epm.ALTERNATE_NAMES(i); name != nil {
			card.Add("X-ALTERNATE-NAME", &vcard.Field{Value: string(name)})
		}
	}

	// Cryptographic keys -> X-SIGNING-KEY / X-ENCRYPTION-KEY
	key := new(EPM.CryptoKey)
	for i := 0; i < epm.KEYSLength(); i++ {
		if epm.KEYS(key, i) {
			if pubKey := key.PUBLIC_KEY(); pubKey != nil {
				var fieldName string
				switch key.KEY_TYPE() {
				case EPM.KeyTypeSigning:
					fieldName = "X-SIGNING-KEY"
				case EPM.KeyTypeEncryption:
					fieldName = "X-ENCRYPTION-KEY"
				default:
					fieldName = "X-PUBLIC-KEY"
				}
				card.Add(fieldName, &vcard.Field{Value: string(pubKey)})
			}
		}
	}

	// Encode to string
	var b strings.Builder
	enc := vcard.NewEncoder(&b)
	if err := enc.Encode(card); err != nil {
		return "", err
	}

	return b.String(), nil
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

// safeString converts a byte slice to string, returning empty string for nil.
func safeString(b []byte) string {
	if b == nil {
		return ""
	}
	return string(b)
}
