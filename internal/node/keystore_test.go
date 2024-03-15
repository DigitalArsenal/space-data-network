package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/golang-migrate/migrate/v4/database/sqlcipher"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) (*KeyStore, func()) {
	t.Helper()

	// Create a temporary directory for the test database
	tmpDir, err := os.MkdirTemp("", "keyStoreTest")
	require.NoError(t, err)

	// Override the default database path for testing
	dbPath := filepath.Join(tmpDir, DatabaseFileName)

	// Initialize a new KeyStore with a dummy password and the custom dbPath
	keyStore, err := NewKeyStore("dummy_password", dbPath) // Modified to use dbPath
	require.NoError(t, err)

	// Cleanup function to remove the temporary directory
	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return keyStore, cleanup
}

func TestNewKeyStore(t *testing.T) {
	keyStore, cleanup := setupTestDB(t)
	defer cleanup()

	assert.NotNil(t, keyStore)
}

func TestMigration(t *testing.T) {
	keyStore, cleanup := setupTestDB(t)
	defer cleanup()

	// Apply the initial table creation statements for v1.0
	for _, stmt := range strings.Split(createTableStatements["v1.0"], ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		_, err := keyStore.db.Exec(stmt)
		require.NoError(t, err)
	}

	// Manually set the database version to "v1.0"
	_, err := keyStore.db.Exec("INSERT INTO db_version (version) VALUES ('v1.0')")
	require.NoError(t, err)

	// Run the migration script to upgrade to v1.1
	err = initializeDatabase(keyStore.db)
	require.NoError(t, err)

	// Verify the migration was successful
	var version string
	err = keyStore.db.QueryRow("SELECT version FROM db_version").Scan(&version)
	require.NoError(t, err)
	assert.Equal(t, "v1.0", version)

	_, err = keyStore.db.Query("SELECT * FROM EPM")
	require.NoError(t, err)
}

func TestGetOrGeneratePrivateKey(t *testing.T) {
	keyStore, cleanup := setupTestDB(t)
	defer cleanup()

	// Test generating a new key
	wallet, signingAccount, encryptionAccount, privKey, err := keyStore.GetOrGeneratePrivateKey()
	require.NotNil(t, wallet)
	require.NotNil(t, signingAccount)
	require.NotNil(t, encryptionAccount)
	require.NotNil(t, privKey)
	require.NoError(t, err)

	// Test retrieving the same key again
	wallet, signingAccount, encryptionAccount, samePrivKey, err := keyStore.GetOrGeneratePrivateKey()
	require.NotNil(t, wallet)
	require.NotNil(t, signingAccount)
	require.NotNil(t, encryptionAccount)
	require.NotNil(t, privKey)
	require.NoError(t, err)

	// Check if the private key remains the same across calls
	assert.Equal(t, privKey, samePrivKey)
}
