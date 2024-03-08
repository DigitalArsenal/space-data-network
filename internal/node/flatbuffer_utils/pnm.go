package flatbuffer_utils

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
)

var PNMFID string = "$PNM"

func CreatePNM(multiformatAddress, cid, ethDigitalSignature string) []byte {
	builder := flatbuffers.NewBuilder(0)
	multiformatAddressOffset := builder.CreateString(multiformatAddress)
	cidOffset := builder.CreateString(cid)
	ethDigitalSignatureOffset := builder.CreateString(ethDigitalSignature)

	// Start the PNM object and set its fields
	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, multiformatAddressOffset)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddETH_DIGITAL_SIGNATURE(builder, ethDigitalSignatureOffset)
	// Add other fields as needed
	pnm := PNM.PNMEnd(builder)

	return SerializePNM(builder, pnm)
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

// SerializePNM takes a PNM object and serializes it into a byte slice.
func SerializePNM(builder *flatbuffers.Builder, pnm flatbuffers.UOffsetT) []byte {
	builder.FinishSizePrefixedWithFileIdentifier(pnm, []byte(PNMFID))
	return builder.FinishedBytes()
}

// ByteReader is an interface that abstracts the byte reading operation.
type ByteReader interface {
	ReadBytes() ([]byte, error)
}

// ByteSliceReader reads bytes from a byte slice.
type ByteSliceReader struct {
	Data []byte
}

func (r *ByteSliceReader) ReadBytes() ([]byte, error) {
	return r.Data, nil
}

// StreamReader reads bytes from a stream.
type StreamReader struct {
	Stream io.Reader
}

func (r *StreamReader) ReadBytes() ([]byte, error) {
	totalSizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(r.Stream, totalSizeBuf); err != nil {
		return nil, fmt.Errorf("failed to read total size prefix: %v", err)
	}
	totalSize := binary.LittleEndian.Uint32(totalSizeBuf)

	data := make([]byte, totalSize)
	if _, err := io.ReadFull(r.Stream, data); err != nil {
		return nil, fmt.Errorf("failed to read data: %v", err)
	}
	return data, nil
}

// DeserializePNM deserializes PNM from a ByteReader.
func DeserializePNM(reader ByteReader) (*PNM.PNM, error) {
	data, err := reader.ReadBytes()
	if err != nil {
		return nil, err
	}
	pnm := PNM.GetRootAsPNM(data, 4) // Skip the size prefix and file identifier
	return pnm, nil
}
