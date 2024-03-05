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
	tmpDir := filepath.Join("..", "..", "tmp", "keystore_test")

	// Ensure the tmp directory exists
	err := os.MkdirAll(tmpDir, 0755)
	require.NoError(t, err, "Failed to create tmp directory")
	//defer os.RemoveAll(tmpDir) // Clean up the tmp directory after the test

	// Set the configuration to use the tmp directory
	config.Conf.Datastore.Directory = tmpDir

	// Continue with the original test setup...
	testPassword := "testPassword"
	ks, err := NewKeyStore(testPassword)
	require.NoError(t, err, "Failed to create KeyStore")
	defer ks.Close() // Ensure the keystore is closed after the test

	require.NotNil(t, ks, "KeyStore is nil")

	// Verify the database file exists
	dbPath := filepath.Join(tmpDir, DatabaseFileName)
	_, err = os.Stat(dbPath)
	require.NoError(t, err, "Database file does not exist")

	// Export the database
	exportPath := filepath.Join(tmpDir, "exported_"+DatabaseFileName)
	err = ks.ExportDatabase(exportPath)
	require.NoError(t, err, "Failed to export database")

	// Read the original database file
	originalDBBytes, err := os.ReadFile(dbPath)
	require.NoError(t, err, "Failed to read original database file")

	// Read the exported database file
	exportedDBBytes, err := os.ReadFile(exportPath)
	require.NoError(t, err, "Failed to read exported database file")

	// Compare the original and exported database bytes
	require.Equal(t, originalDBBytes, exportedDBBytes, "Exported database file does not match the original")
}
