/**
 * Space Data Network - Crypto Wallet & Identity System
 *
 * Features:
 * - HD Wallet (BIP39/BIP32/BIP44/SLIP10)
 * - Multi-chain address derivation (BTC, ETH, SOL, SUI, Cosmos)
 * - ECIES encryption (X25519, secp256k1, P-256)
 * - PIN/Passkey wallet storage
 * - vCard generation with embedded cryptographic keys
 * - Adversarial security with blockchain balance verification
 */

// =============================================================================
// Imports - Using CDN ESM modules
// =============================================================================

import * as bip39 from 'https://esm.sh/bip39@3.1.0';
import { HDKey } from 'https://esm.sh/@scure/bip32@1.3.3';
import { x25519 } from 'https://esm.sh/@noble/curves@1.3.0/ed25519';
import { ed25519 } from 'https://esm.sh/@noble/curves@1.3.0/ed25519';
import { secp256k1 } from 'https://esm.sh/@noble/curves@1.3.0/secp256k1';
import { p256 } from 'https://esm.sh/@noble/curves@1.3.0/p256';
import { sha256 } from 'https://esm.sh/@noble/hashes@1.3.3/sha256';
import { sha512 } from 'https://esm.sh/@noble/hashes@1.3.3/sha512';
import { keccak_256 } from 'https://esm.sh/@noble/hashes@1.3.3/sha3';
import { ripemd160 } from 'https://esm.sh/@noble/hashes@1.3.3/ripemd160';
import { hkdf } from 'https://esm.sh/@noble/hashes@1.3.3/hkdf';
import { pbkdf2 } from 'https://esm.sh/@noble/hashes@1.3.3/pbkdf2';
import { base58, base58check } from 'https://esm.sh/@scure/base@1.1.5';
import { bech32 } from 'https://esm.sh/@scure/base@1.1.5';
import { createV3 } from 'https://esm.sh/vcard-cryptoperson@1.1.10';
import QRCode from 'https://esm.sh/qrcode@1.5.3';

// Make Buffer available globally for bip39
if (typeof window !== 'undefined' && !window.Buffer) {
  const { Buffer } = await import('https://esm.sh/buffer@6.0.3');
  window.Buffer = Buffer;
}

// =============================================================================
// State
// =============================================================================

const state = {
  initialized: false,
  loggedIn: false,
  loginMethod: null, // 'password' | 'seed' | 'stored'

  // Wallet keys
  wallet: {
    x25519: null,
    ed25519: null,
    secp256k1: null,
    p256: null,
  },

  // HD wallet
  masterSeed: null,
  hdRoot: null,

  // Derived addresses
  addresses: {
    btc: null,
    eth: null,
    sol: null,
    sui: null,
    atom: null,
    ada: null,
  },

  // Encryption keys
  encryptionKey: null,
  encryptionIV: null,

  // PKI state
  pki: {
    alice: null,
    bob: null,
    algorithm: 'x25519',
    plaintext: null,
    ciphertext: null,
    header: null,
  },

  // vCard photo (base64 data URI)
  vcardPhoto: null,

  // Mnemonic (seed phrase)
  mnemonic: null,
};

// =============================================================================
// Crypto Configuration
// =============================================================================

const cryptoConfig = {
  btc: {
    name: 'Bitcoin',
    symbol: 'BTC',
    coinType: 0,
    path: "m/44'/0'/0'/0/0",
    explorer: 'https://blockstream.info/address/',
    balanceApi: 'https://blockstream.info/api/address/',
    formatBalance: (satoshis) => `${(satoshis / 100000000).toFixed(8)} BTC`,
  },
  eth: {
    name: 'Ethereum',
    symbol: 'ETH',
    coinType: 60,
    path: "m/44'/60'/0'/0/0",
    explorer: 'https://etherscan.io/address/',
    balanceApi: null,
    formatBalance: (wei) => `${(parseFloat(wei) / 1e18).toFixed(6)} ETH`,
  },
  sol: {
    name: 'Solana',
    symbol: 'SOL',
    coinType: 501,
    path: "m/44'/501'/0'/0'",
    explorer: 'https://solscan.io/account/',
    balanceApi: null,
    formatBalance: (lamports) => `${(lamports / 1e9).toFixed(4)} SOL`,
  },
  sui: {
    name: 'SUI',
    symbol: 'SUI',
    coinType: 784,
    path: "m/44'/784'/0'/0'/0'",
    explorer: 'https://suiscan.xyz/mainnet/address/',
    balanceApi: null,
    formatBalance: (mist) => `${(mist / 1e9).toFixed(4)} SUI`,
  },
  atom: {
    name: 'Cosmos',
    symbol: 'ATOM',
    coinType: 118,
    path: "m/44'/118'/0'/0/0",
    explorer: 'https://www.mintscan.io/cosmos/address/',
    balanceApi: null,
    formatBalance: (uatom) => `${(uatom / 1e6).toFixed(6)} ATOM`,
  },
  ada: {
    name: 'Cardano',
    symbol: 'ADA',
    coinType: 1815,
    path: "m/1852'/1815'/0'/0/0",
    explorer: 'https://cardanoscan.io/address/',
    balanceApi: null,
    formatBalance: (lovelace) => `${(lovelace / 1e6).toFixed(6)} ADA`,
  },
};

// =============================================================================
// Utility Functions
// =============================================================================

function $(id) {
  return document.getElementById(id);
}

function toHex(bytes) {
  return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
}

function fromHex(hex) {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(hex.substr(i * 2, 2), 16);
  }
  return bytes;
}

function truncateAddress(address, start = 8, end = 6) {
  if (!address || address.length <= start + end) return address;
  return `${address.slice(0, start)}...${address.slice(-end)}`;
}

// Alias for toHex (used in vCard generation)
function bytesToHex(bytes) {
  return toHex(bytes);
}

// Show toast notification
function showToast(message, duration = 2000) {
  let toast = document.querySelector('.toast');
  if (!toast) {
    toast = document.createElement('div');
    toast.className = 'toast';
    document.body.appendChild(toast);
  }
  toast.textContent = message;
  toast.classList.add('show');
  setTimeout(() => toast.classList.remove('show'), duration);
}

// =============================================================================
// Entropy Calculation
// =============================================================================

