package epm

import (
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

// DirectoryRecordJSONFromEPM normalizes a signed EPM into the directory JSON
// shape used by FlatSQL directory records.
func DirectoryRecordJSONFromEPM(epmBytes []byte, fallbackPeerID string) (map[string]any, error) {
	if len(epmBytes) == 0 {
		return nil, ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, ErrInvalidEPMData
	}

	epmRecord := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	kind := "user"
	if epmRecord.ENTITY_TYPE() == EPM.EntityTypeNode {
		kind = "node"
	}
	peerID := strings.TrimSpace(fallbackPeerID)
	if peerID == "" {
		peerID = peerIDFromEPM(epmRecord)
	}
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required for EPM directory record")
	}

	info := map[string]any{
		"directory_kind": kind,
		"entity_type":    kind,
		"peer_id":        peerID,
	}

	addLowerString(info, "dn", epmRecord.DN())
	addLowerString(info, "legal_name", epmRecord.LEGAL_NAME())
	addLowerString(info, "family_name", epmRecord.FAMILY_NAME())
	addLowerString(info, "given_name", epmRecord.GIVEN_NAME())
	addLowerString(info, "additional_name", epmRecord.ADDITIONAL_NAME())
	addLowerString(info, "honorific_prefix", epmRecord.HONORIFIC_PREFIX())
	addLowerString(info, "honorific_suffix", epmRecord.HONORIFIC_SUFFIX())
	addLowerString(info, "job_title", epmRecord.JOB_TITLE())
	addLowerString(info, "occupation", epmRecord.OCCUPATION())
	addLowerString(info, "email", epmRecord.EMAIL())
	addLowerString(info, "telephone", epmRecord.TELEPHONE())

	key := new(EPM.CryptoKey)
	if n := epmRecord.KEYSLength(); n > 0 {
		keys := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			if !epmRecord.KEYS(key, i) {
				continue
			}
			entry := make(map[string]any)
			addLowerString(entry, "public_key", key.PUBLIC_KEY())
			addLowerString(entry, "xpub", key.XPUB())
			addLowerString(entry, "key_address", key.KEY_ADDRESS())
			addLowerString(entry, "address_type", key.ADDRESS_TYPE())
			switch key.KEY_TYPE() {
			case EPM.KeyTypeSigning:
				entry["key_type"] = "signing"
			case EPM.KeyTypeEncryption:
				entry["key_type"] = "encryption"
			}
			if len(entry) > 0 {
				keys = append(keys, entry)
			}
		}
		if len(keys) > 0 {
			info["keys"] = keys
		}
	}

	proof := new(EPM.ChainProof)
	if n := epmRecord.CHAIN_PROOFSLength(); n > 0 {
		proofs := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			if !epmRecord.CHAIN_PROOFS(proof, i) {
				continue
			}
			entry := make(map[string]any)
			chain := strings.ToLower(strings.TrimSpace(string(proof.CHAIN())))
			if chain != "" {
				entry["chain"] = chain
			}
			address := strings.TrimSpace(string(proof.ADDRESS()))
			if address != "" {
				entry["address"] = address
				switch chain {
				case "bitcoin":
					info["bitcoin_address"] = address
					addLowerString(info, "bitcoin_key_path", proof.KEY_PATH())
				case "ethereum":
					info["ethereum_address"] = address
					addLowerString(info, "ethereum_key_path", proof.KEY_PATH())
				case "solana":
					info["solana_address"] = address
					addLowerString(info, "solana_key_path", proof.KEY_PATH())
				}
			}
			addLowerString(entry, "public_key", proof.PUBLIC_KEY())
			addLowerString(entry, "key_path", proof.KEY_PATH())
			addLowerString(entry, "signature", proof.SIGNATURE())
			addLowerString(entry, "signed_payload", proof.SIGNED_PAYLOAD())
			addLowerString(entry, "algorithm", proof.ALGORITHM())
			addLowerString(entry, "encoding", proof.ENCODING())
			if len(entry) > 0 {
				proofs = append(proofs, entry)
			}
		}
		if len(proofs) > 0 {
			info["chain_proofs"] = proofs
		}
	}

	if n := epmRecord.MULTIFORMAT_ADDRESSLength(); n > 0 {
		addrs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if value := strings.TrimSpace(string(epmRecord.MULTIFORMAT_ADDRESS(i))); value != "" {
				addrs = append(addrs, value)
			}
		}
		if len(addrs) > 0 {
			info["multiformat_address"] = addrs
		}
	}

	return info, nil
}

// PeerIDFromEPM returns the first peer identity advertised in the EPM
// multiformat addresses.
func PeerIDFromEPM(epmBytes []byte) (string, error) {
	if len(epmBytes) == 0 {
		return "", ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return "", ErrInvalidEPMData
	}
	return peerIDFromEPM(EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)), nil
}

func peerIDFromEPM(epmRecord *EPM.EPM) string {
	for i := 0; i < epmRecord.MULTIFORMAT_ADDRESSLength(); i++ {
		addr := strings.TrimSpace(string(epmRecord.MULTIFORMAT_ADDRESS(i)))
		for _, marker := range []string{"/ipns/", "/p2p/"} {
			if idx := strings.Index(addr, marker); idx >= 0 {
				peerID := strings.TrimSpace(addr[idx+len(marker):])
				if slash := strings.Index(peerID, "/"); slash >= 0 {
					peerID = peerID[:slash]
				}
				if peerID != "" {
					return peerID
				}
			}
		}
	}
	return ""
}

func addLowerString(target map[string]any, key string, value []byte) {
	if trimmed := strings.TrimSpace(string(value)); trimmed != "" {
		target[key] = trimmed
	}
}
