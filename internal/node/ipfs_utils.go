package node

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	files "github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/path"
	ipfsConfig "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core/coreapi"
	coreiface "github.com/ipfs/kubo/core/coreiface"
	"github.com/ipfs/kubo/core/coreiface/options"
)

var datastoreSpec = map[string]interface{}{
	"type": "mount",
	"mounts": []interface{}{
		map[string]interface{}{
			"mountpoint": "/blocks",
			"type":       "measure",
			"prefix":     "flatfs.datastore",
			"child": map[string]interface{}{
				"type":      "flatfs",
				"path":      "blocks",
				"sync":      true,
				"shardFunc": "/repo/flatfs/shard/v1/next-to-last/2",
			},
		},
		map[string]interface{}{
			"mountpoint": "/",
			"type":       "measure",
			"prefix":     "leveldb.datastore",
			"child": map[string]interface{}{
				"type":        "levelds",
				"path":        "datastore",
				"compression": "none",
			},
		},
	},
}

var DatastoreConfig = ipfsConfig.Datastore{
	StorageMax:         "10GB",
	StorageGCWatermark: 90,
	GCPeriod:           "1h", // Example, set according to your needs
	Spec:               datastoreSpec,
	HashOnRead:         false, // Default setting
	BloomFilterSize:    0,     // Default setting
}

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

func (n *Node) ListPinnedFiles(ctx context.Context) ([]string, error) {
	var pinnedFiles []string

	// Get the CoreAPI from the IpfsNode
	api, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return nil, fmt.Errorf("failed to get core API: %w", err)
	}

	// List all pinned files
	pinList, err := api.Pin().Ls(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list pinned files: %w", err)
	}

	// Iterate through the pin list and extract the paths
	for pin := range pinList {
		pinnedFiles = append(pinnedFiles, pin.Path().String())
	}

	return pinnedFiles, nil
}

func (n *Node) UnpinFile(ctx context.Context, pathStr string) error {
	// Convert the string path to an IPFS path
	ipfsPath, err := path.NewPath(pathStr)
	if err != nil {
		return fmt.Errorf("failed to parse path: %w", err)
	}

	// Get the CoreAPI from the IpfsNode
	api, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return fmt.Errorf("failed to get core API: %w", err)
	}

	// Unpin the file
	err = api.Pin().Rm(ctx, ipfsPath)
	if err != nil {
		return fmt.Errorf("failed to unpin file: %w", err)
	}

	return nil
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

	ttl := 24 * time.Hour // Cache TTL of 24

	coreAPI, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return "", fmt.Errorf("failed to get IPFS coreAPI: %w", err)
	}

	// Publish the IPNS record using the IPFS core API
	ipnsPath, err := coreAPI.Name().Publish(ctx, ipfsPath, options.Name.Key("self"), options.Name.ValidTime(ttl))
	if err != nil {
		return "", fmt.Errorf("failed to publish IPNS record: %w", err)
	}

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

	return ipnsPath, nil
}

func (n *Node) ResolveIPNS(ctx context.Context, ipnsPath string) (string, error) {
	if n.IPFS == nil {
		return "", fmt.Errorf("IPFS node is not initialized")
	}

	coreAPI, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return "", fmt.Errorf("failed to get IPFS coreAPI: %w", err)
	}

	resolvedPath, err := coreAPI.Name().Resolve(ctx, ipnsPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve IPNS path: %w", err)
	}

	return resolvedPath.String(), nil
}

func (n *Node) unpublishIPNSRecord() error {

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if n.IPFS == nil {
		return fmt.Errorf("IPFS node is not initialized")
	}

	coreAPI, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return fmt.Errorf("failed to get IPFS coreAPI: %w", err)
	}

	// Path to an empty file or directory on IPFS
	// Use an empty file CID as discussed in the conversation
	emptyContentPath := "/ipfs/bafkqaaa" // This CID points to an inlined empty file

	// Convert the string path to a core path
	emptyPath, err := path.NewPath(emptyContentPath)
	if err != nil {
		return fmt.Errorf("failed to create path: %w", err)
	}

	ttl := 10 * time.Minute // Short TTL to minimize the time this record is active

	// Publish the "empty" record to IPNS
	_, err = coreAPI.Name().Publish(ctx, emptyPath, options.Name.Key("self"), options.Name.ValidTime(ttl))
	if err != nil {
		return fmt.Errorf("failed to publish IPNS record: %w", err)
	}

	return nil
}

func (n *Node) ListDirectoryContents(ctx context.Context, ipfsOrIpnsPath string) ([]coreiface.DirEntry, error) {
	if n.IPFS == nil {
		return nil, fmt.Errorf("IPFS node is not initialized")
	}

	coreAPI, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return nil, fmt.Errorf("failed to get IPFS coreAPI: %w", err)
	}

	resolvedPath, err := path.NewPath(ipfsOrIpnsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create path: %w", err)
	}

	var entries []coreiface.DirEntry
	dir, err := coreAPI.Unixfs().Ls(ctx, resolvedPath)
	if err != nil {
		return nil, err
	}

	for entry := range dir {
		if entry.Err != nil {
			return nil, entry.Err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

func (n *Node) GetFileContents(ctx context.Context, ipfsPath string) ([]byte, error) {
	if n.IPFS == nil {
		return nil, fmt.Errorf("IPFS node is not initialized")
	}

	coreAPI, err := coreapi.NewCoreAPI(n.IPFS)
	if err != nil {
		return nil, fmt.Errorf("failed to get IPFS coreAPI: %w", err)
	}

	resolvedPath, err := path.NewPath(ipfsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create path: %w", err)
	}

	fileNode, err := coreAPI.Unixfs().Get(ctx, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}

	file, ok := fileNode.(files.File)
	if !ok {
		return nil, fmt.Errorf("resolved path is not a file")
	}

	return io.ReadAll(file)
}
