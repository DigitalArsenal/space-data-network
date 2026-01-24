package vcard

import (
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestVCardToQR(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:John Doe\r\nEND:VCARD\r\n"

	pngData, err := VCardToQR(vcard, 256)
	if err != nil {
		t.Fatalf("VCardToQR failed: %v", err)
	}

	// PNG magic bytes
	if len(pngData) < 8 {
		t.Fatal("PNG data too short")
	}
	if pngData[0] != 0x89 || pngData[1] != 0x50 || pngData[2] != 0x4E || pngData[3] != 0x47 {
		t.Error("Invalid PNG header")
	}
}

func TestVCardToQRDefaultSize(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Test\r\nEND:VCARD\r\n"

	// Size 0 should use default
	pngData, err := VCardToQR(vcard, 0)
	if err != nil {
		t.Fatalf("VCardToQR with default size failed: %v", err)
	}
	if len(pngData) == 0 {
		t.Error("Empty PNG data")
	}
}

func TestVCardToQRInvalidSize(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Test\r\nEND:VCARD\r\n"

	// Size too large
	_, err := VCardToQR(vcard, 10000)
	if err != ErrInvalidSize {
		t.Errorf("Expected ErrInvalidSize, got %v", err)
	}
}

func TestVCardToQREmpty(t *testing.T) {
	_, err := VCardToQR("", 256)
	if err != ErrEmptyVCard {
		t.Errorf("Expected ErrEmptyVCard, got %v", err)
	}
}

func TestQRToVCard(t *testing.T) {
	original := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:John Doe\r\nEND:VCARD\r\n"

	// Generate QR
	pngData, err := VCardToQR(original, 256)
	if err != nil {
		t.Fatalf("VCardToQR failed: %v", err)
	}

	// Scan QR
	decoded, err := QRToVCard(pngData)
	if err != nil {
		t.Fatalf("QRToVCard failed: %v", err)
	}

	if decoded != original {
		t.Errorf("Roundtrip mismatch:\ngot:  %q\nwant: %q", decoded, original)
	}
}

func TestQRToVCardEmpty(t *testing.T) {
	_, err := QRToVCard(nil)
	if err == nil {
		t.Error("Expected error for nil data")
	}

	_, err = QRToVCard([]byte{})
	if err == nil {
		t.Error("Expected error for empty data")
	}
}

func TestQRToVCardInvalid(t *testing.T) {
	_, err := QRToVCard([]byte("not a png"))
	if err == nil {
		t.Error("Expected error for invalid PNG")
	}
}

func TestVCardQRRoundtrip(t *testing.T) {
	testCases := []struct {
		name  string
		vcard string
	}{
		{
			name:  "minimal",
			vcard: "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Test\r\nEND:VCARD\r\n",
		},
		{
			name:  "with_email",
			vcard: "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Jane Doe\r\nEMAIL:jane@example.com\r\nEND:VCARD\r\n",
		},
		{
			name:  "full_contact",
			vcard: "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob Smith\r\nN:Smith;Bob;;;\r\nORG:Acme Corp\r\nTITLE:Engineer\r\nEMAIL:bob@acme.com\r\nTEL:+1-555-1234\r\nEND:VCARD\r\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pngData, err := VCardToQR(tc.vcard, 256)
			if err != nil {
				t.Fatalf("VCardToQR failed: %v", err)
			}

			decoded, err := QRToVCard(pngData)
			if err != nil {
				t.Fatalf("QRToVCard failed: %v", err)
			}

			if decoded != tc.vcard {
				t.Errorf("Roundtrip mismatch:\ngot:  %q\nwant: %q", decoded, tc.vcard)
			}
		})
	}
}

func TestEPMToQR(t *testing.T) {
	// Create EPM
	builder := flatbuffers.NewBuilder(256)
	dnOffset := builder.CreateString("Test User")

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	epm := EPM.EPMEnd(builder)
	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	epmBytes := make([]byte, len(builder.FinishedBytes()))
	copy(epmBytes, builder.FinishedBytes())

	// Convert to QR
	pngData, err := EPMToQR(epmBytes, 256)
	if err != nil {
		t.Fatalf("EPMToQR failed: %v", err)
	}

	// Verify it's valid PNG
	if len(pngData) < 8 {
		t.Fatal("PNG data too short")
	}
	if pngData[0] != 0x89 || pngData[1] != 0x50 {
		t.Error("Invalid PNG header")
	}
}

