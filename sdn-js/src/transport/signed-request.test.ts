import { createHash, createPrivateKey, createPublicKey, sign as nodeSign, verify as nodeVerify } from 'node:crypto';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SdnPublishAuthOptions, SdnPublishRequest, SdnPublishSignature } from './signed-request';

// Independent fixture cryptography; production uses native hd-wallet-wasm.
vi.mock('../crypto/index', () => ({
  initHDWallet: vi.fn(async () => true),
  sha256: vi.fn(async (bytes: Uint8Array) => new Uint8Array(createHash('sha256').update(bytes).digest())),
  verify: vi.fn(async (publicKey: Uint8Array, message: Uint8Array, signature: Uint8Array) => nodeVerify(null, message,
    createPublicKey({ key: Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), publicKey]), format: 'der', type: 'spki' }), signature)),
}));

import { SdnPublishAuth } from './signed-request';
import { SDNClient } from '../client';
import * as crypto from '../crypto/index';

const origin = 'https://provider.example';
const fixtureKey = createPrivateKey({
  key: Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), Buffer.alloc(32, 7)]),
  format: 'der', type: 'pkcs8',
});
const publicKeyHex = createPublicKey(fixtureKey).export({ format: 'der', type: 'spki' }).subarray(-32).toString('hex');
const keyId = `sha256:${createHash('sha256').update(Buffer.from(publicKeyHex, 'hex')).digest('hex')}`;
const challenge = Buffer.alloc(32, 34);
const body = new Uint8Array([8, 0, 0, 0, 36, 67, 90, 77]);

function signRequest(request: SdnPublishRequest): SdnPublishSignature {
  const canonical = `SDN-SIGNED-REQUEST/v2\n${request.providerOrigin}\nPOST\n${request.requestUri}\n${request.bodySha256}`;
  const digest = createHash('sha256').update(canonical).digest();
  return {
    schemaVersion: 1, keyId, identityScheme: 'sdn-bip32-slip10-purpose-v1',
    algorithm: 'ed25519', encoding: 'raw', signatureProfile: 'ed25519-sdn-signed-request-v2',
    publicKeyHex, signatureHex: nodeSign(null, Buffer.concat([challenge, digest]), fixtureKey).toString('hex'),
    requestDigestSha256: digest.toString('hex'), challengeId: '11'.repeat(16),
    challengeBase64url: challenge.toString('base64url'),
  };
}

function options(extra: Partial<SdnPublishAuthOptions> = {}): SdnPublishAuthOptions {
  return {
    providerOrigin: origin, publicKeyHex, keyId, entityId: 'source101:entity-1',
    entityName: 'User satellite', documentCount: 1,
    sign: async (request) => signRequest(request), ...extra,
  };
}

function context(uri = '/api/v1/data/publish/CZM.fbs', bytes = body) {
  return { url: origin + uri, method: 'POST', body: bytes };
}

afterEach(() => vi.unstubAllGlobals());

