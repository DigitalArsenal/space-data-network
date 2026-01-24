package sds

import (
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
)

func TestOMMRoundtrip(t *testing.T) {
	// Create OMM with specific values
	builder := NewOMMBuilder().
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithNoradCatID(25544).
		WithEpoch("2024-01-15T12:00:00.000Z").
		WithMeanMotion(15.49).
		WithEccentricity(0.0001215).
		WithInclination(51.6434).
		WithRaOfAscNode(178.1234).
		WithArgOfPericenter(85.5678).
		WithMeanAnomaly(274.9012)

	data := builder.Build()

	// Verify buffer has identifier
	if !OMM.SizePrefixedOMMBufferHasIdentifier(data) {
		t.Fatal("OMM buffer missing identifier")
	}

	// Deserialize
	omm := OMM.GetSizePrefixedRootAsOMM(data, 0)

	// Verify fields
	if string(omm.OBJECT_NAME()) != "ISS (ZARYA)" {
		t.Errorf("OBJECT_NAME mismatch: got %s, want ISS (ZARYA)", omm.OBJECT_NAME())
	}

	if string(omm.OBJECT_ID()) != "1998-067A" {
		t.Errorf("OBJECT_ID mismatch: got %s, want 1998-067A", omm.OBJECT_ID())
	}

	if omm.NORAD_CAT_ID() != 25544 {
		t.Errorf("NORAD_CAT_ID mismatch: got %d, want 25544", omm.NORAD_CAT_ID())
	}

	if string(omm.EPOCH()) != "2024-01-15T12:00:00.000Z" {
		t.Errorf("EPOCH mismatch: got %s", omm.EPOCH())
	}

	if omm.MEAN_MOTION() != 15.49 {
		t.Errorf("MEAN_MOTION mismatch: got %f, want 15.49", omm.MEAN_MOTION())
	}

	if omm.ECCENTRICITY() != 0.0001215 {
		t.Errorf("ECCENTRICITY mismatch: got %f", omm.ECCENTRICITY())
	}

	if omm.INCLINATION() != 51.6434 {
		t.Errorf("INCLINATION mismatch: got %f", omm.INCLINATION())
	}

	if omm.RA_OF_ASC_NODE() != 178.1234 {
		t.Errorf("RA_OF_ASC_NODE mismatch: got %f", omm.RA_OF_ASC_NODE())
	}

	if omm.ARG_OF_PERICENTER() != 85.5678 {
		t.Errorf("ARG_OF_PERICENTER mismatch: got %f", omm.ARG_OF_PERICENTER())
	}

	if omm.MEAN_ANOMALY() != 274.9012 {
		t.Errorf("MEAN_ANOMALY mismatch: got %f", omm.MEAN_ANOMALY())
	}
}

func TestOMMDefaultValues(t *testing.T) {
	// Test with default values
	data := NewOMMBuilder().Build()

	omm := OMM.GetSizePrefixedRootAsOMM(data, 0)

	if string(omm.OBJECT_NAME()) != "TEST-SAT" {
		t.Errorf("Default OBJECT_NAME mismatch: got %s", omm.OBJECT_NAME())
	}

	if string(omm.CENTER_NAME()) != "EARTH" {
		t.Errorf("Default CENTER_NAME mismatch: got %s", omm.CENTER_NAME())
	}
}

