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
	"github.com/ethereum/go-ethereum/accounts"
	_ "github.com/glebarez/go-sqlite"
	"github.com/libp2p/go-libp2p/core/crypto"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
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
		"v1.0": `CREATE TABLE IF NOT EXISTS mnemonics (id INTEGER PRIMARY KEY, mnemonic TEXT NOT NULL);
                 CREATE TABLE IF NOT EXISTS EPM (id INTEGER PRIMARY KEY AUTOINCREMENT, EPM_DATA BLOB NOT NULL);`,
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

/* TODO ingest keys
func padTo32Bytes(data []byte) []byte {
	if len(data) >= 32 {
		return data[:32]
	}

	padded := make([]byte, 32)
	copy(padded, data)
	return padded
}*/

func (ks *KeyStore) GetOrGeneratePrivateKey() (*hdwallet.Wallet, accounts.Account, accounts.Account, crypto.PrivKey, error) {
	var mnemonic string
	err := ks.db.QueryRow("SELECT mnemonic FROM mnemonics WHERE id = 1").Scan(&mnemonic)
	isNewMnemonic := false

	if err == sql.ErrNoRows {
		// Generate a new mnemonic
		mnemonic, err = hdwallet.NewMnemonic(config.Conf.KeyConfig.EntropyLengthBits) // Adjust entropy if needed
		if err != nil {
			return nil, accounts.Account{}, accounts.Account{}, nil, err
		}
		isNewMnemonic = true
	} else if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, nil, err
	}

	// TODO: Encrypt mnemonic before saving to database

	// If it's a new mnemonic, store it in the database
	if isNewMnemonic {
		_, err = ks.db.Exec("INSERT INTO mnemonics (id, mnemonic) VALUES (1, ?)", mnemonic)
		if err != nil {
			return nil, accounts.Account{}, accounts.Account{}, nil, err
		}
	}

	// Create a new HD Wallet from the mnemonic
	wallet, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, nil, err
	}

	// Derive the account using the specified derivation path from the configuration
	sPath := hdwallet.MustParseDerivationPath(config.Conf.Keys.SigningAccountDerivationPath)
	signingAccount, err := wallet.Derive(sPath, true)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, nil, err
	}

	// Derive the account using the specified derivation path from the configuration
	ePath := hdwallet.MustParseDerivationPath(config.Conf.Keys.EncryptionAccountDerivationPath)
	encryptionAccount, err := wallet.Derive(ePath, true)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, nil, err
	}

	// Extract the private key for the derived account
	privKey, err := wallet.PrivateKey(signingAccount)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, nil, err
	}

	// Convert the ECDSA private key to libp2p's format
	libp2pPrivKey, err := crypto.UnmarshalSecp256k1PrivateKey(privKey.D.Bytes())
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, nil, err
	}

	return wallet, signingAccount, encryptionAccount, libp2pPrivKey, nil
}

func (ks *KeyStore) SaveEPM(epmData []byte) error {
	_, err := ks.db.Exec("INSERT OR REPLACE INTO EPM (id, epm_data) VALUES (0, ?)", epmData)
	return err
}

func (ks *KeyStore) LoadEPM() []byte {
	var epmData []byte
	err := ks.db.QueryRow("SELECT epm_data FROM EPM WHERE id = 0").Scan(&epmData)
	if err != nil {
		panic(err)
	}
	return epmData
}

func (ks *KeyStore) SavePNM(pnmData []byte, signature []byte) error {
	_, err := ks.db.Exec("INSERT OR REPLACE INTO PNM (id, pnm_data, signature) VALUES (0, ?, ?)", pnmData, signature)
	return err
}

func (ks *KeyStore) LoadPNM() ([]byte, []byte, error) {
	var pnmData []byte
	var signature []byte
	err := ks.db.QueryRow("SELECT pnm_data, signature FROM PNM WHERE id = 0").Scan(&pnmData, &signature)
	if err != nil {
		return nil, nil, err
	}
	return pnmData, signature, nil
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
