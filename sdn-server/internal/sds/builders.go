// Package sds provides Space Data Standards schema builders for testing.
package sds

import (
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
)

// OMMBuilder creates OMM (Orbit Mean-Elements Message) FlatBuffers for testing.
type OMMBuilder struct {
	builder           *flatbuffers.Builder
	objectName        string
	objectID          string
	noradCatID        uint32
	epoch             string
	meanMotion        float64
	eccentricity      float64
	inclination       float64
	raOfAscNode       float64
	argOfPericenter   float64
	meanAnomaly       float64
	centerName        string
	creationDate      string
	originator        string
	classificationType string
}

// NewOMMBuilder creates a new OMM builder with default values.
func NewOMMBuilder() *OMMBuilder {
	return &OMMBuilder{
		builder:           flatbuffers.NewBuilder(1024),
		objectName:        "TEST-SAT",
		objectID:          "2024-001A",
		noradCatID:        99999,
		epoch:             time.Now().UTC().Format(time.RFC3339),
		meanMotion:        15.5,
		eccentricity:      0.0001,
		inclination:       51.6,
		raOfAscNode:       180.0,
		argOfPericenter:   90.0,
		meanAnomaly:       0.0,
		centerName:        "EARTH",
		creationDate:      time.Now().UTC().Format(time.RFC3339),
		originator:        "SDN-TEST",
		classificationType: "U",
	}
}

// WithObjectName sets the satellite name.
func (b *OMMBuilder) WithObjectName(name string) *OMMBuilder {
	b.objectName = name
	return b
}

// WithObjectID sets the international designator.
func (b *OMMBuilder) WithObjectID(id string) *OMMBuilder {
	b.objectID = id
	return b
}

// WithNoradCatID sets the NORAD catalog ID.
func (b *OMMBuilder) WithNoradCatID(id uint32) *OMMBuilder {
	b.noradCatID = id
	return b
}

// WithEpoch sets the epoch timestamp.
func (b *OMMBuilder) WithEpoch(epoch string) *OMMBuilder {
	b.epoch = epoch
	return b
}

// WithMeanMotion sets the mean motion in rev/day.
func (b *OMMBuilder) WithMeanMotion(n float64) *OMMBuilder {
	b.meanMotion = n
	return b
}

// WithEccentricity sets the orbital eccentricity.
func (b *OMMBuilder) WithEccentricity(e float64) *OMMBuilder {
	b.eccentricity = e
	return b
}

// WithInclination sets the orbital inclination in degrees.
func (b *OMMBuilder) WithInclination(i float64) *OMMBuilder {
	b.inclination = i
	return b
}

// WithRaOfAscNode sets the right ascension of ascending node.
func (b *OMMBuilder) WithRaOfAscNode(ra float64) *OMMBuilder {
	b.raOfAscNode = ra
	return b
}

// WithArgOfPericenter sets the argument of pericenter.
func (b *OMMBuilder) WithArgOfPericenter(arg float64) *OMMBuilder {
	b.argOfPericenter = arg
	return b
}

// WithMeanAnomaly sets the mean anomaly.
func (b *OMMBuilder) WithMeanAnomaly(ma float64) *OMMBuilder {
	b.meanAnomaly = ma
	return b
}

