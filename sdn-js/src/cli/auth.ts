import fs from 'node:fs/promises';
import path from 'node:path';

import { sign } from '../crypto/hd-wallet';
import type { LoadedWallet, WalletPublicInfo } from './wallet';
import { resolveCliHome } from './wallet';

const SESSION_FILE_NAME = 'sessions.json';

export interface LoginToNodeOptions {
  nodeUrl: string;
  wallet: LoadedWallet;
  fetchImpl?: typeof fetch;
  nowMs?: number;
}

export interface LoginToNodeResult {
  nodeUrl: string;
  cookie: string;
  expiresAt?: number;
  user?: unknown;
}

export interface AddUploadUserOptions {
  nodeUrl: string;
  sessionCookie: string;
  walletInfo: WalletPublicInfo;
  trustLevel: 'admin' | 'trusted' | 'standard';
  fetchImpl?: typeof fetch;
}

type SessionStore = Record<string, {
  cookie: string;
  updated_at: string;
}>;

export async function loginToNode(options: LoginToNodeOptions): Promise<LoginToNodeResult> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const ts = Math.floor((options.nowMs ?? Date.now()) / 1000);
  const challengeResponse = await postJSON(fetchImpl, `${nodeOrigin}/api/auth/challenge`, {
    xpub: options.wallet.xpub,
    client_pubkey_hex: options.wallet.signingPublicKeyHex,
    ts,
  });

  const challengeID = requireString(challengeResponse.challenge_id, 'challenge_id');
  const challenge = requireString(challengeResponse.challenge, 'challenge');
  const signature = await sign(
    options.wallet.identity.signingKey.privateKey,
    decodeLooseBase64(challenge),
  );
  const verifyResponse = await postJSON(fetchImpl, `${nodeOrigin}/api/auth/verify`, {
    challenge_id: challengeID,
    xpub: options.wallet.xpub,
    client_pubkey_hex: options.wallet.signingPublicKeyHex,
    challenge,
    signature_hex: bytesToHex(signature),
  });

  const cookie = extractSessionCookie(verifyResponse.response);
  if (!cookie) {
    throw new Error('node auth verify response did not include sdn_wallet_session cookie');
  }
  await writeSessionCookie(nodeOrigin, cookie);

  return {
    nodeUrl: nodeOrigin,
    cookie,
    expiresAt: typeof verifyResponse.expires_at === 'number' ? verifyResponse.expires_at : undefined,
    user: verifyResponse.user,
  };
}

export async function addUploadUser(options: AddUploadUserOptions): Promise<unknown> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const body = {
    xpub: options.walletInfo.xpub,
    name: options.walletInfo.name,
    trust_level: options.trustLevel,
    signing_pubkey_hex: options.walletInfo.signingPublicKeyHex,
  };
  const headers = authJSONHeaders(options.sessionCookie);
  const postResponse = await fetchImpl(`${nodeOrigin}/api/auth/users`, {
    method: 'POST',
    headers,
    body: JSON.stringify(body),
  });

  if (postResponse.ok) {
    return postResponse.json();
  }
  if (postResponse.status !== 409) {
    throw new Error(`add upload user failed: ${postResponse.status} ${await postResponse.text()}`);
  }

  const putResponse = await fetchImpl(
    `${nodeOrigin}/api/auth/users/${encodeURIComponent(options.walletInfo.xpub)}`,
    {
      method: 'PUT',
      headers,
      body: JSON.stringify(body),
    },
  );
  if (!putResponse.ok) {
    throw new Error(`update upload user failed: ${putResponse.status} ${await putResponse.text()}`);
  }
  return putResponse.json();
}

export async function readSessionCookie(nodeUrl: string): Promise<string | undefined> {
  const sessions = await readSessions();
  return sessions[normalizeNodeOrigin(nodeUrl)]?.cookie;
}

async function writeSessionCookie(nodeUrl: string, cookie: string): Promise<void> {
  const cliHome = resolveCliHome();
  await fs.mkdir(cliHome, { recursive: true, mode: 0o700 });
  const sessions = await readSessions();
  sessions[normalizeNodeOrigin(nodeUrl)] = {
    cookie,
    updated_at: new Date().toISOString(),
  };
  const target = sessionPath();
  await fs.writeFile(target, `${JSON.stringify(sessions, null, 2)}\n`, { mode: 0o600 });
  await fs.chmod(target, 0o600);
}

async function readSessions(): Promise<SessionStore> {
  try {
    return JSON.parse(await fs.readFile(sessionPath(), 'utf8')) as SessionStore;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
      return {};
    }
    throw error;
  }
}

function sessionPath(): string {
  return path.join(resolveCliHome(), SESSION_FILE_NAME);
}

async function postJSON(
  fetchImpl: typeof fetch,
  url: string,
  body: Record<string, unknown>,
): Promise<Record<string, unknown> & { response: Response }> {
  const response = await fetchImpl(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new Error(`node auth request failed: ${response.status} ${await response.text()}`);
  }
  const payload = await response.json() as Record<string, unknown>;
  return { ...payload, response };
}

function authJSONHeaders(sessionCookie: string): Record<string, string> {
  return {
    'Content-Type': 'application/json',
    Accept: 'application/json',
    Cookie: sessionCookie,
    'X-Requested-With': 'sdn-cli',
  };
}

function extractSessionCookie(response: Response): string | undefined {
  const raw = response.headers.get('set-cookie');
  if (!raw) {
    return undefined;
  }
  const first = raw.split(',').find((part) => part.trim().startsWith('sdn_wallet_session='));
  const cookie = first?.split(';', 1)[0]?.trim();
  return cookie || undefined;
}

function normalizeNodeOrigin(nodeUrl: string): string {
  const url = new URL(nodeUrl);
  return url.origin;
}

function requireString(value: unknown, name: string): string {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`node auth response missing ${name}`);
  }
  return value;
}

function decodeLooseBase64(value: string): Uint8Array {
  const normalized = value.replaceAll('-', '+').replaceAll('_', '/');
  return Uint8Array.from(Buffer.from(normalized, 'base64'));
}

function bytesToHex(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('hex');
}