func TestQRToEPM(t *testing.T) {
	// Create EPM with known data
	builder := flatbuffers.NewBuilder(256)
	dnOffset := builder.CreateString("QR Test User")
	emailOffset := builder.CreateString("qr@test.com")

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddEMAIL(builder, emailOffset)
	epm := EPM.EPMEnd(builder)
	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	originalBytes := make([]byte, len(builder.FinishedBytes()))
	copy(originalBytes, builder.FinishedBytes())

	// Convert to QR
	pngData, err := EPMToQR(originalBytes, 256)
	if err != nil {
		t.Fatalf("EPMToQR failed: %v", err)
	}

	// Convert back to EPM
	resultBytes, err := QRToEPM(pngData)
	if err != nil {
		t.Fatalf("QRToEPM failed: %v", err)
	}

	// Verify result
	result := EPM.GetSizePrefixedRootAsEPM(resultBytes, 0)
	if string(result.DN()) != "QR Test User" {
		t.Errorf("DN mismatch: got %s, want QR Test User", result.DN())
	}
	if string(result.EMAIL()) != "qr@test.com" {
		t.Errorf("EMAIL mismatch: got %s, want qr@test.com", result.EMAIL())
	}
}

func TestEPMQRFullRoundtrip(t *testing.T) {
	// Create comprehensive EPM
	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Alice Johnson")
	legalNameOffset := builder.CreateString("Johnson Industries")
	familyNameOffset := builder.CreateString("Johnson")
	givenNameOffset := builder.CreateString("Alice")
	emailOffset := builder.CreateString("alice@johnson.com")
	telOffset := builder.CreateString("+1-555-9876")
	titleOffset := builder.CreateString("CEO")
	occupationOffset := builder.CreateString("Executive")

	// Address
	streetOffset := builder.CreateString("123 Tech Blvd")
	localityOffset := builder.CreateString("San Francisco")
	regionOffset := builder.CreateString("CA")
	postalOffset := builder.CreateString("94105")
	countryOffset := builder.CreateString("USA")

	EPM.AddressStart(builder)
	EPM.AddressAddSTREET(builder, streetOffset)
	EPM.AddressAddLOCALITY(builder, localityOffset)
	EPM.AddressAddREGION(builder, regionOffset)
	EPM.AddressAddPOSTAL_CODE(builder, postalOffset)
	EPM.AddressAddCOUNTRY(builder, countryOffset)
	addrOffset := EPM.AddressEnd(builder)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddLEGAL_NAME(builder, legalNameOffset)
	EPM.EPMAddFAMILY_NAME(builder, familyNameOffset)
	EPM.EPMAddGIVEN_NAME(builder, givenNameOffset)
	EPM.EPMAddEMAIL(builder, emailOffset)
	EPM.EPMAddTELEPHONE(builder, telOffset)
	EPM.EPMAddJOB_TITLE(builder, titleOffset)
	EPM.EPMAddOCCUPATION(builder, occupationOffset)
	EPM.EPMAddADDRESS(builder, addrOffset)
	epm := EPM.EPMEnd(builder)
	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	originalBytes := make([]byte, len(builder.FinishedBytes()))
	copy(originalBytes, builder.FinishedBytes())

	// Full roundtrip: EPM -> vCard -> QR -> vCard -> EPM
	pngData, err := EPMToQR(originalBytes, 512)
	if err != nil {
		t.Fatalf("EPMToQR failed: %v", err)
	}

	resultBytes, err := QRToEPM(pngData)
	if err != nil {
		t.Fatalf("QRToEPM failed: %v", err)
	}

	result := EPM.GetSizePrefixedRootAsEPM(resultBytes, 0)

	// Verify all fields
	if string(result.DN()) != "Alice Johnson" {
		t.Errorf("DN mismatch: got %s", result.DN())
	}
	if string(result.LEGAL_NAME()) != "Johnson Industries" {
		t.Errorf("LEGAL_NAME mismatch: got %s", result.LEGAL_NAME())
	}
	if string(result.FAMILY_NAME()) != "Johnson" {
		t.Errorf("FAMILY_NAME mismatch: got %s", result.FAMILY_NAME())
	}
	if string(result.GIVEN_NAME()) != "Alice" {
		t.Errorf("GIVEN_NAME mismatch: got %s", result.GIVEN_NAME())
	}
	if string(result.EMAIL()) != "alice@johnson.com" {
		t.Errorf("EMAIL mismatch: got %s", result.EMAIL())
	}
	if string(result.TELEPHONE()) != "+1-555-9876" {
		t.Errorf("TELEPHONE mismatch: got %s", result.TELEPHONE())
	}
	if string(result.JOB_TITLE()) != "CEO" {
		t.Errorf("JOB_TITLE mismatch: got %s", result.JOB_TITLE())
	}
	if string(result.OCCUPATION()) != "Executive" {
		t.Errorf("OCCUPATION mismatch: got %s", result.OCCUPATION())
	}

	// Verify address
	addr := new(EPM.Address)
	if result.ADDRESS(addr) == nil {
		t.Fatal("ADDRESS is nil")
	}
	if string(addr.STREET()) != "123 Tech Blvd" {
		t.Errorf("STREET mismatch: got %s", addr.STREET())
	}
	if string(addr.LOCALITY()) != "San Francisco" {
		t.Errorf("LOCALITY mismatch: got %s", addr.LOCALITY())
	}
	if string(addr.REGION()) != "CA" {
		t.Errorf("REGION mismatch: got %s", addr.REGION())
	}
	if string(addr.POSTAL_CODE()) != "94105" {
		t.Errorf("POSTAL_CODE mismatch: got %s", addr.POSTAL_CODE())
	}
	if string(addr.COUNTRY()) != "USA" {
		t.Errorf("COUNTRY mismatch: got %s", addr.COUNTRY())
	}
}

