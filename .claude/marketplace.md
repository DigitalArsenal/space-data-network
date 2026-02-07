# OrbPro Plugin Marketplace Architecture

## Overview

The OrbPro Plugin Marketplace is a decentralized plugin distribution system integrated into Space Data Network (SDN). Publishers can sell encrypted WASM plugins, and buyers decrypt them using wallet-based keys derived from `hd-wallet-wasm`. SDN acts as the decentralized marketplace infrastructure — **everyone can run their own store** — while a smart contract enforces platform fees (15% on plugin sales).

### Design Goals

1. **Encrypt once, wrap per-buyer** — Plugin WASM is encrypted with a random DEK; only the 60-byte DEK envelope is generated per purchase (not a full re-encryption of potentially multi-MB WASM)
2. **Two protection modes** — Domain KEK (built-in OrbPro plugins, hostname-gated) and Buyer ECIES KEK (marketplace purchases, wallet-gated), or both simultaneously
3. **Decentralized distribution** — Encrypted plugin blobs live on IPFS; DEK envelopes are delivered via SDN P2P or stored on-chain
4. **Multi-curve support** — secp256k1 (default), P-256, P-384, X25519, Ed25519 via `hd-wallet-wasm`
5. **No server-side key escrow** — The buyer's private key never leaves their browser; decryption is local

---

## Cryptographic Architecture

### DEK Wrapping Pattern

```
                  Server (Publisher)                     Client (Buyer)
                  ──────────────────                     ──────────────

 1. Generate random DEK (32 bytes)
 2. AES-256-GCM(plugin_wasm, DEK) → encrypted_blob
 3. Store encrypted_blob on IPFS

 Per purchase:
 4. Generate ephemeral keypair (Eph_priv, Eph_pub)
 5. ECDH(Eph_priv, Buyer_pub) → shared_secret
 6. HKDF(shared_secret, salt, "orbpro-marketplace:{pluginId}") → wrapping_key
 7. AES-256-GCM(DEK, wrapping_key) → wrapped_DEK
 8. Envelope = { Eph_pub, wrapped_DEK, salt, curve }
 9. Wipe Eph_priv, shared_secret, wrapping_key

                                          ──────────────────────────────
                                          10. Receive Envelope + encrypted_blob
                                          11. ECDH(Buyer_priv, Eph_pub) → shared_secret
                                          12. HKDF(shared_secret, salt, info) → wrapping_key
                                          13. AES-256-GCM-decrypt(wrapped_DEK, wrapping_key) → DEK
                                          14. AES-256-GCM-decrypt(encrypted_blob, DEK) → plugin_wasm
                                          15. WebAssembly.instantiate(plugin_wasm)
                                          16. Wipe DEK from JS memory
```

### Wrapped DEK Format

Each wrapped DEK is 60 bytes (base64-encoded):
```
[IV: 12 bytes] [Ciphertext: 32 bytes] [GCM Tag: 16 bytes]
```

### Key Derivation Path

Buyer plugin keys use BIP-44 derivation via `hd-wallet-wasm`:
```
m/44'/9999'/0'/0/{pluginIndex}
```
- Coin type `9999'` = OrbPro marketplace (unregistered, for internal use)
- Account `0'` = default account
- Change `0` = external chain
- Index = per-plugin sequential index

### Dual KEK Mode

For OrbPro's own plugins distributed via marketplace, both protection modes apply:

```
encrypted_blob ← AES-256-GCM(plugin_wasm, DEK)

Domain KEK:   wrapped_DEK_domain[i]  ← AES-256-GCM(DEK, deriveKey(secret, pluginId, SHA256(domain_i)))
Buyer  KEK:   wrapped_DEK_buyer[j]   ← AES-256-GCM(DEK, HKDF(ECDH(eph, buyer_j_pub)))

Bundle = {
  encryptedData: base64(encrypted_blob),
  domainWrappedKeys: [base64(wrapped_DEK_domain_0), ...],
  buyerWrappedKeys: [{ ephemeralPub, wrappedDEK, salt, curve }, ...]
}
```

Either set of wrapped keys can unwrap the DEK independently. Domain KEK is for direct OrbPro licensees; Buyer KEK is for marketplace purchasers.

---

## SDK Functions

### Server-Side (Node.js) — `@orbpro/plugin-sdk/src/build.js`

| Function | Purpose |
|----------|---------|
| `encryptForMarketplace(data)` | Encrypt plugin with random DEK. Returns `{ encryptedData, dek }` |
| `wrapDEKForBuyer(dek, pubkeyHex, curve, pluginId)` | ECDH wrap DEK for one buyer. Returns envelope `{ ephemeralPub, wrappedDEK, salt, curve }` |
| `encryptWithDualKEK(data, secret, pluginId, domains, buyers)` | Encrypt with both domain + buyer wrapped keys |
| `encryptWithKEKWrapper(data, secret, pluginId, domains)` | Domain-only KEK wrapping (existing) |

