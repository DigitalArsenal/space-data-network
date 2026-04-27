import fs from 'node:fs/promises';
import path from 'node:path';

import {
  encrypt,
  generateKey,
  sha256,
  sign,
} from '../crypto/hd-wallet';
import type { LoadedWallet } from './wallet';

const MODULE_PACKAGE_VERSION = 1;

export interface PackageModuleOptions {
  wasmPath: string;
  outDir: string;
  moduleId: string;
  version: string;
  allowedDomains?: string[];
  requiredScope?: string;
  contentType?: string;
  cacheControl?: string;
  maxGrantTimeoutMs?: number;
  wallet: LoadedWallet;
}

export interface ModulePackageMetadata {
  id: string;
  version: string;
  required_scope?: string;
  content_type?: string;
  cache_control?: string;
  allowed_domains?: string[];
  max_grant_timeout_ms?: number;
}

export interface ModulePackageFile {
  package_version: number;
  metadata: ModulePackageMetadata;
  encrypted_bundle_path: string;
  content_key_hex: string;
  signature_hex: string;
  signer_public_key_hex: string;
  bundle_sha256: string;
  size_bytes: number;
  created_at: string;
}

export interface PackagedModule extends ModulePackageFile {
  moduleId: string;
  version: string;
  packagePath: string;
  encryptedBundlePath: string;
  encryptedBundleBytes: Uint8Array;
  contentKeyHex: string;
  signatureHex: string;
  bundleSHA256: string;
}

export async function packageModule(options: PackageModuleOptions): Promise<PackagedModule> {
  const moduleId = normalizeRequired(options.moduleId, 'moduleId');
  const version = normalizeRequired(options.version, 'version');
  const wasmBytes = await fs.readFile(path.resolve(options.wasmPath));
  const contentKey = generateKey();
  const encryptedBundleBytes = await encrypt(contentKey, wasmBytes);
  const bundleDigest = await sha256(encryptedBundleBytes);
  const signature = await sign(options.wallet.identity.signingKey.privateKey, bundleDigest);

  const slug = `${moduleId}-${version}`.replaceAll(/[^A-Za-z0-9._-]/g, '_');
  const outDir = path.resolve(options.outDir);
  await fs.mkdir(outDir, { recursive: true, mode: 0o700 });
  const encryptedBundlePath = path.join(outDir, `${slug}.wasm.enc`);
  const packagePath = path.join(outDir, `${slug}.sdn-module.json`);
  const metadata = compactMetadata({
    id: moduleId,
    version,
    required_scope: options.requiredScope,
    content_type: options.contentType,
    cache_control: options.cacheControl,
    allowed_domains: options.allowedDomains,
    max_grant_timeout_ms: options.maxGrantTimeoutMs,
  });
  const packageFile: ModulePackageFile = {
    package_version: MODULE_PACKAGE_VERSION,
    metadata,
    encrypted_bundle_path: path.basename(encryptedBundlePath),
    content_key_hex: bytesToHex(contentKey),
    signature_hex: bytesToHex(signature),
    signer_public_key_hex: options.wallet.signingPublicKeyHex,
    bundle_sha256: bytesToHex(bundleDigest),
    size_bytes: encryptedBundleBytes.length,
    created_at: new Date().toISOString(),
  };

  await fs.writeFile(encryptedBundlePath, encryptedBundleBytes, { mode: 0o600 });
  await fs.writeFile(packagePath, `${JSON.stringify(packageFile, null, 2)}\n`, { mode: 0o600 });
  await fs.chmod(encryptedBundlePath, 0o600);
  await fs.chmod(packagePath, 0o600);

  return {
    ...packageFile,
    moduleId,
    version,
    packagePath,
    encryptedBundlePath,
    encryptedBundleBytes,
    contentKeyHex: packageFile.content_key_hex,
    signatureHex: packageFile.signature_hex,
    bundleSHA256: packageFile.bundle_sha256,
  };
}

export async function readModulePackage(packagePath: string): Promise<{
  packageFile: ModulePackageFile;
  encryptedBundlePath: string;
  encryptedBundleBytes: Uint8Array;
}> {
  const resolvedPackagePath = path.resolve(packagePath);
  const packageFile = JSON.parse(await fs.readFile(resolvedPackagePath, 'utf8')) as ModulePackageFile;
  if (packageFile.package_version !== MODULE_PACKAGE_VERSION) {
    throw new Error(`unsupported SDN module package version ${packageFile.package_version}`);
  }
  const encryptedBundlePath = path.resolve(
    path.dirname(resolvedPackagePath),
    packageFile.encrypted_bundle_path,
  );
  const encryptedBundleBytes = await fs.readFile(encryptedBundlePath);
  return { packageFile, encryptedBundlePath, encryptedBundleBytes };
}

function compactMetadata(metadata: ModulePackageMetadata): ModulePackageMetadata {
  return Object.fromEntries(
    Object.entries(metadata).filter(([, value]) => {
      if (Array.isArray(value)) {
        return value.length > 0;
      }
      return value !== undefined && value !== '';
    }),
  ) as ModulePackageMetadata;
}

function normalizeRequired(value: string, name: string): string {
  const normalized = value.trim();
  if (!normalized) {
    throw new Error(`${name} is required`);
  }
  return normalized;
}

function bytesToHex(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('hex');
}