describe('SdnPublishAuth', () => {
  it('uses the registered wallet result for the exact public-client request, without a cookie or SDK challenge fetch', async () => {
    let observed!: SdnPublishRequest;
    const auth = new SdnPublishAuth(options({ sign: async (request) => {
      observed = request;
      expect(Object.isFrozen(request)).toBe(true);
      return signRequest(request);
    } }));
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{"cid":"fixture-cid"}'));
    vi.stubGlobal('fetch', fetch);
    await SDNClient.fromUrl(origin, { authProvider: auth }).publish('CZM.fbs', body);
    expect(Object.keys(observed).sort()).toEqual([
      'protocolVersion', 'providerOrigin', 'method', 'requestUri', 'bodySha256', 'bodyBytes',
      'schema', 'entityId', 'entityName', 'documentCount',
    ].sort());
    expect(observed).toMatchObject({ providerOrigin: origin, schema: 'CZM', bodyBytes: body.length });
    expect(observed.bodySha256).toBe(createHash('sha256').update(body).digest('hex'));
    expect(fetch).toHaveBeenCalledTimes(1);
    const [url, init] = fetch.mock.calls[0];
    expect(url).toBe(context().url);
    expect(init?.credentials).toBe('omit');
    expect(init?.redirect).toBe('error');
    expect((init?.headers as Record<string, string>).Authorization).toBe(
      `SDN-Signed-v2 challenge="${'11'.repeat(16)}", signature="${signRequest(observed).signatureHex}"`);
    expect(new Uint8Array(init?.body as ArrayBuffer)).toEqual(body);
    expect(auth.isAuthenticated()).toBe(false);
  });

  it('publishes the frozen bytes even if the caller changes its buffer while the wallet prompt is pending', async () => {
    let resolve!: (result: SdnPublishSignature) => void;
    let request!: SdnPublishRequest;
    let entered!: () => void;
    const started = new Promise<void>((ready) => { entered = ready; });
    const original = body.slice();
    const mutable = body.slice();
    const auth = new SdnPublishAuth(options({ sign: async (value) => {
      request = value; entered(); return new Promise((ready) => { resolve = ready; });
    } }));
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}'));
    vi.stubGlobal('fetch', fetch);
    const publishing = SDNClient.fromUrl(origin, { authProvider: auth }).publish('CZM.fbs', mutable);
    await started; mutable.fill(255); resolve(signRequest(request));
    await publishing;
    expect(request.bodySha256).toBe(createHash('sha256').update(original).digest('hex'));
    expect(new Uint8Array(fetch.mock.calls[0][1]?.body as ArrayBuffer)).toEqual(original);
  });

  it('verifies the native C++ wallet frozen vector through the actual SDK WASM crypto', async () => {
    // Public subset of hd-wallet-wasm/test/fixtures/sdn-publish-request-v1.json.
    // Full fixture SHA256 63af415d26b0f120984a1e0a5476954ec201e7348344edbb9a6fe71da49bf647.
    // It certifies signing bytes only; this readable body is not an SDS binary publication fixture.
    const native = await vi.importActual<typeof crypto>('../crypto/index');
    expect(await native.initHDWallet()).toBe(true);
    vi.mocked(crypto.initHDWallet).mockImplementationOnce(native.initHDWallet);
    vi.mocked(crypto.sha256).mockImplementationOnce(native.sha256).mockImplementationOnce(native.sha256);
    vi.mocked(crypto.verify).mockImplementationOnce(native.verify);
    const bytes = new TextEncoder().encode('[{"id":"document","version":"1.0"},{"id":"fixture:ground:1","name":"Fixture ground site"}]');
    const providerOrigin = 'https://provider.example:8443';
    const auth = new SdnPublishAuth({
      providerOrigin,
      publicKeyHex: 'f5b8e91319472049d552f37d58f528eecefd68cfc4c462c6fcff279c76afb319',
      keyId: 'sha256:d997ad2bf7dbf21c490695eba54d3054628d7f7fb9037fb8145ea32b4e384b7c',
      entityId: 'fixture:ground:1', entityName: 'Fixture ground site', documentCount: 2,
      sign: async (request) => {
        expect(request.bodyBytes).toBe(90);
        expect(request.bodySha256).toBe('0b3bad14ab6a66d51ab7749884c72b8e1a9a185b16dfde9fbf8f366ed5c934af');
        return {
          schemaVersion: 1, keyId: 'sha256:d997ad2bf7dbf21c490695eba54d3054628d7f7fb9037fb8145ea32b4e384b7c',
          identityScheme: 'sdn-bip32-slip10-purpose-v1', algorithm: 'ed25519', encoding: 'raw',
          signatureProfile: 'ed25519-sdn-signed-request-v2',
          publicKeyHex: 'f5b8e91319472049d552f37d58f528eecefd68cfc4c462c6fcff279c76afb319',
          signatureHex: 'a11e2bdf1915d0b8878aada79490d55f2ec76b854ade4b897a98819b6058450c8f1005550d09b44aee3e8d627102c16da0c3a6e38e841fb4948335571362200e',
          requestDigestSha256: '72d09e2e8e1984929db68b3bc9fe2340649914aa3f97ded55467fd70fd6716ef',
          challengeId: '00112233445566778899aabbccddeeff',
          challengeBase64url: 'AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8',
        };
      },
    });
    await expect(auth.authorizeRequest({ url: providerOrigin + '/api/v1/data/publish/CZM', method: 'POST', body: bytes }))
      .resolves.toHaveProperty('Authorization');
  });

  it.each(['CZM', 'CZM.fbs', 'ETM', 'ETM.fbs'])('supports single and batch %s routes', async (schema) => {
    const auth = new SdnPublishAuth(options());
    await expect(auth.authorizeRequest(context(`/api/v1/data/publish/${schema}`))).resolves.toHaveProperty('Authorization');
    await expect(auth.authorizeRequest(context(`/api/v1/data/publish/batch/${schema}`))).resolves.toHaveProperty('Authorization');
  });

  it.each([
    { url: 'https://other.example/api/v1/data/publish/CZM' },
    { method: 'DELETE' },
    { url: `${origin}/api/v1/data/publish/OMM` },
    { url: `${origin}/api/v1/data/publish/CZM?x=1` },
    { url: `${origin}/api/v1/data/publish/CZM#fragment` },
    { url: `${origin}/api/v1/data/publish/x/../CZM` },
    { url: `${origin}/api/v1/data/publish/%43ZM` },
    { body: new Uint8Array() },
    { body: new Uint8Array(1_048_577) },
  ])('refuses unsupported target/method/body before prompting', async (change) => {
    const sign = vi.fn(async (request: SdnPublishRequest) => signRequest(request));
    await expect(new SdnPublishAuth(options({ sign })).authorizeRequest({ ...context(), ...change })).rejects.toThrow();
    expect(sign).not.toHaveBeenCalled();
  });

  it.each([
    { publicKeyHex: 'ff'.repeat(32) }, { keyId: 'sha256:' + 'aa'.repeat(32) },
    { requestDigestSha256: '00'.repeat(32) }, { signatureHex: '00'.repeat(64) },
    { signatureProfile: 'ed25519-raw-32-v1' }, { signatureProfile: 'ed25519-sdn-signed-request-v1' },
    { identityScheme: 'sdn-fast-password-auth-v1-legacy' },
    { challengeId: 'invalid' }, { challengeBase64url: challenge.toString('base64') },
    { challengeId: { toString: () => '11'.repeat(16) } },
    { signatureHex: { toString: () => '00'.repeat(64) } },
    { unexpected: 'not allowed' },
  ])('refuses mismatched wallet results before publication: %j', async (change) => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch);
    const auth = new SdnPublishAuth(options({ sign: async (request) => ({ ...signRequest(request), ...change }) as SdnPublishSignature }));
    await expect(SDNClient.fromUrl(origin, { authProvider: auth }).publish('CZM', body)).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('refuses a valid signature copied from another body', async () => {
    let previous!: SdnPublishSignature;
    const auth = new SdnPublishAuth(options({ sign: async (request) => previous ??= signRequest(request) }));
    await auth.authorizeRequest(context());
    await expect(auth.authorizeRequest(context('/api/v1/data/publish/CZM.fbs', new Uint8Array([9])))).rejects.toThrow(/exact request/);
  });

  it('refuses an otherwise valid signature made for another provider with the same body and nonce', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch);
    const auth = new SdnPublishAuth(options({ sign: async (request) =>
      signRequest({ ...request, providerOrigin: 'https://other-provider.example' }) }));
    await expect(SDNClient.fromUrl(origin, { authProvider: auth }).publish('CZM.fbs', body))
      .rejects.toThrow(/exact request/);
    expect(fetch).not.toHaveBeenCalled();
  });

  it('stops a pending wallet result after the dialog is cancelled', async () => {
    let resolve!: (result: SdnPublishSignature) => void;
    let request!: SdnPublishRequest;
    let entered!: () => void;
    const started = new Promise<void>((ready) => { entered = ready; });
    const auth = new SdnPublishAuth(options({ sign: async (value) => {
      request = value; entered(); return new Promise((ready) => { resolve = ready; });
    } }));
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch);
    const publishing = SDNClient.fromUrl(origin, { authProvider: auth }).publish('CZM.fbs', body);
    await started; auth.destroy(); resolve(signRequest(request));
    await expect(publishing).rejects.toThrow(/cancelled or replaced/);
    expect(fetch).not.toHaveBeenCalled();
  });

  it('surfaces actual provider refusal without retrying or replacing authority', async () => {
    const fetch = vi.fn(async () => new Response('permission denied', { status: 403 }));
    vi.stubGlobal('fetch', fetch);
    const auth = new SdnPublishAuth(options());
    await expect(SDNClient.fromUrl(origin, { authProvider: auth }).publish('CZM', body)).rejects.toMatchObject({ status: 403 });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each(['http://provider.example', 'https://user@provider.example', 'https://provider.example/',
    'https://provider.example/path', 'https://provider.example?x=1', 'https://provider.example:443', 'https://provider.example:0',
    'https://0177.0.0.1', 'https://[::1]', 'https://bad-.example'])('rejects noncanonical or unsupported provider origin %s', (providerOrigin) => {
    expect(() => new SdnPublishAuth(options({ providerOrigin }))).toThrow(/provider origin/);
  });

  it('allows canonical HTTPS home-provider IPv4 and explicit nondefault port', () => {
    expect(new SdnPublishAuth(options({ providerOrigin: 'https://192.168.1.5:8443' })).providerOrigin)
      .toBe('https://192.168.1.5:8443');
  });

  it.each([{ entityName: 'x'.repeat(257) }, { entityId: '\ud800' }, { entityName: 'x\u0000y' },
    { entityName: 'é'.repeat(129) }, { documentCount: 0 }, { documentCount: 100001 }])('validates confirmation metadata %j', (change) => {
    expect(() => new SdnPublishAuth(options(change))).toThrow();
  });
});
