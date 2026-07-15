// Package sds provides Space Data Standards schema builders for testing.
//
// This is the minimal subset ported onto kubo for the FlatSQL storage rebase:
// only the OMM (Orbit Mean-Elements Message) builder, which is all the
// flatsqlrt partition test and the sdnstore durability test need. The
// sdn-server package also carries EPM/PNM/CAT builders and a wasm-backed
// validator; those pull in the full spacedatastandards.org lib and the
// sdn-server node/wasm packages and are intentionally NOT ported (the kubo
// storage module is SDS-type-neutral and does not depend on them).
package sds

import (
	"strings"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
)

// OMMBuilder creates OMM (Orbit Mean-Elements Message) FlatBuffers for testing.
type OMMBuilder struct {
	builder            *flatbuffers.Builder
	objectName         string
	objectID           string
	noradCatID         uint32
	epoch              string
	meanMotion         float64
	eccentricity       float64
	inclination        float64
	raOfAscNode        float64
	argOfPericenter    float64
	meanAnomaly        float64
	centerName         string
	creationDate       string
	originator         string
	classificationType string
	epochTimestamp     float64
	bstar              float64
	meanMotionDot      float64
	meanMotionDdot     float64
	elementSetNo       uint32
	revAtEpoch         float64
	ephemerisType      string
}

// NewOMMBuilder creates a new OMM builder with default values.
func NewOMMBuilder() *OMMBuilder {
	return &OMMBuilder{
		builder:            flatbuffers.NewBuilder(1024),
		objectName:         "TEST-SAT",
		objectID:           "2024-001A",
		noradCatID:         99999,
		epoch:              time.Now().UTC().Format(time.RFC3339),
		meanMotion:         15.5,
		eccentricity:       0.0001,
		inclination:        51.6,
		raOfAscNode:        180.0,
		argOfPericenter:    90.0,
		meanAnomaly:        0.0,
		centerName:         "EARTH",
		creationDate:       time.Now().UTC().Format(time.RFC3339),
		originator:         "SDN-TEST",
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

// WithEpochTimestamp sets the numeric USER_DEFINED_EPOCH_TIMESTAMP field
// (Unix seconds). Numeric epoch comparisons in FlatSQL are orders of
// magnitude cheaper than strftime over the EPOCH string, so ingest should
// always populate this alongside EPOCH.
func (b *OMMBuilder) WithEpochTimestamp(unixSeconds float64) *OMMBuilder {
	b.epochTimestamp = unixSeconds
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

// WithCreationDate sets the OMM creation date string.
func (b *OMMBuilder) WithCreationDate(creationDate string) *OMMBuilder {
	b.creationDate = creationDate
	return b
}

// WithClassificationType sets the CCSDS CLASSIFICATION_TYPE field for the
// record.  The empty string is treated as "U" (unclassified) to match the
// builder default.  Non-empty values are stored verbatim; normalisation and
// vocabulary enforcement are the caller's responsibility.
func (b *OMMBuilder) WithClassificationType(ct string) *OMMBuilder {
	if ct == "" {
		ct = "U"
	}
	b.classificationType = ct
	return b
}

// WithOriginator sets the CCSDS ORIGINATOR (creating agency). The empty
// string is a no-op, keeping the builder default ("SDN-TEST" — a TEST
// default; production ingest paths must always set an honest originator).
func (b *OMMBuilder) WithOriginator(name string) *OMMBuilder {
	if name = strings.TrimSpace(name); name != "" {
		b.originator = name
	}
	return b
}

// WithBStar sets the SGP4 drag term BSTAR (1/EarthRadii). Zero means unset
// (FlatBuffers omits default-valued slots), matching the source convention.
func (b *OMMBuilder) WithBStar(bstar float64) *OMMBuilder {
	b.bstar = bstar
	return b
}

// WithMeanMotionDot sets the first derivative of mean motion (rev/day^2).
func (b *OMMBuilder) WithMeanMotionDot(v float64) *OMMBuilder {
	b.meanMotionDot = v
	return b
}

// WithMeanMotionDdot sets the second derivative of mean motion (rev/day^3).
func (b *OMMBuilder) WithMeanMotionDdot(v float64) *OMMBuilder {
	b.meanMotionDdot = v
	return b
}

// WithElementSetNo sets the ELSET number of the element set.
func (b *OMMBuilder) WithElementSetNo(n uint32) *OMMBuilder {
	b.elementSetNo = n
	return b
}

// WithRevAtEpoch sets the revolution number at epoch.
func (b *OMMBuilder) WithRevAtEpoch(rev float64) *OMMBuilder {
	b.revAtEpoch = rev
	return b
}

// WithEphemerisType sets the CCSDS EPHEMERIS_TYPE by enum NAME ("SGP",
// "SGP4", "SDP4", "SGP8", "SDP8" — the generated enum type is unexported, so
// the name is resolved through OMM.EnumValuesephemerisFormat at Build time).
// Unknown names are ignored, leaving the schema default (SGP = 0).
func (b *OMMBuilder) WithEphemerisType(name string) *OMMBuilder {
	b.ephemerisType = strings.ToUpper(strings.TrimSpace(name))
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
	// SGP4 propagation terms + element-set identity (zero values are the
	// FlatBuffers slot defaults and are omitted from the encoded buffer).
	OMM.OMMAddBSTAR(b.builder, b.bstar)
	OMM.OMMAddMEAN_MOTION_DOT(b.builder, b.meanMotionDot)
	OMM.OMMAddMEAN_MOTION_DDOT(b.builder, b.meanMotionDdot)
	OMM.OMMAddELEMENT_SET_NO(b.builder, b.elementSetNo)
	OMM.OMMAddREV_AT_EPOCH(b.builder, b.revAtEpoch)
	if b.ephemerisType != "" {
		if v, ok := OMM.EnumValuesephemerisFormat[b.ephemerisType]; ok {
			OMM.OMMAddEPHEMERIS_TYPE(b.builder, v)
		}
	}
	if b.epochTimestamp != 0 {
		OMM.OMMAddUSER_DEFINED_EPOCH_TIMESTAMP(b.builder, b.epochTimestamp)
	}
	omm := OMM.OMMEnd(b.builder)

	OMM.FinishSizePrefixedOMMBuffer(b.builder, omm)

	// Return a copy to avoid buffer reuse issues
	result := make([]byte, len(b.builder.FinishedBytes()))
	copy(result, b.builder.FinishedBytes())
	return result
}
