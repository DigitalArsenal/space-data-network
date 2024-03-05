package node

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

// The expected mnemonic for an entropy of 128 zero bits.
var expectedMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// The expected Ethereum address for the given mnemonic.
var expectedAddress = "0x9858EfFD232B4033E47d90003D41EC34EcaEda94"

func TestSetHDWalletAndExportMnemonic(t *testing.T) {

	err := testNode.Start(ctx)
	require.NoError(t, err)

	defer testNode.Stop() // Ensure we clean up resources after test

	// Testing SetHDWallet with the zero entropy.
	err = testNode.SetHDWallet()
	require.NoError(t, err)
	require.NotNil(t, testNode.wallet)

	// Testing ExportMnemonic to see if it matches the expected mnemonic.
	mnemonic, err := testNode.ExportMnemonic()
	require.NoError(t, err)
	require.Equal(t, expectedMnemonic, mnemonic)

	// Derive the first account and check the Ethereum address.
	path := hdwallet.MustParseDerivationPath("m/44'/60'/0'/0/0")
	account, err := testNode.wallet.Derive(path, false)
	require.NoError(t, err)
	address := account.Address.Hex()
	require.Equal(t, expectedAddress, address)
}

func TestSetHDWalletWithRandomKey(t *testing.T) {
	// Generate a random 128-bit (16-byte) key
	randomKey := make([]byte, 16)
	require.NoError(t, err)

	// Set a random bit in the randomKey
	bitPos, err := rand.Int(rand.Reader, big.NewInt(128))
	require.NoError(t, err)
	byteIndex := bitPos.Int64() / 8
	bitIndex := bitPos.Int64() % 8
	randomKey[byteIndex] |= 1 << bitIndex

	// Initialize a new test node with the random key
	randomTestNode, err := NewNode(ctx, NodeOptions{RawKey: randomKey})
	require.NoError(t, err)

	err = randomTestNode.Start(ctx)
	require.NoError(t, err)

	defer randomTestNode.Stop() // Ensure we clean up resources after test

	// Set HD Wallet with the random key
	err = randomTestNode.SetHDWallet()
	require.NoError(t, err)
	require.NotNil(t, randomTestNode.wallet)

	// Export Mnemonic from the random test node
	mnemonic, err := randomTestNode.ExportMnemonic()
	require.NoError(t, err)
	fmt.Println(mnemonic)

	// Derive the first account and get the Ethereum address
	path := hdwallet.MustParseDerivationPath("m/44'/60'/0'/0/0")
	account, err := randomTestNode.wallet.Derive(path, false)
	require.NoError(t, err)
	address := account.Address.Hex()

	// Check that the mnemonic and address are not what we expect
	require.NotEqual(t, expectedMnemonic, mnemonic, "Mnemonic should not match the expected mnemonic for zero key")
	require.NotEqual(t, expectedAddress, address, "Ethereum address should not match the expected address for zero key")
}