// Build creates the OMM FlatBuffer and returns a copy of the bytes.
func (b *OMMBuilder) Build() []byte {
	b.builder.Reset()

	objectNameOffset := b.builder.CreateString(b.objectName)
	objectIDOffset := b.builder.CreateString(b.objectID)
	epochOffset := b.builder.CreateString(b.epoch)
	centerNameOffset := b.builder.CreateString(b.centerName)
	creationDateOffset := b.builder.CreateString(b.creationDate)
	originatorOffset := b.builder.CreateString(b.originator)
	classificationOffset := b.builder.CreateString(b.classificationType)

	OMM.OMMStart(b.builder)
	OMM.OMMAddOBJECT_NAME(b.builder, objectNameOffset)
	OMM.OMMAddOBJECT_ID(b.builder, objectIDOffset)
	OMM.OMMAddNORAD_CAT_ID(b.builder, b.noradCatID)
	OMM.OMMAddEPOCH(b.builder, epochOffset)
	OMM.OMMAddMEAN_MOTION(b.builder, b.meanMotion)
	OMM.OMMAddECCENTRICITY(b.builder, b.eccentricity)
	OMM.OMMAddINCLINATION(b.builder, b.inclination)
	OMM.OMMAddRA_OF_ASC_NODE(b.builder, b.raOfAscNode)
	OMM.OMMAddARG_OF_PERICENTER(b.builder, b.argOfPericenter)
	OMM.OMMAddMEAN_ANOMALY(b.builder, b.meanAnomaly)
	OMM.OMMAddCENTER_NAME(b.builder, centerNameOffset)
	OMM.OMMAddCREATION_DATE(b.builder, creationDateOffset)
	OMM.OMMAddORIGINATOR(b.builder, originatorOffset)
	OMM.OMMAddCLASSIFICATION_TYPE(b.builder, classificationOffset)
	omm := OMM.OMMEnd(b.builder)

	OMM.FinishSizePrefixedOMMBuffer(b.builder, omm)

	// Return a copy to avoid buffer reuse issues
	result := make([]byte, len(b.builder.FinishedBytes()))
	copy(result, b.builder.FinishedBytes())
	return result
}

// EPMBuilder creates EPM (Entity Profile Message) FlatBuffers for testing.
type EPMBuilder struct {
	builder         *flatbuffers.Builder
	dn              string
	legalName       string
	familyName      string
	givenName       string
	additionalName  string
	honorificPrefix string
	honorificSuffix string
	jobTitle        string
	occupation      string
	email           string
	telephone       string
	country         string
	region          string
	locality        string
	postalCode      string
	street          string
	poBox           string
	alternateNames  []string
	signingKey      string
	encryptionKey   string
	multiAddrs      []string
}

// NewEPMBuilder creates a new EPM builder with default values.
func NewEPMBuilder() *EPMBuilder {
	return &EPMBuilder{
		builder:         flatbuffers.NewBuilder(1024),
		dn:              "Test Entity",
		legalName:       "Test Organization",
		familyName:      "Doe",
		givenName:       "John",
		additionalName:  "Q",
		honorificPrefix: "Dr.",
		honorificSuffix: "Jr.",
		jobTitle:        "Engineer",
		occupation:      "Software Development",
		email:           "test@example.com",
		telephone:       "+1-555-1234",
		country:         "USA",
		region:          "CA",
		locality:        "San Francisco",
		postalCode:      "94102",
		street:          "123 Test Street",
		poBox:           "",
		alternateNames:  []string{"Johnny", "JD"},
		signingKey:      "0x1234567890abcdef",
		encryptionKey:   "0xfedcba0987654321",
		multiAddrs:      []string{"/ipns/k51abc123"},
	}
}

// WithDN sets the distinguished name.
func (b *EPMBuilder) WithDN(dn string) *EPMBuilder {
	b.dn = dn
	return b
}

// WithLegalName sets the legal/organization name.
func (b *EPMBuilder) WithLegalName(name string) *EPMBuilder {
	b.legalName = name
	return b
}

// WithFamilyName sets the family name.
func (b *EPMBuilder) WithFamilyName(name string) *EPMBuilder {
	b.familyName = name
	return b
}

// WithGivenName sets the given name.
func (b *EPMBuilder) WithGivenName(name string) *EPMBuilder {
	b.givenName = name
	return b
}

// WithEmail sets the email address.
func (b *EPMBuilder) WithEmail(email string) *EPMBuilder {
	b.email = email
	return b
}