function calculateEntropy(password) {
  if (!password) return 0;

  let charsetSize = 0;
  if (/[a-z]/.test(password)) charsetSize += 26;
  if (/[A-Z]/.test(password)) charsetSize += 26;
  if (/[0-9]/.test(password)) charsetSize += 10;
  if (/[!@#$%^&*()_+\-=\[\]{};':"\\|,.<>\/?`~]/.test(password)) charsetSize += 32;
  if (/\s/.test(password)) charsetSize += 1;
  if (/[^\x00-\x7F]/.test(password)) charsetSize += 100;

  if (charsetSize === 0) return 0;
  return Math.round(password.length * Math.log2(charsetSize));
}

function updatePasswordStrength(password) {
  const entropy = calculateEntropy(password);
  const fill = $('strength-fill');
  const bits = $('entropy-bits');
  const btn = $('derive-from-password');

  if (bits) bits.textContent = entropy;
  if (fill) fill.className = 'entropy-fill';

  let strength, percentage;

  if (entropy < 28) {
    strength = 'weak';
    percentage = Math.min(25, (entropy / 28) * 25);
  } else if (entropy < 60) {
    strength = 'fair';
    percentage = 25 + ((entropy - 28) / 32) * 25;
  } else if (entropy < 128) {
    strength = 'good';
    percentage = 50 + ((entropy - 60) / 68) * 25;
  } else {
    strength = 'strong';
    percentage = 75 + Math.min(25, ((entropy - 128) / 128) * 25);
  }

  if (fill) {
    fill.classList.add(strength);
    fill.style.width = `${percentage}%`;
  }

  const username = $('wallet-username')?.value;
  if (btn) btn.disabled = !username || password.length < 24;
}

// =============================================================================
// Address Generation
// =============================================================================

// Bitcoin P2PKH address
function generateBtcAddress(publicKey) {
  const hash160 = ripemd160(sha256(publicKey));
  const versioned = new Uint8Array([0x00, ...hash160]);
  const checksum = sha256(sha256(versioned)).slice(0, 4);
  return base58.encode(new Uint8Array([...versioned, ...checksum]));
}

// Ethereum address
function generateEthAddress(publicKey) {
  // Decompress if needed
  let uncompressed;
  if (publicKey.length === 33) {
    const point = secp256k1.ProjectivePoint.fromHex(publicKey);
    uncompressed = point.toRawBytes(false);
  } else {
    uncompressed = publicKey;
  }
  // Keccak256 of public key (without 04 prefix), take last 20 bytes
  const hash = keccak_256(uncompressed.slice(1));
  return '0x' + toHex(hash.slice(-20));
}

// Solana address (Ed25519 public key as base58)
function generateSolAddress(publicKey) {
  return base58.encode(publicKey);
}

// SUI address
function deriveSuiAddress(publicKey, scheme = 0x00) {
  // SUI address = BLAKE2b-256(scheme || publicKey)[0:32]
  const data = new Uint8Array([scheme, ...publicKey]);
  const hash = sha256(data); // Using SHA256 as approximation
  return '0x' + toHex(hash);
}

// Cosmos address (Bech32)
function generateCosmosAddress(publicKey, prefix = 'cosmos') {
  const hash160 = ripemd160(sha256(publicKey));
  return bech32.encode(prefix, bech32.toWords(hash160));
}

// Generate all addresses from wallet keys
function generateAddresses() {
  if (!state.hdRoot) return;

  const addresses = {};

  // Bitcoin - secp256k1 from HD path
  try {
    const btcKey = state.hdRoot.derive(cryptoConfig.btc.path);
    addresses.btc = generateBtcAddress(btcKey.publicKey);
  } catch (e) {
    console.warn('BTC derivation failed:', e);
  }

  // Ethereum - secp256k1 from HD path
  try {
    const ethKey = state.hdRoot.derive(cryptoConfig.eth.path);
    addresses.eth = generateEthAddress(ethKey.publicKey);
  } catch (e) {
    console.warn('ETH derivation failed:', e);
  }

  // Solana - needs Ed25519, derive from seed
  try {
    if (state.wallet.ed25519) {
      addresses.sol = generateSolAddress(state.wallet.ed25519.publicKey);
    }
  } catch (e) {
    console.warn('SOL derivation failed:', e);
  }

  // SUI - Ed25519
  try {
    if (state.wallet.ed25519) {
      addresses.sui = deriveSuiAddress(state.wallet.ed25519.publicKey);
    }
  } catch (e) {
    console.warn('SUI derivation failed:', e);
  }

  // Cosmos
  try {
    const atomKey = state.hdRoot.derive(cryptoConfig.atom.path);
    addresses.atom = generateCosmosAddress(atomKey.publicKey);
  } catch (e) {
    console.warn('ATOM derivation failed:', e);
  }

  state.addresses = addresses;
  return addresses;
}

// Coin type to config mapping
const coinTypeMap = {
  '0': 'btc',
  '2': 'ltc',
  '3': 'doge',
  '60': 'eth',
  '118': 'atom',
  '145': 'bch',
  '330': 'algo',
  '354': 'dot',
  '501': 'sol',
  '784': 'sui',
  '1815': 'ada',
};

// Extended crypto config for additional coins
const extendedCryptoConfig = {
  ...cryptoConfig,
  ltc: { name: 'Litecoin', symbol: 'LTC', coinType: 2, explorer: 'https://blockchair.com/litecoin/address/' },
  doge: { name: 'Dogecoin', symbol: 'DOGE', coinType: 3, explorer: 'https://dogechain.info/address/' },
  bch: { name: 'Bitcoin Cash', symbol: 'BCH', coinType: 145, explorer: 'https://blockchair.com/bitcoin-cash/address/' },
  algo: { name: 'Algorand', symbol: 'ALGO', coinType: 330, explorer: 'https://algoexplorer.io/address/' },
  dot: { name: 'Polkadot', symbol: 'DOT', coinType: 354, explorer: 'https://polkadot.subscan.io/account/' },
};

// Derive and display address based on current HD controls
function deriveAndDisplayAddress() {
  if (!state.hdRoot) {
    // Show not initialized message
    const notInit = $('hd-not-initialized');
    const result = $('derived-result');
    if (notInit) notInit.style.display = 'block';
    if (result) result.style.display = 'none';
    return;
  }

  // Hide not initialized, show result
  const notInit = $('hd-not-initialized');
  const result = $('derived-result');
  if (notInit) notInit.style.display = 'none';
  if (result) result.style.display = 'block';

  const coinType = $('hd-coin')?.value || '0';
  const account = parseInt($('hd-account')?.value || '0', 10);
  const index = parseInt($('hd-index')?.value || '0', 10);

  const coinKey = coinTypeMap[coinType] || 'btc';
  const config = extendedCryptoConfig[coinKey] || cryptoConfig.btc;
  const coinTypeNum = parseInt(coinType, 10);

  try {
    // Build HD path
    const purpose = coinTypeNum === 1815 ? 1852 : 44;
    let path;
    let signingPath;
    let encryptionPath;

    if (coinTypeNum === 501 || coinTypeNum === 784) {
      // Ed25519 coins use hardened derivation
      path = `m/${purpose}'/${coinTypeNum}'/${account}'/${index}'`;
      signingPath = path;
      encryptionPath = `m/${purpose}'/${coinTypeNum}'/${account}'/1'/${index}'`;
    } else if (coinTypeNum === 1815) {
      // Cardano uses 1852' purpose
      path = `m/${purpose}'/${coinTypeNum}'/${account}'/0/${index}`;
      signingPath = path;
      encryptionPath = `m/${purpose}'/${coinTypeNum}'/${account}'/1/${index}`;
    } else {
      // Standard BIP44
      path = `m/${purpose}'/${coinTypeNum}'/${account}'/0/${index}`;
      signingPath = path;
      encryptionPath = `m/${purpose}'/${coinTypeNum}'/${account}'/1/${index}`;
    }

    // Derive key
    let hdKey;
    let address;
    let signingPubKey;

    // Generate address based on coin type
    switch (coinTypeNum) {
      case 0: // BTC
      case 2: // LTC
      case 3: // DOGE
      case 145: // BCH
        hdKey = state.hdRoot.derive(path);
        address = generateBtcAddress(hdKey.publicKey);
        signingPubKey = hdKey.publicKey;
        break;
      case 60: // ETH
        hdKey = state.hdRoot.derive(path);
        address = generateEthAddress(hdKey.publicKey);
        signingPubKey = hdKey.publicKey;
        break;
      case 501: // SOL
        // Solana needs Ed25519 - derive from seed
        const solSeed = hkdf(sha256, state.masterSeed.slice(0, 32), new Uint8Array(0),
          new TextEncoder().encode(`sol-${account}-${index}`), 32);
        const solPub = ed25519.getPublicKey(solSeed);
        address = generateSolAddress(solPub);
        signingPubKey = solPub;
        break;
      case 784: // SUI
        const suiSeed = hkdf(sha256, state.masterSeed.slice(0, 32), new Uint8Array(0),
          new TextEncoder().encode(`sui-${account}-${index}`), 32);
        const suiPub = ed25519.getPublicKey(suiSeed);
        address = deriveSuiAddress(suiPub);
        signingPubKey = suiPub;
        break;
      case 118: // ATOM
        hdKey = state.hdRoot.derive(path);
        address = generateCosmosAddress(hdKey.publicKey);
        signingPubKey = hdKey.publicKey;
        break;
      case 1815: // ADA
        hdKey = state.hdRoot.derive(path);
        address = 'addr1...' + toHex(sha256(hdKey.publicKey)).slice(0, 56);
        signingPubKey = hdKey.publicKey;
        break;
      default:
        hdKey = state.hdRoot.derive(path);
        address = 'Unsupported coin type';
        signingPubKey = hdKey?.publicKey;
    }

    // Update signing key display
    const signingPathEl = $('signing-path');
    const signingPubkeyEl = $('signing-pubkey');
    if (signingPathEl) signingPathEl.textContent = signingPath;
    if (signingPubkeyEl && signingPubKey) signingPubkeyEl.textContent = toHex(signingPubKey);

    // Update encryption key display
    const encPathEl = $('encryption-path');
    const encPubkeyEl = $('encryption-pubkey');
    if (encPathEl) encPathEl.textContent = encryptionPath;
    if (encPubkeyEl && state.wallet.x25519) {
      encPubkeyEl.textContent = toHex(state.wallet.x25519.publicKey);
    }

    // Update address display
    const cryptoNameEl = $('derived-crypto-name');
    const addressEl = $('derived-address');
    const explorerLink = $('derived-explorer-link');

    if (cryptoNameEl) cryptoNameEl.textContent = config.name;
    if (addressEl) addressEl.textContent = truncateAddress(address, 12, 8);
    if (explorerLink && config.explorer) {
      explorerLink.href = config.explorer + address;
    }

    // Generate QR code for address
    const qrCanvas = $('address-qr');
    if (qrCanvas && address) {
      generateQRCode(address, qrCanvas, 64);
    }

    // Update root keys display
    updateRootKeysDisplay();

  } catch (e) {
    console.warn('Address derivation failed:', e);
    const addressEl = $('derived-address');
    if (addressEl) addressEl.textContent = 'Derivation error';
  }
}

// Update root keys display in the Account modal
function updateRootKeysDisplay() {
  if (!state.hdRoot) return;

  // Master public key (xpub)
  const xpubEl = $('wallet-xpub');
  if (xpubEl && state.hdRoot.publicExtendedKey) {
    xpubEl.textContent = state.hdRoot.publicExtendedKey;
  }

  // Master private key (xprv)
  const xprvEl = $('wallet-xprv');
  if (xprvEl && state.hdRoot.privateExtendedKey) {
    xprvEl.textContent = state.hdRoot.privateExtendedKey;
  }

  // Seed phrase (if available)
  const seedDisplay = $('wallet-seed-phrase');
  if (seedDisplay && state.mnemonic) {
    seedDisplay.textContent = state.mnemonic;
  }
}

// =============================================================================
// Key Derivation
// =============================================================================

async function deriveKeysFromPassword(username, password) {
  const encoder = new TextEncoder();
  const usernameSalt = encoder.encode(username);
  const passwordBytes = encoder.encode(password);

  // Initial hash
  const initialHash = sha256(new Uint8Array([...usernameSalt, ...passwordBytes]));

  // Master key via HKDF
  const masterKey = hkdf(sha256, initialHash, usernameSalt, encoder.encode('master-key'), 32);

  // Encryption keys
  state.encryptionKey = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('buffer-encryption-key'), 32);
  state.encryptionIV = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('buffer-encryption-iv'), 16);

  // HD wallet seed (64 bytes)
  const hdSeed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('hd-wallet-seed'), 64);
  state.masterSeed = hdSeed;
  state.hdRoot = HDKey.fromMasterSeed(hdSeed);

  // Generate key pairs
  const x25519Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('x25519-seed'), 32);
  const ed25519Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('ed25519-seed'), 32);
  const secp256k1Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('secp256k1-seed'), 32);
  const p256Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('p256-seed'), 32);

  state.wallet = {
    x25519: {
      privateKey: x25519Seed,
      publicKey: x25519.getPublicKey(x25519Seed),
    },
    ed25519: {
      privateKey: ed25519Seed,
      publicKey: ed25519.getPublicKey(ed25519Seed),
    },
    secp256k1: {
      privateKey: secp256k1Seed,
      publicKey: secp256k1.getPublicKey(secp256k1Seed, true),
    },
    p256: {
      privateKey: p256Seed,
      publicKey: p256.getPublicKey(p256Seed, true),
    },
  };

  return state.wallet;
}

