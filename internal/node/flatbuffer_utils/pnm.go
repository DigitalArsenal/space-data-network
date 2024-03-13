package flatbuffer_utils

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	cid "github.com/ipfs/go-cid"
	multihash "github.com/multiformats/go-multihash"
)

var PNMFID string = "$PNM"

func GenerateCID(data []byte) (string, error) {
	// Hash the data to get a multihash
	hash, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return "", fmt.Errorf("failed to hash EPM data: %w", err)
	}

	// Create a CID using the hashed data
	c := cid.NewCidV1(cid.Raw, hash)

	return c.String(), nil
}

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

// DeserializePNM deserializes PNM from a ByteReader.
func DeserializePNM(ctx context.Context, stream io.Reader) (*PNM.PNM, error) {
	// Create a buffer to hold the size prefix.
	totalSizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, totalSizeBuf); err != nil {
		return nil, fmt.Errorf("failed to read total size prefix: %v", err)
	}
	totalSize := binary.LittleEndian.Uint32(totalSizeBuf)

	// Initialize a buffer to hold the incoming data.
	data := make([]byte, 0, totalSize)

	// Keep reading data until the buffer is filled to the expected size.
	for uint32(len(data)) < totalSize {
		select {
		case <-ctx.Done():
			return nil, ctx.Err() // Context cancellation or deadline exceeded.
		default:
			chunkSize := totalSize - uint32(len(data))
			chunk := make([]byte, chunkSize)
			n, err := io.ReadFull(stream, chunk)
			if err != nil {
				return nil, fmt.Errorf("failed to read PNM data: %v", err)
			}
			data = append(data, chunk[:n]...)
		}
	}

	// Use GetRootAsPNM to deserialize the data.
	pnm := PNM.GetRootAsPNM(data, 0) // The data buffer is ready for deserialization.
	return pnm, nil
}
