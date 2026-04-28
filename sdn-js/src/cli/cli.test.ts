import { execFile } from 'node:child_process';
import fs from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { promisify } from 'node:util';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  addUploadUser,
  loginToNode,
  readSessionCookie,
} from './auth';
import {
  listModules,
  packageModule,
  uploadModule,
} from './module-upload';
import {
  createDefaultQueryDeliveryNode,
  queryModuleDelivery,
} from './module-query';
import {
  createWallet,
  loadWallet,
  resolveCliHome,
} from './wallet';
import {
  sha256,
  verify,
} from '../crypto/hd-wallet';
import { SDNNode } from '../node';

const execFileAsync = promisify(execFile);

let originalCliHome: string | undefined;
let cliHome: string;

describe('sdn CLI wallet', () => {
  beforeEach(async () => {
    originalCliHome = process.env.SDN_CLI_HOME;
    cliHome = await fs.mkdtemp(path.join(tmpdir(), 'sdn-cli-wallet-'));
    process.env.SDN_CLI_HOME = cliHome;
  });

  afterEach(async () => {
    if (originalCliHome === undefined) {
      delete process.env.SDN_CLI_HOME;
    } else {
      process.env.SDN_CLI_HOME = originalCliHome;
    }
    await fs.rm(cliHome, { recursive: true, force: true });
  });

  it('creates an encrypted wallet in the configured hidden CLI home', async () => {
    const created = await createWallet({
      password: 'correct horse battery staple',
      name: 'SDN Upload Test',
    });

    expect(created.name).toBe('SDN Upload Test');
    expect(created.xpub).toMatch(/^xpub/);
    expect(created.peerId.length).toBeGreaterThan(10);
    expect(created.signingPublicKeyHex).toMatch(/^[0-9a-f]{64}$/);

    const walletPath = path.join(resolveCliHome(), 'wallet.json');
    const raw = JSON.parse(await fs.readFile(walletPath, 'utf8'));
    expect(raw.name).toBe('SDN Upload Test');
    expect(raw.xpub).toBe(created.xpub);
    expect(raw.signing_public_key_hex).toBe(created.signingPublicKeyHex);
    expect(JSON.stringify(raw)).not.toContain('correct horse battery staple');
    expect(JSON.stringify(raw)).not.toContain('mnemonic');

    const dirMode = (await fs.stat(resolveCliHome())).mode & 0o777;
    const fileMode = (await fs.stat(walletPath)).mode & 0o777;
    expect(dirMode).toBe(0o700);
    expect(fileMode).toBe(0o600);
  });

  it('loads an encrypted wallet with the correct password', async () => {
    const created = await createWallet({
      password: 'local upload password',
      name: 'SDN Upload Test',
    });

    const loaded = await loadWallet({ password: 'local upload password' });

    expect(loaded.name).toBe(created.name);
    expect(loaded.xpub).toBe(created.xpub);
    expect(loaded.peerId).toBe(created.peerId);
    expect(loaded.signingPublicKeyHex).toBe(created.signingPublicKeyHex);
    expect(loaded.identity.signingKey.publicKey).toEqual(created.identity.signingKey.publicKey);
  });

  it('rejects a wrong wallet password', async () => {
    await createWallet({
      password: 'right password',
      name: 'SDN Upload Test',
    });

    await expect(loadWallet({ password: 'wrong password' })).rejects.toThrow(
      /wallet password/i,
    );
  });
});

