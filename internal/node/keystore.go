package node

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	config "github.com/DigitalArsenal/space-data-network/configs"
	_ "github.com/golang-migrate/migrate/v4/database/sqlcipher"
	"github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/argon2"
)

const (
	KeyDirName        = ".spacedatanetwork"
	DatabaseFileName  = "keys.db"
	EncryptionKeySize = 32
)

type KeyStore struct {
	db     *sql.DB
	dbPath string // Add the dbPath field to store the path to the database
}

func NewKeyStore(password string) (*KeyStore, error) {
	fmt.Println("Initializing keystore")
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	keyDir := filepath.Join(homeDir, KeyDirName)
	if _, err := os.Stat(keyDir); os.IsNotExist(err) {
		err := os.MkdirAll(keyDir, 0700)
		if err != nil {
			return nil, err
		}
	}

	var dbPath string
	if config.Conf.Datastore.Directory != "" {
		dbPath = filepath.Join(config.Conf.Datastore.Directory, DatabaseFileName)
	} else {
		dbPath = filepath.Join(keyDir, DatabaseFileName)
	}

	// Open SQLite database with encryption using SQLCipher
	db, err := sql.Open("sqlite3", fmt.Sprintf("%s?_pragma_key=%s", dbPath, password))
	if err != nil {
		return nil, err
	}

	// Create tables if they don't exist
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS private_keys (id INTEGER PRIMARY KEY, private_key BLOB)`)
	if err != nil {
		return nil, err
	}

	return &KeyStore{db: db, dbPath: dbPath}, nil // Set the dbPath when returning the KeyStore instance
}

func (ks *KeyStore) Close() error {
	fmt.Println("Closing keystore")
	if ks.db != nil {
		return ks.db.Close()
	}
	return nil
}

func padTo32Bytes(data []byte) []byte {
	if len(data) >= 32 {
		return data[:32] // Ensure data is not longer than 32 bytes
	}

	padded := make([]byte, 32) // Create a slice of 32 bytes, automatically initialized to zero
	copy(padded, data)         // Copy the original data into the beginning of the padded slice
	return padded
}

func (ks *KeyStore) GetOrGeneratePrivateKey(options NodeOptions) (crypto.PrivKey, error) {
	if len(options.RawKey) > 0 {
		var priv crypto.PrivKey
		var err error

		options.RawKey = padTo32Bytes(options.RawKey)

		// Handle the provided raw key
		if len(options.RawKey) == 33 {
			priv, err = crypto.UnmarshalPrivateKey(options.RawKey)
		} else if len(options.RawKey) == 32 {
			priv, err = crypto.UnmarshalSecp256k1PrivateKey(options.RawKey)
		} else {
			err = fmt.Errorf("invalid raw key length")
		}

		if err != nil {
			return nil, err
		}

		// Convert the private key to bytes for storage
		privKeyBytes, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, err
		}

		// Delete any existing key
		_, err = ks.db.Exec("DELETE FROM private_keys WHERE id = 1")
		if err != nil {
			return nil, err
		}

		// Insert the new private key into the database
		_, err = ks.db.Exec("INSERT INTO private_keys (id, private_key) VALUES (1, ?)", privKeyBytes)
		if err != nil {
			return nil, err
		}

		return priv, nil
	}

	// Query the database for an existing key if no RawKey is provided
	var privKeyBytes []byte
	err := ks.db.QueryRow("SELECT private_key FROM private_keys WHERE id = 1").Scan(&privKeyBytes)
	if err == sql.ErrNoRows {
		// Generate a new secp256k1 key
		priv, _, err := crypto.GenerateKeyPair(crypto.Secp256k1, 256)
		if err != nil {
			return nil, err
		}
		privKeyBytes, err = crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, err
		}

		// Store the new private key in the database
		_, err = ks.db.Exec("INSERT INTO private_keys (id, private_key) VALUES (1, ?)", privKeyBytes)
		if err != nil {
			return nil, err
		}

		return priv, nil
	} else if err != nil {
		return nil, err
	}

	return crypto.UnmarshalPrivateKey(privKeyBytes)
}

func generatePassword() string {
	hostname, _ := os.Hostname()
	homeDir, _ := os.UserHomeDir()

	input := fmt.Sprintf("%s:%s", homeDir, hostname)
	return hex.EncodeToString(argon2.IDKey([]byte(input), []byte("some_salt"), 1, 64*1024, 4, 32))
}

// ExportDatabase exports the current database to the specified file path.
func (ks *KeyStore) ExportDatabase(exportPath string) error {
	// Ensure the database is not nil
	if ks.db == nil {
		return fmt.Errorf("database is not initialized")
	}

	// Open the current database file
	sourceFile, err := os.Open(ks.dbPath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Create the export file
	destinationFile, err := os.Create(exportPath)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	// Copy the database file to the export file
	_, err = io.Copy(destinationFile, sourceFile)
	return err
}

// ImportDatabase replaces the current database with the one at the specified file path.
func (ks *KeyStore) ImportDatabase(importPath string) error {
	// Ensure the database is closed before replacing the file
	if ks.db != nil {
		if err := ks.db.Close(); err != nil {
			return err
		}
	}

	// Replace the current database file with the imported one
	importedFile, err := os.Open(importPath)
	if err != nil {
		return err
	}
	defer importedFile.Close()

	// Create a new file or truncate the existing file
	destinationFile, err := os.Create(ks.dbPath)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	// Copy the imported database file to the destination file
	_, err = io.Copy(destinationFile, importedFile)
	if err != nil {
		return err
	}

	// Re-open the database
	db, err := sql.Open("sqlite3", ks.dbPath)
	if err != nil {
		return err
	}
	ks.db = db

	return nil
}