async function deriveKeysFromSeed(seedPhrase) {
  const seed = await bip39.mnemonicToSeed(seedPhrase);
  const encoder = new TextEncoder();

  // Store mnemonic for display
  state.mnemonic = seedPhrase.trim();

  // Master key
  const masterKey = hkdf(sha256, new Uint8Array(seed.slice(0, 32)), new Uint8Array(0), encoder.encode('sdn-wallet'), 32);

  // Encryption keys
  state.encryptionKey = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('buffer-encryption-key'), 32);
  state.encryptionIV = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('buffer-encryption-iv'), 16);

  // HD wallet from full seed
  state.masterSeed = new Uint8Array(seed);
  state.hdRoot = HDKey.fromMasterSeed(new Uint8Array(seed));

  // Generate key pairs
  const x25519Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('x25519-seed'), 32);
  const ed25519Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('ed25519-seed'), 32);
  const secp256k1Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('secp256k1-seed'), 32);
  const p256Seed = hkdf(sha256, masterKey, new Uint8Array(0), encoder.encode('p256-seed'), 32);

  state.wallet = {
    x25519: {
      privateKey: x25519Seed,
      publicKey: x25519.getPublicKey(x25519Seed),
    },
    ed25519: {
      privateKey: ed25519Seed,
      publicKey: ed25519.getPublicKey(ed25519Seed),
    },
    secp256k1: {
      privateKey: secp256k1Seed,
      publicKey: secp256k1.getPublicKey(secp256k1Seed, true),
    },
    p256: {
      privateKey: p256Seed,
      publicKey: p256.getPublicKey(p256Seed, true),
    },
  };

  return state.wallet;
}

function generateSeedPhrase(words = 24) {
  const strength = words === 24 ? 256 : 128;
  return bip39.generateMnemonic(strength);
}

function validateSeedPhrase(phrase) {
  return bip39.validateMnemonic(phrase.trim().toLowerCase());
}

// =============================================================================
// PIN-Encrypted Wallet Storage
// =============================================================================

const STORED_WALLET_KEY = 'sdn_encrypted_wallet';
const PASSKEY_CREDENTIAL_KEY = 'sdn_passkey_credential';
const PASSKEY_WALLET_KEY = 'sdn_passkey_wallet';

function deriveKeyFromPIN(pin) {
  const encoder = new TextEncoder();
  const pinBytes = encoder.encode(pin);
  const salt = encoder.encode('sdn-wallet-pin-v1');
  const pinHash = sha256(new Uint8Array([...salt, ...pinBytes]));
  const encryptionKey = hkdf(sha256, pinHash, salt, encoder.encode('pin-encryption-key'), 32);
  const iv = hkdf(sha256, pinHash, salt, encoder.encode('pin-encryption-iv'), 16);
  return { encryptionKey, iv };
}

async function storeWalletWithPIN(pin, walletData) {
  if (!/^\d{6}$/.test(pin)) {
    throw new Error('PIN must be exactly 6 digits');
  }

  const { encryptionKey, iv } = deriveKeyFromPIN(pin);
  const encoder = new TextEncoder();
  const plaintext = encoder.encode(JSON.stringify(walletData));

  const cryptoKey = await crypto.subtle.importKey(
    'raw', encryptionKey, { name: 'AES-GCM' }, false, ['encrypt']
  );

  const ciphertext = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv }, cryptoKey, plaintext
  );

  const stored = {
    ciphertext: btoa(String.fromCharCode(...new Uint8Array(ciphertext))),
    timestamp: Date.now(),
    version: 1,
  };

  localStorage.setItem(STORED_WALLET_KEY, JSON.stringify(stored));
  return true;
}

async function retrieveWalletWithPIN(pin) {
  if (!/^\d{6}$/.test(pin)) {
    throw new Error('PIN must be exactly 6 digits');
  }

  const storedJson = localStorage.getItem(STORED_WALLET_KEY);
  if (!storedJson) throw new Error('No stored wallet found');

  const stored = JSON.parse(storedJson);
  const { encryptionKey, iv } = deriveKeyFromPIN(pin);
  const ciphertext = Uint8Array.from(atob(stored.ciphertext), c => c.charCodeAt(0));

  const cryptoKey = await crypto.subtle.importKey(
    'raw', encryptionKey, { name: 'AES-GCM' }, false, ['decrypt']
  );

  try {
    const plaintext = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv }, cryptoKey, ciphertext
    );
    return JSON.parse(new TextDecoder().decode(plaintext));
  } catch (e) {
    throw new Error('Invalid PIN or corrupted data');
  }
}