### Browser-Side — `@orbpro/plugin-sdk/src/ecies.js`

| Function | Purpose |
|----------|---------|
| `unwrapMarketplaceDEK(envelope, buyerPrivateKey, pluginId, options)` | Unwrap DEK using buyer's private key. Supports X25519 (native), P-256/P-384 (Web Crypto), secp256k1 (via `options.ecdhFn` from hd-wallet-wasm) |
| `wrapMarketplaceDEK(dek, buyerPubkey, pluginId, options)` | Wrap DEK for buyer (browser-side, for P2P re-sharing) |
| `decryptWithDEK(encryptedPayload, dek)` | Decrypt plugin payload with unwrapped DEK |

### Plugin Loader — `@orbpro/plugins/marketplace-loader.js`

| Function | Purpose |
|----------|---------|
| `loadMarketplacePlugin(bundle, wallet, options)` | Full flow: unwrap → decrypt → instantiate WASM → return plugin interface |
| `unwrapDEK(envelope, wallet, pluginId)` | Unwrap DEK using wallet's ECDH |
| `decryptPayload(encryptedData, dek)` | Decrypt encrypted plugin data |
| `verifyPluginHash(wasmBytes, expectedHash)` | Verify SHA-256 integrity hash |
| `getPluginKeyPath(pluginIndex)` | Returns BIP-44 path `m/44'/9999'/0'/0/{index}` |

---

## Purchase Flow

### 1. Publisher Lists Plugin

```javascript
import { encryptForMarketplace } from "@orbpro/plugin-sdk";

// Encrypt once — the blob goes to IPFS
const wasmBinary = fs.readFileSync("plugin.wasm");
const { encryptedData, dek } = encryptForMarketplace(wasmBinary);

// Store encrypted blob
const cid = await ipfs.add(Buffer.from(encryptedData, "base64"));

// Store manifest + CID on SDN
await sdn.publishPlugin({
  pluginId: "com.publisher.my-plugin",
  name: "My Plugin",
  version: "1.0.0",
  cid: cid.toString(),
  price: { amount: 100, currency: "USDC" },
  wasmHash: crypto.createHash("sha256").update(wasmBinary).digest("hex"),
});

// IMPORTANT: Securely store DEK for wrapping future purchases
// Never write raw DEK to disk — use HSM or encrypted vault
vault.store(`dek:com.publisher.my-plugin:1.0.0`, dek);
```

### 2. Buyer Purchases Plugin

The SDN node handles the purchase:

```javascript
// SDN marketplace server (Go or JS)
async function handlePurchase(buyerPubkey, pluginId, txHash) {
  // 1. Verify smart contract payment (15% platform fee deducted)
  const tx = await verifyTransaction(txHash);
  if (!tx.valid) throw new Error("Payment not verified");

  // 2. Load publisher's DEK from vault
  const dek = await vault.load(`dek:${pluginId}:${version}`);

  // 3. Wrap DEK for this buyer
  const envelope = wrapDEKForBuyer(dek, buyerPubkey, "secp256k1", pluginId);

  // 4. Return envelope to buyer (the encrypted blob CID is public)
  return { envelope, cid: plugin.cid };
}
```

### 3. Buyer Loads Plugin (Browser)

```javascript
import { loadMarketplacePlugin } from "@orbpro/plugins/marketplace-loader";

// User's wallet (hd-wallet-wasm)
const wallet = await HDWallet.fromMnemonic(mnemonic);
const pluginKey = wallet.derive("m/44'/9999'/0'/0/0");

// Download encrypted blob from IPFS
const encryptedData = await fetch(`https://ipfs.io/ipfs/${cid}`).then(r => r.text());

// Load plugin
const plugin = await loadMarketplacePlugin(
  {
    pluginId: "com.publisher.my-plugin",
    encryptedData,
    envelope, // from purchase response
    manifest: { name: "My Plugin", version: "1.0.0", type: "Shader" },
  },
  {
    privateKey: pluginKey.privateKey,
    ecdh: (priv, pub) => wallet.ecdh(priv, pub, "secp256k1"),
  },
);

