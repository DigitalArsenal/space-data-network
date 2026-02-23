package vcard

import (
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

func TestEPMToVCard(t *testing.T) {
	epm := createTestEPM()

	vcardStr, err := EPMToVCard(epm)
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}

	// Verify vCard structure
	if !strings.Contains(vcardStr, "BEGIN:VCARD") {
		t.Error("vCard missing BEGIN")
	}
	if !strings.Contains(vcardStr, "VERSION:4.0") {
		t.Error("vCard missing VERSION:4.0")
	}
	if !strings.Contains(vcardStr, "END:VCARD") {
		t.Error("vCard missing END")
	}

	// Verify fields
	if !strings.Contains(vcardStr, "FN:John Doe") {
		t.Error("vCard missing FN field")
	}
	if !strings.Contains(vcardStr, "ORG:Test Organization") {
		t.Error("vCard missing ORG field")
	}
	if !strings.Contains(vcardStr, "EMAIL:john@example.com") {
		t.Error("vCard missing EMAIL field")
	}
	if !strings.Contains(vcardStr, "TEL:+1-555-1234") {
		t.Error("vCard missing TEL field")
	}
}

func TestEPMToVCardName(t *testing.T) {
	epm := createTestEPM()

	vcardStr, err := EPMToVCard(epm)
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}

	// Check structured name (N field)
	// Format: family;given;additional;prefix;suffix
	if !strings.Contains(vcardStr, "N:Doe;John;Q;Dr.;Jr.") {
		t.Errorf("vCard N field incorrect, got:\n%s", vcardStr)
	}
}

func TestEPMToVCardAddress(t *testing.T) {
	epm := createTestEPM()

	vcardStr, err := EPMToVCard(epm)
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}

	// Check address (ADR field)
	// Format: pobox;ext;street;locality;region;code;country
	if !strings.Contains(vcardStr, "ADR:;;123 Test St;Springfield;IL;62701;USA") {
		t.Errorf("vCard ADR field incorrect, got:\n%s", vcardStr)
	}
}

func TestEPMToVCardKeys(t *testing.T) {
	epm := createTestEPM()

	vcardStr, err := EPMToVCard(epm)
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}

	// Check keys
	if !strings.Contains(vcardStr, "X-SIGNING-KEY:0xsigningkey123") {
		t.Errorf("vCard missing X-SIGNING-KEY, got:\n%s", vcardStr)
	}
	if !strings.Contains(vcardStr, "X-ENCRYPTION-KEY:0xencryptionkey456") {
		t.Errorf("vCard missing X-ENCRYPTION-KEY, got:\n%s", vcardStr)
	}
}