function hasStoredWallet() {
  const stored = localStorage.getItem(STORED_WALLET_KEY);
  if (!stored) return null;
  try {
    const data = JSON.parse(stored);
    return {
      exists: true,
      timestamp: data.timestamp,
      date: new Date(data.timestamp).toLocaleDateString(),
    };
  } catch {
    return null;
  }
}

function forgetStoredWallet() {
  localStorage.removeItem(STORED_WALLET_KEY);
  localStorage.removeItem(PASSKEY_CREDENTIAL_KEY);
  localStorage.removeItem(PASSKEY_WALLET_KEY);
}

// =============================================================================
// Passkey (WebAuthn) Wallet Storage
// =============================================================================

function isPasskeySupported() {
  return typeof window !== 'undefined' &&
    window.PublicKeyCredential !== undefined &&
    typeof window.PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable === 'function';
}

function hasPasskey() {
  return localStorage.getItem(PASSKEY_CREDENTIAL_KEY) !== null;
}

async function registerPasskeyAndStoreWallet(walletData) {
  if (!isPasskeySupported()) {
    throw new Error('Passkeys are not supported on this device');
  }

  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const userId = crypto.getRandomValues(new Uint8Array(16));

  const credential = await navigator.credentials.create({
    publicKey: {
      challenge,
      rp: { name: 'Space Data Network', id: window.location.hostname },
      user: { id: userId, name: 'wallet-user', displayName: 'SDN Wallet' },
      pubKeyCredParams: [
        { alg: -7, type: 'public-key' },
        { alg: -257, type: 'public-key' },
      ],
      authenticatorSelection: {
        authenticatorAttachment: 'platform',
        userVerification: 'required',
        residentKey: 'required',
      },
      timeout: 60000,
      attestation: 'none',
    },
  });

  // Derive encryption key from credential
  const keyMaterial = new Uint8Array(credential.rawId);
  const encoder = new TextEncoder();
  const salt = encoder.encode('sdn-passkey-v1');
  const keyHash = sha256(new Uint8Array([...salt, ...keyMaterial]));
  const encryptionKey = hkdf(sha256, keyHash, salt, encoder.encode('passkey-encryption-key'), 32);
  const iv = hkdf(sha256, keyHash, salt, encoder.encode('passkey-encryption-iv'), 16);

  // Encrypt wallet
  const plaintext = encoder.encode(JSON.stringify(walletData));
  const cryptoKey = await crypto.subtle.importKey(
    'raw', encryptionKey, { name: 'AES-GCM' }, false, ['encrypt']
  );
  const ciphertext = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv }, cryptoKey, plaintext
  );

  // Store
  localStorage.setItem(PASSKEY_CREDENTIAL_KEY, JSON.stringify({
    id: btoa(String.fromCharCode(...new Uint8Array(credential.rawId))),
    timestamp: Date.now(),
  }));
  localStorage.setItem(PASSKEY_WALLET_KEY, JSON.stringify({
    ciphertext: btoa(String.fromCharCode(...new Uint8Array(ciphertext))),
    timestamp: Date.now(),
    version: 1,
  }));

  return true;
}

async function authenticatePasskeyAndRetrieveWallet() {
  if (!isPasskeySupported()) {
    throw new Error('Passkeys are not supported');
  }

  const credentialJson = localStorage.getItem(PASSKEY_CREDENTIAL_KEY);
  const walletJson = localStorage.getItem(PASSKEY_WALLET_KEY);
  if (!credentialJson || !walletJson) {
    throw new Error('No passkey wallet found');
  }

  const credentialData = JSON.parse(credentialJson);
  const encryptedWallet = JSON.parse(walletJson);
  const credentialId = Uint8Array.from(atob(credentialData.id), c => c.charCodeAt(0));

  const assertion = await navigator.credentials.get({
    publicKey: {
      challenge: crypto.getRandomValues(new Uint8Array(32)),
      allowCredentials: [{ id: credentialId, type: 'public-key', transports: ['internal'] }],
      userVerification: 'required',
      timeout: 60000,
    },
  });

  // Derive same encryption key
  const keyMaterial = new Uint8Array(assertion.rawId);
  const encoder = new TextEncoder();
  const salt = encoder.encode('sdn-passkey-v1');
  const keyHash = sha256(new Uint8Array([...salt, ...keyMaterial]));
  const encryptionKey = hkdf(sha256, keyHash, salt, encoder.encode('passkey-encryption-key'), 32);
  const iv = hkdf(sha256, keyHash, salt, encoder.encode('passkey-encryption-iv'), 16);

  // Decrypt
  const ciphertext = Uint8Array.from(atob(encryptedWallet.ciphertext), c => c.charCodeAt(0));
  const cryptoKey = await crypto.subtle.importKey(
    'raw', encryptionKey, { name: 'AES-GCM' }, false, ['decrypt']
  );
  const plaintext = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv }, cryptoKey, ciphertext
  );

  return JSON.parse(new TextDecoder().decode(plaintext));
}

// =============================================================================
// ECIES Encryption (PKI)
// =============================================================================

async function eciesEncrypt(recipientPublicKey, plaintext, algorithm = 'x25519') {
  const encoder = new TextEncoder();
  const data = encoder.encode(plaintext);

  // Generate ephemeral key pair
  const ephemeralPrivate = crypto.getRandomValues(new Uint8Array(32));
  let ephemeralPublic, sharedSecret;

  if (algorithm === 'x25519') {
    ephemeralPublic = x25519.getPublicKey(ephemeralPrivate);
    sharedSecret = x25519.getSharedSecret(ephemeralPrivate, recipientPublicKey);
  } else if (algorithm === 'secp256k1') {
    ephemeralPublic = secp256k1.getPublicKey(ephemeralPrivate, true);
    sharedSecret = secp256k1.getSharedSecret(ephemeralPrivate, recipientPublicKey).slice(1);
  } else if (algorithm === 'p256') {
    ephemeralPublic = p256.getPublicKey(ephemeralPrivate, true);
    sharedSecret = p256.getSharedSecret(ephemeralPrivate, recipientPublicKey).slice(1);
  }

  // Derive AES key via HKDF
  const aesKey = hkdf(sha256, sharedSecret, new Uint8Array(0), encoder.encode('ecies-aes-key'), 32);
  const nonce = crypto.getRandomValues(new Uint8Array(12));

  // Encrypt with AES-GCM
  const cryptoKey = await crypto.subtle.importKey(
    'raw', aesKey, { name: 'AES-GCM' }, false, ['encrypt']
  );
  const ciphertext = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv: nonce }, cryptoKey, data
  );

  return {
    ciphertext: new Uint8Array(ciphertext),
    header: {
      algorithm,
      ephemeralPublicKey: toHex(ephemeralPublic),
      nonce: toHex(nonce),
    },
  };
}

async function eciesDecrypt(recipientPrivateKey, ciphertext, header) {
  const { algorithm, ephemeralPublicKey, nonce } = header;
  const ephemeralPub = fromHex(ephemeralPublicKey);
  const nonceBytes = fromHex(nonce);
  const encoder = new TextEncoder();

  // Compute shared secret
  let sharedSecret;
  if (algorithm === 'x25519') {
    sharedSecret = x25519.getSharedSecret(recipientPrivateKey, ephemeralPub);
  } else if (algorithm === 'secp256k1') {
    sharedSecret = secp256k1.getSharedSecret(recipientPrivateKey, ephemeralPub).slice(1);
  } else if (algorithm === 'p256') {
    sharedSecret = p256.getSharedSecret(recipientPrivateKey, ephemeralPub).slice(1);
  }

  // Derive AES key
  const aesKey = hkdf(sha256, sharedSecret, new Uint8Array(0), encoder.encode('ecies-aes-key'), 32);

  // Decrypt
  const cryptoKey = await crypto.subtle.importKey(
    'raw', aesKey, { name: 'AES-GCM' }, false, ['decrypt']
  );
  const plaintext = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: nonceBytes }, cryptoKey, ciphertext
  );

  return new TextDecoder().decode(plaintext);
}

// =============================================================================
// vCard Generation
// =============================================================================

