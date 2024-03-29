package spacedatastandards_utils

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	files "github.com/ipfs/boxo/files"
	config "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	coreiface "github.com/ipfs/kubo/core/coreiface"
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

func createNode(ctx context.Context, repoPath string) (coreiface.CoreAPI, error) {
	repo, err := fsrepo.Open(repoPath)
	if err != nil {
		return nil, err
	}

	node, err := core.NewNode(ctx, &core.BuildCfg{
		Online: true,
		Repo:   repo,
	})
	if err != nil {
		return nil, err
	}

	return coreapi.NewCoreAPI(node)
}

func GenerateCID(data []byte) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repoPath, err := createTempRepo(ctx)
	if err != nil {
		return "", err
	}
	// Ensure the temporary repo is cleaned up after use
	defer os.RemoveAll(repoPath)

	ipfs, err := createNode(ctx, repoPath)
	if err != nil {
		return "", err
	}

	f := files.NewBytesFile(data)
	cidFile, err := ipfs.Unixfs().Add(ctx, f)
	if err != nil {
		return "", fmt.Errorf("could not add data to IPFS: %s", err)
	}

	fmt.Println("Generated CID", cidFile.String())
	return strings.TrimPrefix(cidFile.String(), "/ipfs/"), nil
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

	return SerializePNM(builder, pnm)
}

// SerializePNM takes a PNM object and serializes it into a byte slice.
func SerializePNM(builder *flatbuffers.Builder, pnm flatbuffers.UOffsetT) []byte {
	builder.FinishSizePrefixedWithFileIdentifier(pnm, []byte(PNMFID))
	return builder.FinishedBytes()
}

// DeserializePNM deserializes PNM from a ByteReader.
func DeserializePNM(ctx context.Context, src interface{}) (*PNM.PNM, error) {
	data, _, err := ReadDataFromSource(ctx, src)
	if err != nil {
		return nil, err
	}

	pnm := PNM.GetSizePrefixedRootAsPNM(data, 0)
	return pnm, nil
}
