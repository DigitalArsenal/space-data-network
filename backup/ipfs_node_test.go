package node_test

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"os"
	"testing"

	"github.com/DigitalArsenal/space-data-network/internal/node"
	files "github.com/ipfs/boxo/files"
	boxoPath "github.com/ipfs/boxo/path"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupIpfsNode(t *testing.T) (*core.IpfsNode, func()) {
	// Create a temporary directory for the IPFS repo
	repoPath, err := ioutil.TempDir("", "ipfs-test")
	require.NoError(t, err)
	t.Logf("Temporary repo path: %s", repoPath) // Log the temp directory path

	// Initialize the repo; adjust as per your IPFS version if Init function has different parameters or location
	err = fsrepo.Init(repoPath, nil)
	require.NoError(t, err, "fsrepo.Init failed") // Check error explicitly

	// Open the repo
	repo, err := fsrepo.Open(repoPath)
	require.NoError(t, err, "fsrepo.Open failed") // Check error explicitly

	// Create and start an IPFS node using the opened repo
	cfg := &core.BuildCfg{
		Online: true,
		Repo:   repo,
	}
	node, err := core.NewNode(context.Background(), cfg)
	require.NoError(t, err, "core.NewNode failed") // Check error explicitly

	// Return the cleanup function to remove the temporary directory
	cleanup := func() {
		node.Close()
		os.RemoveAll(repoPath)
	}

	return node, cleanup
}

func TestAddFile(t *testing.T) {
	n, cleanup := setupIpfsNode(t)
	defer cleanup()

	testNode := &node.Node{IPFS: n}
	ctx := context.Background()

	// Create a temporary file
	tempFile, err := os.CreateTemp("", "ipfs-add-test")
	require.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.WriteString("Hello, IPFS!")
	require.NoError(t, err)
	require.NoError(t, tempFile.Close())

	// Test AddFile
	addedPath, err := testNode.AddFile(ctx, tempFile.Name())
	require.NoError(t, err)
	assert.NotEmpty(t, addedPath)

	api, err := coreapi.NewCoreAPI(n)
	require.NoError(t, err)

	// Read back the file and check its content
	pp, _ := boxoPath.NewPath(addedPath.String())
	readFile, err := api.Unixfs().Get(ctx, pp)
	require.NoError(t, err)
	data, err := io.ReadAll(readFile.(files.File))
	require.NoError(t, err)
	assert.Equal(t, "Hello, IPFS!", string(data))
}

func TestAddFileFromStream(t *testing.T) {
	n, cleanup := setupIpfsNode(t)
	defer cleanup()

	testNode := &node.Node{IPFS: n}
	ctx := context.Background()

	// Create a buffer with data
	buffer := bytes.NewBufferString("Hello, IPFS from stream!")

	// Test AddFileFromStream
	addedPath, err := testNode.AddFileFromStream(ctx, buffer)
	require.NoError(t, err)
	assert.NotEmpty(t, addedPath)

	api, err := coreapi.NewCoreAPI(n)
	require.NoError(t, err)

	// Read back the file and check its content
	pp, _ := boxoPath.NewPath(addedPath.String())
	readFile, err := api.Unixfs().Get(ctx, pp)
	require.NoError(t, err)
	data, err := io.ReadAll(readFile.(files.File))
	require.NoError(t, err)
	assert.Equal(t, "Hello, IPFS from stream!", string(data))
}

func TestAddFileFromBytes(t *testing.T) {
	n, cleanup := setupIpfsNode(t)
	defer cleanup()

	testNode := &node.Node{IPFS: n}
	ctx := context.Background()

	// Create a byte slice with data
	data := []byte("Hello, IPFS from bytes!")

	// Test AddFileFromBytes
	addedPath, err := testNode.AddFileFromBytes(ctx, data)
	require.NoError(t, err)
	assert.NotEmpty(t, addedPath)

	api, err := coreapi.NewCoreAPI(n)
	require.NoError(t, err)

	// Read back the file and check its content
	pp, _ := boxoPath.NewPath(addedPath.String())
	readFile, err := api.Unixfs().Get(ctx, pp)
	require.NoError(t, err)
	readData, err := ioutil.ReadAll(readFile.(files.File))
	require.NoError(t, err)
	assert.Equal(t, "Hello, IPFS from bytes!", string(readData))
}