function generateVCard(info, { skipPhoto = false } = {}) {
  // Create UPMT-compatible person object for vcard-cryptoperson
  const person = {
    HONORIFIC_PREFIX: info.prefix || '',
    GIVEN_NAME: info.givenName || '',
    ADDITIONAL_NAME: info.middleName || '',
    FAMILY_NAME: info.familyName || '',
    HONORIFIC_SUFFIX: info.suffix || '',
    CONTACT_POINT: [],
    AFFILIATION: null,
    HAS_OCCUPATION: null,
    KEY: [],
    IMAGE: null,
  };

  // Add email
  if (info.email) {
    person.CONTACT_POINT.push({
      EMAIL: info.email,
      CONTACT_TYPE: 'work',
    });
  }

  // Add organization
  if (info.organization) {
    person.AFFILIATION = {
      NAME: info.organization,
      LEGAL_NAME: info.organization,
    };
  }

  // Add job title
  if (info.title) {
    person.HAS_OCCUPATION = {
      NAME: info.title,
    };
  }

  // Add photo
  if (!skipPhoto && state.vcardPhoto) {
    person.IMAGE = state.vcardPhoto;
  }

  // Add crypto keys if logged in and requested
  if (info.includeKeys && state.wallet.ed25519) {
    // Ed25519 signing key
    person.KEY.push({
      PUBLIC_KEY: bytesToHex(state.wallet.ed25519.publicKey),
      ADDRESS_TYPE: 1, // Ed25519
    });

    // X25519 encryption key
    if (state.wallet.x25519) {
      person.KEY.push({
        PUBLIC_KEY: bytesToHex(state.wallet.x25519.publicKey),
        ADDRESS_TYPE: 2, // X25519
      });
    }
  }

  // Generate vCard
  const note = info.includeKeys && state.wallet.ed25519
    ? `Ed25519: ${bytesToHex(state.wallet.ed25519.publicKey).slice(0, 16)}...`
    : '';
  let vcard = createV3(person, note);

  // Make photo iOS compatible (convert VALUE=URI format to ENCODING=b)
  vcard = vcard.replace(
    /PHOTO;VALUE=URI:data:image\/(jpeg|png);base64,([^\n]+)/gi,
    (match, type, data) => {
      const vcardType = type.toUpperCase();
      // vCard 3.0 line folding: continuation lines start with a space
      let folded = `PHOTO;ENCODING=b;TYPE=${vcardType}:`;
      const lines = data.match(/.{1,74}/g) || [data];
      folded += lines.join('\n ');
      return folded;
    }
  );

  return vcard;
}

async function generateQRCode(text, canvas, width = 256) {
  try {
    await QRCode.toCanvas(canvas, text, { width, margin: 2 });
  } catch (err) {
    console.error('QR code generation failed:', err);
  }
}

// =============================================================================
// Balance Fetching
// =============================================================================

async function fetchBalance(crypto, address) {
  const config = cryptoConfig[crypto];
  if (!config?.balanceApi) return null;

  try {
    if (crypto === 'btc') {
      const response = await fetch(config.balanceApi + address);
      if (!response.ok) return null;
      const data = await response.json();
      const balance = (data.chain_stats?.funded_txo_sum || 0) - (data.chain_stats?.spent_txo_sum || 0);
      return config.formatBalance(balance);
    }
  } catch (err) {
    console.warn('Failed to fetch balance:', err);
  }
  return null;
}

async function refreshAllBalances() {
  for (const [key, address] of Object.entries(state.addresses)) {
    if (!address) continue;

    const balance = await fetchBalance(key, address);
    const balanceEl = $(`wallet-${key}-balance`);
    if (balanceEl && balance) {
      balanceEl.textContent = balance;
    }
  }
}

// =============================================================================
// Login Flow
// =============================================================================

async function handleLogin(method, data) {
  try {
    if (method === 'password') {
      await deriveKeysFromPassword(data.username, data.password);
    } else if (method === 'seed') {
      await deriveKeysFromSeed(data.seedPhrase);
    } else if (method === 'stored-pin') {
      const walletData = await retrieveWalletWithPIN(data.pin);
      // Restore wallet from stored data based on type
      if (walletData.type === 'password') {
        await deriveKeysFromPassword(walletData.username, walletData.password);
      } else if (walletData.seedPhrase) {
        await deriveKeysFromSeed(walletData.seedPhrase);
      } else {
        throw new Error('Invalid stored wallet data');
      }
    } else if (method === 'stored-passkey') {
      const walletData = await authenticatePasskeyAndRetrieveWallet();
      // Restore wallet from stored data based on type
      if (walletData.type === 'password') {
        await deriveKeysFromPassword(walletData.username, walletData.password);
      } else if (walletData.seedPhrase) {
        await deriveKeysFromSeed(walletData.seedPhrase);
      } else {
        throw new Error('Invalid stored wallet data');
      }
    }

    state.loggedIn = true;
    state.loginMethod = method;

    // Generate addresses
    generateAddresses();

    // Update UI
    updateLoginUI();
    closeLoginModal();

    // Derive PKI keys
    derivePKIKeys();

    return true;
  } catch (err) {
    console.error('Login failed:', err);
    throw err;
  }
}

function updateLoginUI() {
  // Hide login prompts, show logged-in content
  document.querySelectorAll('.login-required').forEach(el => el.style.display = 'none');
  document.querySelectorAll('.logged-in-content').forEach(el => el.style.display = 'block');

  // Hide nav login button, show Account and Logout
  const navLogin = $('nav-login');
  const navKeys = $('nav-keys');
  const navLogout = $('nav-logout');

  if (navLogin) navLogin.style.display = 'none';
  if (navKeys) navKeys.style.display = 'inline-flex';
  if (navLogout) navLogout.style.display = 'inline-block';

  // Update mobile login button
  const mobileLogin = $('mobile-login');
  if (mobileLogin) {
    mobileLogin.textContent = 'Logout';
    mobileLogin.classList.add('logged-in');
  }

  // Update adversarial balances
  updateAdversarialUI();

  // Update PKI UI
  updatePKIUI();
}

function logout() {
  // Reset state
  state.loggedIn = false;
  state.loginMethod = null;
  state.wallet = { x25519: null, ed25519: null, secp256k1: null, p256: null };
  state.masterSeed = null;
  state.hdRoot = null;
  state.mnemonic = null;
  state.addresses = { btc: null, eth: null, sol: null, sui: null, atom: null, ada: null };
  state.encryptionKey = null;
  state.encryptionIV = null;
  state.pki = { algorithm: 'x25519', alice: null, bob: null };

  // Update UI - show login prompts, hide logged-in content
  document.querySelectorAll('.login-required').forEach(el => el.style.display = 'block');
  document.querySelectorAll('.logged-in-content').forEach(el => el.style.display = 'none');

  // Show nav login button, hide Account and Logout
  const navLogin = $('nav-login');
  const navKeys = $('nav-keys');
  const navLogout = $('nav-logout');

  if (navLogin) navLogin.style.display = 'inline-block';
  if (navKeys) navKeys.style.display = 'none';
  if (navLogout) navLogout.style.display = 'none';

  // Close keys modal if open
  const keysModal = $('keys-modal');
  if (keysModal) keysModal.classList.remove('active');

  // Update mobile login button
  const mobileLogin = $('mobile-login');
  if (mobileLogin) {
    mobileLogin.textContent = 'Login';
    mobileLogin.classList.remove('logged-in');
  }

  // Update adversarial UI
  updateAdversarialUI();

  // Update PKI UI
  updatePKIUI();

  console.log('Logged out successfully');
}

function updateAdversarialUI() {
  const loginRequired = $('adversarial-login-required');
  const balances = $('adversarial-balances');

  if (loginRequired) loginRequired.style.display = state.loggedIn ? 'none' : 'block';
  if (balances) balances.style.display = state.loggedIn ? 'block' : 'none';

  // Update addresses
  for (const [key, config] of Object.entries(cryptoConfig)) {
    const address = state.addresses[key];
    const addressEl = $(`wallet-${key}-address`);
    const explorerLink = $(`wallet-${key}-explorer`);

    if (addressEl && address) {
      addressEl.textContent = truncateAddress(address, 10, 8);
    }
    if (explorerLink && address) {
      explorerLink.href = config.explorer + address;
    }
  }

  // Fetch balances
  refreshAllBalances();
}

