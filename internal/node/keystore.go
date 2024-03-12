package node

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	config "github.com/DigitalArsenal/space-data-network/configs"
	_ "github.com/glebarez/go-sqlite"
	"github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/argon2"
)

const (
	KeyDirName        = ".spacedatanetwork"
	DatabaseFileName  = "keys.db"
	EncryptionKeySize = 32
	CurrentVersion    = "v1.0"
)

type TableCreationScripts map[string]string
type MigrationScripts map[string]string

var (
	createTableStatements = TableCreationScripts{
		"v1.0": `CREATE TABLE IF NOT EXISTS private_keys (id INTEGER PRIMARY KEY, private_key BLOB);
                 CREATE TABLE IF NOT EXISTS EPM (id INTEGER PRIMARY KEY AUTOINCREMENT, DN TEXT NOT NULL, EPM_DATA BLOB NOT NULL, UNIQUE(DN));`,
	}

	migrations = MigrationScripts{
		"v1.0": ``,
	}
)

type KeyStore struct {
	db     *sql.DB
	dbPath string
}

func NewKeyStore(password string, customPaths ...string) (*KeyStore, error) {
	var dbPath string

	if len(customPaths) > 0 {
		// If a custom path is provided, use it
		dbPath = customPaths[0]
	} else {
		dbPath = filepath.Join(config.Conf.Datastore.Directory, DatabaseFileName)
	}

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), os.ModePerm); err != nil {
		return nil, err
	}

	// Open the database with the resolved path
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma_key=%s", dbPath, password))
	if err != nil {
		return nil, err
	}

	// Initialize the database
	if err := initializeDatabase(db); err != nil {
		db.Close()
		return nil, err
	}

	return &KeyStore{db: db, dbPath: dbPath}, nil
}

func initializeDatabase(db *sql.DB) error {
	log.Println("Starting database initialization...")

	var version string
	// Check for the existence of the db_version table
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS db_version (version TEXT)")
	if err != nil {
		log.Printf("Error creating db_version table: %v", err)
		return err
	}

	// Check the current version in the db_version table
	err = db.QueryRow("SELECT version FROM db_version").Scan(&version)
	if err == sql.ErrNoRows {
		// This is a fresh database, so set up the latest schema without applying migrations
		log.Println("Fresh database detected, setting up the latest schema...")
		stmts, ok := createTableStatements[CurrentVersion]
		if !ok {
			errMsg := fmt.Sprintf("No create table statements for version %s", CurrentVersion)
			log.Println(errMsg)
			return fmt.Errorf(errMsg)
		}

		for _, stmt := range strings.Split(stmts, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := db.Exec(stmt); err != nil {
				log.Printf("Failed to execute table creation statement: %v", err)
				return fmt.Errorf("failed to execute table creation statement: %v", err)
			}
		}
		// Update the db_version table with the current version
		if _, err := db.Exec("INSERT INTO db_version (version) VALUES (?)", CurrentVersion); err != nil {
			log.Printf("Failed to insert current version into db_version table: %v", err)
			return err
		}
	} else if err != nil {
		log.Printf("Error querying current database version: %v", err)
		return err
	} else {
		// Existing database detected, apply necessary migrations
		log.Printf("Current database version: %s", version)
		for version != CurrentVersion {
			migration, exists := migrations[CurrentVersion]
			if !exists {
				errMsg := fmt.Sprintf("Migration to %s not found", CurrentVersion)
				log.Println(errMsg)
				return fmt.Errorf(errMsg)
			}

			log.Printf("Applying migration to %s...", CurrentVersion)
			if _, err := db.Exec(migration); err != nil {
				log.Printf("Failed to apply migration: %v", err)
				return err
			}

			if _, err := db.Exec("UPDATE db_version SET version = ?", CurrentVersion); err != nil {
				log.Printf("Failed to update db_version to %s: %v", CurrentVersion, err)
				return err
			}

			version = CurrentVersion
			log.Printf("Migration to %s applied successfully.", CurrentVersion)
		}
	}

	log.Println("Database initialization completed successfully.")
	return nil
}

func (ks *KeyStore) Close() error {
	if ks.db != nil {
		return ks.db.Close()
	}
	return nil
}

func padTo32Bytes(data []byte) []byte {
	if len(data) >= 32 {
		return data[:32]
	}

	padded := make([]byte, 32)
	copy(padded, data)
	return padded
}

func (ks *KeyStore) GetOrGeneratePrivateKey(options NodeOptions) (crypto.PrivKey, error) {
	if len(options.RawKey) > 0 {
		var priv crypto.PrivKey
		var err error

		options.RawKey = padTo32Bytes(options.RawKey)

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

		privKeyBytes, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, err
		}

		_, err = ks.db.Exec("DELETE FROM private_keys WHERE id = 1")
		if err != nil {
			return nil, err
		}

		_, err = ks.db.Exec("INSERT INTO private_keys (id, private_key) VALUES (1, ?)", privKeyBytes)
		if err != nil {
			return nil, err
		}

		return priv, nil
	}

	var privKeyBytes []byte
	err := ks.db.QueryRow("SELECT private_key FROM private_keys WHERE id = 1").Scan(&privKeyBytes)
	if err == sql.ErrNoRows {
		priv, _, err := crypto.GenerateKeyPair(crypto.Secp256k1, 256)
		if err != nil {
			return nil, err
		}
		privKeyBytes, err = crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, err
		}

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

func (ks *KeyStore) ExportDatabase(exportPath string) error {
	if ks.db == nil {
		return fmt.Errorf("database is not initialized")
	}

	sourceFile, err := os.Open(ks.dbPath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(exportPath)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	_, err = io.Copy(destinationFile, sourceFile)
	return err
}

func (ks *KeyStore) ImportDatabase(importPath string) error {
	if ks.db != nil {
		if err := ks.db.Close(); err != nil {
			return err
		}
	}

	importedFile, err := os.Open(importPath)
	if err != nil {
		return err
	}
	defer importedFile.Close()

	destinationFile, err := os.Create(ks.dbPath)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	_, err = io.Copy(destinationFile, importedFile)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite", ks.dbPath)
	if err != nil {
		return err
	}
	ks.db = db

	return nil
}
