package node

import (
	"fmt"

	config "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/ethereum/go-ethereum/accounts"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/tyler-smith/go-bip39"
)

func (n *Node) SetHDWallet() error {
	privKey, err := n.PrivateKey()
	if err != nil {
		return err
	}

	rawPrivateKeyBytes, err := privKey.Raw()
	if err != nil {
		return fmt.Errorf("failed to get raw private key from node: %v", err)
	}

	if len(rawPrivateKeyBytes) < config.Conf.Key.EntropyLength {
		return fmt.Errorf("not enough bytes in private key for the specified entropy length")
	}

	mnemonic, err := bip39.NewMnemonic(rawPrivateKeyBytes[:config.Conf.Key.EntropyLength])
	if err != nil {
		return fmt.Errorf("failed to generate mnemonic from raw key: %v", err)
	}

	wallet, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return fmt.Errorf("failed to create HD wallet from mnemonic: %v", err)
	}

	n.wallet = wallet
	account, _ := n.GetAccount(config.Conf.Datastore.EthereumDerivationPath)

	// Get the address of the derived account
	address := account.Address
	// Print the Ethereum address
	fmt.Printf("Node Ethereum Address: %s\n", address.Hex())

	return nil
}

func (n *Node) GetAccount(dPath string) (accounts.Account, error) {
	path := hdwallet.MustParseDerivationPath(dPath)

	// Derive the first account using the path
	account, err := n.wallet.Derive(path, true)
	if err != nil {
		return accounts.Account{}, fmt.Errorf("failed to derive account: %s %v", dPath, err)
	}
	return account, nil
}

func (n *Node) ExportMnemonic() (string, error) {
	privKey, err := n.PrivateKey()
	if err != nil {
		return "", fmt.Errorf("failed to get private key: %v", err)
	}

	rawPrivateKeyBytes, err := privKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to extract raw private key bytes: %v", err)
	}

	// Ensure the length of rawPrivateKeyBytes is sufficient for mnemonic generation
	if len(rawPrivateKeyBytes) < 16 {
		return "", fmt.Errorf("private key bytes insufficient for mnemonic generation")
	}

	// Use the appropriate number of bytes from the raw private key for entropy
	entropy := rawPrivateKeyBytes[:config.Conf.Key.EntropyLength]

	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", fmt.Errorf("failed to generate mnemonic: %v", err)
	}

	return mnemonic, nil
}
