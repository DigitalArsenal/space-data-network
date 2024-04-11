package spacedatastandards_utils

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	config "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/repo/fsrepo"
)

var PNMFID string = "$PNM"

func createTempRepo(_ context.Context) (string, error) {
	// Create a unique temporary directory for the repo
	repoPath, err := os.MkdirTemp("", "ipfs-repo-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary repo directory: %s", err)
	}

	cfg, err := config.Init(io.Discard, 2048)
	if err != nil {
		// Clean up the created temporary directory on error
		os.RemoveAll(repoPath)
		return "", err
	}

	err = fsrepo.Init(repoPath, cfg)
	if err != nil {
		// Clean up the created temporary directory on error
		os.RemoveAll(repoPath)
		return "", fmt.Errorf("failed to init ephemeral node: %s", err)
	}

	return repoPath, nil
}

func CreatePNM(multiformatAddress string, cid string, ethDigitalSignature string) []byte {
	builder := flatbuffers.NewBuilder(0)
	multiformatAddressOffset := builder.CreateString(multiformatAddress)
	cidOffset := builder.CreateString(cid)
	ethDigitalSignatureOffset := builder.CreateString(ethDigitalSignature)
	publishTimeStampOffset := builder.CreateString(time.Now().Format(time.RFC3339))
	signatureTypeOffset := builder.CreateString("ETH")

	// Start the PNM object and set its fields
	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, multiformatAddressOffset)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, publishTimeStampOffset)
	PNM.PNMAddSIGNATURE(builder, ethDigitalSignatureOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, signatureTypeOffset)
	// Add other fields as needed
	pnm := PNM.PNMEnd(builder)

	return SerializePNMs(builder, []flatbuffers.UOffsetT{pnm})
}

// SerializePNM takes a PNM object and serializes it into a byte slice.
func SerializePNMs(builder *flatbuffers.Builder, pnms []flatbuffers.UOffsetT) []byte {
	// We will collect all serialized PNMs into this slice
	var serializedPNMs []byte

	for _, pnm := range pnms {
		// Finish each PNM with a size prefix and a file identifier
		builder.FinishSizePrefixedWithFileIdentifier(pnm, []byte(PNMFID))

		// Append the serialized PNM to our slice
		serializedPNMs = append(serializedPNMs, builder.FinishedBytes()...)

		// Reset the builder for the next PNM
		builder.Reset()
	}

	return serializedPNMs
}

// DeserializePNM deserializes PNM from a ByteReader.
func DeserializePNMs(data []byte) ([]*PNM.PNM, error) {
	var pnms []*PNM.PNM
	offset := 0

	for offset < len(data) {
		// Read the size of the next PNM (size is prefixed as a 32-bit unsigned integer)
		if len(data)-offset < 4 {
			return nil, fmt.Errorf("incomplete data for PNM size at offset %d", offset)
		}
		size := binary.LittleEndian.Uint32(data[offset:])
		offset += 4 // Move past the size prefix

		// Ensure the entire PNM is contained within the data slice
		if len(data)-offset < int(size) {
			return nil, fmt.Errorf("incomplete PNM data starting at offset %d", offset)
		}

		// Deserialize the PNM and add it to the slice
		pnmData := data[offset : offset+int(size)]
		pnm := PNM.GetRootAsPNM(pnmData, flatbuffers.GetUOffsetT(pnmData))
		pnms = append(pnms, pnm)

		offset += int(size) // Move to the start of the next PNM
	}

	return pnms, nil
}
