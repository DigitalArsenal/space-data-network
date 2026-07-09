import { buildSignedPnm, PNM_TOPIC } from '../../pnm-publisher';
import { channelDiscoveryTopic, formatChannelId } from '../../channels';
import {
  ed25519PublicKey,
  initHDWallet,
  sha256,
  sign,
  verify,
} from '../../crypto/hd-wallet';

export type SandboxSchema = 'OMM' | 'DPM' | 'PNM' | 'EPM' | 'ENC' | 'PLG' | 'CHN';

export interface SandboxArtifactInput {
  schema: SandboxSchema;
  title: string;
  payload: unknown;
}

export interface SandboxArtifactEvidence {
  schema: SandboxSchema;
  title: string;
  payload: unknown;
  cid: string;
  fileId: string;
  payloadBytes: Uint8Array;
  digest: Uint8Array;
  digestHex: string;
  signature: Uint8Array;
  signatureHex: string;
  publicKey: Uint8Array;
  publicKeyHex: string;
  verified: boolean;
}

export interface SandboxPnmEvidence {
  topic: string;
  bytes: Uint8Array;
  publicKey: Uint8Array;
}

const encoder = new TextEncoder();
const liveUnavailableDetail = 'Live provider fetch is not wired in this docs demo version.';

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function concatBytes(...chunks: Uint8Array[]): Uint8Array {
  const length = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

function sortJsonValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortJsonValue);
  }
  if (value === null || typeof value !== 'object') {
    return value;
  }
  if (typeof (value as { toJSON?: unknown }).toJSON === 'function') {
    return value;
  }

  const sorted: Record<string, unknown> = {};
  for (const key of Object.keys(value as Record<string, unknown>).sort()) {
    sorted[key] = sortJsonValue((value as Record<string, unknown>)[key]);
  }
  return sorted;
}

function canonicalArtifactBytes(input: SandboxArtifactInput): Uint8Array {
  return encoder.encode(
    JSON.stringify(
      sortJsonValue({
        schema: input.schema,
        title: input.title,
        payload: input.payload,
        sandbox: true,
      }),
    ),
  );
}

async function deterministicPrivateKey(schema: SandboxSchema): Promise<Uint8Array> {
  return sha256(encoder.encode(`SDN-ECOSYSTEM-DEMO\0${schema}\0private-key`));
}

function fileIdForSchema(schema: SandboxSchema): string {
  if (!/^[A-Z]{3}$/.test(schema)) {
    throw new Error(`invalid sandbox schema ${JSON.stringify(schema)}`);
  }
  return `$${schema}`;
}

function artifactSignaturePayload(
  schema: SandboxSchema,
  cid: string,
  payloadBytes: Uint8Array,
): Uint8Array {
  return concatBytes(encoder.encode(`SDN-ECOSYSTEM-DEMO\0${schema}\0${cid}\0`), payloadBytes);
}

export function buildSandboxChannelEvidence(input: {
  sourceId: string;
  standardCode: string;
}): {
  channelId: string;
  topic: string;
  sourceId: string;
  standardCode: string;
} {
  const topic = channelDiscoveryTopic(input.standardCode);

  return {
    channelId: formatChannelId(input),
    topic,
    sourceId: input.sourceId,
    standardCode: input.standardCode.trim(),
  };
}

export async function createSandboxArtifactEvidence(
  input: SandboxArtifactInput,
): Promise<SandboxArtifactEvidence> {
  await initHDWallet();

  const payloadBytes = canonicalArtifactBytes(input);
  const digest = await sha256(payloadBytes);
  const digestHex = bytesToHex(digest);
  const cid = `bafyecosystem${digestHex.slice(0, 24)}`;
  const privateKey = await deterministicPrivateKey(input.schema);
  const publicKey = await ed25519PublicKey(privateKey);
  const signaturePayload = artifactSignaturePayload(input.schema, cid, payloadBytes);
  const signature = await sign(privateKey, signaturePayload);
  const verified = await verify(publicKey, signaturePayload, signature);

  return {
    schema: input.schema,
    title: input.title,
    payload: input.payload,
    cid,
    fileId: fileIdForSchema(input.schema),
    payloadBytes,
    digest,
    digestHex,
    signature,
    signatureHex: bytesToHex(signature),
    publicKey,
    publicKeyHex: bytesToHex(publicKey),
    verified,
  };
}

export async function buildSandboxPnmEvidence(
  artifact: SandboxArtifactEvidence,
): Promise<SandboxPnmEvidence> {
  await initHDWallet();

  const privateKey = await deterministicPrivateKey('PNM');
  const publicKey = await ed25519PublicKey(privateKey);
  const bytes = await buildSignedPnm({
    cid: artifact.cid,
    fileId: fileIdForSchema(artifact.schema),
    fileName: `${artifact.schema.toLowerCase()}-sandbox.fb`,
    publishedAt: '2026-07-08T00:00:00.000Z',
    signingKey: privateKey,
  });

  return {
    topic: PNM_TOPIC,
    bytes,
    publicKey,
  };
}

export async function requestLiveConnections(): Promise<{
  mode: 'live';
  connections: Array<{ id: string; status: 'unavailable'; detail: string }>;
}> {
  return {
    mode: 'live',
    connections: [
      {
        id: 'sdn.spaceaware.io',
        status: 'unavailable',
        detail: liveUnavailableDetail,
      },
      {
        id: 'celestrak.eth',
        status: 'unavailable',
        detail: liveUnavailableDetail,
      },
    ],
  };
}