// WithTelephone sets the telephone number.
func (b *EPMBuilder) WithTelephone(tel string) *EPMBuilder {
	b.telephone = tel
	return b
}

// WithAddress sets the address components.
func (b *EPMBuilder) WithAddress(street, locality, region, postalCode, country string) *EPMBuilder {
	b.street = street
	b.locality = locality
	b.region = region
	b.postalCode = postalCode
	b.country = country
	return b
}

// WithJobTitle sets the job title.
func (b *EPMBuilder) WithJobTitle(title string) *EPMBuilder {
	b.jobTitle = title
	return b
}

// WithOccupation sets the occupation.
func (b *EPMBuilder) WithOccupation(occupation string) *EPMBuilder {
	b.occupation = occupation
	return b
}

// WithKeys sets the signing and encryption keys.
func (b *EPMBuilder) WithKeys(signingKey, encryptionKey string) *EPMBuilder {
	b.signingKey = signingKey
	b.encryptionKey = encryptionKey
	return b
}

// WithMultiAddrs sets the multiformat addresses.
func (b *EPMBuilder) WithMultiAddrs(addrs []string) *EPMBuilder {
	b.multiAddrs = addrs
	return b
}

// Build creates the EPM FlatBuffer and returns the bytes.
func (b *EPMBuilder) Build() []byte {
	b.builder.Reset()

	// Create string offsets
	dnOffset := b.builder.CreateString(b.dn)
	legalNameOffset := b.builder.CreateString(b.legalName)
	familyNameOffset := b.builder.CreateString(b.familyName)
	givenNameOffset := b.builder.CreateString(b.givenName)
	additionalNameOffset := b.builder.CreateString(b.additionalName)
	honorificPrefixOffset := b.builder.CreateString(b.honorificPrefix)
	honorificSuffixOffset := b.builder.CreateString(b.honorificSuffix)
	jobTitleOffset := b.builder.CreateString(b.jobTitle)
	occupationOffset := b.builder.CreateString(b.occupation)
	emailOffset := b.builder.CreateString(b.email)
	telephoneOffset := b.builder.CreateString(b.telephone)

	// Create address
	countryOffset := b.builder.CreateString(b.country)
	regionOffset := b.builder.CreateString(b.region)
	localityOffset := b.builder.CreateString(b.locality)
	postalCodeOffset := b.builder.CreateString(b.postalCode)
	streetOffset := b.builder.CreateString(b.street)
	poBoxOffset := b.builder.CreateString(b.poBox)

	EPM.AddressStart(b.builder)
	EPM.AddressAddCOUNTRY(b.builder, countryOffset)
	EPM.AddressAddREGION(b.builder, regionOffset)
	EPM.AddressAddLOCALITY(b.builder, localityOffset)
	EPM.AddressAddPOSTAL_CODE(b.builder, postalCodeOffset)
	EPM.AddressAddSTREET(b.builder, streetOffset)
	EPM.AddressAddPOST_OFFICE_BOX_NUMBER(b.builder, poBoxOffset)
	addressOffset := EPM.AddressEnd(b.builder)

	// Create keys
	signingKeyOffset := b.builder.CreateString(b.signingKey)
	encryptionKeyOffset := b.builder.CreateString(b.encryptionKey)

	EPM.CryptoKeyStart(b.builder)
	EPM.CryptoKeyAddPUBLIC_KEY(b.builder, signingKeyOffset)
	EPM.CryptoKeyAddKEY_TYPE(b.builder, EPM.KeyTypeSigning)
	signingCryptoKey := EPM.CryptoKeyEnd(b.builder)

	EPM.CryptoKeyStart(b.builder)
	EPM.CryptoKeyAddPUBLIC_KEY(b.builder, encryptionKeyOffset)
	EPM.CryptoKeyAddKEY_TYPE(b.builder, EPM.KeyTypeEncryption)
	encryptionCryptoKey := EPM.CryptoKeyEnd(b.builder)

	EPM.EPMStartKEYSVector(b.builder, 2)
	b.builder.PrependUOffsetT(encryptionCryptoKey)
	b.builder.PrependUOffsetT(signingCryptoKey)
	keysVectorOffset := b.builder.EndVector(2)

	// Create alternate names vector
	altNameOffsets := make([]flatbuffers.UOffsetT, len(b.alternateNames))
	for i, name := range b.alternateNames {
		altNameOffsets[i] = b.builder.CreateString(name)
	}
	EPM.EPMStartALTERNATE_NAMESVector(b.builder, len(altNameOffsets))
	for i := len(altNameOffsets) - 1; i >= 0; i-- {
		b.builder.PrependUOffsetT(altNameOffsets[i])
	}
	altNamesVector := b.builder.EndVector(len(altNameOffsets))

	// Create multiformat addresses vector
	multiAddrOffsets := make([]flatbuffers.UOffsetT, len(b.multiAddrs))
	for i, addr := range b.multiAddrs {
		multiAddrOffsets[i] = b.builder.CreateString(addr)
	}
	EPM.EPMStartMULTIFORMAT_ADDRESSVector(b.builder, len(multiAddrOffsets))
	for i := len(multiAddrOffsets) - 1; i >= 0; i-- {
		b.builder.PrependUOffsetT(multiAddrOffsets[i])
	}
	multiAddrsVector := b.builder.EndVector(len(multiAddrOffsets))

	// Build EPM
	EPM.EPMStart(b.builder)
	EPM.EPMAddDN(b.builder, dnOffset)
	EPM.EPMAddLEGAL_NAME(b.builder, legalNameOffset)
	EPM.EPMAddFAMILY_NAME(b.builder, familyNameOffset)
	EPM.EPMAddGIVEN_NAME(b.builder, givenNameOffset)
	EPM.EPMAddADDITIONAL_NAME(b.builder, additionalNameOffset)
	EPM.EPMAddHONORIFIC_PREFIX(b.builder, honorificPrefixOffset)
	EPM.EPMAddHONORIFIC_SUFFIX(b.builder, honorificSuffixOffset)
	EPM.EPMAddJOB_TITLE(b.builder, jobTitleOffset)
	EPM.EPMAddOCCUPATION(b.builder, occupationOffset)
	EPM.EPMAddADDRESS(b.builder, addressOffset)
	EPM.EPMAddALTERNATE_NAMES(b.builder, altNamesVector)
	EPM.EPMAddEMAIL(b.builder, emailOffset)
	EPM.EPMAddTELEPHONE(b.builder, telephoneOffset)
	EPM.EPMAddKEYS(b.builder, keysVectorOffset)
	EPM.EPMAddMULTIFORMAT_ADDRESS(b.builder, multiAddrsVector)
	epm := EPM.EPMEnd(b.builder)

	EPM.FinishSizePrefixedEPMBuffer(b.builder, epm)

	// Return a copy to avoid buffer reuse issues
	result := make([]byte, len(b.builder.FinishedBytes()))
	copy(result, b.builder.FinishedBytes())
	return result
}

