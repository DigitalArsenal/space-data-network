import {
  createCipheriv,
  createDecipheriv,
  pbkdf2Sync,
  randomBytes,
} from 'node:crypto';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import {
  generateMnemonic,
  identityFromMnemonic,
  initHDWallet,
} from '../crypto/hd-wallet';
import type { DerivedIdentity } from '../crypto/types';

const WALLET_FILE_NAME = 'wallet.json';
const WALLET_SCHEMA_VERSION = 1;
const DEFAULT_KDF_ITERATIONS = 310_000;

export interface WalletPublicInfo {
  name: string;
  xpub: string;
  peerId: string;
  signingPublicKeyHex: string;
  encryptionPublicKeyHex: string;
  account: number;
}

export interface LoadedWallet extends WalletPublicInfo {
  identity: DerivedIdentity;
}

export interface CreateWalletOptions {
  password: string;
  name?: string;
  account?: number;
}

export interface LoadWalletOptions {
  password: string;
}

interface WalletSecretPayload {
  seed_phrase: string;
  account: number;
}

interface WalletFile {
  version: number;
  name: string;
  account: number;
  xpub: string;
  peer_id: string;
  signing_public_key_hex: string;
  encryption_public_key_hex: string;
  kdf: {
    name: 'pbkdf2-sha256';
    iterations: number;
    salt: string;
  };
  cipher: {
    name: 'aes-256-gcm';
    iv: string;
    tag: string;
    ciphertext: string;
  };
  created_at: string;
}

export function resolveCliHome(): string {
  const configured = process.env.SDN_CLI_HOME?.trim();
  if (configured) {
    return path.resolve(configured);
  }
  return path.join(os.homedir(), '.spacedatanetwork', 'sdn-js');
}

export function walletPath(): string {
  return path.join(resolveCliHome(), WALLET_FILE_NAME);
}

export async function createWallet(options: CreateWalletOptions): Promise<LoadedWallet> {
  const password = normalizePassword(options.password);
  const account = options.account ?? 0;
  const name = options.name?.trim() || 'SDN CLI Wallet';
  const targetPath = walletPath();

  await fs.mkdir(resolveCliHome(), { recursive: true, mode: 0o700 });

  try {
    await fs.access(targetPath);
    throw new Error(`wallet already exists at ${targetPath}`);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') {
      throw error;
    }
  }

  await ensureHDWallet();
  const seedPhrase = await generateMnemonic({ wordCount: 24 });
  const identity = await identityFromMnemonic(seedPhrase, '', account);
  const publicInfo = publicInfoFromIdentity(identity, name);
  const walletFile = encryptWalletFile(publicInfo, {
    seed_phrase: seedPhrase,
    account,
  }, password);

  await fs.writeFile(targetPath, `${JSON.stringify(walletFile, null, 2)}\n`, { mode: 0o600 });
  await fs.chmod(resolveCliHome(), 0o700);
  await fs.chmod(targetPath, 0o600);

  return {
    ...publicInfo,
    identity,
  };
}

export async function loadWallet(options: LoadWalletOptions): Promise<LoadedWallet> {
  const password = normalizePassword(options.password);
  let walletFile: WalletFile;
  try {
    walletFile = JSON.parse(await fs.readFile(walletPath(), 'utf8')) as WalletFile;
  } catch (error) {
    throw new Error(`failed to read SDN CLI wallet: ${formatError(error)}`);
  }

  if (walletFile.version !== WALLET_SCHEMA_VERSION) {
    throw new Error(`unsupported wallet version ${walletFile.version}`);
  }

  let payload: WalletSecretPayload;
  try {
    payload = decryptWalletFile(walletFile, password);
  } catch (error) {
    throw new Error(`wallet password could not decrypt local wallet: ${formatError(error)}`);
  }

  await ensureHDWallet();
  const identity = await identityFromMnemonic(payload.seed_phrase, '', payload.account);
  const publicInfo = publicInfoFromIdentity(identity, walletFile.name);

  if (
    publicInfo.xpub !== walletFile.xpub ||
    publicInfo.peerId !== walletFile.peer_id ||
    publicInfo.signingPublicKeyHex !== walletFile.signing_public_key_hex
  ) {
    throw new Error('wallet metadata does not match decrypted identity');
  }

  return {
    ...publicInfo,
    identity,
  };
}