function derivePKIKeys() {
  if (!state.wallet) return;

  // Alice uses index 0, Bob uses index 1 from HD wallet
  const alicePath = "m/44'/0'/0'/0/0";
  const bobPath = "m/44'/0'/0'/0/1";

  const algorithm = state.pki.algorithm || 'x25519';

  if (algorithm === 'x25519') {
    const aliceSeed = hkdf(sha256, state.masterSeed.slice(0, 32), new Uint8Array(0), new TextEncoder().encode('alice-x25519'), 32);
    const bobSeed = hkdf(sha256, state.masterSeed.slice(0, 32), new Uint8Array(0), new TextEncoder().encode('bob-x25519'), 32);

    state.pki.alice = {
      privateKey: aliceSeed,
      publicKey: x25519.getPublicKey(aliceSeed),
      path: alicePath,
    };
    state.pki.bob = {
      privateKey: bobSeed,
      publicKey: x25519.getPublicKey(bobSeed),
      path: bobPath,
    };
  } else if (algorithm === 'secp256k1') {
    const aliceKey = state.hdRoot.derive(alicePath);
    const bobKey = state.hdRoot.derive(bobPath);

    state.pki.alice = {
      privateKey: aliceKey.privateKey,
      publicKey: aliceKey.publicKey,
      path: alicePath,
    };
    state.pki.bob = {
      privateKey: bobKey.privateKey,
      publicKey: bobKey.publicKey,
      path: bobPath,
    };
  }
}

function updatePKIUI() {
  const loginPrompt = $('pki-login-prompt');
  const pkiParties = $('pki-parties');
  const pkiDemo = $('pki-demo');
  const pkiControls = $('pki-controls');

  if (state.loggedIn) {
    if (loginPrompt) loginPrompt.style.display = 'none';
    if (pkiParties) pkiParties.style.display = 'flex';
    if (pkiDemo) pkiDemo.style.display = 'block';
    if (pkiControls) pkiControls.style.display = 'flex';

    // Update key displays
    if (state.pki.alice) {
      const alicePub = $('alice-public-key');
      const alicePriv = $('alice-private-key');
      const alicePath = $('alice-path');
      if (alicePub) alicePub.textContent = toHex(state.pki.alice.publicKey).slice(0, 32) + '...';
      if (alicePriv) alicePriv.textContent = '••••••••••••••••';
      if (alicePath) alicePath.textContent = state.pki.alice.path;
    }
    if (state.pki.bob) {
      const bobPub = $('bob-public-key');
      const bobPriv = $('bob-private-key');
      const bobPath = $('bob-path');
      if (bobPub) bobPub.textContent = toHex(state.pki.bob.publicKey).slice(0, 32) + '...';
      if (bobPriv) bobPriv.textContent = '••••••••••••••••';
      if (bobPath) bobPath.textContent = state.pki.bob.path;
    }
  } else {
    if (loginPrompt) loginPrompt.style.display = 'block';
    if (pkiParties) pkiParties.style.display = 'none';
    if (pkiDemo) pkiDemo.style.display = 'none';
    if (pkiControls) pkiControls.style.display = 'none';
  }
}

// =============================================================================
// Modal Management
// =============================================================================

function openLoginModal() {
  const modal = $('login-modal');
  if (modal) {
    modal.classList.add('open');
    document.body.style.overflow = 'hidden';
  }
}

function closeLoginModal() {
  const modal = $('login-modal');
  if (modal) {
    modal.classList.remove('open');
    document.body.style.overflow = '';
  }
}

// =============================================================================
// Event Listeners
// =============================================================================

