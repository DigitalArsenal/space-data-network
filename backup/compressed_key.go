
func getCompressedPublicKeyHex(wallet *hdwallet.Wallet, account accounts.Account) (string, error) {
	// Retrieve the public key in hex format
	pubKeyHex, err := wallet.PublicKeyHex(account)
	if err != nil {
		return "", err
	}

	// Decode the hex string to bytes
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return "", fmt.Errorf("failed to decode hex string: %v", err)
	}

	// Prepend the 0x04 prefix for uncompressed public keys
	pubKeyBytes = append([]byte{0x04}, pubKeyBytes...)

	fmt.Println(pubKeyBytes)
	// Parse the public key using btcec
	pubKey, err := btcec.ParsePubKey(pubKeyBytes, btcec.S256())
	if err != nil {
		return "", fmt.Errorf("failed to parse public key: %v", err)
	}

	// Serialize the public key in compressed format
	compressedPubKey := pubKey.SerializeCompressed()

	// Encode to hex and return
	return hex.EncodeToString(compressedPubKey), nil
}