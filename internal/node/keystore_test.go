package node

import (
	"os"
	"path/filepath"
	"testing"

	config "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/stretchr/testify/require"
)

func TestNewKeyStore(t *testing.T) {
	// Use the 'tmp' directory in the project root
	tmpDir := "../../tmp"

	// Ensure the tmp directory exists
	err := os.MkdirAll(tmpDir, 0755)
	require.NoError(t, err, "Failed to create tmp directory")

	// Set the configuration to use the tmp directory
	config.Conf.Datastore.Directory = tmpDir

	// Continue with the original test setup...
	testPassword := "testPassword"
	ks, err := NewKeyStore(testPassword)
	require.NoError(t, err, "Failed to create KeyStore")
	defer ks.Close()

	require.NotNil(t, ks, "KeyStore is nil")

	// Verify the database file exists
	dbPath := filepath.Join(tmpDir, DatabaseFileName)
	_, err = os.Stat(dbPath)
	defer os.RemoveAll(dbPath) // Clean up after the test
	require.NoError(t, err, "Database file does not exist")
}