// PNMBuilder creates PNM (Publish Notification Message) FlatBuffers for testing.
type PNMBuilder struct {
	builder             *flatbuffers.Builder
	multiformatAddress  string
	publishTimestamp    string
	cid                 string
	fileName            string
	fileID              string
	signature           string
	timestampSignature  string
	signatureType       string
	timestampSigType    string
}

// NewPNMBuilder creates a new PNM builder with default values.
func NewPNMBuilder() *PNMBuilder {
	return &PNMBuilder{
		builder:            flatbuffers.NewBuilder(512),
		multiformatAddress: "/ip4/127.0.0.1/tcp/4001/p2p/QmTest123",
		publishTimestamp:   time.Now().UTC().Format(time.RFC3339),
		cid:                "bafybeiabcdef1234567890",
		fileName:           "test-data.omm",
		fileID:             "OMM.fbs",
		signature:          "0xabcdef1234567890signature",
		timestampSignature: "0x1234567890timestampsig",
		signatureType:      "ETH",
		timestampSigType:   "ETH",
	}
}

// WithMultiformatAddress sets the multiformat address.
func (b *PNMBuilder) WithMultiformatAddress(addr string) *PNMBuilder {
	b.multiformatAddress = addr
	return b
}

// WithPublishTimestamp sets the publish timestamp.
func (b *PNMBuilder) WithPublishTimestamp(ts string) *PNMBuilder {
	b.publishTimestamp = ts
	return b
}

