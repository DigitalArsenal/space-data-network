package node

import (
	"context"
	"fmt"
	"os"

	files "github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/path"

	"github.com/ipfs/kubo/core/coreapi"
)

func (n *Node) AddFile(ctx context.Context, filePath string) (path.ImmutablePath, error) {
	var addedFile = path.ImmutablePath{}
	// Open the file to add to IPFS
	file, err := os.Open(filePath)
	if err != nil {
		return addedFile, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create a ReaderFile using the files package
	readerFile := files.NewReaderFile(file)

	// Get the CoreAPI from the IpfsNode
	api, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return addedFile, fmt.Errorf("failed to get core API: %w", err)
	}

	// Use the CoreAPI to add the file to IPFS
	addedFile, err = api.Unixfs().Add(ctx, readerFile)
	if err != nil {
		return addedFile, fmt.Errorf("failed to add file to IPFS: %w", err)
	}

	return addedFile, nil
}