function encryptWalletFile(
  publicInfo: WalletPublicInfo,
  payload: WalletSecretPayload,
  password: string,
): WalletFile {
  const salt = randomBytes(16);
  const iv = randomBytes(12);
  const key = deriveWalletKey(password, salt, DEFAULT_KDF_ITERATIONS);
  const cipher = createCipheriv('aes-256-gcm', key, iv);
  const plaintext = Buffer.from(JSON.stringify(payload), 'utf8');
  const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const tag = cipher.getAuthTag();

  return {
    version: WALLET_SCHEMA_VERSION,
    name: publicInfo.name,
    account: publicInfo.account,
    xpub: publicInfo.xpub,
    peer_id: publicInfo.peerId,
    signing_public_key_hex: publicInfo.signingPublicKeyHex,
    encryption_public_key_hex: publicInfo.encryptionPublicKeyHex,
    kdf: {
      name: 'pbkdf2-sha256',
      iterations: DEFAULT_KDF_ITERATIONS,
      salt: base64Url(salt),
    },
    cipher: {
      name: 'aes-256-gcm',
      iv: base64Url(iv),
      tag: base64Url(tag),
      ciphertext: base64Url(ciphertext),
    },
    created_at: new Date().toISOString(),
  };
}

function decryptWalletFile(walletFile: WalletFile, password: string): WalletSecretPayload {
  if (walletFile.kdf?.name !== 'pbkdf2-sha256') {
    throw new Error('unsupported wallet KDF');
  }
  if (walletFile.cipher?.name !== 'aes-256-gcm') {
    throw new Error('unsupported wallet cipher');
  }

  const salt = fromBase64Url(walletFile.kdf.salt);
  const iv = fromBase64Url(walletFile.cipher.iv);
  const tag = fromBase64Url(walletFile.cipher.tag);
  const ciphertext = fromBase64Url(walletFile.cipher.ciphertext);
  const key = deriveWalletKey(password, salt, walletFile.kdf.iterations);
  const decipher = createDecipheriv('aes-256-gcm', key, iv);
  decipher.setAuthTag(tag);
  const plaintext = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
  const payload = JSON.parse(plaintext.toString('utf8')) as WalletSecretPayload;

  if (!payload.seed_phrase || typeof payload.seed_phrase !== 'string') {
    throw new Error('wallet payload missing seed phrase');
  }
  if (!Number.isInteger(payload.account) || payload.account < 0) {
    throw new Error('wallet payload has invalid account');
  }
  return payload;
}

function publicInfoFromIdentity(identity: DerivedIdentity, name: string): WalletPublicInfo {
  return {
    name,
    account: identity.account,
    xpub: identity.xpub,
    peerId: identity.peerId,
    signingPublicKeyHex: bytesToHex(identity.signingKey.publicKey),
    encryptionPublicKeyHex: bytesToHex(identity.encryptionKey.publicKey),
  };
}

async function ensureHDWallet(): Promise<void> {
  if (!await initHDWallet()) {
    throw new Error('HD wallet WASM unavailable');
  }
}

function deriveWalletKey(password: string, salt: Buffer, iterations: number): Buffer {
  if (!Number.isInteger(iterations) || iterations < 100_000) {
    throw new Error('wallet KDF iterations are too low');
  }
  return pbkdf2Sync(password, salt, iterations, 32, 'sha256');
}

function normalizePassword(password: string): string {
  if (typeof password !== 'string' || password.length < 8) {
    throw new Error('wallet password must be at least 8 characters');
  }
  return password;
}

function base64Url(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64url');
}

function fromBase64Url(value: string): Buffer {
  return Buffer.from(value, 'base64url');
}

function bytesToHex(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('hex');
}

function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
