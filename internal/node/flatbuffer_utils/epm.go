package flatbuffer_utils

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	EPM "github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
)

var EPMFID string = "$EPM"

func CreateEPM(
	distinguishedName string,
	legalName string,
	familyName string,
	givenName string,
	additionalName string,
	honorificPrefix string,
	honorificSuffix string,
	jobTitle string,
	occupation string,
	alternateNames []string,
	email string,
	telephone string,
) []byte {
	builder := flatbuffers.NewBuilder(0)

	// Create string offsets for all fields that are of string type
	dnOffset := builder.CreateString(distinguishedName)
	legalNameOffset := builder.CreateString(legalName)
	familyNameOffset := builder.CreateString(familyName)
	givenNameOffset := builder.CreateString(givenName)
	additionalNameOffset := builder.CreateString(additionalName)
	honorificPrefixOffset := builder.CreateString(honorificPrefix)
	honorificSuffixOffset := builder.CreateString(honorificSuffix)
	jobTitleOffset := builder.CreateString(jobTitle)
	occupationOffset := builder.CreateString(occupation)
	emailOffset := builder.CreateString(email)
	telephoneOffset := builder.CreateString(telephone)

	// Create vectors for alternate names and multiformat addresses
	alternateNamesVec := createStringVector(builder, alternateNames)

	// Start the EPM object
	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddLEGAL_NAME(builder, legalNameOffset)
	EPM.EPMAddFAMILY_NAME(builder, familyNameOffset)
	EPM.EPMAddGIVEN_NAME(builder, givenNameOffset)
	EPM.EPMAddADDITIONAL_NAME(builder, additionalNameOffset)
	EPM.EPMAddHONORIFIC_PREFIX(builder, honorificPrefixOffset)
	EPM.EPMAddHONORIFIC_SUFFIX(builder, honorificSuffixOffset)
	EPM.EPMAddJOB_TITLE(builder, jobTitleOffset)
	EPM.EPMAddOCCUPATION(builder, occupationOffset)
	EPM.EPMAddALTERNATE_NAMES(builder, alternateNamesVec)
	EPM.EPMAddEMAIL(builder, emailOffset)
	EPM.EPMAddTELEPHONE(builder, telephoneOffset)

	// Here you would normally add keys, but it's removed as per your request.

	// Finish the EPM object
	epm := EPM.EPMEnd(builder)
	builder.Finish(epm)

	// Return the byte slice containing the EPM object
	return builder.FinishedBytes()
}

// createStringVector is a helper function that creates a vector of strings in the FlatBuffers builder.
func createStringVector(builder *flatbuffers.Builder, items []string) flatbuffers.UOffsetT {
	offsets := make([]flatbuffers.UOffsetT, len(items))
	for i, item := range items {
		offsets[i] = builder.CreateString(item)
	}

	builder.StartVector(4, len(items), 4) // Specify the size of each element to be 4 bytes (UOffsetT size)
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}

	return builder.EndVector(len(items))
}

func GenerateEPMCollection(builder *flatbuffers.Builder, epms []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	EPM.EPMCOLLECTIONStartRECORDSVector(builder, len(epms))
	for i := len(epms) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(epms[i])
	}
	records := builder.EndVector(len(epms))

	EPM.EPMCOLLECTIONStart(builder)
	EPM.EPMCOLLECTIONAddRECORDS(builder, records)
	collection := EPM.EPMCOLLECTIONEnd(builder)

	builder.FinishSizePrefixedWithFileIdentifier(collection, []byte(EPMFID))
	return collection
}

func SerializeEPM(builder *flatbuffers.Builder, epm flatbuffers.UOffsetT) []byte {
	builder.FinishSizePrefixedWithFileIdentifier(epm, []byte(EPMFID))
	return builder.FinishedBytes()
}

func DeserializeEPM(ctx context.Context, stream io.Reader) (*EPM.EPM, error) {
	totalSizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, totalSizeBuf); err != nil {
		return nil, fmt.Errorf("failed to read total size prefix: %v", err)
	}
	totalSize := binary.LittleEndian.Uint32(totalSizeBuf)
	data := make([]byte, 0, totalSize)
	for uint32(len(data)) < totalSize {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			chunkSize := totalSize - uint32(len(data))
			chunk := make([]byte, chunkSize)
			n, err := io.ReadFull(stream, chunk)
			if err != nil {
				return nil, fmt.Errorf("failed to read EPM data: %v", err)
			}
			data = append(data, chunk[:n]...)
		}
	}
	epm := EPM.GetRootAsEPM(data, 0)
	return epm, nil
}