// WithCID sets the content identifier.
func (b *PNMBuilder) WithCID(cid string) *PNMBuilder {
	b.cid = cid
	return b
}

// WithFileName sets the file name.
func (b *PNMBuilder) WithFileName(name string) *PNMBuilder {
	b.fileName = name
	return b
}

// WithFileID sets the file ID (schema type).
func (b *PNMBuilder) WithFileID(id string) *PNMBuilder {
	b.fileID = id
	return b
}

// WithSignature sets the CID signature.
func (b *PNMBuilder) WithSignature(sig string) *PNMBuilder {
	b.signature = sig
	return b
}

// WithSignatureType sets the signature type.
func (b *PNMBuilder) WithSignatureType(sigType string) *PNMBuilder {
	b.signatureType = sigType
	return b
}

// Build creates the PNM FlatBuffer and returns the bytes.
func (b *PNMBuilder) Build() []byte {
	b.builder.Reset()

	addrOffset := b.builder.CreateString(b.multiformatAddress)
	timestampOffset := b.builder.CreateString(b.publishTimestamp)
	cidOffset := b.builder.CreateString(b.cid)
	fileNameOffset := b.builder.CreateString(b.fileName)
	fileIDOffset := b.builder.CreateString(b.fileID)
	signatureOffset := b.builder.CreateString(b.signature)
	timestampSigOffset := b.builder.CreateString(b.timestampSignature)
	signatureTypeOffset := b.builder.CreateString(b.signatureType)
	timestampSigTypeOffset := b.builder.CreateString(b.timestampSigType)

	PNM.PNMStart(b.builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(b.builder, addrOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(b.builder, timestampOffset)
	PNM.PNMAddCID(b.builder, cidOffset)
	PNM.PNMAddFILE_NAME(b.builder, fileNameOffset)
	PNM.PNMAddFILE_ID(b.builder, fileIDOffset)
	PNM.PNMAddSIGNATURE(b.builder, signatureOffset)
	PNM.PNMAddTIMESTAMP_SIGNATURE(b.builder, timestampSigOffset)
	PNM.PNMAddSIGNATURE_TYPE(b.builder, signatureTypeOffset)
	PNM.PNMAddTIMESTAMP_SIGNATURE_TYPE(b.builder, timestampSigTypeOffset)
	pnm := PNM.PNMEnd(b.builder)

	PNM.FinishSizePrefixedPNMBuffer(b.builder, pnm)

	// Return a copy to avoid buffer reuse issues
	result := make([]byte, len(b.builder.FinishedBytes()))
	copy(result, b.builder.FinishedBytes())
	return result
}

// CATBuilder creates CAT (Catalog) FlatBuffers for testing.
type CATBuilder struct {
	builder      *flatbuffers.Builder
	objectName   string
	objectID     string
	noradCatID   uint32
	launchDate   string
	launchSite   string
	decayDate    string
	period       float64
	inclination  float64
	apogee       float64
	perigee      float64
	rcs          float64
	orbitCenter  string
	maneuverable bool
	size         float64
	mass         float64
}

// NewCATBuilder creates a new CAT builder with default values.
func NewCATBuilder() *CATBuilder {
	return &CATBuilder{
		builder:      flatbuffers.NewBuilder(1024),
		objectName:   "ISS (ZARYA)",
		objectID:     "1998-067A",
		noradCatID:   25544,
		launchDate:   "1998-11-20",
		launchSite:   "TYMSC",
		decayDate:    "",
		period:       92.9,
		inclination:  51.6,
		apogee:       420.0,
		perigee:      418.0,
		rcs:          0.0,
		orbitCenter:  "EARTH",
		maneuverable: true,
		size:         109.0,
		mass:         419725.0,
	}
}

// WithObjectName sets the object name.
func (b *CATBuilder) WithObjectName(name string) *CATBuilder {
	b.objectName = name
	return b
}

// WithObjectID sets the international designator.
func (b *CATBuilder) WithObjectID(id string) *CATBuilder {
	b.objectID = id
	return b
}

// WithNoradCatID sets the NORAD catalog ID.
func (b *CATBuilder) WithNoradCatID(id uint32) *CATBuilder {
	b.noradCatID = id
	return b
}

// WithLaunchDate sets the launch date.
func (b *CATBuilder) WithLaunchDate(date string) *CATBuilder {
	b.launchDate = date
	return b
}

// WithOrbitalParams sets orbital parameters.
func (b *CATBuilder) WithOrbitalParams(period, inclination, apogee, perigee float64) *CATBuilder {
	b.period = period
	b.inclination = inclination
	b.apogee = apogee
	b.perigee = perigee
	return b
}

// WithManeuverable sets the maneuverable flag.
func (b *CATBuilder) WithManeuverable(m bool) *CATBuilder {
	b.maneuverable = m
	return b
}

// WithMass sets the mass in kg.
func (b *CATBuilder) WithMass(mass float64) *CATBuilder {
	b.mass = mass
	return b
}

// WithSize sets the size in meters.
func (b *CATBuilder) WithSize(size float64) *CATBuilder {
	b.size = size
	return b
}

// Build creates the CAT FlatBuffer and returns the bytes.
func (b *CATBuilder) Build() []byte {
	b.builder.Reset()

	objectNameOffset := b.builder.CreateString(b.objectName)
	objectIDOffset := b.builder.CreateString(b.objectID)
	launchDateOffset := b.builder.CreateString(b.launchDate)
	launchSiteOffset := b.builder.CreateString(b.launchSite)
	decayDateOffset := b.builder.CreateString(b.decayDate)
	orbitCenterOffset := b.builder.CreateString(b.orbitCenter)

	CAT.CATStart(b.builder)
	CAT.CATAddOBJECT_NAME(b.builder, objectNameOffset)
	CAT.CATAddOBJECT_ID(b.builder, objectIDOffset)
	CAT.CATAddNORAD_CAT_ID(b.builder, b.noradCatID)
	CAT.CATAddLAUNCH_DATE(b.builder, launchDateOffset)
	CAT.CATAddLAUNCH_SITE(b.builder, launchSiteOffset)
	CAT.CATAddDECAY_DATE(b.builder, decayDateOffset)
	CAT.CATAddPERIOD(b.builder, b.period)
	CAT.CATAddINCLINATION(b.builder, b.inclination)
	CAT.CATAddAPOGEE(b.builder, b.apogee)
	CAT.CATAddPERIGEE(b.builder, b.perigee)
	CAT.CATAddRCS(b.builder, b.rcs)
	CAT.CATAddORBIT_CENTER(b.builder, orbitCenterOffset)
	CAT.CATAddMANEUVERABLE(b.builder, b.maneuverable)
	CAT.CATAddSIZE(b.builder, b.size)
	CAT.CATAddMASS(b.builder, b.mass)
	cat := CAT.CATEnd(b.builder)

	CAT.FinishSizePrefixedCATBuffer(b.builder, cat)

	// Return a copy to avoid buffer reuse issues
	result := make([]byte, len(b.builder.FinishedBytes()))
	copy(result, b.builder.FinishedBytes())
	return result
}