func TestVCardToEPM(t *testing.T) {
	vcardStr := `BEGIN:VCARD
VERSION:4.0
FN:Jane Smith
N:Smith;Jane;M;Ms.;PhD
ORG:Acme Corp
EMAIL:jane@acme.com
TEL:+1-555-5678
TITLE:Director
ROLE:Management
ADR:;;456 Oak Ave;Chicago;IL;60601;USA
END:VCARD`

	epmBytes, err := VCardToEPM(vcardStr)
	if err != nil {
		t.Fatalf("VCardToEPM failed: %v", err)
	}

	// Verify EPM
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	if string(epm.DN()) != "Jane Smith" {
		t.Errorf("DN mismatch: got %s", epm.DN())
	}
	if string(epm.LEGAL_NAME()) != "Acme Corp" {
		t.Errorf("LEGAL_NAME mismatch: got %s", epm.LEGAL_NAME())
	}
	if string(epm.FAMILY_NAME()) != "Smith" {
		t.Errorf("FAMILY_NAME mismatch: got %s", epm.FAMILY_NAME())
	}
	if string(epm.GIVEN_NAME()) != "Jane" {
		t.Errorf("GIVEN_NAME mismatch: got %s", epm.GIVEN_NAME())
	}
	if string(epm.ADDITIONAL_NAME()) != "M" {
		t.Errorf("ADDITIONAL_NAME mismatch: got %s", epm.ADDITIONAL_NAME())
	}
	if string(epm.HONORIFIC_PREFIX()) != "Ms." {
		t.Errorf("HONORIFIC_PREFIX mismatch: got %s", epm.HONORIFIC_PREFIX())
	}
	if string(epm.HONORIFIC_SUFFIX()) != "PhD" {
		t.Errorf("HONORIFIC_SUFFIX mismatch: got %s", epm.HONORIFIC_SUFFIX())
	}
	if string(epm.EMAIL()) != "jane@acme.com" {
		t.Errorf("EMAIL mismatch: got %s", epm.EMAIL())
	}
	if string(epm.TELEPHONE()) != "+1-555-5678" {
		t.Errorf("TELEPHONE mismatch: got %s", epm.TELEPHONE())
	}
	if string(epm.JOB_TITLE()) != "Director" {
		t.Errorf("JOB_TITLE mismatch: got %s", epm.JOB_TITLE())
	}
	if string(epm.OCCUPATION()) != "Management" {
		t.Errorf("OCCUPATION mismatch: got %s", epm.OCCUPATION())
	}

	// Check address
	addr := new(EPM.Address)
	if epm.ADDRESS(addr) == nil {
		t.Fatal("ADDRESS is nil")
	}
	if string(addr.STREET()) != "456 Oak Ave" {
		t.Errorf("STREET mismatch: got %s", addr.STREET())
	}
	if string(addr.LOCALITY()) != "Chicago" {
		t.Errorf("LOCALITY mismatch: got %s", addr.LOCALITY())
	}
	if string(addr.REGION()) != "IL" {
		t.Errorf("REGION mismatch: got %s", addr.REGION())
	}
	if string(addr.POSTAL_CODE()) != "60601" {
		t.Errorf("POSTAL_CODE mismatch: got %s", addr.POSTAL_CODE())
	}
	if string(addr.COUNTRY()) != "USA" {
		t.Errorf("COUNTRY mismatch: got %s", addr.COUNTRY())
	}
}

func TestVCardToEPMWithKeys(t *testing.T) {
	vcardStr := `BEGIN:VCARD
VERSION:4.0
FN:Test User
X-SIGNING-KEY:0xsigkey123
X-ENCRYPTION-KEY:0xenckey456
END:VCARD`

	epmBytes, err := VCardToEPM(vcardStr)
	if err != nil {
		t.Fatalf("VCardToEPM failed: %v", err)
	}

	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	if epm.KEYSLength() != 2 {
		t.Errorf("Expected 2 keys, got %d", epm.KEYSLength())
	}

	key := new(EPM.CryptoKey)
	foundSigning := false
	foundEncryption := false

	for i := 0; i < epm.KEYSLength(); i++ {
		if epm.KEYS(key, i) {
			pubKey := string(key.PUBLIC_KEY())
			if key.KEY_TYPE() == EPM.KeyTypeSigning && pubKey == "0xsigkey123" {
				foundSigning = true
			}
			if key.KEY_TYPE() == EPM.KeyTypeEncryption && pubKey == "0xenckey456" {
				foundEncryption = true
			}
		}
	}

	if !foundSigning {
		t.Error("Signing key not found or incorrect")
	}
	if !foundEncryption {
		t.Error("Encryption key not found or incorrect")
	}
}

