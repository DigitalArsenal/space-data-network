package node

import (
	config "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/ethereum/go-ethereum/accounts"
	libp2pCrypto "github.com/libp2p/go-libp2p/core/crypto"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

func GenerateWallets(privKey libp2pCrypto.PrivKey) (*hdwallet.Wallet, accounts.Account, accounts.Account, error) {

	entropy, _ := privKey.Raw()
	mnemonic, _ := hdwallet.NewMnemonicFromEntropy(entropy)
	// Create a new HD Wallet from the mnemonic
	wallet, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, err
	}

	// Derive the account using the specified derivation path from the configuration
	sPath := hdwallet.MustParseDerivationPath(config.Conf.Keys.SigningAccountDerivationPath)
	signingAccount, err := wallet.Derive(sPath, true)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, err
	}

	// Derive the account using the specified derivation path from the configuration
	ePath := hdwallet.MustParseDerivationPath(config.Conf.Keys.EncryptionAccountDerivationPath)
	encryptionAccount, err := wallet.Derive(ePath, true)
	if err != nil {
		return nil, accounts.Account{}, accounts.Account{}, err
	}

	return wallet, signingAccount, encryptionAccount, nil
}