describe('sdn CLI node auth', () => {
  beforeEach(async () => {
    originalCliHome = process.env.SDN_CLI_HOME;
    cliHome = await fs.mkdtemp(path.join(tmpdir(), 'sdn-cli-auth-'));
    process.env.SDN_CLI_HOME = cliHome;
  });

  afterEach(async () => {
    if (originalCliHome === undefined) {
      delete process.env.SDN_CLI_HOME;
    } else {
      process.env.SDN_CLI_HOME = originalCliHome;
    }
    await fs.rm(cliHome, { recursive: true, force: true });
  });

  it('logs into a node with the wallet challenge flow and stores the session cookie', async () => {
    const wallet = await createWallet({
      password: 'auth password',
      name: 'SDN Upload Test',
    });
    const challengeBytes = new Uint8Array(32).fill(0x42);
    const fetchImpl = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === 'https://sdn.spaceaware.io/api/auth/challenge') {
        const body = JSON.parse(String(init?.body));
        expect(init?.method).toBe('POST');
        expect(body.xpub).toBe(wallet.xpub);
        expect(body.client_pubkey_hex).toBe(wallet.signingPublicKeyHex);
        expect(body.ts).toBe(1_772_000_000);
        return jsonResponse({
          challenge_id: 'challenge-1',
          challenge: Buffer.from(challengeBytes).toString('base64url'),
          expires_at: 1_772_000_060,
        });
      }
      if (url === 'https://sdn.spaceaware.io/api/auth/verify') {
        const body = JSON.parse(String(init?.body));
        expect(init?.method).toBe('POST');
        expect(body.challenge_id).toBe('challenge-1');
        expect(body.xpub).toBe(wallet.xpub);
        expect(body.client_pubkey_hex).toBe(wallet.signingPublicKeyHex);
        expect(body.challenge).toBe(Buffer.from(challengeBytes).toString('base64url'));
        expect(body.signature_hex).toMatch(/^[0-9a-f]{128}$/);
        return jsonResponse(
          {
            user: { name: 'Uploader', trust_level: 'admin' },
            expires_at: 1_772_086_400,
          },
          {
            'set-cookie': 'sdn_wallet_session=session-token; Path=/; HttpOnly; Secure; SameSite=Lax',
          },
        );
      }
      throw new Error(`unexpected URL ${url}`);
    });

    const result = await loginToNode({
      nodeUrl: 'https://sdn.spaceaware.io',
      wallet,
      fetchImpl,
      nowMs: 1_772_000_000_000,
    });

    expect(result.cookie).toBe('sdn_wallet_session=session-token');
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    await expect(readSessionCookie('https://sdn.spaceaware.io')).resolves.toBe(
      'sdn_wallet_session=session-token',
    );
  });

  it('adds the current wallet as an upload-capable node user', async () => {
    const wallet = await createWallet({
      password: 'admin password',
      name: 'SDN Upload Test',
    });
    const fetchImpl = vi.fn(async (input: string | URL, init?: RequestInit) => {
      expect(String(input)).toBe('https://sdn.spaceaware.io/api/auth/users');
      expect(init?.method).toBe('POST');
      expect(init?.headers).toMatchObject({
        'Content-Type': 'application/json',
        Accept: 'application/json',
        Cookie: 'sdn_wallet_session=admin-token',
        'X-Requested-With': 'sdn-cli',
      });
      expect(JSON.parse(String(init?.body))).toEqual({
        xpub: wallet.xpub,
        name: 'SDN Upload Test',
        trust_level: 'admin',
        signing_pubkey_hex: wallet.signingPublicKeyHex,
      });
      return jsonResponse({ status: 'created' }, {}, 201);
    });

    await expect(addUploadUser({
      nodeUrl: 'https://sdn.spaceaware.io',
      sessionCookie: 'sdn_wallet_session=admin-token',
      walletInfo: wallet,
      trustLevel: 'admin',
      fetchImpl,
    })).resolves.toEqual({ status: 'created' });
  });
});

