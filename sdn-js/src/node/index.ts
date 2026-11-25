/**
 * Node-only persisted identity store for `@spacedatanetwork/sdn-js/node`.
 *
 * Writes the 64-byte HD master seed in an integrity-checked binary envelope:
 *   [8B magic "SDNID01\0"][64B seed][32B SHA-256(magic || seed)] = 104 bytes
 *
 * Security invariants (HERMES ruling, stack #113):
 * - Parent directory is created mode 0o700 (POSIX) or owner-only ACL (Windows).
 * - Key file is written to a temp file in the same directory mode 0o600
 *   (owner-only ACL on Windows), fsynced, then atomically renamed into place.
 * - Seed/key bytes are never printed or logged.
 * - Only ENOENT returns null; any existing unreadable or corrupted file throws
 *   and is left byte-identical on disk.
 */

import { execFile } from 'node:child_process';
import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';

import {
  IDENTITY_SEED_LENGTH,
  loadOrCreateIdentity,
  type IdentityStore,
  type LoadOrCreateIdentityOptions,
} from '../node-identity';

export {
  IDENTITY_SEED_LENGTH,
  loadOrCreateIdentity,
  type IdentityStore,
  type LoadOrCreateIdentityOptions,
};

const execFileAsync = promisify(execFile);

const FILE_MAGIC = Buffer.from('SDNID01\0', 'ascii');
const CHECKSUM_LENGTH = 32;
const ENVELOPE_LENGTH = FILE_MAGIC.length + IDENTITY_SEED_LENGTH + CHECKSUM_LENGTH;

function encodeIdentityEnvelope(seed: Uint8Array): Buffer {
  if (!(seed instanceof Uint8Array) || seed.byteLength !== IDENTITY_SEED_LENGTH) {
    throw new Error(`Identity seed must be ${IDENTITY_SEED_LENGTH} bytes`);
  }
  const body = Buffer.concat([FILE_MAGIC, Buffer.from(seed.buffer, seed.byteOffset, seed.byteLength)]);
  const checksum = createHash('sha256').update(body).digest();
  return Buffer.concat([body, checksum]);
}

function decodeIdentityEnvelope(raw: Buffer, filePath: string): Uint8Array {
  if (raw.byteLength !== ENVELOPE_LENGTH) {
    throw new Error(
      `Corrupted identity file at ${filePath}: unexpected length ${raw.byteLength} (expected ${ENVELOPE_LENGTH}); refusing to replace`,
    );
  }
  const header = raw.subarray(0, FILE_MAGIC.length);
  if (!timingSafeEqual(header, FILE_MAGIC)) {
    throw new Error(
      `Corrupted identity file at ${filePath}: invalid header; refusing to replace`,
    );
  }
  const body = raw.subarray(0, FILE_MAGIC.length + IDENTITY_SEED_LENGTH);
  const storedDigest = raw.subarray(FILE_MAGIC.length + IDENTITY_SEED_LENGTH);
  const expectedDigest = createHash('sha256').update(body).digest();
  if (!timingSafeEqual(storedDigest, expectedDigest)) {
    throw new Error(
      `Corrupted identity file at ${filePath}: checksum mismatch; refusing to replace`,
    );
  }
  return new Uint8Array(
    raw.subarray(FILE_MAGIC.length, FILE_MAGIC.length + IDENTITY_SEED_LENGTH),
  );
}

async function restrictWindowsOwnerAcl(targetPath: string, isDirectory: boolean): Promise<void> {
  const user = process.env.USERNAME || os.userInfo().username;
  if (!user) {
    throw new Error('Cannot determine owning Windows user for identity ACL');
  }
  const domain = process.env.USERDOMAIN;
  const principal = domain ? `${domain}\\${user}` : user;
  const perm = isDirectory ? `${principal}:(OI)(CI)F` : `${principal}:F`;
  await execFileAsync('icacls', [targetPath, '/inheritance:r', '/grant:r', perm], {
    windowsHide: true,
  });
}

async function secureDirectory(dirPath: string): Promise<void> {
  await fs.mkdir(dirPath, { recursive: true, mode: 0o700 });
  if (process.platform === 'win32') {
    await restrictWindowsOwnerAcl(dirPath, true);
  } else {
    await fs.chmod(dirPath, 0o700);
  }
}

async function secureFile(filePath: string): Promise<void> {
  if (process.platform === 'win32') {
    await restrictWindowsOwnerAcl(filePath, false);
  } else {
    await fs.chmod(filePath, 0o600);
  }
}

/**
 * Create a Node.js filesystem-backed `IdentityStore` at `filePath`.
 */
export function fileIdentityStore(filePath: string): IdentityStore {
  if (typeof filePath !== 'string' || filePath.trim().length === 0) {
    throw new TypeError('fileIdentityStore requires a non-empty file path');
  }
  const resolvedPath = path.resolve(filePath);

  return {
    async load(): Promise<Uint8Array | null> {
      let raw: Buffer;
      try {
        raw = await fs.readFile(resolvedPath);
      } catch (err) {
        const code = (err as NodeJS.ErrnoException | null)?.code;
        if (code === 'ENOENT') {
          return null;
        }
        throw new Error(
          `Failed to read identity file at ${resolvedPath} (${code ?? 'error'}); refusing to replace`,
        );
      }
      return decodeIdentityEnvelope(raw, resolvedPath);
    },

    async save(seed: Uint8Array): Promise<void> {
      const envelope = encodeIdentityEnvelope(seed);
      const dirPath = path.dirname(resolvedPath);
      await secureDirectory(dirPath);

      const tmpName = `.${path.basename(resolvedPath)}.tmp-${process.pid}-${randomBytes(8).toString('hex')}`;
      const tmpPath = path.join(dirPath, tmpName);
      let handle: fs.FileHandle | null = null;
      try {
        handle = await fs.open(tmpPath, 'wx', 0o600);
        await handle.writeFile(envelope);
        await handle.sync();
        await handle.close();
        handle = null;

        await secureFile(tmpPath);
        await fs.rename(tmpPath, resolvedPath);
        await secureFile(resolvedPath);
      } catch (err) {
        if (handle) {
          await handle.close().catch(() => {});
        }
        await fs.unlink(tmpPath).catch(() => {});
        throw err;
      }
    },
  };
}
