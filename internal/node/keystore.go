package node

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/syndtr/goleveldb/leveldb"
	"golang.org/x/crypto/argon2"
)

const KeyDirName = ".spacedatanetwork"

type KeyStore struct {
	db *leveldb.DB
}

func NewKeyStore() (*KeyStore, error) {
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

	dbPath := filepath.Join(keyDir, "keys.db")
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		return nil, err
	}

	return &KeyStore{db: db}, nil
}

func (ks *KeyStore) GetPrivateKey(pass string) (crypto.PrivKey, error) {
	privKeyData, err := ks.db.Get([]byte("privateKey"), nil)
	if err == leveldb.ErrNotFound {
		// Generate a new secp256k1 key
		priv, _, err := crypto.GenerateKeyPair(crypto.Secp256k1, 256)
		if err != nil {
			return nil, err
		}
		privKeyData, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, err
		}
		err = ks.db.Put([]byte("privateKey"), privKeyData, nil)
		if err != nil {
			return nil, err
		}
		return priv, nil
	} else if err != nil {
		return nil, err
	}

	return crypto.UnmarshalPrivateKey(privKeyData)
}

func generatePassword() string {
	hostname, _ := os.Hostname()
	input := fmt.Sprintf("%s:%s", os.Getenv("HOME"), hostname)
	return hex.EncodeToString(argon2.IDKey([]byte(input), []byte("some_salt"), 1, 64*1024, 4, 32))
}
