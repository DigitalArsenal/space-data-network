package node

import (
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

func isWASMBinary(data []byte) bool {
	return len(data) >= len(wasmMagic) &&
		data[0] == wasmMagic[0] &&
		data[1] == wasmMagic[1] &&
		data[2] == wasmMagic[2] &&
		data[3] == wasmMagic[3]
}

// loadKeyBrokerWASMBytes loads an OrbPro key-broker plugin from disk.
// Supports either raw WASM bytes or encrypted JSON envelope artifacts.
func (n *Node) loadKeyBrokerWASMBytes(wasmPath string) ([]byte, bool, error) {
	artifactBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, false, fmt.Errorf("read key broker artifact: %w", err)
	}

	if isWASMBinary(artifactBytes) {
		return artifactBytes, false, nil
	}

	recipientKey, err := n.findPluginDecryptPrivateKey()
	if err != nil {
		return nil, false, fmt.Errorf("resolve key broker decryption key: %w", err)
	}
	if len(recipientKey) == 0 {
		return nil, false, fmt.Errorf("key broker artifact %q is encrypted, but no decryption key is configured", wasmPath)
	}

	decryptedBytes, err := license.DecryptStagedArtifactEnvelope(artifactBytes, recipientKey)
	if err != nil {
		return nil, false, fmt.Errorf("decrypt key broker artifact: %w", err)
	}
	if !isWASMBinary(decryptedBytes) {
		return nil, false, fmt.Errorf("decrypted key broker artifact is not valid WASM")
	}

	return decryptedBytes, true, nil
}