func TestEPMRoundtrip(t *testing.T) {
	builder := NewEPMBuilder().
		WithDN("John Doe").
		WithLegalName("Acme Corporation").
		WithFamilyName("Doe").
		WithGivenName("John").
		WithEmail("john.doe@acme.com").
		WithTelephone("+1-555-0100").
		WithAddress("456 Main St", "Springfield", "IL", "62701", "USA").
		WithJobTitle("Chief Engineer").
		WithOccupation("Aerospace Engineering")

	data := builder.Build()

	// Verify buffer has identifier
	if !EPM.SizePrefixedEPMBufferHasIdentifier(data) {
		t.Fatal("EPM buffer missing identifier")
	}

	// Deserialize
	epm := EPM.GetSizePrefixedRootAsEPM(data, 0)

	// Verify fields
	if string(epm.DN()) != "John Doe" {
		t.Errorf("DN mismatch: got %s", epm.DN())
	}

	if string(epm.LEGAL_NAME()) != "Acme Corporation" {
		t.Errorf("LEGAL_NAME mismatch: got %s", epm.LEGAL_NAME())
	}

	if string(epm.FAMILY_NAME()) != "Doe" {
		t.Errorf("FAMILY_NAME mismatch: got %s", epm.FAMILY_NAME())
	}

	if string(epm.GIVEN_NAME()) != "John" {
		t.Errorf("GIVEN_NAME mismatch: got %s", epm.GIVEN_NAME())
	}

	if string(epm.EMAIL()) != "john.doe@acme.com" {
		t.Errorf("EMAIL mismatch: got %s", epm.EMAIL())
	}

	if string(epm.TELEPHONE()) != "+1-555-0100" {
		t.Errorf("TELEPHONE mismatch: got %s", epm.TELEPHONE())
	}

	if string(epm.JOB_TITLE()) != "Chief Engineer" {
		t.Errorf("JOB_TITLE mismatch: got %s", epm.JOB_TITLE())
	}

	if string(epm.OCCUPATION()) != "Aerospace Engineering" {
		t.Errorf("OCCUPATION mismatch: got %s", epm.OCCUPATION())
	}

	// Verify address
	addr := new(EPM.Address)
	if epm.ADDRESS(addr) == nil {
		t.Fatal("ADDRESS is nil")
	}

	if string(addr.STREET()) != "456 Main St" {
		t.Errorf("STREET mismatch: got %s", addr.STREET())
	}

	if string(addr.LOCALITY()) != "Springfield" {
		t.Errorf("LOCALITY mismatch: got %s", addr.LOCALITY())
	}

	if string(addr.REGION()) != "IL" {
		t.Errorf("REGION mismatch: got %s", addr.REGION())
	}

	if string(addr.POSTAL_CODE()) != "62701" {
		t.Errorf("POSTAL_CODE mismatch: got %s", addr.POSTAL_CODE())
	}

	if string(addr.COUNTRY()) != "USA" {
		t.Errorf("COUNTRY mismatch: got %s", addr.COUNTRY())
	}
}

func TestEPMKeys(t *testing.T) {
	builder := NewEPMBuilder().
		WithKeys("0xsigningkey123", "0xencryptionkey456")

	data := builder.Build()
	epm := EPM.GetSizePrefixedRootAsEPM(data, 0)

	// Verify keys
	if epm.KEYSLength() != 2 {
		t.Errorf("Expected 2 keys, got %d", epm.KEYSLength())
	}

	key := new(EPM.CryptoKey)

	// First key should be signing
	if !epm.KEYS(key, 0) {
		t.Fatal("Failed to get first key")
	}
	if key.KEY_TYPE() != EPM.KeyTypeSigning {
		t.Errorf("First key type mismatch: got %d, want Signing", key.KEY_TYPE())
	}
	if string(key.PUBLIC_KEY()) != "0xsigningkey123" {
		t.Errorf("Signing key mismatch: got %s", key.PUBLIC_KEY())
	}

	// Second key should be encryption
	if !epm.KEYS(key, 1) {
		t.Fatal("Failed to get second key")
	}
	if key.KEY_TYPE() != EPM.KeyTypeEncryption {
		t.Errorf("Second key type mismatch: got %d, want Encryption", key.KEY_TYPE())
	}
	if string(key.PUBLIC_KEY()) != "0xencryptionkey456" {
		t.Errorf("Encryption key mismatch: got %s", key.PUBLIC_KEY())
	}
}