func TestVCardToQRImage(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Image Test\r\nEND:VCARD\r\n"

	img, err := VCardToQRImage(vcard, 256)
	if err != nil {
		t.Fatalf("VCardToQRImage failed: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Errorf("Image size mismatch: got %dx%d, want 256x256", bounds.Dx(), bounds.Dy())
	}
}

func TestQRImageToVCard(t *testing.T) {
	original := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Image Roundtrip\r\nEND:VCARD\r\n"

	// Generate image
	img, err := VCardToQRImage(original, 256)
	if err != nil {
		t.Fatalf("VCardToQRImage failed: %v", err)
	}

	// Scan image
	decoded, err := QRImageToVCard(img)
	if err != nil {
		t.Fatalf("QRImageToVCard failed: %v", err)
	}

	if decoded != original {
		t.Errorf("Roundtrip mismatch:\ngot:  %q\nwant: %q", decoded, original)
	}
}

func TestQRImageToVCardNil(t *testing.T) {
	_, err := QRImageToVCard(nil)
	if err == nil {
		t.Error("Expected error for nil image")
	}
}

func TestQRDifferentSizes(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Size Test\r\nEND:VCARD\r\n"

	sizes := []int{128, 256, 512}
	for _, size := range sizes {
		t.Run(strings.ReplaceAll(string(rune(size)), "", ""), func(t *testing.T) {
			pngData, err := VCardToQR(vcard, size)
			if err != nil {
				t.Fatalf("VCardToQR at size %d failed: %v", size, err)
			}

			decoded, err := QRToVCard(pngData)
			if err != nil {
				t.Fatalf("QRToVCard at size %d failed: %v", size, err)
			}

			if decoded != vcard {
				t.Errorf("Roundtrip at size %d failed", size)
			}
		})
	}
}

func TestQRWithLongData(t *testing.T) {
	// Test with a vCard that has more content
	vcard := `BEGIN:VCARD
VERSION:4.0
FN:Dr. Elizabeth Maria Constantine-Wellington III
N:Constantine-Wellington;Elizabeth;Maria;Dr.;III
ORG:International Space Agency Consortium Ltd.
TITLE:Chief Space Systems Architect
ROLE:Aerospace Engineering Leadership
EMAIL:elizabeth.cwellington@isac.space
TEL:+1-555-987-6543
ADR:;;1234 Orbital Research Boulevard;Houston;Texas;77058;United States
URL:/ipns/k51qzi5uqu5dlvj2baxnqndepeb86cbk3lg26r2hi
END:VCARD
`
	// Normalize line endings
	vcard = strings.ReplaceAll(vcard, "\n", "\r\n")

	pngData, err := VCardToQR(vcard, 512)
	if err != nil {
		t.Fatalf("VCardToQR with long data failed: %v", err)
	}

	decoded, err := QRToVCard(pngData)
	if err != nil {
		t.Fatalf("QRToVCard with long data failed: %v", err)
	}

	if decoded != vcard {
		t.Errorf("Long data roundtrip failed:\ngot:  %q\nwant: %q", decoded, vcard)
	}
}

func BenchmarkVCardToQR(b *testing.B) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Benchmark User\r\nEMAIL:bench@test.com\r\nEND:VCARD\r\n"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = VCardToQR(vcard, 256)
	}
}

func BenchmarkQRToVCard(b *testing.B) {
	vcard := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Benchmark User\r\nEMAIL:bench@test.com\r\nEND:VCARD\r\n"
	pngData, _ := VCardToQR(vcard, 256)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = QRToVCard(pngData)
	}
}

func BenchmarkEPMQRRoundtrip(b *testing.B) {
	builder := flatbuffers.NewBuilder(256)
	dnOffset := builder.CreateString("Benchmark User")
	emailOffset := builder.CreateString("bench@test.com")

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddEMAIL(builder, emailOffset)
	epm := EPM.EPMEnd(builder)
	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	epmBytes := make([]byte, len(builder.FinishedBytes()))
	copy(epmBytes, builder.FinishedBytes())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pngData, _ := EPMToQR(epmBytes, 256)
		_, _ = QRToEPM(pngData)
	}
}
