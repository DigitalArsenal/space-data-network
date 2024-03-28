package node

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	config "github.com/DigitalArsenal/space-data-network/configs"
	cryptoUtils "github.com/DigitalArsenal/space-data-network/internal/node/crypto_utils"
	"github.com/ethereum/go-ethereum/accounts"
	ipfsConfig "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/repo"
	"github.com/ipfs/kubo/repo/fsrepo"
	libp2pCrypto "github.com/libp2p/go-libp2p/core/crypto"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/tyler-smith/go-bip39"
)

func GenerateWalletAndIPFSRepo(ctx context.Context, mnemonicInput string) (repo.Repo, *hdwallet.Wallet, accounts.Account, accounts.Account, libp2pCrypto.PrivKey, string, error) {
	var err error
	var unencryptedPrivateKey []byte
	var cfg *ipfsConfig.Config
	var ipfsConfigDir = filepath.Join(config.Conf.Datastore.Directory, "ipfs")

	mnemonic := mnemonicInput

	if len(mnemonic) == 0 {

		repo, err := fsrepo.Open(ipfsConfigDir)
		if err == nil {
			cfg, _ = repo.Config()
			pkBytes, _ := base64.StdEncoding.DecodeString(cfg.Identity.PrivKey)
			unencryptedPrivateKey = cryptoUtils.DecryptPrivateKey(pkBytes)
		}

		if len(unencryptedPrivateKey) > 0 {
			mnemonic, _ = hdwallet.NewMnemonicFromEntropy(unencryptedPrivateKey)
		} else {
			mnemonic, err = hdwallet.NewMnemonic(config.Conf.KeyConfig.EntropyLengthBits)
			if err != nil {
				return nil, nil, accounts.Account{}, accounts.Account{}, &libp2pCrypto.Secp256k1PrivateKey{}, "", err
			}
		}
	}

	unencryptedPrivateKey, _ = bip39.EntropyFromMnemonic(mnemonic)

	wallet, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, nil, accounts.Account{}, accounts.Account{}, &libp2pCrypto.Secp256k1PrivateKey{}, "", err
	}

	sPath := hdwallet.MustParseDerivationPath(config.Conf.Keys.SigningAccountDerivationPath)
	signingAccount, err := wallet.Derive(sPath, true)
	if err != nil {
		return nil, nil, accounts.Account{}, accounts.Account{}, &libp2pCrypto.Secp256k1PrivateKey{}, "", err
	}

	ePath := hdwallet.MustParseDerivationPath(config.Conf.Keys.EncryptionAccountDerivationPath)
	encryptionAccount, err := wallet.Derive(ePath, true)
	if err != nil {
		return nil, nil, accounts.Account{}, accounts.Account{}, &libp2pCrypto.Secp256k1PrivateKey{}, "", err
	}

	signingPrivKey, err := wallet.PrivateKeyBytes(signingAccount)
	if err != nil {
		return nil, nil, accounts.Account{}, accounts.Account{}, &libp2pCrypto.Secp256k1PrivateKey{}, "", err
	}

	libp2pPrivKey, err := libp2pCrypto.UnmarshalSecp256k1PrivateKey(signingPrivKey)
	if err != nil {
		return nil, nil, accounts.Account{}, accounts.Account{}, &libp2pCrypto.Secp256k1PrivateKey{}, "", err
	}

	// Encrypt the private key bytes for storage
	encryptedPrivKey := cryptoUtils.EncryptPrivateKey(unencryptedPrivateKey)

	// Convert encrypted private key to base64 for easier storage and handling
	encPrivKeyBase64 := base64.StdEncoding.EncodeToString([]byte(encryptedPrivKey))

	ipfsRepo, err := loadOrCreateIPFSRepo(ctx, encPrivKeyBase64)
	if err != nil {
		return nil, nil, accounts.Account{}, accounts.Account{}, libp2pPrivKey, encPrivKeyBase64, err
	}

	return ipfsRepo, wallet, signingAccount, encryptionAccount, libp2pPrivKey, encPrivKeyBase64, err
}

func loadOrCreateIPFSRepo(_ context.Context, privKeyBase64 string) (repo.Repo, error) {
	var ipfsConfigDir = filepath.Join(config.Conf.Datastore.Directory, "ipfs")

	if _, err := os.Stat(filepath.Join(ipfsConfigDir, "config")); os.IsNotExist(err) {

		newCfg := &ipfsConfig.Config{
			Identity: ipfsConfig.Identity{
				PrivKey: privKeyBase64,
			},
			Datastore: DatastoreConfig,
		}

		if err := os.MkdirAll(ipfsConfigDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create IPFS config directory: %w", err)
		}

		if err := fsrepo.Init(ipfsConfigDir, newCfg); err != nil {
			return nil, fmt.Errorf("failed to initialize IPFS fsrepo: %w", err)
		}
	}

	repo, err := fsrepo.Open(ipfsConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open fsrepo: %w", err)
	}

	return repo, nil
}