func TestEPMAlternateNames(t *testing.T) {
	builder := NewEPMBuilder()
	// Use default alternate names: ["Johnny", "JD"]

	data := builder.Build()
	epm := EPM.GetSizePrefixedRootAsEPM(data, 0)

	if epm.ALTERNATE_NAMESLength() != 2 {
		t.Errorf("Expected 2 alternate names, got %d", epm.ALTERNATE_NAMESLength())
	}

	name0 := string(epm.ALTERNATE_NAMES(0))
	name1 := string(epm.ALTERNATE_NAMES(1))

	if name0 != "Johnny" && name1 != "Johnny" {
		t.Error("Expected 'Johnny' in alternate names")
	}

	if name0 != "JD" && name1 != "JD" {
		t.Error("Expected 'JD' in alternate names")
	}
}

func TestPNMRoundtrip(t *testing.T) {
	builder := NewPNMBuilder().
		WithMultiformatAddress("/ip4/192.168.1.1/tcp/4001/p2p/QmTestPeerID123").
		WithPublishTimestamp("2024-01-15T12:00:00.000Z").
		WithCID("bafybeiabcdef1234567890testcid").
		WithFileName("satellite-data.omm").
		WithFileID("OMM.fbs").
		WithSignature("0xsignature123abc").
		WithSignatureType("ETH")

	data := builder.Build()

	// Verify buffer has identifier
	if !PNM.SizePrefixedPNMBufferHasIdentifier(data) {
		t.Fatal("PNM buffer missing identifier")
	}

	// Deserialize
	pnm := PNM.GetSizePrefixedRootAsPNM(data, 0)

	// Verify fields
	if string(pnm.MULTIFORMAT_ADDRESS()) != "/ip4/192.168.1.1/tcp/4001/p2p/QmTestPeerID123" {
		t.Errorf("MULTIFORMAT_ADDRESS mismatch: got %s", pnm.MULTIFORMAT_ADDRESS())
	}

	if string(pnm.PUBLISH_TIMESTAMP()) != "2024-01-15T12:00:00.000Z" {
		t.Errorf("PUBLISH_TIMESTAMP mismatch: got %s", pnm.PUBLISH_TIMESTAMP())
	}

	if string(pnm.CID()) != "bafybeiabcdef1234567890testcid" {
		t.Errorf("CID mismatch: got %s", pnm.CID())
	}

	if string(pnm.FILE_NAME()) != "satellite-data.omm" {
		t.Errorf("FILE_NAME mismatch: got %s", pnm.FILE_NAME())
	}

	if string(pnm.FILE_ID()) != "OMM.fbs" {
		t.Errorf("FILE_ID mismatch: got %s", pnm.FILE_ID())
	}

	if string(pnm.SIGNATURE()) != "0xsignature123abc" {
		t.Errorf("SIGNATURE mismatch: got %s", pnm.SIGNATURE())
	}

	if string(pnm.SIGNATURE_TYPE()) != "ETH" {
		t.Errorf("SIGNATURE_TYPE mismatch: got %s", pnm.SIGNATURE_TYPE())
	}
}

