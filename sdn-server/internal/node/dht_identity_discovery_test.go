package node

import (
	"encoding/hex"
	"testing"
)

func TestComputeModuleDeliveryDiscoveryCID(t *testing.T) {
	t.Parallel()

	pubKey, err := hex.DecodeString("021111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("hex.DecodeString failed: %v", err)
	}

	discoveryCID, err := computeModuleDeliveryDiscoveryCID(pubKey)
	if err != nil {
		t.Fatalf("computeModuleDeliveryDiscoveryCID failed: %v", err)
	}

	const expected = "bafkreicfbpehrnn2ynqbs7tf7cu4rj57tsw222kkna33ytyfaz6v37oniu"
	if got := discoveryCID.String(); got != expected {
		t.Fatalf("discovery CID = %q, want %q", got, expected)
	}
}

func TestComputeModuleDeliveryDiscoveryCIDRejectsNonCompressedKey(t *testing.T) {
	t.Parallel()

	if _, err := computeModuleDeliveryDiscoveryCID(make([]byte, 32)); err == nil {
		t.Fatal("expected error for non-compressed provider key")
	}
}
