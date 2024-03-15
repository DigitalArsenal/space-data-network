package spacedatastandards_utils

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	files "github.com/ipfs/go-libipfs/files"
	config "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	coreiface "github.com/ipfs/kubo/core/coreiface"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo/fsrepo"
)

var PNMFID string = "$PNM"

func setupPlugins(externalPluginsPath string) error {
	plugins, err := loader.NewPluginLoader(filepath.Join(externalPluginsPath, "plugins"))
	if err != nil {
		return fmt.Errorf("error loading plugins: %s", err)
	}
	if err := plugins.Initialize(); err != nil {
		return fmt.Errorf("error initializing plugins: %s", err)
	}
	if err := plugins.Inject(); err != nil {
		return fmt.Errorf("error injecting plugins: %s", err)
	}
	return nil
}

func createTempRepo(_ context.Context) (string, error) {
	repoPath := os.TempDir()

	cfg, err := config.Init(io.Discard, 2048)
	if err != nil {
		return "", err
	}

	err = fsrepo.Init(repoPath, cfg)
	if err != nil {
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

	if err := setupPlugins(""); err != nil {
		return "", err
	}

	repoPath, err := createTempRepo(ctx)
	if err != nil {
		return "", err
	}

	ipfs, err := createNode(ctx, repoPath)
	if err != nil {
		return "", err
	}

	f := files.NewBytesFile(data)
	cidFile, err := ipfs.Unixfs().Add(ctx, f)
	if err != nil {
		return "", fmt.Errorf("could not add data to IPFS: %s", err)
	}

	return cidFile.String(), nil
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
	fileID := string(data[4:8])
	if fileID != PNMFID {
		return nil, fmt.Errorf("unexpected file identifier: got %s, want %s", fileID, EPMFID)
	}

	// Use GetRootAsPNM to deserialize the data.
	pnm := PNM.GetRootAsPNM(data, 0) // The data buffer is ready for deserialization.
	return pnm, nil
}