describe('sdn CLI module packaging and upload', () => {
  beforeEach(async () => {
    originalCliHome = process.env.SDN_CLI_HOME;
    cliHome = await fs.mkdtemp(path.join(tmpdir(), 'sdn-cli-module-'));
    process.env.SDN_CLI_HOME = cliHome;
  });

  afterEach(async () => {
    if (originalCliHome === undefined) {
      delete process.env.SDN_CLI_HOME;
    } else {
      process.env.SDN_CLI_HOME = originalCliHome;
    }
    await fs.rm(cliHome, { recursive: true, force: true });
  });

  it('encrypts and signs module package bytes', async () => {
    const wallet = await createWallet({
      password: 'package password',
      name: 'SDN Upload Test',
    });
    const sourceBytes = Uint8Array.from([0, 97, 115, 109, 1, 0, 0, 0]);
    const wasmPath = path.join(cliHome, 'test.wasm');
    const outDir = path.join(cliHome, 'dist');
    await fs.writeFile(wasmPath, sourceBytes);

    const packaged = await packageModule({
      wasmPath,
      outDir,
      moduleId: 'com.spaceaware.test-protocol',
      version: '0.0.1',
      allowedDomains: ['spaceaware.io'],
      requiredScope: 'spaceaware:test',
      wallet,
    });

    expect(packaged.moduleId).toBe('com.spaceaware.test-protocol');
    expect(packaged.version).toBe('0.0.1');
    expect(packaged.contentKeyHex).toMatch(/^[0-9a-f]{64}$/);
    expect(packaged.signatureHex).toMatch(/^[0-9a-f]{128}$/);
    expect(packaged.encryptedBundleBytes).not.toEqual(sourceBytes);
    expect(await fs.readFile(packaged.encryptedBundlePath)).toEqual(
      Buffer.from(packaged.encryptedBundleBytes),
    );

    const digest = await sha256(packaged.encryptedBundleBytes);
    await expect(verify(
      wallet.identity.signingKey.publicKey,
      digest,
      hexToBytes(packaged.signatureHex),
    )).resolves.toBe(true);

    const packageJSON = JSON.parse(await fs.readFile(packaged.packagePath, 'utf8'));
    expect(packageJSON.metadata).toMatchObject({
      id: 'com.spaceaware.test-protocol',
      version: '0.0.1',
      required_scope: 'spaceaware:test',
      allowed_domains: ['spaceaware.io'],
    });
    expect(packageJSON.signer_public_key_hex).toBe(wallet.signingPublicKeyHex);
  });

  it('uploads a packaged encrypted module through the server multipart API', async () => {
    const wallet = await createWallet({
      password: 'upload password',
      name: 'SDN Upload Test',
    });
    const wasmPath = path.join(cliHome, 'test.wasm');
    await fs.writeFile(wasmPath, Uint8Array.from([0, 97, 115, 109, 1, 0, 0, 0]));
    const packaged = await packageModule({
      wasmPath,
      outDir: path.join(cliHome, 'dist'),
      moduleId: 'com.spaceaware.test-protocol',
      version: '0.0.1',
      allowedDomains: ['spaceaware.io'],
      wallet,
    });
    const fetchImpl = vi.fn(async (input: string | URL, init?: RequestInit) => {
      expect(String(input)).toBe('https://sdn.spaceaware.io/api/v1/plugin-modules/upload');
      expect(init?.method).toBe('POST');
      expect(init?.headers).toMatchObject({
        Accept: 'application/json',
        Cookie: 'sdn_wallet_session=session-token',
        'X-Requested-With': 'sdn-cli',
      });
      const form = init?.body as FormData;
      expect(form.get('metadata')).toContain('com.spaceaware.test-protocol');
      expect(form.get('content_key_hex')).toBe(packaged.contentKeyHex);
      expect(form.get('signature_hex')).toBe(packaged.signatureHex);
      expect(form.get('bundle')).toBeInstanceOf(Blob);
      return jsonResponse({
        id: 'com.spaceaware.test-protocol',
        version: '0.0.1',
        bundle_sha256: packaged.bundleSHA256,
        size_bytes: packaged.encryptedBundleBytes.length,
      });
    });

    await expect(uploadModule({
      nodeUrl: 'https://sdn.spaceaware.io',
      packagePath: packaged.packagePath,
      sessionCookie: 'sdn_wallet_session=session-token',
      fetchImpl,
    })).resolves.toMatchObject({
      id: 'com.spaceaware.test-protocol',
      version: '0.0.1',
    });
  });

  it('lists plugin modules with the stored node session', async () => {
    const fetchImpl = vi.fn(async (input: string | URL, init?: RequestInit) => {
      expect(String(input)).toBe('https://sdn.spaceaware.io/api/v1/plugin-modules');
      expect(init?.method).toBe('GET');
      expect(init?.headers).toMatchObject({
        Accept: 'application/json',
        Cookie: 'sdn_wallet_session=session-token',
        'X-Requested-With': 'sdn-cli',
      });
      return jsonResponse({
        count: 1,
        modules: [{ id: 'com.spaceaware.test-protocol', version: '0.0.1', status: 'stopped' }],
      });
    });

    await expect(listModules({
      nodeUrl: 'https://sdn.spaceaware.io',
      sessionCookie: 'sdn_wallet_session=session-token',
      fetchImpl,
    })).resolves.toEqual({
      count: 1,
      modules: [{ id: 'com.spaceaware.test-protocol', version: '0.0.1', status: 'stopped' }],
    });
  });

  it('queries encrypted module delivery through an SDNNode-compatible protocol client', async () => {
    const wallet = await createWallet({
      password: 'query password',
      name: 'SDN Upload Test',
    });
    const requestEncryptedModuleBundle = vi.fn(async (request: {
      serverDescriptor: { publicKey: string };
      moduleId: string;
      requesterDomain: string;
    }) => {
      expect(request.serverDescriptor.publicKey).toBe('02'.padEnd(66, '0'));
      expect(request.moduleId).toBe('com.spaceaware.test-protocol');
      expect(request.requesterDomain).toBe('spaceaware.io');
      return {
        provider: { peerId: 'provider-peer' },
        grant: {
          bundleDescriptor: {
            cid: 'bafyencryptedmodule',
            moduleId: 'com.spaceaware.test-protocol',
            moduleVersion: '0.0.1',
          },
          wrappedContentKey: { test: true },
        },
        grantResponseBytes: new Uint8Array([1, 2, 3]),
        encryptedBundleBytes: new Uint8Array([4, 5, 6]),
      };
    });
    const stop = vi.fn(async () => undefined);

    await expect(queryModuleDelivery({
      nodeUrl: 'https://sdn.spaceaware.io',
      moduleId: 'com.spaceaware.test-protocol',
      requesterDomain: 'spaceaware.io',
      wallet,
      providerDescriptor: { publicKey: '02'.padEnd(66, '0') },
      nodeFactory: async () => ({ requestEncryptedModuleBundle, stop }),
      unwrapContentKey: async () => new Uint8Array(32).fill(0x22),
      decryptBundle: async () => new Uint8Array([0, 97, 115, 109]),
    })).resolves.toEqual({
      protocol_id: '/space-data-network/module-delivery/1.0.0',
      provider_peer_id: 'provider-peer',
      module_id: 'com.spaceaware.test-protocol',
      module_version: '0.0.1',
      cid: 'bafyencryptedmodule',
      encrypted_size_bytes: 3,
      decrypted_size_bytes: 4,
    });

    expect(requestEncryptedModuleBundle).toHaveBeenCalledTimes(1);
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it('configures default query transport fetches from the node origin', async () => {
    const wallet = await createWallet({
      password: 'query transport password',
      name: 'SDN Upload Test',
    });
    const fakeNode = {
      requestEncryptedModuleBundle: vi.fn(),
      stop: vi.fn(),
    } as unknown as SDNNode;
    const createSpy = vi.spyOn(SDNNode, 'create').mockResolvedValue(fakeNode);
    try {
      await expect(createDefaultQueryDeliveryNode(
        wallet,
        'https://sdn.spaceaware.io',
      )).resolves.toBe(fakeNode);
      expect(createSpy).toHaveBeenCalledWith({
        identity: wallet.identity,
        enableStorage: false,
        ipfsApiBaseUrl: 'https://sdn.spaceaware.io/api/v0',
        ipfsGatewayBaseUrl: 'https://sdn.spaceaware.io/ipfs',
      });
    } finally {
      createSpy.mockRestore();
    }
  });
});

describe('sdn CLI command surface', () => {
  it('prints usage for nested --help without requiring a wallet password', async () => {
    const cliPath = path.join(process.cwd(), 'dist', 'cli', 'index.mjs');
    const { stdout } = await execFileAsync(process.execPath, [
      cliPath,
      'module',
      'query',
      '--help',
    ]);

    expect(stdout).toContain('sdn wallet init');
    expect(stdout).toContain('sdn module query');
  });
});

function jsonResponse(
  body: unknown,
  headers: Record<string, string> = {},
  status = 200,
): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: String(status),
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: {
      get(name: string): string | null {
        return headers[name.toLowerCase()] ?? headers[name] ?? null;
      },
    },
  } as Response;
}

function hexToBytes(hex: string): Uint8Array {
  return Uint8Array.from(Buffer.from(hex, 'hex'));
}
