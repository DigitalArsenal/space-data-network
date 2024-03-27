package node

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	config "github.com/DigitalArsenal/space-data-network/configs"
	cryptoUtils "github.com/DigitalArsenal/space-data-network/internal/node/crypto_utils"
	ipfsConfig "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p/core/crypto"
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

var datastoreConfig = ipfsConfig.Datastore{
	StorageMax:         "10GB",
	StorageGCWatermark: 90,
	GCPeriod:           "1h", // Example, set according to your needs
	Spec:               datastoreSpec,
	HashOnRead:         false, // Default setting
	BloomFilterSize:    0,     // Default setting
}

var (
	pluginsLoaded sync.Once // Declared at the package level
)

func LoadOrCreateIPFSRepo(ctx context.Context) (repo.Repo, crypto.PrivKey, string, error) {

	pluginsLoaded.Do(func() {
		plugins, err := loader.NewPluginLoader(filepath.Join("", "plugins"))
		if err != nil {
			fmt.Printf("error loading plugins: %s\n", err)
			return
		}

		// Load preloaded and external plugins
		if err := plugins.Initialize(); err != nil {
			fmt.Printf("error initializing plugins: %s\n", err)
			return
		}

		if err := plugins.Inject(); err != nil {
			fmt.Printf("error initializing plugins: %s\n", err)
			return
		}
	})

	ipfsConfigDir := filepath.Join(config.Conf.Datastore.Directory, "ipfs")

	// Check if IPFS config directory already exists
	if _, err := os.Stat(filepath.Join(ipfsConfigDir, "config")); os.IsNotExist(err) {
		// IPFS config does not exist, so generate and initialize a new repo

		privKey, _, err := crypto.GenerateKeyPair(crypto.Secp256k1, 256)
		if err != nil {
			return nil, nil, "", fmt.Errorf("failed to generate private key: %w", err)
		}

		privKeyBytes, err := crypto.MarshalPrivateKey(privKey)
		if err != nil {
			return nil, nil, "", fmt.Errorf("failed to marshal private key: %w", err)
		}

		encryptedPrivKey := cryptoUtils.EncryptPrivateKey(privKeyBytes)

		privKeyBase64 := base64.StdEncoding.EncodeToString([]byte(encryptedPrivKey))

		newCfg := &ipfsConfig.Config{
			Identity: ipfsConfig.Identity{
				PrivKey: privKeyBase64,
			},
			Datastore: datastoreConfig,
			Ipns: ipfsConfig.Ipns{
				UsePubsub: ipfsConfig.True,
			},
		}

		if err := os.MkdirAll(ipfsConfigDir, 0700); err != nil {
			return nil, nil, "", fmt.Errorf("failed to create IPFS config directory: %w", err)
		}

		if err := fsrepo.Init(ipfsConfigDir, newCfg); err != nil {
			return nil, nil, "", fmt.Errorf("failed to initialize IPFS fsrepo: %w", err)
		}
	}

	// Open the existing repo
	repo, err := fsrepo.Open(ipfsConfigDir)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to open fsrepo: %w", err)
	}

	cfg, err := repo.Config()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to read IPFS config: %w", err)
	}

	// Decrypt the private key for in-memory use, keeping the encrypted version on disk
	encryptedPrivKeyBytes, err := base64.StdEncoding.DecodeString(cfg.Identity.PrivKey)

	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to decode private key: %w", err)
	}

	decryptedPrivKeyBytes := cryptoUtils.DecryptPrivateKey(encryptedPrivKeyBytes)
	privKey, err := crypto.UnmarshalPrivateKey(decryptedPrivKeyBytes)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to unmarshal decrypted private key: %w", err)
	}

	// Update the in-memory cfg with the unencrypted private key
	privKeyBytes, err := crypto.MarshalPrivateKey(privKey)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to marshal private key: %w", err)
	}
	cfg.Identity.PrivKey = base64.StdEncoding.EncodeToString(privKeyBytes) /**/

	// Writing back the in-memory updated configuration to repo is optional and based on use case
	// If required, use `repo.SetConfig(cfg)` followed by `repo.Close()`, then `fsrepo.Open(ipfsConfigDir)` again

	return repo, privKey, base64.StdEncoding.EncodeToString([]byte(encryptedPrivKeyBytes)), nil
}