func TestEPMVCardRoundtrip(t *testing.T) {
	original := createTestEPM()

	// EPM -> vCard
	vcardStr, err := EPMToVCard(original)
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}

	// vCard -> EPM
	recovered, err := VCardToEPM(vcardStr)
	if err != nil {
		t.Fatalf("VCardToEPM failed: %v", err)
	}

	// Compare key fields
	origEPM := EPM.GetSizePrefixedRootAsEPM(original, 0)
	recvEPM := EPM.GetSizePrefixedRootAsEPM(recovered, 0)

	if string(origEPM.DN()) != string(recvEPM.DN()) {
		t.Errorf("DN mismatch after roundtrip: %s vs %s", origEPM.DN(), recvEPM.DN())
	}
	if string(origEPM.LEGAL_NAME()) != string(recvEPM.LEGAL_NAME()) {
		t.Errorf("LEGAL_NAME mismatch after roundtrip: %s vs %s", origEPM.LEGAL_NAME(), recvEPM.LEGAL_NAME())
	}
	if string(origEPM.FAMILY_NAME()) != string(recvEPM.FAMILY_NAME()) {
		t.Errorf("FAMILY_NAME mismatch after roundtrip: %s vs %s", origEPM.FAMILY_NAME(), recvEPM.FAMILY_NAME())
	}
	if string(origEPM.GIVEN_NAME()) != string(recvEPM.GIVEN_NAME()) {
		t.Errorf("GIVEN_NAME mismatch after roundtrip: %s vs %s", origEPM.GIVEN_NAME(), recvEPM.GIVEN_NAME())
	}
	if string(origEPM.EMAIL()) != string(recvEPM.EMAIL()) {
		t.Errorf("EMAIL mismatch after roundtrip: %s vs %s", origEPM.EMAIL(), recvEPM.EMAIL())
	}
	if string(origEPM.TELEPHONE()) != string(recvEPM.TELEPHONE()) {
		t.Errorf("TELEPHONE mismatch after roundtrip: %s vs %s", origEPM.TELEPHONE(), recvEPM.TELEPHONE())
	}
}

func TestVCardEPMRoundtrip(t *testing.T) {
	original := `BEGIN:VCARD
VERSION:4.0
FN:Roundtrip Test
N:Test;Roundtrip;;;
ORG:Test Org
EMAIL:test@roundtrip.com
TEL:+1-555-9999
TITLE:Tester
ROLE:QA
ADR:;;789 Round St;Circle;CA;90210;USA
X-SIGNING-KEY:0xroundsigkey
X-ENCRYPTION-KEY:0xroundenckey
END:VCARD`

	// vCard -> EPM
	epmBytes, err := VCardToEPM(original)
	if err != nil {
		t.Fatalf("VCardToEPM failed: %v", err)
	}

	// EPM -> vCard
	recovered, err := EPMToVCard(epmBytes)
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}

	// Check key fields are preserved
	if !strings.Contains(recovered, "FN:Roundtrip Test") {
		t.Error("FN not preserved in roundtrip")
	}
	if !strings.Contains(recovered, "ORG:Test Org") {
		t.Error("ORG not preserved in roundtrip")
	}
	if !strings.Contains(recovered, "EMAIL:test@roundtrip.com") {
		t.Error("EMAIL not preserved in roundtrip")
	}
	if !strings.Contains(recovered, "X-SIGNING-KEY:0xroundsigkey") {
		t.Error("X-SIGNING-KEY not preserved in roundtrip")
	}
	if !strings.Contains(recovered, "X-ENCRYPTION-KEY:0xroundenckey") {
		t.Error("X-ENCRYPTION-KEY not preserved in roundtrip")
	}
}

func TestEmptyEPM(t *testing.T) {
	_, err := EPMToVCard(nil)
	if err != ErrEmptyEPM {
		t.Errorf("Expected ErrEmptyEPM, got %v", err)
	}

	_, err = EPMToVCard([]byte{})
	if err != ErrEmptyEPM {
		t.Errorf("Expected ErrEmptyEPM for empty slice, got %v", err)
	}
}

func TestEmptyVCard(t *testing.T) {
	_, err := VCardToEPM("")
	if err != ErrEmptyVCard {
		t.Errorf("Expected ErrEmptyVCard, got %v", err)
	}
}

func TestMinimalVCard(t *testing.T) {
	vcardStr := `BEGIN:VCARD
VERSION:4.0
FN:Minimal
END:VCARD`

	epmBytes, err := VCardToEPM(vcardStr)
	if err != nil {
		t.Fatalf("VCardToEPM failed: %v", err)
	}

	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	if string(epm.DN()) != "Minimal" {
		t.Errorf("DN mismatch: got %s", epm.DN())
	}
}

