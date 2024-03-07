package node

import (
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
)

var PNMFID string = "$PNM"

func GeneratePNM(builder *flatbuffers.Builder, multiformatAddress, cid, ethDigitalSignature string) flatbuffers.UOffsetT {
	multiformatAddressOffset := builder.CreateString(multiformatAddress)
	cidOffset := builder.CreateString(cid)
	ethDigitalSignatureOffset := builder.CreateString(ethDigitalSignature)

	// Start the PNM object and set its fields
	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, multiformatAddressOffset)
	PNM.PNMAddCID_FID(builder, cidOffset)
	PNM.PNMAddETH_DIGITAL_SIGNATURE(builder, ethDigitalSignatureOffset)
	// Add other fields as needed
	pnm := PNM.PNMEnd(builder)

	return pnm
}

func GeneratePNMCollection(builder *flatbuffers.Builder, pnms []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	// Create a vector of PNMs
	PNM.PNMCOLLECTIONStartRECORDSVector(builder, len(pnms))
	for i := len(pnms) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(pnms[i])
	}
	records := builder.EndVector(len(pnms))

	// Start the PNMCOLLECTION object
	PNM.PNMCOLLECTIONStart(builder)
	PNM.PNMCOLLECTIONAddRECORDS(builder, records)
	collection := PNM.PNMCOLLECTIONEnd(builder)

	return collection
}
