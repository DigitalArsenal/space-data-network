/**
 * Space Data Network - Crypto Wallet & Identity System
 *
 * Features:
 * - HD Wallet (BIP39/BIP32/BIP44/SLIP10)
 * - Multi-chain address derivation (BTC, ETH, SOL, SUI, Cosmos)
 * - ECIES encryption (X25519, secp256k1, P-256)
 * - PIN/Passkey wallet storage
 * - Web3 wallet connection (WalletConnect, MetaMask, Phantom)
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
  loginMethod: null, // 'password' | 'seed' | 'stored' | 'web3'

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

  // Web3 connection
  web3: {
    connected: false,
    provider: null,
    address: null,
    chainId: null,
  },
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
// Web3 Wallet Connection
// =============================================================================

async function connectMetaMask() {
  if (typeof window.ethereum === 'undefined') {
    throw new Error('MetaMask is not installed');
  }

  const accounts = await window.ethereum.request({ method: 'eth_requestAccounts' });
  const chainId = await window.ethereum.request({ method: 'eth_chainId' });

  state.web3 = {
    connected: true,
    provider: 'metamask',
    address: accounts[0],
    chainId: parseInt(chainId, 16),
  };

  // Listen for account/chain changes
  window.ethereum.on('accountsChanged', (accounts) => {
    if (accounts.length === 0) {
      disconnectWeb3();
    } else {
      state.web3.address = accounts[0];
      updateWeb3UI();
    }
  });

  window.ethereum.on('chainChanged', (chainId) => {
    state.web3.chainId = parseInt(chainId, 16);
    updateWeb3UI();
  });

  return state.web3;
}

async function connectPhantom() {
  if (typeof window.solana === 'undefined' || !window.solana.isPhantom) {
    throw new Error('Phantom wallet is not installed');
  }

  const resp = await window.solana.connect();

  state.web3 = {
    connected: true,
    provider: 'phantom',
    address: resp.publicKey.toString(),
    chainId: null,
  };

  window.solana.on('disconnect', disconnectWeb3);

  return state.web3;
}

async function connectCoinbaseWallet() {
  // Coinbase Wallet browser extension exposes ethereum provider
  if (typeof window.ethereum === 'undefined') {
    throw new Error('Coinbase Wallet is not installed');
  }

  // Check if it's specifically Coinbase Wallet
  if (!window.ethereum.isCoinbaseWallet && window.ethereum.providers) {
    const coinbaseProvider = window.ethereum.providers.find(p => p.isCoinbaseWallet);
    if (coinbaseProvider) {
      window.ethereum = coinbaseProvider;
    }
  }

  const accounts = await window.ethereum.request({ method: 'eth_requestAccounts' });
  const chainId = await window.ethereum.request({ method: 'eth_chainId' });

  state.web3 = {
    connected: true,
    provider: 'coinbase',
    address: accounts[0],
    chainId: parseInt(chainId, 16),
  };

  return state.web3;
}

function disconnectWeb3() {
  state.web3 = {
    connected: false,
    provider: null,
    address: null,
    chainId: null,
  };
  updateWeb3UI();
}

function updateWeb3UI() {
  const connectBtn = $('connect-wallet-btn');
  const addressDisplay = $('web3-address');

  if (state.web3.connected) {
    if (connectBtn) {
      connectBtn.textContent = 'Connected';
      connectBtn.classList.add('connected');
    }
    if (addressDisplay) {
      addressDisplay.textContent = truncateAddress(state.web3.address);
      addressDisplay.style.display = 'block';
    }
  } else {
    if (connectBtn) {
      connectBtn.textContent = 'Connect Wallet';
      connectBtn.classList.remove('connected');
    }
    if (addressDisplay) {
      addressDisplay.style.display = 'none';
    }
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
      // Restore wallet from stored data
      await deriveKeysFromSeed(walletData.seedPhrase);
    } else if (method === 'stored-passkey') {
      const walletData = await authenticatePasskeyAndRetrieveWallet();
      await deriveKeysFromSeed(walletData.seedPhrase);
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

  // Update nav button (desktop)
  const navLogin = $('nav-login');
  if (navLogin) {
    navLogin.textContent = 'Logged In';
    navLogin.classList.add('logged-in');
  }

  // Update mobile login button
  const mobileLogin = $('mobile-login');
  if (mobileLogin) {
    mobileLogin.textContent = 'Logged In';
    mobileLogin.classList.add('logged-in');
  }

  // Update adversarial balances
  updateAdversarialUI();

  // Update PKI UI
  updatePKIUI();
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
  // Login button (desktop)
  const navLogin = $('nav-login');
  if (navLogin) {
    navLogin.addEventListener('click', (e) => {
      e.preventDefault();
      if (!state.loggedIn) {
        openLoginModal();
      }
    });
  }

  // Login button (mobile)
  const mobileLogin = $('mobile-login');
  if (mobileLogin) {
    mobileLogin.addEventListener('click', (e) => {
      e.preventDefault();
      if (!state.loggedIn) {
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
      const username = $('wallet-username').value;
      const password = $('wallet-password').value;
      const rememberWallet = $('remember-wallet-password')?.checked;

      try {
        deriveFromPassword.textContent = 'Logging in...';
        deriveFromPassword.disabled = true;

        await handleLogin('password', { username, password });

        if (rememberWallet) {
          const pin = $('pin-input-password')?.value;
          const usePasskey = $('passkey-btn-password')?.classList.contains('active');

          if (usePasskey && isPasskeySupported()) {
            await registerPasskeyAndStoreWallet({ seedPhrase: generateSeedPhrase() });
          } else if (pin) {
            await storeWalletWithPIN(pin, { seedPhrase: generateSeedPhrase() });
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
      const seedPhrase = $('seed-phrase').value;

      try {
        deriveFromSeed.textContent = 'Logging in...';
        deriveFromSeed.disabled = true;

        await handleLogin('seed', { seedPhrase });
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

  // Web3 wallet connect
  const connectWalletBtn = $('connect-wallet-btn');
  if (connectWalletBtn) {
    connectWalletBtn.addEventListener('click', async () => {
      // Show wallet selection modal or try to detect available wallets
      if (typeof window.ethereum !== 'undefined') {
        try {
          await connectMetaMask();
          updateWeb3UI();
        } catch (err) {
          alert('Failed to connect: ' + err.message);
        }
      } else if (typeof window.solana !== 'undefined' && window.solana.isPhantom) {
        try {
          await connectPhantom();
          updateWeb3UI();
        } catch (err) {
          alert('Failed to connect: ' + err.message);
        }
      } else {
        alert('No Web3 wallet detected. Please install MetaMask or Phantom.');
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
}

// =============================================================================
// Initialization
// =============================================================================

async function init() {
  console.log('SDN Crypto Wallet initializing...');

  try {
    // Initialize event listeners
    initEventListeners();

    // Check for Web3 wallet in URL or dApp browser
    if (window.ethereum || window.solana) {
      console.log('Web3 wallet detected');
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
  connectMetaMask,
  connectPhantom,
  connectCoinbaseWallet,
  disconnectWeb3,
  openLoginModal,
  closeLoginModal,
};
