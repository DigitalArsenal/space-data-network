package node

import (
	"database/sql"
	"encoding/hex"
	"fmt"
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
	db *sql.DB
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

	dbPath := filepath.Join(keyDir, DatabaseFileName)

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

	return &KeyStore{db: db}, nil
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
	input := fmt.Sprintf("%s:%s", config.Conf.Datastore.Directory, hostname)
	return hex.EncodeToString(argon2.IDKey([]byte(input), []byte("some_salt"), 1, 64*1024, 4, 32))
}