function initEventListeners() {
  // Login/Logout button (desktop)
  const navLogin = $('nav-login');
  if (navLogin) {
    navLogin.addEventListener('click', (e) => {
      e.preventDefault();
      if (state.loggedIn) {
        logout();
      } else {
        openLoginModal();
      }
    });
  }

  // Login/Logout button (mobile)
  const mobileLogin = $('mobile-login');
  if (mobileLogin) {
    mobileLogin.addEventListener('click', (e) => {
      e.preventDefault();
      if (state.loggedIn) {
        logout();
      } else {
        openLoginModal();
      }
    });
  }

  // Modal close
  document.querySelectorAll('.modal-close').forEach(btn => {
    btn.addEventListener('click', closeLoginModal);
  });

  // Click outside modal to close
  const loginModal = $('login-modal');
  if (loginModal) {
    loginModal.addEventListener('click', (e) => {
      if (e.target === loginModal) closeLoginModal();
    });
  }

  // Method tabs
  document.querySelectorAll('.method-tab').forEach(tab => {
    tab.addEventListener('click', () => {
      const method = tab.dataset.method;
      document.querySelectorAll('.method-tab').forEach(t => t.classList.remove('active'));
      document.querySelectorAll('.method-content').forEach(c => c.classList.remove('active'));
      tab.classList.add('active');
      const content = $(`${method}-method`);
      if (content) content.classList.add('active');
    });
  });

  // Password input
  const passwordInput = $('wallet-password');
  if (passwordInput) {
    passwordInput.addEventListener('input', (e) => updatePasswordStrength(e.target.value));
  }

  // Password login button
  const deriveFromPassword = $('derive-from-password');
  if (deriveFromPassword) {
    deriveFromPassword.addEventListener('click', async () => {
      const username = $('wallet-username')?.value?.trim();
      const password = $('wallet-password')?.value;
      const rememberWallet = $('remember-wallet-password')?.checked;

      if (!username || !password) {
        alert('Please enter both username and password');
        return;
      }

      try {
        deriveFromPassword.textContent = 'Logging in...';
        deriveFromPassword.disabled = true;

        await handleLogin('password', { username, password });

        if (rememberWallet) {
          const pin = $('pin-input-password')?.value;
          const usePasskey = $('passkey-btn-password')?.classList.contains('active');

          // Store credentials for later restoration
          const walletData = { type: 'password', username, password };

          if (usePasskey && isPasskeySupported()) {
            try {
              await registerPasskeyAndStoreWallet(walletData);
              console.log('Wallet stored with passkey');
            } catch (e) {
              console.error('Failed to store with passkey:', e);
              alert('Failed to save with passkey: ' + e.message);
            }
          } else if (pin && pin.length === 6) {
            try {
              await storeWalletWithPIN(pin, walletData);
              console.log('Wallet stored with PIN');
            } catch (e) {
              console.error('Failed to store with PIN:', e);
              alert('Failed to save with PIN: ' + e.message);
            }
          } else if (rememberWallet && !usePasskey && (!pin || pin.length !== 6)) {
            alert('Please enter a 6-digit PIN to remember your wallet');
          }
        }
      } catch (err) {
        alert('Login failed: ' + err.message);
      } finally {
        deriveFromPassword.textContent = 'Login';
        deriveFromPassword.disabled = false;
      }
    });
  }

  // Seed phrase login
  const generateSeedBtn = $('generate-seed');
  if (generateSeedBtn) {
    generateSeedBtn.addEventListener('click', () => {
      const textarea = $('seed-phrase');
      if (textarea) textarea.value = generateSeedPhrase();
      const deriveBtn = $('derive-from-seed');
      if (deriveBtn) deriveBtn.disabled = false;
    });
  }

  const validateSeedBtn = $('validate-seed');
  if (validateSeedBtn) {
    validateSeedBtn.addEventListener('click', () => {
      const textarea = $('seed-phrase');
      const isValid = validateSeedPhrase(textarea?.value || '');
      alert(isValid ? 'Valid seed phrase!' : 'Invalid seed phrase');
    });
  }

  const deriveFromSeed = $('derive-from-seed');
  if (deriveFromSeed) {
    deriveFromSeed.addEventListener('click', async () => {
      const seedPhrase = $('seed-phrase')?.value?.trim();
      const rememberWallet = $('remember-wallet-seed')?.checked;

      if (!seedPhrase) {
        alert('Please enter a seed phrase');
        return;
      }

      if (!validateSeedPhrase(seedPhrase)) {
        alert('Invalid seed phrase');
        return;
      }

      try {
        deriveFromSeed.textContent = 'Logging in...';
        deriveFromSeed.disabled = true;

        await handleLogin('seed', { seedPhrase });

        if (rememberWallet) {
          const pin = $('pin-input-seed')?.value;
          const usePasskey = $('passkey-btn-seed')?.classList.contains('active');

          // Store seed phrase for later restoration
          const walletData = { type: 'seed', seedPhrase };

          if (usePasskey && isPasskeySupported()) {
            try {
              await registerPasskeyAndStoreWallet(walletData);
              console.log('Wallet stored with passkey');
            } catch (e) {
              console.error('Failed to store with passkey:', e);
              alert('Failed to save with passkey: ' + e.message);
            }
          } else if (pin && pin.length === 6) {
            try {
              await storeWalletWithPIN(pin, walletData);
              console.log('Wallet stored with PIN');
            } catch (e) {
              console.error('Failed to store with PIN:', e);
              alert('Failed to save with PIN: ' + e.message);
            }
          } else if (rememberWallet && !usePasskey && (!pin || pin.length !== 6)) {
            alert('Please enter a 6-digit PIN to remember your wallet');
          }
        }
      } catch (err) {
        alert('Login failed: ' + err.message);
      } finally {
        deriveFromSeed.textContent = 'Login';
        deriveFromSeed.disabled = false;
      }
    });
  }

  // PIN unlock
  const unlockStoredWallet = $('unlock-stored-wallet');
  if (unlockStoredWallet) {
    unlockStoredWallet.addEventListener('click', async () => {
      const pin = $('pin-input-unlock').value;

      try {
        unlockStoredWallet.textContent = 'Unlocking...';
        unlockStoredWallet.disabled = true;

        await handleLogin('stored-pin', { pin });
      } catch (err) {
        alert('Unlock failed: ' + err.message);
      } finally {
        unlockStoredWallet.textContent = 'Unlock with PIN';
        unlockStoredWallet.disabled = false;
      }
    });
  }

  // Passkey unlock
  const unlockWithPasskey = $('unlock-with-passkey');
  if (unlockWithPasskey) {
    unlockWithPasskey.addEventListener('click', async () => {
      try {
        await handleLogin('stored-passkey', {});
      } catch (err) {
        alert('Passkey unlock failed: ' + err.message);
      }
    });
  }

  // Forget wallet
  const forgetWallet = $('forget-stored-wallet');
  if (forgetWallet) {
    forgetWallet.addEventListener('click', () => {
      if (confirm('Are you sure you want to forget this wallet?')) {
        forgetStoredWallet();
        const storedTab = $('stored-tab');
        if (storedTab) storedTab.style.display = 'none';
      }
    });
  }

  // PKI encryption
  const pkiEncryptBtn = $('pki-encrypt');
  if (pkiEncryptBtn) {
    pkiEncryptBtn.addEventListener('click', async () => {
      const plaintext = $('pki-plaintext')?.value;
      if (!plaintext || !state.pki.bob) return;

      try {
        const result = await eciesEncrypt(
          state.pki.bob.publicKey,
          plaintext,
          state.pki.algorithm
        );

        state.pki.ciphertext = result.ciphertext;
        state.pki.header = result.header;

        const ciphertextEl = $('pki-ciphertext');
        const headerEl = $('pki-header');
        const ciphertextStep = $('pki-ciphertext-step');
        const decryptStep = $('pki-decrypt-step');

        if (ciphertextEl) ciphertextEl.textContent = toHex(result.ciphertext);
        if (headerEl) headerEl.textContent = JSON.stringify(result.header, null, 2);
        if (ciphertextStep) ciphertextStep.style.display = 'block';
        if (decryptStep) decryptStep.style.display = 'block';
      } catch (err) {
        alert('Encryption failed: ' + err.message);
      }
    });
  }

  // PKI decryption
  const pkiDecryptBtn = $('pki-decrypt');
  if (pkiDecryptBtn) {
    pkiDecryptBtn.addEventListener('click', async () => {
      if (!state.pki.ciphertext || !state.pki.header || !state.pki.bob) return;

      try {
        const plaintext = await eciesDecrypt(
          state.pki.bob.privateKey,
          state.pki.ciphertext,
          state.pki.header
        );

        const decryptedEl = $('pki-decrypted');
        const resultStep = $('pki-result-step');
        const verification = $('pki-verification');

        if (decryptedEl) decryptedEl.textContent = plaintext;
        if (resultStep) resultStep.style.display = 'block';
        if (verification) verification.style.display = 'flex';
      } catch (err) {
        alert('Decryption failed: ' + err.message);
      }
    });
  }

  // Wrong key demo
  const pkiWrongKeyBtn = $('pki-wrong-key');
  if (pkiWrongKeyBtn) {
    pkiWrongKeyBtn.addEventListener('click', async () => {
      if (!state.pki.ciphertext || !state.pki.header || !state.pki.alice) return;

      const wrongResult = $('pki-wrong-result');

      try {
        await eciesDecrypt(
          state.pki.alice.privateKey,
          state.pki.ciphertext,
          state.pki.header
        );
        // Should fail
        if (wrongResult) {
          wrongResult.textContent = 'Unexpected: Decryption succeeded with wrong key!';
          wrongResult.style.display = 'block';
        }
      } catch (err) {
        if (wrongResult) {
          wrongResult.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg><span>Decryption failed - Only Bob\'s private key can decrypt!</span>';
          wrongResult.style.display = 'flex';
        }
      }
    });
  }

  // Refresh balances
  const refreshBalancesBtn = $('refresh-balances');
  if (refreshBalancesBtn) {
    refreshBalancesBtn.addEventListener('click', () => {
      refreshAllBalances();
    });
  }

  // Check for stored wallet on load
  const storedWallet = hasStoredWallet();
  if (storedWallet) {
    const storedTab = $('stored-tab');
    const storedDate = $('stored-wallet-date');
    if (storedTab) storedTab.style.display = 'block';
    if (storedDate) storedDate.textContent = `Stored on ${storedWallet.date}`;

    // Check for passkey
    if (hasPasskey()) {
      const pinSection = $('stored-pin-section');
      const passkeySection = $('stored-passkey-section');
      const divider = $('stored-divider');
      if (passkeySection) passkeySection.style.display = 'block';
      if (divider) divider.style.display = 'block';
    }
  }

  // Remember wallet checkboxes
  document.querySelectorAll('[id^="remember-wallet-"]').forEach(checkbox => {
    checkbox.addEventListener('change', (e) => {
      const target = e.target.id.replace('remember-wallet-', '');
      const options = $(`remember-options-${target}`);
      if (options) {
        options.style.display = e.target.checked ? 'block' : 'none';
      }
    });
  });

  // Remember method selector
  document.querySelectorAll('.remember-method-btn').forEach(btn => {
    btn.addEventListener('click', (e) => {
      const method = e.target.dataset.method;
      const target = e.target.dataset.target;

      document.querySelectorAll(`.remember-method-btn[data-target="${target}"]`).forEach(b => {
        b.classList.remove('active');
      });
      e.target.classList.add('active');

      const pinGroup = $(`pin-group-${target}`);
      const passkeyInfo = $(`passkey-info-${target}`);

      if (method === 'pin') {
        if (pinGroup) pinGroup.style.display = 'block';
        if (passkeyInfo) passkeyInfo.style.display = 'none';
      } else {
        if (pinGroup) pinGroup.style.display = 'none';
        if (passkeyInfo) passkeyInfo.style.display = 'flex';
      }
    });
  });

  // PIN input validation
  document.querySelectorAll('.pin-input, .pin-input-large').forEach(input => {
    input.addEventListener('input', (e) => {
      e.target.value = e.target.value.replace(/\D/g, '').slice(0, 6);

      // Enable unlock button when PIN is complete
      if (e.target.id === 'pin-input-unlock') {
        const unlockBtn = $('unlock-stored-wallet');
        if (unlockBtn) unlockBtn.disabled = e.target.value.length !== 6;
      }
    });
  });

  // ==========================================================================
  // Account Modal Handlers
  // ==========================================================================

  // Account button (opens keys modal)
  const navKeys = $('nav-keys');
  if (navKeys) {
    navKeys.addEventListener('click', () => {
      const keysModal = $('keys-modal');
      if (keysModal) {
        keysModal.classList.add('active');
        deriveAndDisplayAddress();
      }
    });
  }

  // Logout button
  const navLogout = $('nav-logout');
  if (navLogout) {
    navLogout.addEventListener('click', () => {
      logout();
    });
  }

  // Keys modal close
  const keysModal = $('keys-modal');
  if (keysModal) {
    keysModal.querySelectorAll('.modal-close').forEach(btn => {
      btn.addEventListener('click', () => {
        keysModal.classList.remove('active');
      });
    });

    keysModal.addEventListener('click', (e) => {
      if (e.target === keysModal) keysModal.classList.remove('active');
    });
  }

  // Modal tabs (Keys/vCard)
  document.querySelectorAll('.modal-tab').forEach(tab => {
    tab.addEventListener('click', () => {
      const tabId = tab.dataset.modalTab;
      if (!tabId) return;

      // Update tabs
      tab.closest('.modal-tabs').querySelectorAll('.modal-tab').forEach(t => t.classList.remove('active'));
      tab.classList.add('active');

      // Update content
      const modal = tab.closest('.modal');
      if (modal) {
        modal.querySelectorAll('.modal-tab-content').forEach(c => c.classList.remove('active'));
        const content = modal.querySelector(`#${tabId}`);
        if (content) content.classList.add('active');
      }
    });
  });

  // Quick derive buttons (coin type is in data-coin attribute)
  document.querySelectorAll('.quick-derive .glass-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const coinType = btn.dataset.coin;
      if (coinType) {
        const hdCoin = $('hd-coin');
        if (hdCoin) {
          hdCoin.value = coinType;
          deriveAndDisplayAddress();
        }
      }
    });
  });

  // HD controls (network, account, index)
  const hdCoin = $('hd-coin');
  const hdAccount = $('hd-account');
  const hdIndex = $('hd-index');

  if (hdCoin) hdCoin.addEventListener('change', deriveAndDisplayAddress);
  if (hdAccount) hdAccount.addEventListener('change', deriveAndDisplayAddress);
  if (hdIndex) hdIndex.addEventListener('change', deriveAndDisplayAddress);

  // Reveal key buttons
  document.querySelectorAll('.reveal-key-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.dataset.target;
      const target = $(targetId);
      if (target) {
        const isRevealed = target.dataset.revealed === 'true';
        target.dataset.revealed = (!isRevealed).toString();
        target.classList.toggle('blurred', isRevealed);
      }
    });
  });

  // Copy key buttons
  document.querySelectorAll('.copy-btn, .copy-key-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.dataset.copy;
      const target = $(targetId);
      if (target) {
        navigator.clipboard.writeText(target.textContent);
        showToast('Copied!');
      }
    });
  });

  // vCard photo upload
  const vcardPhotoInput = $('vcard-photo-input');
  if (vcardPhotoInput) {
    vcardPhotoInput.addEventListener('change', (e) => {
      const file = e.target.files?.[0];
      if (!file) return;

      const reader = new FileReader();
      reader.onload = (event) => {
        const img = new Image();
        img.onload = () => {
          // Resize to 128x128
          const canvas = document.createElement('canvas');
          canvas.width = 128;
          canvas.height = 128;
          const ctx = canvas.getContext('2d');
          if (ctx) {
            ctx.drawImage(img, 0, 0, 128, 128);
            const dataUrl = canvas.toDataURL('image/jpeg', 0.7);
            state.vcardPhoto = dataUrl;

            // Update preview
            const preview = $('vcard-photo-preview');
            if (preview) {
              preview.innerHTML = `<img src="${dataUrl}" alt="Photo">`;
            }

            // Show remove button
            const removeBtn = $('vcard-photo-remove');
            if (removeBtn) removeBtn.style.display = 'inline-block';
          }
        };
        img.src = event.target?.result;
      };
      reader.readAsDataURL(file);
    });
  }

  // vCard photo remove
  const vcardPhotoRemove = $('vcard-photo-remove');
  if (vcardPhotoRemove) {
    vcardPhotoRemove.addEventListener('click', () => {
      state.vcardPhoto = null;

      // Reset preview
      const preview = $('vcard-photo-preview');
      if (preview) {
        preview.innerHTML = `
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="photo-placeholder-icon">
            <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/>
            <circle cx="12" cy="7" r="4"/>
          </svg>
        `;
      }

      // Hide remove button
      vcardPhotoRemove.style.display = 'none';

      // Clear input
      const input = $('vcard-photo-input');
      if (input) input.value = '';
    });
  }

  // Generate vCard button
  const generateVcardBtn = $('generate-vcard');
  if (generateVcardBtn) {
    generateVcardBtn.addEventListener('click', async () => {
      const info = {
        prefix: $('vcard-prefix')?.value || '',
        givenName: $('vcard-firstname')?.value || '',
        middleName: $('vcard-middlename')?.value || '',
        familyName: $('vcard-lastname')?.value || '',
        suffix: $('vcard-suffix')?.value || '',
        email: $('vcard-email')?.value || '',
        organization: $('vcard-org')?.value || '',
        title: $('vcard-title')?.value || '',
        includeKeys: $('include-keys')?.checked ?? true,
      };

      // Generate vCard (without photo for QR)
      const vcard = generateVCard(info, { skipPhoto: true });

      // Show result view
      const formView = $('vcard-form-view');
      const resultView = $('vcard-result-view');
      if (formView) formView.style.display = 'none';
      if (resultView) resultView.style.display = 'flex';

      // Generate QR code
      const qrCanvas = $('qr-code');
      if (qrCanvas) {
        await generateQRCode(vcard, qrCanvas);
      }

      // Show preview
      const preview = $('vcard-preview');
      if (preview) preview.textContent = vcard;

      // Store for download (with photo)
      const vcardWithPhoto = generateVCard(info);
      resultView.dataset.vcard = vcardWithPhoto;
      resultView.dataset.name = `${info.givenName || 'contact'}_${info.familyName || 'vcard'}`;
    });
  }

  // Download vCard button
  const downloadVcardBtn = $('download-vcard');
  if (downloadVcardBtn) {
    downloadVcardBtn.addEventListener('click', () => {
      const resultView = $('vcard-result-view');
      const vcard = resultView?.dataset.vcard;
      const name = resultView?.dataset.name || 'contact';

      if (!vcard) return;

      const blob = new Blob([vcard], { type: 'text/vcard;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${name}.vcf`;
      a.click();
      URL.revokeObjectURL(url);
    });
  }

  // Copy vCard button
  const copyVcardBtn = $('copy-vcard');
  if (copyVcardBtn) {
    copyVcardBtn.addEventListener('click', () => {
      const preview = $('vcard-preview');
      if (preview) {
        navigator.clipboard.writeText(preview.textContent);
        showToast('Copied to clipboard!');
      }
    });
  }

  // Back to vCard editor
  const vcardBackBtn = $('vcard-back-btn');
  if (vcardBackBtn) {
    vcardBackBtn.addEventListener('click', () => {
      const formView = $('vcard-form-view');
      const resultView = $('vcard-result-view');
      if (formView) formView.style.display = 'block';
      if (resultView) resultView.style.display = 'none';
    });
  }
}

// =============================================================================
// Initialization
// =============================================================================

async function init() {
  console.log('SDN Crypto Wallet initializing...');

  try {
    // Initialize event listeners
    initEventListeners();

    // Check for stored wallet
    const stored = hasStoredWallet();
    if (stored) {
      console.log('Found stored wallet from', stored.date);
    }

    state.initialized = true;
    console.log('SDN Crypto Wallet initialized');

  } catch (err) {
    console.error('Initialization failed:', err);
  }
}

// Auto-init when DOM is ready
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}

// =============================================================================
// Exports
// =============================================================================

export {
  state,
  generateSeedPhrase,
  validateSeedPhrase,
  deriveKeysFromPassword,
  deriveKeysFromSeed,
  generateAddresses,
  eciesEncrypt,
  eciesDecrypt,
  generateVCard,
  openLoginModal,
  closeLoginModal,
};
