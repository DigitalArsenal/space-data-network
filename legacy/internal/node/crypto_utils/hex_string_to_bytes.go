package crypto_utils

import (
	"encoding/hex"
	"strings"
)

func HexStringToBytes(hexStr string) ([]byte, error) {
	// Remove the "0x" prefix if it exists
	cleanedHexStr := strings.TrimPrefix(hexStr, "0x")

	// Decode the hex string into bytes
	bytes, err := hex.DecodeString(cleanedHexStr)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}