// Use the plugin
const result = plugin.call("compute", inputPtr);
```

---

## Smart Contract Integration

### Fee Structure

| Fee | Amount | Recipient |
|-----|--------|-----------|
| Plugin sale | 85% | Publisher |
| Platform fee | 15% | DigitalArsenal.io |

### Contract Interface

```solidity
interface IOrbProMarketplace {
    /// @notice Record a plugin purchase on-chain
    /// @param pluginId Plugin identifier hash
    /// @param buyerPubkey Buyer's compressed public key
    /// @param amount Payment amount in base units
    function purchase(
        bytes32 pluginId,
        bytes calldata buyerPubkey,
        uint256 amount
    ) external payable;

    /// @notice Verify a purchase occurred
    /// @param pluginId Plugin identifier hash
    /// @param buyerPubkey Buyer's compressed public key
    function verifyPurchase(
        bytes32 pluginId,
        bytes calldata buyerPubkey
    ) external view returns (bool);

    /// @notice Withdraw publisher earnings
    function withdraw() external;

    event PluginPurchased(
        bytes32 indexed pluginId,
        bytes buyerPubkey,
        uint256 amount,
        uint256 platformFee
    );
}
```

### Purchase Verification

Before wrapping a DEK for a buyer, the marketplace node verifies the on-chain purchase:

```go
// sdn-server/marketplace/verify.go
func VerifyPurchase(contract *OrbProMarketplace, pluginId [32]byte, buyerPubkey []byte) error {
    verified, err := contract.VerifyPurchase(nil, pluginId, buyerPubkey)
    if err != nil {
        return fmt.Errorf("contract call failed: %w", err)
    }
    if !verified {
        return ErrPurchaseNotFound
    }
    return nil
}
```

---

## EME (Encrypted Message Envelope) Integration

The DEK envelope can be serialized using the spacedatastandards.org EME FlatBuffer schema (file identifier `$EME`):

| EME Field | Marketplace Mapping |
|-----------|-------------------|
| `ENCRYPTED_BLOB` | `wrappedDEK` (base64 → bytes) |
| `EPHEMERAL_PUBLIC_KEY` | `ephemeralPub` (hex → bytes) |
| `TAG` | Included in `wrappedDEK` (last 16 bytes of GCM output) |
| `IV` | Included in `wrappedDEK` (first 12 bytes of GCM output) |
| `PUBLIC_KEY_IDENTIFIER` | `buyerPubkeyHash` (SHA-256 of buyer's pubkey, used as salt) |
| `CIPHER_SUITE` | `ECDH-secp256k1+HKDF-SHA256+AES-256-GCM` |
| `KDF_PARAMETERS` | `{ salt, info: "orbpro-marketplace:{pluginId}" }` |
| `ENCRYPTION_ALGORITHM_PARAMETERS` | AES-256-GCM parameters |

This allows DEK envelopes to be transmitted as standard SDS messages over the SDN P2P network.

---

## Security Properties

1. **Forward secrecy per-purchase** — Each DEK wrap uses a fresh ephemeral keypair. Compromising the publisher's long-term key doesn't reveal past DEKs (the ephemeral private key is wiped immediately)
2. **No key escrow** — Buyer's private key never leaves their browser. DEK unwrapping is 100% client-side
3. **Minimal per-buyer overhead** — 60 bytes per wrapped DEK vs. re-encrypting entire multi-MB WASM
4. **GCM authentication** — Both the DEK wrap and plugin encryption use authenticated encryption. Tampering is detected
5. **Domain separation** — HKDF info string includes pluginId, preventing cross-plugin DEK reuse attacks
6. **On-chain verification** — Smart contract prevents DEK wrapping without payment. Even if an attacker intercepts the protocol, they can't get a valid DEK envelope without a verified on-chain purchase
7. **WASM isolation** — For OrbPro's own domain-gated plugins, the protection-runtime WASM handles hostname extraction and key derivation internally (emscripten_run_script_string), so the key never touches JavaScript

---

## File Map

```
OrbPro/
  packages/
    plugin-sdk/
      src/
        build.js          # Server-side: encryptForMarketplace, wrapDEKForBuyer, encryptWithDualKEK
        ecies.js          # Browser-side: unwrapMarketplaceDEK, wrapMarketplaceDEK, decryptWithDEK
        plk.js            # PLK license validation (alternative to wallet flow)
      index.js            # Re-exports all SDK functions
    orbpro-plugins/
      marketplace-loader.js     # Full client-side load flow: unwrap → decrypt → instantiate
      default-suite.js          # Domain-gated plugin loading (existing)
      protection-runtime/       # WASM-based domain validation + KEK decrypt (existing)

space-data-network/
  sdn-server/
    marketplace/          # Go: purchase verification, DEK wrapping endpoint
  sdn-js/
    marketplace/          # JS: client SDK for marketplace interaction
  contracts/
    OrbProMarketplace.sol # Smart contract for purchase records + fee splitting
```