func createTestEPM() []byte {
	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("John Doe")
	legalNameOffset := builder.CreateString("Test Organization")
	familyNameOffset := builder.CreateString("Doe")
	givenNameOffset := builder.CreateString("John")
	additionalNameOffset := builder.CreateString("Q")
	prefixOffset := builder.CreateString("Dr.")
	suffixOffset := builder.CreateString("Jr.")
	jobTitleOffset := builder.CreateString("Engineer")
	occupationOffset := builder.CreateString("Software")
	emailOffset := builder.CreateString("john@example.com")
	telOffset := builder.CreateString("+1-555-1234")

	// Address
	countryOff := builder.CreateString("USA")
	regionOff := builder.CreateString("IL")
	localityOff := builder.CreateString("Springfield")
	postalOff := builder.CreateString("62701")
	streetOff := builder.CreateString("123 Test St")

	EPM.AddressStart(builder)
	EPM.AddressAddCOUNTRY(builder, countryOff)
	EPM.AddressAddREGION(builder, regionOff)
	EPM.AddressAddLOCALITY(builder, localityOff)
	EPM.AddressAddPOSTAL_CODE(builder, postalOff)
	EPM.AddressAddSTREET(builder, streetOff)
	addrOffset := EPM.AddressEnd(builder)

	// Keys
	sigKeyOff := builder.CreateString("0xsigningkey123")
	encKeyOff := builder.CreateString("0xencryptionkey456")

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, sigKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	sigCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
	encCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(encCryptoKey)
	builder.PrependUOffsetT(sigCryptoKey)
	keysVec := builder.EndVector(2)

	// Multiformat addresses
	addr1Off := builder.CreateString("/ipns/k51abc123")
	EPM.EPMStartMULTIFORMAT_ADDRESSVector(builder, 1)
	builder.PrependUOffsetT(addr1Off)
	multiAddrsVec := builder.EndVector(1)

	// Alternate names
	alt1Off := builder.CreateString("Johnny")
	alt2Off := builder.CreateString("JD")
	EPM.EPMStartALTERNATE_NAMESVector(builder, 2)
	builder.PrependUOffsetT(alt2Off)
	builder.PrependUOffsetT(alt1Off)
	altNamesVec := builder.EndVector(2)

	// Build EPM
	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddLEGAL_NAME(builder, legalNameOffset)
	EPM.EPMAddFAMILY_NAME(builder, familyNameOffset)
	EPM.EPMAddGIVEN_NAME(builder, givenNameOffset)
	EPM.EPMAddADDITIONAL_NAME(builder, additionalNameOffset)
	EPM.EPMAddHONORIFIC_PREFIX(builder, prefixOffset)
	EPM.EPMAddHONORIFIC_SUFFIX(builder, suffixOffset)
	EPM.EPMAddJOB_TITLE(builder, jobTitleOffset)
	EPM.EPMAddOCCUPATION(builder, occupationOffset)
	EPM.EPMAddADDRESS(builder, addrOffset)
	EPM.EPMAddALTERNATE_NAMES(builder, altNamesVec)
	EPM.EPMAddEMAIL(builder, emailOffset)
	EPM.EPMAddTELEPHONE(builder, telOffset)
	EPM.EPMAddKEYS(builder, keysVec)
	EPM.EPMAddMULTIFORMAT_ADDRESS(builder, multiAddrsVec)
	epm := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	return result
}

func BenchmarkEPMToVCard(b *testing.B) {
	epm := createTestEPM()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = EPMToVCard(epm)
	}
}

func BenchmarkVCardToEPM(b *testing.B) {
	vcardStr := `BEGIN:VCARD
VERSION:4.0
FN:John Doe
N:Doe;John;Q;Dr.;Jr.
ORG:Test Organization
EMAIL:john@example.com
TEL:+1-555-1234
TITLE:Engineer
ROLE:Software
ADR:;;123 Test St;Springfield;IL;62701;USA
X-SIGNING-KEY:0xsigningkey123
X-ENCRYPTION-KEY:0xencryptionkey456
END:VCARD`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = VCardToEPM(vcardStr)
	}
}
