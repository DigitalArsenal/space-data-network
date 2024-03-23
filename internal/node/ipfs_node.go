package node

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	files "github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/path"
	"github.com/ipfs/kubo/core/coreiface/options"

	"github.com/ipfs/kubo/core/coreapi"
)

func (n *Node) AddFile(ctx context.Context, filePath string) (path.ImmutablePath, error) {
	var addedFile path.ImmutablePath
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

func (n *Node) AddFileFromStream(ctx context.Context, stream io.Reader) (path.ImmutablePath, error) {
	var addedFile path.ImmutablePath

	// Create a ReaderFile from the stream
	readerFile := files.NewReaderFile(stream)

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

func (n *Node) AddFileFromBytes(ctx context.Context, data []byte) (path.ImmutablePath, error) {
	var addedFile path.ImmutablePath

	// Convert the byte array to a Reader
	f := files.NewBytesFile(data)

	// Get the CoreAPI from the IpfsNode
	api, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return addedFile, fmt.Errorf("failed to get core API: %w", err)
	}

	// Use the CoreAPI to add the file to IPFS
	addedFile, err = api.Unixfs().Add(ctx, f)
	if err != nil {
		return addedFile, fmt.Errorf("failed to add file to IPFS: %w", err)
	}

	// Pin the added file to ensure it is not garbage collected
	if err := api.Pin().Add(ctx, addedFile); err != nil {
		return path.ImmutablePath{}, fmt.Errorf("failed to pin added file: %w", err)
	}
	return addedFile, nil
}

func (n *Node) PublishIPNSRecord(ctx context.Context, ipfsPathString string) (string, error) {
	if n.IPFS == nil {
		return "", fmt.Errorf("IPFS node is not initialized")
	}

	// Create a new path from the provided IPFS path string
	ipfsPath, err := path.NewPath(ipfsPathString)
	if err != nil {
		return "", fmt.Errorf("failed to create path: %w", err)
	}

	ttl := 24 * time.Hour // Cache TTL of 24 hours

	// Use coreapi to publish the IPNS record. This assumes you have initialized coreapi with your IPFS node.
	coreAPI, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return "", fmt.Errorf("failed to get IPFS coreAPI: %w", err)
	}

	// Publish the IPNS record using the IPFS core API
	ipnsPath, err := coreAPI.Name().Publish(ctx, ipfsPath, options.Name.Key("self"), options.Name.ValidTime(ttl))
	if err != nil {
		return "", fmt.Errorf("failed to publish IPNS record: %w", err)
	}

	fmt.Printf("Published IPNS record: %s\n", ipnsPath.String())

	// Since the IPNS record might not directly give us a CID, we return the whole path
	return ipnsPath.String(), nil
}

func (n *Node) AddFolderToIPNS(ctx context.Context, folderPath string) (string, error) {
	// Get the CoreAPI from the IpfsNode
	api, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return "", fmt.Errorf("failed to get core API: %w", err)
	}

	// Stat the directory to get the FileInfo
	stat, err := os.Stat(folderPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat folder: %w", err)
	}

	// Create a SerialFile for the directory
	folderFiles, err := files.NewSerialFile(folderPath, false, stat)
	if err != nil {
		return "", fmt.Errorf("failed to create serial file for folder: %w", err)
	}

	// Add the directory to IPFS
	folderCid, err := api.Unixfs().Add(ctx, folderFiles)
	if err != nil {
		return "", fmt.Errorf("failed to add folder to IPFS: %w", err)
	}

	// Pin the directory CID to ensure it remains on the node
	if err := api.Pin().Add(ctx, folderCid); err != nil {
		return "", fmt.Errorf("failed to pin folder CID: %w", err)
	}

	// Publish the directory CID to IPNS
	ipnsPath, err := n.PublishIPNSRecord(ctx, folderCid.String())
	if err != nil {
		return "", fmt.Errorf("failed to publish folder CID to IPNS: %w", err)
	}

	fmt.Printf("Folder published to IPNS at path: %s\n", ipnsPath)
	return ipnsPath, nil
}