func TestCATRoundtrip(t *testing.T) {
	builder := NewCATBuilder().
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithNoradCatID(25544).
		WithLaunchDate("1998-11-20").
		WithOrbitalParams(92.9, 51.6, 420.0, 418.0).
		WithManeuverable(true).
		WithMass(419725.0).
		WithSize(109.0)

	data := builder.Build()

	// Verify buffer has identifier
	if !CAT.SizePrefixedCATBufferHasIdentifier(data) {
		t.Fatal("CAT buffer missing identifier")
	}

	// Deserialize
	cat := CAT.GetSizePrefixedRootAsCAT(data, 0)

	// Verify fields
	if string(cat.OBJECT_NAME()) != "ISS (ZARYA)" {
		t.Errorf("OBJECT_NAME mismatch: got %s", cat.OBJECT_NAME())
	}

	if string(cat.OBJECT_ID()) != "1998-067A" {
		t.Errorf("OBJECT_ID mismatch: got %s", cat.OBJECT_ID())
	}

	if cat.NORAD_CAT_ID() != 25544 {
		t.Errorf("NORAD_CAT_ID mismatch: got %d", cat.NORAD_CAT_ID())
	}

	if string(cat.LAUNCH_DATE()) != "1998-11-20" {
		t.Errorf("LAUNCH_DATE mismatch: got %s", cat.LAUNCH_DATE())
	}

	if cat.PERIOD() != 92.9 {
		t.Errorf("PERIOD mismatch: got %f", cat.PERIOD())
	}

	if cat.INCLINATION() != 51.6 {
		t.Errorf("INCLINATION mismatch: got %f", cat.INCLINATION())
	}

	if cat.APOGEE() != 420.0 {
		t.Errorf("APOGEE mismatch: got %f", cat.APOGEE())
	}

	if cat.PERIGEE() != 418.0 {
		t.Errorf("PERIGEE mismatch: got %f", cat.PERIGEE())
	}

	if cat.MANEUVERABLE() != true {
		t.Error("MANEUVERABLE mismatch: expected true")
	}

	if cat.MASS() != 419725.0 {
		t.Errorf("MASS mismatch: got %f", cat.MASS())
	}

	if cat.SIZE() != 109.0 {
		t.Errorf("SIZE mismatch: got %f", cat.SIZE())
	}
}

func TestMultipleBuilds(t *testing.T) {
	// Test that builder can be reused for multiple builds
	builder := NewOMMBuilder()

	// First build
	data1 := builder.WithObjectName("SAT-1").WithNoradCatID(10001).Build()
	omm1 := OMM.GetSizePrefixedRootAsOMM(data1, 0)
	if string(omm1.OBJECT_NAME()) != "SAT-1" {
		t.Errorf("First build OBJECT_NAME mismatch: got %s", omm1.OBJECT_NAME())
	}

	// Second build with different values
	data2 := builder.WithObjectName("SAT-2").WithNoradCatID(10002).Build()
	omm2 := OMM.GetSizePrefixedRootAsOMM(data2, 0)
	if string(omm2.OBJECT_NAME()) != "SAT-2" {
		t.Errorf("Second build OBJECT_NAME mismatch: got %s", omm2.OBJECT_NAME())
	}

	// Verify first build is unchanged
	omm1Check := OMM.GetSizePrefixedRootAsOMM(data1, 0)
	if string(omm1Check.OBJECT_NAME()) != "SAT-1" {
		t.Errorf("First build was modified: got %s", omm1Check.OBJECT_NAME())
	}
}

func TestEmptyStrings(t *testing.T) {
	// Test handling of empty strings
	builder := NewOMMBuilder().
		WithObjectName("").
		WithObjectID("")

	data := builder.Build()
	omm := OMM.GetSizePrefixedRootAsOMM(data, 0)

	if string(omm.OBJECT_NAME()) != "" {
		t.Errorf("Expected empty OBJECT_NAME, got %s", omm.OBJECT_NAME())
	}

	if string(omm.OBJECT_ID()) != "" {
		t.Errorf("Expected empty OBJECT_ID, got %s", omm.OBJECT_ID())
	}
}

func TestLargeValues(t *testing.T) {
	// Test with large numeric values
	builder := NewOMMBuilder().
		WithNoradCatID(4294967295). // Max uint32
		WithMeanMotion(999999.999999).
		WithEccentricity(0.9999999999)

	data := builder.Build()
	omm := OMM.GetSizePrefixedRootAsOMM(data, 0)

	if omm.NORAD_CAT_ID() != 4294967295 {
		t.Errorf("Large NORAD_CAT_ID mismatch: got %d", omm.NORAD_CAT_ID())
	}

	if omm.MEAN_MOTION() != 999999.999999 {
		t.Errorf("Large MEAN_MOTION mismatch: got %f", omm.MEAN_MOTION())
	}
}
