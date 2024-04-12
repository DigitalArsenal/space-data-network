package sds_utils

import (
	"context"
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

	// Return the serialized PNM
	return SerializePNM(builder, pnm)
}

// SerializePNM takes a PNM object and serializes it into a byte slice.
func SerializePNM(builder *flatbuffers.Builder, epm flatbuffers.UOffsetT) []byte {
	builder.FinishSizePrefixedWithFileIdentifier(epm, []byte(PNMFID))
	return builder.FinishedBytes()
}

// DeserializePNM deserializes PNM from a ByteReader.
func DeserializePNM(ctx context.Context, src interface{}) (*PNM.PNM, error) {
	data, err := ReadDataFromSource(ctx, src)
	if err != nil {
		return nil, err
	}

	epm := PNM.GetSizePrefixedRootAsPNM(data, 0)
	return epm, nil
}
